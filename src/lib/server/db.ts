import Database from 'better-sqlite3';
import { dirname } from 'node:path';
import { mkdirSync } from 'node:fs';
import type {
	DiagnosticTest,
	Direction,
	EndpointConfig,
	NodeRecord,
	PacketObservation,
	TestStatus
} from '../types';
import { withDerivedStatus } from '../milestones';

export interface CreateTestInput {
	browserId: string;
	userPublicKey: string;
	endpoint: EndpointConfig;
	code: string;
	qrPayload: string;
	expiresAt: string;
}

export interface ObservationInput {
	testId: string;
	direction: Direction;
	source: string;
	packetHash: string;
	observerId?: string | null;
	observerName?: string | null;
	hopCount: number;
	/** Raw per-hop short hashes. */
	path: string[];
	/** Resolution hints: resolved publicKey per hop (or null), aligned to {@link path}. */
	pathKeys: (string | null)[];
	decodedType?: string | null;
	createdAt?: string;
}

interface TestRow {
	id: string;
	browser_id: string;
	user_public_key: string;
	endpoint_id: string;
	endpoint_name: string;
	endpoint_region: string;
	endpoint_public_key: string;
	code: string;
	status: TestStatus;
	qr_payload: string;
	outbound_seen_at: string | null;
	outbound_endpoint_seen_at: string | null;
	outbound_ack_seen_at: string | null;
	reply_broadcast_at: string | null;
	return_seen_at: string | null;
	reply_ack_seen_at: string | null;
	reply_endpoint_ack_at: string | null;
	outbound_hash: string | null;
	outbound_ack_hash: string | null;
	outbound_ack_crc_hex: string | null;
	return_hash: string | null;
	reply_hash: string | null;
	reply_ack_hash: string | null;
	reply_ack_crc_hex: string | null;
	outbound_hex: string | null;
	outbound_ack_hex: string | null;
	return_hex: string | null;
	reply_hex: string | null;
	reply_ack_hex: string | null;
	reply_status: string | null;
	created_at: string;
	updated_at: string;
	expires_at: string;
}

interface ObservationRow {
	id: number;
	test_id: string;
	direction: Direction;
	source: string;
	packet_hash: string;
	observer_id: string | null;
	observer_name: string | null;
	hop_count: number;
	path_json: string;
	path_keys_json: string;
	decoded_type: string | null;
	created_at: string;
}

interface NodeRow {
	public_key: string;
	name: string;
	short_hash: string;
	node_type: number | null;
	lat: number | null;
	lon: number | null;
	updated_at: string;
	source: string;
}

export class HopbackDatabase {
	readonly db: Database.Database;

	constructor(path: string) {
		mkdirSync(dirname(path), { recursive: true });
		this.db = new Database(path);
		this.db.pragma('journal_mode = WAL');
		this.db.pragma('foreign_keys = ON');
		this.migrate();
	}

	createTest(input: CreateTestInput): DiagnosticTest {
		const now = new Date().toISOString();
		const id = input.code;

		this.db
			.prepare(
				`insert into tests (
					id, browser_id, user_public_key, endpoint_id, endpoint_name, endpoint_region,
					endpoint_public_key, code, status, qr_payload, created_at, updated_at, expires_at
				) values (?, ?, ?, ?, ?, ?, ?, ?, 'waiting', ?, ?, ?, ?)`
			)
			.run(
				id,
				input.browserId,
				input.userPublicKey,
				input.endpoint.id,
				input.endpoint.name,
				input.endpoint.region,
				input.endpoint.publicKey,
				input.code,
				input.qrPayload,
				now,
				now,
				input.expiresAt
			);

		return this.getTest(id)!;
	}

	getTest(id: string): DiagnosticTest | null {
		const row = this.db.prepare('select * from tests where id = ?').get(id) as TestRow | undefined;
		if (!row) return null;
		return this.mapTest(row, this.listObservations(id));
	}

	listTestsForBrowser(browserId: string, limit = 30, offset = 0): DiagnosticTest[] {
		const rows = this.db
			.prepare('select * from tests where browser_id = ? order by created_at desc limit ? offset ?')
			.all(browserId, limit, offset) as TestRow[];
		return rows.map((row) => this.mapTest(row, this.listObservations(row.id)));
	}

