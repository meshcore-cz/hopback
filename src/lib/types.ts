export type TestStatus = 'waiting' | 'detected' | 'replying' | 'completed' | 'failed' | 'expired';

export type Direction = 'outbound' | 'return';

export interface EndpointConfig {
	id: string;
	name: string;
	region: string;
	publicKey: string;
	type?: number;
	host?: string;
	location?: {
		label: string;
		lat?: number;
		lon?: number;
	};
	privateKey?: string;
	agentId?: string;
}

export interface NodeRecord {
	publicKey: string;
	name: string;
	shortHash: string;
	nodeType?: number | null;
	lat?: number | null;
	lon?: number | null;
	updatedAt: string;
	source: string;
}

/**
 * Minimal, shared description of a mesh node. Observations reference nodes by
 * key into {@link DiagnosticTest.nodes} instead of embedding a copy each time.
 */
export interface NodeRef {
	name: string;
	shortHash: string;
	publicKey?: string | null;
	lat?: number | null;
	lon?: number | null;
}

export interface DeliveryPathRow {
	key: string;
	name: string;
	meta: string;
	short: string;
	publicKey?: string | null;
	shortHash?: string | null;
	lat?: number | null;
	lon?: number | null;
	hasCoords?: boolean;
	tone: 'edge' | 'hop';
}

export interface DeliveryPathOption {
	key: string;
	direction: Direction;
	hopCount: number;
	observationId: number;
	packetHash: string;
	createdAt: string;
	rows: DeliveryPathRow[];
}

export interface PropagationMapPoint {
	key: string;
	name: string;
	kind: 'node' | 'observer' | 'endpoint';
	publicKey?: string | null;
	lat: number;
	lon: number;
}

/** Packet classification shared by the observations table and the map legend. */
export type PacketKind = 'user msg' | 'ack+path' | 'ack' | 'reply' | 'reply ack';

export interface PropagationMapSegment {
	key: string;
	direction: Direction;
	kind: PacketKind;
	from: [number, number];
	to: [number, number];
}

export interface PropagationMapData {
	points: PropagationMapPoint[];
	segments: PropagationMapSegment[];
}

export interface PacketObservation {
	id: number;
	direction: Direction;
	source: string;
	packetHash: string;
	observerId?: string | null;
	observerName?: string | null;
	/** Key into {@link DiagnosticTest.nodes} for the observing node, if resolved. */
	observerKey?: string | null;
	hopCount: number;
	/** Raw per-hop short hashes as seen on the mesh. */
	path: string[];
	/**
	 * Per-hop key into {@link DiagnosticTest.nodes}, aligned to {@link path}.
	 * `null` for hops that could not be resolved to a known node.
	 */
	pathKeys: (string | null)[];
	distanceKm?: number | null;
	decodedType?: string | null;
	createdAt: string;
}

export interface DiagnosticTest {
	id: string;
	browserId: string;
	userPublicKey: string;
	endpointId: string;
	endpointName: string;
	endpointRegion: string;
	endpointPublicKey: string;
	endpointLocation?: EndpointConfig['location'];
	code: string;
	status: TestStatus;
	qrPayload: string;
	qrDataUrl?: string;
	outboundSeenAt?: string | null;
	outboundEndpointSeenAt?: string | null;
	outboundAckSeenAt?: string | null;
	replyBroadcastAt?: string | null;
	returnSeenAt?: string | null;
	replyAckSeenAt?: string | null;
	replyEndpointAckAt?: string | null;
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
	createdAt: string;
	updatedAt: string;
	expiresAt: string;
	observations: PacketObservation[];
	/** Shared node lookup keyed by node key (publicKey or short hash). */
	nodes: Record<string, NodeRef>;
	observationCount?: number;
	deliveryPaths?: Record<Direction, DeliveryPathOption[]>;
	propagationMap?: PropagationMapData;
	pathStatistics?: PathStatistics;
}

export interface PathStatistics {
	totalPaths: number;
	outboundPaths: number;
	returnPaths: number;
	longestDistanceKm?: number | null;
	longestDistanceLabel?: string | null;
	longestHopCount: number;
	shortestHopCount: number;
	averageDistanceKm?: number | null;
}

export interface RuntimeStatus {
	analyzers: Array<{
		url: string;
		state: 'connecting' | 'open' | 'closed' | 'error';
		lastMessageAt?: string;
		lastError?: string;
	}>;
	endpoints: Array<{
		id: string;
		name: string;
		ready: boolean;
		connected: boolean;
		agentId?: string;
		ipcReady: boolean;
		lastSeenAt?: string;
	}>;
	agents: Array<{
		id: string;
		endpointId?: string;
		ipcReady: boolean;
		connectedAt: string;
		lastSeenAt: string;
	}>;
	nodes: number;
	observers: number;
	activeObservers: number;
	activeTests: number;
	verbose: boolean;
}

export interface BrowserEvent {
	type: 'testUpdated' | 'observation' | 'status' | 'hello' | 'error';
	test?: DiagnosticTest;
	testId?: string;
	status?: RuntimeStatus;
	observation?: PacketObservation;
	message?: string;
}
