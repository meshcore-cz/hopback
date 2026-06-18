<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { ArrowLeft, ChevronLeft, ChevronRight, Clock3, RotateCw } from '@lucide/svelte';
	import type { DiagnosticTest } from '$lib/types';
	import { apiFetch } from '$lib/client/api';
	import { t } from '$lib/i18n/index.svelte';
	import LanguageSwitcher from '$lib/i18n/LanguageSwitcher.svelte';
	import TestHistoryTable from '$lib/TestHistoryTable.svelte';

	const pageSize = 100;

	let tests = $state<DiagnosticTest[]>([]);
	let ownTestIds = $state<string[]>([]);
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
			ownTestIds = loadLocalTestIds();
			const query = new URLSearchParams({
				limit: String(pageSize),
				offset: String(offset)
			});
			if (ownTestIds.length) query.set('ids', ownTestIds.join(','));
			const response = await apiFetch(`/api/tests?${query}`);
			const payload = (await response.json()) as {
				tests?: DiagnosticTest[];
				total?: number;
				message?: string;
			};
			if (!response.ok) throw new Error(payload.message || t('tests.couldNotLoad'));
			tests = payload.tests || [];
			total = payload.total ?? tests.length;
		} catch (err) {
			error = err instanceof Error ? err.message : t('tests.couldNotLoad');
		} finally {
			loading = false;
		}
	}

	async function goToPage(nextPage: number) {
		currentPage = Math.min(totalPages, Math.max(1, nextPage));
		const target = new URL(resolve('/tests'), window.location.origin);
		target.searchParams.set('page', String(currentPage));
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		await goto(target, { replaceState: true, noScroll: true });
		await loadTests();
	}

	function parsePage(value: string | null) {
		const parsed = Number(value);
		if (!Number.isFinite(parsed) || parsed < 1) return 1;
		return Math.floor(parsed);
	}

	function loadLocalTestIds() {
		try {
			const stored = localStorage.getItem('hopback.testIds');
			const parsed = stored ? (JSON.parse(stored) as string[]) : [];
			return Array.isArray(parsed)
				? parsed.filter((id) => typeof id === 'string' && id).slice(0, 200)
				: [];
		} catch {
			return [];
		}
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
					<p class="text-sm text-neutral-500">{t('history.visible', { count: total })}</p>
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

		<TestHistoryTable {tests} {ownTestIds} {loading} emptyText={t('tests.empty')} />

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
