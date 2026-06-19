<script lang="ts">
	import 'leaflet/dist/leaflet.css';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { fade } from 'svelte/transition';
	import {
		AlertCircle,
		AlertTriangle,
		ArrowLeft,
		CheckCircle2,
		Circle,
		CircleDot,
		CircleHelp,
		ClipboardList,
		Clock,
		Copy,
		Eye,
		GitBranch,
		Hash,
		Timer,
		Users,
		Waypoints,
		ExternalLink,
		KeyRound,
		LoaderCircle,
		Map,
		Maximize2,
		Minimize2,
		Radio,
		RotateCw,
		Route,
		Send,
		Terminal,
		XCircle
	} from '@lucide/svelte';
	import type {
		BrowserEvent,
		DeliveryPathOption,
		DeliveryPathRow,
		DiagnosticTest,
		PacketKind,
		PacketObservation,
		RuntimeStatus
	} from '$lib/types';
	import { apiFetch, wsUrl } from '$lib/client/api';
	import { packetKind } from '$lib/milestones';
	import { t, tn, localeTag } from '$lib/i18n/index.svelte';
	import LanguageSwitcher from '$lib/i18n/LanguageSwitcher.svelte';
	import { APP_VERSION } from '$lib/version';

	// Messages vs acks use deliberately contrasting hues so the two are easy to tell
	// apart on the map: outbound message = teal, its ack = blue; return message = orange,
	// its ack = magenta.
	const KIND_COLORS: Record<PacketKind, string> = {
		'user msg': '#0d9488',
		'ack+path': '#2563eb',
		ack: '#2563eb',
		reply: '#ea580c',
		'reply ack': '#db2777'
	};

	function kindColor(kind: PacketKind) {
		return KIND_COLORS[kind] ?? '#0f766e';
	}

	const POINT_LEGEND: Array<{
		kind: 'endpoint' | 'observer' | 'node';
		label: string;
		color: string;
	}> = [
		{ kind: 'endpoint', label: 'Endpoint', color: '#111827' },
		{ kind: 'observer', label: 'Observer', color: '#7c3aed' },
		{ kind: 'node', label: 'Node', color: '#0f766e' }
	];

	function formatDateTime(value?: string | null) {
		if (!value) return '';
		const date = new Date(value);
		return Number.isFinite(date.getTime()) ? date.toLocaleString(localeTag()) : '';
	}

	type LeafletModule = typeof import('leaflet');
	type LatLng = [number, number];

	let test = $state<DiagnosticTest | null>(null);
	let runtimeStatus = $state<RuntimeStatus | null>(null);
	let copiedField = $state('');
	let error = $state('');
	let repeating = $state(false);
	let leaflet = $state<LeafletModule | null>(null);
	let mapElement = $state<HTMLDivElement | undefined>();
	let mapFullWidth = $state(false);
	let mapInstance: import('leaflet').Map | null = null;
	let mapBuiltOn: HTMLDivElement | null = null;
	let routeLayer: import('leaflet').LayerGroup | null = null;
	let socket: WebSocket | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let statusPoller: ReturnType<typeof setInterval> | null = null;
	let testPoller: ReturnType<typeof setInterval> | null = null;
	let selectedKinds = $state<string[]>([]);
	// Packet-observation path display: node names vs. raw hex path hashes. The
	// header button sets the default for every row; per-row buttons override it
	// (keyed by observation id) so a single path can be flipped on its own.
	let pathHex = $state(false);
	let rowHexOverride = $state<Record<number, boolean>>({});
	// The activity section can show the full packet table or a curated event console.
	let obsView = $state<'table' | 'console'>('table');

	function rowHex(id: number) {
		return rowHexOverride[id] ?? pathHex;
	}

	function toggleRowHex(id: number) {
		rowHexOverride = { ...rowHexOverride, [id]: !rowHex(id) };
	}

	// Flipping the global default resets per-row overrides so it acts as a master.
	function toggleAllHex() {
		pathHex = !pathHex;
		rowHexOverride = {};
	}

	// Per-delivery-path hex toggle, keyed by the option's key.
	let deliveryHex = $state<Record<string, boolean>>({});

	function toggleDeliveryHex(key: string) {
		deliveryHex = { ...deliveryHex, [key]: !deliveryHex[key] };
	}
	// Map legend chips double as filters: kinds listed here are hidden on the map.
	let hiddenMapLinks = $state<PacketKind[]>([]);
	let hiddenMapPoints = $state<string[]>([]);

	function toggleMapLink(kind: PacketKind) {
		hiddenMapLinks = hiddenMapLinks.includes(kind)
			? hiddenMapLinks.filter((item) => item !== kind)
			: [...hiddenMapLinks, kind];
	}

	function toggleMapPoint(kind: string) {
		hiddenMapPoints = hiddenMapPoints.includes(kind)
			? hiddenMapPoints.filter((item) => item !== kind)
			: [...hiddenMapPoints, kind];
	}

	let outbound = $derived(test?.observations.filter((item) => item.direction === 'outbound') ?? []);
	let returned = $derived(test?.observations.filter((item) => item.direction === 'return') ?? []);
	let filteredObservations = $derived(
		test?.observations.filter(
			(observation) => !selectedKinds.length || selectedKinds.includes(kindRetryKey(observation))
		) ?? []
	);
	// Maps a packet hash to its re-send rank within its kind: 0 = original transmission,
	// 1 = first retry, 2 = second, … Distinct content hashes for a kind are distinct
	// transmissions, ordered by when each first appeared on the mesh.
	let retryRankByHash = $derived.by(() => {
		type HashTime = { hash: string; at: number };
		const firstSeen: Record<string, { kind: string; at: number }> = {};
		for (const observation of test?.observations ?? []) {
			if (!observation.packetHash) continue;
			const at = new Date(observation.createdAt).getTime();
			const prev = firstSeen[observation.packetHash];
			if (!prev || at < prev.at)
				firstSeen[observation.packetHash] = { kind: packetKindLabel(observation), at };
		}
		const byKind: Record<string, HashTime[]> = {};
		for (const hash of Object.keys(firstSeen)) {
			const entry = firstSeen[hash];
			const list = byKind[entry.kind] ?? (byKind[entry.kind] = []);
			list.push({ hash, at: entry.at });
		}
		const ranks: Record<string, number> = {};
		for (const list of Object.values(byKind)) {
			list.sort((a, b) => a.at - b.at);
			list.forEach((item, index) => (ranks[item.hash] = index));
		}
		return ranks;
	});

	let hasObserved = $derived(Boolean(test?.observations.length));
	let expiredWithoutPackets = $derived(
		Boolean(test && test.status === 'expired' && test.observations.length === 0)
	);
	let mapKinds = $derived(
		test?.propagationMap
			? [...new Set(test.propagationMap.segments.map((segment) => segment.kind))].filter(
					(kind): kind is PacketKind => kind in KIND_COLORS
				)
			: []
	);
	let mapPointKinds = $derived(
		test?.propagationMap
			? new Set(test.propagationMap.points.map((point) => point.kind))
			: new Set<string>()
	);
	let pageTitle = $derived(
		test
			? `${t('detail.title', { id: test.id, name: test.endpointName })} · Hopback`
			: `${t('detail.loading', { id: page.params.id ?? '' })} · Hopback`
	);
	let pageDescription = $derived(
		test ? t('detail.title', { id: test.id, name: test.endpointName }) : t('header.tagline')
	);
	let statusTone = $derived(
		test?.status === 'completed'
			? 'bg-teal-100 text-teal-900 border-teal-200'
			: test?.status === 'failed' || test?.status === 'expired'
				? 'bg-red-100 text-red-900 border-red-200'
				: 'bg-neutral-100 text-neutral-800 border-neutral-200'
	);

	onMount(() => {
		void Promise.all([loadTest(), loadStatus(), loadLeaflet()]);
		statusPoller = setInterval(() => void loadStatus(), 5000);
		testPoller = setInterval(() => {
			if (test && isIngestionOpen(test)) void loadTest();
		}, 10000);
		return () => {
			if (statusPoller) clearInterval(statusPoller);
			if (testPoller) clearInterval(testPoller);
			disconnect();
			mapInstance?.remove();
			mapInstance = null;
			mapBuiltOn = null;
		};
	});

	$effect(() => {
		if (!test) return;
		if (isLiveTest(test)) connect();
		else disconnect();
	});

	$effect(() => {
		// Re-render when the legend filters change.
		void hiddenMapLinks;
		void hiddenMapPoints;
		if (!leaflet || !mapElement || !test || !hasObserved) return;
		ensureMap();
		renderMap();
		// Scroll-wheel zoom is only enabled in full-width mode, where the map no longer
		// competes with page scrolling for the wheel.
		if (mapInstance) {
			if (mapFullWidth) mapInstance.scrollWheelZoom.enable();
			else mapInstance.scrollWheelZoom.disable();
		}
	});

	async function loadLeaflet() {
		leaflet = await import('leaflet');
	}

	async function loadTest() {
		const response = await apiFetch(`/api/tests/${page.params.id}`);
		const payload = await response.json();
		if (!response.ok) {
			error = payload.message || 'Test not found.';
			return;
		}
		test = payload.test;
	}

	// Starts a fresh test for the same endpoint + user key, then loads it. A full
	// navigation is used so the page (poller, socket, map) re-initialises cleanly.
	async function repeatTest() {
		if (!test || repeating) return;
		repeating = true;
		error = '';
		try {
			const response = await apiFetch('/api/tests', {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({
					userPublicKey: test.userPublicKey,
					endpointId: test.endpointId
				})
			});
			const payload = await response.json();
			if (!response.ok) {
				error = payload.message || t('detail.repeat.error');
				repeating = false;
				return;
			}
			window.location.assign(resolve('/[id]', { id: payload.test.id }));
		} catch {
			error = t('detail.repeat.error');
			repeating = false;
		}
	}

	async function loadStatus() {
		const response = await apiFetch('/api/status');
		runtimeStatus = await response.json();
	}

	function connect() {
		if (socket && socket.readyState <= WebSocket.OPEN) return;
		socket = new WebSocket(wsUrl('/ws?browserId=anonymous'));
		socket.onopen = () => {
			socket?.send(JSON.stringify({ type: 'subscribe', testId: page.params.id }));
		};
		socket.onmessage = (event) => {
			const payload = JSON.parse(event.data) as BrowserEvent;
			if (payload.status) runtimeStatus = payload.status;
			if (
				payload.type === 'observation' &&
				payload.testId === page.params.id &&
				payload.observation
			) {
				if (!test) return;
				const updated = payload.test || test;
				test = mergeTestUpdate(test, updated, payload.observation);
				return;
			}
			if (payload.test && payload.test.id === page.params.id) {
				test = test ? mergeTestUpdate(test, payload.test) : payload.test;
			}
		};
		socket.onclose = () => {
			socket = null;
			if (isLiveTest(test)) reconnectTimer = setTimeout(connect, 2500);
		};
	}

	function disconnect() {
		if (reconnectTimer) clearTimeout(reconnectTimer);
		reconnectTimer = null;
		if (!socket) return;
		const closing = socket;
		socket = null;
		closing.onclose = null;
		closing.close();
	}

	async function copyText(value: string, field: string) {
		await navigator.clipboard.writeText(value);
		copiedField = field;
		setTimeout(() => {
			if (copiedField === field) copiedField = '';
		}, 1400);
	}

	function isLiveTest(current: DiagnosticTest | null) {
		return Boolean(
			current &&
			current.status !== 'failed' &&
			current.status !== 'expired' &&
			(current.status !== 'completed' || isIngestionOpen(current))
		);
	}

	function isIngestionOpen(current: DiagnosticTest) {
		const first = firstPacketSeenAt(current);
		if (!first) return false;
		const firstMs = new Date(first).getTime();
		return Number.isFinite(firstMs) && Date.now() - firstMs < 5 * 60 * 1000;
	}

	function mergeTestUpdate(
		current: DiagnosticTest,
		updated: DiagnosticTest,
		observation?: PacketObservation
	): DiagnosticTest {
		const observations = mergeObservations(current.observations, updated.observations, observation);
		return {
			...current,
			...updated,
			qrDataUrl: updated.qrDataUrl || current.qrDataUrl,
			nodes: { ...current.nodes, ...updated.nodes },
			observations
		};
	}

	function mergeObservations(
		current: PacketObservation[],
		updated: PacketObservation[] | undefined,
		observation?: PacketObservation
	) {
		const base = updated?.length ? updated : current;
		const next = observation ? [observation, ...base] : base;
		const seen: Record<string, true> = {};
		return next.filter((item) => {
			const key = `${item.id}:${item.packetHash}:${item.direction}:${item.source}`;
			if (seen[key]) return false;
			seen[key] = true;
			return true;
		});
	}

	function latency(start?: string | null, end?: string | null) {
		if (!start || !end) return t('common.pending');
		const delta = new Date(end).getTime() - new Date(start).getTime();
		if (!Number.isFinite(delta) || delta < 0) return t('common.pending');
		return `${(delta / 1000).toFixed(1)} s`;
	}

	function bestHopCount(items: PacketObservation[]) {
		if (!items.length) return t('common.pending');
		return Math.min(...items.map((item) => item.hopCount)).toString();
	}

	// Hop count for a route panel measured at the DESTINATION, not the global min
	// (which would be 0 whenever any observer near the sender heard it directly).
	// Outbound: the hops the message took to reach the endpoint. Return: the user's
	// receipt isn't observed directly, so the reply-ACK's hops back to the endpoint
	// stand in for the user<->endpoint distance.
	function deliveryHops(
		direction: 'outbound' | 'return',
		messages: PacketObservation[],
		acks: PacketObservation[],
		current?: DiagnosticTest | null
	) {
		if (current) {
			if (direction === 'outbound') {
				const ep = firstObservation(messages.filter((m) => isEndpointObservation(m, current)));
				if (ep) return hopLabel(ep.hopCount);
			} else {
				const epAck = firstObservation(acks.filter((a) => isEndpointObservation(a, current)));
				if (epAck) return hopLabel(epAck.hopCount);
			}
		}
		return hopLabel(bestHopCount(messages));
	}

	// Classifies a set of observations by the routing method the packet was sent
	// with (from the packet header). Falls back to hop-count inference only for
	// legacy observations recorded before the header route was captured.
	function routeKindOf(items: PacketObservation[]) {
		if (!items.length) return null;
		const routes = new Set(items.map((item) => item.route).filter(Boolean));
		if (routes.size) {
			if (routes.size > 1) return t('route.kind.mixed');
			return routes.has('direct') ? t('route.kind.direct') : t('route.kind.flood');
		}
		const direct = items.some((item) => item.hopCount === 0);
		const flood = items.some((item) => item.hopCount > 0);
		if (direct && flood) return t('route.kind.mixed');
		return flood ? t('route.kind.flood') : t('route.kind.direct');
	}

	// Prefer the message's own routing. When the message itself was never observed
	// (e.g. a unicast reply where only its flooded ACK was seen), fall back to the
	// ACK's route so a completed leg doesn't read as "pending".
	function routeKind(items: PacketObservation[]) {
		return (
			routeKindOf(messageObservations(items)) ??
			routeKindOf(ackObservations(items)) ??
			t('common.pending')
		);
	}

	function isAckPacket(observation: PacketObservation) {
		return ['ACK', 'PATH', 'PATH_IDENTITY', 'RESPONSE', 'MULTIPART'].includes(
			observation.decodedType || ''
		);
	}

	function packetKindLabel(observation: PacketObservation) {
		return packetKind(observation);
	}

	// Translated display for a raw packet kind (logic/filtering still uses the raw value).
	function kindLabel(kind: string) {
		return t(`kind.${kind}`);
	}

	// Raw filter key combining a packet kind with its re-send rank: "reply" for the
	// original, "reply^1" for the first retry, etc. Used so retries filter separately.
	function kindRetryKey(observation: PacketObservation) {
		const base = packetKindLabel(observation);
		const rank = observation.packetHash ? (retryRankByHash[observation.packetHash] ?? 0) : 0;
		return rank > 0 ? `${base}^${rank}` : base;
	}

	// Translated display for a raw kind+rank key, e.g. "reply^1" → "reply ^1".
	function kindKeyLabel(key: string) {
		const caret = key.lastIndexOf('^');
		return caret === -1
			? kindLabel(key)
			: `${kindLabel(key.slice(0, caret))} ^${key.slice(caret + 1)}`;
	}

	// Kind label annotated with its re-send rank, e.g. "reply ^1" for the first retry.
	function kindLabelWithRetry(observation: PacketObservation) {
		return kindKeyLabel(kindRetryKey(observation));
	}

	// Sentence-case a label so it reads consistently when it opens a console title.
	function capitalize(value: string) {
		return value ? value.charAt(0).toUpperCase() + value.slice(1) : value;
	}

	function packetKindClass(observation: PacketObservation) {
		if (isAckPacket(observation)) {
			return observation.direction === 'outbound'
				? 'bg-cyan-100 text-cyan-800'
				: 'bg-amber-100 text-amber-800';
		}
		return observation.direction === 'outbound'
			? 'bg-teal-100 text-teal-800'
			: 'bg-orange-100 text-orange-800';
	}

	function messageObservations(items: PacketObservation[]) {
		return items.filter((item) => !isAckPacket(item));
	}

	function ackObservations(items: PacketObservation[]) {
		return items.filter(isAckPacket);
	}

	// Labels the ACK box by what was actually observed: a PATH-carrying ACK (ack+path)
	// vs a bare ACK. Falls back to the expected type for the direction when nothing
	// has been observed yet.
	function ackTypeLabel(acks: PacketObservation[], outbound: boolean) {
		const hasPath = acks.some(
			(ack) => ack.decodedType === 'PATH' || ack.decodedType === 'PATH_IDENTITY'
		);
		if (acks.length) return hasPath ? t('route.ackPath') : t('route.ack');
		return outbound ? t('route.ackPath') : t('route.ack');
	}

	// Same label as ackTypeLabel, but annotated with how the observed ACKs travelled,
	// e.g. "ACK (flood)".
	function ackLabel(acks: PacketObservation[], outbound: boolean) {
		const base = ackTypeLabel(acks, outbound);
		const routing = routeKindOf(acks);
		return routing ? `${base} (${routing})` : base;
	}

	function uniqueObserverCount(items: PacketObservation[]) {
		return uniqueObserverKeys(items).length;
	}

	// A node that transmitted a packet shouldn't count as an observer of it: our
	// endpoint hears its own outgoing reply/ACK over RF, but that self-echo isn't a
	// real third-party sighting. Drop it before counting observers of endpoint-sent
	// packets (the reply, the outbound ACK).
	function externalObservers(items: PacketObservation[], current: DiagnosticTest) {
		return items.filter((item) => !isEndpointObservation(item, current));
	}

	// "observed / active" — how many of the currently-live network observers
	// picked up this packet. Falls back to the bare count when the active total
	// isn't known yet.
	function observerCoverage(items: PacketObservation[]) {
		const observed = uniqueObserverCount(items);
		const active = runtimeStatus?.activeObservers ?? 0;
		const total = Math.max(active, observed);
		if (!total) return String(observed);
		return `${observed}/${total}`;
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

	function analyzerNodeUrl(publicKey: string) {
		return `${analyzerBase()}/#/nodes/${publicKey}`;
	}

	function analyzerBase() {
		const source = runtimeStatus?.analyzers[0]?.url || 'wss://analyzer.meshcore.cz';
		return source
			.replace(/^wss:\/\//, 'https://')
			.replace(/^ws:\/\//, 'http://')
			.replace(/\/$/, '');
	}

	function pathSignature(path?: (string | null | undefined)[]) {
		return (path ?? []).map((hop) => String(hop ?? '').toLowerCase()).join(',');
	}

	// The analyzer's map view wants its own observation id, which the websocket feed
	// doesn't carry. Match our observation to one in the analyzer's packet API by
	// observer + path and return its id, so the row can deep-link to that exact obs.
	function matchAnalyzerObsId(
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		list: any[],
		observation: PacketObservation
	): number | null {
		const observer = (observation.observerId || '').toLowerCase();
		if (!observer || !Array.isArray(list)) return null;
		const ours = pathSignature(observation.path);
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		const parsePathJson = (raw: any): string[] => {
			if (Array.isArray(raw)) return raw;
			try {
				const parsed = JSON.parse(raw ?? '[]');
				return Array.isArray(parsed) ? parsed : [];
			} catch {
				return [];
			}
		};
		const sameObserver = list.filter((o) => (o?.observer_id || '').toLowerCase() === observer);
		const exact = sameObserver.find((o) => pathSignature(parsePathJson(o?.path_json)) === ours);
		const match = exact ?? sameObserver[0];
		return typeof match?.id === 'number' ? match.id : null;
	}

	async function openObservationOnAnalyzer(observation: PacketObservation) {
		const base = analyzerBase();
		// Open the tab synchronously (inside the click) so popup blockers don't kill
		// it, then point it at the resolved deep link once the lookup returns.
		const win = window.open('', '_blank');
		let url = `${base}/#/map?packet=${observation.packetHash}`;
		try {
			// Proxied through our backend: the analyzer API sends no CORS headers, so a
			// direct browser fetch would be blocked.
			const res = await apiFetch(`/api/analyzer/packet/${observation.packetHash}`);
			if (res.ok) {
				const data = await res.json();
				const obsId = matchAnalyzerObsId(data?.packet?.observations ?? [], observation);
				if (obsId != null) url = `${base}/#/map?packet=${observation.packetHash}&obs=${obsId}`;
			}
		} catch {
			// CORS or network failure — fall back to the packet map without an obs id.
		}
		if (win) win.location.href = url;
		else window.open(url, '_blank', 'noreferrer');
	}

	function firstObservation(items: PacketObservation[]) {
		if (!items.length) return null;
		return items.reduce<PacketObservation | null>((earliest, observation) => {
			if (!earliest) return observation;
			return new Date(observation.createdAt).getTime() < new Date(earliest.createdAt).getTime()
				? observation
				: earliest;
		}, null);
	}

	function lastObservation(items: PacketObservation[]) {
		if (!items.length) return null;
		return items.reduce<PacketObservation | null>((latest, observation) => {
			if (!latest) return observation;
			return new Date(observation.createdAt).getTime() > new Date(latest.createdAt).getTime()
				? observation
				: latest;
		}, null);
	}

	function formatDurationMs(ms: number) {
		if (ms < 1000) return `${ms} ms`;
		return `${(ms / 1000).toFixed(2)} s`;
	}

	function duration(start?: string | null, end?: string | null) {
		if (!start || !end) return t('common.pending');
		const delta = new Date(end).getTime() - new Date(start).getTime();
		if (!Number.isFinite(delta) || delta < 0) return t('common.pending');
		return formatDurationMs(delta);
	}

	function hopLabel(value: string | number) {
		if (value === t('common.pending')) return t('common.pending');
		const count = Number(value);
		if (!Number.isFinite(count)) return String(value);
		return tn('unit.hop', count);
	}

	function observerCountLabel(items: PacketObservation[]) {
		return tn('unit.observer', uniqueObserverCount(items));
	}

	function afterPhrase(start?: string | null, end?: string | null) {
		if (!start || !end) return '';
		const value = duration(start, end);
		return value === t('common.pending') ? '' : t('progress.after', { duration: value });
	}

	function firstPacketSeenAt(current: DiagnosticTest) {
		return firstObservation(current.observations)?.createdAt || current.outboundSeenAt || null;
	}

	function propagationMs(items: PacketObservation[]) {
		const first = firstObservation(items);
		const last = lastObservation(items);
		if (!first || !last) return null;
		const delta = new Date(last.createdAt).getTime() - new Date(first.createdAt).getTime();
		return Number.isFinite(delta) && delta >= 0 ? delta : null;
	}

	function propagationTime(items: PacketObservation[]) {
		const ms = propagationMs(items);
		return ms === null ? t('common.pending') : formatDurationMs(ms);
	}

	// Combined spread of the outbound and return legs — how long packets kept
	// arriving across the whole round trip.
	function totalPropagation() {
		const out = propagationMs(messageObservations(outbound));
		const ret = propagationMs(messageObservations(returned));
		if (out === null && ret === null) return t('common.pending');
		return formatDurationMs((out ?? 0) + (ret ?? 0));
	}

	function deliveryTime(
		direction: 'outbound' | 'return',
		messages: PacketObservation[],
		acks: PacketObservation[],
		current: DiagnosticTest
	) {
		const firstMessage = firstObservation(messages);
		if (direction === 'outbound') {
			const endpointMessage = firstObservation(
				messages.filter((item) => isEndpointObservation(item, current))
			);
			return duration(
				firstMessage?.createdAt,
				endpointMessage?.createdAt || current.outboundEndpointSeenAt
			);
		}
		// Measure from the FIRST reply broadcast — never a later flood-fallback retry —
		// to the first reply ACK that came back. The first transmission is the earliest
		// of: the recorded broadcast time, the endpoint's own reply sighting, and the
		// first reply seen on the mesh. We floor those to the endpoint's receipt of the
		// user message, so a clock-skewed broadcast that lands implausibly early (before
		// the endpoint even had the message) is discarded rather than inflating the span.
		const endpointReply = firstObservation(
			messages.filter((item) => isEndpointObservation(item, current))
		);
		const floorMs = current.outboundEndpointSeenAt
			? new Date(current.outboundEndpointSeenAt).getTime()
			: 0;
		const replyBroadcastMs = current.replyBroadcastAt
			? new Date(current.replyBroadcastAt).getTime()
			: NaN;
		const startCandidates = [
			Number.isFinite(replyBroadcastMs) ? Math.max(replyBroadcastMs, floorMs) : null,
			endpointReply?.createdAt,
			firstMessage?.createdAt
		]
			.filter((value): value is string | number => value != null && value !== '')
			.map((value) => (typeof value === 'number' ? value : new Date(value).getTime()))
			.filter((ms) => Number.isFinite(ms) && ms >= floorMs);
		const replyStart = startCandidates.length
			? new Date(Math.min(...startCandidates)).toISOString()
			: current.replyBroadcastAt || endpointReply?.createdAt || firstMessage?.createdAt;
		const firstAck = firstObservation(acks);
		const endpointAck = firstObservation(
			acks.filter((item) => isEndpointObservation(item, current))
		);
		const endCandidates = [
			current.replyEndpointAckAt,
			endpointAck?.createdAt,
			current.replyAckSeenAt,
			firstAck?.createdAt
		]
			.filter((value): value is string => Boolean(value))
			.map((value) => new Date(value).getTime())
			.filter(Number.isFinite);
		if (!endCandidates.length) return duration(replyStart, null);
		const replyEnd = Math.max(...endCandidates);
		const boundedStartCandidates = startCandidates.filter((ms) => ms <= replyEnd);
		const boundedReplyStart = boundedStartCandidates.length
			? new Date(Math.min(...boundedStartCandidates)).toISOString()
			: replyStart;
		return duration(boundedReplyStart, new Date(replyEnd).toISOString());
	}

	function packetHashButtonLabel(hash: string) {
		return hash.length > 6 ? hash.slice(0, 6) : hash;
	}

	// Delivery rows are built server-side (English); translate their edge labels here
	// using the row key (`${direction}:start|end|${index}:…`) and the test state.
	// Shortest badge length (2 or 4 chars) that keeps every node in a path
	// distinguishable, so the circle never overflows with long hashes.
	function deliveryBadgeLen(rows: DeliveryPathOption['rows']) {
		const shorts = rows.map((row) => row.short ?? '');
		const unique2 = new Set(shorts.map((short) => short.slice(0, 2))).size === shorts.length;
		return unique2 ? 2 : 4;
	}

	function deliveryBadge(short: string, len: number) {
		return short.length <= len ? short : short.slice(0, len);
	}

	function deliveryRowName(
		row: DeliveryPathOption['rows'][number],
		current: DiagnosticTest,
		direction: 'outbound' | 'return'
	) {
		if (row.key.endsWith(':start'))
			return direction === 'outbound' ? t('delivery.userApp') : current.endpointName;
		if (row.key.endsWith(':end'))
			return direction === 'outbound' ? current.endpointName : t('delivery.userApp');
		return row.name;
	}

	function deliveryRowMeta(
		row: DeliveryPathOption['rows'][number],
		current: DiagnosticTest,
		direction: 'outbound' | 'return'
	) {
		if (row.key.endsWith(':start'))
			return direction === 'outbound' ? t('delivery.sentMessage') : t('delivery.sentReply');
		if (row.key.endsWith(':end')) {
			if (direction === 'outbound')
				return current.outboundEndpointSeenAt
					? t('delivery.endpointReceived')
					: t('delivery.endpointPending');
			return current.returnSeenAt ? t('delivery.userReceived') : t('delivery.userPending');
		}
		const index = Number(row.key.split(':')[1]);
		return t('delivery.hop', { n: Number.isFinite(index) ? index + 1 : '' });
	}

	function formatDistance(value?: number | null) {
		if (!Number.isFinite(value)) return t('common.na');
		if (value! < 1) return `${Math.round(value! * 1000)} m`;
		return `${value!.toFixed(value! < 10 ? 1 : 0)} km`;
	}

	function packetKindOptions() {
		return new SetLike((test?.observations ?? []).map(kindRetryKey)).values;
	}

	function toggleKind(kind: string) {
		selectedKinds = selectedKinds.includes(kind)
			? selectedKinds.filter((item) => item !== kind)
			: [...selectedKinds, kind];
	}

	function clearKindFilter() {
		selectedKinds = [];
	}

	function kindFilterClass(kind: string) {
		if (!selectedKinds.includes(kind))
			return 'bg-neutral-100 text-neutral-700 hover:bg-neutral-200';
		if (kind.includes('ack')) return 'bg-cyan-100 text-cyan-800';
		if (kind.includes('reply')) return 'bg-orange-100 text-orange-800';
		return 'bg-teal-100 text-teal-800';
	}

	function progressPercent(current: DiagnosticTest) {
		if (current.replyEndpointAckAt || current.status === 'completed') return 100;
		if (current.replyAckSeenAt) return 83;
		if (current.returnSeenAt) return 67;
		if (current.replyBroadcastAt) return 50;
		if (current.outboundEndpointSeenAt) return 33;
		if (current.outboundSeenAt) return 17;
		return 0;
	}

	function activeProgressIndex(current: DiagnosticTest) {
		const percent = progressPercent(current);
		if (percent >= 100) return 5;
		if (percent >= 83) return 4;
		if (percent >= 67) return 3;
		if (percent >= 50) return 2;
		if (percent >= 33) return 1;
		return 0;
	}

	function stepCompleted(current: DiagnosticTest, index: number) {
		const percent = progressPercent(current);
		return percent >= Math.round(((index + 1) / 6) * 100);
	}

	function waitingFor(current: DiagnosticTest) {
		if (current.status === 'completed') return t('progress.waiting.completed');
		if (current.status === 'failed') return t('progress.waiting.failed');
		if (current.status === 'expired') return t('progress.waiting.expired');
		if (!current.outboundSeenAt) return t('progress.waiting.outbound');
		if (endpointAgentKnownUnavailable(current)) return t('progress.waiting.agentOffline');
		if (!current.outboundEndpointSeenAt) return t('progress.waiting.endpoint');
		if (!current.replyBroadcastAt) return t('progress.waiting.reply');
		if (!current.returnSeenAt) return t('progress.waiting.return');
		if (!current.replyAckSeenAt) return t('progress.waiting.replyAck');
		return t('progress.waiting.endpointAck');
	}

	function progressSteps(current: DiagnosticTest) {
		const firstPacket = firstPacketSeenAt(current);
		const outboundMessages = messageObservations(outbound);
		const returnMessages = messageObservations(returned);
		const returnAcks = ackObservations(returned);
		const endpointOutbound = firstObservation(
			outboundMessages.filter((item) => isEndpointObservation(item, current))
		);
		const endpointAck = firstObservation(
			returnAcks.filter((item) => isEndpointObservation(item, current))
		);

		return [
			{
				label: t('progress.step.outboundSeen'),
				detail: current.outboundSeenAt
					? t('progress.detail.outboundSeen', {
							observers: observerCountLabel(outboundMessages),
							after: afterPhrase(firstPacket, current.outboundSeenAt)
						})
					: t('progress.detail.outboundSeenWaiting')
			},
			{
				label: t('progress.step.endpointSaw'),
				detail: current.outboundEndpointSeenAt
					? t('progress.detail.endpointSaw', {
							after: afterPhrase(firstPacket, current.outboundEndpointSeenAt),
							hops: hopLabel(endpointOutbound?.hopCount ?? bestHopCount(outboundMessages))
						})
					: endpointAgentKnownUnavailable(current)
						? t('progress.detail.endpointSawOffline')
						: t('progress.detail.endpointSawWaiting')
			},
			{
				label: t('progress.step.replySent'),
				detail: current.replyBroadcastAt
					? t('progress.detail.replySent', {
							after: afterPhrase(
								current.outboundEndpointSeenAt || current.outboundSeenAt,
								current.replyBroadcastAt
							)
						})
					: current.outboundEndpointSeenAt
						? current.replyStatus || t('progress.detail.replySentPreparing')
						: t('progress.detail.replySentBlocked')
			},
			{
				label: t('progress.step.replySeen'),
				detail: current.returnSeenAt
					? t('progress.detail.replySeen', {
							observers: observerCountLabel(returnMessages),
							after: afterPhrase(current.replyBroadcastAt, current.returnSeenAt),
							hops: hopLabel(bestHopCount(returnMessages))
						})
					: current.replyAckSeenAt
						? t('progress.detail.replySeenInferred', {
								after: afterPhrase(current.replyBroadcastAt, current.replyAckSeenAt)
							})
						: current.replyBroadcastAt
							? t('progress.detail.replySeenWaiting')
							: t('progress.detail.replySeenBlocked')
			},
			{
				label: t('progress.step.ackSeen'),
				detail: current.replyAckSeenAt
					? t('progress.detail.ackSeen', {
							observers: observerCountLabel(returnAcks),
							after: afterPhrase(current.returnSeenAt, current.replyAckSeenAt)
						})
					: current.returnSeenAt
						? t('progress.detail.ackSeenWaiting')
						: t('progress.detail.ackSeenBlocked')
			},
			{
				label: t('progress.step.ackReturned'),
				detail: current.replyEndpointAckAt
					? t('progress.detail.ackReturned', {
							after: afterPhrase(current.replyAckSeenAt, current.replyEndpointAckAt),
							hops: hopLabel(endpointAck?.hopCount ?? bestHopCount(returnAcks))
						})
					: current.replyAckSeenAt
						? t('progress.detail.ackReturnedWaiting')
						: t('progress.detail.ackReturnedBlocked')
			}
		];
	}

	function endpointCoords(): LatLng | null {
		const lat = test?.endpointLocation?.lat;
		const lon = test?.endpointLocation?.lon;
		return Number.isFinite(lat) && Number.isFinite(lon) ? [lat!, lon!] : null;
	}

	function ensureMap() {
		if (!leaflet || !mapElement) return;
		// Toggling the full-width layout moves the map into a different DOM element, so
		// rebuild the Leaflet instance whenever the bound element changes.
		if (mapInstance && mapBuiltOn === mapElement) return;
		if (mapInstance) {
			mapInstance.remove();
			mapInstance = null;
			routeLayer = null;
		}
		const center = endpointCoords() || [50.478, 13.975];
		mapInstance = leaflet.map(mapElement, {
			zoomAnimation: false,
			markerZoomAnimation: false,
			fadeAnimation: false,
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
		mapBuiltOn = mapElement;
	}

	function renderMap() {
		if (!leaflet || !mapInstance || !routeLayer || !test?.propagationMap) return;
		routeLayer.clearLayers();

		const bounds: LatLng[] = [];

		for (const segment of test.propagationMap.segments) {
			if (hiddenMapLinks.includes(segment.kind)) continue;
			const from: LatLng = segment.from;
			const to: LatLng = segment.to;
			leaflet
				.polyline([from, to], {
					color: kindColor(segment.kind),
					weight: 3,
					opacity: 0.5,
					dashArray: segment.direction === 'return' ? '8 8' : undefined
				})
				.addTo(routeLayer);
			bounds.push(from, to);
		}

		for (const point of test.propagationMap.points) {
			if (hiddenMapPoints.includes(point.kind)) continue;
			const coords: LatLng = [point.lat, point.lon];
			leaflet
				.circleMarker(coords, {
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
			bounds.push(coords);
		}

		if (bounds.length >= 2) mapInstance.fitBounds(bounds, { padding: [24, 24], maxZoom: 12 });
		else if (bounds.length === 1) mapInstance.setView(bounds[0], 10);
		setTimeout(() => mapInstance?.invalidateSize(), 0);
	}

	// Console offsets are always forward from the first event (the stream is clamped
	// monotonic upstream), so they read as "+Xms" / "+X.XXs".
	function consoleOffset(ms: number) {
		if (ms < 1000) return `+${Math.round(ms)}ms`;
		return `+${(ms / 1000).toFixed(2)}s`;
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

	function nodeFor(key?: string | null) {
		return key ? (test?.nodes?.[key] ?? null) : null;
	}

	// Mirrors the server's map radius: a hop resolved beyond this from the endpoint
	// is treated as a bogus path-hash collision, not a real relay.
	const MAX_MAP_DISTANCE_KM = 2000;

	function haversineKm(lat1: number, lon1: number, lat2: number, lon2: number) {
		const earthKm = 6371.0088;
		const toRad = (v: number) => (v * Math.PI) / 180;
		const dLat = toRad(lat2 - lat1);
		const dLon = toRad(lon2 - lon1);
		const a =
			Math.sin(dLat / 2) ** 2 +
			Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLon / 2) ** 2;
		return earthKm * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
	}

	// Whether a coordinate sits implausibly far from the endpoint (out of map range).
	function coordTooFar(lat?: number | null, lon?: number | null) {
		if (!Number.isFinite(lat) || !Number.isFinite(lon)) return false;
		const ep = endpointCoords();
		if (!ep) return false;
		return haversineKm(ep[0], ep[1], lat as number, lon as number) > MAX_MAP_DISTANCE_KM;
	}

	function compactPath(observation: PacketObservation) {
		return observation.path.map((hash, index) => {
			const ref = nodeFor(observation.pathKeys[index]);
			const outOfRange = coordTooFar(ref?.lat, ref?.lon);
			return {
				name: ref?.name || hash,
				hex: String(hash).toUpperCase(),
				publicKey: ref?.publicKey ?? null,
				shortHash: ref?.shortHash || hash,
				lat: ref?.lat,
				lon: ref?.lon,
				// A bogus far coordinate is no better than none for the map/distance.
				hasCoords: Number.isFinite(ref?.lat) && Number.isFinite(ref?.lon) && !outOfRange,
				outOfRange
			};
		});
	}

	type CompactHop = ReturnType<typeof compactPath>[number];

	// Path-chip styling: out-of-range hops get a distinct (rose) background, hops
	// with no usable coordinates stay amber, located hops are neutral.
	function hopChipClass(hop: CompactHop, interactive: boolean) {
		if (hop.outOfRange)
			return interactive
				? 'bg-rose-100 text-rose-700 hover:bg-rose-200'
				: 'bg-rose-100 text-rose-700';
		if (!hop.hasCoords)
			return interactive
				? 'bg-orange-100 text-orange-700 hover:bg-orange-200'
				: 'bg-orange-100 text-orange-700';
		return interactive
			? 'bg-neutral-100 text-neutral-600 hover:bg-teal-100 hover:text-teal-800'
			: 'bg-neutral-100 text-neutral-600';
	}

	function hopChipTitle(hop: CompactHop) {
		return hop.outOfRange ? t('obs.path.outOfRange') : undefined;
	}

	// Delivery paths follow the app's directional convention: outbound (to the
	// endpoint) is teal, return (from the endpoint) is orange — matching the route
	// cards and map links. `line` feeds the CSS connector gradient via --delivery-line.
	function deliveryAccent(direction: 'outbound' | 'return') {
		return direction === 'outbound'
			? {
					rowHover: 'hover:bg-teal-50',
					edgeBadge: 'bg-teal-100 text-teal-900 group-hover:bg-teal-200',
					hopBadge:
						'bg-neutral-100 text-neutral-700 group-hover:bg-teal-100 group-hover:text-teal-900',
					link: 'text-neutral-300 group-hover:text-teal-700',
					line: 'rgba(20, 184, 166, 0.36)'
				}
			: {
					rowHover: 'hover:bg-orange-50',
					edgeBadge: 'bg-orange-100 text-orange-900 group-hover:bg-orange-200',
					hopBadge:
						'bg-neutral-100 text-neutral-700 group-hover:bg-orange-100 group-hover:text-orange-900',
					link: 'text-neutral-300 group-hover:text-orange-700',
					line: 'rgba(234, 88, 12, 0.36)'
				};
	}

	// Retries = how many times a logical message was re-sent. Relays of one
	// transmission share a content hash, so distinct content hashes for a kind are
	// distinct transmissions; one of them is the original. Captures both the user's
	// app re-sending and our own reply flood-fallback re-sends.
	function retriesForKind(kind: string) {
		const hashes = new Set(
			(test?.observations ?? [])
				.filter((observation) => packetKindLabel(observation) === kind)
				.map((observation) => observation.packetHash)
				.filter(Boolean)
		);
		return Math.max(0, hashes.size - 1);
	}

	// Tooltip text for a conflicted delivery hop: the chosen node plus the rivals.
	function conflictTitle(row: DeliveryPathRow) {
		const names = (row.alternatives ?? []).map(
			(node) => node.name || node.shortHash || t('common.na')
		);
		const header = t('delivery.conflict', { n: names.length + 1 });
		return names.length ? `${header}\n${t('delivery.conflictOthers')} ${names.join(', ')}` : header;
	}

	function isEndpointObservation(observation: PacketObservation, current: DiagnosticTest) {
		const observer = (observation.observerId || observation.observerKey || '').toLowerCase();
		const endpointKey = current.endpointPublicKey.toLowerCase();
		return (
			observation.source.startsWith('agent:') ||
			observer === endpointKey ||
			Boolean(observation.observerName?.toLowerCase().includes(current.endpointName.toLowerCase()))
		);
	}

	function observerLabel(observation: PacketObservation) {
		return nodeFor(observation.observerKey)?.name || observation.observerName || observation.source;
	}

	function endpointAgentStatus(current: DiagnosticTest) {
		return runtimeStatus?.endpoints.find((endpoint) => endpoint.id === current.endpointId);
	}

	function endpointAgentReady(current: DiagnosticTest) {
		return Boolean(endpointAgentStatus(current)?.ready);
	}

	function endpointAgentKnownUnavailable(current: DiagnosticTest) {
		return Boolean(runtimeStatus && !endpointAgentReady(current));
	}

	function endpointAgentWarning(current: DiagnosticTest) {
		if (!runtimeStatus || current.status === 'completed' || current.status === 'expired') return '';
		const endpoint = endpointAgentStatus(current);
		if (endpoint?.ready) return '';
		if (endpoint?.connected) {
			return t('detail.agent.connected', { name: current.endpointName });
		}
		return t('detail.agent.offline', { name: current.endpointName });
	}

	type ConsoleTone = 'start' | 'endpoint' | 'outboundAck' | 'reply' | 'ack' | 'retry' | 'done';
	type ConsoleEvent = {
		id: string;
		time: string;
		/** Logical position in the round trip; the console orders by this, not raw time. */
		order: number;
		/** Clamped, monotonic offset (ms) from the first event, used for display. */
		relMs: number;
		tone: ConsoleTone;
		/** Title text; contains literal `{hash}`/`{msgHash}` slots where chips render. */
		title: string;
		/** Content hash of the packet this event is about, for the inline chip + link. */
		hash: string | null;
		/** Secondary hash for `{msgHash}` — e.g. the message an ACK acknowledges. */
		msgHash: string | null;
		detail: string;
	};

	const CONSOLE_TONE: Record<ConsoleTone, { dot: string; text: string }> = {
		start: { dot: 'bg-teal-500', text: 'text-teal-700' },
		endpoint: { dot: 'bg-teal-600', text: 'text-teal-800' },
		outboundAck: { dot: 'bg-blue-600', text: 'text-blue-700' },
		reply: { dot: 'bg-orange-500', text: 'text-orange-700' },
		ack: { dot: 'bg-pink-500', text: 'text-pink-700' },
		retry: { dot: 'bg-amber-500', text: 'text-amber-700' },
		done: { dot: 'bg-emerald-600', text: 'text-emerald-700' }
	};

	// A curated event stream of the round-trip milestones plus notable packets
	// (retries). It's ordered by the logical sequence of the round trip — not raw
	// timestamps — because some milestones (e.g. replyBroadcastAt) can carry a
	// stale/clock-skewed time that would otherwise jump them out of order. Displayed
	// offsets are clamped to stay non-negative and monotonic, so a skewed timestamp
	// reads as +0ms in its proper slot rather than a nonsensical negative jump.
	function consoleEvents(current: DiagnosticTest): ConsoleEvent[] {
		type Raw = Omit<ConsoleEvent, 'relMs'>;
		const raws: Raw[] = [];
		const add = (
			time: string | null | undefined,
			order: number,
			tone: ConsoleTone,
			title: string,
			hash: string | null | undefined,
			detail: string,
			msgHash?: string | null
		) => {
			if (time)
				raws.push({
					id: `${tone}:${title}:${time}`,
					time,
					order,
					tone,
					title,
					hash: hash || null,
					msgHash: msgHash || null,
					detail
				});
		};
		const outMsgs = messageObservations(outbound);
		const retMsgs = messageObservations(returned);
		const endpointOut = firstObservation(outMsgs.filter((o) => isEndpointObservation(o, current)));
		const ep = { endpoint: current.endpointName };
		// Endpoint-leg steps (receive/send) show the endpoint↔user hop distance instead
		// of an observer count — the endpoint is the sender/receiver there, not a
		// third-party sighting.
		const endpointHops = hopLabel(endpointOut?.hopCount ?? bestHopCount(outMsgs));
		add(
			current.outboundSeenAt,
			10,
			'start',
			t('console.event.outboundSeen'),
			current.outboundHash,
			observerCountLabel(outMsgs)
		);
		add(
			current.outboundEndpointSeenAt,
			20,
			'endpoint',
			t('console.event.endpointSaw', ep),
			endpointOut?.packetHash || current.outboundHash,
			endpointHops
		);
		// The endpoint's acknowledgment answering the user's message, sent before the
		// reply. Names both the ACK packet ({hash}) and the message it acks ({msgHash}).
		add(
			current.outboundAckSeenAt,
			28,
			'outboundAck',
			t('console.event.outboundAck', ep),
			current.outboundAckHash,
			endpointHops,
			current.outboundHash
		);
		add(
			current.replyBroadcastAt,
			30,
			'reply',
			t('console.event.replySent', ep),
			current.replyHash,
			endpointHops
		);
		// The reply is sent by the endpoint, so its own RF self-echo isn't a real
		// observer — count only third-party sightings.
		const replyObservers = externalObservers(retMsgs, current);
		add(
			current.returnSeenAt,
			40,
			'reply',
			t('console.event.replySeen'),
			current.returnHash || current.replyHash,
			replyObservers.length ? observerCountLabel(replyObservers) : ''
		);
		// The reply ACK is sent by the user, so the endpoint is a genuine observer —
		// count all of them, no self-echo exclusion.
		const retAcks = ackObservations(returned);
		add(
			current.replyAckSeenAt,
			50,
			'ack',
			t('console.event.ackSeen'),
			current.replyAckHash,
			retAcks.length ? observerCountLabel(retAcks) : '',
			current.returnHash || current.replyHash
		);
		add(
			current.replyEndpointAckAt,
			60,
			'done',
			t('console.event.ackReturned', ep),
			current.replyAckHash,
			endpointHops
		);
		// Retries: each new content hash for a message kind (after the first) is a
		// fresh transmission, i.e. a re-send. Slotted next to the kind they re-send.
		for (const [kind, order] of [
			['user msg', 25],
			['reply', 45]
		] as const) {
			const ordered = (current.observations ?? [])
				.filter((o) => packetKindLabel(o) === kind && o.packetHash)
				.slice()
				.sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime());
			const seen = new Set<string>();
			for (const o of ordered) {
				if (seen.has(o.packetHash)) continue;
				seen.add(o.packetHash);
				if (seen.size > 1)
					add(
						o.createdAt,
						order,
						'retry',
						t('console.event.retry', { packet: capitalize(t(`console.packet.${kind}`)) }),
						o.packetHash,
						tn('unit.retry', seen.size - 1)
					);
			}
		}
		raws.sort(
			(a, b) => a.order - b.order || new Date(a.time).getTime() - new Date(b.time).getTime()
		);
		// Clamp display offsets to the first event so they never run negative or
		// backwards, even when an underlying timestamp is skewed.
		const base = raws.length ? new Date(raws[0].time).getTime() : 0;
		let prev = 0;
		return raws.map((raw) => {
			const delta = new Date(raw.time).getTime() - base;
			const relMs = Math.max(prev, Number.isFinite(delta) ? delta : prev, 0);
			prev = relMs;
			return { ...raw, relMs };
		});
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

<svelte:head>
	<title>{pageTitle}</title>
	<meta name="description" content={pageDescription} />
	<!-- Per-test diagnostic pages are ephemeral and per-browser, so keep them out of search indexes. -->
	<meta name="robots" content="noindex, nofollow" />
	<meta property="og:type" content="website" />
	<meta property="og:title" content={pageTitle} />
	<meta property="og:description" content={pageDescription} />
	<meta name="twitter:card" content="summary" />
	<meta name="twitter:title" content={pageTitle} />
	<meta name="twitter:description" content={pageDescription} />
</svelte:head>

<main class="mx-auto flex min-h-screen w-full max-w-7xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
	<div class="flex items-center justify-between gap-2 overflow-x-auto pb-1">
		<a
			class="inline-flex h-10 shrink-0 items-center gap-2 rounded-md border border-neutral-300 bg-white px-3 text-sm font-medium text-neutral-800 hover:bg-neutral-100"
			href={resolve('/')}
		>
			<ArrowLeft size={16} />
			{t('common.back')}
		</a>

		<div class="flex shrink-0 items-center gap-2">
			{#if test && (test.status === 'completed' || test.status === 'failed' || test.status === 'expired')}
				<button
					type="button"
					onclick={repeatTest}
					disabled={repeating}
					class="inline-flex size-10 shrink-0 items-center justify-center gap-2 rounded-md border border-neutral-300 bg-white text-sm font-medium text-teal-700 transition hover:border-teal-300 hover:bg-teal-50 disabled:cursor-not-allowed disabled:opacity-60 sm:w-auto sm:px-3"
					title={t('detail.repeat.hint')}
					aria-label={t('detail.repeat.button')}
				>
					{#if repeating}
						<LoaderCircle size={16} class="animate-spin" />
					{:else}
						<RotateCw size={16} />
					{/if}
					<span class="hidden sm:inline">{t('detail.repeat.button')}</span>
				</button>
			{/if}
			<LanguageSwitcher />
		</div>
	</div>

	{#if error}
		<section class="rounded-md border border-red-200 bg-red-50 p-5 text-red-800">{error}</section>
	{:else if !test}
		<section
			class="flex min-h-[70vh] flex-1 flex-col items-center justify-center gap-4 text-neutral-500"
			in:fade={{ duration: 200 }}
		>
			<LoaderCircle size={40} class="animate-spin text-teal-700" />
			<p class="text-sm font-medium">{t('detail.loading', { id: page.params.id ?? '' })}</p>
		</section>
	{:else}
		<header
			class="grid gap-4 rounded-md border border-neutral-300 bg-white p-4 shadow-sm lg:grid-cols-[1fr_auto] lg:items-center"
		>
			<div>
				<div class="mb-3 flex flex-wrap items-center gap-2">
					<span class={`rounded-md border px-2.5 py-1 text-sm font-semibold ${statusTone}`}
						>{t(`status.${test.status}`)}</span
					>
					<span class="text-sm text-neutral-500">{test.endpointRegion}</span>
				</div>
				<h1 class="text-2xl font-semibold text-neutral-950">
					{t('detail.title', { id: test.id, name: test.endpointName })}
				</h1>
				<div class="mt-2 flex flex-wrap gap-2 text-xs">
					<span
						class="inline-flex items-center gap-1 rounded border border-neutral-200 bg-neutral-50 px-2 py-1 text-neutral-600"
					>
						<KeyRound size={13} />
						<span>{t('detail.endpoint')}</span>
						<span class="mono font-semibold text-neutral-900"
							>{test.endpointPublicKey.slice(0, 8)}</span
						>
					</span>
					<span
						class="inline-flex items-center gap-1 rounded border border-neutral-200 bg-neutral-50 px-2 py-1 text-neutral-600"
					>
						<KeyRound size={13} />
						<span>{t('detail.user')}</span>
						<span class="mono font-semibold text-neutral-900">{test.userPublicKey.slice(0, 8)}</span
						>
					</span>
					<span
						class="mono inline-flex items-center rounded border border-neutral-200 bg-neutral-50 px-2 py-1 font-semibold text-neutral-700"
					>
						{test.id}
					</span>
					<span
						class="inline-flex items-center gap-1 rounded border border-neutral-200 bg-neutral-50 px-2 py-1 text-neutral-600"
					>
						<Clock size={13} />
						{formatDateTime(test.createdAt)}
					</span>
				</div>
			</div>

			<div class="grid grid-cols-2 gap-2 text-sm sm:grid-cols-4">
				<div
					class={`rounded-md border px-3 py-2 transition ${outbound.length ? 'border-teal-200 bg-teal-50' : 'border-neutral-200 bg-white'}`}
				>
					<div class="flex items-center gap-2">
						<span
							class={`grid size-7 place-items-center rounded ${outbound.length ? 'bg-teal-100 text-teal-800' : 'bg-neutral-100 text-neutral-500'}`}
						>
							{#if outbound.length}<CheckCircle2 size={16} />{:else}<Send size={16} />{/if}
						</span>
						<div>
							<p class="text-neutral-500">{t('detail.card.outbound')}</p>
							<p class="font-semibold">
								{outbound.length
									? t('detail.seenCount', { n: outbound.length })
									: t('detail.waiting')}
							</p>
						</div>
					</div>
				</div>
				<div
					class={`rounded-md border px-3 py-2 transition ${returned.length ? 'border-orange-200 bg-orange-50' : 'border-neutral-200 bg-white'}`}
				>
					<div class="flex items-center gap-2">
						<span
							class={`grid size-7 place-items-center rounded ${returned.length ? 'bg-orange-100 text-orange-800' : 'bg-neutral-100 text-neutral-500'}`}
						>
							{#if returned.length}<CheckCircle2 size={16} />{:else}<Route size={16} />{/if}
						</span>
						<div>
							<p class="text-neutral-500">{t('detail.card.return')}</p>
							<p class="font-semibold">
								{returned.length
									? t('detail.seenCount', { n: returned.length })
									: t('detail.waiting')}
							</p>
						</div>
					</div>
				</div>
				<div class="rounded-md border border-blue-200 bg-blue-50 px-3 py-2 transition">
					<div class="flex items-center gap-2">
						<span class="grid size-7 place-items-center rounded bg-blue-100 text-blue-800">
							<Clock size={16} />
						</span>
						<div>
							<p class="text-neutral-500">{t('detail.card.elapsed')}</p>
							<p class="font-semibold">
								{latency(firstPacketSeenAt(test), test.replyEndpointAckAt)}
							</p>
						</div>
					</div>
				</div>
				<div class="rounded-md border border-violet-200 bg-violet-50 px-3 py-2 transition">
					<div class="flex items-center gap-2">
						<span class="grid size-7 place-items-center rounded bg-violet-100 text-violet-800">
							<Radio size={16} />
						</span>
						<div>
							<p class="text-neutral-500">{t('detail.card.propagation')}</p>
							<p class="font-semibold">{totalPropagation()}</p>
						</div>
					</div>
				</div>
			</div>
		</header>

		{#if expiredWithoutPackets}
			{@render ExpiredWithoutPacketsPanel()}
		{:else}
			{#if endpointAgentWarning(test)}
				<section class="rounded-md border border-red-200 bg-red-50 p-4 text-red-800 shadow-sm">
					<div class="flex gap-3">
						<AlertCircle size={22} class="mt-0.5 shrink-0" />
						<div>
							<h2 class="font-semibold">{t('detail.agent.title')}</h2>
							<p class="mt-1 text-sm text-red-700">{endpointAgentWarning(test)}</p>
						</div>
					</div>
				</section>
			{/if}

			{@render ProgressPanel(test)}

			{#if hasObserved && mapFullWidth}
				{@render MapPanel(true)}
			{/if}

			<section class="grid min-w-0 gap-5 lg:grid-cols-[360px_minmax(0,1fr)] lg:items-start">
				<aside class="grid min-w-0 content-start gap-4">
					{#if hasObserved && !mapFullWidth}
						{@render MapPanel(false)}
					{:else if !hasObserved}
						{@render InstructionsPanel(test)}
					{/if}
					{@render DeliveryPathsPanel(test)}
					{@render PathStatisticsPanel(test)}
				</aside>

				<div class="grid min-w-0 gap-5">
					<section class="grid min-w-0 gap-4 md:grid-cols-2">
						{@render RoutePanel(t('route.userToEndpoint'), 'send', outbound)}
						{@render RoutePanel(t('route.endpointToUser'), 'return', returned)}
					</section>

					<section class="min-w-0 rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
						<div class="mb-4 flex flex-wrap items-center justify-between gap-3">
							<div class="flex items-center gap-2">
								<Clock size={18} class="text-amber-700" />
								<h2 class="text-lg font-semibold text-neutral-950">{t('obs.title')}</h2>
								<div class="ml-1 inline-flex rounded-md border border-neutral-300 p-0.5 text-xs">
									<button
										type="button"
										onclick={() => (obsView = 'table')}
										class={`inline-flex items-center gap-1 rounded px-2 py-1 font-medium transition ${obsView === 'table' ? 'bg-neutral-900 text-white' : 'text-neutral-600 hover:bg-neutral-100'}`}
									>
										<ClipboardList size={13} />
										<span class="hidden sm:inline">{t('obs.view.table')}</span>
									</button>
									<button
										type="button"
										onclick={() => (obsView = 'console')}
										class={`inline-flex items-center gap-1 rounded px-2 py-1 font-medium transition ${obsView === 'console' ? 'bg-neutral-900 text-white' : 'text-neutral-600 hover:bg-neutral-100'}`}
									>
										<Terminal size={13} />
										<span class="hidden sm:inline">{t('obs.view.console')}</span>
									</button>
								</div>
							</div>
							{#if obsView === 'table'}
								<div class="flex items-center gap-3">
									<p class="text-sm text-neutral-500">
										{t('obs.summary', {
											shown: filteredObservations.length,
											total: test.observations.length,
											observers: uniqueObserverCount(filteredObservations),
											paths: uniquePathCount(filteredObservations)
										})}
									</p>
									<button
										type="button"
										onclick={toggleAllHex}
										class={`inline-flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-medium transition ${pathHex ? 'border-teal-300 bg-teal-50 text-teal-800' : 'border-neutral-300 text-neutral-600 hover:bg-neutral-100'}`}
										title={t('obs.path.toggle')}
									>
										<Hash size={13} />
										<span>{pathHex ? t('obs.path.names') : t('obs.path.hex')}</span>
									</button>
								</div>
							{/if}
						</div>
						{#if obsView === 'table'}
							<div class="mb-3 flex flex-wrap gap-2 text-xs">
								<button
									class={`rounded px-2 py-1 font-semibold transition ${selectedKinds.length === 0 ? 'bg-neutral-900 text-white' : 'bg-neutral-100 text-neutral-700 hover:bg-neutral-200'}`}
									type="button"
									onclick={clearKindFilter}
								>
									{t('obs.filter.all')}
								</button>
								{#each packetKindOptions() as kind (kind)}
									<button
										class={`rounded px-2 py-1 font-semibold transition ${kindFilterClass(kind)}`}
										type="button"
										onclick={() => toggleKind(kind)}
									>
										{kindKeyLabel(kind)}
									</button>
								{/each}
							</div>
							<div class="max-w-full overflow-x-auto">
								<table class="min-w-[680px] text-left text-xs sm:w-full sm:min-w-0">
									<thead class="border-b border-neutral-200 text-neutral-500">
										<tr>
											<th class="py-2 pr-2 font-semibold">{t('obs.col.time')}</th>
											<th class="px-2 py-2 font-semibold">{t('obs.col.kind')}</th>
											<th class="px-2 py-2 font-semibold">{t('obs.col.observer')}</th>
											<th class="px-2 py-2 font-semibold">{t('obs.col.hops')}</th>
											<th class="px-2 py-2 font-semibold">{t('obs.col.distance')}</th>
											<th class="px-2 py-2 font-semibold">{t('obs.col.path')}</th>
										</tr>
									</thead>
									<tbody class="divide-y divide-neutral-100">
										{#each [...filteredObservations] as observation (observation.id)}
											{@const firstSeen = firstObservationTime(test.observations)}
											{@const endpointObservation = isEndpointObservation(observation, test)}
											{@const rowIsHex = rowHex(observation.id)}
											<tr
												class={endpointObservation
													? 'group bg-teal-50/75 cursor-pointer text-neutral-950'
													: 'group cursor-pointer text-neutral-700 hover:bg-neutral-50'}
												onclick={(event) => {
													event.preventDefault();
													openObservationOnAnalyzer(observation);
												}}
											>
												<td class="mono py-2 pr-2 text-neutral-500">
													{relativeTime(
														observation.createdAt,
														new Date(firstSeen ?? test.createdAt).toISOString()
													)}
												</td>
												<td class="px-2 py-2">
													<span
														class={`inline-flex items-center rounded px-1.5 py-0.5 font-semibold ${packetKindClass(observation)}`}
													>
														{kindLabelWithRetry(observation)}
													</span>
												</td>
												<td class="px-2 py-2">
													<p class="max-w-32 truncate font-medium text-sm">
														{observerLabel(observation)}
													</p>
													{#if observation.observerId}
														<p class="mono max-w-32 truncate text-[10px] text-neutral-500">
															{observation.observerId}
														</p>
													{/if}
												</td>
												<td class="px-2 py-2">{observation.hopCount}</td>
												<td class="px-2 py-2 font-medium text-neutral-600">
													<div class="flex flex-col items-start gap-1">
														<span>{formatDistance(observation.distanceKm)}</span>
														{#if observation.path.length}
															<button
																type="button"
																title={t('obs.path.toggle')}
																aria-pressed={rowIsHex}
																class={`size-5 place-items-center rounded border transition focus:grid ${rowIsHex ? 'grid border-teal-300 bg-teal-50 text-teal-700' : 'hidden border-neutral-200 text-neutral-400 hover:bg-neutral-100 hover:text-neutral-700 group-hover:grid'}`}
																onclick={(event) => {
																	event.stopPropagation();
																	toggleRowHex(observation.id);
																}}
															>
																<Hash size={12} />
															</button>
														{/if}
													</div>
												</td>
												<td class="max-w-64 overflow-x-auto px-2 py-2 sm:max-w-80">
													<div class="flex flex-wrap items-center gap-1">
														{#each compactPath(observation) as hop, index (`${hop.publicKey || hop.shortHash}:${index}`)}
															{#if index > 0}
																<span class="text-[10px] text-neutral-400">→</span>
															{/if}
															{#if hop.publicKey}
																<button
																	type="button"
																	title={hopChipTitle(hop)}
																	class={`mono rounded px-1.5 py-0.5 text-[10px] transition-colors ${hopChipClass(hop, true)}`}
																	onclick={(event) => {
																		event.stopPropagation();
																		if (hop.publicKey)
																			window.open(
																				analyzerNodeUrl(hop.publicKey),
																				'_blank',
																				'noreferrer'
																			);
																	}}>{rowIsHex ? hop.hex : hop.name}</button
																>
															{:else}
																<span
																	title={hopChipTitle(hop)}
																	class={`mono rounded px-1.5 py-0.5 text-[10px] ${hopChipClass(hop, false)}`}
																	>{rowIsHex ? hop.hex : hop.name}</span
																>
															{/if}
														{/each}
													</div>
												</td>
											</tr>
										{:else}
											<tr>
												<td colspan="6" class="py-5 text-center text-sm text-neutral-500">
													{t('obs.waiting')}
												</td>
											</tr>
										{/each}
									</tbody>
								</table>
							</div>
						{:else}
							{@render ConsolePanel(test)}
						{/if}
					</section>
				</div>
			</section>

			<section class="grid gap-3 md:grid-cols-4">
				{@render Metric(t('metric.outboundHops'), bestHopCount(outbound))}
				{@render Metric(t('metric.returnHops'), bestHopCount(returned))}
				{@render Metric(t('metric.reply'), test.replyStatus || t('common.pending'))}
				{@render Metric(
					t('metric.expires'),
					new Date(test.expiresAt).toLocaleTimeString(localeTag())
				)}
			</section>
		{/if}
	{/if}
	<footer class="pb-3 text-center text-sm text-neutral-500">
		{t('footer.credit')}
		<a
			class="font-medium text-neutral-700 hover:text-teal-800"
			href="https://github.com/meshcore-cz/hopback"
		>
			meshcore-cz/hopback
		</a>
		<span class="mx-1 text-neutral-400">·</span>
		<span>v{APP_VERSION}</span>
	</footer>
</main>

{#snippet ExpiredWithoutPacketsPanel()}
	<section class="rounded-md border border-red-200 bg-red-50 p-6 text-red-900 shadow-sm">
		<div class="flex flex-col gap-4 sm:flex-row sm:items-start">
			<span class="grid size-12 shrink-0 place-items-center rounded-md bg-red-100 text-red-700">
				<XCircle size={26} />
			</span>
			<div>
				<h2 class="text-xl font-semibold">{t('detail.expiredEmpty.title')}</h2>
				<p class="mt-2 max-w-2xl text-sm leading-6 text-red-800">
					{t('detail.expiredEmpty.body')}
				</p>
			</div>
		</div>
	</section>
{/snippet}

{#snippet Metric(label: string, value: string)}
	<div class="rounded-md border border-neutral-300 bg-white p-3 shadow-sm">
		<p class="text-sm text-neutral-500">{label}</p>
		<p class="mt-1 truncate font-semibold text-neutral-950">{value}</p>
	</div>
{/snippet}

{#snippet ConsoleHashChip(hash: string)}
	<button
		class="mono inline-flex shrink-0 items-center gap-0.5 rounded border border-neutral-300 bg-neutral-50 px-1 py-0.5 align-baseline font-semibold text-neutral-500 hover:border-teal-700 hover:bg-teal-50 hover:text-teal-900"
		type="button"
		style="font-size: 11px; line-height: 1;"
		onclick={() => window.open(analyzerPacketUrl(hash), '_blank', 'noreferrer')}
		title={t('route.openMessage')}
	>
		<span>{packetHashButtonLabel(hash)}</span>
		<ExternalLink size={10} strokeWidth={2} />
	</button>
{/snippet}

{#snippet ConsolePanel(current: DiagnosticTest)}
	{@const events = consoleEvents(current)}
	{#if events.length}
		<ol class="relative ml-2 space-y-4 border-l border-neutral-200 py-1 pl-5">
			{#each events as event, index (event.id)}
				{@const tone = CONSOLE_TONE[event.tone]}
				{@const segments = event.title.split(/(\{hash\}|\{msgHash\})/)}
				<li class="console-event relative" style={`--row-index: ${index}`}>
					<span
						class={`absolute -left-[1.43rem] top-1 size-2.5 rounded-full ring-4 ring-white ${tone.dot}`}
					></span>
					<div class="flex items-baseline justify-between gap-3">
						<p
							class="flex flex-wrap items-baseline gap-x-1.5 text-sm font-semibold text-neutral-900"
						>
							{#each segments as segment}
								{#if segment === '{hash}'}
									{#if event.hash}{@render ConsoleHashChip(event.hash)}{/if}
								{:else if segment === '{msgHash}'}
									{#if event.msgHash}{@render ConsoleHashChip(event.msgHash)}{/if}
								{:else if segment.trim()}
									<span>{segment.trim()}</span>
								{/if}
							{/each}
						</p>
						<span class="mono shrink-0 text-[11px] text-neutral-400">
							{consoleOffset(event.relMs)}
						</span>
					</div>
					<!-- The final point closes the timeline; its detail (a stray hop count)
					     would dangle below the last dot, so we omit it. -->
					{#if event.detail && index !== events.length - 1}
						<p class={`mt-0.5 text-xs font-medium ${tone.text}`}>{event.detail}</p>
					{/if}
				</li>
			{/each}
		</ol>
	{:else}
		<div
			class="rounded-md border border-dashed border-neutral-300 bg-neutral-50 px-3 py-8 text-center text-sm text-neutral-500"
		>
			{t('console.empty')}
		</div>
	{/if}
{/snippet}

{#snippet MapPanel(full: boolean)}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex items-center gap-2">
			<Map size={18} class="text-teal-700" />
			<h2 class="text-lg font-semibold text-neutral-950">{t('map.title')}</h2>
			<button
				type="button"
				onclick={() => (mapFullWidth = !mapFullWidth)}
				class="ml-auto hidden items-center gap-1.5 rounded-md border border-neutral-300 px-2 py-1 text-xs font-medium text-neutral-600 transition hover:bg-neutral-100 lg:inline-flex"
				title={full ? t('map.collapse') : t('map.expand')}
			>
				{#if full}
					<Minimize2 size={14} />
				{:else}
					<Maximize2 size={14} />
				{/if}
				<span>{full ? t('map.collapse') : t('map.expand')}</span>
			</button>
		</div>
		<div
			bind:this={mapElement}
			class={`hopback-map rounded-md border border-neutral-200 ${full ? 'hopback-map-full' : ''}`}
		></div>
		<div class="mt-3 space-y-2 text-xs">
			<div class="flex flex-wrap items-center gap-2">
				<span class="font-semibold uppercase tracking-wide text-neutral-400"
					>{t('map.legend.nodes')}</span
				>
				{#each POINT_LEGEND.filter((item) => mapPointKinds.has(item.kind)) as item (item.kind)}
					{@const hidden = hiddenMapPoints.includes(item.kind)}
					<button
						type="button"
						onclick={() => toggleMapPoint(item.kind)}
						class={`inline-flex items-center gap-1 rounded border px-2 py-1 transition ${hidden ? 'border-neutral-200 bg-white text-neutral-400' : 'border-transparent bg-neutral-100 text-neutral-700 hover:border-neutral-300'}`}
						title={hidden ? t('map.legend.show') : t('map.legend.hide')}
					>
						<span
							class="size-2 rounded-full"
							style={`background:${hidden ? '#d4d4d4' : item.color}`}
						></span>
						<span class={hidden ? 'line-through' : ''}>{t(`map.point.${item.kind}`)}</span>
					</button>
				{/each}
			</div>
			{#if mapKinds.length}
				<div class="flex flex-wrap items-center gap-2">
					<span class="font-semibold uppercase tracking-wide text-neutral-400"
						>{t('map.legend.links')}</span
					>
					{#each mapKinds as kind (kind)}
						{@const hidden = hiddenMapLinks.includes(kind)}
						<button
							type="button"
							onclick={() => toggleMapLink(kind)}
							class={`inline-flex items-center gap-1 rounded border px-2 py-1 transition ${hidden ? 'border-neutral-200 bg-white text-neutral-400' : 'border-transparent bg-neutral-100 text-neutral-700 hover:border-neutral-300'}`}
							title={hidden ? t('map.legend.show') : t('map.legend.hide')}
						>
							<span
								class="size-2 rounded-full"
								style={`background:${hidden ? '#d4d4d4' : kindColor(kind)}`}
							></span>
							<span class={hidden ? 'line-through' : ''}>{kindLabel(kind)}</span>
						</button>
					{/each}
				</div>
			{/if}
		</div>
	</section>
{/snippet}

{#snippet InstructionsPanel(current: DiagnosticTest)}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="flex items-center gap-2">
			<ClipboardList size={18} class="text-teal-700" />
			<h2 class="text-lg font-semibold text-neutral-950">{t('instr.title')}</h2>
		</div>
		<p class="mt-3 text-sm leading-6 text-neutral-600">
			{t('instr.desc')}
		</p>

		<div class="mt-3 rounded-md border border-neutral-200 bg-neutral-50 p-3">
			<p class="text-xs font-semibold uppercase tracking-wide text-neutral-500">
				{t('instr.tempCode')}
			</p>
			<div class="mt-2 flex items-center justify-between gap-2">
				<p class="mono text-2xl font-semibold text-neutral-950">{current.code}</p>
				<button
					class="grid size-10 place-items-center rounded-md border border-neutral-300 bg-white transition hover:bg-neutral-100"
					type="button"
					onclick={() => copyText(current.code, 'code')}
					title={t('instr.copyCode')}
				>
					{#if copiedField === 'code'}<CheckCircle2 size={18} class="text-teal-700" />{:else}<Copy
							size={18}
						/>{/if}
				</button>
			</div>
		</div>

		<div class="mt-4 rounded-md border border-neutral-200 bg-neutral-50 p-3">
			<div class="flex items-start justify-between gap-3">
				<div class="min-w-0">
					<p class="text-xs font-semibold uppercase tracking-wide text-neutral-500">
						{t('instr.endpointKey')}
					</p>
					<p class="mono mt-2 break-all text-xs text-neutral-700">{current.endpointPublicKey}</p>
				</div>
				<button
					class="grid size-10 shrink-0 place-items-center rounded-md border border-neutral-300 bg-white transition hover:bg-neutral-100"
					type="button"
					onclick={() => copyText(current.endpointPublicKey, 'endpoint-key')}
					title={t('instr.copyEndpointKey')}
				>
					{#if copiedField === 'endpoint-key'}<CheckCircle2
							size={18}
							class="text-teal-700"
						/>{:else}<Copy size={18} />{/if}
				</button>
			</div>
		</div>

		{#if current.status === 'expired'}
			<div class="mt-4 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-800">
				{t('instr.expired')}
			</div>
		{:else}
			{#if current.qrDataUrl}
				<img
					class="mx-auto mt-4 size-64 rounded-md border border-neutral-200 bg-white p-2 transition duration-300 hover:scale-[1.01]"
					src={current.qrDataUrl}
					alt="MeshCore contact QR"
				/>
			{/if}
		{/if}

		<div class="mt-4 rounded-md border border-neutral-200 bg-neutral-50 p-3">
			<p class="text-xs font-semibold uppercase tracking-wide text-neutral-500">
				{t('instr.link')}
			</p>
			<div class="mt-2 flex items-start gap-2">
				<button
					class="mono min-w-0 flex-1 break-all text-left text-xs text-teal-800 underline-offset-2 hover:underline"
					type="button"
					onclick={() => window.location.assign(current.qrPayload)}
				>
					{current.qrPayload}
				</button>
				<button
					class="grid size-10 shrink-0 place-items-center rounded-md border border-neutral-300 bg-white transition hover:bg-neutral-100"
					type="button"
					onclick={() => copyText(current.qrPayload, 'meshcore-link')}
					title={t('instr.copyLink')}
				>
					{#if copiedField === 'meshcore-link'}<CheckCircle2
							size={18}
							class="text-teal-700"
						/>{:else}<Copy size={18} />{/if}
				</button>
				<button
					class="grid size-10 shrink-0 place-items-center rounded-md border border-neutral-300 bg-white transition hover:bg-neutral-100"
					type="button"
					onclick={() => window.location.assign(current.qrPayload)}
					title={t('instr.openLink')}
				>
					<ExternalLink size={18} />
				</button>
			</div>
		</div>
	</section>
{/snippet}

{#snippet DeliveryPathsPanel(current: DiagnosticTest)}
	{@const outboundPaths = current.deliveryPaths?.outbound ?? []}
	{@const returnPaths = current.deliveryPaths?.return ?? []}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex items-center gap-2">
			<Route size={18} class="text-teal-700" />
			<h2 class="text-lg font-semibold text-neutral-950">{t('delivery.title')}</h2>
		</div>
		<div class="grid gap-4">
			{@render DeliveryPathList(t('delivery.toEndpoint'), 'outbound', outboundPaths, current)}
			{@render DeliveryPathList(t('delivery.fromEndpoint'), 'return', returnPaths, current)}
		</div>
	</section>
{/snippet}

{#snippet DeliveryPathList(
	title: string,
	direction: 'outbound' | 'return',
	paths: DeliveryPathOption[],
	current: DiagnosticTest
)}
	{@const accent = deliveryAccent(direction)}
	<div>
		<p class="mb-2 text-sm font-semibold text-neutral-900">{title}</p>
		{#if paths.length}
			<div class="grid gap-3">
				{#each paths as option (option.key)}
					{@const badgeLen = deliveryBadgeLen(option.rows)}
					<div class="rounded-md border border-neutral-200 p-2.5">
						<div class="mb-2 flex items-center justify-between gap-2">
							<button
								type="button"
								class="inline-flex items-center gap-1 text-xs font-medium text-neutral-600 transition hover:text-teal-700"
								onclick={() =>
									window.open(analyzerPacketUrl(option.packetHash), '_blank', 'noreferrer')}
								title={t('route.openMessage')}
							>
								<span
									>{(option.kinds?.length ? option.kinds : [option.kind])
										.map(kindLabel)
										.join(' / ')}</span
								>
								<ExternalLink size={11} strokeWidth={2} />
							</button>
							<div class="flex items-center gap-1.5">
								<span class="rounded bg-neutral-100 px-2 py-0.5 text-xs text-neutral-600">
									{option.hopCount === 0 ? t('route.kind.direct') : hopLabel(option.hopCount)}
								</span>
								{#if option.hashWidth}
									<span
										class="rounded bg-neutral-100 px-2 py-0.5 text-xs font-medium text-neutral-500"
										title={t('delivery.hashWidth')}
									>
										{option.hashWidth}b
									</span>
									<button
										type="button"
										title={t('obs.path.toggle')}
										aria-pressed={deliveryHex[option.key] ?? false}
										class={`grid size-[22px] place-items-center rounded border transition ${deliveryHex[option.key] ? 'border-teal-300 bg-teal-50 text-teal-700' : 'border-neutral-200 text-neutral-400 hover:bg-neutral-100 hover:text-neutral-700'}`}
										onclick={() => toggleDeliveryHex(option.key)}
									>
										<Hash size={12} />
									</button>
								{/if}
							</div>
						</div>
						<div class="grid gap-3">
							{#each option.rows as row, index (row.key)}
								<button
									class={`delivery-row group relative flex items-start gap-3 rounded-md text-left transition ${row.publicKey ? accent.rowHover : 'cursor-default'}`}
									style={`--row-index: ${index}; --delivery-line: ${accent.line}`}
									type="button"
									disabled={!row.publicKey}
									onclick={() => {
										if (row.publicKey)
											window.open(analyzerNodeUrl(row.publicKey), '_blank', 'noreferrer');
									}}
									title={row.publicKey ? t('route.openNode') : undefined}
								>
									<span
										class={`z-10 grid size-10 shrink-0 place-items-center rounded-full text-sm font-semibold tracking-tight tabular-nums transition ${row.tone === 'edge' ? accent.edgeBadge : accent.hopBadge}`}
									>
										{deliveryBadge(row.short, badgeLen)}
									</span>
									<span class="min-w-0 pt-0.5">
										<span class="block truncate text-sm font-semibold text-neutral-950"
											>{deliveryHex[option.key] && row.hex
												? row.hex
												: deliveryRowName(row, current, direction)}</span
										>
										<span class="block text-xs text-neutral-500"
											>{deliveryRowMeta(row, current, direction)}</span
										>
									</span>
									<span class="ml-auto mt-0.5 flex shrink-0 items-center gap-1.5">
										{#if row.conflict}
											<span
												class="inline-flex items-center gap-0.5 rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700"
												title={conflictTitle(row)}
											>
												<AlertTriangle size={11} />
												{(row.alternatives?.length ?? 0) + 1}
											</span>
										{/if}
										{#if row.publicKey}
											<ExternalLink size={13} class={`mt-0.5 transition ${accent.link}`} />
										{/if}
									</span>
								</button>
							{/each}
						</div>
					</div>
				{/each}
			</div>
		{:else}
			<div
				class="rounded-md border border-dashed border-neutral-300 bg-neutral-50 px-3 py-4 text-sm text-neutral-500"
			>
				{t('delivery.waiting')}
			</div>
		{/if}
	</div>
{/snippet}

{#snippet PathStatisticsPanel(current: DiagnosticTest)}
	{@const stats = current.pathStatistics}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex items-center gap-2">
			<Clock size={18} class="text-neutral-600" />
			<h2 class="text-lg font-semibold text-neutral-950">{t('stats2.title')}</h2>
		</div>
		{#if stats}
			<div class="grid grid-cols-2 gap-2 text-sm">
				<div class="rounded-md bg-neutral-50 p-2">
					<p class="text-neutral-500">{t('stats2.uniquePaths')}</p>
					<p class="font-semibold text-neutral-950">{stats.totalPaths}</p>
				</div>
				<div class="rounded-md bg-neutral-50 p-2">
					<p class="text-neutral-500">{t('stats2.outbound')}</p>
					<p class="font-semibold text-neutral-950">{stats.outboundPaths}</p>
				</div>
				<div class="rounded-md bg-neutral-50 p-2">
					<p class="text-neutral-500">{t('stats2.return')}</p>
					<p class="font-semibold text-neutral-950">{stats.returnPaths}</p>
				</div>
				<div class="rounded-md bg-neutral-50 p-2">
					<p class="text-neutral-500">{t('stats2.longestHops')}</p>
					<p class="font-semibold text-neutral-950">{hopLabel(stats.longestHopCount)}</p>
				</div>
				<div class="rounded-md bg-neutral-50 p-2">
					<p class="text-neutral-500">{t('stats2.shortestHops')}</p>
					<p class="font-semibold text-neutral-950">{hopLabel(stats.shortestHopCount)}</p>
				</div>
			</div>
			<div class="mt-2 rounded-md border border-neutral-200 bg-neutral-50 p-3 text-sm">
				<p class="text-neutral-500">{t('stats2.longestDistance')}</p>
				<p class="mt-1 font-semibold text-neutral-950">
					{formatDistance(stats.longestDistanceKm)}
				</p>
				{#if stats.longestDistanceLabel}
					<p class="mt-1 line-clamp-2 text-xs text-neutral-500">{stats.longestDistanceLabel}</p>
				{/if}
			</div>
			<div class="mt-2 rounded-md border border-neutral-200 bg-neutral-50 p-3 text-sm">
				<p class="text-neutral-500">{t('stats2.averageDistance')}</p>
				<p class="mt-1 font-semibold text-neutral-950">
					{formatDistance(stats.averageDistanceKm)}
				</p>
			</div>
		{:else}
			<div
				class="rounded-md border border-dashed border-neutral-300 bg-neutral-50 px-3 py-4 text-sm text-neutral-500"
			>
				{t('stats2.waiting')}
			</div>
		{/if}
	</section>
{/snippet}

{#snippet ProgressPanel(current: DiagnosticTest)}
	{@const percent = progressPercent(current)}
	{@const active = activeProgressIndex(current)}
	{@const steps = progressSteps(current)}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
			<div>
				<h2 class="text-lg font-semibold text-neutral-950">{t('progress.title')}</h2>
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
	{@const direction = icon === 'send' ? 'outbound' : 'return'}
	{@const messages = messageObservations(observations)}
	{@const acks = ackObservations(observations)}
	<!-- Return messages (the reply) are sent by the endpoint, so its own RF self-echo
	     isn't a real observer — exclude it from the observer count. -->
	{@const observerMessages =
		direction === 'return' && test ? externalObservers(messages, test) : messages}
	{@const firstMessage = firstObservation(messages)}
	{@const firstAck = firstObservation(acks)}
	{@const directionHash =
		icon === 'send'
			? test?.outboundHash || firstMessage?.packetHash
			: test?.returnHash || firstMessage?.packetHash || test?.replyHash}
	{@const ackHash =
		icon === 'send'
			? test?.outboundAckHash || firstAck?.packetHash
			: test?.replyAckHash || firstAck?.packetHash}
	{@const teal = {
		text: 'text-teal-700',
		softBg: 'bg-teal-50',
		softBorder: 'border-teal-200',
		iconBg: 'bg-teal-100 text-teal-700'
	}}
	{@const orange = {
		text: 'text-orange-700',
		softBg: 'bg-orange-50',
		softBorder: 'border-orange-200',
		iconBg: 'bg-orange-100 text-orange-700'
	}}
	{@const accent = icon === 'send' ? teal : orange}
	<!-- The ACK confirming this leg's message travels the OPPOSITE direction, so its
	     box carries the other leg's accent colour as a visual cue. -->
	{@const ackAccent = icon === 'send' ? orange : teal}
	<section class="rounded-md border border-neutral-300 bg-white p-4 shadow-sm">
		<div class="mb-4 flex items-center justify-between gap-3">
			<div class="flex min-w-0 items-center gap-2">
				<span class={`grid size-8 shrink-0 place-items-center rounded-md ${accent.iconBg}`}>
					{#if icon === 'send'}<Send size={16} />{:else}<Route size={16} />{/if}
				</span>
				<h2 class="truncate text-lg font-semibold text-neutral-950">{title}</h2>
			</div>
			{#if directionHash}
				<button
					class="mono inline-flex shrink-0 items-center gap-0.5 rounded border border-neutral-300 bg-neutral-50 px-1 py-0.5 font-semibold text-neutral-500 hover:border-teal-700 hover:bg-teal-50 hover:text-teal-900"
					type="button"
					style="font-size: 11px; line-height: 1;"
					onclick={() => window.open(analyzerPacketUrl(directionHash), '_blank', 'noreferrer')}
					title={t('route.openMessage')}
				>
					<span>{packetHashButtonLabel(directionHash)}</span>
					{#if retriesForKind(icon === 'send' ? 'user msg' : 'reply') > 0}
						<span class="text-amber-600"
							>+{retriesForKind(icon === 'send' ? 'user msg' : 'reply')}</span
						>
					{/if}
					<ExternalLink size={10} strokeWidth={2} />
				</button>
			{/if}
		</div>

		{#if observations.length}
			{@const deliveryValue = test
				? deliveryTime(direction, messages, acks, test)
				: t('common.pending')}
			{@const deliveryPending = deliveryValue === t('common.pending')}
			{@const retries = retriesForKind(icon === 'send' ? 'user msg' : 'reply')}
			<div
				class={`flex items-stretch justify-between gap-3 rounded-lg border ${accent.softBorder} ${accent.softBg} p-3`}
			>
				<div class="min-w-0">
					<p
						class="flex items-center gap-1 text-xs font-medium uppercase tracking-wide text-neutral-500"
					>
						{icon === 'send'
							? t('route.deliveryTimeEstimated')
							: t('route.deliveryConfirmationTime')}
						{#if icon === 'send'}
							<span class="cursor-help" title={t('route.deliveryTimeEstimatedHint')}>
								<CircleHelp size={13} class="shrink-0 text-neutral-400" />
							</span>
						{/if}
					</p>
					<p class="mt-0.5 truncate text-2xl font-bold text-neutral-950">
						{icon === 'send' && !deliveryPending ? '~' : ''}{deliveryValue}
					</p>
				</div>
				<div class="flex flex-col items-end justify-center gap-1 text-right">
					<span
						class={`inline-flex items-center gap-1 rounded-full border bg-white px-2 py-0.5 text-xs font-semibold ${accent.softBorder} ${accent.text}`}
					>
						<GitBranch size={12} />
						{routeKind(observations)}
					</span>
					<span class="text-xs font-medium text-neutral-500">
						{deliveryHops(direction, messages, acks, test)}{#if retries > 0}
							· {tn('unit.retry', retries)}{/if}
					</span>
				</div>
			</div>

			<div class="mt-3 grid grid-cols-2 gap-2 text-sm lg:grid-cols-4">
				<div class="rounded-md border border-neutral-200 bg-neutral-50/70 p-2.5">
					<div class="flex min-w-0 items-center gap-1.5 text-neutral-400">
						<Eye size={13} class="shrink-0" />
						<p class="truncate text-xs">{t('route.observations')}</p>
					</div>
					<p class="mt-1 font-semibold tabular-nums text-neutral-900">{messages.length}</p>
				</div>
				<div class="rounded-md border border-neutral-200 bg-neutral-50/70 p-2.5">
					<div class="flex min-w-0 items-center gap-1.5 text-neutral-400">
						<Users size={13} class="shrink-0" />
						<p class="truncate text-xs">{t('route.observers')}</p>
					</div>
					<p class="mt-1 whitespace-nowrap font-semibold tabular-nums text-neutral-900">
						{observerCoverage(observerMessages)}
					</p>
				</div>
				<div class="rounded-md border border-neutral-200 bg-neutral-50/70 p-2.5">
					<div class="flex min-w-0 items-center gap-1.5 text-neutral-400">
						<Waypoints size={13} class="shrink-0" />
						<p class="truncate text-xs">{t('route.paths')}</p>
					</div>
					<p class="mt-1 font-semibold tabular-nums text-neutral-900">
						{uniquePathCount(messages)}
					</p>
				</div>
				<div class="rounded-md border border-neutral-200 bg-neutral-50/70 p-2.5">
					<div class="flex min-w-0 items-center gap-1.5 text-neutral-400">
						<Timer size={13} class="shrink-0" />
						<p class="truncate text-xs">{t('route.propagation')}</p>
					</div>
					<p class="mt-1 whitespace-nowrap font-semibold tabular-nums text-neutral-900">
						{propagationTime(messages)}
					</p>
				</div>
			</div>

			<!-- An outbound ACK we send is self-recorded so it shows up at all; that copy
			     isn't a real network sighting, so for the outbound leg we count only
			     independent observers. The return (reply) ACK is sent by the user, so the
			     endpoint observing it is genuine and all observations count. -->
			{@const externalAcks = icon === 'send' && test ? externalObservers(acks, test) : acks}
			<div
				class={`mt-3 flex flex-wrap items-center justify-between gap-2 rounded-md border ${ackAccent.softBorder} ${ackAccent.softBg} p-3 text-sm`}
			>
				<div class="flex items-center gap-2">
					<CheckCircle2
						size={16}
						class={externalAcks.length ? ackAccent.text : 'text-neutral-300'}
					/>
					<div>
						<p class="text-xs font-medium text-neutral-500">{ackLabel(acks, icon === 'send')}</p>
						<p class="font-semibold text-neutral-900">
							{externalAcks.length
								? t('route.observed', { n: externalAcks.length })
								: acks.length || ackHash
									? t('route.sentNotSeen')
									: t('common.pending')}
						</p>
					</div>
				</div>
				{#if ackHash}
					<button
						class="mono inline-flex shrink-0 items-center gap-0.5 rounded border border-neutral-300 bg-white px-1 py-0.5 font-semibold text-neutral-500 hover:border-teal-700 hover:bg-teal-50 hover:text-teal-900"
						type="button"
						style="font-size: 11px; line-height: 1;"
						onclick={() => window.open(analyzerPacketUrl(ackHash), '_blank', 'noreferrer')}
						title={t('route.openAck')}
					>
						<span>{packetHashButtonLabel(ackHash)}</span>
						{#if retriesForKind(icon === 'send' ? 'ack+path' : 'reply ack') > 0}
							<span class="text-amber-600"
								>+{retriesForKind(icon === 'send' ? 'ack+path' : 'reply ack')}</span
							>
						{/if}
						<ExternalLink size={10} strokeWidth={2} />
					</button>
				{/if}
			</div>
		{:else}
			<div
				class="flex min-h-40 items-center justify-center rounded-md border border-dashed border-neutral-300 bg-neutral-50 text-sm text-neutral-500"
			>
				<XCircle size={17} class="mr-2" />
				{t('route.noPacket')}
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

	@media (min-width: 1024px) {
		.hopback-map-full {
			height: 540px;
		}
	}

	.progress-scan {
		animation: progress-scan 1.8s ease-in-out infinite;
	}

	.delivery-row {
		animation: delivery-row-in 260ms ease-out both;
		animation-delay: calc(var(--row-index) * 42ms);
	}

	.console-event {
		animation: delivery-row-in 240ms ease-out both;
		animation-delay: calc(var(--row-index) * 40ms);
	}

	.delivery-row:not(:last-child)::before {
		background: linear-gradient(
			180deg,
			var(--delivery-line, rgba(20, 184, 166, 0.36)),
			rgba(212, 212, 212, 0.72)
		);
		content: '';
		height: calc(100% + 0.75rem);
		left: 1.25rem;
		position: absolute;
		top: 2.25rem;
		width: 2px;
	}

	@layer utilities {
		/* Hide scrollbar for Chrome, Safari and Opera */
		.no-scrollbar::-webkit-scrollbar {
			display: none;
		}
		/* Hide scrollbar for IE, Edge and Firefox */
		.no-scrollbar {
			-ms-overflow-style: none; /* IE and Edge */
			scrollbar-width: none; /* Firefox */
		}
	}

	@keyframes progress-scan {
		0% {
			transform: translateX(-120%);
		}
		100% {
			transform: translateX(820%);
		}
	}

	@keyframes delivery-row-in {
		from {
			opacity: 0;
			transform: translateY(4px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}
</style>
