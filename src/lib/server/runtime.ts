import type { Server } from 'node:http';
import { randomUUID } from 'node:crypto';
import type { WebSocket } from 'ws';
import { WebSocketServer } from 'ws';
import {
	deriveTestStatus,
	isAckObservation,
	isEndpointObservation,
	packetKind,
	withDerivedStatus
} from '../milestones';
import type {
	DeliveryPathOption,
	DeliveryPathRow,
	DiagnosticTest,
	Direction,
	EndpointConfig,
	NodeRecord,
	NodeRef,
	PacketObservation,
	PathStatistics,
	PropagationMapData,
	RuntimeStatus
} from '../types';
import { getConfig, type AppConfig } from './config';
import { CoreScopeMonitor, type CoreScopePacket } from './corescope';
import { HopbackDatabase, type CreateTestInput, type ObservationInput } from './db';
import {
	buildReplyPackets,
	decodeEnvelope,
	identifyPacket,
	packetCode,
	packetContentHash,
	registerEndpointKeys
} from './mesh';
import { NodeDirectory } from './nodes';
import { ObserverDirectory, type ObserverRecord } from './observers';

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
	| {
			type: 'sendRawResult';
			testId?: string;
			packetRole?: AgentPacketRole;
			rawHex?: string;
			ok: boolean;
			error?: string;
	  };

type TestFactFields = Parameters<HopbackDatabase['updateTestFacts']>[1];
type AgentPacketRole = 'outboundAck' | 'replyMessage';

interface PendingPacketMatch {
	testId: string;
	direction: 'outbound' | 'return';
	decodedType: string;
}

interface PendingAgentSend {
	testId: string;
	role: AgentPacketRole;
	hex: string;
	hash: string | null;
	ackCrcHex?: string;
}

type ActiveTest = {
	test: DiagnosticTest;
	contentHashes: Set<string>;
	observationKeys: Set<string>;
	createdAt: number;
};

const MAX_MAP_DISTANCE_KM = 2000;
const MAX_DISTANCE_SEGMENT_KM = 500;

