<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { Activity, ArrowLeft, Cpu, Radio, RadioTower, Wifi } from '@lucide/svelte';
	import type { RuntimeStatus } from '$lib/types';
	import { apiFetch } from '$lib/client/api';
	import { t, locale } from '$lib/i18n/index.svelte';
	import LanguageSwitcher from '$lib/i18n/LanguageSwitcher.svelte';
	import { relativeTime, uptime } from '$lib/time';

	type StatusAgent = RuntimeStatus['agents'][number];

	let status = $state<RuntimeStatus | null>(null);
	let error = $state('');
	let poller: ReturnType<typeof setInterval> | null = null;

	async function loadStatus() {
		try {
			const res = await apiFetch('/api/status');
			if (!res.ok) throw new Error(`status ${res.status}`);
			status = (await res.json()) as RuntimeStatus;
			error = '';
		} catch {
			error = t('opstatus.couldNotLoad');
		}
	}

	onMount(() => {
		loadStatus();
		poller = setInterval(loadStatus, 5000);
		return () => {
			if (poller) clearInterval(poller);
		};
	});

	async function goToAudit(endpointId: string) {
		const target = new URL(resolve('/audit'), window.location.origin);
		target.searchParams.set('endpoint', endpointId);
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		await goto(target);
	}

	function agentForEndpoint(endpointId: string): StatusAgent | undefined {
		return status?.agents.find((agent) => agent.endpointId === endpointId);
	}

	function endpointState(ep: RuntimeStatus['endpoints'][number]) {
		if (!ep.connected)
			return { label: t('opstatus.agent.offline'), tone: 'bg-red-100 text-red-800' };
		if (!ep.ipcReady)
			return { label: t('opstatus.agent.ipcNotReady'), tone: 'bg-amber-100 text-amber-800' };
		return { label: t('opstatus.agent.online'), tone: 'bg-emerald-100 text-emerald-800' };
	}

	function analyzerTone(state: string) {
		if (state === 'open') return 'bg-emerald-100 text-emerald-800';
		if (state === 'connecting') return 'bg-amber-100 text-amber-800';
		return 'bg-red-100 text-red-800';
	}

	let summary = $derived([
		{
			id: 'endpoints',
			icon: Radio,
			label: t('opstatus.stat.endpointsOnline'),
			value: status
				? `${status.endpoints.filter((e) => e.ready).length}/${status.endpoints.length}`
				: '—'
		},
		{
			id: 'analyzers',
			icon: Wifi,
			label: t('opstatus.stat.analyzers'),
			value: status
				? `${status.analyzers.filter((a) => a.state === 'open').length}/${status.analyzers.length}`
				: '—'
		},
		{
			id: 'observers',
			icon: Activity,
			label: t('opstatus.stat.observers'),
			value: status ? String(status.activeObservers ?? 0) : '—'
		},
		{
			id: 'nodes',
			icon: Cpu,
			label: t('opstatus.stat.nodes'),
			value: status ? status.nodes.toLocaleString() : '—'
		},
		{
			id: 'activeTests',
			icon: Activity,
			label: t('opstatus.stat.activeTests'),
			value: status ? String(status.activeTests ?? 0) : '—'
		}
	]);
</script>

<svelte:head>
	<title>Hopback · {t('opstatus.title')}</title>
</svelte:head>

