<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { fade, fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import {
		AlertCircle,
		ArrowRight,
		CheckCircle2,
		Clock3,
		LoaderCircle,
		MapPin,
		Plus,
		Radio,
		RotateCw,
		Search,
		X
	} from '@lucide/svelte';
	import type { DiagnosticTest, EndpointConfig, NodeRecord, RuntimeStatus } from '$lib/types';
	import { apiFetch } from '$lib/client/api';
	import { t } from '$lib/i18n/index.svelte';
	import LanguageSwitcher from '$lib/i18n/LanguageSwitcher.svelte';
	import TestHistoryTable from '$lib/TestHistoryTable.svelte';

	interface EndpointOption {
		id: string;
		name: string;
		region: string;
		publicKey: string;
		host?: string;
		location?: EndpointConfig['location'];
	}

	type PublicKeyMode = 'manual' | 'node';
	type StatusEndpoint = RuntimeStatus['endpoints'][number];

	let userPublicKey = $state('');
	let publicKeyMode = $state<PublicKeyMode>('manual');
	let nodeQuery = $state('');
	let nodeResults = $state<NodeRecord[]>([]);
	let selectedUserNode = $state<NodeRecord | null>(null);
	let nodeBusy = $state(false);
	let nodeSearchError = $state('');
	let nodeSearchTimer: ReturnType<typeof setTimeout> | null = null;
	let nodeSearchSeq = 0;
	let selectedEndpoint = $state('');
	let endpointsLoaded = $state(false);
	let history = $state<DiagnosticTest[]>([]);
	let ownTestIds = $state<string[]>([]);
	let historyTotal = $state(0);
	let historyLoading = $state(false);
	let historyLoaded = $state(false);
	let status = $state<RuntimeStatus | null>(null);
	let busy = $state(false);
	let error = $state('');
	let mounted = $state(false);
	let statusPoller: ReturnType<typeof setInterval> | null = null;

	let endpointOptions: EndpointOption[] = $derived(status?.endpoints ?? []);
	let selectedEndpointDetails = $derived(
		endpointOptions.find((endpoint) => endpoint.id === selectedEndpoint)
	);
	let selectedEndpointStatus = $derived(endpointAgentStatus(selectedEndpoint));
	let selectedEndpointUnavailable = $derived(
		Boolean(selectedEndpoint && (!status || !selectedEndpointStatus?.ready))
	);
	let normalizedPublicKey = $derived(userPublicKey.trim().toLowerCase());
	let publicKeyIsValid = $derived(/^[0-9a-f]{64}$/.test(normalizedPublicKey));
	let publicKeyError = $derived(
		userPublicKey.trim() && !publicKeyIsValid ? t('home.userKey.invalid') : ''
	);
	let statusStats = $derived([
		{
			id: 'endpoints',
			label: t('stats.endpoints'),
			value: status ? `${readyEndpointCount()}/${status.endpoints.length}` : ''
		},
		{
			id: 'analyzers',
			label: t('stats.analyzers'),
			value: status
				? `${status.analyzers.filter((item) => item.state === 'open').length}/${status.analyzers.length}`
				: ''
		},
		{
			id: 'observers',
			label: t('stats.observers'),
			value: status ? String(status.activeObservers ?? status.observers ?? 0) : ''
		},
		{ id: 'nodes', label: t('stats.nodes'), value: status ? String(status.nodes) : '' }
	]);

	onMount(() => {
		mounted = true;
		userPublicKey = localStorage.getItem('hopback.userPublicKey') || '';
		ownTestIds = loadLocalTestIds();
		void refresh();
		statusPoller = setInterval(() => void loadStatus(), 5000);
		return () => {
			if (statusPoller) clearInterval(statusPoller);
			if (nodeSearchTimer) clearTimeout(nodeSearchTimer);
		};
	});

	$effect(() => {
		if (!mounted) return;
		localStorage.setItem('hopback.userPublicKey', normalizedPublicKey);
	});

	function loadLocalTestIds() {
		try {
			const stored = localStorage.getItem('hopback.testIds');
			if (stored) {
				const parsed = JSON.parse(stored) as string[];
				if (Array.isArray(parsed)) {
					return parsed.filter((id) => typeof id === 'string' && id).slice(0, 200);
				}
			}
		} catch {
			return [];
		}
		return [];
	}

	async function refresh() {
		await Promise.all([loadStatus(), loadHistory()]);
	}

	async function loadStatus() {
		const response = await apiFetch('/api/status');
		status = await response.json();
		if (!selectedEndpoint && status?.endpoints[0]) selectedEndpoint = status.endpoints[0].id;
		endpointsLoaded = true;
	}

	async function loadHistory() {
		historyLoading = true;
		try {
			ownTestIds = loadLocalTestIds();
			const query = new URLSearchParams({ limit: '15', offset: '0' });
			if (ownTestIds.length) query.set('ids', ownTestIds.join(','));
			const response = await apiFetch(`/api/tests?${query}`);
			const payload = (await response.json()) as { tests?: DiagnosticTest[]; total?: number };
			history = payload.tests || [];
			historyTotal = payload.total ?? history.length;
		} catch {
			history = [];
			historyTotal = 0;
		} finally {
			historyLoading = false;
			historyLoaded = true;
		}
	}

	function setPublicKeyMode(mode: PublicKeyMode) {
		publicKeyMode = mode;
		error = '';
		if (mode === 'node' && !nodeResults.length) void searchNodes();
	}

	function updateManualPublicKey(value: string) {
		userPublicKey = value;
		selectedUserNode = null;
	}

	function scheduleNodeSearch(value: string) {
		nodeQuery = value;
		selectedUserNode = null;
		if (nodeSearchTimer) clearTimeout(nodeSearchTimer);
		nodeSearchTimer = setTimeout(() => void searchNodes(), 180);
	}

	async function searchNodes() {
		const searchId = ++nodeSearchSeq;
		nodeBusy = true;
		nodeSearchError = '';
		try {
			const response = await apiFetch(
				`/api/nodes?q=${encodeURIComponent(nodeQuery.trim())}&limit=8&recentDays=30`
			);
			const payload = (await response.json()) as { nodes?: NodeRecord[]; message?: string };
			if (!response.ok) throw new Error(payload.message || 'Could not search nodes.');
			if (searchId !== nodeSearchSeq) return;
			nodeResults = (payload.nodes || []).filter((node) => /^[0-9a-f]{64}$/i.test(node.publicKey));
		} catch (err) {
			if (searchId !== nodeSearchSeq) return;
			nodeSearchError = err instanceof Error ? err.message : 'Could not search nodes.';
			nodeResults = [];
		} finally {
			if (searchId === nodeSearchSeq) nodeBusy = false;
		}
	}

	function selectUserNode(node: NodeRecord) {
		selectedUserNode = node;
		userPublicKey = node.publicKey.toLowerCase();
		nodeQuery = node.name;
	}

	function clearUserNode() {
		selectedUserNode = null;
		userPublicKey = '';
		nodeQuery = '';
		nodeResults = [];
	}

	async function createTest(event: SubmitEvent) {
		event.preventDefault();
		error = '';
		const endpoint = endpointOptions.find((option) => option.id === selectedEndpoint);
		if (!endpoint) {
			error = t('home.error.chooseEndpoint');
			return;
		}
		if (!endpointAgentStatus(endpoint.id)?.ready) {
			error = t('home.error.endpointOffline', { name: endpoint.name });
			return;
		}
		if (!publicKeyIsValid) {
			error = t('home.error.invalidKey');
			return;
		}

		busy = true;
		const response = await apiFetch('/api/tests', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({
				userPublicKey: normalizedPublicKey,
				endpointId: endpoint.id
			})
		});
		const payload = await response.json();
		busy = false;

		if (!response.ok) {
			error = payload.message || t('home.error.couldNotStart');
			return;
		}

		await goto(resolve('/[id]', { id: payload.test.id }));
	}

	function relativeTime(value?: string | null) {
		if (!value) return t('common.pending');
		const date = new Date(value);
		const now = Date.now();
		const delta = Math.floor((now - date.getTime()) / 1000);
		if (!Number.isFinite(delta)) return t('common.pending');
		if (delta < 0) return t('time.justNow');
		if (delta < 60) return t('time.secondsAgo', { n: delta });
		if (delta < 3600) return t('time.minutesAgo', { n: Math.floor(delta / 60) });
		if (delta < 86400) return t('time.hoursAgo', { n: Math.floor(delta / 3600) });
		if (delta < 604800) return t('time.daysAgo', { n: Math.floor(delta / 86400) });
		return t('time.weeksAgo', { n: Math.floor(delta / 604800) });
	}

	function endpointAgentStatus(endpointId: string): StatusEndpoint | undefined {
		return status?.endpoints.find((endpoint) => endpoint.id === endpointId);
	}

	function readyEndpointCount() {
		return status?.endpoints.filter((endpoint) => endpoint.ready).length ?? 0;
	}

	// Compact online indicator for the endpoint card (label + colour tone).
	function endpointOnline(endpointId: string) {
		const current = endpointAgentStatus(endpointId);
		if (!status || !current)
			return { label: t('home.agent.short.checking'), tone: 'bg-neutral-100 text-neutral-500' };
		if (current.ready)
			return { label: t('home.agent.short.online'), tone: 'bg-teal-100 text-teal-800' };
		if (current.connected)
			return { label: t('home.agent.short.connecting'), tone: 'bg-amber-100 text-amber-800' };
		return { label: t('home.agent.short.offline'), tone: 'bg-red-100 text-red-800' };
	}
