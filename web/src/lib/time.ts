import { t } from '$lib/i18n/index.svelte';

/** Human "x ago" string for an RFC3339/ISO timestamp, or an em dash when missing. */
export function relativeTime(iso?: string | null): string {
	if (!iso) return '—';
	const ms = Date.parse(iso);
	if (Number.isNaN(ms)) return '—';
	const delta = Math.floor((Date.now() - ms) / 1000);
	if (delta < 0) return t('time.justNow');
	if (delta < 60) return t('time.secondsAgo', { n: delta });
	if (delta < 3600) return t('time.minutesAgo', { n: Math.floor(delta / 60) });
	if (delta < 86400) return t('time.hoursAgo', { n: Math.floor(delta / 3600) });
	if (delta < 604800) return t('time.daysAgo', { n: Math.floor(delta / 86400) });
	return t('time.weeksAgo', { n: Math.floor(delta / 604800) });
}

/** Compact uptime like "3d 4h", "5h 12m", "9m" from a process start timestamp. */
export function uptime(iso?: string | null): string {
	if (!iso) return '—';
	const ms = Date.parse(iso);
	if (Number.isNaN(ms)) return '—';
	let s = Math.floor((Date.now() - ms) / 1000);
	if (s < 0) s = 0;
	const d = Math.floor(s / 86400);
	const h = Math.floor((s % 86400) / 3600);
	const m = Math.floor((s % 3600) / 60);
	if (d > 0) return `${d}d ${h}h`;
	if (h > 0) return `${h}h ${m}m`;
	if (m > 0) return `${m}m`;
	return `${s}s`;
}
