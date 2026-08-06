# Canarium

Lightweight, embedded-first power-event orchestrator. Observes environmental facts (UPS state, temperature, GPIO), evaluates policy conditions, and executes ordered shutdown/wake sequences with dependency awareness, safety constraints, and verification.

Not a UPS monitor -- NUT does that. Canarium is the policy and execution layer that NUT deliberately does not provide.

## Features

- **Fact-driven policy** -- pluggable sources (NUT, SNMP, GPIO) produce typed facts; hybrid conditions (structured + expr) evaluate against them with universal dwell support
- **Ordered shutdown** -- stages with entry conditions, dependency awareness, client state tracking, point-of-no-return
- **Verified wake** -- WOL with retry, probe verification, staggered inrush management, per-client wake policy (power_state/retain_state)
- **Abort and resume** -- pre-PONR abort transitions to wake; mid-sequence restart resumes from last completed stage
- **Safety model** -- three-valued logic (unavailable facts never trigger), disarmed/dry-run/armed modes, self-preservation checks
- **Simulation** -- replay fact timelines against plans without executing; same engine as live operation
- **Web UI** -- embedded Svelte SPA with real-time WebSocket updates, client/plan management, event log
- **Single binary** -- Go with embedded frontend, targets Raspberry Pi 3+ (128 MB memory ceiling)

## Quick start

```bash
# Build
make build

# Validate config
./canarium validate -c examples/basic.yaml

# Run (disarmed by default)
./canarium run -c config.yaml

# Simulate a plan
./canarium simulate --plan outage --timeline timeline.json -c config.yaml
```

## Docker (with NUT)

Canarium reads from [NUT](https://networkupstools.org/) — it doesn't talk to UPS hardware directly. The included `compose.yaml` runs both services together using [instantlinux/nut-upsd](https://hub.docker.com/r/instantlinux/nut-upsd):

```bash
# 1. Configure
cp .env.example .env          # edit with your NUT password + client credentials
cp examples/basic.yaml canarium.yaml  # edit with your clients and plans

# 2. Find your UPS USB device
lsusb | grep -i ups            # note the Bus/Device numbers

# 3. Update compose.yaml with your USB device path (or use privileged mode)

# 4. Deploy
docker compose up -d
```

NUT is configured via environment variables in `.env` — no separate config files needed for standard USB UPS setups. For advanced configurations (custom drivers, SNMP UPS, multiple units), mount config files into the NUT container's `/etc/nut/local/` directory.

To run Canarium standalone (without the bundled NUT container):

```bash
docker run -d \
  -p 8420:8420 \
  -v ./canarium.yaml:/etc/canarium/config.yaml \
  -v canarium-data:/var/lib/canarium \
  ghcr.io/nkcx/canarium:latest
```

## Architecture

```
Sources → Fact Context → Policy → Planner → Executor → Transports
```

**Core modules:** NUT, SNMP, SSH, WOL, exec, REST, GPIO, webhook

**Shipped transports:** Proxmox, TrueNAS, OPNsense

## Security notes

- **SSH host key verification:** The SSH transport currently uses `InsecureIgnoreHostKey()`. This is a known limitation for v1 — host key verification against a known_hosts file is planned for a future release.
- **TLS:** Canarium does not terminate TLS directly. Use a reverse proxy (Traefik, nginx, Caddy) for HTTPS.
- **Auth:** v1 ships with single local admin authentication. Federated auth (OIDC, LDAP) is planned for v2.

See [docs/SPEC.md](docs/SPEC.md) for the full specification.
