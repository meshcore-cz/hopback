<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import {
		AlertCircle,
		ArrowLeft,
		CheckCircle2,
		ChevronLeft,
		ChevronRight,
		CircleDashed,
		Clock3,
		KeyRound,
		RotateCw
	} from '@lucide/svelte';
	import type { DiagnosticTest, TestStatus } from '$lib/types';
	import { t } from '$lib/i18n/index.svelte';
	import LanguageSwitcher from '$lib/i18n/LanguageSwitcher.svelte';

	const pageSize = 15;

	let tests = $state<DiagnosticTest[]>([]);
	let total = $state(0);
	let loading = $state(false);
	let error = $state('');
	let currentPage = $state(1);

	let totalPages = $derived(Math.max(1, Math.ceil(total / pageSize)));
	let offset = $derived((currentPage - 1) * pageSize);

	onMount(() => {
		currentPage = parsePage(page.url.searchParams.get('page'));
		void loadTests();
	});

	async function loadTests() {
		loading = true;
		error = '';
		try {
			const stored = localStorage.getItem('hopback.testIds');
			const ids = stored ? (JSON.parse(stored) as string[]) : [];
			if (!Array.isArray(ids) || !ids.length) {
				tests = [];
				total = 0;
				return;
			}
			total = ids.length;
			const pageIds = ids.slice(offset, offset + pageSize);
			const response = await fetch('/api/tests/meta', {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({ ids: pageIds })
			});
			const payload = (await response.json()) as {
				tests?: DiagnosticTest[];
				message?: string;
			};
			if (!response.ok) throw new Error(payload.message || t('tests.couldNotLoad'));
			tests = payload.tests || [];
		} catch (err) {
			error = err instanceof Error ? err.message : t('tests.couldNotLoad');
		} finally {
			loading = false;
		}
	}

	async function goToPage(nextPage: number) {
		currentPage = Math.min(totalPages, Math.max(1, nextPage));
		await goto(`${resolve('/tests')}?page=${currentPage}`, { replaceState: true, noScroll: true });
		await loadTests();
	}

	function parsePage(value: string | null) {
		const parsed = Number(value);
		if (!Number.isFinite(parsed) || parsed < 1) return 1;
		return Math.floor(parsed);
	}

	function statusMeta(current: TestStatus) {
		if (current === 'completed')
			return {
				icon: CheckCircle2,
				label: t('status.completed'),
				className: 'bg-teal-50 text-teal-800'
			};
		if (current === 'failed' || current === 'expired')
			return { icon: AlertCircle, label: t(`status.${current}`), className: 'bg-red-50 text-red-800' };
		return {
			icon: CircleDashed,
			label: t(`status.${current}`),
			className: 'bg-neutral-100 text-neutral-700'
		};
	}

	function totalTime(test: DiagnosticTest) {
		const unfinished = test.status === 'expired' || test.status === 'failed' ? '—' : t('common.pending');
		if (!test.returnSeenAt) return unfinished;
		const start = new Date(test.createdAt).getTime();
		const end = new Date(test.returnSeenAt).getTime();
		if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return unfinished;
		return `${((end - start) / 1000).toFixed(1)} s`;
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

<main class="mx-auto flex min-h-screen w-full max-w-6xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
	<a
		class="inline-flex w-fit items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 py-2 text-sm font-medium text-neutral-800 transition hover:bg-neutral-100"
		href={resolve('/')}
	>
		<ArrowLeft size={16} />
		{t('common.back')}
	</a>

	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<div class="flex items-center gap-2">
				<Clock3 size={18} class="text-neutral-500" />
				<div>
					<h1 class="text-xl font-semibold text-neutral-950">{t('tests.title')}</h1>
					<p class="text-sm text-neutral-500">{t('history.saved', { count: total })}</p>
				</div>
			</div>
			<div class="flex items-center gap-2">
				<LanguageSwitcher />
				<button
					class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 transition hover:bg-neutral-100"
					type="button"
					onclick={loadTests}
					title={t('common.refresh')}
				>
					<RotateCw class={loading ? 'animate-spin' : ''} size={18} />
				</button>
			</div>
		</div>

		{#if error}
			<p class="mb-4 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
				{error}
			</p>
		{/if}

		<div class="overflow-x-auto">
			<table class="w-full min-w-[760px] text-left text-sm">
				<thead class="border-b border-neutral-200 text-xs uppercase text-neutral-500">
					<tr>
						<th class="py-2 pr-3 font-semibold">{t('history.col.status')}</th>
						<th class="px-3 py-2 font-semibold">{t('history.col.endpoint')}</th>
						<th class="px-3 py-2 font-semibold">{t('history.col.userKey')}</th>
						<th class="px-3 py-2 font-semibold">{t('history.col.code')}</th>
						<th class="px-3 py-2 font-semibold">{t('history.col.time')}</th>
						<th class="px-3 py-2 font-semibold">{t('history.col.observed')}</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-neutral-100">
					{#each tests as test (test.id)}
						{@const meta = statusMeta(test.status)}
						{@const StatusIcon = meta.icon}
							{@const time = totalTime(test)}
						<tr
							class="group cursor-pointer transition hover:bg-neutral-50"
							onclick={() => goto(resolve('/[id]', { id: test.id }))}
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
								<p class="font-medium text-neutral-950 group-hover:text-teal-800">
									{test.endpointName}
								</p>
								<p class="truncate text-xs text-neutral-500">{relativeTime(test.createdAt)}</p>
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
							<td colspan="6" class="py-5 text-center text-sm text-neutral-500">
								{loading ? t('tests.loading') : t('tests.empty')}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>

		<div class="mt-4 flex items-center justify-between gap-3">
			<p class="text-sm text-neutral-500">
				{t('tests.page', { current: currentPage, total: totalPages })}
			</p>
			<div class="flex items-center gap-2">
				<button
					class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 transition hover:bg-neutral-100"
					type="button"
					onclick={() => goToPage(currentPage - 1)}
					disabled={currentPage <= 1 || loading}
					title={t('tests.prev')}
				>
					<ChevronLeft size={18} />
				</button>
				<button
					class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 transition hover:bg-neutral-100"
					type="button"
					onclick={() => goToPage(currentPage + 1)}
					disabled={currentPage >= totalPages || loading}
					title={t('tests.next')}
				>
					<ChevronRight size={18} />
				</button>
			</div>
		</div>
	</section>
</main>
