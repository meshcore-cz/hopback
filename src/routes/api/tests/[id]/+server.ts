import QRCode from 'qrcode';
import { json, type RequestHandler } from '@sveltejs/kit';
import { getRuntime } from '$lib/server/runtime';

export const GET: RequestHandler = async ({ params }) => {
	const runtime = getRuntime();
	if (!params.id) return json({ message: 'Test not found' }, { status: 404 });
	const test = runtime.db.getTest(params.id);
	if (!test) return json({ message: 'Test not found' }, { status: 404 });

	return json({
		test: {
			...runtime.decorateTest(test),
			qrDataUrl: await QRCode.toDataURL(test.qrPayload, { margin: 1, width: 280 })
		}
	});
};
