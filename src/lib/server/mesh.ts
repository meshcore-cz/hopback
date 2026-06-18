import { load, type Envelope, type MeshcoreWasm } from '@meshcore-cz/meshpkt';
import { randomBytes } from 'node:crypto';
import type { DiagnosticTest, EndpointConfig } from '../types';

let meshPromise: Promise<MeshcoreWasm> | null = null;

export interface DecodedMatch {
	type: string;
	text?: string;
	ackCrcHex?: string;
	direction: 'outbound' | 'return';
}

export interface BuiltPacket {
	hex: string;
	text?: string;
	ackCrcHex?: string;
	type: 'ACK' | 'PATH' | 'TXT_MSG' | 'TXT_MSG_IDENTITY';
}

export async function mesh() {
	meshPromise ??= load();
	return meshPromise;
}

export function isHex(value: string, bytes?: number) {
	const normalized = value.trim();
	return /^[0-9a-f]+$/i.test(normalized) && (!bytes || normalized.length === bytes * 2);
}

export function packetCode() {
	return randomBytes(3).toString('hex');
}

export function decodeTextHex(hex: string | undefined) {
	if (!hex || !/^[0-9a-f]+$/i.test(hex) || hex.length % 2 !== 0) return undefined;
	const bytes = Uint8Array.from(hex.match(/.{2}/g)!.map((byte) => Number.parseInt(byte, 16)));
	const text = new TextDecoder().decode(bytes).replace(/\0/g, '').trim();
	return text || undefined;
}

export async function decodeEnvelope(rawHex: string): Promise<Envelope | null> {
	const api = await mesh();
	const envelope = api.decodeEnvelope(rawHex);
	if ('error' in envelope) return null;
	return envelope;
}

export async function packetContentHash(rawHex: string): Promise<string | null> {
	const api = await mesh();
	const result = api.computeContentHash(rawHex);
	if ('error' in result) return null;
	return result.hash;
}

export async function identifyPacket(
	rawHex: string,
	tests: DiagnosticTest[]
): Promise<(DecodedMatch & { test: DiagnosticTest }) | null> {
	const api = await mesh();
	const envelope = api.decodeEnvelope(rawHex);
	if ('error' in envelope) return null;

	for (const test of tests) {
		const endpointKey = endpointPrivateKey(test);
		if (!endpointKey) continue;

		const decoded = tryDecodePayload(api, envelope, endpointKey, test.userPublicKey);
		if (!decoded) continue;

		if (decoded.ackCrcHex) {
			if (decoded.ackCrcHex === test.replyAckCrcHex) {
				return {
					test,
					type: decoded.type,
					ackCrcHex: decoded.ackCrcHex,
					direction: 'return'
				};
			}
			if (decoded.ackCrcHex === test.outboundAckCrcHex) {
				return {
					test,
					type: decoded.type,
					ackCrcHex: decoded.ackCrcHex,
					direction: 'outbound'
				};
			}
		}

		const text = decoded.text || decodeTextHex(decoded.dataHex);
		const haystack = [text, decoded.dataHex, rawHex].filter(Boolean).join('\n');
		if (haystack.includes(test.code)) {
			const isReplyPayload = Boolean(text?.includes(replyTextNeedle(test.code)));
			const isAck =
				decoded.type === 'PATH' || decoded.type === 'PATH_IDENTITY' || decoded.type === 'RESPONSE';
			return {
				test,
				type: decoded.type,
				text,
				direction: isReplyPayload || (isAck && test.replyBroadcastAt) ? 'return' : decoded.direction
			};
		}
	}

	return null;
}

function replyTextNeedle(code: string) {
	return `Hopback ${code} received by`;
}

export async function buildReplyPacket(
	endpoint: EndpointConfig,
	userPublicKey: string,
	code: string,
	outboundRawHex?: string
) {
	const result = await buildReplyPackets(endpoint, userPublicKey, code, outboundRawHex);
	if ('error' in result) return result;
	const reply = result.packets.find((packet) => packet.type.startsWith('TXT_MSG'));
	return reply ? { hex: reply.hex, text: result.text } : { error: 'Reply packet was not built' };
}

