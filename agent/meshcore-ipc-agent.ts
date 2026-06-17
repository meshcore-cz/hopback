import { existsSync, readFileSync } from 'node:fs';
import net from 'node:net';
import WebSocket from 'ws';

const RF_WATCH_REQUEST_ID = 1;
const SEND_TIMEOUT_MS = 10_000;

loadDotEnv();

const backendUrl = mustEnv('HOPBACK_BACKEND_WS');
const secret = mustEnv('HOPBACK_AGENT_SECRET');
const endpointId = mustEnv('HOPBACK_ENDPOINT_ID');
const agentId = envValue('HOPBACK_AGENT_ID') || `agent-${endpointId}`;
const ipcPath = envValue('MESHCORE_IPC_PATH')
	? expandHome(mustEnv('MESHCORE_IPC_PATH'))
	: undefined;
const ipcHost = envValue('MESHCORE_IPC_HOST');
const ipcPort = envValue('MESHCORE_IPC_PORT') ? Number(mustEnv('MESHCORE_IPC_PORT')) : undefined;
const ipcDevice = envValue('MESHCORE_DEVICE');

if (!ipcPath && (!ipcHost || !Number.isInteger(ipcPort))) {
	throw new Error('MESHCORE_IPC_PATH or both MESHCORE_IPC_HOST and MESHCORE_IPC_PORT are required');
}

let backend: WebSocket | null = null;
let ipc: net.Socket | null = null;
let ipcBuffer = '';
let ipcConnectedOnce = false;
let rfSubscribedOnce = false;
let rfWatching = false;
let ipcStartupFailed = false;
let ipcRequestId = RF_WATCH_REQUEST_ID;

connectIpc();

function connectBackend() {
	const url = new URL(backendUrl);
	url.pathname = '/agent';
	url.searchParams.set('secret', secret);
	url.searchParams.set('id', agentId);
	url.searchParams.set('endpointId', endpointId);

	backend = new WebSocket(url);
	backend.on('open', () => {
		console.log(`[agent] connected to ${url.origin}`);
		backend?.send(
			JSON.stringify({ type: 'hello', id: agentId, endpointId, ipcReady: isIpcReady() })
		);
	});

	backend.on('message', (message) => {
		const payload = JSON.parse(message.toString()) as {
			type?: string;
			testId?: string;
			rawHex?: string;
		};
		if (payload.type === 'sendRaw' && payload.rawHex) {
			sendRaw(payload.rawHex, payload.testId);
		}
	});

	backend.on('close', () => {
		console.warn('[agent] backend disconnected');
		setTimeout(connectBackend, 3000).unref();
	});

	backend.on('error', (error) => console.warn('[agent] backend error:', error.message));
}

function connectIpc() {
	ipcBuffer = '';
	rfWatching = false;

	const socket = createIpcSocket();
	ipc = socket;
	socket.setEncoding('utf8');

	socket.on('connect', () => {
		ipcConnectedOnce = true;
		console.log('[agent] meshcore-go IPC connected');

		socket.write(
			JSON.stringify({
				id: RF_WATCH_REQUEST_ID,
				...(ipcDevice ? { device: ipcDevice } : {}),
				method: 'watch_rf'
			}) + '\n'
		);

		if (!backend || backend.readyState === WebSocket.CLOSED) connectBackend();
		sendBackendStatus();
	});

	socket.on('data', (chunk) => {
		ipcBuffer += chunk;
		let newline = ipcBuffer.indexOf('\n');
		while (newline >= 0) {
			const line = ipcBuffer.slice(0, newline).trim();
			ipcBuffer = ipcBuffer.slice(newline + 1);
			if (line) handleIpcLine(line, socket);
			newline = ipcBuffer.indexOf('\n');
		}
	});

	socket.on('close', () => {
		if (ipc === socket) ipc = null;
		rfWatching = false;
		console.warn('[agent] meshcore-go IPC disconnected');
		if (!ipcConnectedOnce) failStartup('meshcore-go IPC closed before connecting');
		sendBackendStatus();
		setTimeout(connectIpc, 3000).unref();
	});

	socket.on('error', (error) => {
		console.warn('[agent] IPC error:', error.message);
		if (!ipcConnectedOnce) failStartup(`cannot connect to meshcore-go IPC: ${error.message}`);
	});
}

function handleIpcLine(line: string, socket: net.Socket) {
	try {
		const message: unknown = JSON.parse(line);
		if (!isRecord(message)) return;

		if (isIpcResponse(message)) {
			if (!message.ok) {
				console.warn('[agent] RF subscription rejected:', message.error || 'unknown error');
				if (!rfSubscribedOnce) failStartup(message.error || 'RF subscription rejected');
				socket.destroy();
				return;
			}

			rfSubscribedOnce = true;
			rfWatching = true;
			console.log('[agent] observing MeshCore RF packets');
			sendBackendStatus();
			return;
		}

		if (!isRfEvent(message)) {
			console.warn('[agent] unexpected IPC message:', message);
			return;
		}

		const rawHex = Buffer.from(message.bytes, 'base64').toString('hex');
		if (backend?.readyState !== WebSocket.OPEN) return;

		backend.send(
			JSON.stringify({
				type: 'observedPacket',
				rawHex,
				timestamp: message.timestamp,
				rssi: message.rssi,
				snr: message.snr
			})
		);
	} catch (error) {
		console.warn('[agent] bad IPC JSON:', error);
	}
}

