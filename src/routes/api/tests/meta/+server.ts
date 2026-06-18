import { json, type RequestHandler } from '@sveltejs/kit';
import { getConfig } from '$lib/server/config';
import { HopbackDatabase } from '$lib/server/db';

let db: HopbackDatabase | null = null;

function getDb() {
	db ??= new HopbackDatabase(getConfig().databasePath);
	return db;
}

export const POST: RequestHandler = async ({ request }) => {
	const body = (await request.json()) as { ids?: string[] };
	const ids = Array.isArray(body.ids)
		? body.ids.filter((id): id is string => typeof id === 'string')
		: [];
	return json({ tests: getDb().getTestMetas(ids) });
};
