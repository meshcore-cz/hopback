import { browser } from '$app/environment';
import { env } from '$env/dynamic/public';

const apiBase = normalizeBase(env.PUBLIC_HOPBACK_API_URL || '');
const explicitWsBase = normalizeBase(env.PUBLIC_HOPBACK_WS_URL || '');

export function apiUrl(path: string) {
	const nextPath = normalizePath(path);
	return apiBase ? `${apiBase}${nextPath}` : nextPath;
}

export function apiFetch(path: string, init?: RequestInit) {
	return fetch(apiUrl(path), init);
}

export function wsUrl(path: string) {
	const nextPath = normalizePath(path);
	if (explicitWsBase) return `${explicitWsBase}${nextPath}`;
	if (apiBase) return `${apiBase.replace(/^http:/, 'ws:').replace(/^https:/, 'wss:')}${nextPath}`;
	if (!browser) return nextPath;
	const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
	return `${protocol}//${window.location.host}${nextPath}`;
}

function normalizeBase(value: string) {
	return value.trim().replace(/\/+$/, '');
}

function normalizePath(path: string) {
	return path.startsWith('/') ? path : `/${path}`;
}
