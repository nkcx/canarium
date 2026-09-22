# Upgrading

This release fixes a number of defects whose corrections change behaviour.
Most configurations need no changes; the ones that do are listed here, with
what each looks like when you hit it.

Run `canarium validate -c your-config.yaml` first. It now catches most of
these before the daemon starts.

---

## Breaking: TLS certificates are verified

**Affects:** clients using the `proxmox`, `truenas` or `opnsense` transports.

These transports hardcoded `InsecureSkipVerify: true`, so API tokens for your
hypervisor, storage array and firewall were sent over connections any on-path
attacker could impersonate. TrueNAS additionally fell back from `wss://` to
plaintext `ws://` on any TLS error, which — given TrueNAS ships a self-signed
certificate — meant the common case was sending an API key in cleartext.

**Symptom:** `x509: certificate signed by unknown authority` when the
transport runs.

**Fix,** preferred — pin the appliance's certificate:

```bash
openssl s_client -connect nas.lan:443 -showcerts </dev/null 2>/dev/null \
  | openssl x509 > /etc/canarium/nas-ca.pem
```

```yaml
clients:
  - name: brick
    transport: truenas
    config:
      tls_ca_cert: /etc/canarium/nas-ca.pem
```

**Fix,** if the appliance's certificate names a hostname you reach by IP:

```yaml
    config:
      tls_ca_cert: /etc/canarium/nas-ca.pem
      tls_server_name: nas.lan
```

**Fix,** accepting the risk knowingly:

```yaml
    config:
      tls_insecure_skip_verify: true
```

This logs a warning naming the client on every connection, and
`canarium doctor` reports it.

---

## Breaking: SSH host keys are verified

**Affects:** clients using the `ssh` transport.

The transport used `InsecureIgnoreHostKey()`, so anyone able to intercept the
connection could impersonate a host: the shutdown command went to the
attacker, the real machine stayed up, and Canarium recorded success.

The default policy is now `accept-new` — a host's key is learned on first
contact and pinned thereafter — so **no change is needed for most
deployments**. Learning happens the first time Canarium probes or shuts down
each host, during normal operation rather than during an outage.

**Symptom, if a key changes:** `ssh: host key mismatch`. That is either a
reinstall or an attack. If you reinstalled the host, remove its line from
`<data_dir>/known_hosts`.

**To require keys up front instead:**

```yaml
transports:
  ssh:
    host_key_policy: strict
    known_hosts: /etc/canarium/known_hosts
```

```bash
ssh-keyscan -H nas.lan hypervisor.lan >> /etc/canarium/known_hosts
```

**To restore the old behaviour:** `host_key_policy: insecure`.

---

## Breaking: the SSH key path must be configured

**Affects:** clients using the `ssh` transport.

`ssh.Config` existed but was always constructed empty, so the key path always
defaulted to `$HOME/.ssh/id_ed25519` — a path that does not exist in the
container image.

**Symptom:** `ssh: no key_path configured`.

**Fix:**

```yaml
transports:
  ssh:
    key_path: /etc/canarium/id_ed25519
    user: root                      # optional, defaults to root
    command: "shutdown -h now"      # optional
```

Per-client overrides go in that client's `config:` block.

---

## Breaking: unknown config keys are rejected

The YAML decoder discarded keys it did not recognise, so
`point_of_no_retrun: true` produced a stage with no point of no return and no
complaint, and `wake_policy: retain-state` (hyphen, not underscore) produced a
client that got woken when it should not have been.

**Symptom:** `field <name> not found in type config.StageConfig` at startup.

**Fix:** correct the spelling. The error names the offending key and line.

---

## Breaking: unset environment variables are an error

An unresolved `${VAR}` was left in the document as a literal, so a credential
silently became the nine-character string `"${TOKEN}"` and every request using
it failed to authenticate with no indication why.

**Symptom:** `config references environment variables that are not set: ...`

**Fix:** set them. If you use the shipped `compose.yaml`, it now passes `.env`
through with `env_file:` — that was missing, which is why `${VAR}` substitution
never worked under Docker at all.

`canarium validate` treats them as warnings and substitutes a placeholder, so
structural checks still run in CI without production secrets. `--strict-env`
opts into the daemon's behaviour.

---

## Breaking: timeline durations are seconds or duration strings

**Affects:** simulation timelines.

