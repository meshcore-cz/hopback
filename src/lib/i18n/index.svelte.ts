import { browser } from '$app/environment';
import { en, type MessageKey } from './en';
import { cs } from './cs';

export type Locale = 'en' | 'cs';
export const locales: Locale[] = ['en', 'cs'];

const dictionaries: Record<Locale, Record<string, string>> = { en, cs };
const STORAGE_KEY = 'hopback.locale';

let currentLocale = $state<Locale>('en');

export function locale() {
	return currentLocale;
}

export function setLocale(next: Locale) {
	if (!locales.includes(next)) return;
	currentLocale = next;
	if (browser) {
		localStorage.setItem(STORAGE_KEY, next);
		document.documentElement.lang = next;
	}
}

/** Detect from a stored choice, else the browser language. Call once on the client. */
export function initLocale() {
	if (!browser) return;
	const stored = localStorage.getItem(STORAGE_KEY);
	if (stored === 'en' || stored === 'cs') {
		setLocale(stored);
		return;
	}
	setLocale(navigator.language?.toLowerCase().startsWith('cs') ? 'cs' : 'en');
}

function interpolate(template: string, params?: Record<string, string | number>) {
	if (!params) return template;
	return template.replace(/\{(\w+)\}/g, (match, key) =>
		key in params ? String(params[key]) : match
	);
}

/** Translate a key. Reads currentLocale so callers re-render on locale change. */
export function t(key: MessageKey | (string & {}), params?: Record<string, string | number>) {
	const dict = dictionaries[currentLocale];
	const message = dict[key] ?? en[key as MessageKey] ?? key;
	return interpolate(message, params);
}

/** Czech-aware plural category. */
function pluralCategory(n: number): 'one' | 'few' | 'many' | 'other' {
	if (currentLocale === 'cs') {
		if (n === 1) return 'one';
		if (n >= 2 && n <= 4) return 'few';
		return 'many';
	}
	return n === 1 ? 'one' : 'other';
}

/** Pluralized translation: looks up `${prefix}.${category}`, falling back to `.other`/`.one`. */
export function tn(prefix: string, n: number, params?: Record<string, string | number>) {
	const dict = dictionaries[currentLocale];
	const category = pluralCategory(n);
	const key =
		`${prefix}.${category}` in dict
			? `${prefix}.${category}`
			: `${prefix}.other` in dict
				? `${prefix}.other`
				: `${prefix}.one`;
	return t(key, { n, ...params });
}

/** BCP-47 tag for Intl / toLocale* formatting. */
export function localeTag() {
	return currentLocale === 'cs' ? 'cs-CZ' : 'en-US';
}
