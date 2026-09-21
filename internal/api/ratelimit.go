package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Login throttling parameters.
//
// Canarium has a single admin password and no account-lockout story, so the
// goal is to make online guessing impractical without ever locking the real
// operator out permanently. After maxLoginFailures failures from one source
// within failureWindow, that source is refused for lockoutDuration; a
// successful login clears the counter immediately.
const (
	maxLoginFailures = 5
	failureWindow    = 15 * time.Minute
	lockoutDuration  = 15 * time.Minute

	// limiterGCInterval is how often stale entries are swept, bounding the
	// limiter's memory against an attacker cycling source addresses.
	limiterGCInterval = 10 * time.Minute

	// maxSetupAttempts bounds unauthenticated first-run setup requests from
	// one source. It is lower than the login threshold: setup succeeds at
	// most once in a daemon's lifetime, so anything beyond a couple of
	// attempts is either a mistake or an attack.
	maxSetupAttempts = 3
)

// loginFailureDelay is applied to every rejected login. It bounds the rate of
// a distributed attack that spreads guesses across many source addresses to
// stay under the per-source threshold, at negligible cost to a human who
// mistyped their password.
//
// A var only so tests can zero it; see TestMain.
var loginFailureDelay = 500 * time.Millisecond

// failureLimiter throttles repeated failures per key, typically a client IP.
//
// It is intentionally in-memory: the state is small, resets on restart
// (which requires local access anyway), and keeping it out of SQLite avoids
// a write on every failed login attempt, which would itself be a cheap
// denial-of-service vector against flash storage.
type failureLimiter struct {
	mu      sync.Mutex
	entries map[string]*failureEntry

	// now is injectable so tests need not sleep.
	now func() time.Time

	maxFailures int
	window      time.Duration
	lockout     time.Duration

	lastGC time.Time
}

type failureEntry struct {
	failures    int
	firstFailed time.Time
	lockedUntil time.Time
}

func newFailureLimiter(maxFailures int, window, lockout time.Duration) *failureLimiter {
	return &failureLimiter{
		entries:     make(map[string]*failureEntry),
		now:         time.Now,
		maxFailures: maxFailures,
		window:      window,
		lockout:     lockout,
	}
}

// Allow reports whether key may attempt a login, and if not, how long until
// it may.
func (l *failureLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.gcLocked(now)

	e, ok := l.entries[key]
	if !ok {
		return true, 0
	}

	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}

	// The lockout has elapsed, or the failure window has rolled over.
	if !e.lockedUntil.IsZero() || now.Sub(e.firstFailed) > l.window {
		delete(l.entries, key)
	}

	return true, 0
}

// RecordFailure registers a failed attempt and locks the key out once the
// threshold is reached.
func (l *failureLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	e, ok := l.entries[key]
	if !ok || now.Sub(e.firstFailed) > l.window {
		l.entries[key] = &failureEntry{failures: 1, firstFailed: now}
		return
	}

	e.failures++
	if e.failures >= l.maxFailures {
		e.lockedUntil = now.Add(l.lockout)
	}
}

// Reset clears a key's failure history after a successful authentication.
func (l *failureLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// gcLocked drops entries that can no longer affect a decision. The caller
// must hold l.mu.
func (l *failureLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < limiterGCInterval {
		return
	}
	l.lastGC = now

	for key, e := range l.entries {
		expired := now.After(e.lockedUntil) && now.Sub(e.firstFailed) > l.window
		if expired {
			delete(l.entries, key)
		}
	}
}

// clientIP identifies the source of a request for throttling purposes.
//
// X-Forwarded-For is consulted only when the request itself arrived from a
// trusted proxy — see Server.trustedProxy. Trusting the header on the
// operator's say-so alone was not enough: with trust_proxy_headers set, any
// client that could reach the port directly could both evade throttling by
// varying the header and lock the real administrator out by forging theirs.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The left-most entry is the original client; the proxy appends
			// each subsequent hop.
			if first, _, found := strings.Cut(xff, ","); found {
				xff = first
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			return real
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr is not always host:port (unix sockets, tests).
		return r.RemoteAddr
	}
	return host
}
