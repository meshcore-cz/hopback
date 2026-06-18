<script lang="ts">
	import { Languages } from '@lucide/svelte';
	import { locale, locales, setLocale, t } from './index.svelte';

	let { compact = false }: { compact?: boolean } = $props();
</script>

<div
	class="inline-flex items-center gap-1 rounded-md border border-neutral-300 bg-white/75 p-1 text-sm"
	role="group"
	aria-label={t('lang.label')}
>
	{#if !compact}
		<Languages size={15} class="ml-1 text-neutral-400" />
	{/if}
	{#each locales as code (code)}
		<button
			class={`rounded px-2 py-1 font-medium uppercase transition ${
				locale() === code
					? 'bg-neutral-900 text-white'
					: 'text-neutral-600 hover:bg-neutral-100 hover:text-neutral-900'
			}`}
			type="button"
			aria-pressed={locale() === code}
			title={t(`lang.${code}`)}
			onclick={() => setLocale(code)}
		>
			{code}
		</button>
	{/each}
</div>
