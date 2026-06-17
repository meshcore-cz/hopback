import { createServer } from 'node:http';
import { handler } from '../build/handler.js';
import { attachHopbackGateway } from '../src/lib/server/runtime.ts';

const host = process.env.HOST || '0.0.0.0';
const port = Number(process.env.PORT || 3000);
const server = createServer(handler);

attachHopbackGateway(server);

server.listen(port, host, () => {
	console.log(`Hopback listening on http://${host}:${port}`);
});
