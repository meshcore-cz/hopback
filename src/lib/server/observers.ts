import { fetchJsonWithRetry } from './http';

export interface ObserverRecord {
	id: string;
	name: string;
	iata?: string | null;
	lastSeen?: string | null;
	firstSeen?: string | null;
	packetCount?: number | null;
	clientVersion?: string | null;
	lat?: number | null;
	lon?: number | null;
	nodeRole?: string | null;
	updatedAt: string;
	source: string;
}

export class ObserverDirectory {
	private observers = new Map<string, ObserverRecord>();

	constructor(
		private readonly observerApiUrls: string[],
		private readonly verbose: boolean
	) {}

	start() {
		void this.refreshAll();
		setInterval(() => void this.refreshAll(), 5 * 60 * 1000).unref();
	}

	async refreshAll() {
		for (const url of this.observerApiUrls) {
			try {
				const payload = await fetchJsonWithRetry(url, { label: 'observers', verbose: this.verbose });
				const observers = normalizeObservers(payload, url);
				for (const observer of observers) {
					this.observers.set(observer.id, observer);
				}
				if (this.verbose)
					console.log(`[observers] loaded ${observers.length} observers from ${url}`);
			} catch (error) {
				console.warn(`[observers] failed to refresh ${url}:`, error);
			}
		}
	}

	count() {
		return this.observers.size;
	}

	/**
	 * Observers that reported a packet within the given window — i.e. currently
	 * live on the mesh rather than every observer ever seen. Defaults to 5 minutes.
	 */
	activeCount(windowMs = 5 * 60 * 1000) {
		const cutoff = Date.now() - windowMs;
		let count = 0;
		for (const observer of this.observers.values()) {
			const lastSeen = observer.lastSeen ? new Date(observer.lastSeen).getTime() : NaN;
			if (Number.isFinite(lastSeen) && lastSeen >= cutoff) count += 1;
		}
		return count;
	}

	list(): ObserverRecord[] {
		return [...this.observers.values()];
	}

	findById(id: string): ObserverRecord | undefined {
		return (
			this.observers.get(id) ||
			[...this.observers.values()].find(
				(observer) => observer.id.toLowerCase() === id.toLowerCase()
			)
		);
	}
}

function normalizeObservers(payload: unknown, source: string): ObserverRecord[] {
	const rows = Array.isArray(payload)
		? payload
		: Array.isArray((payload as { data?: unknown[] })?.data)
			? (payload as { data: unknown[] }).data
			: Array.isArray((payload as { observers?: unknown[] })?.observers)
				? (payload as { observers: unknown[] }).observers
				: [];
	const now = new Date().toISOString();

	return rows
		.map((row) => normalizeObserver(row, source, now))
		.filter((observer): observer is ObserverRecord => Boolean(observer));
}

function normalizeObserver(row: unknown, source: string, now: string): ObserverRecord | null {
	if (!row || typeof row !== 'object') return null;
	const record = row as Record<string, unknown>;
	const id = stringField(record, 'id', 'public_key', 'publicKey', 'observer_id', 'observerId');
	if (!id) return null;

	const name =
		stringField(record, 'name', 'observer_name', 'observerName') || `Observer ${id.slice(0, 8)}`;

	return {
		id,
		name,
		iata: stringField(record, 'iata') ?? null,
		lastSeen:
			stringField(record, 'last_seen', 'lastSeen', 'last_packet_at', 'lastPacketAt') ?? null,
		firstSeen: stringField(record, 'first_seen', 'firstSeen') ?? null,
		packetCount: numberField(record, 'packet_count', 'packetCount', 'packetsLastHour') ?? null,
		clientVersion: stringField(record, 'client_version', 'clientVersion') ?? null,
		lat: numberField(record, 'lat', 'latitude') ?? null,
		lon: numberField(record, 'lon', 'lng', 'longitude') ?? null,
		nodeRole: stringField(record, 'node_role', 'nodeRole', 'role') ?? null,
		updatedAt: now,
		source
	};
}

function stringField(record: Record<string, unknown>, ...keys: string[]) {
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