	getTestMetas(ids: string[]): DiagnosticTest[] {
		if (!ids.length) return [];
		const placeholders = ids.map(() => '?').join(',');
		const rows = this.db
			.prepare(
				`select
					t.id,
					t.browser_id,
					t.user_public_key,
					t.endpoint_id,
					t.endpoint_name,
					t.endpoint_region,
					t.endpoint_public_key,
					t.code,
					t.status,
					t.outbound_seen_at,
					t.outbound_endpoint_seen_at,
					t.outbound_ack_seen_at,
					t.reply_broadcast_at,
					t.return_seen_at,
					t.reply_ack_seen_at,
					t.reply_endpoint_ack_at,
					t.created_at,
					t.updated_at,
					t.expires_at,
					count(o.id) as observation_count
				from tests t
				left join observations o on o.test_id = t.id
				where t.id in (${placeholders})
				group by t.id
				order by t.created_at desc`
			)
			.all(...ids) as Array<TestRow & { observation_count: number }>;
		return rows.map((row) =>
			withDerivedStatus({
				...this.mapTest(row, []),
				observationCount: Number(row.observation_count) || 0
			})
		);
	}

	countTestsForBrowser(browserId: string) {
		return Number(
			(
				this.db
					.prepare('select count(*) as count from tests where browser_id = ?')
					.get(browserId) as { count: number }
			).count
		);
	}

	listActiveTests(): DiagnosticTest[] {
		const now = new Date().toISOString();
		const rows = this.db
			.prepare(
				`select * from tests
				where expires_at > ?
				order by created_at asc`
			)
			.all(now) as TestRow[];
		return rows.map((row) => this.mapTest(row, this.listObservations(row.id)));
	}

	updateTestFacts(
		id: string,
		fields: {
			outboundSeenAt?: string;
			outboundEndpointSeenAt?: string;
			outboundAckSeenAt?: string;
			replyBroadcastAt?: string;
			returnSeenAt?: string;
			replyAckSeenAt?: string;
			replyEndpointAckAt?: string;
			outboundHash?: string | null;
			outboundAckHash?: string | null;
			outboundAckCrcHex?: string | null;
			returnHash?: string | null;
			replyHash?: string | null;
			replyAckHash?: string | null;
			replyAckCrcHex?: string | null;
			outboundHex?: string | null;
			outboundAckHex?: string | null;
			returnHex?: string | null;
			replyHex?: string | null;
			replyAckHex?: string | null;
			replyStatus?: string | null;
		}
	) {
		this.db
			.prepare(
				`update tests
				set outbound_seen_at = coalesce(?, outbound_seen_at),
					outbound_endpoint_seen_at = coalesce(?, outbound_endpoint_seen_at),
					outbound_ack_seen_at = coalesce(?, outbound_ack_seen_at),
					reply_broadcast_at = coalesce(?, reply_broadcast_at),
					return_seen_at = coalesce(?, return_seen_at),
					reply_ack_seen_at = coalesce(?, reply_ack_seen_at),
					reply_endpoint_ack_at = coalesce(?, reply_endpoint_ack_at),
					outbound_hash = coalesce(?, outbound_hash),
					outbound_ack_hash = coalesce(?, outbound_ack_hash),
					outbound_ack_crc_hex = coalesce(?, outbound_ack_crc_hex),
					return_hash = coalesce(?, return_hash),
					reply_hash = coalesce(?, reply_hash),
					reply_ack_hash = coalesce(?, reply_ack_hash),
					reply_ack_crc_hex = coalesce(?, reply_ack_crc_hex),
					outbound_hex = coalesce(?, outbound_hex),
					outbound_ack_hex = coalesce(?, outbound_ack_hex),
					return_hex = coalesce(?, return_hex),
					reply_hex = coalesce(?, reply_hex),
					reply_ack_hex = coalesce(?, reply_ack_hex),
					reply_status = coalesce(?, reply_status),
					updated_at = ?
				where id = ?`
			)
			.run(
				fields.outboundSeenAt ?? null,
				fields.outboundEndpointSeenAt ?? null,
				fields.outboundAckSeenAt ?? null,
				fields.replyBroadcastAt ?? null,
				fields.returnSeenAt ?? null,
				fields.replyAckSeenAt ?? null,
				fields.replyEndpointAckAt ?? null,
				fields.outboundHash ?? null,
				fields.outboundAckHash ?? null,
				fields.outboundAckCrcHex ?? null,
				fields.returnHash ?? null,
				fields.replyHash ?? null,
				fields.replyAckHash ?? null,
				fields.replyAckCrcHex ?? null,
				fields.outboundHex ?? null,
				fields.outboundAckHex ?? null,
				fields.returnHex ?? null,
				fields.replyHex ?? null,
				fields.replyAckHex ?? null,
				fields.replyStatus ?? null,
				new Date().toISOString(),
				id
			);
	}

