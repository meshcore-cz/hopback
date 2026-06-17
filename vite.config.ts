import tailwindcss from '@tailwindcss/vite';
import type { Server } from 'node:http';
import { defineConfig } from 'vitest/config';
import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { attachHopbackGateway } from './src/lib/server/runtime';

export default defineConfig({
	plugins: [
		tailwindcss(),
		{
			name: 'hopback-ws',
			configureServer(server) {
				if (server.httpServer) attachHopbackGateway(server.httpServer as Server);
			}
		},
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			adapter: adapter()
		})
	],
	test: {
		expect: { requireAssertions: true },
		projects: [
			{
				extends: './vite.config.ts',
				test: {
					name: 'server',
					environment: 'node',
					include: ['src/**/*.{test,spec}.{js,ts}'],
					exclude: ['src/**/*.svelte.{test,spec}.{js,ts}']
				}
			}
		]
	}
});
