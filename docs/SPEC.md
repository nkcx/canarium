# Canarium — Product Specification

**Version:** 0.3
**Status:** Design — pre-implementation

---

## 1. Summary

Canarium monitors environmental conditions — power loss, temperature, flooding — and orchestrates the orderly transition of your infrastructure from up to down. When those conditions clear, it brings everything back up again, in the right order, verified and safe.

It is not a monitoring tool or an early warning system. It is the thing that actually does something when the environment changes: shuts down your servers in the right order before the battery runs out, then wakes them back up when power returns and the UPS has recharged.

It runs on minimal hardware — a Raspberry Pi or any Linux SBC. It does not perform arbitrary automations; every action is part of a plan with stages, dependencies, and safety constraints.

It is not a UPS monitor. NUT already does that job well. Canarium is the policy and execution layer that NUT deliberately does not provide.

### 1.1 Problem statement

Existing tooling splits along an unhelpful line:

- **NUT** is an excellent sensing and driver layer with a crude policy layer. `upsmon` broadcasts a shutdown signal to all clients at once; `upssched` offers timers but no sequencing, no dependency awareness, and no concept of bringing anything back up.
- **Commercial suites** (PowerChute, Eaton IPP/IPM) have real sequencing but are vendor-locked to their own UPS hardware, assume homogeneous fleets, and implement ordering through per-agent timer arithmetic that is difficult to reason about and impossible to validate.
- **Homelab scripts** solve one person's topology and don't generalize.

Nobody covers the full cycle: ordered shutdown with dependency awareness, *and* conditional staged restart.

### 1.2 Design principles

1. **Sensing is not policy.** Canarium never drives hardware directly. NUT, SNMP, and other sources are read-only inputs. UPS outlet control goes through NUT's existing write path.
2. **The config file is the product.** A user should be able to read one file and know exactly what will happen during an outage.
3. **Fail safe, not fail fast.** Absence of information is never evidence of an outage.
4. **Untested orchestration is broken orchestration.** Simulation and dry-run are core features, not afterthoughts.
5. **Modules are peers.** Nothing in the core knows what a battery is. Canarium is lifecycle-oriented — it understands shutdown and wake as lifecycle verbs — but has no built-in vocabulary for any specific kind of infrastructure.
6. **Easy for the trivial case, configurable for the complex case.** A single-UPS, five-machine homelab should take ten minutes to configure. A dual-UPS, multi-subnet, mixed-vendor rack should be fully expressible.
7. **Embedded-first.** Canarium runs on a Raspberry Pi that nobody touches for a year. Memory, CPU, and storage constraints drive design decisions.

### 1.3 Non-goals

- Replacing NUT drivers or speaking to UPS hardware directly.
- General-purpose home automation. Canarium executes plans, not arbitrary actions.
- Cluster-aware orchestration (Proxmox HA, Ceph quorum) in v1. Explicitly deferred.
- Managing its own host's power state. Canarium is designed to outlive everything it controls.

### 1.4 Target hardware

Hard requirement: Raspberry Pi 3 (1 GB RAM, arm64/armv7). Stretch goal: Raspberry Pi 2 (1 GB RAM, armv7). Also supports Pi 4, Pi 5, and any Linux SBC (ODROID, BeagleBone, PINE64, etc.).

The practical floor: any Linux system with 512 MB RAM, armhf or arm64 or amd64.

Memory ceiling for the Canarium process (core + all loaded modules + web UI server): 128 MB. This is a design constraint, not just a target.

---

## 2. Concepts

| Term | Meaning |
|---|---|
| **Fact** | A single typed, named, timestamped observation. `nut.myups.battery.charge = 47` |
| **Source** | A module that produces facts. NUT, SNMP, HTTP poll, GPIO, temperature. |
| **Fact context** | The current set of all facts, with freshness metadata. The only input to policy. |
| **Client** | A thing Canarium can control. A server, a switch port, a PDU outlet. |
| **Client state** | The observed lifecycle state of a client: `unknown`, `up`, `shutting_down`, `down`, `down_unverified`, `waking`. |
| **Transport** | A module that performs actions against a client. SSH, Proxmox API, WOL, SNMP. |
| **Capability** | An action a transport can perform for a client: `shutdown`, `wake`, `probe`. |
| **Tag** | A label on a client, used for grouping. `tags: [compute, rack-a]` |
| **Condition** | A boolean expression over the fact context, optionally with a dwell requirement. |
| **Stage** | An ordered group of clients that transition together, with an entry condition and a wait timeout. |
| **Plan** | An ordered list of stages with a trigger, abort condition, and wake gate. |
| **Sequence** | A running execution of a plan. Config is snapshot at sequence start. |
| **PONR** | Point of no return. Must be explicitly marked. |
| **Feed** | A mapping from a client's power supply to a UPS source. |

---

## 3. Architecture

```
┌─────────────┐
│   Sources   │  NUT · SNMP · HTTP · GPIO · temperature · manual
└──────┬──────┘
       │ facts (typed, timestamped, quality-tagged)
       ▼
┌─────────────┐
│Fact context │  current values + freshness + declared schema
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Policy    │  conditions → triggers → plan selection → feed threat
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Planner   │  resolve stages, validate dependencies, compute budgets
└──────┬──────┘
       │
       ▼
┌─────────────┐
│  Executor   │  run stages, track client state, confirm, retry, persist
└──────┬──────┘
       │ actions
       ▼
┌─────────────┐
│ Transports  │  SSH · Proxmox · TrueNAS · OPNsense · WOL · SNMP · NUT · REST
└─────────────┘
```

### 3.1 Implementation

