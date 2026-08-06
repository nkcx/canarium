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

## Docker

```bash
docker run -d \
  -p 8420:8420 \
  -v ./config.yaml:/etc/canarium/config.yaml \
  -v canarium-data:/var/lib/canarium \
  ghcr.io/nkcx/canarium:latest
```

## Architecture

```
Sources → Fact Context → Policy → Planner → Executor → Transports
```

**Core modules:** NUT, SNMP, SSH, WOL, exec, REST, GPIO, webhook

**Shipped transports:** Proxmox, TrueNAS, OPNsense

See [docs/SPEC.md](docs/SPEC.md) for the full specification.
