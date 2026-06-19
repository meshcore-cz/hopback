<script lang="ts">
	import { resolve } from '$app/paths';
	import { t } from '$lib/i18n/index.svelte';
	import { APP_VERSION } from '$lib/version';
</script>

<footer
	class="mx-auto w-full max-w-6xl px-4 pb-4 pt-2 text-center text-sm text-neutral-500 sm:px-6 lg:px-8"
>
	{t('footer.credit')}
	<a
		class="font-medium text-neutral-700 hover:text-teal-800"
		href="https://github.com/meshcore-cz/hopback"
	>
		meshcore-cz/hopback
	</a>
	<span class="mx-1 text-neutral-400">·</span>
	<a class="font-medium text-neutral-700 hover:text-teal-800" href={resolve('/status')}>
		{t('footer.status')}
	</a>
	<span class="mx-1 text-neutral-400">·</span>
	<span>v{APP_VERSION}</span>
</footer>