function sendRaw(rawHex: string, testId?: string) {
	if (!isIpcReady()) {
		backend?.send(
			JSON.stringify({ type: 'sendRawResult', testId, ok: false, error: 'IPC is not connected' })
		);
		return;
	}

	sendMeshPacket(rawHex)
		.then((response) => {
			backend?.send(
				JSON.stringify({
					type: 'sendRawResult',
					testId,
					ok: response.ok,
					error: response.error
				})
			);
		})
		.catch((error: unknown) => {
			backend?.send(
				JSON.stringify({
					type: 'sendRawResult',
					testId,
					ok: false,
					error: error instanceof Error ? error.message : String(error)
				})
			);
		});
}

function isIpcReady() {
	return Boolean(ipc && !ipc.destroyed && ipc.readyState === 'open' && rfWatching);
}

function sendBackendStatus() {
	if (backend?.readyState === WebSocket.OPEN) {
		backend.send(
			JSON.stringify({ type: 'hello', id: agentId, endpointId, ipcReady: isIpcReady() })
		);
	}
}

function sendMeshPacket(rawHex: string) {
	const packet = Buffer.from(rawHex, 'hex').toString('base64');
	return sendIpcRequest('send_mesh_packet', { priority: 0, packet });
}

function sendIpcRequest(method: string, params?: Record<string, unknown>): Promise<IpcResponse> {
	return new Promise((resolve, reject) => {
		const id = nextIpcRequestId();
		let buffer = '';
		let settled = false;
		const socket = createIpcSocket();
		socket.setEncoding('utf8');

		const settle = (callback: () => void) => {
			if (settled) return;
			settled = true;
			clearTimeout(timeout);
			socket.destroy();
			callback();
		};

		const timeout = setTimeout(() => {
			settle(() => reject(new Error(`${method} timed out after ${SEND_TIMEOUT_MS}ms`)));
		}, SEND_TIMEOUT_MS);
		timeout.unref();

		socket.on('connect', () => {
			socket.write(
				JSON.stringify({
					id,
					...(ipcDevice ? { device: ipcDevice } : {}),
					method,
					...(params ? { params } : {})
				}) + '\n'
			);
		});

		socket.on('data', (chunk) => {
			buffer += chunk;
			let newline = buffer.indexOf('\n');
			while (newline >= 0) {
				const line = buffer.slice(0, newline).trim();
				buffer = buffer.slice(newline + 1);
				newline = buffer.indexOf('\n');
				if (!line) continue;

				try {
					const message: unknown = JSON.parse(line);
					if (!isIpcResponse(message)) continue;
					settle(() => resolve(message));
				} catch (error) {
					settle(() => reject(error));
				}
			}
		});

		socket.on('error', (error) => settle(() => reject(error)));
		socket.on('close', () => {
			settle(() => reject(new Error(`${method} IPC socket closed before response`)));
		});
	});
}

function createIpcSocket() {
	return ipcPath
		? net.createConnection(ipcPath)
		: net.createConnection({ host: ipcHost!, port: ipcPort! });
}

interface IpcResponse {
	id: number;
	ok: boolean;
	error?: string;
}

interface RfEvent {
	timestamp: string;
	snr: number | null;
	rssi: number | null;
	bytes: string;
}

function isIpcResponse(value: Record<string, unknown>): value is IpcResponse {
	return typeof value.id === 'number' && typeof value.ok === 'boolean';
}

function isRfEvent(value: Record<string, unknown>): value is RfEvent {
	return (
		typeof value.bytes === 'string' &&
		typeof value.timestamp === 'string' &&
		(typeof value.snr === 'number' || value.snr === null) &&
		(typeof value.rssi === 'number' || value.rssi === null)
	);
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === 'object' && value !== null;
}

function nextIpcRequestId() {
	ipcRequestId += 1;
	return ipcRequestId;
}

function loadDotEnv(path = '.env') {
	if (!existsSync(path)) return;

	for (const line of readFileSync(path, 'utf8').split(/\r?\n/)) {
		const trimmed = line.trim();
		if (!trimmed || trimmed.startsWith('#')) continue;
		const match = /^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/.exec(trimmed);
		if (!match) continue;

		const [, key, rawValue] = match;
		if (process.env[key] !== undefined) continue;
		process.env[key] = unquoteEnvValue(rawValue.trim());
	}
}

function unquoteEnvValue(value: string) {
	if (
		(value.startsWith('"') && value.endsWith('"')) ||
		(value.startsWith("'") && value.endsWith("'"))
	) {
		return value.slice(1, -1);
	}
	return value;
}

function envValue(name: string) {
	return process.env[name]?.trim() || undefined;
}

function mustEnv(name: string) {
	const value = envValue(name);
	if (!value) throw new Error(`${name} is required`);
	return value;
}

function expandHome(path: string) {
	if (path === '~') return process.env.HOME || path;
	if (path.startsWith('~/')) return `${process.env.HOME || '~'}${path.slice(1)}`;
	return path;
}

function failStartup(message: string): never {
	if (ipcStartupFailed) process.exit(1);
	ipcStartupFailed = true;
	console.error(`[agent] startup failed: ${message}`);
	process.exit(1);
}
