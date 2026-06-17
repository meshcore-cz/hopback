import type { Server } from 'node:http';
import { randomUUID } from 'node:crypto';
import type { WebSocket } from 'ws';
import { WebSocketServer } from 'ws';
import type { DiagnosticTest, EndpointConfig, RuntimeStatus } from '../types';
import { getConfig, type AppConfig } from './config';
import { CoreScopeMonitor, type CoreScopePacket } from './corescope';
import { HopbackDatabase } from './db';
import {
	buildReplyPacket,
	decodeEnvelope,
	identifyPacket,
	packetCode,
	registerEndpointKeys
} from './mesh';
import { NodeDirectory } from './nodes';
import { ObserverDirectory } from './observers';

interface BrowserClient {
	socket: WebSocket;
	browserId: string;
	testIds: Set<string>;
}

interface AgentClient {
	socket: WebSocket;
	id: string;
	endpointId?: string;
	ipcReady: boolean;
	connectedAt: string;
	lastSeenAt: string;
}

type AgentMessage =
	| { type: 'hello'; id?: string; endpointId?: string; ipcReady?: boolean }
	| {
			type: 'observedPacket';
			rawHex: string;
			timestamp?: string;
			rssi?: number | null;
			snr?: number | null;
	  }
	| { type: 'sendRawResult'; testId?: string; ok: boolean; error?: string };

export class HopbackRuntime {
	readonly config: AppConfig;
	readonly db: HopbackDatabase;
	readonly nodes: NodeDirectory;
	readonly observers: ObserverDirectory;
	private readonly monitor: CoreScopeMonitor;
	private readonly browsers = new Set<BrowserClient>();
	private readonly agents = new Map<string, AgentClient>();
	private readonly pendingReplyPackets = new Map<string, string>();
	private started = false;

	constructor(config = getConfig()) {
		this.config = config;
		this.db = new HopbackDatabase(config.databasePath);
		this.nodes = new NodeDirectory(this.db, config.nodeApiUrls, config.verbose);
		this.observers = new ObserverDirectory(config.observerApiUrls, config.verbose);
		this.monitor = new CoreScopeMonitor(
			config.coreScopeUrls,
			(packet) => void this.handleCoreScopePacket(packet),
			config.verbose
		);
		registerEndpointKeys(config.endpoints, config.privateKey);
	}

	start() {
		if (this.started) return;
		this.started = true;
		this.nodes.start();
		this.observers.start();
		this.monitor.start();
		if (this.config.verbose) console.log('[runtime] Hopback started');
	}

	attach(server: Server) {
		this.start();
		const wss = new WebSocketServer({ noServer: true });

		server.on('upgrade', (request, socket, head) => {
			const url = new URL(request.url || '/', 'http://localhost');
			if (url.pathname !== '/ws' && url.pathname !== '/agent') return;

			if (
				url.pathname === '/agent' &&
				!this.isAgentAuthorized(request.headers.authorization, url.searchParams.get('secret'))
			) {
				socket.write('HTTP/1.1 401 Unauthorized\r\n\r\n');
				socket.destroy();
				return;
			}

			wss.handleUpgrade(request, socket, head, (ws) => {
				if (url.pathname === '/agent') {
					this.addAgent(ws, url);
				} else {
					this.addBrowser(ws, url);
				}
			});
		});
	}

	createCode() {
		return packetCode();
	}

	getEndpoints() {
		return this.config.endpoints;
	}

	status(): RuntimeStatus {
		return {
			analyzers: this.monitor.status(),
			agents: [...this.agents.values()].map((agent) => ({
				id: agent.id,
				endpointId: agent.endpointId,
				ipcReady: agent.ipcReady,
				connectedAt: agent.connectedAt,
				lastSeenAt: agent.lastSeenAt
			})),
			nodes: this.db.countNodes(),
			observers: this.observers.count(),
			activeTests: this.db.listActiveTests().length,
			verbose: this.config.verbose
		};
	}

	decorateTest(test: DiagnosticTest): DiagnosticTest {
		const endpoint = this.endpointForTest(test);
		return {
			...test,
			endpointLocation: endpoint.location
		};
	}