Go. Single static binary, cross-compiled for `linux/amd64`, `linux/arm64`, `linux/arm` (armv7). No runtime dependency on the target host.

Web UI: embedded SPA built with Svelte and Tailwind CSS, compiled into the Go binary via `embed.FS`. The Go backend serves the static bundle and exposes a REST + WebSocket API. No Node runtime required on the target.

### 3.2 Deployment

Single process. Container image and native binary both first-class.

Container images published to `ghcr.io/nkcx/canarium`, multi-arch (amd64, arm64, armv7).

Reference `compose.yaml` shipped in the repository:

```yaml
services:
  canarium:
    image: ghcr.io/nkcx/canarium:latest
    ports:
      - "8420:8420"
    volumes:
      - ./canarium.yaml:/etc/canarium/config.yaml
      - canarium-data:/var/lib/canarium
    devices:
      - /dev/gpiochip0:/dev/gpiochip0  # optional: GPIO source
    restart: unless-stopped

volumes:
  canarium-data:
```

GPIO passthrough uses `/dev/gpiochip0` (the modern `libgpiod` chardev interface, not deprecated sysfs).

### 3.3 Storage architecture

**Hot state** — active sequence state, dwell timers, client states — lives in a SQLite database (`/var/lib/canarium/state.db`) using WAL mode. SQLite provides atomic state transitions, crash recovery, and bounded storage without the complexity of a separate database server. Typical footprint: 2-4 MB RAM.

On embedded systems with SD cards, the database directory can optionally be pointed at tmpfs (`/run/canarium/`) with periodic checkpoints to persistent storage on stage transitions, PONR crossings, and sequence completions. This trades crash-recovery granularity for flash longevity.

**Audit journal** — JSONL export of state transitions and command records, rotated per sequence. Read-only after sequence completion. Used for replay, export, and the simulation engine. Generated from the SQLite state, not the primary persistence mechanism.

**Config** — YAML file, owned by the daemon. See §9.1.

---

## 4. Fact model

### 4.1 Declaration

Every source module declares its facts at load time. The core has no built-in vocabulary.

```yaml
# Declared by the NUT source module, not written by users
facts:
  - name: battery.charge
    type: percent
    range: [0, 100]
    description: "State of charge"
  - name: battery.runtime
    type: duration
    description: "Estimated runtime remaining"
  - name: status
    type: set
    values: [OL, OB, LB, RB, CHRG, DISCHRG, ALARM, OVER, TRIM, BOOST, BYPASS, OFF]
    description: "UPS status flags (multiple may be active simultaneously)"
```

Facts are namespaced by source instance: `nut.rack_ups.battery.charge`.

**Naming:** fact path segments use underscores, not hyphens, to ensure they are valid identifiers in the expression language without escaping. Source instance names follow the same rule. The user-facing display name can contain hyphens; the config key cannot.

Declarations drive config validation at load, UI form generation, and type checking in expressions.

### 4.2 Types

| Type | Go type | Notes |
|---|---|---|
| `number` | `float64` | General numeric value |
| `percent` | `float64` | Constrained 0-100; UI renders as gauge |
| `duration` | `time.Duration` | Seconds in config; parsed from NUT's seconds value |
| `enum` | `string` | One of declared values |
| `set` | `[]string` | Multiple simultaneous values (e.g., NUT status flags) |
| `bool` | `bool` | Binary state |
| `string` | `string` | Freeform text |

All numeric types use `float64`. No implicit unit conversion — sources declare units in their schema, and the UI displays them, but the expression engine operates on raw values. Temperature is always Celsius in the fact model; conversion is a display concern.

### 4.3 Freshness and quality

Every fact carries `value`, `updated_at`, and `quality` ∈ {`good`, `stale`, `unknown`}.

- `good` — updated within the source's declared poll interval × 2.
- `stale` — older than that but previously known.
- `unknown` — never received, or the source reported an error.

### 4.4 Three-valued logic for unavailable facts

Facts with `unknown` or `stale` quality are not coerced to `false`. Instead, the expression engine uses three-valued logic:

- A structured condition referencing an `unknown` fact evaluates to `unavailable`, not `true` or `false`.
- `unavailable` propagates through `and`/`or`/`not` using SQL-style semantics: `true AND unavailable = unavailable`, `false AND unavailable = false`, `NOT unavailable = unavailable`.
- **Shutdown triggers require `true`.** An `unavailable` result does not satisfy a shutdown trigger. This is the core fail-safe: losing sight of the UPS does not shut anything down.
- **Wake gates require `true`.** An `unavailable` result does not satisfy a wake gate. Losing sight of the UPS does not spuriously wake anything.
- **Abort conditions require `true`.** An `unavailable` abort does not cancel a shutdown sequence.

This prevents the dangerous case where `NOT(battery_low)` becomes `true` when battery data is unavailable.

**In-sequence condition latching:** once a sequence is triggered and running, conditions that were `true` at trigger time and are now `unavailable` due to transient source disconnection retain their last-known `true` value for up to a configurable latch period (default: source poll interval × 5). This prevents a transient NUT disconnection from stranding a mid-shutdown sequence. The latch is cleared when the fact returns to `good` quality or the latch period expires, whichever comes first.

Prolonged `stale`/`unknown` raises an alarm and can *optionally* be configured as a trigger (default: off), making "assume the worst on comms loss" an explicit opt-in.

---

## 5. Condition model

Hybrid: structured predicates for common cases, expression escape hatch for everything else. The UI builds forms from source-declared fact schemas; expressions are available for compound logic the form can't express.

### 5.1 Structured conditions

