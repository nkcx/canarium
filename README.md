<p align="center">
  <img src="docs/Canarium.jpg" alt="Canarium" width="250">
</p>

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
- **Safety first** -- stale or unknown facts never satisfy a condition, so losing contact with a sensor cannot trigger a shutdown; three operating modes (disarmed, dry-run, armed) so you can watch before you trust
- **Simulation** -- replay scripted outage timelines against your plans without touching anything real, including which hosts *wouldn't* get shut down
- **Preflight** -- `canarium doctor` contacts everything your config names, so a bad credential surfaces on a Tuesday rather than during an outage
- **Web UI** -- see what's happening, what would happen, and what did happen. Built for the case it's actually used in: read on a phone, in the dark, while the battery drains. Runtime remaining leads the page; everything else is arranged behind it
- **Runs anywhere** -- single Go binary with embedded UI, targets a Raspberry Pi 3 with a 128 MB memory ceiling

## Quick start

```bash
# Build
make build

# Check the config offline: transports, fact references, expressions,
# client references, dependency ordering. No network access.
./canarium validate -c examples/basic.yaml

# Check it against reality: can sources authenticate and report? do client
# addresses resolve and answer? are credentials present?
./canarium doctor -c config.yaml

# Replay a scripted outage against a plan, without touching anything
./canarium simulate --plan outage --timeline examples/outage-timeline.json -c config.yaml

# Run (starts in disarmed mode -- watches but doesn't act)
./canarium run -c config.yaml
```

Other commands:

```bash
./canarium hash-password              # for canarium.auth.password_hash
./canarium token create ci --scope read   # API token for monitoring
./canarium token list
./canarium token revoke ci
```

On first run, open the web UI and set an admin password. Until you do, the
API rejects every request -- Canarium does not run unauthenticated.

## Docker (with NUT)

