# Hopback

MeshCore Test is a web-based diagnostic service for checking real connectivity between a user and selected [MeshCore](http://meshcore.io/) endpoints in different cities or regions. The user enters their public key, chooses an endpoint, and receives a temporary code and QR contact card. After sending the code over MeshCore, the page updates live as the message is detected, routed, received, verified, and answered.

The interface visualizes both directions of the test: the path from the user to the endpoint and the return path back to the user. It can display resolved repeaters, hop counts, packet observations, timing, signal data, route differences, reply status, and whether the full round trip succeeded or only one direction worked.

The service combines a simple web frontend, a central real-time backend, [CoreScope](https://github.com/Kpa-clawbot/CoreScope) analyzer data, and lightweight agents connected to MeshCore nodes. Its goal is to make MeshCore testing easy for normal users while also providing useful routing and radio diagnostics for network operators.

## Stack

- SvelteKit 2 / Svelte 5 frontend
- Node adapter with a custom WebSocket gateway
- SQLite via `better-sqlite3`
- CoreScope WebSocket monitors for `wss://analyzer.meshcore.cz` and `wss://mc.pp0.co`
- CoreScope node cache loaded from `/api/nodes?limit=2000&offset=0`
- Mesh packet matching through `@meshcore-cz/meshpkt`
- Lightweight agent bridge for meshcore-go IPC

## Development

```sh
npm install
cp config.yaml.example config.yaml
cp .env.example .env
npm run dev
```

Open `http://localhost:5173`. The dev server attaches Hopback WebSocket routes at:

- `/ws` for browsers
- `/agent` for authenticated agents

The same flow is available through Make:

```sh
make config
make agent-env
make meshpkt-use-local
make stack-web
```

To run the web gateway and the lightweight IPC agent together:

```sh
make stack
```

Useful targets:

- `make dev` starts the SvelteKit gateway.
- `make agent` starts the meshcore-go IPC agent.
- `make verify` runs format, type checks, lint, tests, and production build.
- `make meshpkt-use-local` rebuilds and installs `../meshpkt/js`.

## Production

```sh
npm run build
npm run start
```

`npm run start` launches `server/index.ts`, mounts the SvelteKit build, and attaches the same WebSocket gateway.

## Backend And Web Configuration

Backend and web runtime configuration lives in `config.yaml`:

```yaml
service:
  name: Hopback
  databasePath: data/hopback.sqlite
  autoReply: true
  agentSecret: change-this-secret

coreScope:
  urls:
    - wss://analyzer.meshcore.cz
    - wss://mc.pp0.co

endpoints:
  - id: kololec
    host: test.kololec.cz
    name: 'Kololeč Test'
    region: Okres Litoměřice, Ústecký Kraj, Česká Republika
    publicKey: 5430101DB427C7E403D1D3C619C056C1BE969FC785ECC7B01774B5AD4BFCCA2B
    type: 1
    location:
      label: Kololeč, Okres Litoměřice, Ústecký Kraj, Česká Republika
      lat: 50.478
      lon: 13.975
```

Backend/web secrets belong in the ignored local `config.yaml`. For packet decryption and replies, each endpoint needs either its own `privateKey` or the optional `service.privateKey` fallback in YAML.

`config.yaml` is ignored by git. Commit changes to `config.yaml.example` when you want to change the shared template.

Hopback fails at startup when required backend/web configuration is missing or malformed. In the current central-decrypt flow, that includes `service.agentSecret` and an endpoint private key through either `endpoint.privateKey` or `service.privateKey`.

## Agent

The included agent is a JSON-lines bridge between Hopback and meshcore-go IPC. Agent settings live in `.env`:

```env
HOPBACK_BACKEND_WS=ws://127.0.0.1:5173/agent
HOPBACK_AGENT_SECRET=change-this-secret
HOPBACK_ENDPOINT_ID=kololec
HOPBACK_AGENT_ID=kololec-agent
MESHCORE_IPC_PATH=~/Library/Caches/mc/backend.sock
# MESHCORE_DEVICE=default-device-id
```

Run the agent with:

```sh
npm run agent
```

`HOPBACK_AGENT_SECRET` must match `service.agentSecret` from the backend/web `config.yaml`.

The agent opens a dedicated long-running IPC socket and sends `watch_rf` immediately after connecting. meshcore-go acknowledges that request first, then streams RF events with base64 `bytes`; Hopback converts those bytes to packet hex and forwards RSSI/SNR/timestamp to the backend.

Hopback reply commands are sent through separate one-off IPC sockets using `send_mesh_packet`:

```json
{ "id": 2, "method": "send_mesh_packet", "params": { "priority": 0, "packet": "..." } }
```

The agent fails at startup if required `.env` values are missing or it cannot connect to `MESHCORE_IPC_PATH` or the configured TCP IPC endpoint. After one successful IPC connection, later disconnects are retried.

## Verification

```sh
npm run check
npm run lint
npm run test
npm run build
```