<main class="mx-auto flex min-h-screen w-full max-w-6xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
	<header
		class="flex flex-col gap-4 border-b border-neutral-300/80 pb-5 lg:flex-row lg:items-end lg:justify-between"
	>
		<div class="flex items-center gap-3">
			<div class="grid size-11 place-items-center rounded-md bg-neutral-950 text-white">
				<Activity size={24} />
			</div>
			<div>
				<p class="text-sm font-semibold uppercase tracking-wide text-teal-700">
					{t('header.tagline')}
				</p>
				<h1 class="flex items-center gap-2 text-3xl font-semibold tracking-normal text-neutral-950">
					<span>{t('opstatus.title')}</span>
					{#if status?.network?.flag}
						<span title={status.network.name}>{status.network.flag}</span>
					{/if}
				</h1>
			</div>
		</div>
		<div class="flex flex-col items-stretch gap-3 sm:items-end">
			<div class="flex items-center justify-end gap-2">
				<a
					class="inline-flex items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 py-2 text-sm font-medium text-neutral-800 transition hover:bg-neutral-100"
					href={resolve('/')}
				>
					<ArrowLeft size={16} />
					{t('common.back')}
				</a>
				<a
					class="inline-flex items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 py-2 text-sm font-medium text-neutral-800 transition hover:bg-neutral-100"
					href={resolve('/audit')}
				>
					<RadioTower size={16} />
					{t('audit.link')}
				</a>
				<LanguageSwitcher />
			</div>
			<p class="text-sm text-neutral-500">{t('opstatus.subtitle')}</p>
		</div>
	</header>

	{#if error}
		<p class="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">{error}</p>
	{/if}

	<section class="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
		{#each summary as stat, index (stat.id)}
			<div
				class="min-w-0 rounded-md border border-neutral-300 bg-white px-3 py-3"
				in:fly={{ y: 8, duration: 350, delay: index * 60, easing: cubicOut }}
			>
				<div class="flex items-center gap-1.5 text-neutral-500">
					<stat.icon size={14} class="shrink-0" />
					<p class="truncate text-xs">{stat.label}</p>
				</div>
				<p class="mt-1 text-xl font-semibold text-neutral-950">{stat.value}</p>
			</div>
		{/each}
	</section>

	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm sm:p-5">
		<h2 class="mb-4 text-xl font-semibold text-neutral-950">{t('opstatus.endpoints.title')}</h2>
		{#if status && status.endpoints.length === 0}
			<p class="text-sm text-neutral-500">{t('opstatus.endpoints.empty')}</p>
		{:else if !status}
			<div class="space-y-2">
				{#each [0, 1] as placeholder (placeholder)}
					<div class="h-20 animate-pulse rounded-md bg-neutral-100"></div>
				{/each}
			</div>
		{:else}
			<ul class="flex flex-col gap-3">
				{#each status.endpoints as ep, index (ep.id)}
					{@const agent = agentForEndpoint(ep.id)}
					{@const state = endpointState(ep)}
					<li
						class="rounded-md border border-neutral-200 bg-neutral-50/60 p-4"
						in:fly={{ y: 8, duration: 300, delay: index * 50, easing: cubicOut }}
					>
						<div class="flex flex-wrap items-start justify-between gap-3">
							<div class="min-w-0">
								<p class="font-semibold text-neutral-950">{ep.name}</p>
								<p class="text-sm text-neutral-500">{ep.region}</p>
								<p class="mt-1 break-all font-mono text-xs text-neutral-400">{ep.publicKey}</p>
							</div>
							<div class="flex shrink-0 items-center gap-2">
								<button
									class="inline-flex items-center gap-1.5 rounded-full border border-neutral-300 px-2.5 py-1 text-xs font-medium text-neutral-700 transition hover:bg-neutral-100"
									type="button"
									onclick={() => goToAudit(ep.id)}
								>
									<RadioTower size={12} />
									{t('audit.link')}
									<span class="rounded-full bg-neutral-200 px-1.5 font-semibold text-neutral-700"
										>{ep.outgoingPackets.toLocaleString()}</span
									>
								</button>
								<span
									class={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-semibold ${state.tone}`}
								>
									<span class="size-1.5 rounded-full bg-current"></span>
									{state.label}
								</span>
							</div>
						</div>

						{#if agent}
							<dl
								class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 border-t border-neutral-200 pt-3 text-sm sm:grid-cols-3 lg:grid-cols-6"
							>
								<div>
									<dt class="text-xs text-neutral-500">{t('opstatus.col.version')}</dt>
									<dd class="text-neutral-900">{agent.version || t('common.na')}</dd>
								</div>
								<div>
									<dt class="text-xs text-neutral-500">{t('opstatus.col.platform')}</dt>
									<dd class="font-mono text-neutral-900">{agent.platform || t('common.na')}</dd>
								</div>
								<div>
									<dt class="text-xs text-neutral-500">{t('opstatus.col.uptime')}</dt>
									<dd class="text-neutral-900">{uptime(agent.startedAt)}</dd>
								</div>
								<div>
									<dt class="text-xs text-neutral-500">{t('opstatus.col.packets')}</dt>
									<dd class="text-neutral-900">{(agent.observedPackets ?? 0).toLocaleString()}</dd>
								</div>
								<div>
									<dt class="text-xs text-neutral-500">{t('opstatus.col.lastPacket')}</dt>
									<dd class="text-neutral-900">{relativeTime(agent.lastObservedAt)}</dd>
								</div>
								<div>
									<dt class="text-xs text-neutral-500">{t('opstatus.col.lastSeen')}</dt>
									<dd class="text-neutral-900">{relativeTime(agent.lastSeenAt)}</dd>
								</div>
							</dl>
						{:else}
							<p class="mt-3 border-t border-neutral-200 pt-3 text-sm text-neutral-500">
								{t('opstatus.noAgent')}
							</p>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm sm:p-5">
		<h2 class="mb-4 text-xl font-semibold text-neutral-950">{t('opstatus.analyzers.title')}</h2>
		{#if status && status.analyzers.length === 0}
			<p class="text-sm text-neutral-500">{t('opstatus.analyzers.empty')}</p>
		{:else if status}
			<ul class="flex flex-col gap-2">
				{#each status.analyzers as analyzer (analyzer.url)}
					<li
						class="flex flex-wrap items-center justify-between gap-2 rounded-md border border-neutral-200 bg-neutral-50/60 px-3 py-2"
					>
						<span class="break-all font-mono text-xs text-neutral-700">{analyzer.url}</span>
						<span class="flex items-center gap-2">
							{#if analyzer.lastMessageAt}
								<span class="text-xs text-neutral-400">{relativeTime(analyzer.lastMessageAt)}</span>
							{/if}
							<span
								class={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-semibold ${analyzerTone(analyzer.state)}`}
							>
								<span class="size-1.5 rounded-full bg-current"></span>
								{t(`opstatus.analyzer.${analyzer.state}`)}
							</span>
						</span>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	{#if status?.network?.name && status.network.url}
		<p class="text-center text-sm text-neutral-500">
			{status.network.message?.[locale()] ?? t('network.scope', { name: status.network.name })}
		</p>
	{/if}
</main>
