<script lang="ts">
	import 'leaflet/dist/leaflet.css';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import {
		ArrowLeft,
		CheckCircle2,
		Circle,
		CircleDot,
		ClipboardList,
		Clock,
		Copy,
		ExternalLink,
		Map,
		Route,
		Send,
		XCircle
	} from '@lucide/svelte';
	import type {
		BrowserEvent,
		DiagnosticTest,
		NodeRecord,
		PacketObservation,
		RuntimeStatus
	} from '$lib/types';

	type LeafletModule = typeof import('leaflet');
	type LatLng = [number, number];

	let test = $state<DiagnosticTest | null>(null);
	let runtimeStatus = $state<RuntimeStatus | null>(null);
	let copied = $state(false);
	let error = $state('');
	let browserId = $state('');
	let leaflet = $state<LeafletModule | null>(null);
	let mapElement = $state<HTMLDivElement | undefined>();
	let mapInstance: import('leaflet').Map | null = null;
	let routeLayer: import('leaflet').LayerGroup | null = null;

	let outbound = $derived(test?.observations.filter((item) => item.direction === 'outbound') ?? []);
	let returned = $derived(test?.observations.filter((item) => item.direction === 'return') ?? []);
	let hasObserved = $derived(Boolean(test?.observations.length));
	let statusTone = $derived(
		test?.status === 'completed'
			? 'bg-teal-100 text-teal-900 border-teal-200'
			: test?.status === 'partial'
				? 'bg-amber-100 text-amber-900 border-amber-200'
				: test?.status === 'failed'
					? 'bg-red-100 text-red-900 border-red-200'
					: 'bg-neutral-100 text-neutral-800 border-neutral-200'
	);

	onMount(() => {
		browserId = localStorage.getItem('hopback.browserId') || crypto.randomUUID();
		localStorage.setItem('hopback.browserId', browserId);
		void Promise.all([loadTest(), loadStatus(), loadLeaflet()]);
		connect();
		return () => {
			mapInstance?.remove();
			mapInstance = null;
		};
	});

	$effect(() => {
		if (!leaflet || !mapElement || !test || !hasObserved) return;
		ensureMap();
		renderMap();
	});

	async function loadLeaflet() {
		leaflet = await import('leaflet');
	}

	async function loadTest() {
		const response = await fetch(`/api/tests/${page.params.id}`);
		const payload = await response.json();
		if (!response.ok) {
			error = payload.message || 'Test not found.';
			return;
		}
		test = payload.test;
	}

	async function loadStatus() {
		const response = await fetch('/api/status');
		runtimeStatus = await response.json();
	}

	function connect() {
		const socket = new WebSocket(`/ws?browserId=${encodeURIComponent(browserId)}`);
		socket.onopen = () =>
			socket.send(JSON.stringify({ type: 'subscribe', testId: page.params.id }));
		socket.onmessage = (event) => {
			const payload = JSON.parse(event.data) as BrowserEvent;
			if (payload.test && payload.test.id === page.params.id) test = payload.test;
			if (payload.status) runtimeStatus = payload.status;
		};
		socket.onclose = () => setTimeout(connect, 2500);
	}

	async function copyCode() {
		if (!test) return;
		await navigator.clipboard.writeText(test.code);
		copied = true;
		setTimeout(() => (copied = false), 1400);
	}

	function latency(start?: string | null, end?: string | null) {
		if (!start || !end) return 'pending';
		const delta = new Date(end).getTime() - new Date(start).getTime();
		if (!Number.isFinite(delta) || delta < 0) return 'pending';
		return `${(delta / 1000).toFixed(1)} s`;
	}

	function bestHopCount(items: PacketObservation[]) {
		if (!items.length) return 'pending';
		return Math.min(...items.map((item) => item.hopCount)).toString();
	}

	function uniqueObserverCount(items: PacketObservation[]) {
		return uniqueObserverKeys(items).length;
	}

	function uniquePathCount(items: PacketObservation[]) {
		return new SetLike(items.map((item) => item.path.join('>'))).values.length;
	}

	function uniqueObserverKeys(items: PacketObservation[]) {
		return new SetLike(
			items.map((item) => item.observerId || item.observerName || item.source).filter(Boolean)
		).values;
	}

	function analyzerPacketUrl(packetHash: string) {
		const source = runtimeStatus?.analyzers[0]?.url || 'wss://analyzer.meshcore.cz';
		const base = source.replace(/^wss:\/\//, 'https://').replace(/^ws:\/\//, 'http://');
		return `${base.replace(/\/$/, '')}/#/packets/${packetHash}`;
	}

	function progressPercent(current: DiagnosticTest) {
		if (current.status === 'failed' || current.status === 'expired') return 0;
		if (current.returnSeenAt || current.status === 'completed') return 100;
		if (current.status === 'replying' || current.replyStatus) return 75;
		if (current.outboundSeenAt || current.status === 'verified') return 50;
		return 0;
	}

	function activeProgressIndex(current: DiagnosticTest) {
		const percent = progressPercent(current);
		if (percent >= 100) return 3;
		if (percent >= 75) return 3;
		if (percent >= 50) return 2;
		return 0;
	}

	function stepCompleted(current: DiagnosticTest, index: number) {
		const percent = progressPercent(current);
		return percent >= (index + 1) * 25;
	}

	function waitingFor(current: DiagnosticTest) {
		if (current.status === 'completed') return 'Round trip completed.';
		if (current.status === 'failed') return 'The test failed.';
		if (current.status === 'expired') return 'The test expired.';
		if (!current.outboundSeenAt) return 'Waiting for you to add the contact and send the code.';
		if (current.status === 'replying') return 'Endpoint agent is sending the response.';
		if (!current.returnSeenAt) return 'Waiting for the return packet back to the user.';
		return 'Finalizing observations.';
	}

	function endpointCoords(): LatLng | null {
		const lat = test?.endpointLocation?.lat;
		const lon = test?.endpointLocation?.lon;
		return Number.isFinite(lat) && Number.isFinite(lon) ? [lat!, lon!] : null;
	}

	function nodeCoords(node: NodeRecord): LatLng | null {
		return Number.isFinite(node.lat) && Number.isFinite(node.lon) ? [node.lat!, node.lon!] : null;
	}

	function observationPoints(items: PacketObservation[]) {
		const points: Array<{ name: string; coords: LatLng; kind: 'node' | 'observer' | 'endpoint' }> =
			[];
		const seen: string[] = [];

		for (const observation of items) {
			const observerCoords = observation.observerNode ? nodeCoords(observation.observerNode) : null;
			if (observerCoords) {
				const key = `${observerCoords[0]},${observerCoords[1]},observer:${observation.observerId}`;
				if (!seen.includes(key)) {
					seen.push(key);
					points.push({
						name: observation.observerNode?.name || observation.observerName || 'Observer',
						coords: observerCoords,
						kind: 'observer'
					});
				}
			}

			for (const node of observation.resolvedPath) {
				const coords = nodeCoords(node);
				if (!coords) continue;
				const key = `${coords[0]},${coords[1]},${node.publicKey}`;
				if (seen.includes(key)) continue;
				seen.push(key);
				points.push({ name: node.name, coords, kind: 'node' });
			}
		}

		const endpoint = endpointCoords();
		if (endpoint && test) {
			points.push({ name: test.endpointName, coords: endpoint, kind: 'endpoint' });
		}

		return points;
	}

	function routeLineForObservation(observation: PacketObservation, includeEndpointAtEnd: boolean) {
		const points =
			observation.resolvedPath.map(nodeCoords).filter((point): point is LatLng => Boolean(point)) ??
			[];
		const endpoint = endpointCoords();
		if (endpoint) {
			if (includeEndpointAtEnd) points.push(endpoint);
			else points.unshift(endpoint);
		}
		return points;
	}

	function ensureMap() {
		if (!leaflet || !mapElement || mapInstance) return;
		const center = endpointCoords() || [50.478, 13.975];
		mapInstance = leaflet.map(mapElement, {
			center,
			zoom: 10,
			scrollWheelZoom: false,
			attributionControl: true
		});
		leaflet
			.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
				maxZoom: 18,
				attribution: '&copy; OpenStreetMap'
			})
			.addTo(mapInstance);
		routeLayer = leaflet.layerGroup().addTo(mapInstance);
	}

	function renderMap() {
		if (!leaflet || !mapInstance || !routeLayer) return;
		routeLayer.clearLayers();

		const bounds: LatLng[] = [];

		for (const observation of outbound) {
			const line = routeLineForObservation(observation, true);
			if (line.length < 2) continue;
			leaflet.polyline(line, { color: '#0f766e', weight: 3, opacity: 0.28 }).addTo(routeLayer);
			bounds.push(...line);
		}
		for (const observation of returned) {
			const line = routeLineForObservation(observation, false);
			if (line.length < 2) continue;
			leaflet
				.polyline(line, { color: '#c2410c', weight: 3, opacity: 0.28, dashArray: '8 8' })
				.addTo(routeLayer);
			bounds.push(...line);
		}

		for (const point of observationPoints(test?.observations ?? [])) {
			leaflet
				.circleMarker(point.coords, {
					radius: point.kind === 'endpoint' ? 8 : 6,
					color:
						point.kind === 'endpoint'
							? '#111827'
							: point.kind === 'observer'
								? '#7c3aed'
								: '#0f766e',
					weight: 2,
					fillColor:
						point.kind === 'endpoint'
							? '#111827'
							: point.kind === 'observer'
								? '#c4b5fd'
								: '#5eead4',
					fillOpacity: 0.86
				})
				.bindPopup(point.name)
				.addTo(routeLayer);
			bounds.push(point.coords);
		}

		if (bounds.length >= 2) mapInstance.fitBounds(bounds, { padding: [24, 24], maxZoom: 12 });
		else if (bounds.length === 1) mapInstance.setView(bounds[0], 10);
		setTimeout(() => mapInstance?.invalidateSize(), 0);
	}

	function relativeTime(value: string, start: string) {
		const delta = new Date(value).getTime() - new Date(start).getTime();
		if (!Number.isFinite(delta)) return '+?';
		if (Math.abs(delta) < 1000) return `${delta >= 0 ? '+' : '-'}${Math.abs(delta)}ms`;
		return `${delta >= 0 ? '+' : '-'}${(Math.abs(delta) / 1000).toFixed(2)}s`;
	}

	function firstObservationTime(observations: PacketObservation[]) {
		if (!observations.length) return null;
		return observations.reduce((earliest, observation) => {
			const time = new Date(observation.createdAt).getTime();
			return time < earliest ? time : earliest;
		}, Number.POSITIVE_INFINITY);
	}

	function compactPath(observation: PacketObservation) {
		if (!observation.path.length) return 'direct';
		return observation.path.join(' ');
	}

	function isEndpointObservation(observation: PacketObservation, current: DiagnosticTest) {
		const observer = (
			observation.observerId ||
			observation.observerNode?.publicKey ||
			''
		).toLowerCase();
		const endpointKey = current.endpointPublicKey.toLowerCase();
		return (
			observation.source.startsWith('agent:') ||
			observer === endpointKey ||
			Boolean(observation.observerName?.toLowerCase().includes(current.endpointName.toLowerCase()))
		);
	}

	function observerLabel(observation: PacketObservation) {
		return observation.observerNode?.name || observation.observerName || observation.source;
	}

	class SetLike<T> {
		values: T[] = [];

		constructor(items: T[]) {
			for (const item of items) {
				if (!this.values.includes(item)) this.values.push(item);
			}
		}
	}
