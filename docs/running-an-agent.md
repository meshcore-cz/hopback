# Running a Hopback agent

A Hopback **agent** connects one MeshCore radio to a regional Hopback backend.
The backend uses your radio purely as a **packet pipe**: the agent watches raw RF
packets and forwards them to the backend, and relays raw packets the backend asks
it to transmit. That is all it does.

> **Your companion identity is never used.** The agent does **not** read, use, or
> expose the identity (keys, contacts, messages) stored in your MeshCore
> companion app or device. Hopback works at the raw-packet level only — it sends
> and receives packets signed with **its own endpoint private key**, which lives
> on the backend, not on your device. Connecting an agent does not give Hopback
> access to your personal MeshCore identity.

---

## Step 1 — Get a connection secret from your regional operator

Before installing anything, **contact the operators of your regional Hopback
instance** (open an issue on their repository or reach them on your community
channel). Tell them about the node you want to register, including:

- a short **name** and **region/location** for the node,
- the node's **public key**,
- whether you can provide its **private key** for central reply signing, or will
  sign locally,
- the radio's approximate **coordinates** (optional, used for the map).

The operator adds your node as an **endpoint** in their `config.yaml` and sends
you back a unique **agent secret**. You cannot connect without it — the backend
uses the secret to recognise which endpoint your agent serves and to authorise
the connection. Keep it private.

---

## Requirements

- A machine that can reach the backend over WebSocket (a small VPS, SBC, or even
  a Raspberry Pi is plenty).
- A MeshCore radio reachable from that machine through one of:
  - a running [`meshcore-go`](https://github.com/meshcore-cz/meshcore-go) IPC
    daemon (Unix socket or TCP), or
  - a direct MeshCore **companion TCP** bridge such as `meshcore-proxy`.
- Either Docker, or the ability to run a single static Go binary.

---

## Step 2 — Install the agent

### Option A: Docker

```sh
docker run -d --name hopback-agent \
  -e HOPBACK_BACKEND_WS=ws://hopback-host:3000/agent \
  -e HOPBACK_AGENT_SECRET=the-secret-you-received \
  -e MESHCORE_URI=tcp://10.0.0.30:5000 \
  ghcr.io/meshcore-cz/hopback-agent:latest
```

To reach a host Unix IPC socket instead of a TCP bridge, bind-mount the socket
and use `-e MESHCORE_URI=ipc+unix:///path/inside/container.sock`.

### Option B: Binary

Build (or download) `hopback-agent`, then create a `.env` file next to it:

```env
HOPBACK_BACKEND_WS=ws://hopback-host:3000/agent
HOPBACK_AGENT_SECRET=the-secret-you-received
MESHCORE_URI=ipc+unix://~/Library/Caches/mc/backend.sock
```

Run it:

```sh
./hopback-agent
```

---

## Configuration

The agent is configured entirely through environment variables (a local `.env`
file is loaded automatically when present).

| Variable               | Required | Description                                                                                                                                         |
| ---------------------- | -------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `HOPBACK_BACKEND_WS`   | yes      | WebSocket URL of the backend's agent endpoint, e.g. `ws://host:3000/agent` (use `wss://` for TLS).                                                  |
| `HOPBACK_AGENT_SECRET` | yes      | The per-endpoint secret your operator gave you. The backend matches it to your endpoint automatically — you do **not** set an endpoint or agent id. |
| `MESHCORE_URI`         | yes      | How to reach the radio. See below.                                                                                                                  |
| `MESHCORE_DEVICE`      | no       | Device id when your radio backend exposes more than one.                                                                                            |

### `MESHCORE_URI` forms

```env
# meshcore-go daemon over a Unix IPC socket
MESHCORE_URI=ipc+unix://~/Library/Caches/mc/backend.sock
# meshcore-go daemon over TCP IPC
MESHCORE_URI=ipc+tcp://127.0.0.1:1738
# direct companion protocol via a TCP bridge (e.g. meshcore-proxy)
MESHCORE_URI=tcp://10.0.0.30:5000
```

The older `MESHCORE_IPC_PATH`, `MESHCORE_IPC_HOST`, and `MESHCORE_IPC_PORT`
settings are still accepted when `MESHCORE_URI` is empty.

---

## Step 3 — Verify it is running

1. Watch the agent logs. You should see it connect to the backend and then
   `observing MeshCore RF packets via IPC`.
2. Open the instance's **Operator status** page (footer link, `/status`). Your
   endpoint should show **Online**, with the agent version, platform, uptime, and
   a rising **packets** count as RF traffic is observed.

If the endpoint shows **IPC not ready**, the agent reached the backend but cannot
talk to the radio yet — check `MESHCORE_URI` and that your radio backend is
running. If it shows **Offline**, the agent is not connecting — check
`HOPBACK_BACKEND_WS`, network reachability, and that the secret is correct.

---

## Operation notes

- The agent reconnects automatically to both the backend and the radio, so it is
  safe to run under `systemd`, Docker restart policies, or a process supervisor.
- It keeps no local database and stores no secrets beyond the environment, so
  upgrading is just replacing the binary or pulling a new image.
- One agent serves exactly one endpoint. Run additional agents (each with its own
  secret and radio) to cover more nodes.
