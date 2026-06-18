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
				if (process.env.VITEST) return;
				const { attachHopbackGateway } = await server.ssrLoadModule('/src/lib/server/runtime.ts');
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
