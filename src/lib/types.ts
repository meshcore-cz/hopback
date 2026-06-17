export type TestStatus =
	| 'created'
	| 'waiting'
	| 'detected'
	| 'verified'
	| 'replying'
	| 'completed'
	| 'partial'
	| 'failed'
	| 'expired';

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
	raw?: unknown;
}

export interface PacketObservation {
	id: number;
	testId: string;
	direction: Direction;
	source: string;
	packetHash: string;
	observerId?: string | null;
	observerName?: string | null;
	observerNode?: NodeRecord | null;
	firstSeen?: string | null;
	rssi?: number | null;
	snr?: number | null;
	hopCount: number;
	path: string[];
	resolvedPath: NodeRecord[];
	decodedType?: string | null;
	decodedText?: string | null;
	rawHex?: string | null;
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
	returnSeenAt?: string | null;
	replyStatus?: string | null;
	createdAt: string;
	updatedAt: string;
	expiresAt: string;
	observations: PacketObservation[];
}

export interface RuntimeStatus {
	analyzers: Array<{
		url: string;
		state: 'connecting' | 'open' | 'closed' | 'error';
		lastMessageAt?: string;
		lastError?: string;
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
	activeTests: number;
	verbose: boolean;
}

export interface BrowserEvent {
	type: 'testUpdated' | 'status' | 'hello' | 'error';
	test?: DiagnosticTest;
	status?: RuntimeStatus;
	message?: string;
}
