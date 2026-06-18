import QRCode from 'qrcode';
import { json, type RequestHandler } from '@sveltejs/kit';
import { isHex } from '$lib/server/mesh';
import { getRuntime } from '$lib/server/runtime';
import type { DiagnosticTest, EndpointConfig } from '$lib/types';

interface CreateTestBody {
	userPublicKey?: string;
	endpointId?: string;
}

export const GET: RequestHandler = ({ url }) => {
	const browserId = url.searchParams.get('browserId');
	const limit = clampNumber(url.searchParams.get('limit'), 30, 1, 100);
	const offset = clampNumber(url.searchParams.get('offset'), 0, 0, 100_000);
	if (!browserId) return json({ tests: [], total: 0, limit, offset });
	const runtime = getRuntime();
	return json({
		tests: runtime.listTestsForBrowser(browserId, limit, offset).map((test) => redactPrivate(test)),
		total: runtime.countTestsForBrowser(browserId),
		limit,
		offset
	});
};

export const POST: RequestHandler = async ({ request }) => {
	const body = (await request.json()) as CreateTestBody;
	const userPublicKey = body.userPublicKey?.trim();

	if (!userPublicKey || !isHex(userPublicKey, 32)) {
		return json({ message: 'User public key must be 64 hex characters' }, { status: 400 });
	}

	const runtime = getRuntime();
	const endpoint = resolveEndpoint(runtime.getEndpoints(), body);
	if (!endpoint) return json({ message: 'Choose an endpoint' }, { status: 400 });
	if (!isHex(endpoint.publicKey, 32)) {
		return json({ message: 'Endpoint public key must be 64 hex characters' }, { status: 400 });
	}
	if (!runtime.isEndpointReady(endpoint.id)) {
		return json(
			{ message: `${endpoint.name} endpoint agent is offline. Start its agent before testing.` },
			{ status: 503 }
		);
	}

	const code = createUnusedCode(runtime);
	if (!code) return json({ message: 'Could not allocate a unique test code' }, { status: 503 });
	const expiresAt = new Date(Date.now() + runtime.config.testTtlMinutes * 60_000).toISOString();
	const qrPayload = buildQrPayload(runtime.config.serviceName, endpoint, code);
	const test = runtime.createTest({
		browserId: crypto.randomUUID(),
		userPublicKey,
		endpoint,
		code,
		qrPayload,
		expiresAt
	});
	const qrDataUrl = await QRCode.toDataURL(qrPayload, { margin: 1, width: 280 });

	return json({ test: redactPrivate({ ...runtime.decorateTest(test), qrDataUrl }) });
};

function resolveEndpoint(
	configured: EndpointConfig[],
	body: CreateTestBody
): EndpointConfig | null {
	const configuredEndpoint = configured.find((endpoint) => endpoint.id === body.endpointId);
	if (configuredEndpoint) return configuredEndpoint;
	return null;
}

function buildQrPayload(_serviceName: string, endpoint: EndpointConfig, code: string) {
	const params = new URLSearchParams({
		name: endpoint.name,
		public_key: endpoint.publicKey,
		type: String(endpoint.type),
		message: code
	});
	return `meshcore://contact/add?${params.toString()}`;
}

function redactPrivate(test: DiagnosticTest) {
	return test;
}

function createUnusedCode(runtime: ReturnType<typeof getRuntime>) {
	for (let attempt = 0; attempt < 10; attempt += 1) {
		const code = runtime.createCode();
		if (!runtime.getTest(code)) return code;
	}
	return null;
}

function clampNumber(value: string | null, fallback: number, min: number, max: number) {
	const parsed = Number(value);
	if (!Number.isFinite(parsed)) return fallback;
	return Math.min(max, Math.max(min, Math.floor(parsed)));
}