`at` and `duration` were typed `time.Duration`, which JSON decodes only from
integer *nanoseconds*. A timeline written `"at": 300` — the obvious way to say
five minutes — meant 300 nanoseconds, so every event fired on the first tick.

**Fix:** write `"at": "5m"`, or a number of seconds. See
`examples/outage-timeline.json`.

---

## Behaviour change: stale facts no longer satisfy conditions

A stale fact keeps its last observed value, and the evaluator accepted it. If
the UPS source died moments after reporting "on battery, 25% charge", that
reading stayed true forever and would fire a shutdown from data that no longer
described reality.

Only `good` quality counts now. A fact is stale after two poll intervals
without an update.

**What you might notice:** a plan that used to trigger no longer does, because
its source is not actually reporting. `canarium doctor` will tell you which.
This is the intended outcome — the README's claim that losing contact with a
sensor never triggers a shutdown is now true.

---

## Behaviour change: the point of no return moves later

PONR was crossed when the stage loop *reached* a stage, before its entry
condition was evaluated — so abort was disabled for the entire wait, which
defaults to an hour. `examples/basic.yaml` puts `point_of_no_return` on the
first stage, which meant abort was disabled the instant the plan triggered and
mains power returning would not stop the shutdown.

It is now crossed when the stage dispatches. Sequences are abortable for
longer, which is what the setting was always meant to express.

---

## Behaviour change: explicit zero durations are honoured

`stagger: 0s` meant "use the 45-second default" because the parsed value was
compared against zero. It now means what it says: wake everything at once. The
same applied to budgets, guard periods, wait timeouts and probe intervals.

**Check** any config that sets one of these to zero deliberately.

---

## Behaviour change: skipped stages are announced

`wait_policy: skip` (the default) abandons a stage whose entry condition has
not held within `wait_timeout` — its clients are never shut down. That was
always the documented behaviour, but it happened silently.

It now emits a `stage_skipped` event naming the clients left running, records
it in the journal, and `canarium validate` reports which stages could do this.

---

## New: things that were configured but never did anything

These fields existed in the schema and were read by no code. They now work,
which may change behaviour if you had set them:

| Field | What it now does |
|---|---|
| `canarium.journal_retain` | Finished sequences are pruned past the window, as are exported journal files. Was never read; the database grew forever. |
| `canarium.config_readonly` | `POST /api/mode` is refused, so the file stays authoritative. |
| `canarium.auth.password_hash` | Pins the admin password; first-run setup is disabled. |
| `clients[].feeds`, `feed_policy` | Drive the derived `client.<name>.threatened` fact. |
| `clients[].comms_loss_assumes` | Resolves what an unreadable feed means. Defaults to `safe`. |
| `sources[].type: gpio` | Actually starts a GPIO source. Was silently ignored. |
| NUT `username` / `password` | Sent to the server. Were dropped, so authenticated NUT servers rejected Canarium and `post_shutdown` could never work. |

---

## New: the admin password can be changed

`POST /api/auth/password`, and an Admin Password section in Settings. It
requires the current password and signs out every session, including the one
that made the change — so you will land back on the login screen. That is
the point: a rotation exists because the old password may be compromised,
and sessions established under it should not survive.

The section is hidden when `canarium.auth.password_hash` pins the password
in the config file, and the endpoint refuses, since the file is canonical.

---

## New: forwarded headers are only trusted from a trusted peer

`trust_proxy_headers: true` used to accept `X-Forwarded-For` from any
connection, so any client could claim any source address — which set the
`Secure` cookie flag and, more usefully to an attacker, sidestepped the
per-address login rate limit by picking a fresh address for each attempt.

Headers are now honoured only when the connection itself comes from a
trusted peer. The default is loopback plus the RFC 1918 and ULA ranges,
which covers the usual same-host or same-network proxy without any
configuration. Narrow it with `canarium.auth.trusted_proxies`:

```yaml
canarium:
  auth:
    trust_proxy_headers: true
    trusted_proxies: [10.0.0.8/32]
```

If your proxy is on a public address, you must list it — otherwise its
requests are treated as direct and every client appears to come from the
proxy's address.

---

## New: minimum password length is 12

Was 8. Existing passwords keep working; the limit applies at setup.

Existing password hashes are upgraded from the old unsalted SHA-256 to bcrypt
automatically on the next successful login. Nothing is required of you.

---

## New: sessions are invalidated

