# Canarium User Guide

This guide walks through configuring and deploying Canarium. For the full technical specification, see [SPEC.md](SPEC.md).

---

## What Canarium does

Canarium monitors environmental conditions — power loss, temperature, UPS state — and orchestrates the orderly transition of your infrastructure from up to down. When those conditions clear, it brings everything back up again, in the right order, verified and safe.

Every action is part of a **plan**: a sequence of **stages** that execute in order, each with its own entry condition. Stages contain **clients** — the servers, switches, and devices Canarium controls. Clients have **transports** — the mechanism Canarium uses to shut them down and wake them up.

## Concepts

**Facts** are environmental observations: battery charge, UPS status, temperature. They come from **sources** like NUT, SNMP, or GPIO sensors.

**Conditions** test facts: "battery below 50%" or "UPS on battery for 30 seconds." Conditions can require a **dwell** period — the condition must stay true for a duration before it fires.

**Plans** tie conditions to actions. A plan has a **trigger** (when to start), an **abort** condition (when to stop, if power returns), **shutdown stages** (what to turn off and when), and a **wake** section (how to bring things back).

## Configuration overview

Canarium's configuration is a single YAML file. It has four sections:

```yaml
canarium:       # daemon settings (mode, listen address, data directory)
sources:        # where environmental data comes from
clients:        # what Canarium controls
plans:          # what happens when conditions are met
```

---

## Sources

Sources produce facts. The most common source is NUT, which monitors a UPS.

### NUT source

```yaml
sources:
  - name: ups
    type: nut
    config:
      instances:
        - name: rack_ups
          host: nut              # NUT server hostname or IP
          port: 3493
          ups: ups               # UPS name in NUT (from ups.conf)
          username: canarium     # NUT user (from upsd.users)
          password: ${NUT_PASSWORD}
          poll_interval: 15s
```

This produces facts like:
- `rack_ups.battery.charge` — battery percentage (0–100)
- `rack_ups.battery.runtime` — estimated seconds remaining
- `rack_ups.status` — UPS status flags (OL, OB, LB, CHRG, etc.)
- `rack_ups.ups.load` — load percentage

NUT status is a **set of flags**, not a single value. A UPS can be `OL CHRG` (online and charging) simultaneously. Use `contains` in conditions to test for a specific flag.

### SNMP source

Poll arbitrary SNMP OIDs from network devices:

```yaml
sources:
  - name: switch_monitor
    type: snmp
    config:
      instances:
        - name: core_switch
          host: 10.0.1.2
          snmp_version: 2
          snmp_community: public
          poll_interval: 30s
          oids:
            - oid: "1.3.6.1.2.1.1.3.0"
              name: uptime
              type: number
```

### GPIO source

Read GPIO pins on a Raspberry Pi (digital inputs, power-present detection):

```yaml
sources:
  - name: gpio
    type: gpio
    config:
      pins:
        - name: mains_present
          chip: gpiochip0
          line: 17
          type: digital
          active_low: false
          poll_interval: 5s
          description: "Mains power detection relay"
```

Requires `/dev/gpiochip0` passed through to the container. Temperature sensors (1-Wire, e.g., DS18B20) use the kernel's `/sys/bus/w1` interface, not GPIO chardev — a dedicated temperature source module is planned for v1.1.

---

## Clients

A client is anything Canarium can shut down or wake up: a server, a NAS, a firewall, or a group of PoE-powered devices.

### Basic client (SSH)

The simplest client uses SSH to issue a shutdown command and a TCP probe to check if it's up:

```yaml
clients:
  - name: webserver
    transport: ssh
    address: 10.0.10.50
    mac: "aa:bb:cc:dd:ee:01"
    tags: [compute]
    feeds: [rack_ups]
    shutdown_budget: 3m
    probe:
      method: tcp
      port: 22
    wake:
      transport: wol
      broadcast: 10.0.10.255
```

**`address`** — hostname or IP. Canarium resolves hostnames to IPs and discovers MACs via ARP automatically, keeping the cache fresh. At sequence start, it snapshots all resolved addresses so wake doesn't depend on DNS (which may be down).

**`mac`** — optional if Canarium can discover it via ARP (same subnet, host is up). Required for reliable WOL if the host is on a different VLAN or might be down when Canarium starts.

**`shutdown_budget`** — how long to wait for the client to shut down. Also serves as the state transition timeout: if Canarium can't probe the client (e.g., the switch it's behind is already down), it assumes shutdown completed after this duration.

