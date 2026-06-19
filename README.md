# Hopback

Hopback is a web-based diagnostic service for checking real connectivity between a user and selected [MeshCore](http://meshcore.io/) endpoints in different cities or regions. The user enters their public key, chooses an endpoint, and receives a temporary code and QR contact card. After sending the code over MeshCore, the page updates live as the message is detected, routed, received, verified, and answered.

The interface visualizes both directions of the test: the path from the user to the endpoint and the return path back to the user. It can display resolved repeaters, hop counts, packet observations, timing, signal data, route differences, reply status, and whether the full round trip succeeded or only one direction worked.

The service combines a simple web frontend, a central real-time backend, [CoreScope](https://github.com/Kpa-clawbot/CoreScope) analyzer data, and lightweight agents connected to MeshCore nodes. Its goal is to make MeshCore testing easy for normal users while also providing useful routing and radio diagnostics for network operators.

## Stack

- SvelteKit 2 / Svelte 5 frontend
- Static Svelte frontend embedded into the Go backend binary
- Go HTTP/WebSocket backend
- SQLite
- CoreScope WebSocket real-time metrics and node information
- Mesh packet matching through native `meshpkt`
- Lightweight agent bridge for meshcore-go IPC or direct MeshCore companion TCP

## Development

```sh
npm install
cp config.yaml.example config.yaml
cp .env.example .env
make server
make dev
```

Open `http://localhost:5173`. In development, Vite serves the frontend and proxies runtime traffic to the Go backend at `http://127.0.0.1:3000`:

- `/ws` for browsers
- `/agent` for authenticated agents

The same flow is available through Make:

```sh
make config
make agent-env
make stack
```

To run the backend, frontend, and lightweight IPC agent together:

```sh
make stack
```

Useful targets:

- `make server` starts the Go backend.
- `make dev` starts the Vite frontend.
- `make agent` starts the Go meshcore-go IPC agent.
- `make verify` runs format, type checks, lint, tests, and production build.

## Production

```sh
make build
make start
```

`make build` writes the static Svelte frontend into `cmd/hopbackd/frontend/`, builds `bin/hopbackd` with those assets embedded, and builds `bin/hopback-agent`. `make start` launches the single Go web process, which serves the UI, `/api/*`, `/ws`, and `/agent`.

## Docker

Prebuilt multi-arch (`amd64`/`arm64`) images are published to GitHub Container Registry on every tagged release:

- `ghcr.io/meshcore-cz/hopbackd` — the web backend with the frontend embedded.
- `ghcr.io/meshcore-cz/hopback-agent` — the lightweight radio bridge.

Tags follow the release: `latest`, the full version (`0.9.1`), and the minor series (`0.9`). Pin a specific version for reproducible deployments. The `main` branch also publishes a rolling `main` tag.

### Docker Compose (recommended)

The repository ships a ready-to-edit [`docker-compose.yml`](docker-compose.yml) that runs the backend and an agent together:

```sh
cp config.yaml.example config.yaml   # edit service.agentSecret, endpoints, keys
docker compose up -d
```

Open `http://localhost:3000`. The backend reads `config.yaml` mounted read-only at `/app/config.yaml` and stores its SQLite database in the `hopback-data` volume (`databasePath: data/hopback.sqlite` resolves to `/app/data`). The agent reaches the backend over the internal compose network at `ws://hopbackd:3000/agent`; set `HOPBACK_AGENT_SECRET` to match `service.agentSecret` and point `MESHCORE_URI` at your radio.

Update to a newer image with:

```sh
docker compose pull && docker compose up -d
```

### Plain Docker

Backend only:

```sh
docker run -d --name hopbackd \
  -p 3000:3000 \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v hopback-data:/app/data \
  ghcr.io/meshcore-cz/hopbackd:latest
```

Agent only (configuration comes from environment variables; no `.env` file needed):

```sh
docker run -d --name hopback-agent \
  -e HOPBACK_BACKEND_WS=ws://hopback-host:3000/agent \
  -e HOPBACK_AGENT_SECRET=change-this-secret \
  -e HOPBACK_ENDPOINT_ID=kololec \
  -e HOPBACK_AGENT_ID=kololec-agent \
  -e MESHCORE_URI=tcp://10.0.0.30:5000 \
  ghcr.io/meshcore-cz/hopback-agent:latest
```

To connect the agent to a host Unix IPC socket instead, bind-mount it and use `MESHCORE_URI=ipc+unix:///path/inside/container.sock`.

### Building images locally

```sh
docker build -f Dockerfile.hopbackd -t hopbackd:dev .
docker build -f Dockerfile.agent -t hopback-agent:dev .
```

The image builds resolve `meshcore-go` from its published module, so the local `../meshcore-go` checkout used for development is not required.

## Versioning

```sh
make release VERSION=v0.9.1
```

The release target checks the tree, updates `package.json` to the unprefixed version (`0.9.1`), commits it, tags `v0.9.1`, and pushes the branch and tag. Makefile targets read the package version and stamp it into the Go backend and agent with `-ldflags`.

Pushing a `v*` tag also triggers the `Release Binaries` workflow, which builds `hopbackd` and `hopback-agent` for Linux (`amd64`/`arm64`) and macOS (`amd64`/`arm64`), attaches the `.tar.gz` archives and `SHA256SUMS` to the GitHub Release, and updates the `hopbackd` and `hopback-agent` formulae in [`meshcore-cz/homebrew-tap`](https://github.com/meshcore-cz/homebrew-tap). The `Docker Images` workflow publishes the matching GHCR images.

> The tap update needs a `HOMEBREW_TAP_TOKEN` repository secret — a token with `contents:write` on `meshcore-cz/homebrew-tap`. Without it, the release still publishes; only the Homebrew bump is skipped.

### Homebrew

```sh
brew install meshcore-cz/tap/hopbackd
brew install meshcore-cz/tap/hopback-agent
```

### Prebuilt Binaries

Download the archive for your platform from the [Releases page](https://github.com/meshcore-cz/hopback/releases), verify it, and install:

```sh
tar -xzf hopbackd_v0.9.1_linux_amd64.tar.gz
install -m 755 hopbackd /usr/local/bin/hopbackd
```

`hopbackd` still needs a `config.yaml` in its working directory; the agent reads its `.env` or environment variables.

### Server Install With Go

From a checkout, build the frontend and version-stamped Go binaries before installing them:

```sh
make build
install -m 755 bin/hopbackd /usr/local/bin/hopbackd
install -m 755 bin/hopback-agent /usr/local/bin/hopback-agent
```

Then run `hopbackd` from the directory containing `config.yaml`:

```sh
HOST=0.0.0.0 PORT=3000 hopbackd
```

For an agent-only machine, no frontend build is needed:

```sh
go install github.com/meshcore-cz/hopback/cmd/hopback-agent@latest
```

For a frontend-only static build, such as GitHub Pages:

```sh
PUBLIC_HOPBACK_API_URL=https://hopback.pp0.co npm run build:frontend
```

The Pages workflow in `.github/workflows/pages.yml` publishes `build/`. Set these repository variables as needed:

- `PUBLIC_HOPBACK_API_URL` for the Go backend HTTP origin, for example `https://hopback.pp0.co`
- `PUBLIC_HOPBACK_WS_URL` only if WebSockets use a different origin, for example `wss://hopback.pp0.co`
- `PUBLIC_BASE_PATH` for project Pages, for example `/hopback`; leave empty for a custom domain or root Pages site

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

The included agent can use either the existing meshcore-go JSON IPC daemon or a direct MeshCore companion TCP connection such as `meshcore-proxy`. Agent settings live in `.env`:

```env
HOPBACK_BACKEND_WS=ws://127.0.0.1:3000/agent
HOPBACK_AGENT_SECRET=change-this-secret
HOPBACK_ENDPOINT_ID=kololec
HOPBACK_AGENT_ID=kololec-agent
MESHCORE_URI=ipc+unix://~/Library/Caches/mc/backend.sock
# MESHCORE_URI=ipc+tcp://127.0.0.1:1738
# MESHCORE_URI=tcp://10.0.0.30:5000
# MESHCORE_DEVICE=default-device-id
```

Run the agent with:

```sh
make agent
```

After `make build`, the compiled agent is available at `bin/hopback-agent`.

`HOPBACK_AGENT_SECRET` must match `service.agentSecret` from the backend/web `config.yaml`.

`MESHCORE_URI` takes precedence. The older `MESHCORE_IPC_PATH`, `MESHCORE_IPC_HOST`, and `MESHCORE_IPC_PORT` settings are still accepted when `MESHCORE_URI` is empty.

In IPC mode, the agent opens a dedicated long-running IPC socket and sends `watch_rf` immediately after connecting. meshcore-go acknowledges that request first, then streams RF events with base64 `bytes`; Hopback converts those bytes to packet hex and forwards RSSI/SNR/timestamp to the backend. Reply commands are sent through separate one-off IPC sockets using `send_mesh_packet`:

```json
{ "id": 2, "method": "send_mesh_packet", "params": { "priority": 0, "packet": "..." } }
```

In direct companion mode (`tcp://host:5000`), the agent keeps one persistent `meshcore-go` SDK client. Observations and sends share that companion connection.

The agent fails at startup if required `.env` values are missing or it cannot connect to the configured radio backend. After one successful radio connection, later disconnects are retried.

## Verification

```sh
make check
make lint
make test
make build
```
