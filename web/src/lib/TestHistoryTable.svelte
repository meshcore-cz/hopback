<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { AlertCircle, CheckCircle2, CircleDashed, KeyRound } from '@lucide/svelte';
	import type { DiagnosticTest, TestStatus } from '$lib/types';
	import { t, localeTag } from '$lib/i18n/index.svelte';

	interface Props {
		tests: DiagnosticTest[];
		ownTestIds?: string[];
		loading?: boolean;
		emptyText: string;
	}

	let { tests, ownTestIds = [], loading = false, emptyText }: Props = $props();

	function isOwnTest(test: DiagnosticTest) {
		return ownTestIds.includes(test.id);
	}

	function statusMeta(current: TestStatus) {
		if (current === 'completed')
			return {
				icon: CheckCircle2,
				label: t('status.completed'),
				className: 'bg-teal-50 text-teal-800'
			};
		if (current === 'failed' || current === 'expired')
			return {
				icon: AlertCircle,
				label: t(`status.${current}`),
				className: 'bg-red-50 text-red-800'
			};
		return {
			icon: CircleDashed,
			label: t(`status.${current}`),
			className: 'bg-neutral-100 text-neutral-700'
		};
	}

	function totalTime(test: DiagnosticTest) {
		const unfinished =
			test.status === 'expired' || test.status === 'failed' ? '—' : t('common.pending');
		if (!test.returnSeenAt) return unfinished;
		const start = new Date(test.createdAt).getTime();
		const end = new Date(test.returnSeenAt).getTime();
		if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return unfinished;
		return `${((end - start) / 1000).toFixed(1)} s`;
	}

	function formatDateTime(value?: string | null) {
		if (!value) return '—';
		const date = new Date(value);
		if (!Number.isFinite(date.getTime())) return '—';
		return date.toLocaleString(localeTag(), { dateStyle: 'short', timeStyle: 'short' });
	}

	function relativeTime(value?: string | null) {
		if (!value) return t('common.pending');
		const date = new Date(value);
		const delta = Math.floor((Date.now() - date.getTime()) / 1000);
		if (!Number.isFinite(delta)) return t('common.pending');
		if (delta < 0) return t('time.justNow');
		if (delta < 60) return t('time.secondsAgo', { n: delta });
		if (delta < 3600) return t('time.minutesAgo', { n: Math.floor(delta / 60) });
		if (delta < 86400) return t('time.hoursAgo', { n: Math.floor(delta / 3600) });
		if (delta < 604800) return t('time.daysAgo', { n: Math.floor(delta / 86400) });
		return t('time.weeksAgo', { n: Math.floor(delta / 604800) });
	}
</script>

<div class="overflow-x-auto">
	<table class="w-full min-w-[860px] text-left text-sm">
		<thead class="border-b border-neutral-200 text-xs uppercase text-neutral-500">
			<tr>
				<th class="py-2 pr-3 font-semibold">{t('history.col.status')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.endpoint')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.date')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.userKey')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.code')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.time')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.observed')}</th>
			</tr>
		</thead>
		<tbody class="divide-y divide-neutral-100">
			{#each tests as test, index (test.id)}
				{@const meta = statusMeta(test.status)}
				{@const StatusIcon = meta.icon}
				{@const time = totalTime(test)}
				{@const mine = isOwnTest(test)}
				<tr
					class={`group cursor-pointer transition-colors ${mine ? 'bg-teal-50/70 hover:bg-teal-100/70' : 'hover:bg-neutral-50'}`}
					onclick={() => goto(resolve('/[id]', { id: test.id }))}
					in:slide={{ duration: 300, delay: Math.min(index, 12) * 40, easing: cubicOut }}
				>
					<td class="py-2 pr-3">
						<span
							class={`inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-semibold ${meta.className}`}
						>
							<StatusIcon size={13} />
							{meta.label}
						</span>
					</td>
					<td class="px-3 py-2">
						<div class="flex items-center gap-2">
							<p class="font-medium text-neutral-950 group-hover:text-teal-800">
								{test.endpointName}
							</p>
							{#if mine}
								<span
									class="rounded bg-teal-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-teal-800"
								>
									{t('history.mine')}
								</span>
							{/if}
						</div>
					</td>
					<td class="whitespace-nowrap px-3 py-2 text-neutral-600">
						<span title={relativeTime(test.createdAt)}>{formatDateTime(test.createdAt)}</span>
					</td>
					<td class="mono px-3 py-2 text-xs text-neutral-500">
						<span class="inline-flex items-center gap-1">
							<KeyRound size={13} />
							{test.userPublicKey.slice(0, 10)}
						</span>
					</td>
					<td class="mono px-3 py-2 font-semibold text-neutral-800">{test.code}</td>
					<td class="px-3 py-2 text-neutral-600">
						<span class={time === 'pending' || time === '—' ? 'opacity-50' : ''}>{time}</span>
					</td>
					<td class="px-3 py-2 text-neutral-600">
						{test.observationCount ?? test.observations.length}
					</td>
				</tr>
			{:else}
				<tr>
					<td colspan="7" class="py-5 text-center text-sm text-neutral-500">
						{loading ? t('tests.loading') : emptyText}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>
