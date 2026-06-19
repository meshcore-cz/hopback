<script lang="ts">
	import './layout.css';
	import favicon from '$lib/assets/favicon.png';
	import { onMount } from 'svelte';
	import { initLocale } from '$lib/i18n/index.svelte';
	import Footer from '$lib/Footer.svelte';

	let { children } = $props();

	onMount(() => initLocale());
</script>

<svelte:head>
	<link rel="icon" type="image/png" href={favicon} />
	<title>Hopback</title>
</svelte:head>
{@render children()}
<Footer />
