import { json, type RequestHandler } from '@sveltejs/kit';
import { getRuntime } from '$lib/server/runtime';

export const POST: RequestHandler = async ({ request }) => {
	const body = (await request.json()) as { ids?: string[] };
	const ids = Array.isArray(body.ids) ? body.ids.filter((id): id is string => typeof id === 'string') : [];
	const runtime = getRuntime();
	return json({ tests: runtime.getTestMetas(ids) });
};