export class HopbackRuntime {
	readonly config: AppConfig;
	readonly db: HopbackDatabase;
	readonly nodes: NodeDirectory;
	readonly observers: ObserverDirectory;
	private readonly monitor: CoreScopeMonitor;
	private readonly browsers = new Set<BrowserClient>();
	private readonly agents = new Map<string, AgentClient>();
	private readonly pendingPacketsByRaw = new Map<string, PendingPacketMatch>();
	private readonly pendingPacketsByHash = new Map<string, PendingPacketMatch>();
	private readonly activeTests = new Map<string, ActiveTest>();
	private readonly replyBuildsInFlight = new Set<string>();
	private readonly sendsInFlight = new Map<string, PendingAgentSend>();
	private readonly sendOrder = new Map<string, AgentPacketRole[]>();
	private readonly dbWriteQueue: Array<() => void> = [];
	private readonly pendingFactWrites = new Map<string, TestFactFields>();
	private dbWriteScheduled = false;
	private factWriteScheduled = false;
	private nextObservationId = -1;
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
		this.hydrateActiveTests();
		this.nodes.start();
		this.observers.start();
		this.monitor.start();
		this.startActiveTestCleanup();
		if (this.config.verbose) console.log('[runtime] Hopback started');
	}

	private hydrateActiveTests() {
		for (const test of this.db.listActiveTests()) this.registerActiveTest(test);
	}

	private startActiveTestCleanup() {
		setInterval(() => {
			const cutoff = Date.now() - 30 * 60 * 1000;
			for (const [id, active] of this.activeTests) {
				const expiredAt = new Date(active.test.expiresAt).getTime();
				if (expiredAt < cutoff) this.activeTests.delete(id);
			}
		}, 60_000).unref();
	}

	registerActiveTest(test: DiagnosticTest, contentHash?: string) {
		const active = this.activeTests.get(test.id);
		if (active) {
			active.test = this.decorateTest({ ...active.test, ...test });
			if (contentHash) active.contentHashes.add(contentHash);
			for (const observation of active.test.observations) {
				active.observationKeys.add(this.observationKey(observation));
			}
			return active;
		}

		const decorated = this.decorateTest(test);
		this.activeTests.set(test.id, {
			test: decorated,
			contentHashes: new Set(contentHash ? [contentHash] : []),
			observationKeys: new Set(decorated.observations.map((item) => this.observationKey(item))),
			createdAt: Date.now()
		});
		return this.activeTests.get(test.id)!;
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

	createTest(input: CreateTestInput): DiagnosticTest {
		const test = this.decorateTest(this.db.createTest(input));
		this.registerActiveTest(test);
		return test;
	}

	getTest(id: string): DiagnosticTest | null {
		const active = this.activeTests.get(id);
		if (active) {
			active.test = this.decorateTest(active.test);
			return active.test;
		}
		return this.decorateNullableTest(this.db.getTest(id));
	}

	listTestsForBrowser(browserId: string, limit = 30, offset = 0): DiagnosticTest[] {
		const tests = new Map(
			this.db
				.listTestsForBrowser(browserId, limit + offset, 0)
				.map((test) => [test.id, this.decorateTest(test)])
		);
		for (const active of this.activeTests.values()) {
			if (active.test.browserId === browserId)
				tests.set(active.test.id, this.decorateTest(active.test));
		}
		return [...tests.values()]
			.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime())
			.slice(offset, offset + limit);
	}

	getTestMetas(ids: string[]): DiagnosticTest[] {
		if (!ids.length) return [];
		const fromDb = new Map(this.db.getTestMetas(ids).map((test) => [test.id, test]));
		for (const active of this.activeTests.values()) {
			if (ids.includes(active.test.id)) {
				fromDb.set(active.test.id, {
					...this.decorateTest(active.test),
					observationCount: active.test.observations.length
				});
			}
		}
		return [...fromDb.values()].sort(
			(a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()
		);
	}

	countTestsForBrowser(browserId: string) {
		return this.db.countTestsForBrowser(browserId);
	}

	getEndpoints() {
		return this.config.endpoints;
	}

	status(): RuntimeStatus {
		return {
			analyzers: this.monitor.status(),
			endpoints: this.endpointStatuses(),
			agents: [...this.agents.values()].map((agent) => ({
				id: agent.id,
				endpointId: agent.endpointId,
				ipcReady: agent.ipcReady,
				connectedAt: agent.connectedAt,
				lastSeenAt: agent.lastSeenAt
			})),
			nodes: this.db.countNodes(),
			observers: this.observers.count(),
			activeObservers: this.observers.activeCount(),
			activeTests: this.activeTests.size,
			verbose: this.config.verbose
		};
	}

	isEndpointReady(endpointId: string) {
		return this.endpointStatuses().some((endpoint) => endpoint.id === endpointId && endpoint.ready);
	}

	private endpointStatuses(): RuntimeStatus['endpoints'] {
		return this.config.endpoints.map((endpoint) => {
			const agent = [...this.agents.values()].find(
				(candidate) => candidate.endpointId === endpoint.id
			);
			return {
				id: endpoint.id,
				name: endpoint.name,
				ready: Boolean(agent?.ipcReady),
				connected: Boolean(agent),
				agentId: agent?.id || endpoint.agentId,
				ipcReady: Boolean(agent?.ipcReady),
				lastSeenAt: agent?.lastSeenAt
			};
		});
	}

	decorateTest(test: DiagnosticTest): DiagnosticTest {
		const endpoint = this.endpointForTest(test);
		const nodes: Record<string, NodeRef> = {};
		const observations = test.observations.map((observation) =>
			this.resolveObservation(observation, nodes)
		);
		const withEndpoint = withDerivedStatus({
			...test,
			endpointLocation: endpoint.location,
			observations,
			nodes
		});
		return {
			...withEndpoint,
			deliveryPaths: {
				outbound: this.deliveryPathOptions('outbound', withEndpoint),
				return: this.deliveryPathOptions('return', withEndpoint)
			},
			propagationMap: this.propagationMap(withEndpoint),
			pathStatistics: this.pathStatistics(withEndpoint)
		};
	}

	private decorateNullableTest(test: DiagnosticTest | null): DiagnosticTest | null {
		return test ? this.decorateTest(test) : null;
	}

	/**
	 * Resolves an observation's path and observer against the live node directory,
	 * recording any resolved nodes into the shared `nodes` map and returning a lean
	 * observation that only references those nodes by key.
	 */
	private resolveObservation(
		observation: PacketObservation,
		nodes: Record<string, NodeRef>
	): PacketObservation {
		const resolvedPath = this.nodes.resolvePath(observation.path, observation.pathKeys);
		const pathKeys = resolvedPath.map((node) => this.registerNode(node, nodes));

		const observerNode = this.observerNode(observation.observerId, observation.observerName);
		const observerKey = observerNode ? this.registerNode(observerNode, nodes) : null;

		return {
			id: observation.id,
			direction: observation.direction,
			source: observation.source,
			packetHash: observation.packetHash,
			observerId: observation.observerId,
			observerName: observation.observerName,
			observerKey,
			hopCount: observation.hopCount,
			path: observation.path,
			pathKeys,
			distanceKm: this.pathDistanceKm(pathKeys, nodes),
			decodedType: observation.decodedType,
			createdAt: observation.createdAt
		};
	}

	/**
	 * Adds a resolved node to the shared map and returns its key, or null when the
	 * node could not be resolved (so the client falls back to the raw short hash).
	 */
	private registerNode(node: NodeRecord, nodes: Record<string, NodeRef>): string | null {
		if (node.source === 'packet') return null;
		const key = node.publicKey || node.shortHash;
		if (!nodes[key]) nodes[key] = this.toNodeRef(node);
		return key;
	}

	private toNodeRef(node: NodeRecord): NodeRef {
		return {
			name: node.name,
			shortHash: node.shortHash,
			publicKey: node.publicKey,
			lat: node.lat ?? null,
			lon: node.lon ?? null
		};
	}

	private nodeRefFor(
		observation: PacketObservation,
		index: number,
		nodes: Record<string, NodeRef>
	): NodeRef | null {
		const key = observation.pathKeys[index];
		return key ? (nodes[key] ?? null) : null;
	}

	private pathDistanceKm(pathKeys: (string | null)[], nodes: Record<string, NodeRef>) {
		let total = 0;
		let previous: [number, number] | null = null;
		for (const key of pathKeys) {
			const coords = key ? this.refCoords(nodes[key]) : null;
			if (coords && previous) {
				const segment = this.haversineKm(previous, coords);
				if (segment <= MAX_DISTANCE_SEGMENT_KM) total += segment;
			}
			if (coords) previous = coords;
		}
		return total > 0 ? total : null;
	}

	private deliveryPathOptions(direction: Direction, test: DiagnosticTest): DeliveryPathOption[] {
		const observations = test.observations.filter((item) => item.direction === direction);
		if (!observations.length) return [];

		const best = observations.reduce((winner, observation) => {
			const score = this.pathSortScore(observation);
			const winnerScore = this.pathSortScore(winner);
			if (score < winnerScore) return observation;
			if (score === winnerScore && new Date(observation.createdAt).getTime() < new Date(winner.createdAt).getTime()) {
				return observation;
			}
			return winner;
		});

		return [
			{
				key: this.deliveryPathKey(best),
				direction,
				hopCount: best.hopCount,
				observationId: best.id,
				packetHash: best.packetHash,
				createdAt: best.createdAt,
				rows: this.deliveryRows(direction, best, test)
			}
		];
	}

	private deliveryPathKey(observation: PacketObservation) {
		const path = observation.path
			.map((hash, index) => observation.pathKeys[index] || hash)
			.join('>');
		return path || `direct:${observation.hopCount}`;
	}

	private pathSortScore(observation: PacketObservation) {
		return observation.path.length || observation.hopCount || 0;
	}

	private deliveryRows(
		direction: Direction,
		observation: PacketObservation,
		test: DiagnosticTest
	): DeliveryPathRow[] {
		const fromUser = direction === 'outbound';
		const endpointCoords = this.endpointCoords(test);
		const endpointNode = this.endpointNode(test);
		const endpointPublicKey = endpointNode?.publicKey || test.endpointPublicKey;
		const endpointShort = endpointNode
			? this.shortKey(endpointNode.publicKey)
			: this.shortKey(test.endpointPublicKey);
		const rows: DeliveryPathRow[] = [
			{
				key: `${direction}:start`,
				name: fromUser ? 'Your MeshCore app' : test.endpointName,
				meta: fromUser ? 'Sent the message' : 'Sent the reply',
				short: fromUser ? this.shortKey(test.userPublicKey) : endpointShort,
				publicKey: fromUser ? test.userPublicKey : endpointPublicKey,
				lat: fromUser ? null : (endpointNode?.lat ?? endpointCoords?.[0]),
				lon: fromUser ? null : (endpointNode?.lon ?? endpointCoords?.[1]),
				hasCoords:
					!fromUser &&
					(Boolean(endpointNode && this.nodeCoords(endpointNode)) || Boolean(endpointCoords)),
				tone: 'edge'
			}
		];

		for (const [index, hash] of observation.path.entries()) {
			const ref = this.nodeRefFor(observation, index, test.nodes);
			const coords = this.refCoords(ref);
			rows.push({
				key: `${direction}:${index}:${observation.pathKeys[index] || hash}`,
				name: ref?.name || hash,
				meta: `Hop ${index + 1}`,
				short: this.shortKey(ref?.publicKey || ref?.shortHash || hash),
				publicKey: ref?.publicKey ?? null,
				shortHash: ref?.shortHash ?? hash,
				lat: ref?.lat ?? null,
				lon: ref?.lon ?? null,
				hasCoords: Boolean(coords),
				tone: 'hop'
			});
		}

		rows.push({
			key: `${direction}:end`,
			name: fromUser ? test.endpointName : 'Your MeshCore app',
			meta: fromUser
				? test.outboundEndpointSeenAt
					? 'Endpoint received the message'
					: 'Endpoint receipt pending'
				: test.returnSeenAt
					? 'You received the reply'
					: 'User receipt pending',
			short: fromUser ? endpointShort : this.shortKey(test.userPublicKey),
			publicKey: fromUser ? endpointPublicKey : test.userPublicKey,
			lat: fromUser ? (endpointNode?.lat ?? endpointCoords?.[0]) : null,
			lon: fromUser ? (endpointNode?.lon ?? endpointCoords?.[1]) : null,
			hasCoords:
				fromUser &&
				(Boolean(endpointNode && this.nodeCoords(endpointNode)) || Boolean(endpointCoords)),
			tone: 'edge'
		});

		return rows;
	}

	private shortKey(value?: string | null) {
		return (value || '----').slice(0, 4);
	}

	private propagationMap(test: DiagnosticTest): PropagationMapData {
		const endpoint = this.endpointCoords(test);
		const points = new Map<string, PropagationMapData['points'][number]>();
		const segments: PropagationMapData['segments'] = [];

		for (const observation of test.observations) {
			const observerRef = observation.observerKey
				? (test.nodes[observation.observerKey] ?? null)
				: null;
			const observerCoords = this.refCoords(observerRef);
			if (
				observerCoords &&
				(!endpoint || this.haversineKm(observerCoords, endpoint) <= MAX_MAP_DISTANCE_KM)
			) {
				const key = `observer:${observation.observerId || observation.observerName}`;
				points.set(key, {
					key,
					name: observerRef?.name || observation.observerName || 'Observer',
					kind: 'observer',
					publicKey: observerRef?.publicKey || observation.observerId,
					lat: observerCoords[0],
					lon: observerCoords[1]
				});
			}

			let previous: [number, number] | null = null;
			for (const [index, hash] of observation.path.entries()) {
				const ref = this.nodeRefFor(observation, index, test.nodes);
				const coords = this.refCoords(ref);
				if (!coords || (endpoint && this.haversineKm(coords, endpoint) > MAX_MAP_DISTANCE_KM)) {
					previous = null;
					continue;
				}
				const key = `node:${ref?.publicKey || ref?.shortHash || hash}`;
				points.set(key, {
					key,
					name: ref?.name || hash,
					kind: 'node',
					publicKey: ref?.publicKey ?? null,
					lat: coords[0],
					lon: coords[1]
				});
				if (previous) {
					segments.push({
						key: `${observation.id}:${index}`,
						direction: observation.direction,
						kind: packetKind(observation),
						from: previous,
						to: coords
					});
				}
				previous = coords;
			}
		}

		if (endpoint) {
			points.set(`endpoint:${test.endpointPublicKey}`, {
				key: `endpoint:${test.endpointPublicKey}`,
				name: test.endpointName,
				kind: 'endpoint',
				publicKey: test.endpointPublicKey,
				lat: endpoint[0],
				lon: endpoint[1]
			});
		}

		return { points: [...points.values()], segments };
	}

	private pathStatistics(test: DiagnosticTest): PathStatistics {
		const unique = new Map<string, PacketObservation>();
		for (const observation of test.observations) {
			unique.set(this.deliveryPathKey(observation), observation);
		}
		const paths = [...unique.values()];
		const distances = paths
			.map((observation) => observation.distanceKm)
			.filter((distance): distance is number => Number.isFinite(distance));
		const hopCounts = paths.map((observation) => observation.hopCount);
		const longest = paths.reduce<PacketObservation | null>((best, observation) => {
			if (!best) return observation;
			const bestDistance = best.distanceKm ?? -1;
			const distance = observation.distanceKm ?? -1;
			if (distance !== bestDistance) return distance > bestDistance ? observation : best;
			return observation.hopCount > best.hopCount ? observation : best;
		}, null);

		return {
			totalPaths: paths.length,
			outboundPaths: new Set(
				test.observations
					.filter((observation) => observation.direction === 'outbound')
					.map((observation) => this.deliveryPathKey(observation))
			).size,
			returnPaths: new Set(
				test.observations
					.filter((observation) => observation.direction === 'return')
					.map((observation) => this.deliveryPathKey(observation))
			).size,
			longestDistanceKm: longest?.distanceKm ?? null,
			longestDistanceLabel: longest ? this.pathLabel(longest, test.nodes) : null,
			longestHopCount: hopCounts.length ? Math.max(...hopCounts) : 0,
			shortestHopCount: hopCounts.length ? Math.min(...hopCounts) : 0,
			averageDistanceKm: distances.length
				? distances.reduce((sum, distance) => sum + distance, 0) / distances.length
				: null
		};
	}

	private pathLabel(observation: PacketObservation, nodes: Record<string, NodeRef>) {
		const names = observation.path.map(
			(hash, index) => this.nodeRefFor(observation, index, nodes)?.name || hash
		);
		return names.length ? names.join(' -> ') : `${observation.direction} direct`;
	}

	private endpointCoords(test: DiagnosticTest): [number, number] | null {
		const lat = test.endpointLocation?.lat;
		const lon = test.endpointLocation?.lon;
		return Number.isFinite(lat) && Number.isFinite(lon) ? [lat!, lon!] : null;
	}

	private nodeCoords(node: NodeRecord): [number, number] | null {
		return Number.isFinite(node.lat) && Number.isFinite(node.lon) ? [node.lat!, node.lon!] : null;
	}

	private endpointNode(test: DiagnosticTest): NodeRecord | null {
		const endpoint = this.endpointCoords(test);
		if (!endpoint) return null;
		const candidates = this.db
			.listNodes('', 2000)
			.filter((node) => this.nodeCoords(node))
			.map((node) => ({ node, distance: this.haversineKm(this.nodeCoords(node)!, endpoint) }))
			.filter((candidate) => candidate.distance <= 5)
			.sort((a, b) => a.distance - b.distance);
		return candidates[0]?.node || null;
	}

	private observerNode(
		observerId?: string | null,
		observerName?: string | null
	): NodeRecord | null {
		if (!observerId) return null;
		const node = this.nodes.resolvePath([observerId], [observerId])[0];
		if (node.source !== 'packet' || this.nodeCoords(node)) return node;
		const observer = this.observers.findById(observerId) || this.findObserverByName(observerName);
		return observer ? this.observerRecordNode(observer, observerId) : node;
	}

	private findObserverByName(observerName?: string | null): ObserverRecord | undefined {
		if (!observerName) return undefined;
		return this.observers
			.list()
			.find((observer) => observer.name.toLowerCase() === observerName.toLowerCase());
	}

	private observerRecordNode(observer: ObserverRecord, fallbackId: string): NodeRecord {
		return {
			publicKey: observer.id || fallbackId,
			name: observer.name,
			shortHash: (observer.id || fallbackId).slice(0, 8),
			lat: observer.lat,
			lon: observer.lon,
			updatedAt: observer.updatedAt,
			source: observer.source
		};
	}

	private refCoords(ref?: NodeRef | null): [number, number] | null {
		if (!ref) return null;
		return Number.isFinite(ref.lat) && Number.isFinite(ref.lon) ? [ref.lat!, ref.lon!] : null;
	}

	private haversineKm(a: [number, number], b: [number, number]): number {
		const R = 6371;
		const dLat = ((b[0] - a[0]) * Math.PI) / 180;
		const dLon = ((b[1] - a[1]) * Math.PI) / 180;
		const lat1 = (a[0] * Math.PI) / 180;
		const lat2 = (b[0] * Math.PI) / 180;
		const h = Math.sin(dLat / 2) ** 2 + Math.cos(lat1) * Math.cos(lat2) * Math.sin(dLon / 2) ** 2;
		return 2 * R * Math.atan2(Math.sqrt(h), Math.sqrt(1 - h));
	}

	private async handleCoreScopePacket(packet: CoreScopePacket) {
		const lowerRaw = packet.rawHex.toLowerCase();
		const pending =
			this.pendingPacketsByHash.get(packet.hash) || this.pendingPacketsByRaw.get(lowerRaw);
		if (pending) {
			await this.recordPacket(
				packet,
				this.getTest(pending.testId),
				pending.direction,
				pending.decodedType
			);
			return;
		}

		const match = await identifyPacket(
			packet.rawHex,
			[...this.activeTests.values()].map((active) => active.test)
		);
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

		const recorded = await this.recordPacket(
			packet,
			match.test,
			match.direction,
			match.type,
			match.text
		);

		if (
			recorded &&
			match.direction === 'outbound' &&
			this.config.autoReply &&
			packet.source.startsWith('agent:')
		) {
			void this.sendReply(recorded.test);
		}
	}

	private async recordPacket(
		packet: CoreScopePacket,
		test: DiagnosticTest | null,
		direction: 'outbound' | 'return',
		type?: string | null,
		text?: string | null
	) {
		if (!test) return null;
		const active = this.registerActiveTest(test, packet.hash);
		const envelope = await decodeEnvelope(packet.rawHex);
		const path = packet.path.length ? packet.path : (envelope?.hops ?? []);
		// Capture-time resolution hints: the publicKey per hop, used to re-resolve
		// names/coords on every read. No node objects are stored on the observation.
		const resolvedPath = this.nodes.resolvePath(path, packet.resolvedPath);
		const pathKeys = resolvedPath.map((node) => (node.source === 'packet' ? null : node.publicKey));
		const createdAt = new Date().toISOString();
		const observation: PacketObservation = {
			id: this.nextObservationId--,
			direction,
			source: packet.source,
			packetHash: packet.hash,
			observerId: packet.observerId,
			observerName: packet.observerName,
			hopCount: path.length || envelope?.hopCount || 0,
			path,
			pathKeys,
			decodedType: type || packet.payloadType || envelope?.type,
			createdAt
		};

		const key = this.observationKey(observation);
		if (active.observationKeys.has(key)) return null;
		active.observationKeys.add(key);
		active.test.observations = [observation, ...active.test.observations];
		active.test = this.decorateTest(
			this.applyObservationMilestones(active.test, observation, packet.rawHex)
		);

		const input: ObservationInput = {
			testId: test.id,
			direction: observation.direction,
			source: observation.source,
			packetHash: observation.packetHash,
			observerId: observation.observerId,
			observerName: observation.observerName,
			hopCount: observation.hopCount,
			path: observation.path,
			pathKeys: observation.pathKeys,
			decodedType: observation.decodedType,
			createdAt
		};
		this.enqueueDbWrite(() => {
			this.db.addObservation(input);
		});

		this.publishObservation(active.test, observation);
		return { test: active.test, observation };
	}

	private applyObservationMilestones(
		test: DiagnosticTest,
		observation: PacketObservation,
		rawHex: string
	) {
		const facts: TestFactFields = {};
		const isEndpoint = isEndpointObservation(observation, test);
		const isAck = isAckObservation(observation);

		if (observation.direction === 'outbound') {
			if (isAck) {
				if (!test.outboundAckSeenAt) {
					facts.outboundAckSeenAt = observation.createdAt;
					facts.outboundAckHash = observation.packetHash;
					facts.outboundAckHex = rawHex;
					facts.replyStatus = 'Outbound ACK observed';
				}
			} else if (!test.outboundSeenAt) {
				facts.outboundSeenAt = observation.createdAt;
				facts.outboundHash = observation.packetHash;
				facts.outboundHex = rawHex;
			}
			if (!isAck && isEndpoint && !test.outboundEndpointSeenAt) {
				facts.outboundEndpointSeenAt = observation.createdAt;
				facts.outboundHash ??= observation.packetHash;
				facts.outboundHex ??= rawHex;
			}
		}

		if (observation.direction === 'return') {
			if (isAck) {
				if (!test.replyAckSeenAt) {
					facts.replyAckSeenAt = observation.createdAt;
					facts.replyAckHash = observation.packetHash;
					facts.replyAckHex = rawHex;
					facts.replyStatus = 'Reply ACK observed';
				}
				if (isEndpoint && !test.replyEndpointAckAt) {
					facts.replyEndpointAckAt = observation.createdAt;
					facts.replyStatus = 'Reply ACK arrived at endpoint';
				}
			} else if (!test.returnSeenAt) {
				facts.returnSeenAt = observation.createdAt;
				facts.returnHash = observation.packetHash;
				facts.returnHex = rawHex;
				facts.replyStatus = 'Reply packet observed';
			}
		}

		return this.updateTestFactsInMemory(test.id, facts) || test;
	}

	private async sendReply(test: DiagnosticTest) {
		test = this.getTest(test.id) || test;
		if (
			this.replyBuildsInFlight.has(test.id) ||
			this.sendsInFlight.has(this.sendKey(test.id, 'replyMessage')) ||
			this.sendsInFlight.has(this.sendKey(test.id, 'outboundAck')) ||
			(test.replyBroadcastAt && test.outboundAckHash) ||
			test.replyEndpointAckAt
		) {
			return;
		}
		this.replyBuildsInFlight.add(test.id);

		try {
			const endpoint = this.endpointForTest(test);
			if (!test.outboundHex) {
				console.error(`[reply] cannot build ACK for test ${test.id}: missing outbound hex`);
				this.publishTest(
					this.updateTestFactsInMemory(test.id, {
						replyStatus: 'Cannot build reply: outbound packet was not captured'
					})
				);
				return;
			}

			const packetBundle = await buildReplyPackets(
				endpoint,
				test.userPublicKey,
				test.code,
				test.outboundHex
			);
			if ('error' in packetBundle) {
				this.publishTest(
					this.updateTestFactsInMemory(test.id, { replyStatus: packetBundle.error })
				);
				return;
			}

			const agent = [...this.agents.values()].find(
				(candidate) => candidate.endpointId === test.endpointId && candidate.ipcReady
			);
			if (!agent) {
				this.publishTest(
					this.updateTestFactsInMemory(test.id, {
						replyStatus: 'Outbound reached endpoint, but no endpoint agent with IPC is connected'
					})
				);
				return;
			}

			for (const packet of packetBundle.packets) {
				const role: AgentPacketRole =
					packet.type === 'ACK' || packet.type === 'PATH' ? 'outboundAck' : 'replyMessage';
				if (role === 'outboundAck' && test.outboundAckHash) continue;
				if (role === 'replyMessage' && test.replyBroadcastAt) continue;

				const hash = await packetContentHash(packet.hex);
				const pendingMatch: PendingPacketMatch = {
					testId: test.id,
					direction: role === 'outboundAck' ? 'outbound' : 'return',
					decodedType: packet.type
				};
				this.pendingPacketsByRaw.set(packet.hex.toLowerCase(), pendingMatch);
				if (hash) this.pendingPacketsByHash.set(hash, pendingMatch);
				this.sendsInFlight.set(this.sendKey(test.id, role), {
					testId: test.id,
					role,
					hex: packet.hex,
					hash,
					ackCrcHex: packet.ackCrcHex
				});
				this.queueSendRole(test.id, role);

				this.publishTest(
					this.updateTestFactsInMemory(
						test.id,
						role === 'outboundAck'
							? {
									outboundAckHash: hash,
									outboundAckCrcHex: packet.ackCrcHex,
									outboundAckHex: packet.hex,
									replyStatus: `Outbound ACK queued through ${agent.id}`
								}
							: {
									replyHash: hash,
									replyAckCrcHex: packet.ackCrcHex,
									replyHex: packet.hex,
									replyStatus: `Reply queued through ${agent.id}`
								}
					)
				);
				agent.socket.send(
					JSON.stringify({
						type: 'sendRaw',
						testId: test.id,
						packetRole: role,
						rawHex: packet.hex
					})
				);
				break;
			}
		} finally {
			this.replyBuildsInFlight.delete(test.id);
		}
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
					const test = this.getTest(payload.testId);
					if (test) socket.send(JSON.stringify({ type: 'testUpdated', test }));
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
				const hash = await packetContentHash(message.rawHex);
				await this.handleCoreScopePacket({
					source: `agent:${agent.id}`,
					rawHex: message.rawHex,
					hash: hash ?? message.rawHex.slice(0, 16),
					path: [],
					firstSeen: message.timestamp,
					rssi: message.rssi,
					snr: message.snr,
					original: message
				});
			}

			if (message.type === 'sendRawResult' && message.testId) {
				const test = this.getTest(message.testId);
				if (!test) return;
				const role = this.resolveSendResultRole(test.id, message);
				const prepared = this.sendsInFlight.get(this.sendKey(test.id, role));
				this.sendsInFlight.delete(this.sendKey(test.id, role));
				const fields = this.fieldsForSendResult(role, message.ok, prepared, message.error);
				const updated = this.updateTestFactsInMemory(test.id, fields);
				this.publishTest(updated);
				if (updated && message.ok && role === 'outboundAck') void this.sendReply(updated);
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

	private updateTestFactsInMemory(id: string, fields: TestFactFields) {
		if (!Object.values(fields).some((value) => value !== undefined && value !== null)) {
			return this.getTest(id);
		}

		let active = this.activeTests.get(id);
		if (!active) {
			const test = this.db.getTest(id);
			if (!test) return null;
			active = this.registerActiveTest(test);
		}

		active.test = this.decorateTest({
			...active.test,
			...fields,
			updatedAt: new Date().toISOString(),
			status: deriveTestStatus({ ...active.test, ...fields })
		});
		this.enqueueFactWrite(id, fields);
		return active.test;
	}

	private enqueueFactWrite(id: string, fields: TestFactFields) {
		const pending = this.pendingFactWrites.get(id) || {};
		this.pendingFactWrites.set(id, { ...pending, ...fields });
		if (this.factWriteScheduled) return;
		this.factWriteScheduled = true;
		this.enqueueDbWrite(() => {
			this.factWriteScheduled = false;
			const writes = [...this.pendingFactWrites.entries()];
			this.pendingFactWrites.clear();
			for (const [testId, facts] of writes) this.db.updateTestFacts(testId, facts);
		});
	}

	private enqueueDbWrite(write: () => void) {
		this.dbWriteQueue.push(write);
		if (this.dbWriteScheduled) return;
		this.dbWriteScheduled = true;
		setImmediate(() => this.flushDbWrites());
	}

	private flushDbWrites() {
		this.dbWriteScheduled = false;
		const writes = this.dbWriteQueue.splice(0, 250);
		for (const write of writes) {
			try {
				write();
			} catch (error) {
				console.error('[db] background write failed:', error);
			}
		}
		if (this.dbWriteQueue.length) this.enqueueDbWrite(() => {});
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

	private publishObservation(test: DiagnosticTest, observation: PacketObservation) {
		// decorateTest resolves the full node map; pick the freshly resolved twin of
		// this observation so the event references keys present in `test.nodes`.
		const decorated = this.decorateTest(test);
		const resolved =
			decorated.observations.find((item) => item.id === observation.id) ?? observation;
		const payload = JSON.stringify({
			type: 'observation',
			testId: decorated.id,
			test: decorated,
			observation: resolved
		});
		for (const client of this.browsers) {
			if (client.browserId === test.browserId || client.testIds.has(test.id)) {
				client.socket.send(payload);
			}
		}
	}

	private async retryPendingReplies(agent: AgentClient) {
		const tests = [...this.activeTests.values()]
			.map((active) => active.test)
			.filter(
				(test) =>
					test.endpointId === agent.endpointId &&
					Boolean(test.outboundEndpointSeenAt) &&
					(!test.outboundAckHash || !test.replyBroadcastAt) &&
					!this.sendsInFlight.has(this.sendKey(test.id, 'outboundAck')) &&
					!this.sendsInFlight.has(this.sendKey(test.id, 'replyMessage')) &&
					!test.replyEndpointAckAt
			);

		for (const test of tests) await this.sendReply(test);
	}

	private fieldsForSendResult(
		role: AgentPacketRole,
		ok: boolean,
		prepared: PendingAgentSend | undefined,
		error: string | undefined
	): TestFactFields {
		if (!ok) return { replyStatus: error || 'Agent failed to send packet' };
		if (role === 'outboundAck') {
			return {
				outboundAckHash: prepared?.hash,
				outboundAckCrcHex: prepared?.ackCrcHex,
				outboundAckHex: prepared?.hex,
				replyStatus: 'Outbound ACK handed to MeshCore IPC'
			};
		}
		return {
			replyBroadcastAt: new Date().toISOString(),
			replyHash: prepared?.hash,
			replyAckCrcHex: prepared?.ackCrcHex,
			replyHex: prepared?.hex,
			replyStatus: 'Reply handed to MeshCore IPC'
		};
	}

	private sendKey(testId: string, role: AgentPacketRole) {
		return `${testId}:${role}`;
	}

	private queueSendRole(testId: string, role: AgentPacketRole) {
		const queued = this.sendOrder.get(testId) || [];
		queued.push(role);
		this.sendOrder.set(testId, queued);
	}

	private resolveSendResultRole(
		testId: string,
		message: Extract<AgentMessage, { type: 'sendRawResult' }>
	): AgentPacketRole {
		if (message.rawHex) {
			for (const role of ['outboundAck', 'replyMessage'] as const) {
				const pending = this.sendsInFlight.get(this.sendKey(testId, role));
				if (pending?.hex.toLowerCase() === message.rawHex.toLowerCase()) {
					this.removeQueuedSendRole(testId, role);
					return role;
				}
			}
		}

		if (message.packetRole) {
			this.removeQueuedSendRole(testId, message.packetRole);
			return message.packetRole;
		}

		const queued = this.sendOrder.get(testId) || [];
		while (queued.length) {
			const role = queued.shift()!;
			if (this.sendsInFlight.has(this.sendKey(testId, role))) {
				if (queued.length) this.sendOrder.set(testId, queued);
				else this.sendOrder.delete(testId);
				return role;
			}
		}
		this.sendOrder.delete(testId);
		return 'replyMessage';
	}

	private removeQueuedSendRole(testId: string, role: AgentPacketRole) {
		const queued = this.sendOrder.get(testId);
		if (!queued) return;
		const index = queued.indexOf(role);
		if (index >= 0) queued.splice(index, 1);
		if (queued.length) this.sendOrder.set(testId, queued);
		else this.sendOrder.delete(testId);
	}

	private observationKey(
		observation: Pick<
			PacketObservation,
			'packetHash' | 'direction' | 'source' | 'observerId' | 'path'
		>
	) {
		return [
			observation.packetHash,
			observation.direction,
			observation.source,
			observation.observerId || '',
			JSON.stringify(observation.path)
		].join('|');
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
