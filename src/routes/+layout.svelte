<script lang="ts">
	import './layout.css';
	import favicon from '$lib/assets/favicon.svg';
	import { onMount } from 'svelte';
	import { initLocale } from '$lib/i18n/index.svelte';

	let { children } = $props();

	onMount(() => initLocale());
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<title>Hopback</title>
</svelte:head>
{@render children()}
