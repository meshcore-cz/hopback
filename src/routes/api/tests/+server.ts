import QRCode from 'qrcode';
import { json, type RequestHandler } from '@sveltejs/kit';
import { isHex } from '$lib/server/mesh';
import { getRuntime } from '$lib/server/runtime';
import type { DiagnosticTest, EndpointConfig } from '$lib/types';

interface CreateTestBody {
	browserId?: string;
	userPublicKey?: string;
	endpointId?: string;
}

export const GET: RequestHandler = ({ url }) => {
	const browserId = url.searchParams.get('browserId');
	if (!browserId) return json({ tests: [] });
	const runtime = getRuntime();
	return json({
		tests: runtime.db
			.listTestsForBrowser(browserId)
			.map((test) => redactPrivate(runtime.decorateTest(test)))
	});
};

export const POST: RequestHandler = async ({ request }) => {
	const body = (await request.json()) as CreateTestBody;
	const browserId = body.browserId?.trim();
	const userPublicKey = body.userPublicKey?.trim();

	if (!browserId) return json({ message: 'Missing browser id' }, { status: 400 });
	if (!userPublicKey || !isHex(userPublicKey, 32)) {
		return json({ message: 'User public key must be 64 hex characters' }, { status: 400 });
	}

	const runtime = getRuntime();
	const endpoint = resolveEndpoint(runtime.getEndpoints(), body);
	if (!endpoint) return json({ message: 'Choose an endpoint' }, { status: 400 });
	if (!isHex(endpoint.publicKey, 32)) {
		return json({ message: 'Endpoint public key must be 64 hex characters' }, { status: 400 });
	}

	const code = runtime.createCode();
	const expiresAt = new Date(Date.now() + runtime.config.testTtlMinutes * 60_000).toISOString();
	const qrPayload = buildQrPayload(runtime.config.serviceName, endpoint, code);
	const test = runtime.db.createTest({
		browserId,
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
