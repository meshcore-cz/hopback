import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vitest/config';
import { sveltekit } from '@sveltejs/kit/vite';

const backend = process.env.HOPBACK_DEV_BACKEND || 'http://127.0.0.1:3000';

export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	server: {
		proxy: {
			'/api': backend,
			'/ws': { target: backend, ws: true },
			'/agent': { target: backend, ws: true }
		}
	},
	test: {
		expect: { requireAssertions: true },
		projects: [
			{
				extends: './vite.config.ts',
				test: {
					name: 'server',
					environment: 'node',
					include: ['web/src/**/*.{test,spec}.{js,ts}'],
					exclude: ['web/src/**/*.svelte.{test,spec}.{js,ts}']
				}
			}
		]
	}
});
