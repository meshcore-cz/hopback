<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { AlertCircle, CheckCircle2, CircleDashed, KeyRound } from '@lucide/svelte';
	import type { DiagnosticTest, TestStatus } from '$lib/types';
	import { t, localeTag } from '$lib/i18n/index.svelte';
	import { deriveMilestones, isAckObservation, isEndpointObservation } from '$lib/milestones';

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

	function unfinishedTime(test: DiagnosticTest) {
		return test.status === 'expired' || test.status === 'failed' ? '—' : t('common.pending');
	}

	function firstObservationAt(test: DiagnosticTest) {
		if (!test.observations.length) return test.outboundSeenAt || null;
		return test.observations.reduce<string | null>((earliest, observation) => {
			if (!earliest) return observation.createdAt;
			return new Date(observation.createdAt).getTime() < new Date(earliest).getTime()
				? observation.createdAt
				: earliest;
		}, null);
	}

	function elapsedTime(test: DiagnosticTest) {
		const milestones = deriveMilestones(test);
		const startAt = firstObservationAt(test) || milestones.outboundSeenAt;
		if (!startAt || !milestones.replyEndpointAckAt) return unfinishedTime(test);
		const start = new Date(startAt).getTime();
		const end = new Date(milestones.replyEndpointAckAt).getTime();
		if (!Number.isFinite(start) || !Number.isFinite(end) || end < start)
			return unfinishedTime(test);
		return `${((end - start) / 1000).toFixed(1)}s`;
	}

	function propagationMsFromObservations(test: DiagnosticTest) {
		if (!test.observations.length) return null;
		const totals = ['outbound', 'return'].map((direction) => {
			const times = test.observations
				.filter((item) => item.direction === direction && !isAckObservation(item))
				.map((item) => new Date(item.createdAt).getTime())
				.filter(Number.isFinite);
			if (!times.length) return null;
			return Math.max(...times) - Math.min(...times);
		});
		if (totals.every((value) => value === null)) return null;
		return totals.reduce<number>((sum, value) => sum + (value ?? 0), 0);
	}

	function formatPropagationMs(ms: number) {
		if (ms < 1000) return `${ms}ms`;
		return `${(ms / 1000).toFixed(2)}s`;
	}

	function elapsedWithPropagation(test: DiagnosticTest) {
		const elapsed = elapsedTime(test);
		if (elapsed === t('common.pending') || elapsed === '—') return elapsed;
		const propagationMs = test.propagationMs ?? propagationMsFromObservations(test);
		if (propagationMs == null) return elapsed;
		return `${elapsed} (${formatPropagationMs(propagationMs)})`;
	}

	function hopRangeFromObservations(test: DiagnosticTest) {
		const hops = test.observations
			.filter(
				(item) =>
					item.direction === 'outbound' &&
					!isAckObservation(item) &&
					isEndpointObservation(item, test)
			)
			.map((item) => item.hopCount)
			.filter(Number.isFinite);
		if (!hops.length) return null;
		return { min: Math.min(...hops), max: Math.max(...hops) };
	}

	function hopRange(test: DiagnosticTest) {
		const fromMeta =
			test.outboundHopMin != null && test.outboundHopMax != null
				? { min: test.outboundHopMin, max: test.outboundHopMax }
				: null;
		const range = fromMeta ?? hopRangeFromObservations(test);
		if (!range)
			return test.status === 'expired' || test.status === 'failed' ? '—' : t('common.pending');
		return range.min === range.max ? String(range.min) : `${range.min}-${range.max}`;
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

	function historyDate(value?: string | null) {
		if (!value) return '—';
		const date = new Date(value);
		const delta = Date.now() - date.getTime();
		if (Number.isFinite(delta) && delta >= 0 && delta < 24 * 60 * 60 * 1000) {
			return relativeTime(value);
		}
		return formatDateTime(value);
	}
</script>

<div class="overflow-x-auto">
	<table class="w-full table-fixed text-left text-sm max-[720px]:min-w-[760px]">
		<colgroup>
			<col class="w-[9%]" />
			<col class="w-[14%]" />
			<col class="w-[32%]" />
			<col class="w-[5%]" />
			<col class="w-[7%]" />
			<col class="w-[19%]" />
			<col class="w-[14%]" />
		</colgroup>
		<thead class="border-b border-neutral-200 text-xs uppercase text-neutral-500">
			<tr>
				<th class="py-2 pr-3 font-semibold">{t('history.col.code')}</th>
				<th class="py-2 pr-3 font-semibold">{t('history.col.status')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.route')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.hops')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.observed')}</th>
				<th class="px-3 py-2 font-semibold">{t('history.col.elapsed')}</th>
				<th class="px-3 py-2 text-right font-semibold">{t('history.col.date')}</th>
			</tr>
		</thead>
		<tbody class="divide-y divide-neutral-100">
			{#each tests as test, index (test.id)}
				{@const meta = statusMeta(test.status)}
				{@const StatusIcon = meta.icon}
				{@const time = elapsedWithPropagation(test)}
				{@const hops = hopRange(test)}
				{@const mine = isOwnTest(test)}
				<tr
					class={`group cursor-pointer transition-colors ${mine ? 'bg-teal-50/70 hover:bg-teal-100/70' : 'hover:bg-neutral-50'}`}
					onclick={() => goto(resolve('/[id]', { id: test.id }))}
				>
					<td class="mono truncate py-2 pr-3 font-semibold text-neutral-800">{test.code}</td>
					<td class="py-2 pr-3">
						<span
							class={`inline-flex items-center gap-1 rounded px-2 py-1 text-xs font-semibold ${meta.className}`}
						>
							<StatusIcon size={13} />
							{meta.label}
						</span>
					</td>
					<td class="px-3 py-2">
						<div
							class="flex min-w-0 items-center gap-2"
							title={`${test.userPublicKey} ↔ ${test.endpointName}`}
						>
							<KeyRound size={14} class="shrink-0 text-neutral-500" />
							<p class="min-w-0 truncate font-medium text-neutral-950 group-hover:text-teal-800">
								<span class="mono">{test.userPublicKey.slice(0, 8)}</span>
								<span class="mx-1 text-neutral-400">↔</span>
								<span>{test.endpointName}</span>
							</p>
							{#if mine}
								<span
									class="shrink-0 rounded bg-teal-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-teal-800"
								>
									{t('history.mine')}
								</span>
							{/if}
						</div>
					</td>
					<td class="whitespace-nowrap px-3 py-2 text-neutral-600">
						<span class={hops === t('common.pending') || hops === '—' ? 'opacity-50' : ''}
							>{hops}</span
						>
					</td>
					<td class="truncate px-3 py-2 text-neutral-600">
						{test.observationCount ?? test.observations.length}
					</td>
					<td class="truncate px-3 py-2 text-neutral-600">
						<span class={time === t('common.pending') || time === '—' ? 'opacity-50' : ''}
							>{time}</span
						>
					</td>
					<td class="truncate whitespace-nowrap px-3 py-2 text-right text-neutral-600">
						<span title={formatDateTime(test.createdAt)}>{historyDate(test.createdAt)}</span>
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
