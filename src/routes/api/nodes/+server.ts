import { json, type RequestHandler } from '@sveltejs/kit';
import { getRuntime } from '$lib/server/runtime';

export const GET: RequestHandler = ({ url }) => {
	const runtime = getRuntime();
	const query = url.searchParams.get('q') || '';
	const endpoints = runtime.getEndpoints();
	const recentDays = Number(url.searchParams.get('recentDays') || 0);
	const updatedAfter =
		Number.isFinite(recentDays) && recentDays > 0
			? new Date(Date.now() - recentDays * 24 * 60 * 60 * 1000).toISOString()
			: undefined;
	const nodes = runtime.db.listNodes(
		query,
		Number(url.searchParams.get('limit') || 200),
		updatedAfter
	);

	return json({
		endpoints,
		nodes,
		configuredEndpointCount: endpoints.length
	});
};
