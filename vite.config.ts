import tailwindcss from '@tailwindcss/vite';
import type { Server } from 'node:http';
import { defineConfig } from 'vitest/config';
import { sveltekit } from '@sveltejs/kit/vite';

export default defineConfig({
	plugins: [
		tailwindcss(),
		{
			name: 'hopback-ws',
			async configureServer(server) {
				const { attachHopbackGateway } = await import('./src/lib/server/runtime');
				if (server.httpServer) attachHopbackGateway(server.httpServer as Server);
			}
		},
		sveltekit()
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