Canarium reads from [NUT](https://networkupstools.org/) -- it doesn't talk to UPS hardware directly. The included `compose.yaml` runs both services together using [instantlinux/nut-upsd](https://hub.docker.com/r/instantlinux/nut-upsd):

```bash
# 1. Configure
cp .env.example .env          # edit with your NUT password + client credentials
cp examples/basic.yaml canarium.yaml  # edit with your clients and plans

# 2. Find your UPS USB device
lsusb | grep -i ups            # note the Bus/Device numbers

# 3. Deploy
docker compose up -d
```

**USB passthrough.** The compose file passes the UPS through without
privileged mode. Three things have to line up, and it does all three:
`/dev/bus/usb` is bind-mounted whole (so a UPS that re-enumerates does not
break the mapping), `device_cgroup_rules` grants access to USB character
devices (major 189), and SELinux confinement is disabled for that container
-- on Fedora IoT, Fedora CoreOS and RHEL the container type otherwise cannot
read device nodes under `/dev/bus/usb`, and the denial appears only in the
host's audit log (`ausearch -m AVC -ts recent`).

NUT is configured via environment variables in `.env` -- no separate config files needed for standard USB UPS setups. For advanced configurations (custom drivers, SNMP UPS, multiple units), mount config files into the NUT container's `/etc/nut/local/` directory.

**WOL and multi-VLAN:** Each client's wake config targets a specific broadcast address for its subnet. Canarium infers this from the client IP when possible, but for multi-VLAN setups you should set it explicitly (`broadcast: 10.0.10.255`). To reach multiple VLANs from Docker, attach the container to macvlan or ipvlan networks -- see the commented examples in `compose.yaml`.

To run Canarium standalone (without the bundled NUT container):

```bash
docker run -d \
  -p 8420:8420 \
  -v ./canarium.yaml:/etc/canarium/config.yaml \
  -v canarium-data:/var/lib/canarium \
  ghcr.io/nkcx/canarium:0.1.3
```

### Image tags

Images are published to `ghcr.io/nkcx/canarium` for `linux/amd64`,
`linux/arm64` and `linux/arm/v7`.

| Tag | Moves |
|---|---|
| `0.1.3` | Never. Pin this for anything you care about. |
| `0.1` | With each patch release in the 0.1 series. |
| `latest` | With each release. |
| `main` | With every commit to `main`. Unreleased; expect breakage. |

Each image carries an SBOM and signed build provenance, so you can
establish it was built by this repository rather than by whoever last held
a registry token:

```bash
gh attestation verify oci://ghcr.io/nkcx/canarium:0.1.3 --repo nkcx/canarium
```

## How it works

```
Sources → Fact Context → Policy → Planner → Executor → Transports
```

**Sources** produce environmental facts (battery charge, temperature, status flags). **Policy** evaluates conditions against those facts with optional dwell requirements ("battery above 60% for 5 minutes"). **Plans** define staged shutdown and wake sequences. **Transports** execute actions against your infrastructure (SSH, Proxmox API, WOL, SNMP PoE, etc).

**Sources** (produce facts): NUT, SNMP, GPIO

**Transports** (act on clients): SSH, WOL, exec, REST, SNMP PoE, NUT outlet,
Proxmox, TrueNAS, OPNsense

**Notifications:** webhook

### Multi-UPS

Clients declare which UPS feeds them, and a policy for what that means:

```yaml
clients:
  - name: hypervisor
    feeds: [rack_ups_a, rack_ups_b]
    feed_policy: all        # dual-PSU: only threatened when both are failing
  - name: nas
    feeds: [rack_ups_a]
    feed_policy: any        # single-PSU (the default)
```

Canarium derives a `client.<name>.threatened` fact from this, which stages can
condition on. If a feed's status cannot be read, the client is treated as *not*
threatened -- losing a sensor never starts a shutdown. Set
`comms_loss_assumes: threatened` per client to invert that.

## Security notes

- **Authentication fails closed.** Until an admin password is set, every
  authenticated endpoint refuses requests. The web UI's first-run screen
  creates one; `canarium hash-password` pins one in the config file instead,
  for immutable deployments.

- **SSH host keys are verified.** The default policy is `accept-new`: a host's
  key is learned on first contact and pinned thereafter, and a subsequent
  change is refused. Set `transports.ssh.host_key_policy: strict` to require
  keys be present in `known_hosts` up front. Learned keys live in
  `<data_dir>/known_hosts`.

- **TLS certificates are verified** for the Proxmox, TrueNAS and OPNsense
  transports. Appliances with self-signed certificates need one of:

  ```yaml
  config:
    tls_ca_cert: /etc/canarium/nas-ca.pem     # pin the appliance's cert
    # or, accepting the risk knowingly:
    tls_insecure_skip_verify: true
  ```

- **Canarium terminates no TLS of its own.** Put it behind a reverse proxy
  (Traefik, nginx, Caddy) for HTTPS, and set
  `canarium.auth.trust_proxy_headers: true` so session cookies are marked
  `Secure`. Forwarded headers are honoured only from a trusted peer —
  loopback and the private ranges by default:

  ```yaml
  canarium:
    auth:
      trust_proxy_headers: true
      trusted_proxies: [10.0.0.8/32]   # narrow it to your proxy
  ```

  A request from anywhere else is treated as direct, so nobody can forge
  their own source address by sending `X-Forwarded-For`. The shipped compose
  file binds the API to localhost.

- **API tokens** carry a scope: `read` can observe, `admin` can arm the
  executor and abort sequences. Give monitoring a read token.

- **Auth is a single local admin**, rotatable from Settings. Federated auth
  (OIDC, LDAP) is not implemented.

- **Every finished sequence writes a JSONL audit journal** to `journal/` in
  the data directory: what ran, what was dispatched, what came back, and the
  addresses as they were before anything went down.

See [docs/SPEC.md](docs/SPEC.md) for the full specification.

## License

MIT — see [LICENSE](LICENSE).
