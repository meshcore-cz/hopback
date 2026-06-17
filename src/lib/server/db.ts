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
	firstSeen?: string | null;
	rssi?: number | null;
	snr?: number | null;
	hopCount: number;
	path: string[];
	resolvedPath: NodeRecord[];
	decodedType?: string | null;
	decodedText?: string | null;
	rawHex?: string | null;
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
	return_seen_at: string | null;
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
	first_seen: string | null;
	rssi: number | null;
	snr: number | null;
	hop_count: number;
	path_json: string;
	resolved_path_json: string;
	decoded_type: string | null;
	decoded_text: string | null;
	raw_hex: string | null;
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
	raw_json: string | null;
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

	listTestsForBrowser(browserId: string, limit = 30): DiagnosticTest[] {
		const rows = this.db
			.prepare('select * from tests where browser_id = ? order by created_at desc limit ?')
			.all(browserId, limit) as TestRow[];
		return rows.map((row) => this.mapTest(row, this.listObservations(row.id)));
	}

	listActiveTests(): DiagnosticTest[] {
		const now = new Date().toISOString();
		const rows = this.db
			.prepare(
				`select * from tests
				where expires_at > ?
				and status not in ('completed', 'failed', 'expired')
				order by created_at asc`
			)
			.all(now) as TestRow[];
		return rows.map((row) => this.mapTest(row, this.listObservations(row.id)));
	}

	updateStatus(
		id: string,
		status: TestStatus,
		fields: { outboundSeenAt?: string; returnSeenAt?: string; replyStatus?: string | null } = {}
	) {
		const current = this.getTest(id);
		if (!current) return null;

		this.db
			.prepare(
				`update tests
				set status = ?,
					outbound_seen_at = coalesce(?, outbound_seen_at),
					return_seen_at = coalesce(?, return_seen_at),
					reply_status = coalesce(?, reply_status),
					updated_at = ?
				where id = ?`
			)
			.run(
				status,
				fields.outboundSeenAt ?? null,
				fields.returnSeenAt ?? null,
				fields.replyStatus ?? null,
				new Date().toISOString(),
				id
			);

		return this.getTest(id);
	}

	addObservation(input: ObservationInput): PacketObservation | null {
		const pathJson = JSON.stringify(input.path);
		const resolvedPathJson = JSON.stringify(input.resolvedPath);
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
					test_id, direction, source, packet_hash, observer_id, observer_name, first_seen, rssi, snr,
					hop_count, path_json, resolved_path_json, decoded_type, decoded_text, raw_hex, created_at
				) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			)
			.run(
				input.testId,
				input.direction,
				input.source,
				input.packetHash,
				input.observerId ?? null,
				input.observerName ?? null,
				input.firstSeen ?? null,
				input.rssi ?? null,
				input.snr ?? null,
				input.hopCount,
				pathJson,
				resolvedPathJson,
				input.decodedType ?? null,
				input.decodedText ?? null,
				input.rawHex ?? null,
				new Date().toISOString()
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
			.prepare('select * from observations where test_id = ? order by created_at asc')
			.all(testId) as ObservationRow[];
		return rows.map((row) => this.mapObservation(row));
	}

	upsertNodes(nodes: NodeRecord[]) {
		const statement = this.db.prepare(
			`insert into nodes (public_key, name, short_hash, node_type, lat, lon, updated_at, source, raw_json)
			values (@publicKey, @name, @shortHash, @nodeType, @lat, @lon, @updatedAt, @source, @rawJson)
			on conflict(public_key) do update set
				name = excluded.name,
				short_hash = excluded.short_hash,
				node_type = excluded.node_type,
				lat = excluded.lat,
				lon = excluded.lon,
				updated_at = excluded.updated_at,
				source = excluded.source,
				raw_json = excluded.raw_json`
		);
		const run = this.db.transaction((records: NodeRecord[]) => {
			for (const node of records) {
				statement.run({ ...node, rawJson: node.raw ? JSON.stringify(node.raw) : null });
			}
		});
		run(nodes);
	}

	listNodes(query = '', limit = 200): NodeRecord[] {
		const search = `%${query.toLowerCase()}%`;
		const rows = this.db
			.prepare(
				`select * from nodes
				where lower(name) like ? or lower(public_key) like ? or lower(short_hash) like ?
				order by name asc
				limit ?`
			)
			.all(search, search, search, limit) as NodeRow[];
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
				return_seen_at text,
				reply_status text,
				created_at text not null,
				updated_at text not null,
				expires_at text not null
			);

			create index if not exists tests_browser_idx on tests(browser_id, created_at desc);
			create index if not exists tests_active_idx on tests(status, expires_at);

			create table if not exists observations (
				id integer primary key autoincrement,
				test_id text not null references tests(id) on delete cascade,
				direction text not null,
				source text not null,
				packet_hash text not null,
				observer_id text,
				observer_name text,
				first_seen text,
				rssi real,
				snr real,
				hop_count integer not null,
				path_json text not null,
				resolved_path_json text not null,
				decoded_type text,
				decoded_text text,
				raw_hex text,
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
				source text not null,
				raw_json text
			);

			create index if not exists nodes_short_hash_idx on nodes(short_hash);
			create index if not exists nodes_name_idx on nodes(name);
		`);
		this.migrateObservationsShape();
		this.db.exec(`
			create index if not exists observations_packet_observer_path_idx
			on observations(test_id, packet_hash, direction, source, observer_id, path_json);
		`);
	}

	private migrateObservationsShape() {
		const columns = this.db.pragma('table_info(observations)') as Array<{ name: string }>;
		const hasObserverId = columns.some((column) => column.name === 'observer_id');
		const indexes = this.db.pragma('index_list(observations)') as Array<{ name: string }>;
		const hasOldAutoUnique = indexes.some((index) => index.name.startsWith('sqlite_autoindex'));
		if (hasObserverId && !hasOldAutoUnique) return;

		this.db.exec(`
			alter table observations rename to observations_old;

			create table observations (
				id integer primary key autoincrement,
				test_id text not null references tests(id) on delete cascade,
				direction text not null,
				source text not null,
				packet_hash text not null,
				observer_id text,
				observer_name text,
				first_seen text,
				rssi real,
				snr real,
				hop_count integer not null,
				path_json text not null,
				resolved_path_json text not null,
				decoded_type text,
				decoded_text text,
				raw_hex text,
				created_at text not null
			);

			insert into observations (
				id, test_id, direction, source, packet_hash, observer_id, observer_name, first_seen,
				rssi, snr, hop_count, path_json, resolved_path_json, decoded_type, decoded_text,
				raw_hex, created_at
			)
			select
				id, test_id, direction, source, packet_hash,
				${hasObserverId ? 'observer_id' : 'null'},
				observer_name, first_seen, rssi, snr, hop_count, path_json, resolved_path_json,
				decoded_type, decoded_text, raw_hex, created_at
			from observations_old;

			drop table observations_old;
		`);
	}

	private mapTest(row: TestRow, observations: PacketObservation[]): DiagnosticTest {
		return {
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
			returnSeenAt: row.return_seen_at,
			replyStatus: row.reply_status,
			createdAt: row.created_at,
			updatedAt: row.updated_at,
			expiresAt: row.expires_at,
			observations
		};
	}

	private mapObservation(row: ObservationRow): PacketObservation {
		return {
			id: row.id,
			testId: row.test_id,
			direction: row.direction,
			source: row.source,
			packetHash: row.packet_hash,
			observerId: row.observer_id,
			observerName: row.observer_name,
			observerNode: row.observer_id ? this.findNodeByPublicKey(row.observer_id) : null,
			firstSeen: row.first_seen,
			rssi: row.rssi,
			snr: row.snr,
			hopCount: row.hop_count,
			path: JSON.parse(row.path_json),
			resolvedPath: JSON.parse(row.resolved_path_json),
			decodedType: row.decoded_type,
			decodedText: row.decoded_text,
			rawHex: row.raw_hex,
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
			source: row.source,
			raw: row.raw_json ? JSON.parse(row.raw_json) : undefined
		};
	}
}