```yaml
when:
  condition: or
  conditions:
    - condition: numeric
      fact: nut.rack_ups.battery.charge
      below: 50
    - condition: numeric
      fact: nut.rack_ups.battery.runtime
      below: 10m
    - condition: state
      fact: nut.rack_ups.status
      contains: LB
```

Supported condition types: `numeric` (`above`/`below`/`equals`), `state` (`is`/`is_not`/`in`/`contains` for set-type facts), `and`, `or`, `not`, `template`, `schedule`, `client_state`.

### 5.2 Dwell is universal

**Every condition type accepts `for:`.**

```yaml
- condition: numeric
  fact: nut.rack_ups.battery.charge
  above: 60
  for: 5m
```

This is a deliberate departure from Home Assistant, where `for:` works on `state` conditions but not `numeric_state` conditions. That gap is one of the most persistently reported friction points in HA. For Canarium the numeric-plus-dwell case is not an edge case — it *is* the wake gate. "Battery above 60% for five minutes" is the single most important expression in the entire product, and it must be a first-class structured condition.

Dwell tracking uses monotonic time while the daemon is running. Across a daemon restart, persisted dwell state records wall-clock timestamps; on resume, the daemon credits only the elapsed time it can prove (time between the last recorded `true` evaluation and the restart), and treats the restart gap conservatively (not counted toward dwell). On systems without a reliable RTC (common on Raspberry Pis), Canarium waits for NTP sync before resuming dwell credit.

Each condition instance has a stable identity derived from its position in the plan and a content hash. Reordering conditions in the config resets dwell for the moved conditions; editing a condition's parameters resets its dwell. This is deliberate — a changed condition should re-prove itself.

### 5.3 Template escape hatch

For logic the structured forms can't express:

```yaml
- condition: template
  value: >
    fact("nut.rack_ups.battery.charge") > 60 &&
    (fact("nut.rack_ups.status").contains("OL") || fact("nut.rack_ups.status").contains("CHRG")) &&
    fact("temp.rack.celsius") < 35
```

**Expression language: `expr-lang/expr`.** HA's template conditions work by rendering Jinja to a string and testing truthiness — that's why they fail confusingly and can't be statically checked. `expr` is a Go-native expression language with familiar infix syntax, static type checking against a declared environment, guaranteed termination, and no side effects. Because fact schemas are declared, Canarium can type-check every expression at config load.

**Fact access in expressions:** facts are accessed via the `fact()` function, which returns the typed value or `nil` if the fact is unavailable. The expression environment also exposes `quality()` returning `"good"`, `"stale"`, or `"unknown"`, and `age()` returning seconds since last update. A `nil` result from `fact()` in any comparison evaluates to `unavailable` under three-valued logic (§4.4).

Shorthand form permitted in condition lists:

```yaml
when:
  - 'fact("nut.rack_ups.battery.charge") < 50'
  - condition: state
    fact: nut.rack_ups.status
    contains: OB
```

### 5.4 Validation

Two commands:

- **`canarium validate`** — offline, deterministic. Type-checks every expression, resolves every fact reference against loaded module declarations, verifies every client reference, validates dependency/ordering constraints, detects dependency cycles, checks stage/budget consistency, and reports computed PONR. Exits non-zero on any failure. Suitable for CI against a config repo. No network access, no live device contact.

- **`canarium doctor`** — live preflight. Runs everything `validate` does, plus: probes every client for connectivity, tests transport credentials, verifies SNMP MIB availability, detects TrueNAS API version, resolves DNS and ARP, and confirms NUT connectivity. Reports results per-client. Requires a running daemon or `--standalone` with source/transport config.

---

## 6. Clients and transports

### 6.1 Client definition

```yaml
clients:
  - name: steel
    description: "Compute node"
    transport: proxmox
    address: 10.0.10.11
    mac: "aa:bb:cc:dd:ee:01"          # optional: auto-discovered if not specified
    credentials: ${PROXMOX_STEEL_TOKEN}
    tags: [compute, rack-a]
    feeds: [rack_ups]                  # power supply → UPS mapping
    shutdown_budget: 3m
    wake_policy: power_state           # "power_state" (default) or "retain_state"
    probe:
      method: tcp
      port: 22
    wake:
      transport: wol
      broadcast: 10.0.10.255
    depends_on: [brick]
    after: [vanadium]
```

#### Feeds and power threat

`feeds` maps a client's power supplies to UPS sources. A single-PSU machine has one feed; a dual-PSU machine has two:

```yaml
clients:
  - name: storage-server
    feeds: [rack_ups_a, rack_ups_b]
    feed_policy: all    # "all" = shut down only when ALL feeds are threatened
                        # "any" = shut down when ANY feed is threatened (default)
```

If `feeds` is omitted, the client is associated with all declared UPS sources (the trivial single-UPS case requires no feeds configuration at all).

`feed_policy` defaults to `any` (non-redundant). Dual-PSU machines set `all` — only shut down when both UPSes are failing.

**Derived fact: `client.<name>.threatened`.** The policy engine computes a boolean `threatened` fact per client based on its feeds and feed policy. A client is `threatened` when its feed policy is satisfied — for `any`, when any associated UPS is on battery or in alarm; for `all`, when all are. Stage conditions can reference this derived fact:

```yaml
stages:
  - name: compute
    when:
      condition: state
      fact: client.steel.threatened
      is: true
      for: 30s
```

This connects feed policy to plan evaluation explicitly. A stage triggered by UPS A going on battery will not shut down a dual-fed client protected by UPS B (when using `feed_policy: all`), because that client's `threatened` fact is `false`.