export async function buildReplyPackets(
	endpoint: EndpointConfig,
	userPublicKey: string,
	code: string,
	outboundRawHex?: string
) {
	const api = await mesh();
	const privateKey = endpoint.privateKey?.trim().toLowerCase();
	if (!privateKey) return { error: 'Endpoint private key is not configured' };

	const text = `Hopback ${code} received by ${endpoint.name}`;
	const timestamp = Math.floor(Date.now() / 1000);
	const replyAckCrcHex = ackCrcHex(api.textAckCrc(timestamp, 1, text, endpoint.publicKey));
	const packets: BuiltPacket[] = [];
	let replyPath: { path: string; pathHashSize: number } | null = null;

	if (outboundRawHex) {
		const inbound = decodeInboundTextForAck(api, outboundRawHex, privateKey, userPublicKey);
		if ('error' in inbound) return { error: inbound.error };
		replyPath = { path: inbound.path, pathHashSize: inbound.pathHashSize };
		const outboundAckCrcHex = ackCrcHex(
			api.textAckCrc(inbound.timestamp, inbound.attempt, inbound.text, userPublicKey)
		);

		// Send the path-bearing ACK (ack+path) only when the inbound message recorded
		// a real multi-hop path — then the ACK routes back along the reverse path
		// instead of flooding. For a direct-neighbour message (empty path) a path ACK
		// would degrade to a FLOOD packet, so we send the plain (cheaper) ACK instead.
		// Note: MeshCore has no non-flooding ACK, so a direct-message ACK still floods.
		const pathAck =
			inbound.path.length === 0
				? null
				: privateKey.length === 128
					? api.encodePathTextAckExpanded(
							privateKey,
							endpoint.publicKey,
							userPublicKey,
							inbound.timestamp,
							inbound.attempt,
							inbound.text,
							inbound.path,
							inbound.pathHashSize
						)
					: privateKey.length === 64
						? api.encodePathTextAckIdentity(
								privateKey,
								userPublicKey,
								inbound.timestamp,
								inbound.attempt,
								inbound.text,
								inbound.path,
								inbound.pathHashSize
							)
						: { error: `Endpoint privateKey must be 64 or 128 hex characters, got ${privateKey.length}` };

		if (pathAck && !('error' in pathAck)) {
			packets.push({ hex: pathAck.hex, ackCrcHex: outboundAckCrcHex, type: 'PATH' });
		} else {
			if (pathAck && 'error' in pathAck) {
				console.warn(`[reply] path ACK unavailable (${pathAck.error}); falling back to flood ACK`);
			}
			const plainAck = api.encodeTextAck(
				inbound.timestamp,
				inbound.attempt,
				inbound.text,
				userPublicKey
			);
			if ('error' in plainAck) return { error: plainAck.error };
			packets.push({ hex: plainAck.hex, ackCrcHex: outboundAckCrcHex, type: 'ACK' });
		}
	}

	const identityReply =
		privateKey.length === 128
			? replyPath
				? api.encodeDirectTextExpandedPath(
						privateKey,
						endpoint.publicKey,
						userPublicKey,
						text,
						timestamp,
						1,
						replyPath.path,
						replyPath.pathHashSize
					)
				: api.encodeDirectTextExpanded(
						privateKey,
						endpoint.publicKey,
						userPublicKey,
						text,
						timestamp,
						1
					)
			: privateKey.length === 64
				? replyPath
					? api.encodeDirectTextIdentityPath(
							privateKey,
							userPublicKey,
							text,
							timestamp,
							1,
							replyPath.path,
							replyPath.pathHashSize
						)
					: api.encodeDirectTextIdentity(privateKey, userPublicKey, text, timestamp, 1)
				: {
						error: `Endpoint privateKey must be 64 or 128 hex characters, got ${privateKey.length}`
					};
	if (!('error' in identityReply)) {
		packets.push({
			hex: identityReply.hex,
			text,
			ackCrcHex: replyAckCrcHex,
			type: 'TXT_MSG_IDENTITY'
		});
		return { packets, text, replyAckCrcHex };
	}

	return { error: identityReply.error };
}

function decodeInboundTextForAck(
	api: MeshcoreWasm,
	rawHex: string,
	privateKey: string,
	userPublicKey: string
):
	| {
			route: string;
			timestamp: number;
			attempt: number;
			text: string;
			path: string;
			pathHashSize: number;
	  }
	| { error: string } {
	const envelope = api.decodeEnvelope(rawHex);
	if ('error' in envelope) return envelope;
	if (envelope.type !== 'TXT_MSG') return { error: 'Inbound packet is not TXT_MSG' };

	const expanded = api.decodeDirectTextExpanded(envelope.payloadHex, privateKey, userPublicKey);
	if (!('error' in expanded)) {
		return {
			route: envelope.route,
			timestamp: expanded.timestamp,
			attempt: expanded.attempt,
			text: expanded.text,
			path: envelope.hops.join(''),
			pathHashSize: envelope.pathHashSize
		};
	}

	const identity = api.decodeDirectTextIdentity(
		envelope.payloadHex,
		privateKey.slice(0, 64),
		userPublicKey
	);
	if (!('error' in identity)) {
		return {
			route: envelope.route,
			timestamp: identity.timestamp,
			attempt: identity.attempt,
			text: identity.text,
			path: envelope.hops.join(''),
			pathHashSize: envelope.pathHashSize
		};
	}

	return { error: expanded.error };
}