	addObservation(input: ObservationInput): PacketObservation | null {
		const pathJson = JSON.stringify(input.path);
		const pathKeysJson = JSON.stringify(input.pathKeys);
		const existing = this.db
			.prepare(
				`select id from observations
				where test_id = ?
				and packet_hash = ?
				and direction = ?
				and source = ?
				and coalesce(observer_id, '') = coalesce(?, '')
				and path_json = ?`
			)
			.get(
				input.testId,
				input.packetHash,
				input.direction,
				input.source,
				input.observerId ?? null,
				pathJson
			);
		if (existing) return null;

		const result = this.db
			.prepare(
				`insert into observations (
					test_id, direction, source, packet_hash, observer_id, observer_name,
					hop_count, path_json, path_keys_json, decoded_type, created_at
				) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			)
			.run(
				input.testId,
				input.direction,
				input.source,
				input.packetHash,
				input.observerId ?? null,
				input.observerName ?? null,
				input.hopCount,
				pathJson,
				pathKeysJson,
				input.decodedType ?? null,
				input.createdAt ?? new Date().toISOString()
			);

		return this.getObservation(Number(result.lastInsertRowid));
	}

	getObservation(id: number): PacketObservation | null {
		const row = this.db.prepare('select * from observations where id = ?').get(id) as
			| ObservationRow
			| undefined;
		return row ? this.mapObservation(row) : null;
	}

	listObservations(testId: string): PacketObservation[] {
		const rows = this.db
			.prepare('select * from observations where test_id = ? order by created_at desc')
			.all(testId) as ObservationRow[];
		return rows.map((row) => this.mapObservation(row));
	}

	upsertNodes(nodes: NodeRecord[]) {
		const statement = this.db.prepare(
			`insert into nodes (public_key, name, short_hash, node_type, lat, lon, updated_at, source)
			values (@publicKey, @name, @shortHash, @nodeType, @lat, @lon, @updatedAt, @source)
			on conflict(public_key) do update set
				name = excluded.name,
				short_hash = excluded.short_hash,
				node_type = excluded.node_type,
				lat = excluded.lat,
				lon = excluded.lon,
				updated_at = excluded.updated_at,
				source = excluded.source`
		);
		const run = this.db.transaction((records: NodeRecord[]) => {
			for (const node of records) {
				statement.run({
					publicKey: node.publicKey,
					name: node.name,
					shortHash: node.shortHash,
					nodeType: node.nodeType ?? null,
					lat: node.lat ?? null,
					lon: node.lon ?? null,
					updatedAt: node.updatedAt,
					source: node.source
				});
			}
		});
		run(nodes);
	}

	listNodes(query = '', limit = 200, updatedAfter?: string): NodeRecord[] {
		const search = `%${query.toLowerCase()}%`;
		const recentClause = updatedAfter ? 'and updated_at >= ?' : '';
		const params = updatedAfter
			? [search, search, search, updatedAfter, limit]
			: [search, search, search, limit];
		const rows = this.db
			.prepare(
				`select * from nodes
				where (lower(name) like ? or lower(public_key) like ? or lower(short_hash) like ?)
				${recentClause}
				order by updated_at desc, name asc
				limit ?`
			)
			.all(...params) as NodeRow[];
		return rows.map((row) => this.mapNode(row));
	}

	findNodeByHash(shortHash: string): NodeRecord | null {
		const row = this.db
			.prepare('select * from nodes where lower(short_hash) = lower(?) limit 1')
			.get(shortHash) as NodeRow | undefined;
		return row ? this.mapNode(row) : null;
	}

	findNodeByPublicKey(publicKey: string): NodeRecord | null {
		const row = this.db
			.prepare('select * from nodes where lower(public_key) = lower(?) limit 1')
			.get(publicKey) as NodeRow | undefined;
		return row ? this.mapNode(row) : null;
	}

	countNodes() {
		return Number(
			(this.db.prepare('select count(*) as count from nodes').get() as { count: number }).count
		);
	}

	private migrate() {
		this.db.exec(`
			create table if not exists tests (
				id text primary key,
				browser_id text not null,
				user_public_key text not null,
				endpoint_id text not null,
				endpoint_name text not null,
				endpoint_region text not null,
				endpoint_public_key text not null,
				code text not null,
				status text not null,
				qr_payload text not null,
				outbound_seen_at text,
				outbound_endpoint_seen_at text,
				outbound_ack_seen_at text,
				reply_broadcast_at text,
				return_seen_at text,
				reply_ack_seen_at text,
				reply_endpoint_ack_at text,
				outbound_hash text,
				outbound_ack_hash text,
				outbound_ack_crc_hex text,
				return_hash text,
				reply_hash text,
				reply_ack_hash text,
				reply_ack_crc_hex text,
				outbound_hex text,
				outbound_ack_hex text,
				return_hex text,
				reply_hex text,
				reply_ack_hex text,
				reply_status text,
				created_at text not null,
				updated_at text not null,
				expires_at text not null
			);
		`);

		// Observations and nodes hold only derived/refreshable data, so when an
		// older bloated schema is detected we simply rebuild the lean tables.
		this.dropLegacyTable('observations', 'resolved_path_json');
		this.dropLegacyTable('nodes', 'raw_json');

		this.db.exec(`
			create table if not exists observations (
				id integer primary key autoincrement,
				test_id text not null references tests(id) on delete cascade,
				direction text not null,
				source text not null,
				packet_hash text not null,
				observer_id text,
				observer_name text,
				hop_count integer not null,
				path_json text not null,
				path_keys_json text not null default '[]',
				decoded_type text,
				created_at text not null
			);

			create table if not exists nodes (
				public_key text primary key,
				name text not null,
				short_hash text not null,
				node_type integer,
				lat real,
				lon real,
				updated_at text not null,
				source text not null
			);

			create index if not exists nodes_short_hash_idx on nodes(short_hash);
			create index if not exists nodes_name_idx on nodes(name);
			create index if not exists observations_packet_observer_path_idx
				on observations(test_id, packet_hash, direction, source, observer_id, path_json);
		`);
	}

	/** Drops a table when it still carries a column from the legacy bloated schema. */
	private dropLegacyTable(table: string, legacyColumn: string) {
		const columns = this.db.pragma(`table_info(${table})`) as Array<{ name: string }>;
		if (columns.some((column) => column.name === legacyColumn)) {
			this.db.exec(`drop table ${table}`);
		}
	}

	private mapTest(row: TestRow, observations: PacketObservation[]): DiagnosticTest {
		return withDerivedStatus({
			id: row.id,
			browserId: row.browser_id,
			userPublicKey: row.user_public_key,
			endpointId: row.endpoint_id,
			endpointName: row.endpoint_name,
			endpointRegion: row.endpoint_region,
			endpointPublicKey: row.endpoint_public_key,
			code: row.code,
			status: row.status,
			qrPayload: row.qr_payload,
			outboundSeenAt: row.outbound_seen_at,
			outboundEndpointSeenAt: row.outbound_endpoint_seen_at,
			outboundAckSeenAt: row.outbound_ack_seen_at,
			replyBroadcastAt: row.reply_broadcast_at,
			returnSeenAt: row.return_seen_at,
			replyAckSeenAt: row.reply_ack_seen_at,
			replyEndpointAckAt: row.reply_endpoint_ack_at,
			outboundHash: row.outbound_hash,
			outboundAckHash: row.outbound_ack_hash,
			outboundAckCrcHex: row.outbound_ack_crc_hex,
			returnHash: row.return_hash,
			replyHash: row.reply_hash,
			replyAckHash: row.reply_ack_hash,
			replyAckCrcHex: row.reply_ack_crc_hex,
			outboundHex: row.outbound_hex,
			outboundAckHex: row.outbound_ack_hex,
			returnHex: row.return_hex,
			replyHex: row.reply_hex,
			replyAckHex: row.reply_ack_hex,
			replyStatus: row.reply_status,
			createdAt: row.created_at,
			updatedAt: row.updated_at,
			expiresAt: row.expires_at,
			observations,
			nodes: {}
		});
	}

	/**
	 * Returns the stored observation facts. Node names, coordinates, distance and
	 * the shared node map are resolved later by the runtime (see decorateTest).
	 */
	private mapObservation(row: ObservationRow): PacketObservation {
		return {
			id: row.id,
			direction: row.direction,
			source: row.source,
			packetHash: row.packet_hash,
			observerId: row.observer_id,
			observerName: row.observer_name,
			hopCount: row.hop_count,
			path: JSON.parse(row.path_json) as string[],
			pathKeys: JSON.parse(row.path_keys_json) as (string | null)[],
			decodedType: row.decoded_type,
			createdAt: row.created_at
		};
	}

	private mapNode(row: NodeRow): NodeRecord {
		return {
			publicKey: row.public_key,
			name: row.name,
			shortHash: row.short_hash,
			nodeType: row.node_type,
			lat: row.lat,
			lon: row.lon,
			updatedAt: row.updated_at,
			source: row.source
		};
	}
}
