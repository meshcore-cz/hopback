import QRCode from 'qrcode';
import { json, type RequestHandler } from '@sveltejs/kit';
import { getRuntime } from '$lib/server/runtime';

export const GET: RequestHandler = async ({ params }) => {
	const runtime = getRuntime();
	if (!params.id) return json({ message: 'Test not found' }, { status: 404 });
	const test = runtime.getTest(params.id);
	if (!test) return json({ message: 'Test not found' }, { status: 404 });
	if (test.status === 'expired') return json({ test });

	return json({
		test: {
			...test,
			qrDataUrl: await QRCode.toDataURL(test.qrPayload, { margin: 1, width: 280 })
		}
	});
};
