# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's
[security advisory form](https://github.com/nkcx/canarium/security/advisories/new)
rather than opening a public issue.

Include what you can: affected version, configuration, and what an attacker
would gain. A proof of concept helps but is not required.

## What Canarium is

Canarium holds credentials that can shut down an entire fleet: SSH keys,
hypervisor API tokens, storage array keys, firewall credentials. Treat the
host running it as you would any other management-plane system. A compromise
of Canarium is a compromise of everything it can reach.

## Security posture

**Authentication fails closed.** Until an admin password is set, every
authenticated endpoint refuses requests. There is no unauthenticated mode.

**Passwords** are stored as bcrypt hashes (cost 12), pre-hashed so that
bcrypt's 72-byte input limit does not truncate a long passphrase. Login is
rate limited per source address, and every rejection is delayed.

**Sessions** are 256-bit tokens from `crypto/rand`. Only a SHA-256 digest is
stored, so a database leak does not yield usable cookies. They expire after 24
hours and are pruned hourly.

**API tokens** carry a scope. A `read` token can observe the daemon but
cannot arm it, abort a sequence, or force a stage.

**TLS** is verified for every outbound HTTPS and WSS connection. Appliances
with self-signed certificates require an explicit `tls_ca_cert` to pin, or an
explicit `tls_insecure_skip_verify` opt-out that is logged on every use and
reported by `canarium doctor`.

**SSH host keys** are verified. The default policy learns a key on first
contact and refuses a subsequent change.

**Canarium terminates no TLS of its own.** Run it behind a reverse proxy and
set `canarium.auth.trust_proxy_headers` so session cookies are marked
`Secure`. The shipped compose file binds the API to localhost.

**Credentials are redacted** from logs, the journal, the event stream and
webhook payloads.

**The state database** (admin password hash, session tokens, API token
digests, learned SSH host keys) is created mode 0600 in a 0700 directory. If
the data directory already exists with looser permissions, Canarium warns at
startup rather than changing it.

**The container runs as a non-root user** (uid 65532) with no capabilities.

## Known limitations

- Authentication is a single local admin. There is no federated auth, no
  multi-user support, and no audit trail of who did what beyond the source
  address in the logs.
- There is no rate limiting on endpoints other than login.
- The `exec` transport runs configured commands through `sh -c` by design.
  Anyone who can write the configuration file can already specify what gets
  shut down and how, so the config file is a trust boundary equivalent to
  shell access as the daemon's user.
