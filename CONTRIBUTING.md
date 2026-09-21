# Contributing

## Getting set up

```bash
git clone https://github.com/nkcx/canarium.git
cd canarium
make build      # builds the frontend, then the binary
```

A clean clone builds without the frontend too — `go build ./...`,
`go vet ./...` and `go test ./...` all work immediately. A binary built that
way serves the API but tells you the UI is missing.

## Before you push

```bash
make check
```

That runs everything CI does: `gofmt`, `go vet`, `golangci-lint`,
`go test -race`, and `go mod tidy -diff`. CI additionally runs `govulncheck`
and `npm audit`.

## What this code is for

Canarium shuts down other people's infrastructure, usually while a battery is
draining and nobody is watching. That shapes what counts as a good change:

**Failure has a direction.** When something is unknown, prefer the outcome
that does not act. A stale fact must not satisfy a condition. An unreachable
host is not a host that is off. A probe that fails tells you about the
network, not about the machine.

**Silence is a bug.** A stage that is skipped, a client that could not be
shut down, a source that stopped reporting — each of these must produce an
event and a log line saying what happened and what it means. Several of the
worst defects in this codebase's history were things that worked exactly as
written and said nothing about it.

**Config mistakes belong at validate time.** `canarium validate` is offline
and deterministic; if a typo can only be discovered during an outage, that is
a gap in validation.

## Tests

Every behavioural change needs a test that fails without it. For a bug fix,
the useful discipline is to write the test first, confirm it reproduces the
defect against the unfixed code, and say so in the commit message — several
commits here record exactly that, and it is the difference between a test
that documents a fix and one that proves it.

Prefer tests that describe the consequence rather than the mechanism:
`TestStaleFactNeverSatisfiesNumericCondition` over `TestQualityCheck`.

The engine has a harness (`internal/engine/harness_test.go`) providing a fake
transport, a real database and compressed timings, so a sequence test runs in
milliseconds rather than minutes.

## Commits

One logical change per commit, with a message that explains what was wrong
and why the fix is the right one. The existing history is the style guide.
