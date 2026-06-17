import { load, type Envelope, type MeshcoreWasm } from '@meshcore-cz/meshpkt';
import { randomBytes } from 'node:crypto';
import type { DiagnosticTest, EndpointConfig } from '../types';

let meshPromise: Promise<MeshcoreWasm> | null = null;

export interface DecodedMatch {
	type: string;
	text?: string;
	direction: 'outbound' | 'return';
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
	return randomBytes(2).toString('hex');
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
		//console.log("envelope", envelope, { endpointKey, userPublicKey: test.userPublicKey });

		const decoded = tryDecodePayload(api, envelope, endpointKey, test.userPublicKey);
		if (!decoded) continue;

		console.log('Decoded!!!!!', decoded);

		const text = decoded.text || decodeTextHex(decoded.dataHex);
		const haystack = [text, decoded.dataHex, rawHex].filter(Boolean).join('\n');
		if (haystack.includes(test.code)) {
			return {
				test,
				type: decoded.type,
				text,
				direction: decoded.direction
			};
		}
	}

	return null;
}

export async function buildReplyPacket(
	endpoint: EndpointConfig,
	userPublicKey: string,
	code: string
) {
	const api = await mesh();
	const privateKey = endpoint.privateKey;
	if (!privateKey) return { error: 'Endpoint private key is not configured' };

	const text = `Hopback ${code} received by ${endpoint.name}`;
	const timestamp = Math.floor(Date.now() / 1000);

	if (privateKey.length >= 64) {
		const identityReply = api.encodeDirectTextIdentity(
			privateKey.slice(0, 64),
			userPublicKey,
			text,
			timestamp,
			1
		);
		if (!('error' in identityReply)) return { hex: identityReply.hex, text };
	}

	const directReply = api.encodeDirectText(privateKey.slice(0, 64), userPublicKey, text);
	if (!('error' in directReply)) return { hex: directReply.hex, text };

	return { error: directReply.error };
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
): { type: string; text?: string; dataHex?: string; direction: 'outbound' | 'return' } | null {
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
		if (!('error' in response))
			return { type: 'RESPONSE', dataHex: response.dataHex, direction: 'return' };
	}

	if (envelope.type === 'PATH') {
		const path = api.decodePath(envelope.payloadHex, privateKey.slice(0, 64), peerPublicKey);
		if (!('error' in path)) return { type: 'PATH', dataHex: path.extraHex, direction: 'return' };

		const identityPath = api.decodePathIdentity(
			envelope.payloadHex,
			privateKey.slice(0, 64),
			peerPublicKey
		);
		if (!('error' in identityPath))
			return { type: 'PATH_IDENTITY', dataHex: identityPath.extra, direction: 'return' };
	}

	return null;
}