</script>

<main class="mx-auto flex min-h-screen w-full max-w-7xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
	<a
		class="inline-flex w-fit items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 py-2 text-sm font-medium text-neutral-800 hover:bg-neutral-100"
		href={resolve('/')}
	>
		<ArrowLeft size={16} />
		Back
	</a>

	{#if error}
		<section class="rounded-md border border-red-200 bg-red-50 p-5 text-red-800">{error}</section>
	{:else if !test}
		<section class="rounded-md border border-neutral-300 bg-white p-5 text-neutral-600">
			Loading test...
		</section>
	{:else}
		<header
			class="grid gap-4 rounded-md border border-neutral-300 bg-white p-4 shadow-sm lg:grid-cols-[1fr_auto] lg:items-center"
		>
			<div>
				<div class="mb-3 flex flex-wrap items-center gap-2">
					<span class={`rounded-md border px-2.5 py-1 text-sm font-semibold ${statusTone}`}
						>{test.status}</span
					>
					<span class="text-sm text-neutral-500">{test.endpointRegion}</span>
				</div>
				<h1 class="text-2xl font-semibold text-neutral-950">{test.endpointName}</h1>
				<p class="mono mt-2 break-all text-sm text-neutral-500">{test.id}</p>
			</div>

			<div class="grid grid-cols-3 gap-2 text-sm">
				<div class="rounded-md border border-neutral-200 px-3 py-2">
					<p class="text-neutral-500">Outbound</p>
					<p class="font-semibold">{outbound.length ? 'seen' : 'waiting'}</p>
				</div>
				<div class="rounded-md border border-neutral-200 px-3 py-2">
					<p class="text-neutral-500">Return</p>
					<p class="font-semibold">{returned.length ? 'seen' : 'waiting'}</p>
				</div>
				<div class="rounded-md border border-neutral-200 px-3 py-2">
					<p class="text-neutral-500">Elapsed</p>
					<p class="font-semibold">{latency(test.createdAt, test.returnSeenAt)}</p>
				</div>
			</div>
		</header>

		{@render ProgressPanel(test)}

		<section class="grid gap-5 lg:grid-cols-[360px_minmax(0,1fr)]">
			<aside class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
				{#if hasObserved}
					<div class="mb-4 flex items-center gap-2">
						<Map size={18} class="text-teal-700" />
						<h2 class="text-lg font-semibold text-neutral-950">Map</h2>
					</div>
					<div
						bind:this={mapElement}
						class="hopback-map rounded-md border border-neutral-200"
					></div>
					<div class="mt-3 flex flex-wrap gap-2 text-xs">
						<span class="inline-flex items-center gap-1 rounded bg-teal-50 px-2 py-1 text-teal-800">
							<span class="size-2 rounded-full bg-teal-700"></span>
							User to endpoint
						</span>
						<span
							class="inline-flex items-center gap-1 rounded bg-orange-50 px-2 py-1 text-orange-800"
						>
							<span class="size-2 rounded-full bg-orange-700"></span>
							Endpoint to user
						</span>
					</div>
				{:else}
					<div class="flex items-center gap-2">
						<ClipboardList size={18} class="text-teal-700" />
						<h2 class="text-lg font-semibold text-neutral-950">Instructions</h2>
					</div>
					<p class="mt-3 text-sm leading-6 text-neutral-600">
						Please send the code below to the endpoint. You can add the contact manually using the
						endpoint public key, or scan the QR code below.
					</p>
					<div class="mt-3 rounded-md border border-neutral-200 bg-neutral-50 p-3">
						<p class="text-xs font-semibold uppercase tracking-wide text-neutral-500">
							Endpoint public key
						</p>
						<p class="mono mt-2 break-all text-xs text-neutral-700">{test.endpointPublicKey}</p>
					</div>
					{#if test.qrDataUrl}
						<img
							class="mx-auto mt-4 size-64 rounded-md border border-neutral-200 bg-white p-2"
							src={test.qrDataUrl}
							alt="MeshCore contact QR"
						/>
					{/if}
					<div class="mt-4 rounded-md border border-neutral-200 bg-neutral-50 p-3">
						<p class="text-xs font-semibold uppercase tracking-wide text-neutral-500">
							Temporary code
						</p>
						<div class="mt-2 flex items-center justify-between gap-2">
							<p class="mono text-2xl font-semibold text-neutral-950">{test.code}</p>
							<button
								class="grid size-10 place-items-center rounded-md border border-neutral-300 bg-white hover:bg-neutral-100"
								type="button"
								onclick={copyCode}
								title="Copy code"
							>
								{#if copied}<CheckCircle2 size={18} class="text-teal-700" />{:else}<Copy
										size={18}
									/>{/if}
							</button>
						</div>
					</div>
					<p class="mono mt-4 break-all text-xs text-neutral-500">{test.qrPayload}</p>
				{/if}
			</aside>

			<div class="grid gap-5">
				<section class="grid gap-4 md:grid-cols-2">
					{@render RoutePanel('User to endpoint', 'send', outbound)}
					{@render RoutePanel('Endpoint to user', 'return', returned)}
				</section>

				<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
					<div class="mb-4 flex flex-wrap items-center justify-between gap-3">
						<div class="flex items-center gap-2">
							<Clock size={18} class="text-amber-700" />
							<h2 class="text-lg font-semibold text-neutral-950">Packet Observations</h2>
						</div>
						<p class="text-sm text-neutral-500">
							{test.observations.length} observations · {uniqueObserverCount(test.observations)}
							observers · {uniquePathCount(test.observations)} paths
						</p>
					</div>
					<div class="overflow-x-auto">
						<table class="w-full text-left text-xs">
							<thead class="border-b border-neutral-200 text-neutral-500">
								<tr>
									<th class="py-2 pr-2 font-semibold">Time</th>
									<th class="px-2 py-2 font-semibold">Kind</th>
									<th class="px-2 py-2 font-semibold">Observer</th>
									<th class="px-2 py-2 font-semibold">Path</th>
									<th class="px-2 py-2 font-semibold">Hops</th>
									<th class="px-2 py-2 font-semibold">RSSI</th>
									<th class="px-2 py-2 font-semibold">SNR</th>
									<th class="py-2 pl-2 font-semibold">Packet</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-neutral-100">
								{#each [...test.observations].reverse() as observation (observation.id)}
									{@const firstSeen = firstObservationTime(test.observations)}
									{@const endpointObservation = isEndpointObservation(observation, test)}
									<tr
										class={endpointObservation
											? 'bg-teal-50/75 text-neutral-950'
											: 'text-neutral-700 hover:bg-neutral-50'}
									>
										<td class="mono py-2 pr-2 text-neutral-500">
											{relativeTime(observation.createdAt, new Date(firstSeen ?? test.createdAt).toISOString())}
										</td>
										<td class="px-2 py-2">
											<span
												class={`inline-flex items-center rounded px-1.5 py-0.5 font-semibold ${observation.direction === 'outbound' ? 'bg-teal-100 text-teal-800' : 'bg-orange-100 text-orange-800'}`}
											>
												{endpointObservation ? 'endpoint' : observation.direction}
											</span>
										</td>
										<td class="px-2 py-2">
											<p class="max-w-48 truncate font-medium">
												{observerLabel(observation)}
											</p>
											{#if observation.observerId}
												<p class="mono max-w-48 truncate text-[10px] text-neutral-500">
													{observation.observerId}
												</p>
											{/if}
										</td>
										<td class="mono max-w-96 truncate px-2 py-2 text-[11px] text-neutral-500">
											{compactPath(observation)}
										</td>
										<td class="px-2 py-2">{observation.hopCount}</td>
										<td class="px-2 py-2">{observation.rssi ?? 'n/a'}</td>
										<td class="px-2 py-2">{observation.snr ?? 'n/a'}</td>
										<td class="mono py-2 pl-2">
											<button
												class="inline-flex items-center gap-1 rounded border border-neutral-200 bg-white px-1.5 py-0.5 text-[11px] text-neutral-600 hover:border-teal-700 hover:text-teal-800"
												type="button"
												onclick={() =>
													window.open(
														analyzerPacketUrl(observation.packetHash),
														'_blank',
														'noreferrer'
													)}
											>
												{observation.packetHash}
												<ExternalLink size={10} />
											</button>
										</td>
									</tr>
								{:else}
									<tr>
										<td colspan="8" class="py-5 text-center text-sm text-neutral-500">
											Waiting for matching packets.
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</section>
			</div>
		</section>

		<section class="grid gap-3 md:grid-cols-4">
			{@render Metric('Outbound hops', bestHopCount(outbound))}
			{@render Metric('Return hops', bestHopCount(returned))}
			{@render Metric('Reply', test.replyStatus || 'pending')}
			{@render Metric('Expires', new Date(test.expiresAt).toLocaleTimeString())}
		</section>
	{/if}
</main>

{#snippet Metric(label: string, value: string)}
	<div class="rounded-md border border-neutral-300 bg-white p-3 shadow-sm">
		<p class="text-sm text-neutral-500">{label}</p>
		<p class="mt-1 truncate font-semibold text-neutral-950">{value}</p>
	</div>
{/snippet}

{#snippet ProgressPanel(current: DiagnosticTest)}
	{@const percent = progressPercent(current)}
	{@const active = activeProgressIndex(current)}
	{@const steps = [
		{ label: 'Send code', detail: 'Add contact and send message' },
		{ label: 'Gateway received', detail: 'Packet appears on CoreScope' },
		{ label: 'Response sent', detail: 'Endpoint agent sends reply' },
		{ label: 'Delivered', detail: 'Return packet observed' }
	]}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
			<div>
				<h2 class="text-lg font-semibold text-neutral-950">Live Progress</h2>
				<p class="text-sm text-neutral-500">{waitingFor(current)}</p>
			</div>
			<div class="rounded-md bg-neutral-100 px-3 py-2 text-sm font-semibold text-neutral-800">
				{percent}%
			</div>
		</div>

		<div class="relative mb-5 h-2 overflow-hidden rounded-full bg-neutral-200">
			<div
				class="h-full rounded-full bg-teal-600 transition-all duration-700"
				style={`width: ${percent}%`}
			></div>
			{#if current.status !== 'completed' && current.status !== 'failed' && current.status !== 'expired'}
				<div class="progress-scan absolute inset-y-0 w-28 rounded-full bg-white/45"></div>
			{/if}
		</div>

		<div class="grid gap-3 md:grid-cols-4">
			{#each steps as step, index (step.label)}
				{@const complete = stepCompleted(current, index)}
				<div
					class={`rounded-md border p-3 transition ${complete ? 'border-teal-200 bg-teal-50' : index === active ? 'border-amber-300 bg-amber-50 shadow-sm' : 'border-neutral-200 bg-neutral-50'}`}
				>
					<div class="mb-2 flex items-center gap-2">
						{#if complete}
							<CheckCircle2 size={18} class="text-teal-700" />
						{:else if index === active}
							<span class="relative inline-grid size-[18px] place-items-center">
								<span class="absolute size-[18px] animate-ping rounded-full bg-amber-300/70"></span>
								<CircleDot size={18} class="relative text-amber-700" />
							</span>
						{:else}
							<Circle size={18} class="text-neutral-400" />
						{/if}
						<p class="text-sm font-semibold text-neutral-950">{step.label}</p>
					</div>
					<p class="text-xs text-neutral-500">{step.detail}</p>
				</div>
			{/each}
		</div>
	</section>
{/snippet}

{#snippet RoutePanel(title: string, icon: string, observations: PacketObservation[])}
	{@const firstObservation = observations[0]}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex items-center justify-between gap-3">
			<div class="flex min-w-0 items-center gap-2">
				{#if icon === 'send'}<Send size={18} class="shrink-0 text-teal-700" />{:else}<Route
						size={18}
						class="shrink-0 text-amber-700"
					/>{/if}
				<h2 class="truncate text-lg font-semibold text-neutral-950">{title}</h2>
			</div>
			{#if firstObservation}
				<button
					class="mono inline-flex shrink-0 items-center gap-0.5 rounded border border-neutral-300 bg-neutral-50 px-1 py-0.5 font-semibold text-neutral-500 hover:border-teal-700 hover:bg-teal-50 hover:text-teal-900"
					type="button"
					style="font-size: 11px; line-height: 1;"
					onclick={() =>
						window.open(analyzerPacketUrl(firstObservation.packetHash), '_blank', 'noreferrer')}
					title="Open packet in analyzer"
				>
					<span>{firstObservation.packetHash}</span>
					<ExternalLink size={10} strokeWidth={2} />
				</button>
			{/if}
		</div>

		{#if observations.length}
			{#each observations.slice(0, 1) as observation (observation.id)}
				<div class="grid grid-cols-2 gap-2 text-sm xl:grid-cols-4">
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">Observations</p>
						<p class="font-semibold">{observations.length}</p>
					</div>
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">Observers</p>
						<p class="font-semibold">{uniqueObserverCount(observations)}</p>
					</div>
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">Paths</p>
						<p class="font-semibold">{uniquePathCount(observations)}</p>
					</div>
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">Hops</p>
						<p class="font-semibold">{bestHopCount(observations)}</p>
					</div>
				</div>
				<div class="mt-3 grid grid-cols-3 gap-2 text-sm">
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">First</p>
						<p class="mono font-semibold">
							{relativeTime(observation.createdAt, test?.createdAt || observation.createdAt)}
						</p>
					</div>
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">RSSI</p>
						<p class="font-semibold">{observation.rssi ?? 'n/a'}</p>
					</div>
					<div class="rounded-md bg-neutral-50 p-2">
						<p class="text-neutral-500">SNR</p>
						<p class="font-semibold">{observation.snr ?? 'n/a'}</p>
					</div>
				</div>
				<p class="mono mt-3 truncate text-xs text-neutral-500">{compactPath(observation)}</p>
			{/each}
		{:else}
			<div
				class="flex min-h-40 items-center justify-center rounded-md border border-dashed border-neutral-300 bg-neutral-50 text-sm text-neutral-500"
			>
				<XCircle size={17} class="mr-2" />
				No matching packet yet
			</div>
		{/if}
	</section>
{/snippet}

<style>
	.hopback-map {
		height: 360px;
		width: 100%;
		overflow: hidden;
	}

	.progress-scan {
		animation: progress-scan 1.8s ease-in-out infinite;
	}

	@keyframes progress-scan {
		0% {
			transform: translateX(-120%);
		}
		100% {
			transform: translateX(820%);
		}
	}
</style>
