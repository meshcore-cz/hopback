# Running a Hopback agent

A Hopback **agent** connects one MeshCore radio to a regional Hopback backend.

The agent watches raw RF packets and forwards them to the backend. It also transmits raw packets requested by the backend.

> **Your companion identity is never used.**
>
> The agent does not read, use, or expose the identity, private keys, contacts, channels, or messages stored in your MeshCore companion app or device.
>
> Hopback works only with raw packets. Messages sent by a Hopback endpoint are signed using a separate endpoint private key stored on the regional backend.

---

## Contents

* [Request an agent secret](#step-1--request-an-agent-secret)
* [Requirements](#requirements)
* [Install the agent](#step-2--install-the-agent)

  * [Run from source](#option-a--run-from-source)
  * [Release binary](#option-b--release-binary)
  * [Docker Compose](#option-c--docker-compose)
  * [Homebrew on macOS](#option-d--homebrew-on-macos)
* [Configuration](#configuration)

  * [`MESHCORE_URI` forms](#meshcore_uri-forms)
* [Run as a systemd service](#running-as-a-systemd-service)
* [Verify the connection](#step-3--verify-the-connection)
* [Troubleshooting](#troubleshooting)
* [Operation notes](#operation-notes)

---

## Step 1 — Request an agent secret

Contact the operator of your regional Hopback instance and provide:

* a short name for the node,
* its region or approximate location,
* optional coordinates for the map.

The operator registers the node as a Hopback endpoint and sends you a unique **agent secret**.

The backend uses this secret to identify and authorize your agent. You do not need to configure an endpoint ID or agent ID yourself.

Keep the secret private.

---

## Requirements

You need a machine that can reach:

* the Hopback backend over WebSocket,
* a MeshCore radio through either:

  * a running [`meshcore-go`](https://github.com/meshcore-cz/meshcore-go) IPC daemon, or
  * a direct MeshCore companion TCP bridge such as `meshcore-proxy`.

A Raspberry Pi, home server, VPS, Mac, or similar machine is sufficient.

---

## Step 2 — Install the agent

Choose one of the following methods.

| Method         | Best for                                |
| -------------- | --------------------------------------- |
| Source         | Development and contributors            |
| Release binary | Simple Linux installation               |
| Docker Compose | Servers and container-based deployments |
| Homebrew       | macOS users                             |

### Option A — Run from source

You can build and run the agent directly from the Hopback repository.

See the repository development documentation for source build and development instructions.

---

### Option B — Release binary

Download the correct `hopback-agent` binary for your operating system and architecture from the Hopback releases.

On Linux, install it into `/usr/local/bin`:

```sh
sudo install -m 755 hopback-agent /usr/local/bin/hopback-agent
```

Create a configuration directory:

```sh
mkdir -p ~/.config/hopback-agent
cd ~/.config/hopback-agent
```

Create `.env`:

```env
HOPBACK_BACKEND_WS=wss://hopback.example.org/agent
HOPBACK_AGENT_SECRET=the-secret-you-received
MESHCORE_URI=ipc+tcp://127.0.0.1:1738
```

Run the agent from that directory:

```sh
hopback-agent
```

The agent automatically loads `.env` from its current working directory.

---

### Option C — Docker Compose

Create a directory for the agent:

```sh
mkdir hopback-agent
cd hopback-agent
```

Create `compose.yaml`:

```yaml
services:
  hopback-agent:
    image: ghcr.io/meshcore-cz/hopback-agent:latest
    container_name: hopback-agent
    restart: unless-stopped
    environment:
      HOPBACK_BACKEND_WS: wss://hopback.example.org/agent
      HOPBACK_AGENT_SECRET: the-secret-you-received
      MESHCORE_URI: tcp://10.0.0.30:5000
```

Start the agent:

```sh
docker compose up -d
```

Watch its logs:

```sh
docker compose logs -f
```

To upgrade:

```sh
docker compose pull
docker compose up -d
```

#### Connecting to meshcore-go over TCP IPC

When meshcore-go runs on the Docker host, use:

```yaml
MESHCORE_URI: ipc+tcp://host.docker.internal:1738
```

On Linux, also add a host gateway mapping:

```yaml
services:
  hopback-agent:
    image: ghcr.io/meshcore-cz/hopback-agent:latest
    container_name: hopback-agent
    restart: unless-stopped
    extra_hosts:
      - host.docker.internal:host-gateway
    environment:
      HOPBACK_BACKEND_WS: wss://hopback.example.org/agent
      HOPBACK_AGENT_SECRET: the-secret-you-received
      MESHCORE_URI: ipc+tcp://host.docker.internal:1738
```

The meshcore-go IPC server must listen on an address reachable from the container.

#### Connecting through a Unix socket

Mount the directory containing the socket:

```yaml
services:
  hopback-agent:
    image: ghcr.io/meshcore-cz/hopback-agent:latest
    container_name: hopback-agent
    restart: unless-stopped
    volumes:
      - /absolute/path/to/socket-directory:/meshcore-ipc
    environment:
      HOPBACK_BACKEND_WS: wss://hopback.example.org/agent
      HOPBACK_AGENT_SECRET: the-secret-you-received
      MESHCORE_URI: ipc+unix:///meshcore-ipc/backend.sock
```

Use an absolute path for the host directory. Do not use `~` in Docker volume paths.

---

### Option D — Homebrew on macOS

Install the agent through the MeshCore Homebrew tap:

```sh
brew install meshcore-cz/tap/hopback-agent
```

Create a configuration directory:

```sh
mkdir -p ~/.config/hopback-agent
cd ~/.config/hopback-agent
```

Create `.env`:

```env
HOPBACK_BACKEND_WS=wss://hopback.example.org/agent
HOPBACK_AGENT_SECRET=the-secret-you-received
MESHCORE_URI=ipc+unix:///Users/your-user/Library/Caches/mc/backend.sock
```

Run the agent from that directory:

```sh
hopback-agent
```

To upgrade:

```sh
brew update
brew upgrade hopback-agent
```

---

## Configuration

The agent is configured through environment variables.

A local `.env` file is loaded automatically when present in the current working directory. Docker Compose and systemd can provide the same variables directly.

| Variable               | Required | Description                                                                      |
| ---------------------- | -------- | -------------------------------------------------------------------------------- |
| `HOPBACK_BACKEND_WS`   | yes      | WebSocket URL of the backend agent endpoint. Use `wss://` when TLS is available. |
| `HOPBACK_AGENT_SECRET` | yes      | Secret provided by the regional Hopback operator.                                |
| `MESHCORE_URI`         | yes      | Connection URI describing how to reach the MeshCore radio.                       |
| `MESHCORE_DEVICE`      | no       | Device ID when meshcore-go exposes more than one radio.                          |

Do not commit your agent secret to a public repository.

### `MESHCORE_URI` forms

#### meshcore-go over a Unix socket

```env
MESHCORE_URI=ipc+unix:///home/user/.cache/mc/backend.sock
```

Typical macOS example:

```env
MESHCORE_URI=ipc+unix:///Users/user/Library/Caches/mc/backend.sock
```

When using a local `.env` file, a home-directory shortcut is also accepted:

```env
MESHCORE_URI=ipc+unix://~/Library/Caches/mc/backend.sock
```

Prefer an absolute path with Docker and systemd.

#### meshcore-go over TCP IPC

```env
MESHCORE_URI=ipc+tcp://127.0.0.1:1738
```

#### Direct companion TCP bridge

For a bridge such as `meshcore-proxy`:

```env
MESHCORE_URI=tcp://10.0.0.30:5000
```

The URI types use different protocols:

* `ipc+unix://` connects to meshcore-go IPC through a Unix socket.
* `ipc+tcp://` connects to meshcore-go IPC over TCP.
* `tcp://` connects directly to the MeshCore companion protocol over TCP.

The older `MESHCORE_IPC_PATH`, `MESHCORE_IPC_HOST`, and `MESHCORE_IPC_PORT` variables are still supported when `MESHCORE_URI` is empty.

New installations should use `MESHCORE_URI`.

---

## Running as a systemd service

This example assumes the release binary is installed at:

```text
/usr/local/bin/hopback-agent
```

Create `/etc/hopback-agent.env`:

```env
HOPBACK_BACKEND_WS=wss://hopback.example.org/agent
HOPBACK_AGENT_SECRET=the-secret-you-received
MESHCORE_URI=ipc+tcp://127.0.0.1:1738
```

Protect the file:

```sh
sudo chmod 600 /etc/hopback-agent.env
```

Create `/etc/systemd/system/hopback-agent.service`:

```ini
[Unit]
Description=Hopback MeshCore agent
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/hopback-agent.env
ExecStart=/usr/local/bin/hopback-agent
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start the service:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now hopback-agent
```

Check its status:

```sh
systemctl status hopback-agent
```

Follow its logs:

```sh
journalctl -u hopback-agent -f
```

When using a Unix socket, configure an absolute path:

```env
MESHCORE_URI=ipc+unix:///run/meshcore/backend.sock
```

The systemd service must have permission to access the socket.

---

## Step 3 — Verify the connection

Inspect the agent logs.

A successful connection should show that the agent:

1. connected to the MeshCore radio or IPC daemon,
2. connected to the Hopback backend,
3. started observing raw RF packets.

Depending on the installation method:

```sh
# Docker Compose
docker compose logs -f

# systemd
journalctl -u hopback-agent -f
```

Then open the Hopback instance's **Operator status** page, usually available through the footer or at `/status`.

Your endpoint should show:

* **Online** status,
* agent version and platform,
* uptime,
* radio connection status,
* an increasing packet count as RF traffic is observed.

---

## Troubleshooting

### IPC not ready

The agent connected to the Hopback backend but cannot reach the MeshCore radio.

Check:

* that `MESHCORE_URI` uses the correct protocol,
* that meshcore-go or the TCP bridge is running,
* the socket path or TCP address,
* Unix socket permissions,
* Docker networking and socket mounts,
* `MESHCORE_DEVICE` when meshcore-go exposes multiple radios.

### Offline

The backend is not receiving an agent connection.

Check:

* `HOPBACK_BACKEND_WS`,
* whether `ws://` or `wss://` is correct,
* network and firewall access,
* whether the agent secret is correct,
* the agent logs.

### Online but no packets

The backend connection works, but no RF packets are being observed.

Check:

* that the radio is connected,
* that the correct device is selected,
* that the node is receiving RF traffic,
* that the radio frequency and parameters are correct.

---

## Operation notes

* The agent automatically reconnects to both the backend and the radio.
* It keeps no local database.
* The only local secret is the configured agent secret.
* Upgrading means replacing the binary, pulling the newest Docker image, or running `brew upgrade`.
* One agent serves exactly one Hopback endpoint.
* Run one agent per radio or location, with a separate secret for each endpoint.
