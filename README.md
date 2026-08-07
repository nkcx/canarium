# Canarium

Canarium monitors environmental conditions -- power loss, temperature, flooding -- and orchestrates the orderly transition of your infrastructure from up to down. When those conditions clear, it brings everything back up again, in the right order, verified and safe.

It's not a monitoring tool or an early warning system. It's the thing that actually *does something* when the environment changes: shuts down your servers in the right order before the battery runs out, then wakes them back up when power returns and the UPS has recharged.

## Why

When the power goes out at 2am, you need something that:

- Shuts down your NAS *before* your hypervisor, not after
- Waits for the battery to actually recharge before waking anything
- Doesn't wake a machine that was off before the outage
- Confirms each host is actually down before moving on
- Can be tested without staging a blackout

NUT monitors your UPS. Commercial suites are vendor-locked. Homelab scripts don't generalize. Canarium is the orchestration layer in between.

## Features

- **Environmental awareness** -- pluggable sources (UPS via NUT, SNMP, GPIO sensors) feed environmental state into a unified fact model
- **Ordered shutdown** -- staged transitions with dependency awareness, entry conditions, and point-of-no-return
- **Verified wake** -- WOL with retry and probe verification, staggered to manage inrush, per-client wake policy
- **Abort and resume** -- power comes back mid-shutdown? Canarium stops, waits for in-flight shutdowns to complete, then starts bringing things back up
- **Safety first** -- losing contact with a sensor never triggers a shutdown; three operating modes (disarmed, dry-run, armed) so you can watch before you trust
- **Simulation** -- replay recorded or scripted outage timelines against your plans without touching anything real
- **Web UI** -- see what's happening, what would happen, and what did happen
- **Runs anywhere** -- single Go binary with embedded UI, targets a Raspberry Pi 3 with a 128 MB memory ceiling

## Quick start

```bash
# Build
make build

# Validate config
./canarium validate -c examples/basic.yaml

# Run (starts in disarmed mode -- watches but doesn't act)
./canarium run -c config.yaml

# Simulate a plan against a scripted outage
./canarium simulate --plan outage --timeline timeline.json -c config.yaml
```

## Docker (with NUT)

Canarium reads from [NUT](https://networkupstools.org/) -- it doesn't talk to UPS hardware directly. The included `compose.yaml` runs both services together using [instantlinux/nut-upsd](https://hub.docker.com/r/instantlinux/nut-upsd):

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

NUT is configured via environment variables in `.env` -- no separate config files needed for standard USB UPS setups. For advanced configurations (custom drivers, SNMP UPS, multiple units), mount config files into the NUT container's `/etc/nut/local/` directory.

To run Canarium standalone (without the bundled NUT container):

```bash
docker run -d \
  -p 8420:8420 \
  -v ./canarium.yaml:/etc/canarium/config.yaml \
  -v canarium-data:/var/lib/canarium \
  ghcr.io/nkcx/canarium:latest
```

## How it works

```
Sources → Fact Context → Policy → Planner → Executor → Transports
```

**Sources** produce environmental facts (battery charge, temperature, status flags). **Policy** evaluates conditions against those facts with optional dwell requirements ("battery above 60% for 5 minutes"). **Plans** define staged shutdown and wake sequences. **Transports** execute actions against your infrastructure (SSH, Proxmox API, WOL, SNMP PoE, etc).

**Core modules:** NUT, SNMP, SSH, WOL, exec, REST, GPIO, webhook

**Shipped transports:** Proxmox, TrueNAS, OPNsense

## Security notes

- **SSH host key verification:** The SSH transport currently uses `InsecureIgnoreHostKey()`. This is a known limitation for v1 -- host key verification against a known_hosts file is planned for a future release.
- **TLS:** Canarium does not terminate TLS directly. Use a reverse proxy (Traefik, nginx, Caddy) for HTTPS.
- **Auth:** v1 ships with single local admin authentication. Federated auth (OIDC, LDAP) is planned for v2.

See [docs/SPEC.md](docs/SPEC.md) for the full specification.
