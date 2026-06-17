import WebSocket from 'ws';

export interface AnalyzerState {
	url: string;
	state: 'connecting' | 'open' | 'closed' | 'error';
	lastMessageAt?: string;
	lastError?: string;
}

export interface CoreScopePacket {
	source: string;
	rawHex: string;
	hash: string;
	observerId?: string | null;
	observerName?: string | null;
	firstSeen?: string | null;
	rssi?: number | null;
	snr?: number | null;
	path: string[];
	resolvedPath?: string[];
	payloadType?: string | null;
	original: unknown;
}

export class CoreScopeMonitor {
	private sockets = new Map<string, WebSocket>();
	private states = new Map<string, AnalyzerState>();

	constructor(
		private readonly urls: string[],
		private readonly onPacket: (packet: CoreScopePacket) => void,
		private readonly verbose: boolean
	) {}

	start() {
		for (const url of this.urls) this.connect(url);
	}

	status() {
		return this.urls.map((url) => this.states.get(url) || { url, state: 'closed' as const });
	}

	private connect(url: string) {
		this.states.set(url, { url, state: 'connecting' });
		const socket = new WebSocket(url);
		this.sockets.set(url, socket);

		socket.on('open', () => {
			this.states.set(url, { url, state: 'open' });
			if (this.verbose) console.log(`[corescope] connected ${url}`);
		});

		socket.on('message', (message) => {
			this.states.set(url, {
				...(this.states.get(url) || { url, state: 'open' }),
				lastMessageAt: new Date().toISOString()
			});
			const packet = normalizePacket(url, message.toString());
			if (packet) this.onPacket(packet);
		});

		socket.on('error', (error) => {
			this.states.set(url, { url, state: 'error', lastError: error.message });
			console.warn(`[corescope] ${url}:`, error.message);
		});

		socket.on('close', () => {
			this.states.set(url, {
				...(this.states.get(url) || { url, state: 'closed' }),
				state: 'closed'
			});
			this.sockets.delete(url);
			setTimeout(() => this.connect(url), 3_000).unref();
		});
	}
}

function normalizePacket(source: string, text: string): CoreScopePacket | null {
	try {
		const message = JSON.parse(text) as Record<string, unknown>;
		if (message.type !== 'packet') return null;
		const data = message.data as Record<string, unknown> | undefined;
		if (!data) return null;
		const packet = ((data.packet as Record<string, unknown> | undefined) || data) as Record<
			string,
			unknown
		>;
		const rawHex = stringField(packet, 'raw_hex', 'rawHex');
		if (!rawHex) return null;
		const decoded = data.decoded as Record<string, unknown> | undefined;
		const header = decoded?.header as Record<string, unknown> | undefined;

		return {
			source,
			rawHex,
			hash: stringField(packet, 'hash') || rawHex.slice(0, 16),
			observerId: stringField(packet, 'observer_id', 'observerId'),
			observerName: stringField(packet, 'observer_name', 'observerName'),
			firstSeen: stringField(packet, 'first_seen', 'timestamp', 'created_at'),
			rssi: numberField(packet, 'rssi'),
			snr: numberField(packet, 'snr'),
			path: parsePath(packet.path_json) || parsePath(packet.path) || [],
			resolvedPath: parseStringArray(packet.resolved_path),
			payloadType: stringField(header, 'payloadTypeName'),
			original: data
		};
	} catch {
		return null;
	}
}

function parsePath(value: unknown) {
	if (Array.isArray(value)) return value.map(String);
	if (typeof value !== 'string') return null;
	try {
		const parsed = JSON.parse(value) as unknown;
		return Array.isArray(parsed) ? parsed.map(String) : null;
	} catch {
		return null;
	}
}

function parseStringArray(value: unknown) {
	if (Array.isArray(value)) return value.map(String);
	return undefined;
}

function stringField(record: Record<string, unknown> | undefined, ...keys: string[]) {
	if (!record) return undefined;
	for (const key of keys) {
		const value = record[key];
		if (typeof value === 'string' && value.trim()) return value.trim();
	}
	return undefined;
}

function numberField(record: Record<string, unknown>, ...keys: string[]) {
	for (const key of keys) {
		const value = record[key];
		if (typeof value === 'number' && Number.isFinite(value)) return value;
		if (typeof value === 'string' && value.trim() && Number.isFinite(Number(value)))
			return Number(value);
	}
	return null;
}
