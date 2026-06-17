import { json, type RequestHandler } from '@sveltejs/kit';
import { getRuntime } from '$lib/server/runtime';

export const GET: RequestHandler = ({ url }) => {
	const runtime = getRuntime();
	const query = url.searchParams.get('q') || '';
	const endpoints = runtime.getEndpoints();
	const nodes = runtime.db.listNodes(query, Number(url.searchParams.get('limit') || 200));

	return json({
		endpoints,
		nodes,
		configuredEndpointCount: endpoints.length
	});
};
