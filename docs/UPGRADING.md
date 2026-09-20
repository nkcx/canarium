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
| `canarium.journal_retain` | Finished sequences are pruned past the window. Was never read; the database grew forever. |
| `canarium.config_readonly` | `POST /api/mode` is refused, so the file stays authoritative. |
| `canarium.auth.password_hash` | Pins the admin password; first-run setup is disabled. |
| `clients[].feeds`, `feed_policy` | Drive the derived `client.<name>.threatened` fact. |
| `clients[].comms_loss_assumes` | Resolves what an unreadable feed means. Defaults to `safe`. |
| `sources[].type: gpio` | Actually starts a GPIO source. Was silently ignored. |
| NUT `username` / `password` | Sent to the server. Were dropped, so authenticated NUT servers rejected Canarium and `post_shutdown` could never work. |

---

## New: minimum password length is 12

Was 8. Existing passwords keep working; the limit applies at setup.

Existing password hashes are upgraded from the old unsalted SHA-256 to bcrypt
automatically on the next successful login. Nothing is required of you.

---

## New: sessions are invalidated

Sessions moved from the generic key-value table to their own, and old rows are
discarded by the migration. **Everyone will need to log in again once.**