**`tags`** — labels for grouping clients in stages. A client can have multiple tags.

**`feeds`** — which UPS sources power this client. Omit for single-UPS setups (defaults to all sources).

### Windows Server (SSH)

Windows with OpenSSH uses the same `ssh` transport with a custom command:

```yaml
clients:
  - name: windows-server
    transport: ssh
    address: 10.0.10.60
    mac: "aa:bb:cc:dd:ee:02"
    tags: [compute]
    feeds: [rack_ups]
    shutdown_budget: 3m
    config:
      command: "shutdown /s /t 0"
    probe:
      method: tcp
      port: 22
    wake:
      transport: wol
      broadcast: 10.0.10.255
```

The `config.command` field overrides the default `shutdown -h now`. The SSH transport is key-based only — mount your SSH keys into the container and configure the key path if it's not the default.

### Proxmox hypervisor

```yaml
clients:
  - name: hypervisor
    transport: proxmox
    address: 10.0.10.11
    mac: "aa:bb:cc:dd:ee:03"
    credentials: user@pam!canarium=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
    tags: [compute]
    feeds: [rack_ups]
    shutdown_budget: 5m
    depends_on: [storage]
    probe:
      method: tcp
      port: 8006
    wake:
      transport: wol
      broadcast: 10.0.10.255
```

The Proxmox transport uses the API with a token scoped to `Sys.PowerMgmt`. **Set `shutdown_budget` generously** — Proxmox must gracefully stop all VMs and containers before the node shuts down. If your VMs take 3 minutes to stop, the node needs at least that plus overhead.

**`depends_on: [storage]`** means: shut down the hypervisor *before* the storage (it goes down first), and wake the storage *before* the hypervisor (it comes up first). If storage fails to wake, the hypervisor is not attempted. Use this when the hypervisor mounts NFS or iSCSI from the storage server.

### TrueNAS storage

```yaml
clients:
  - name: storage
    transport: truenas
    address: 10.0.10.20
    mac: "aa:bb:cc:dd:ee:04"
    credentials: 1-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    tags: [storage]
    feeds: [rack_ups]
    shutdown_budget: 5m
    probe:
      method: tcp
      port: 443
    wake:
      transport: wol
      broadcast: 10.0.10.255
```

TrueNAS uses JSON-RPC 2.0 over WebSocket. The module auto-detects the API version and uses the appropriate authentication method (legacy API key or SCRAM-SHA-512 for TrueNAS 26+).

### OPNsense firewall

```yaml
clients:
  - name: firewall
    transport: opnsense
    address: 10.0.1.1
    credentials: api-key:api-secret
    tags: [infrastructure]
    feeds: [rack_ups]
    shutdown_budget: 2m
    probe:
      method: tcp
      port: 443
```

