<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { onMount } from 'svelte';
	import {
		AlertCircle,
		ArrowRight,
		CheckCircle2,
		CircleDashed,
		Clock3,
		KeyRound,
		LoaderCircle,
		MapPin,
		Plus,
		Radio,
		RotateCw
	} from '@lucide/svelte';
	import type { DiagnosticTest, EndpointConfig, RuntimeStatus, TestStatus } from '$lib/types';

	interface EndpointOption {
		id: string;
		name: string;
		region: string;
		publicKey: string;
		host?: string;
		location?: EndpointConfig['location'];
	}

	let browserId = $state('');
	let userPublicKey = $state('');
	let selectedEndpoint = $state('');
	let configuredEndpoints = $state<EndpointConfig[]>([]);
	let history = $state<DiagnosticTest[]>([]);
	let status = $state<RuntimeStatus | null>(null);
	let busy = $state(false);
	let error = $state('');
	let mounted = $state(false);

	let endpointOptions: EndpointOption[] = $derived(configuredEndpoints);
	let selectedEndpointDetails = $derived(
		endpointOptions.find((endpoint) => endpoint.id === selectedEndpoint)
	);
	let normalizedPublicKey = $derived(userPublicKey.trim().toLowerCase());
	let publicKeyIsValid = $derived(/^[0-9a-f]{64}$/.test(normalizedPublicKey));
	let publicKeyError = $derived(
		userPublicKey.trim() && !publicKeyIsValid ? 'Invalid public key. Use 64 hex characters.' : ''
	);

	onMount(() => {
		mounted = true;
		browserId = localStorage.getItem('hopback.browserId') || crypto.randomUUID();
		localStorage.setItem('hopback.browserId', browserId);
		userPublicKey = localStorage.getItem('hopback.userPublicKey') || '';
		void refresh();
		connect();
	});

	$effect(() => {
		if (!mounted) return;
		localStorage.setItem('hopback.userPublicKey', normalizedPublicKey);
	});

	async function refresh() {
		await Promise.all([loadStatus(), loadEndpoints(), loadHistory()]);
	}

	async function loadStatus() {
		const response = await fetch('/api/status');
		status = await response.json();
	}

	async function loadEndpoints() {
		const response = await fetch('/api/nodes?limit=1');
		const payload = (await response.json()) as { endpoints: EndpointConfig[] };
		configuredEndpoints = payload.endpoints;
		if (!selectedEndpoint && payload.endpoints[0]) selectedEndpoint = payload.endpoints[0].id;
	}

	async function loadHistory() {
		if (!browserId) return;
		const response = await fetch(`/api/tests?browserId=${encodeURIComponent(browserId)}`);
		const payload = (await response.json()) as { tests: DiagnosticTest[] };
		history = payload.tests;
	}

	function connect() {
		const socket = new WebSocket(`/ws?browserId=${encodeURIComponent(browserId)}`);
		socket.onmessage = (event) => {
			const payload = JSON.parse(event.data);
			if (payload.status) status = payload.status;
			if (payload.test) {
				history = [payload.test, ...history.filter((test) => test.id !== payload.test.id)].slice(
					0,
					30
				);
			}
		};
		socket.onclose = () => setTimeout(connect, 2500);
	}

	async function createTest(event: SubmitEvent) {
		event.preventDefault();
		error = '';
		const endpoint = endpointOptions.find((option) => option.id === selectedEndpoint);
		if (!endpoint) {
			error = 'Choose an endpoint.';
			return;
		}
		if (!publicKeyIsValid) {
			error = 'Enter a valid 64-character public key.';
			return;
		}

		busy = true;
		const response = await fetch('/api/tests', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({
				browserId,
				userPublicKey: normalizedPublicKey,
				endpointId: endpoint.id
			})
		});
		const payload = await response.json();
		busy = false;

		if (!response.ok) {
			error = payload.message || 'Could not start test.';
			return;
		}

		await goto(resolve('/[id]', { id: payload.test.id }));
	}

	function statusMeta(current: TestStatus) {
		if (current === 'completed')
			return { icon: CheckCircle2, label: 'Completed', className: 'bg-teal-50 text-teal-800' };
		if (current === 'partial')
			return { icon: AlertCircle, label: 'Partial', className: 'bg-amber-50 text-amber-800' };
		if (current === 'failed' || current === 'expired')
			return { icon: AlertCircle, label: current, className: 'bg-red-50 text-red-800' };
		return { icon: CircleDashed, label: current, className: 'bg-neutral-100 text-neutral-700' };
	}

	function totalTime(test: DiagnosticTest) {
		if (!test.returnSeenAt) return 'pending';
		const start = new Date(test.createdAt).getTime();
		const end = new Date(test.returnSeenAt).getTime();
		if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return 'pending';
		return `${((end - start) / 1000).toFixed(1)} s`;
	}

	function relativeTime(value?: string | null) {
		if (!value) return 'pending';
		const date = new Date(value);
		const now = Date.now();
		const delta = Math.floor((now - date.getTime()) / 1000);
		if (!Number.isFinite(delta)) return 'pending';
		if (delta < 0) return 'just now';
		if (delta < 60) return `${delta}s ago`;
		if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
		if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
		if (delta < 604800) return `${Math.floor(delta / 86400)}d ago`;
		return `${Math.floor(delta / 604800)}w ago`;
	}

	function formatDate(value: string) {
		return new Date(value).toLocaleString(undefined, {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
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
					MeshCore diagnostics
				</p>
				<h1 class="text-3xl font-semibold tracking-normal text-neutral-950">Hopback</h1>
			</div>
		</div>

		<div class="grid grid-cols-4 gap-2 text-sm">
			<div class="rounded-md border border-neutral-300 bg-white/75 px-3 py-2">
				<p class="text-neutral-500">Endpoints</p>
				<p class="font-semibold text-neutral-950">{status?.agents.length ?? 0}</p>
			</div>
			<div class="rounded-md border border-neutral-300 bg-white/75 px-3 py-2">
				<p class="text-neutral-500">Analyzers</p>
				<p class="font-semibold text-neutral-950">
					{status?.analyzers.filter((item) => item.state === 'open').length ?? 0}/{status?.analyzers
						.length ?? 0}
				</p>
			</div>
			<div class="rounded-md border border-neutral-300 bg-white/75 px-3 py-2">
				<p class="text-neutral-500">Observers</p>
				<p class="font-semibold text-neutral-950">{status?.observers ?? 0}</p>
			</div>
			<div class="rounded-md border border-neutral-300 bg-white/75 px-3 py-2">
				<p class="text-neutral-500">Nodes</p>
				<p class="font-semibold text-neutral-950">{status?.nodes ?? 0}</p>
			</div>
		</div>
	</header>

	<form
		class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm sm:p-5"
		onsubmit={createTest}
	>
		<div class="mb-5 flex items-center justify-between gap-3">
			<div>
				<h2 class="text-xl font-semibold text-neutral-950">New Connectivity Test</h2>
				<p class="text-sm text-neutral-500">
					Public key, destination, temporary code, live round trip.
				</p>
			</div>
			<button
				class="inline-flex size-10 items-center justify-center rounded-md border border-neutral-300 bg-white text-neutral-800 hover:bg-neutral-100"
				type="button"
				onclick={refresh}
				title="Refresh"
			>
				<RotateCw size={18} />
			</button>
		</div>

		<label class="block text-sm font-medium text-neutral-700" for="public-key"
			>User public key</label
		>
		<div class="relative mt-2">
			<input
				id="public-key"
				class={`mono w-full rounded-md border bg-neutral-50 px-3 py-3 pr-10 text-sm outline-none ring-teal-600/20 focus:ring-4 ${publicKeyError ? 'border-red-400 focus:border-red-500 focus:ring-red-500/10' : 'border-neutral-300 focus:border-teal-700'}`}
				bind:value={userPublicKey}
				placeholder="64 hex characters"
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
		{#if publicKeyError}
			<p class="mt-2 text-sm text-red-700">{publicKeyError}</p>
		{/if}

		<div class="mt-5">
			<p class="block text-sm font-medium text-neutral-700">Endpoint</p>
			<div class="mt-2 grid gap-2">
				{#each endpointOptions as endpoint (endpoint.id)}
					<button
						class={`rounded-md border px-3 py-3 text-left transition ${selectedEndpoint === endpoint.id ? 'border-teal-700 bg-teal-50 ring-4 ring-teal-600/10' : 'border-neutral-300 bg-neutral-50 hover:border-neutral-500'}`}
						type="button"
						onclick={() => (selectedEndpoint = endpoint.id)}
					>
						<span class="flex items-start justify-between gap-3">
							<span class="min-w-0">
								<span class="block truncate font-semibold text-neutral-950">{endpoint.name}</span>
								<span class="mt-1 flex items-center gap-1 text-sm text-neutral-600">
									<MapPin size={15} />
									<span class="truncate">{endpoint.location?.label || endpoint.region}</span>
								</span>
							</span>
							<span class="mono shrink-0 rounded bg-white px-2 py-1 text-xs text-neutral-500">
								{endpoint.publicKey.slice(0, 6)}
							</span>
						</span>
					</button>
				{:else}
					<p
						class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900"
					>
						No endpoints configured.
					</p>
				{/each}
			</div>
			{#if selectedEndpointDetails?.host}
				<p class="mt-2 text-sm text-neutral-500">{selectedEndpointDetails.host}</p>
			{/if}
		</div>

		{#if error}
			<p class="mt-4 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
				{error}
			</p>
		{/if}

		<button
			class="mt-5 inline-flex w-full items-center justify-center gap-2 rounded-md bg-neutral-950 px-4 py-3 font-semibold text-white hover:bg-neutral-800"
			disabled={busy || !selectedEndpoint || !publicKeyIsValid}
		>
			{#if busy}
				<LoaderCircle class="animate-spin" size={18} />
			{:else}
				<Plus size={18} />
			{/if}
			Start test
		</button>
	</form>

	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-3 flex items-center justify-between gap-3">
			<div class="flex items-center gap-2">
				<Clock3 size={18} class="text-neutral-500" />
				<h2 class="text-base font-semibold text-neutral-900">Previous Tests</h2>
			</div>
			<span class="text-xs text-neutral-500">{history.length} saved in this browser</span>
		</div>

		<div class="overflow-x-auto">
			<table class="w-full min-w-[760px] text-left text-sm">
				<thead class="border-b border-neutral-200 text-xs uppercase text-neutral-500">
					<tr>
						<th class="py-2 pr-3 font-semibold">Status</th>
						<th class="px-3 py-2 font-semibold">Endpoint</th>
						<th class="px-3 py-2 font-semibold">User key</th>
						<th class="px-3 py-2 font-semibold">Code</th>
						<th class="px-3 py-2 font-semibold">Time</th>
						<th class="px-3 py-2 font-semibold">Observed</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-neutral-100">
					{#each history as test (test.id)}
						{@const meta = statusMeta(test.status)}
						{@const StatusIcon = meta.icon}
						<tr
							class="group cursor-pointer hover:bg-neutral-50"
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
								<span class={totalTime(test) === 'pending' ? 'opacity-50' : ''}>{totalTime(test)}</span>
							</td>
							<td class="px-3 py-2 text-neutral-600">{test.observations.length}</td>
						</tr>
					{:else}
						<tr>
							<td colspan="6" class="py-5 text-center text-sm text-neutral-500">
								No tests for this browser yet.
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</section>

	<footer class="pb-3 text-center text-sm text-neutral-500">
		<a
			class="font-medium text-neutral-700 hover:text-teal-800"
			href="https://github.com/meshcore-cz/hopback"
		>
			meshcore-cz/hopback
		</a>
	</footer>
</main>