For `unknown` feeds: if a feed's UPS status is `unknown`, the `threatened` derivation treats it as not-threatened (fail-safe — don't shut down because you lost comms). This is configurable per client via `comms_loss_assumes: threatened` for operators who prefer the conservative approach.

#### Tags

Clients carry `tags`, which are used in stage definitions:

```yaml
stages:
  - name: compute
    clients: [tag:compute, extra-host]  # tag references and individual names
```

A client can have multiple tags. `tag:X` in a client list expands to all clients with that tag at validation time. A client referenced both by tag and by name is included once (deduplicated).

#### Dependency and ordering

- **`depends_on`**: hard dependency. "Steel depends on brick" means brick must be up before steel can wake. If brick fails to wake, steel is not attempted. On shutdown, steel goes down before brick. Validation rejects placing a client and its `depends_on` target in the same stage. Validation rejects cycles. Hard dependencies are enforced regardless of how stages are specified — explicit stages that violate a `depends_on` constraint are a validation error.
- **`after` / `before`**: soft ordering preference. When stages are not specified, these determine client ordering within auto-generated stages. When stages are specified, `after`/`before` serve as validation — a warning is raised if the stage order contradicts the declared preference. Failure does not propagate through `after`/`before`.

When neither stages nor dependencies are specified, clients are unordered (all transition together). When `depends_on` is specified without explicit stages, the planner derives stage assignment from the dependency graph via topological sort. When explicit stages are specified, they take precedence — `depends_on` validates (and errors on violation), `after`/`before` validate (and warn on violation).

#### Wake policy

- **`power_state`** (default): wake this client whenever the wake plan runs and the client is found to be `down`, regardless of whether it was on before the outage. This is the right default — most infrastructure should be running, and the simplest mental model is "shut everything down, bring everything back up."
- **`retain_state`**: only wake this client if it was observed `up` before the sequence started. Pre-sequence state is recorded when the sequence triggers. Use this for machines that are intentionally powered off (dev boxes, seasonal workloads).

### 6.2 Client state machine

Every client has an observed state tracked by the executor:

```
unknown ──probe succeeds──▶ up
unknown ──probe fails──▶ down
up ──shutdown issued──▶ shutting_down
shutting_down ──probe confirms down──▶ down
shutting_down ──shutdown_budget expires, no probe confirmation──▶ down_unverified
down ──wake issued──▶ waking
down_unverified ──guard period expires──▶ down (eligible for wake)
down_unverified ──probe confirms down──▶ down
down_unverified ──probe confirms up──▶ up
waking ──probe confirms up──▶ up
waking ──retries exhausted──▶ failed
```

States:

- **`unknown`**: initial state. Client has not been probed since Canarium started.
- **`up`**: probe confirms the client is running.
- **`shutting_down`**: shutdown command issued, awaiting confirmation.
- **`down`**: confirmed not running. Eligible for wake.
- **`down_unverified`**: `shutdown_budget` expired but probe could not confirm the client is down (e.g., probe target is behind a switch that's already off). The client is assumed down for stage-progress purposes, but a guard period (default: 60s, configurable) must elapse before wake is attempted. During the guard period, the executor continues probing — if the client responds `up`, it transitions to `up`; if it confirms `down`, it transitions to `down` immediately. This prevents the race condition of sending a wake command to a machine still mid-shutdown.
- **`waking`**: wake command issued, awaiting probe confirmation.
- **`failed`**: wake retries exhausted. Surfaced in the UI for manual intervention. A failed client blocks its `depends_on` dependents from wake.

Stage progress is determined by client state, not by wake eligibility. A client reaching `down_unverified` allows the stage to advance. Wake eligibility is a separate concern evaluated later.

### 6.3 Auto-discovery and resolution

Users specify what they know: hostname, IP, MAC, or any combination. Canarium resolves the rest:

- **At config load:** resolve hostnames via DNS, discover MACs via ARP for clients that are reachable. Warn if resolution fails. MAC discovery via ARP is best-effort — it only works for hosts on the same subnet and only when they're up. For reliable WOL, static MAC configuration is recommended.
- **Continuously:** background refresh of the resolution cache on a configurable interval. The cached state is always warm.
- **At sequence start:** snapshot all resolved details into the state database. This snapshot is what the wake plan uses — name resolution is forbidden at wake time because DNS may be one of the things that's down.

### 6.4 Transport interface

A transport module implements any subset of:

```go
type Transport interface {
    Capabilities() []Capability
    Execute(ctx context.Context, client Client, action Action) (Result, error)
    Probe(ctx context.Context, client Client) (ClientState, error)
}
```

`Action` is a typed enum: `Shutdown`, `Wake`, `PoeOff`, `PoeOn`, `OutletOff`, `OutletOn`. Each action carries metadata: whether it is idempotent (safe to retry on crash recovery) and its expected completion timeout.

Transports that lack a capability are rejected at validation time if a plan requires it. A client using a shutdown-only transport with no `wake:` block is valid — it won't be woken, and the UI says so.

### 6.5 Module taxonomy

#### Core modules (in-tree, always shipped)

| Module | Kind | Capabilities | Notes |
|---|---|---|---|
| `nut` | source + transport | Source: UPS facts (status as flag set). Transport: `upscmd` for outlet control, `shutdown.return`. | NUT is the primary UPS interface. |
| `snmp` | source + transport | Source: any SNMP-pollable fact. Transport: PoE control (RFC 3621). | See §6.7. |
| `ssh` | transport | shutdown, probe | Key-based only. No password auth. Configurable command; defaults to `shutdown -h now`. |
| `wol` | transport | wake | Magic packet. Configurable broadcast address, repeat count, and directed unicast mode for cross-subnet wake. |
| `exec` | transport | any | Run a local command. Last-resort escape hatch. |
| `rest` | transport | shutdown, wake, probe | Configurable HTTP client: templated URL, method, headers, payload, response parsing. |
| `webhook` | notification | — | Fire-and-forget HTTP POST on events. Not a transport. See §8.5. |
| `gpio` | source | — | GPIO pins via `libgpiod`. Temperature sensors, power-present detection, physical arm/disarm switch. |

#### Shipped with v1 (in-tree, may migrate to plugin system in v2+)

| Module | Kind | Notes |
|---|---|---|
| `proxmox` | transport | `POST /api2/json/nodes/{node}/status` with `command=shutdown`. API token scoped to `Sys.PowerMgmt`. |
| `truenas` | transport | JSON-RPC 2.0 over WebSocket. See §6.6. |
| `opnsense` | transport | `POST /api/core/system/halt` for shutdown. Interface control in v1.1+. |

#### v1.1+

| Module | Kind |
|---|---|
| `winrm` | transport |
| `ipmi` | transport |
| `redfish` | transport |
| `http-poll` | source |
| `temperature` | source (1-Wire, I²C) |

### 6.6 TrueNAS module — design note

The REST API was deprecated in TrueNAS 25.04 and removed in TrueNAS 26; the module uses the versioned JSON-RPC 2.0 over WebSocket API. TrueNAS 26 introduces SCRAM-SHA-512 mutual authentication for API keys.

The module maintains a persistent WebSocket connection with reconnection handling. Probe results come from an independent path (TCP or ICMP), never from WebSocket liveness — a live WebSocket does not prove the host is healthy, only that the API is responding.

Auth is version-dependent: the module negotiates API version on connect and selects legacy API-key login or SCRAM mutual auth accordingly. Version detection happens at `canarium doctor` time (not offline `validate`).

### 6.7 SNMP PoE — design note

Per-port PoE is controllable through RFC 3621 POWER-ETHERNET-MIB (`pethPsePortAdminEnable` at OID `1.3.6.1.2.1.105.1.1.1.3`). SNMP SET to `1` enables, `2` disables.

```yaml
clients:
  - name: poe-cameras
    transport: snmp-poe
    address: 10.0.1.2
    tags: [nonessential]
    shutdown_budget: 0s
    config:
      snmp_version: 3
      snmp_user: canarium
      snmp_auth_pass: ${SWITCH_AUTH_PASS}
      snmp_priv_pass: ${SWITCH_PRIV_PASS}
      ports:
        - { group: 1, port: 5 }
        - { group: 1, port: 6 }
```

Caveats:
- Some vendors don't implement the MIB. `canarium doctor` probes for the MIB and fails loudly.
- SNMP SET requires write access (SNMPv3 with authPriv recommended).
- Validation rejects configs where a PoE client's ports include the switch port carrying Canarium's own network path, where derivable.

---

## 7. Plans and sequences

### 7.1 Plan structure

```yaml
plans:
  - name: outage
    trigger:
      condition: state
      fact: nut.rack_ups.status
      contains: OB
      for: 30s

    abort:
      condition: state
      fact: nut.rack_ups.status
      contains: OL
      for: 60s

    shutdown:
      post_shutdown:
        action: upscmd
        command: shutdown.return
        delay: 120

      stages:
        - name: shed-load
          when: "true"
          clients: [poe-cameras, tag:nonessential]
          budget: 10s
          wait_timeout: 0s

        - name: compute
          when:
            condition: or
            conditions:
              - {condition: numeric, fact: nut.rack_ups.battery.charge, below: 50}
              - {condition: numeric, fact: nut.rack_ups.battery.runtime, below: 10m}
          clients: [steel, vanadium, tag:compute]
          budget: 5m
          wait_timeout: 30m
          point_of_no_return: true

        - name: wifi
          when: {condition: numeric, fact: nut.rack_ups.battery.charge, below: 35}
          clients: [tag:wifi]
          budget: 10s

        - name: infrastructure
          when: {condition: numeric, fact: nut.rack_ups.battery.charge, below: 25}
          clients: [niobium, brick]
          budget: 3m

    wake:
      gate:
        condition: and
        conditions:
          - {condition: state, fact: nut.rack_ups.status, contains: OL}
          - {condition: numeric, fact: nut.rack_ups.battery.charge, above: 60, for: 5m}
      stagger: 45s
      order: reverse
      verify:
        probe_interval: 30s
        boot_deadline: 5m
        retries: 3
```

### 7.2 Stage execution — shutdown

For each stage, in order:

1. **Wait** until the stage's `when` condition evaluates to `true`. While waiting, the plan's `abort` condition is live (if pre-PONR). `when` conditions are **edge-triggered and latched**: once the condition becomes `true`, the stage begins even if the condition subsequently becomes `false` (e.g., battery charge fluctuating). The `wait_timeout` limits how long a stage will wait for its condition; if exceeded, the behavior is configurable: `skip` (default), `escalate` (notification + skip), or `hold` (wait indefinitely, requires manual intervention via UI). Default `wait_timeout`: 1 hour.
2. **Issue** the shutdown action to all clients in the stage concurrently.
3. **Confirm.** Wait for each client to reach `down` or `down_unverified`. Per-client `shutdown_budget` is the timeout. If a stage-level `budget` is also specified and is shorter than any client's `shutdown_budget`, `canarium validate` warns — the stage budget takes precedence and may cut short a client's shutdown.
4. **Log** the outcome per client. Record the command dispatch and result in the state database (intent journaling — see §8.4).
5. **Advance** to the next stage.

Step 3 is a deliberate improvement over commercial products. PowerChute and Eaton wait the full configured delay whether the task finished early or ran long. Confirming completion reclaims runtime on every stage.

### 7.3 Post-shutdown UPS action

After all shutdown stages complete, the plan can issue a final UPS command via the NUT transport. The canonical use case: `upscmd <ups> shutdown.return` with a delay, which commands the UPS to cut outlet power after a configurable number of seconds, then restore it when mains returns.

This enables wake for hosts that have no WOL, no IPMI, and rely on BIOS "restore on AC power" behavior. The sequence:
1. Canarium shuts down all clients.
2. Canarium issues `shutdown.return` with a 120-second delay.
3. The UPS cuts outlet power 120 seconds later (Canarium's host should be on a separate, always-on outlet or a dedicated mini-UPS).
4. When mains returns, the UPS restores outlet power.
5. BIOS restore-on-AC powers on the hosts.
6. Canarium's wake plan detects them coming up via probe, or issues WOL to any that didn't auto-start.

`post_shutdown` is optional. When specified, `canarium validate` verifies that Canarium's own host is not powered by an outlet that will be cut.

### 7.4 Point of no return

Once a stage marked `point_of_no_return: true` begins, the `abort` condition is ignored for the remainder of the sequence. Mains restoration no longer cancels; the sequence completes and control passes to the wake plan.

**PONR must be explicitly marked.** There is no implicit default. `canarium validate` reports which stage is marked PONR (or that no stage is, in which case the entire sequence is abortable). This prevents surprising behavior changes when dependencies are added or stages are reordered.

Plans with no PONR are fully abortable at any point. This is valid — not all plans need a point of no return.

### 7.5 Abort and interruption

**Pre-PONR abort** — when the abort condition fires before PONR is crossed:

1. **Stop** issuing new shutdown commands. Dispatch within a stage is bounded — the executor processes clients in small batches, checking the abort condition between batches.
2. **Wait** for any client in `shutting_down` to reach `down` or `down_unverified` (confirmed by probe or `shutdown_budget` expiry). Never send a wake command to a machine that might still be shutting down.
3. **Transition** to the wake plan. The wake gate is evaluated — if conditions are met (mains present, charge sufficient), wake begins.
4. **Wake from the top.** The wake plan runs its full stage order. For each client, the executor probes first:
   - `up`: skip (already running).
   - `down` or `down_unverified` (past guard period): check `wake_policy`. If `power_state`, issue wake. If `retain_state`, check pre-sequence snapshot — only wake if the client was `up` before the sequence started.
   - `down_unverified` (within guard period): wait for guard period, then proceed as above.
   - `depends_on` target in `failed` state: do not attempt this client.

**Post-PONR:** Abort condition is ignored. Sequence completes shutdown fully, then the wake plan evaluates its gate normally.

### 7.6 Wake plan

Wake is not the inverse function of shutdown; it is a separate plan with different failure modes.

**Pre-sequence state.** When a sequence triggers, the executor records the current observed state of every client in the plan. This snapshot determines wake eligibility for `retain_state` clients.

**Gating.** The wake gate is evaluated against the fact context. The canonical gate is mains-present AND charge-above-threshold-for-a-dwell-period. The dwell prevents a flapping outage from cycling the rack.

**Ordering.** `order: reverse` reverses the shutdown stage order. Alternatively, an explicit wake stage list can be specified. `depends_on` is enforced: dependencies are woken before their dependents.

**Staggering.** Clients are woken with a configurable interval. Simultaneous wake produces coordinated inrush from spinning disks and PSU capacitors at precisely the moment the UPS is mid-recharge. Default 45s.

**Verification.** WOL is unacknowledged UDP. Wake verification has three configurable timers:
- `probe_interval`: how often to probe after issuing wake (default 30s).
- `boot_deadline`: maximum time to wait for a client to come up after wake (default 5m).
- `retries`: how many times to re-send the wake command if the client isn't up by `boot_deadline` (default 3).

Probe confirms `up` when the probe target responds. For TCP probes, an open port does not guarantee the host is fully ready — it means the OS is up and the probed service is listening.

**Partial wake.** A client that fails to wake after all retries transitions to `failed`. A notification fires. The wake plan continues to subsequent stages — a failed client never blocks stage progression. However, clients with a `depends_on` pointing to the `failed` client are not attempted. Clients with only `after` ordering relative to the failed client are attempted.

After the sequence completes, failed clients are surfaced in the UI for manual retry.

**Name resolution.** All addresses and MACs are resolved from the snapshot taken at sequence start. DNS is likely still down when the wake plan runs.

### 7.7 Concurrent sequences

One active sequence per plan. Multiple plans can execute concurrently if they control different clients. A client can only be in one active sequence at a time — enforced by the executor via locks in the state database.

If plan A and plan B both trigger and share clients, the second plan queues until the first completes, then re-evaluates its trigger (it may no longer need to fire). Queued plans that remain untriggered for a configurable period (default: 24h) are expired with a notification.

### 7.8 Boot / sync phase

On daemon startup (cold boot or restart), Canarium:

1. Opens the state database. If a sequence was active, it resumes (see §8.4).
2. If no active sequence: initiates a background probe sweep of all declared clients, populating initial client states.
3. Evaluates fact sources. If an active outage is detected (e.g., UPS on battery), the trigger fires normally — the shutdown plan runs.
4. If mains are present and facts satisfy the wake gate, and clients are found `down`, Canarium enters the wake plan (respecting `wake_policy` per client).

This handles the common case of total power loss: Canarium's host boots on mains return, probes the environment, and initiates wake for infrastructure that's still down.

---

## 8. Safety model

Canarium's failure mode is not "doesn't work" — it's "works wrongly, once a year, unattended." The safety model gets disproportionate weight.

### 8.1 Modes

- **`disarmed`** — sources poll, conditions evaluate, everything is logged, nothing executes. Default after install.
- **`dry-run`** — sequences run end to end with all timing, but transports log intent instead of acting.
- **`armed`** — live.

Mode is set explicitly and displayed prominently. Canarium never self-arms.

### 8.2 Fail-safe defaults

- Unknown or stale facts produce `unavailable` in condition evaluation, which never satisfies triggers, gates, or aborts (§4.4).
- Source communication loss raises an alarm; treating it as an outage is explicit opt-in (default off).
- In-sequence condition latching prevents transient source disconnection from stranding a running sequence (§4.4).

### 8.3 Self-preservation

Canarium must outlive everything it controls. Validation rejects:

- A config where Canarium's own host appears as a client.
- A PoE client whose ports include the switch port carrying Canarium's network path, where derivable.
- A `post_shutdown` action that would cut power to Canarium's own host.
- A shutdown plan that would remove Canarium's route to a client scheduled for a later stage. This is a graph reachability check over declared network topology — a validation error when topology is specified, a warning when it isn't.

### 8.4 Persistence and crash recovery

**State database:** SQLite WAL at `/var/lib/canarium/state.db`. All durable state lives here:

- Active sequence state, current stage, and per-client state.
- Per-condition dwell timers.
- Pre-sequence client state snapshot (for `retain_state` wake policy).
- Resolved addresses and MACs (the wake snapshot).
- PONR flag.
- Client ownership locks (for concurrent sequence safety).

**Intent journaling:** before dispatching any action, the executor writes an intent record to the database: `{client, action, timestamp, status: "dispatching"}`. After the transport returns, the record is updated to `{status: "dispatched", result}`. On crash recovery:

- An intent with `status: "dispatching"` (crash before dispatch): the action was never sent. The executor probes the client to determine current state before deciding whether to retry.
- An intent with `status: "dispatched"` but no stage-completion record: the action was sent but the stage didn't finish. The executor probes to reconcile.
- Idempotent actions (SSH shutdown, WOL) are safe to retry. Non-idempotent actions (exec, outlet toggle) are not retried automatically — the executor probes and logs, leaving manual intervention for ambiguous cases.

Transports declare whether each action is idempotent in their capability manifest.

**Fsync:** the database is opened with `PRAGMA synchronous = NORMAL` (WAL mode default), which fsyncs at checkpoint boundaries. For critical writes (intent records, PONR flag), the executor forces a WAL checkpoint. This balances durability with write volume on flash storage.

On restart mid-sequence, Canarium resumes from the last completed stage. Within a partially-completed stage, it probes all clients to reconcile actual state with recorded state before proceeding.

### 8.5 Notifications

Webhook POST with structured JSON on: trigger, stage start/complete, PONR crossing, abort, wake gate satisfied, per-client wake success/failure, client state transitions, and any validation or source alarm.

Native integrations (ntfy, Gotify, email) may follow; the webhook is the contract.

### 8.6 Audit journal

JSONL export of state transitions and command records, generated from the SQLite state database. One file per sequence, rotated on completion. Records state changes and command events — not every polling-cycle condition evaluation (that would produce excessive writes on SD cards).

Completed journals are retained for a configurable period (default 30 days), then rotated. Used for replay and export via the simulation engine (§9.3).

---

## 9. Configuration and interface

### 9.1 File is canonical

Canarium's configuration lives in a YAML file that the daemon owns. Everything — clients, plans, module config, expressions — is expressed there.

The web UI edits configuration **through the daemon**, which writes the file. This preserves a single source of truth while allowing a full editing UI:

- The file remains human-readable, diffable, and reviewable. A config that shuts down your infrastructure belongs in version control.
- `canarium validate <file>` runs standalone in CI with no daemon.
- The daemon watches the file and reloads on external change. If the UI has unsaved edits when an external write lands, the UI surfaces the conflict rather than silently overwriting.
- **Config reload during active sequence:** deferred until the sequence completes. The active sequence runs against the snapshot taken at trigger time. The user can manually abort through the UI to force a reload.
- The daemon retains the last known-good configuration. If a file write (from UI or external) produces invalid config, the daemon rejects it, logs the validation errors, and continues with the previous config. The UI surfaces the errors.
- Secrets are referenced by environment variable or secret-file path, never inlined, so the config file is safe to commit.
- A `--config.readonly` flag disables UI config writes for GitOps deployments where config is managed externally (Ansible, Git). The UI still functions for monitoring and manual actions but the config editor is disabled.

### 9.2 Web UI

Embedded SPA (Svelte + Tailwind CSS), served from the Go binary. WebSocket for real-time updates.

**Dashboard.** Current fact values with quality indicators, mode (disarmed/dry-run/armed), active sequence with stage progress and per-client state, recent event log.

**Client editor.** Form-driven, with per-transport fields rendered from the transport's declared schema. Test buttons for connectivity, credentials, probe, and wake — per client. Auto-discovered details (MAC, resolved IP) displayed alongside user-specified values.

**Plan editor.** Stage list with drag ordering; condition builder rendered from declared fact schemas; template escape hatch as a text field with live type-checking feedback. Visual dependency graph showing client ordering. Computed PONR prominently displayed.

**Simulation.** Inject arbitrary fact values and watch the plan evaluate without executing. A `mock` source module lets users script a fact timeline (`battery.charge: 100 → 45 over 4m`) and replay it. Simulation and the live executor share the same evaluation engine — simulation tests actual semantics.

**Test shutdown.** Execute the full sequence in dry-run mode with real timing.

### 9.3 API

- **`/api/health`** — unauthenticated. Returns `{"status": "ok"}` and mode. Suitable for external health checks.
- **`/api/status`** — authenticated. Fact values, client states, sequence status, UPS info. For dashboards and Home Assistant integration.
- **Write operations** (arm/disarm, trigger, abort, config changes) — authenticated.
- **WebSocket** — authenticated. Real-time event streaming.

Default bind: `0.0.0.0:8420`. TLS is expected to be provided by a reverse proxy. Canarium does not terminate TLS directly — this keeps the binary simple and avoids certificate management on an embedded device.

### 9.4 Authentication

**v1:** Single local admin account. Password hashed with bcrypt, stored in the state database (not the config file). Session cookie with configurable expiry (default: 24h) and secure/httponly/samesite flags. Login throttling (5 attempts per minute).

Long-lived API token for programmatic access. Token is generated via the UI or CLI, hashed with SHA-256 in the state database, displayed once at creation. API tokens are scoped to read-only or read-write.

**v2+:** Federated auth support (OIDC, LDAP). The auth interface is designed for this from v1, but no external identity provider dependency at runtime — Canarium must work when DNS and SSO are down.

### 9.5 Simulation and testing

- A `mock` source module scripts fact timelines and replays them.
- Recorded real outages can be exported as timelines from the audit journal and replayed.
- Shipped module test suites run against protocol simulators, not live hardware.
- `canarium simulate --plan outage --timeline flapping.yaml` runs headless and exits non-zero if the plan behaves unexpectedly, making plan behavior testable in CI.
- The simulation engine uses the same planner and executor as live operation — simulation tests real semantics, not an approximation.

---

## 10. Module SDK

Modules are compiled in for v1. Out-of-tree plugins are deferred to v2+ — a plugin ABI is a large commitment.

A module provides:

1. A **manifest** — name, version, kind (source, transport, or both), config schema, declared facts or capabilities. Capabilities include idempotency and timeout metadata per action.
2. An **implementation** of the `Source` and/or `Transport` interface.
3. A **simulator** for its protocol, used by its own tests and available to users for dry-run and simulation.

The manifest drives config validation and UI form generation with no UI code per module. Adding a transport does not require touching the frontend.

---

## 11. Home Assistant integration

**v2/v3:** A custom HA integration that exposes Canarium state as HA sensors:

- Sequence status (idle, shutting_down, waking, etc.)
- Per-client state (up, down, shutting_down, waking, failed)
- UPS facts forwarded as sensor entities
- Mode (disarmed, dry-run, armed)
- Feed threat status per client

This is a read-only integration: HA reads Canarium's authenticated API. Canarium does not depend on HA. HA's REST sensor platform can approximate this against Canarium's API without a custom integration, but a native integration provides auto-discovery and proper entity management.

---

## 12. Roadmap

**v1.0**
Core engine, fact model with three-valued logic, hybrid conditions with universal dwell, plans with explicit PONR and abort, client state machine with `down_unverified`, wake with dependency enforcement and verification and per-client wake policy, intent journaling with SQLite, full web UI with simulation, NUT source/transport, GPIO source, and the `ssh` / `wol` / `snmp` / `exec` / `rest` / `webhook` / `proxmox` / `truenas` / `opnsense` transports. Multi-UPS feed mapping with `any`/`all` policy and derived `threatened` facts. Local auth. `validate` and `doctor` commands.

**v1.1**
`winrm`, `ipmi`, `redfish` transports. SNMP UPS source (RFC 1628). HTTP poll and temperature (1-Wire, I²C) sources. OPNsense interface control.

**v1.2**
Multi-UPS AND/OR redundancy logic beyond simple `any`/`all`. Scheduled and manual triggers. Journal export and replay tooling.

**v2**
Out-of-tree plugin system (inspired by OPNsense's plugin model). Federated auth (OIDC, LDAP). Home Assistant custom integration. Shipped v1 transports (Proxmox, TrueNAS, OPNsense) migrate to plugins.

**v3**
Cluster-aware shutdown (Proxmox HA, Ceph quorum). Distributed Canarium instances with leader election for sites with multiple independent power domains.

---

## 13. Resolved design decisions

1. **Multi-UPS in v1?** Yes. `feeds`, `feed_policy`, and derived `client.<name>.threatened` facts in the v1 schema.
2. **Does Canarium command the UPS?** Yes, through NUT's write path (`upscmd`). The NUT module is both a source and a transport. `post_shutdown` enables `shutdown.return` for restore-on-AC hosts.
3. **Authentication model?** Single local admin for v1. Auth interface designed for federated providers in v2+.
4. **`depends_on` vs stages?** Both coexist. Explicit stages take precedence but must not violate `depends_on` (validation error). `after`/`before` are soft (validation warning). Unassigned clients are auto-placed by dependency graph.
5. **Wake policy?** Per-client: `power_state` (default, wake always) or `retain_state` (wake only if previously up).
6. **State persistence?** SQLite WAL for hot state, JSONL audit journal for export/replay.
7. **`down_unverified`?** Distinct state with guard period before wake eligibility. Prevents shutdown/wake race.
8. **PONR?** Must be explicitly marked. No implicit default.
9. **Condition availability?** Three-valued logic. `unavailable` never satisfies triggers, gates, or aborts.
10. **`validate` vs `doctor`?** `validate` is offline/deterministic. `doctor` adds live connectivity checks.