	private async handleCoreScopePacket(packet: CoreScopePacket) {
		const lowerRaw = packet.rawHex.toLowerCase();
		const pendingTestId = this.pendingReplyPackets.get(lowerRaw);
		if (pendingTestId) {
			this.pendingReplyPackets.delete(lowerRaw);
			await this.recordPacket(packet, this.db.getTest(pendingTestId), 'return', 'TXT_MSG');
			return;
		}

		const match = await identifyPacket(packet.rawHex, this.db.listActiveTests());
		if (!match) return;

		if (this.config.verbose) {
			console.log('[packet] matched', {
				test: match.test.id,
				code: match.test.code,
				type: match.type,
				direction: match.direction,
				source: packet.source,
				text: match.text
			});
		}

		await this.recordPacket(packet, match.test, match.direction, match.type, match.text);

		if (match.direction === 'outbound' && this.config.autoReply && !match.test.outboundSeenAt) {
			await this.sendReply(match.test);
		}
	}

	private async recordPacket(
		packet: CoreScopePacket,
		test: DiagnosticTest | null,
		direction: 'outbound' | 'return',
		type?: string | null,
		text?: string | null
	) {
		if (!test) return;
		const envelope = await decodeEnvelope(packet.rawHex);
		const path = packet.path.length ? packet.path : (envelope?.hops ?? []);
		this.db.addObservation({
			testId: test.id,
			direction,
			source: packet.source,
			packetHash: packet.hash,
			observerId: packet.observerId,
			observerName: packet.observerName,
			firstSeen: packet.firstSeen,
			rssi: packet.rssi,
			snr: packet.snr,
			hopCount: path.length || envelope?.hopCount || 0,
			path,
			resolvedPath: this.nodes.resolvePath(path, packet.resolvedPath),
			decodedType: type || packet.payloadType || envelope?.type,
			decodedText: text,
			rawHex: packet.rawHex
		});

		const now = new Date().toISOString();
		const refreshed = this.db.getTest(test.id);
		if (!refreshed) return;

		if (direction === 'return') {
			this.publishTest(
				this.db.updateStatus(test.id, 'completed', {
					returnSeenAt: now,
					replyStatus: 'Return packet observed'
				})
			);
		} else if (refreshed.returnSeenAt) {
			this.publishTest(this.db.updateStatus(test.id, 'completed', { outboundSeenAt: now }));
		} else {
			this.publishTest(this.db.updateStatus(test.id, 'verified', { outboundSeenAt: now }));
		}
	}

	private async sendReply(test: DiagnosticTest) {
		const endpoint = this.endpointForTest(test);
		const packet = await buildReplyPacket(endpoint, test.userPublicKey, test.code);
		if ('error' in packet) {
			this.publishTest(this.db.updateStatus(test.id, 'partial', { replyStatus: packet.error }));
			return;
		}

		this.pendingReplyPackets.set(packet.hex.toLowerCase(), test.id);
		const agent = [...this.agents.values()].find(
			(candidate) => candidate.endpointId === test.endpointId && candidate.ipcReady
		);
		if (!agent) {
			this.publishTest(
				this.db.updateStatus(test.id, 'partial', {
					replyStatus: 'Verified, but no endpoint agent with IPC is connected'
				})
			);
			return;
		}

		agent.socket.send(JSON.stringify({ type: 'sendRaw', testId: test.id, rawHex: packet.hex }));
		this.publishTest(
			this.db.updateStatus(test.id, 'replying', { replyStatus: `Reply queued through ${agent.id}` })
		);
	}

	private endpointForTest(test: DiagnosticTest): EndpointConfig {
		return (
			this.config.endpoints.find((endpoint) => endpoint.id === test.endpointId) || {
				id: test.endpointId,
				name: test.endpointName,
				region: test.endpointRegion,
				publicKey: test.endpointPublicKey,
				privateKey: this.config.privateKey
			}
		);
	}

	private addBrowser(socket: WebSocket, url: URL) {
		const client: BrowserClient = {
			socket,
			browserId: url.searchParams.get('browserId') || 'anonymous',
			testIds: new Set()
		};
		this.browsers.add(client);
		socket.send(JSON.stringify({ type: 'hello', status: this.status() }));

		socket.on('message', (message) => {
			try {
				const payload = JSON.parse(message.toString()) as { type?: string; testId?: string };
				if (payload.type === 'subscribe' && payload.testId) {
					client.testIds.add(payload.testId);
					const test = this.db.getTest(payload.testId);
					if (test)
						socket.send(JSON.stringify({ type: 'testUpdated', test: this.decorateTest(test) }));
				}
			} catch {
				socket.send(JSON.stringify({ type: 'error', message: 'Invalid WebSocket message' }));
			}
		});

		socket.on('close', () => this.browsers.delete(client));
	}