function ackCrcHex(result: { crc: number } | { error: string }) {
	if ('error' in result) return undefined;
	return (result.crc >>> 0).toString(16).padStart(8, '0');
}

export function endpointPrivateKey(test: Pick<DiagnosticTest, 'endpointId'>) {
	const globalEndpoints = globalThis as typeof globalThis & {
		__hopbackEndpointKeys?: Map<string, string>;
	};
	return globalEndpoints.__hopbackEndpointKeys?.get(test.endpointId);
}

export function registerEndpointKeys(endpoints: EndpointConfig[], fallbackPrivateKey?: string) {
	const globalEndpoints = globalThis as typeof globalThis & {
		__hopbackEndpointKeys?: Map<string, string>;
	};
	globalEndpoints.__hopbackEndpointKeys = new Map(
		endpoints
			.map((endpoint) => [endpoint.id, endpoint.privateKey || fallbackPrivateKey] as const)
			.filter((entry): entry is readonly [string, string] => Boolean(entry[1]))
	);
}

function tryDecodePayload(
	api: MeshcoreWasm,
	envelope: Envelope,
	privateKey: string,
	peerPublicKey: string
): {
	type: string;
	text?: string;
	dataHex?: string;
	ackCrcHex?: string;
	direction: 'outbound' | 'return';
} | null {
	if (envelope.type === 'ACK') {
		const ack = api.decodeAck(envelope.payloadHex);
		if (!('error' in ack)) return { type: 'ACK', ackCrcHex: ack.crcHex, direction: 'outbound' };
	}

	if (envelope.type === 'TXT_MSG') {
		const expanded = api.decodeDirectTextExpanded(envelope.payloadHex, privateKey, peerPublicKey);
		if (!('error' in expanded)) {
			return { type: 'TXT_MSG', text: expanded.text, direction: 'outbound' };
		}

		const identity = api.decodeDirectTextIdentity(
			envelope.payloadHex,
			privateKey.slice(0, 64),
			peerPublicKey
		);
		if (!('error' in identity)) {
			return { type: 'TXT_MSG_IDENTITY', text: identity.text, direction: 'outbound' };
		}
	}

	if (envelope.type === 'REQ') {
		const req = api.decodeReq(envelope.payloadHex, privateKey.slice(0, 64), peerPublicKey);
		if (!('error' in req)) return { type: 'REQ', dataHex: req.dataHex, direction: 'outbound' };
	}

	if (envelope.type === 'ANON_REQ') {
		const req = api.decodeAnonReq(envelope.payloadHex, privateKey);
		if (!('error' in req)) return { type: 'ANON_REQ', dataHex: req.dataHex, direction: 'outbound' };
	}

	if (envelope.type === 'RESPONSE') {
		const response = api.decodeResponse(
			envelope.payloadHex,
			privateKey.slice(0, 64),
			peerPublicKey
		);
		if (!('error' in response)) {
			const ack = api.decodeAck(response.dataHex);
			return {
				type: 'RESPONSE',
				dataHex: response.dataHex,
				ackCrcHex: 'error' in ack ? undefined : ack.crcHex,
				direction: 'outbound'
			};
		}
	}

	if (envelope.type === 'PATH') {
		if (privateKey.length === 128) {
			const expandedPath = api.decodePathExpanded(envelope.payloadHex, privateKey, peerPublicKey);
			if (!('error' in expandedPath)) {
				return {
					type: 'PATH',
					dataHex: expandedPath.extra,
					ackCrcHex:
						expandedPath.ackCrc !== undefined
							? (expandedPath.ackCrc >>> 0).toString(16).padStart(8, '0')
							: undefined,
					direction: 'outbound'
				};
			}
		}

		const path = api.decodePath(envelope.payloadHex, privateKey.slice(0, 64), peerPublicKey);
		if (!('error' in path)) {
			const ack = api.decodeAck(path.extraHex);
			return {
				type: 'PATH',
				dataHex: path.extraHex,
				ackCrcHex: 'error' in ack ? undefined : ack.crcHex,
				direction: 'outbound'
			};
		}

		const identityPath = api.decodePathIdentity(
			envelope.payloadHex,
			privateKey.slice(0, 64),
			peerPublicKey
		);
		if (!('error' in identityPath))
			return {
				type: 'PATH_IDENTITY',
				dataHex: identityPath.extra,
				ackCrcHex:
					identityPath.ackCrc !== undefined
						? (identityPath.ackCrc >>> 0).toString(16).padStart(8, '0')
						: undefined,
				direction: 'outbound'
			};
	}

	return null;
}
