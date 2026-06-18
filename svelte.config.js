import adapter from '@sveltejs/adapter-static';

const base = process.env.PUBLIC_BASE_PATH || '';
const output = process.env.SVELTEKIT_OUTPUT_DIR || 'build';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	compilerOptions: {
		// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
		runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
	},
	kit: {
		adapter: adapter({
			assets: output,
			fallback: 'index.html',
			pages: output,
			strict: false
		}),
		files: {
			appTemplate: 'web/src/app.html',
			assets: 'web/static',
			lib: 'web/src/lib',
			routes: 'web/src/routes'
		},
		paths: {
			base
		}
	}
};

export default config;
