import type { NodeRecord } from '../types';
import type { HopbackDatabase } from './db';
import { fetchJsonWithRetry } from './http';

export class NodeDirectory {
	constructor(
		private readonly db: HopbackDatabase,
		private readonly nodeApiUrls: string[],
		private readonly verbose: boolean
	) {}

	start() {
		void this.refreshAll();
		setInterval(() => void this.refreshAll(), 5 * 60 * 1000).unref();
	}

	async refreshAll() {
		for (const url of this.nodeApiUrls) {
			try {
				const payload = await fetchJsonWithRetry(url, { label: 'nodes', verbose: this.verbose });
				const nodes = normalizeNodes(payload, url);
				this.db.upsertNodes(nodes);
				if (this.verbose) console.log(`[nodes] loaded ${nodes.length} nodes from ${url}`);
			} catch (error) {
				console.warn(`[nodes] failed to refresh ${url}:`, error);
			}
		}
	}

	resolvePath(path: string[], resolvedPath?: (string | null | undefined)[]): NodeRecord[] {
		return path.map((hash, index) => {
			const publicKey = resolvedPath?.[index] ?? undefined;
			const byKey = publicKey ? this.db.listNodes(publicKey, 1)[0] : undefined;
			const byHash = this.db.findNodeByHash(hash);
			return (
				byKey ||
				byHash || {
					publicKey: publicKey || hash,
					name: publicKey ? `Node ${publicKey.slice(0, 8)}` : `Hop ${hash}`,
					shortHash: hash,
					updatedAt: new Date().toISOString(),
					source: 'packet'
				}
			);
		});
	}
}

function normalizeNodes(payload: unknown, source: string): NodeRecord[] {
	const rows = Array.isArray(payload)
		? payload
		: Array.isArray((payload as { data?: unknown[] })?.data)
			? (payload as { data: unknown[] }).data
			: Array.isArray((payload as { nodes?: unknown[] })?.nodes)
				? (payload as { nodes: unknown[] }).nodes
				: [];
	const now = new Date().toISOString();

	return rows
		.map((row) => normalizeNode(row, source, now))
		.filter((node): node is NodeRecord => Boolean(node));
}

function normalizeNode(row: unknown, source: string, now: string): NodeRecord | null {
	if (!row || typeof row !== 'object') return null;
	const record = row as Record<string, unknown>;
	const publicKey = stringField(record, 'public_key', 'publicKey', 'pub_key', 'id', 'node_id');
	if (!publicKey) return null;

	const name =
		stringField(record, 'name', 'node_name', 'observer_name') || `Node ${publicKey.slice(0, 8)}`;
	const shortHash =
		stringField(record, 'short_hash', 'hash', 'destHash') ||
		(typeof publicKey === 'string' ? publicKey.slice(0, 2).toUpperCase() : '');

	return {
		publicKey,
		name,
		shortHash,
		nodeType: numberField(record, 'node_type', 'nodeType', 'type'),
		lat: numberField(record, 'lat', 'latitude'),
		lon: numberField(record, 'lon', 'lng', 'longitude'),
		updatedAt: stringField(record, 'updated_at', 'last_seen', 'timestamp') || now,
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
