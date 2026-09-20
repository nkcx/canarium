package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeClock lets the limiter tests advance time without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(maxFailures int, window, lockout time.Duration) (*failureLimiter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := newFailureLimiter(maxFailures, window, lockout)
	l.now = clock.Now
	return l, clock
}

func TestLimiterAllowsUntilThreshold(t *testing.T) {
	l, _ := newTestLimiter(3, time.Minute, time.Minute)

	for i := 0; i < 2; i++ {
		if allowed, _ := l.Allow("1.2.3.4"); !allowed {
			t.Fatalf("attempt %d refused before the threshold", i+1)
		}
		l.RecordFailure("1.2.3.4")
	}

	if allowed, _ := l.Allow("1.2.3.4"); !allowed {
		t.Error("third attempt refused; the threshold is 3 failures")
	}
	l.RecordFailure("1.2.3.4")

	allowed, retryAfter := l.Allow("1.2.3.4")
	if allowed {
		t.Error("fourth attempt allowed after reaching the failure threshold")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want positive", retryAfter)
	}
}

func TestLimiterIsPerKey(t *testing.T) {
	l, _ := newTestLimiter(2, time.Minute, time.Minute)

	for i := 0; i < 5; i++ {
		l.RecordFailure("attacker")
	}

	if allowed, _ := l.Allow("attacker"); allowed {
		t.Error("the offending key was not locked out")
	}
	if allowed, _ := l.Allow("innocent-bystander"); !allowed {
		t.Error("an unrelated key was locked out; one attacker would deny service to the operator")
	}
}

func TestLimiterUnlocksAfterLockout(t *testing.T) {
	l, clock := newTestLimiter(2, time.Minute, 10*time.Minute)

	l.RecordFailure("1.2.3.4")
	l.RecordFailure("1.2.3.4")
	if allowed, _ := l.Allow("1.2.3.4"); allowed {
		t.Fatal("not locked out after reaching the threshold")
	}

	clock.Advance(9 * time.Minute)
	if allowed, _ := l.Allow("1.2.3.4"); allowed {
		t.Error("unlocked before the lockout elapsed")
	}

	clock.Advance(2 * time.Minute)
	if allowed, _ := l.Allow("1.2.3.4"); !allowed {
		t.Error("still locked out after the lockout elapsed; the operator would be shut out permanently")
	}
}

func TestLimiterWindowRollsOver(t *testing.T) {
	l, clock := newTestLimiter(3, 5*time.Minute, time.Hour)

	l.RecordFailure("1.2.3.4")
	l.RecordFailure("1.2.3.4")

	// Well past the failure window: the old failures should not count.
	clock.Advance(6 * time.Minute)
	l.RecordFailure("1.2.3.4")

	if allowed, _ := l.Allow("1.2.3.4"); !allowed {
		t.Error("stale failures outside the window still counted toward the threshold")
	}
}

func TestLimiterResetClearsHistory(t *testing.T) {
	l, _ := newTestLimiter(2, time.Minute, time.Hour)

	l.RecordFailure("1.2.3.4")
	l.RecordFailure("1.2.3.4")
	if allowed, _ := l.Allow("1.2.3.4"); allowed {
		t.Fatal("not locked out")
	}

	l.Reset("1.2.3.4")

	if allowed, _ := l.Allow("1.2.3.4"); !allowed {
		t.Error("Reset did not clear the lockout; a successful login must restore access")
	}
}

func TestLimiterGCBoundsMemory(t *testing.T) {
	l, clock := newTestLimiter(2, time.Minute, time.Minute)

	for i := 0; i < 1000; i++ {
		l.RecordFailure(string(rune('a'+i%26)) + string(rune(i)))
	}
	if len(l.entries) == 0 {
		t.Fatal("no entries recorded")
	}

	// Push everything past both the window and the lockout, then trigger a
	// sweep.
	clock.Advance(limiterGCInterval + time.Hour)
	l.Allow("trigger-gc")

	if len(l.entries) != 0 {
		t.Errorf("%d stale entries survived collection; memory is unbounded "+
			"against an attacker cycling source addresses", len(l.entries))
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		trustProxy bool
		want       string
	}{
		{
			name:       "direct connection",
			remoteAddr: "203.0.113.5:54321",
			want:       "203.0.113.5",
		},
		{
			name:       "ipv6 direct",
			remoteAddr: "[fd00::1]:54321",
			want:       "fd00::1",
		},
		{
			name:       "forwarded header ignored when proxy untrusted",
			remoteAddr: "203.0.113.5:54321",
			headers:    map[string]string{"X-Forwarded-For": "1.1.1.1"},
			trustProxy: false,
			want:       "203.0.113.5",
		},
		{
			name:       "forwarded header honoured when proxy trusted",
			remoteAddr: "10.0.0.2:54321",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9"},
			trustProxy: true,
			want:       "203.0.113.9",
		},
		{
			name:       "forwarded chain takes the left-most entry",
			remoteAddr: "10.0.0.2:54321",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.1, 10.0.0.2"},
			trustProxy: true,
			want:       "203.0.113.9",
		},
		{
			name:       "x-real-ip fallback",
			remoteAddr: "10.0.0.2:54321",
			headers:    map[string]string{"X-Real-IP": "203.0.113.7"},
			trustProxy: true,
			want:       "203.0.113.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/auth/login", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if got := clientIP(req, tt.trustProxy); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLoginIsThrottled exercises the limiter through the real handler.
func TestLoginIsThrottled(t *testing.T) {
	s, _ := newTestServer(t)

	// Keep the test fast: the handler sleeps on every rejection.
	s.loginLimiter = newFailureLimiter(3, time.Minute, time.Hour)

	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d", rec.Code)
	}

	attempt := func() int {
		req := httptest.NewRequest("POST", "/api/auth/login", stringReader(`{"password":"wrong"}`))
		req.RemoteAddr = "203.0.113.5:1234"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 3; i++ {
		if code := attempt(); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, code)
		}
	}

	if code := attempt(); code != http.StatusTooManyRequests {
		t.Errorf("attempt past the threshold: got %d, want 429", code)
	}

	// The correct password from a different source must still work — a
	// lockout must never become a denial of service for the real operator.
	req := httptest.NewRequest("POST", "/api/auth/login", stringReader(`{"password":"`+testPassword+`"}`))
	req.RemoteAddr = "198.51.100.10:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("login from an unaffected source: got %d, want 200", rec.Code)
	}
}

// TestSuccessfulLoginClearsFailures verifies a mistyped password does not
// leave a lingering penalty.
func TestSuccessfulLoginClearsFailures(t *testing.T) {
	s, _ := newTestServer(t)
	s.loginLimiter = newFailureLimiter(3, time.Minute, time.Hour)

	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d", rec.Code)
	}

	send := func(password string) int {
		req := httptest.NewRequest("POST", "/api/auth/login", stringReader(`{"password":"`+password+`"}`))
		req.RemoteAddr = "203.0.113.5:1234"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec.Code
	}

	send("wrong")
	send("wrong")

	if code := send(testPassword); code != http.StatusOK {
		t.Fatalf("correct password after two failures: got %d, want 200", code)
	}

	// The counter is clear, so a fresh run of failures is allowed again.
	for i := 0; i < 3; i++ {
		if code := send("wrong"); code != http.StatusUnauthorized {
			t.Fatalf("post-reset attempt %d: got %d, want 401", i+1, code)
		}
	}
}

func stringReader(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}