Note: this firewall has no `wake:` section. Canarium won't attempt to wake it. If your firewall supports WOL or IPMI, add a wake config. If it relies on BIOS "restore on AC power," consider using the plan's `post_shutdown` action to cycle UPS outlet power (see [Post-shutdown UPS action](#post-shutdown-ups-action)).

### PoE-powered devices (SNMP)

Use the `snmp-poe` transport to control Power over Ethernet on managed switches. This is useful when you want to cut power to a group of devices (cameras, access points, IoT sensors) to conserve battery during an outage.

**Key concept:** the client represents a *group of PoE ports on a switch*, not the switch itself. The switch stays on — Canarium controls which ports supply power.

```yaml
clients:
  - name: cameras
    description: "All PoE cameras on core switch"
    transport: snmp-poe
    address: 10.0.1.2          # the switch's management IP
    tags: [nonessential]
    feeds: [rack_ups]
    shutdown_budget: 0s        # PoE off is instant
    config:
      snmp_version: 3
      snmp_user: canarium
      snmp_auth_pass: ${SWITCH_AUTH_PASS}
      snmp_priv_pass: ${SWITCH_PRIV_PASS}
      ports:
        - { group: 1, port: 1 }
        - { group: 1, port: 2 }
        - { group: 1, port: 3 }
        - { group: 1, port: 4 }
        - { group: 1, port: 5 }
        - { group: 1, port: 6 }
        - { group: 1, port: 7 }
        - { group: 1, port: 8 }
```

When this client's stage triggers, Canarium issues SNMP SET commands to disable PoE on all listed ports simultaneously. When the wake plan runs, it re-enables them.

**How it works:** Canarium writes to the RFC 3621 POWER-ETHERNET-MIB OID `pethPsePortAdminEnable` (1.3.6.1.2.1.105.1.1.1.3). Each port is indexed by `group` and `port` number. On most non-modular switches, `group` is always `1`. The port number is the physical port number.

**Finding port numbers:** Log into your switch's management interface and note which physical ports your PoE devices are connected to. Or query the MIB directly:

```bash
# List all PoE port states (requires snmpwalk)
snmpwalk -v3 -u canarium -l authPriv \
  -a SHA -A 'auth-pass' -x AES -X 'priv-pass' \
  10.0.1.2 1.3.6.1.2.1.105.1.1.1.3
```

**Grouping:** You can have one client per device (`camera-front-door` with port 5) or one client for all devices (`all-cameras` with ports 1–8). Grouping all ports into one client means they all power down and up together as a single operation. Use separate clients if you need different shutdown/wake timing for different devices.

**SNMPv3 is recommended** because PoE control requires SNMP write access. SNMPv2c works but sends the community string in plaintext. Configure your switch with an SNMPv3 user that has write access to the POWER-ETHERNET-MIB.

**Not all switches support the standard MIB.** Some vendors use proprietary OIDs. Run the `snmpwalk` command above — if it returns values, the standard MIB is supported. If not, you may need a vendor-specific module (not yet implemented) or the `exec` transport with a CLI command.

**Don't cut your own uplink.** Canarium validates that PoE clients don't include the port carrying Canarium's own network path, where it can determine this. But it can't always know your topology — double-check that you're not listing the port your Pi is connected to.

### Clients without wake

Not every client needs a wake path. A client with no `wake:` section will be shut down but not woken. This is appropriate for:

- Devices that auto-start on power restoration (BIOS "restore on AC")
- Devices where wake is handled by another system
- PoE devices that will be re-enabled when their switch port is powered back on

### Wake policy

By default, Canarium wakes every client in the plan that's found to be down. If you have machines that are intentionally powered off (dev boxes, seasonal workloads), set `wake_policy: retain_state` — Canarium will only wake them if they were up before the outage:

```yaml
clients:
  - name: dev-box
    wake_policy: retain_state
    # ... rest of config
```

---

## Dependencies and ordering

### Stages

Stages execute in order, top to bottom. Each stage has a `when` condition that must be true before it begins. Clients within a stage shut down concurrently.

A typical ordering: shed non-essential load first (PoE devices), then compute (hypervisors, app servers), then storage, then infrastructure (firewalls, switches).

### depends_on (hard dependency)

`depends_on` declares a service dependency. "A depends on B" means:

- **Shutdown:** A goes down before B
- **Wake:** B comes up before A
- **Failure propagation:** if B fails to wake, A is not attempted

Use this when one host genuinely cannot function without another (hypervisor needs its NFS server).

Canarium validates that a client and its dependency are not in the same stage — that would be a contradiction (they can't shut down concurrently if one must go first).

### after/before (soft ordering)

`after` and `before` express ordering preferences without hard failure propagation. If A is `after: [B]` and B fails to wake, A is still attempted.

When explicit stages are specified, `after`/`before` serve as validation only — Canarium warns if the stage order contradicts the declared preference but doesn't override it.

---

## Plans

A plan defines what happens when environmental conditions change.

### Trigger

The condition that starts the shutdown sequence:

```yaml
trigger:
  condition: state
  fact: rack_ups.status
  contains: OB         # UPS is On Battery
  for: 30s             # must be true for 30 seconds
```

The `for` (dwell) prevents a momentary power blip from triggering a full shutdown. 30 seconds is a reasonable default.

### Abort

The condition that cancels the shutdown (if it hasn't passed the point of no return):

```yaml
abort:
  condition: state
  fact: rack_ups.status
  contains: OL         # UPS is On Line (mains restored)
  for: 60s
```

The longer dwell on abort (60s vs 30s on trigger) prevents abort-and-retrigger cycling during flapping power.

When abort fires:
1. Canarium stops issuing new shutdown commands
2. Clients already shutting down are allowed to complete (you can't un-send a shutdown)
3. Once all in-flight shutdowns finish, the wake plan begins
4. Wake probes each client — those still up are skipped, those already down are woken

### Shutdown stages

```yaml
shutdown:
  stages:
    - name: nonessential
      when: "true"                    # immediately on trigger
      clients: [tag:nonessential]
      budget: 10s

    - name: compute
      when:
        condition: numeric
        fact: rack_ups.battery.charge
        below: 50
      clients: [tag:compute]
      budget: 5m
      point_of_no_return: true

    - name: storage
      when:
        condition: numeric
        fact: rack_ups.battery.charge
        below: 35
      clients: [tag:storage]
      budget: 5m

    - name: infrastructure
      when:
        condition: numeric
        fact: rack_ups.battery.charge
        below: 25
      clients: [tag:infrastructure]
      budget: 3m
```

**Stage `when` conditions** control *when* a stage begins, not *whether* it begins. If the battery stabilizes at 55%, the "compute" stage (below 50) hasn't triggered yet. If power returns and abort fires, those clients are never shut down.

**`budget`** is the maximum time the stage waits for all its clients to confirm shutdown. It should be at least as long as the longest `shutdown_budget` of any client in the stage.

**`point_of_no_return`** — once this stage starts executing, the abort condition is ignored. The sequence will complete the full shutdown and then run the wake plan. Set this on the stage where aborting would leave you worse off than completing (e.g., once the DNS server is down, aborting leaves half your infrastructure unreachable).

PONR must be explicitly marked — Canarium does not infer it.

### Wake

```yaml
wake:
  gate:
    condition: and
    conditions:
      - condition: state
        fact: rack_ups.status
        contains: OL
      - condition: numeric
        fact: rack_ups.battery.charge
        above: 60
        for: 5m
  stagger: 45s
  order: reverse
  probe_interval: 30s
  boot_deadline: 5m
  retries: 3
```

**`gate`** — the condition that must be true before any client is woken. The canonical gate is "mains power restored AND battery above 60% for 5 minutes." The dwell prevents waking into a flapping outage.

**`stagger`** — delay between waking each client. Prevents coordinated inrush current from spinning disks and PSU capacitors hitting the UPS while it's mid-recharge.

**`order: reverse`** — wake in reverse shutdown order (infrastructure first, then storage, then compute, then nonessential). Or specify explicit wake stages.

**`probe_interval`** — how often to check if a woken client is up.

**`boot_deadline`** — how long to wait for a client to come up after sending wake.

**`retries`** — how many times to re-send WOL if the client doesn't come up within the boot deadline. WOL is unacknowledged UDP — it gets dropped by switches, and NICs lose WOL config after hard power cuts.

### Post-shutdown UPS action

After all stages complete, Canarium can command the UPS to cut and restore outlet power. This enables wake for hosts that have no WOL or IPMI but have BIOS set to "restore on AC power":

```yaml
shutdown:
  post_shutdown:
    action: upscmd
    command: shutdown.return
    delay: 120              # seconds before UPS cuts outlets
    ups: rack_ups
```

The UPS cuts outlet power after the delay, then restores it when mains returns. Hosts with "restore on AC" boot automatically. **Make sure Canarium's own host is not powered by an outlet that will be cut** — it needs to stay on to run the wake plan.

---

## Conditions

### Structured conditions

For common comparisons, use structured conditions. The web UI can render these as forms.

**Numeric:** compare a fact to a threshold.
```yaml
condition: numeric
fact: rack_ups.battery.charge
below: 50
```

**State:** test a fact's value. For set-type facts (like NUT status), use `contains`.
```yaml
condition: state
fact: rack_ups.status
contains: OB
```

**Logical:** combine conditions with `and`, `or`, `not`.
```yaml
condition: and
conditions:
  - condition: state
    fact: rack_ups.status
    contains: OL
  - condition: numeric
    fact: rack_ups.battery.charge
    above: 60
    for: 5m
```

### Dwell (for:)

Every condition type supports `for:` — a duration the condition must remain true before it fires. This is critical for wake gates ("battery above 60% for 5 minutes") and trigger debouncing ("on battery for 30 seconds").

### Template expressions

For logic that structured conditions can't express, use the `expr` template syntax:

```yaml
condition: template
value: >
  fact("rack_ups.battery.charge") > 60 &&
  contains(fact("rack_ups.status"), "OL") &&
  fact("temp.rack.celsius") < 35
```

Facts are accessed via `fact("key")`, which returns the value or `nil` if unavailable. `quality("key")` returns `"good"`, `"stale"`, or `"unknown"`. `age("key")` returns seconds since last update.

---

## Safety

### Modes

Canarium starts in **disarmed** mode. Sources poll, conditions evaluate, everything is logged, but nothing executes. This lets you verify that facts are flowing and conditions evaluate as expected.

Switch to **dry-run** to test the full sequence with real timing — transports log what they would do instead of acting.

Switch to **armed** only when you're confident the configuration is correct.

### Fail-safe behavior

- **Unknown facts never trigger shutdown.** If Canarium loses contact with NUT, it does not assume the power is out. Losing sensor data is not evidence of an emergency.
- **Unknown facts never satisfy wake gates.** If Canarium can't read the UPS, it doesn't start waking things up either.
- **Abort doesn't strand in-flight shutdowns.** When abort fires, clients mid-shutdown are allowed to finish before wake begins.
- **Canarium never shuts down its own host.** Validation rejects configs where the host running Canarium appears as a client.

### Simulation

Test your plans without touching real infrastructure:

```bash
# Create a fact timeline (JSON)
cat > outage.json << 'EOF'
{
  "duration": 600000000000,
  "events": [
    {"at": 0, "fact": "rack_ups.status", "value": ["OL"]},
    {"at": 0, "fact": "rack_ups.battery.charge", "value": 100},
    {"at": 30000000000, "fact": "rack_ups.status", "value": ["OB"]},
    {"at": 120000000000, "fact": "rack_ups.battery.charge", "value": 45},
    {"at": 300000000000, "fact": "rack_ups.battery.charge", "value": 20},
    {"at": 400000000000, "fact": "rack_ups.status", "value": ["OL"]},
    {"at": 400000000000, "fact": "rack_ups.battery.charge", "value": 80}
  ]
}
EOF

# Run the simulation
canarium simulate --plan outage --timeline outage.json -c config.yaml
```

Durations are in nanoseconds (Go convention). The simulation uses the same evaluation engine as live operation — if it triggers in simulation, it will trigger for real.

---

## Deployment

### Docker Compose (recommended)

See the project's `compose.yaml` for a complete example with NUT. Key points:

- NUT needs USB passthrough to talk to the UPS (`privileged: true` or specific `devices:`)
- Canarium needs access to your SSH keys for SSH-based transports
- For WOL across VLANs, attach Canarium to Docker networks that can reach each target subnet (macvlan or ipvlan)
- For Traefik, use labels instead of port mapping

### WOL and network topology

WOL magic packets are UDP broadcasts. Each client's `broadcast` address targets a specific subnet. Canarium infers the broadcast from the client IP when it can see the subnet (same L2 network), but for multi-VLAN setups you should set it explicitly:

```yaml
wake:
  transport: wol
  broadcast: 10.0.10.255      # broadcast for the 10.0.10.0/24 VLAN
```

To send WOL across VLANs from a Docker container, attach it to macvlan networks on the appropriate interfaces:

```yaml
# In compose.yaml
networks:
  vlan10:
    driver: macvlan
    driver_opts:
      parent: eth0.10           # VLAN 10 subinterface
    ipam:
      config:
        - subnet: 10.0.10.0/24
          gateway: 10.0.10.1
```

### Validation

Always validate your config before deploying:

```bash
# Offline validation (no network access, safe for CI)
canarium validate -c config.yaml

# Live preflight (probes clients, tests credentials)
canarium doctor -c config.yaml
```

`validate` catches: missing fields, invalid durations, duplicate names, dependency cycles, same-stage dependency violations, unknown client references, invalid expressions.

`doctor` adds: client connectivity, transport credential verification, SNMP MIB availability, DNS resolution, NUT connectivity.

---

## Troubleshooting

### Facts not appearing

- Check that the source is configured and the daemon can reach it (`canarium doctor`)
- For NUT: verify `upsc ups@hostname` works from the Canarium host
- For SNMP: verify `snmpget` works with your credentials
- Facts appear as `unknown` quality until the first successful poll

### Shutdown not triggering

- Check the mode: `disarmed` evaluates conditions but never executes
- Check the trigger condition in the web UI — it shows the current evaluation state
- Check dwell: the condition must be true for the full `for:` duration
- Check fact quality: `unknown` or `stale` facts never satisfy triggers

### WOL not working

- Verify the MAC address is correct
- Verify the broadcast address targets the right subnet
- Check that the target host has WOL enabled in BIOS and OS (`ethtool eth0 | grep Wake-on`)
- WOL is unacknowledged UDP — there's no error when it's silently dropped
- Managed switches may block WOL; check switch configuration
- After a hard power cut, some NICs lose WOL config until the OS re-enables it

### Client stuck in shutting_down

- The client's `shutdown_budget` hasn't expired yet — Canarium is waiting
- Once the budget expires, the client transitions to `down_unverified`
- A guard period (default 60s) must pass before wake is attempted
- Check if the shutdown command actually ran: look at the event log in the web UI