Sessions moved from the generic key-value table to their own, and old rows are
discarded by the migration. **Everyone will need to log in again once.**

---

## New: every sequence writes an audit journal

SPEC §8.6 specified it; nothing implemented it. Each finished sequence now
writes `journal/<sequence-id>.jsonl` in the data directory — one
self-describing JSON object per line, in time order:

- a `sequence` header: plan, final state, whether the point of no return was
  crossed, and the wake snapshot of addresses and MACs
- a `stage` record per stage, with per-client outcomes
- an `intent` record per dispatched command, with what the transport returned

The database is still the authority. The journal is the portable artefact:
something to archive, diff between outages, or hand to a colleague. It is
mode 0600 in a 0700 directory, because it names every client and its address.

Files expire on the same `canarium.journal_retain` schedule as the database
rows. If you need a longer audit window, copy them off the device.

No configuration is required. If your data directory is a bind mount, the
`journal/` subdirectory appears inside it after the first sequence.

---

## New: validation is stricter

`canarium validate` now fails on things it used to accept:

| What | Why |
|---|---|
| Negative durations | `-5m` parsed fine and produced a budget that expired instantly. |
| A plan that shuts down Canarium's own host | Canarium has to survive to run the wake plan (SPEC §8.3). Matched by hostname, configured addresses and local interface addresses. |
| `post_shutdown` naming an unconfigured UPS, or an action other than `upscmd` | Previously accepted and silently did nothing at the end of an outage. |

And warns on:

| What | Why |
|---|---|
| `shutdown_budget` under 90s | A cleanly powered-off host on a local network cannot usually be confirmed down for about a minute, so short budgets finish as `down_unverified`. |
| `snmp-poe` clients with no `switch_address` | `address` is being used for both the switch and the powered device. Still works, but the probe is checking the wrong machine. |

If validation now fails on a config that used to pass, it is reporting
something that was already not doing what it looked like it was doing.

---

## Changed: what `:latest` points at

`ghcr.io/nkcx/canarium:latest` used to move with every commit to `main`, so
a `docker compose pull` could bring in unreleased work. It now follows
releases.

If you were relying on the old behaviour, `:main` does what `:latest` used
to. If you were not, you were running unreleased commits without meaning
to, and this fixes it.

Version tags are the better answer either way:

| Tag | Moves |
|---|---|
| `0.1.1` | Never. Pin this for anything you care about. |
| `0.1` | With each patch release in the 0.1 series. |
| `latest` | With each release. |
| `main` | With every commit to `main`. Unreleased; expect breakage. |

The shipped `compose.yaml` now pins a version rather than floating. This is
the thing that shuts your fleet down; an unattended pull should not be able
to change its behaviour.

Images also carry an SBOM and signed build provenance now:

```bash
gh attestation verify oci://ghcr.io/nkcx/canarium:0.1.1 --repo nkcx/canarium
```

---

## Changed: disarmed mode now evaluates triggers

Disarmed was documented as "sources poll, conditions evaluate, nothing
executes", but the policy loop returned before evaluating anything unless
the executor was armed or in dry-run. So the mode whose purpose is letting
you verify your plans gave no signal at all when a real outage would have
fired one.

Triggers are now evaluated and timed while disarmed. Nothing executes. When
a trigger holds, a `would_trigger` event is emitted and logged, once per
episode.

**Arming restarts trigger timers.** Because disarmed now times triggers,
arming during an outage whose trigger had already held for its full `for:`
would otherwise start the sequence immediately. Instead the trigger has to
hold again, observed while armed. The cost is one dwell period; the arm
confirmation tells you when a trigger is already holding.

Webhook consumers will see the new `would_trigger` event type.

---

## Changed: API responses

| Endpoint | Change |
|---|---|
| `GET /api/plans` | Now describes each plan in full: trigger, abort, stages (with resolved clients and any references that match nothing), post-shutdown, and wake gate, each condition with its live evaluation. `name` and `stages` (the count) are unchanged. |
| `GET /api/status` | Adds `version` and `config_warnings`, the validation warnings from startup. Previously these were only logged. `clients` no longer includes clients removed from the configuration. |
| `GET /api/clients`, `GET /api/plans` | Return `[]` rather than `null` when empty. |
| `GET /api/facts` | `updated_at` is `null` for a fact that has never been reported, rather than `0001-01-01T00:00:00Z`. |