</script>

<main class="mx-auto flex min-h-screen w-full max-w-5xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
	<header
		class="flex flex-col gap-4 border-b border-neutral-300/80 pb-5 lg:flex-row lg:items-end lg:justify-between"
	>
		<div class="flex items-center gap-3">
			<div class="grid size-11 place-items-center rounded-md bg-neutral-950 text-white">
				<Radio size={24} />
			</div>
			<div>
				<p class="text-sm font-semibold uppercase tracking-wide text-teal-700">
					{t('header.tagline')}
				</p>
				<h1 class="text-3xl font-semibold tracking-normal text-neutral-950">Hopback</h1>
			</div>
		</div>

		<div class="flex flex-col items-stretch gap-3 sm:items-end">
			<div class="flex justify-end">
				<LanguageSwitcher />
			</div>
			<div class="grid grid-cols-4 gap-2 text-sm">
				{#if status}
					{#each statusStats as stat, index (stat.id)}
						<div
							class="rounded-md border border-neutral-300 bg-white/75 px-3 py-2"
							in:fly={{ y: 8, duration: 350, delay: index * 70, easing: cubicOut }}
						>
							<p class="text-neutral-500">{stat.label}</p>
							<p class="font-semibold text-neutral-950">{stat.value}</p>
						</div>
					{/each}
				{:else}
					{#each statusStats as stat (stat.id)}
						<div class="rounded-md border border-neutral-200 bg-white/75 px-3 py-2">
							<p class="text-neutral-500">{stat.label}</p>
							<div class="mt-1 h-4 w-8 animate-pulse rounded bg-neutral-200"></div>
						</div>
					{/each}
				{/if}
			</div>
		</div>
	</header>

	<form
		class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm sm:p-5"
		onsubmit={createTest}
	>
		<div class="mb-5 flex items-center justify-between gap-3">
			<div>
				<h2 class="text-xl font-semibold text-neutral-950">{t('home.newTest.title')}</h2>
				<p class="text-sm text-neutral-500">
					{t('home.newTest.subtitle')}
				</p>
			</div>
			<button
				class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 hover:bg-neutral-100"
				type="button"
				onclick={refresh}
				title={t('common.refresh')}
			>
				<RotateCw size={18} />
			</button>
		</div>

		<div>
			<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
				<label class="block text-sm font-medium text-neutral-700" for="public-key"
					>{t('home.userKey.label')}</label
				>
				<div
					class="grid grid-cols-2 rounded-md border border-neutral-300 bg-neutral-100 p-1 text-sm"
				>
					<button
						class={`rounded px-3 py-1.5 font-medium transition ${publicKeyMode === 'manual' ? 'bg-white text-neutral-950 shadow-sm' : 'text-neutral-600 hover:text-neutral-950'}`}
						type="button"
						onclick={() => setPublicKeyMode('manual')}
					>
						{t('home.mode.manual')}
					</button>
					<button
						class={`rounded px-3 py-1.5 font-medium transition ${publicKeyMode === 'node' ? 'bg-white text-neutral-950 shadow-sm' : 'text-neutral-600 hover:text-neutral-950'}`}
						type="button"
						onclick={() => setPublicKeyMode('node')}
					>
						{t('home.mode.node')}
					</button>
				</div>
			</div>

			{#if publicKeyMode === 'manual'}
				<div class="relative mt-2">
					<input
						id="public-key"
						class={`mono w-full rounded-md border bg-neutral-50 px-3 py-3 pr-10 text-sm outline-none ring-teal-600/20 focus:ring-4 ${publicKeyError ? 'border-red-400 focus:border-red-500 focus:ring-red-500/10' : 'border-neutral-300 focus:border-teal-700'}`}
						value={userPublicKey}
						oninput={(event) =>
							updateManualPublicKey((event.currentTarget as HTMLInputElement).value)}
						placeholder={t('home.userKey.placeholder')}
						autocomplete="off"
						spellcheck="false"
						aria-invalid={Boolean(publicKeyError)}
					/>
					{#if publicKeyIsValid}
						<CheckCircle2
							class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-teal-700"
							size={18}
						/>
					{:else if userPublicKey.trim()}
						<AlertCircle
							class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-red-600"
							size={18}
						/>
					{/if}
				</div>
			{:else}
				<div class="mt-2">
					<div class="relative">
						<Search
							class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-neutral-400"
							size={18}
						/>
						<input
							class="w-full rounded-md border border-neutral-300 bg-neutral-50 px-10 py-3 text-sm outline-none ring-teal-600/20 transition focus:border-teal-700 focus:ring-4"
							value={nodeQuery}
							oninput={(event) =>
								scheduleNodeSearch((event.currentTarget as HTMLInputElement).value)}
							onfocus={() => {
								if (!nodeResults.length) void searchNodes();
							}}
							placeholder={t('home.node.placeholder')}
							autocomplete="off"
							spellcheck="false"
						/>
						{#if selectedUserNode || nodeQuery}
							<button
								class="absolute right-2 top-1/2 grid size-8 -translate-y-1/2 place-items-center rounded-md text-neutral-500 transition hover:bg-neutral-200 hover:text-neutral-900"
								type="button"
								onclick={clearUserNode}
								title={t('home.node.clear')}
							>
								<X size={16} />
							</button>
						{/if}
					</div>

					{#if selectedUserNode}
						<div class="mt-2 rounded-md border border-teal-200 bg-teal-50 px-3 py-2">
							<p class="truncate text-sm font-semibold text-teal-950">{selectedUserNode.name}</p>
							<p class="mono mt-0.5 truncate text-xs text-teal-800">
								{selectedUserNode.publicKey.slice(0, 16)} · {relativeTime(
									selectedUserNode.updatedAt
								)}
							</p>
						</div>
					{:else}
						<div class="mt-2 overflow-hidden rounded-md border border-neutral-200 bg-white">
							{#each nodeResults as node (node.publicKey)}
								<button
									class="block w-full border-b border-neutral-100 px-3 py-2 text-left transition last:border-b-0 hover:bg-teal-50"
									type="button"
									onclick={() => selectUserNode(node)}
								>
									<span class="block truncate text-sm font-semibold text-neutral-950">
										{node.name}
									</span>
									<span class="mono mt-0.5 block truncate text-xs text-neutral-500">
										{node.publicKey.slice(0, 16)} · {relativeTime(node.updatedAt)}
									</span>
								</button>
							{:else}
								<p class="px-3 py-3 text-sm text-neutral-500">
									{nodeBusy ? t('home.node.searching') : t('home.node.empty')}
								</p>
							{/each}
						</div>
					{/if}

					{#if nodeSearchError}
						<p class="mt-2 text-sm text-red-700">{nodeSearchError}</p>
					{/if}
				</div>
			{/if}

			{#if publicKeyError}
				<p class="mt-2 text-sm text-red-700">{publicKeyError}</p>
			{/if}
		</div>

		<div class="mt-5">
			<p class="block text-sm font-medium text-neutral-700">{t('home.endpoint.label')}</p>
			{#if status && !readyEndpointCount()}
				<div class="mt-2 rounded-md border border-red-200 bg-red-50 px-3 py-3 text-sm text-red-800">
					<div class="flex gap-2">
						<AlertCircle size={18} class="mt-0.5 shrink-0" />
						<div>
							<p class="font-semibold">{t('home.endpoint.noneReady.title')}</p>
							<p class="mt-0.5 text-red-700">
								{t('home.endpoint.noneReady.desc')}
							</p>
						</div>
					</div>
				</div>
			{/if}
			<div class="mt-2 grid gap-2">
				{#each endpointOptions as endpoint, index (endpoint.id)}
					{@const agent = endpointAgentStatus(endpoint.id)}
					{@const disabled = Boolean(status && !agent?.ready)}
					{@const online = endpointOnline(endpoint.id)}
					<button
						class={`rounded-md border px-3 py-3 text-left transition ${disabled ? 'cursor-not-allowed border-neutral-200 bg-neutral-100 opacity-65' : selectedEndpoint === endpoint.id ? 'border-teal-700 bg-teal-50 ring-4 ring-teal-600/10' : 'border-neutral-300 bg-neutral-50 hover:border-neutral-500'}`}
						type="button"
						in:fly={{ y: 8, duration: 300, delay: index * 60, easing: cubicOut }}
						{disabled}
						onclick={() => {
							if (!disabled) selectedEndpoint = endpoint.id;
						}}
					>
						<span class="flex items-start justify-between gap-3">
							<span class="min-w-0">
								<span class="block truncate font-semibold text-neutral-950">{endpoint.name}</span>
								<span class="mt-1 flex items-center gap-1 text-sm text-neutral-600">
									<MapPin size={15} />
									<span class="truncate">{endpoint.location?.label || endpoint.region}</span>
								</span>
							</span>
							<span
								class={`inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-semibold ${online.tone}`}
							>
								<span class="size-1.5 rounded-full bg-current"></span>
								{online.label}
							</span>
						</span>
					</button>
				{:else}
					{#if !endpointsLoaded}
						{#each [0, 1] as placeholder (placeholder)}
							<div class="rounded-md border border-neutral-200 bg-neutral-50 px-3 py-3">
								<div class="flex items-start justify-between gap-3">
									<div class="min-w-0 flex-1 space-y-2">
										<div class="h-4 w-32 animate-pulse rounded bg-neutral-200"></div>
										<div class="h-3 w-48 animate-pulse rounded bg-neutral-200"></div>
										<div class="h-5 w-24 animate-pulse rounded bg-neutral-200"></div>
									</div>
									<div class="h-5 w-12 animate-pulse rounded bg-neutral-200"></div>
								</div>
							</div>
						{/each}
					{:else}
						<p
							class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900"
							in:fade={{ duration: 200 }}
						>
							{t('home.endpoint.none')}
						</p>
					{/if}
				{/each}
			</div>
			{#if status && selectedEndpointUnavailable && selectedEndpointDetails}
				<p class="mt-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
					{t('home.endpoint.disabled', { name: selectedEndpointDetails.name })}
				</p>
			{/if}
		</div>

		{#if error}
			<p class="mt-4 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
				{error}
			</p>
		{/if}

		<button
			class="group mt-5 inline-flex w-full items-center justify-center gap-2 rounded-md bg-neutral-950 px-4 py-3 font-semibold text-white shadow-sm transition-all duration-200 hover:-translate-y-0.5 hover:bg-neutral-800 hover:shadow-lg hover:shadow-neutral-900/15 active:translate-y-0 active:shadow-sm disabled:translate-y-0 disabled:cursor-not-allowed disabled:opacity-60 disabled:shadow-none disabled:hover:bg-neutral-950"
			disabled={busy || !selectedEndpoint || !publicKeyIsValid || selectedEndpointUnavailable}
		>
			{#if busy}
				<LoaderCircle class="animate-spin" size={18} />
			{:else}
				<Plus size={18} class="transition-transform duration-200 group-hover:rotate-90" />
			{/if}
			{t('home.start')}
		</button>
	</form>

	{#if historyLoaded && history.length > 0}
		<section
			class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm"
			in:fade={{ duration: 250 }}
		>
			<div class="mb-3 flex items-center justify-between gap-3">
				<div class="flex items-center gap-2">
					<Clock3 size={18} class="text-neutral-500" />
					<h2 class="text-base font-semibold text-neutral-900">{t('history.title')}</h2>
				</div>
				<span class="text-xs text-neutral-500">{t('history.visible', { count: historyTotal })}</span
				>
			</div>

			<TestHistoryTable
				tests={history}
				{ownTestIds}
				loading={historyLoading}
				emptyText={t('tests.empty')}
			/>
			{#if historyLoading}
				<p class="mt-3 text-sm text-neutral-500">{t('history.refreshing')}</p>
			{/if}
			{#if historyTotal > 15}
				<div class="mt-4 flex justify-end">
					<a
						class="inline-flex items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 py-2 text-sm font-medium text-neutral-800 transition hover:border-teal-700 hover:bg-teal-50 hover:text-teal-900"
						href={resolve('/tests')}
					>
						{t('history.showAll')}
						<ArrowRight size={16} />
					</a>
				</div>
			{/if}
		</section>
	{/if}

	<footer class="pb-3 text-center text-sm text-neutral-500">
		{t('footer.credit')}
		<a
			class="font-medium text-neutral-700 hover:text-teal-800"
			href="https://github.com/meshcore-cz/hopback"
		>
			meshcore-cz/hopback
		</a>
	</footer>
</main>