	private addAgent(socket: WebSocket, url: URL) {
		const now = new Date().toISOString();
		const id = url.searchParams.get('id') || randomUUID();
		const agent: AgentClient = {
			socket,
			id,
			endpointId: url.searchParams.get('endpointId') || undefined,
			ipcReady: false,
			connectedAt: now,
			lastSeenAt: now
		};
		this.agents.set(id, agent);
		socket.send(JSON.stringify({ type: 'hello', id, status: this.status() }));
		this.publishStatus();

		socket.on('message', (message) => {
			agent.lastSeenAt = new Date().toISOString();
			void this.handleAgentMessage(agent, message.toString());
		});

		socket.on('close', () => {
			this.agents.delete(id);
			this.publishStatus();
		});
	}

	private async handleAgentMessage(agent: AgentClient, text: string) {
		try {
			const message = JSON.parse(text) as AgentMessage;
			if (message.type === 'hello') {
				agent.endpointId = message.endpointId || agent.endpointId;
				agent.ipcReady = Boolean(message.ipcReady);
				if (message.id && message.id !== agent.id) {
					this.agents.delete(agent.id);
					agent.id = message.id;
					this.agents.set(agent.id, agent);
				}
				this.publishStatus();
				if (agent.ipcReady) void this.retryPendingReplies(agent);
			}

			if (message.type === 'observedPacket') {
				await this.handleCoreScopePacket({
					source: `agent:${agent.id}`,
					rawHex: message.rawHex,
					hash: message.rawHex.slice(0, 16),
					path: [],
					firstSeen: message.timestamp,
					rssi: message.rssi,
					snr: message.snr,
					original: message
				});
			}

			if (message.type === 'sendRawResult' && message.testId) {
				const status = message.ok
					? 'Reply handed to MeshCore IPC'
					: message.error || 'Agent failed to send reply';
				const test = this.db.getTest(message.testId);
				if (test)
					this.publishTest(
						this.db.updateStatus(test.id, message.ok ? 'replying' : 'partial', {
							replyStatus: status
						})
					);
			}
		} catch (error) {
			agent.socket.send(
				JSON.stringify({
					type: 'error',
					message: error instanceof Error ? error.message : 'Invalid agent message'
				})
			);
		}
	}

	private publishTest(test: DiagnosticTest | null) {
		if (!test) return;
		const payload = JSON.stringify({ type: 'testUpdated', test: this.decorateTest(test) });
		for (const client of this.browsers) {
			if (client.browserId === test.browserId || client.testIds.has(test.id)) {
				client.socket.send(payload);
			}
		}
	}

	private async retryPendingReplies(agent: AgentClient) {
		const tests = this.db
			.listActiveTests()
			.filter(
				(test) =>
					test.endpointId === agent.endpointId &&
					Boolean(test.outboundSeenAt) &&
					!test.returnSeenAt &&
					(test.status === 'partial' || test.status === 'verified' || test.status === 'replying')
			);

		for (const test of tests) {
			await this.sendReply(test);
		}
	}

	private publishStatus() {
		const payload = JSON.stringify({ type: 'status', status: this.status() });
		for (const client of this.browsers) client.socket.send(payload);
	}

	private isAgentAuthorized(authorization: string | undefined, querySecret: string | null) {
		if (querySecret && querySecret === this.config.agentSecret) return true;
		return authorization === `Bearer ${this.config.agentSecret}`;
	}
}

export function getRuntime() {
	const globalRuntime = globalThis as typeof globalThis & { __hopbackRuntime?: HopbackRuntime };
	globalRuntime.__hopbackRuntime ??= new HopbackRuntime();
	globalRuntime.__hopbackRuntime.start();
	return globalRuntime.__hopbackRuntime;
}

export function attachHopbackGateway(server: Server) {
	getRuntime().attach(server);
}
