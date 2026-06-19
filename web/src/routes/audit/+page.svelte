<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import {
		ArrowLeft,
		Check,
		ChevronLeft,
		ChevronRight,
		Copy,
		ExternalLink,
		RadioTower,
		RotateCw,
		X
	} from '@lucide/svelte';
	import type { EndpointConfig, OutgoingPacket } from '$lib/types';
	import { apiFetch } from '$lib/client/api';
	import { t } from '$lib/i18n/index.svelte';
	import LanguageSwitcher from '$lib/i18n/LanguageSwitcher.svelte';

	const pageSize = 50;

	let packets = $state<OutgoingPacket[]>([]);
	let endpoints = $state<EndpointConfig[]>([]);
	let total = $state(0);
	let loading = $state(false);
	let error = $state('');
	let currentPage = $state(1);
	let endpointFilter = $state('');
	let analyzerUrl = $state('');
	let modalPacket = $state<OutgoingPacket | null>(null);
	let copied = $state(false);

	let totalPages = $derived(Math.max(1, Math.ceil(total / pageSize)));
	let offset = $derived((currentPage - 1) * pageSize);

	onMount(() => {
		endpointFilter = page.url.searchParams.get('endpoint') ?? '';
		currentPage = parsePage(page.url.searchParams.get('page'));
		void loadPackets();
	});

	async function loadPackets() {
		loading = true;
		error = '';
		try {
			// Transient query builder, not reactive state, so SvelteURLSearchParams is unnecessary.
			// eslint-disable-next-line svelte/prefer-svelte-reactivity
			const query = new URLSearchParams({
				limit: String(pageSize),
				offset: String(offset)
			});
			if (endpointFilter) query.set('endpointId', endpointFilter);
			const response = await apiFetch(`/api/audit?${query}`);
			const payload = (await response.json()) as {
				packets?: OutgoingPacket[];
				total?: number;
				endpoints?: EndpointConfig[];
				analyzerUrl?: string;
				message?: string;
			};
			if (!response.ok) throw new Error(payload.message || t('audit.couldNotLoad'));
			packets = payload.packets || [];
			total = payload.total ?? packets.length;
			endpoints = payload.endpoints || [];
			analyzerUrl = payload.analyzerUrl || '';
		} catch (err) {
			error = err instanceof Error ? err.message : t('audit.couldNotLoad');
		} finally {
			loading = false;
		}
	}

	async function syncUrl() {
		const target = new URL(resolve('/audit'), window.location.origin);
		if (endpointFilter) target.searchParams.set('endpoint', endpointFilter);
		if (currentPage > 1) target.searchParams.set('page', String(currentPage));
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		await goto(target, { replaceState: true, noScroll: true });
	}

	async function goToPage(nextPage: number) {
		currentPage = Math.min(totalPages, Math.max(1, nextPage));
		await syncUrl();
		await loadPackets();
	}

	async function onFilterChange() {
		currentPage = 1;
		await syncUrl();
		await loadPackets();
	}

	function parsePage(value: string | null) {
		const parsed = Number(value);
		if (!Number.isFinite(parsed) || parsed < 1) return 1;
		return Math.floor(parsed);
	}

	function endpointName(id: string) {
		return endpoints.find((ep) => ep.id === id)?.name ?? id;
	}

	function statusBadge(packet: OutgoingPacket) {
		if (packet.ok === true)
			return { label: t('audit.status.ok'), tone: 'bg-emerald-100 text-emerald-800' };
		if (packet.ok === false)
			return { label: t('audit.status.failed'), tone: 'bg-red-100 text-red-800' };
		return { label: t('audit.status.pending'), tone: 'bg-amber-100 text-amber-800' };
	}

	function formatTime(iso: string) {
		const date = new Date(iso);
		return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
	}

	function analyzerPacketUrl(contentHash: string) {
		const base = (analyzerUrl || 'wss://analyzer.meshcore.cz')
			.replace(/^wss:\/\//, 'https://')
			.replace(/^ws:\/\//, 'http://')
			.replace(/\/$/, '');
		return `${base}/#/packets/${contentHash}`;
	}

	function packetToolUrl(rawHex: string) {
		return `https://meshcore-cz.github.io/meshcore-packet-tool/?d_packet=${rawHex}`;
	}

	function openExternal(url: string) {
		window.open(url, '_blank', 'noopener,noreferrer');
	}

	function openModal(packet: OutgoingPacket) {
		modalPacket = packet;
		copied = false;
	}

	function closeModal() {
		modalPacket = null;
	}

	async function copyRaw() {
		if (!modalPacket) return;
		try {
			await navigator.clipboard.writeText(modalPacket.rawHex);
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			copied = false;
		}
	}

	function onKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') closeModal();
	}
</script>

<svelte:window on:keydown={onKeydown} />

<svelte:head>
	<title>Hopback · {t('audit.title')}</title>
</svelte:head>

<main class="mx-auto flex min-h-screen w-full max-w-6xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
	<a
		class="inline-flex w-fit items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 py-2 text-sm font-medium text-neutral-800 transition hover:bg-neutral-100"
		href={resolve('/status')}
	>
		<ArrowLeft size={16} />
		{t('common.back')}
	</a>

	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<div class="flex items-center gap-2">
				<RadioTower size={18} class="text-neutral-500" />
				<div>
					<h1 class="text-xl font-semibold text-neutral-950">{t('audit.title')}</h1>
					<p class="text-sm text-neutral-500">{t('audit.count', { count: total })}</p>
				</div>
			</div>
			<div class="flex items-center gap-2">
				<select
					class="h-10 rounded-md border border-neutral-300 bg-white px-2 text-sm text-neutral-800"
					bind:value={endpointFilter}
					onchange={onFilterChange}
				>
					<option value="">{t('audit.filter.all')}</option>
					{#each endpoints as ep (ep.id)}
						<option value={ep.id}>{ep.name}</option>
					{/each}
				</select>
				<LanguageSwitcher />
				<button
					class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 transition hover:bg-neutral-100"
					type="button"
					onclick={loadPackets}
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
			<table class="w-full min-w-[44rem] border-collapse text-sm">
				<thead>
					<tr
						class="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-500"
					>
						<th class="px-2 py-2 font-medium">{t('audit.col.time')}</th>
						<th class="px-2 py-2 font-medium">{t('audit.col.endpoint')}</th>
						<th class="px-2 py-2 font-medium">{t('audit.col.role')}</th>
						<th class="px-2 py-2 font-medium">{t('audit.col.test')}</th>
						<th class="px-2 py-2 font-medium">{t('audit.col.status')}</th>
						<th class="px-2 py-2 font-medium">{t('audit.col.contentHash')}</th>
						<th class="px-2 py-2 font-medium">{t('audit.col.links')}</th>
					</tr>
				</thead>
				<tbody>
					{#if packets.length === 0}
						<tr>
							<td class="px-2 py-6 text-center text-neutral-500" colspan="7">
								{loading ? '…' : t('audit.empty')}
							</td>
						</tr>
					{:else}
						{#each packets as packet (packet.id)}
							{@const badge = statusBadge(packet)}
							<tr class="border-b border-neutral-100 align-top">
								<td class="whitespace-nowrap px-2 py-2 text-neutral-700"
									>{formatTime(packet.createdAt)}</td
								>
								<td class="px-2 py-2 text-neutral-900">{endpointName(packet.endpointId)}</td>
								<td class="whitespace-nowrap px-2 py-2 font-mono text-xs text-neutral-700"
									>{packet.packetRole}</td
								>
								<td class="px-2 py-2">
									{#if packet.testId}
										<a
											class="font-mono text-teal-700 hover:underline"
											href={resolve('/[id]', { id: packet.testId })}>{packet.testId}</a
										>
									{:else}
										<span class="text-neutral-400">{t('common.na')}</span>
									{/if}
								</td>
								<td class="px-2 py-2">
									<span
										class={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-semibold ${badge.tone}`}
										title={packet.error ?? ''}
									>
										<span class="size-1.5 rounded-full bg-current"></span>
										{badge.label}
									</span>
								</td>
								<td class="px-2 py-2">
									{#if packet.contentHash}
										<button
											class="font-mono text-xs text-teal-700 hover:underline"
											type="button"
											onclick={() => openModal(packet)}
											title={t('audit.modal.title')}>{packet.contentHash}</button
										>
									{:else}
										<button
											class="font-mono text-xs text-neutral-500 hover:text-neutral-900"
											type="button"
											onclick={() => openModal(packet)}>{t('audit.col.contentHash')}…</button
										>
									{/if}
								</td>
								<td class="px-2 py-2">
									<div class="flex items-center gap-3 text-xs">
										{#if packet.contentHash}
											<button
												class="inline-flex items-center gap-1 text-teal-700 hover:underline"
												type="button"
												onclick={() => openExternal(analyzerPacketUrl(packet.contentHash ?? ''))}
											>
												{t('audit.link.analyzer')}
												<ExternalLink size={11} strokeWidth={2} />
											</button>
										{/if}
										<button
											class="inline-flex items-center gap-1 text-teal-700 hover:underline"
											type="button"
											onclick={() => openExternal(packetToolUrl(packet.rawHex))}
										>
											{t('audit.link.packetTool')}
											<ExternalLink size={11} strokeWidth={2} />
										</button>
									</div>
								</td>
							</tr>
						{/each}
					{/if}
				</tbody>
			</table>
		</div>

		<div class="mt-4 flex items-center justify-between gap-3">
			<p class="text-sm text-neutral-500">
				{t('audit.page', { current: currentPage, total: totalPages })}
			</p>
			<div class="flex items-center gap-2">
				<button
					class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 transition hover:bg-neutral-100"
					type="button"
					onclick={() => goToPage(currentPage - 1)}
					disabled={currentPage <= 1 || loading}
					title={t('audit.prev')}
				>
					<ChevronLeft size={18} />
				</button>
				<button
					class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 transition hover:bg-neutral-100"
					type="button"
					onclick={() => goToPage(currentPage + 1)}
					disabled={currentPage >= totalPages || loading}
					title={t('audit.next')}
				>
					<ChevronRight size={18} />
				</button>
			</div>
		</div>
	</section>
</main>

{#if modalPacket}
	<!-- Backdrop click (on the backdrop itself, not the dialog) closes the modal. -->
	<div
		class="fixed inset-0 z-50 flex items-center justify-center bg-neutral-950/50 p-4"
		role="presentation"
		onclick={(event) => event.target === event.currentTarget && closeModal()}
	>
		<div
			class="w-full max-w-2xl rounded-lg border border-neutral-300 bg-white shadow-xl"
			role="dialog"
			aria-modal="true"
			aria-label={t('audit.modal.title')}
			tabindex="-1"
		>
			<div class="flex items-center justify-between border-b border-neutral-200 px-4 py-3">
				<h2 class="text-sm font-semibold text-neutral-950">{t('audit.modal.title')}</h2>
				<button
					class="inline-flex size-8 items-center justify-center rounded-md text-neutral-500 transition hover:bg-neutral-100 hover:text-neutral-900"
					type="button"
					onclick={closeModal}
					title={t('audit.modal.close')}
				>
					<X size={18} />
				</button>
			</div>
			<div class="flex flex-col gap-3 p-4">
				<dl class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
					<div>
						<dt class="text-neutral-500">{t('audit.col.role')}</dt>
						<dd class="font-mono text-neutral-900">{modalPacket.packetRole}</dd>
					</div>
					<div>
						<dt class="text-neutral-500">{t('audit.col.endpoint')}</dt>
						<dd class="text-neutral-900">{endpointName(modalPacket.endpointId)}</dd>
					</div>
					{#if modalPacket.contentHash}
						<div class="col-span-2">
							<dt class="text-neutral-500">{t('audit.col.contentHash')}</dt>
							<dd class="break-all font-mono text-neutral-900">{modalPacket.contentHash}</dd>
						</div>
					{/if}
				</dl>
				<code
					class="block max-h-64 overflow-y-auto break-all rounded-md border border-neutral-200 bg-neutral-50 p-3 font-mono text-xs text-neutral-700"
					>{modalPacket.rawHex}</code
				>
				<div class="flex items-center gap-3">
					<button
						class="inline-flex items-center gap-1.5 rounded-md border border-neutral-300 bg-white px-3 py-1.5 text-xs font-medium text-neutral-800 transition hover:bg-neutral-100"
						type="button"
						onclick={copyRaw}
					>
						{#if copied}
							<Check size={14} />
							{t('audit.modal.copied')}
						{:else}
							<Copy size={14} />
							{t('audit.modal.copy')}
						{/if}
					</button>
					<button
						class="inline-flex items-center gap-1.5 rounded-md border border-neutral-300 bg-white px-3 py-1.5 text-xs font-medium text-teal-700 transition hover:bg-neutral-100"
						type="button"
						onclick={() => modalPacket && openExternal(packetToolUrl(modalPacket.rawHex))}
					>
						{t('audit.link.packetTool')}
						<ExternalLink size={12} strokeWidth={2} />
					</button>
					{#if modalPacket.contentHash}
						<button
							class="inline-flex items-center gap-1.5 rounded-md border border-neutral-300 bg-white px-3 py-1.5 text-xs font-medium text-teal-700 transition hover:bg-neutral-100"
							type="button"
							onclick={() =>
								modalPacket?.contentHash &&
								openExternal(analyzerPacketUrl(modalPacket.contentHash))}
						>
							{t('audit.link.analyzer')}
							<ExternalLink size={12} strokeWidth={2} />
						</button>
					{/if}
				</div>
			</div>
		</div>
	</div>
{/if}
