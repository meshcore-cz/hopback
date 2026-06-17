import { json, type RequestHandler } from '@sveltejs/kit';
import { getRuntime } from '$lib/server/runtime';

export const GET: RequestHandler = () => {
	return json(getRuntime().status());
};
