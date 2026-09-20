package api

import (
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/engine"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

// testPassword is long enough to satisfy MinPasswordLength.
const testPassword = "a-sufficiently-long-password"

// newTestServer builds a Server backed by a real on-disk database in a
// temporary directory. webFS is left empty: routes() logs a warning and
// serves the API only, which is what these tests exercise.
func newTestServer(t *testing.T) (*Server, *state.DB) {
	t.Helper()

	db, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening state database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients: []config.ClientConfig{
			{Name: "nas", Transport: "ssh", Address: "10.0.0.1"},
		},
		Plans: []config.PlanConfig{{Name: "outage"}},
	}

	store := facts.NewStore()
	evaluator := conditions.NewEvaluator(store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	executor := engine.NewExecutor(cfg, store, evaluator, db, logger)

	var emptyFS embed.FS
	return NewServer(cfg, store, executor, db, emptyFS, logger), db
}

func do(t *testing.T, s *Server, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return out
}

// sessionCookie completes a login and returns the resulting session cookie.
func sessionCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()

	rec := do(t, s, "POST", "/api/auth/login", `{"password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: got %d, body %s", rec.Code, rec.Body.String())
	}

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login succeeded but set no session cookie")
	return nil
}

// protectedEndpoints are every route that must require authentication.
var protectedEndpoints = []struct {
	method string
	path   string
	body   string
}{
	{"GET", "/api/status", ""},
	{"GET", "/api/facts", ""},
	{"GET", "/api/clients", ""},
	{"GET", "/api/plans", ""},
	{"GET", "/api/sequence", ""},
	{"POST", "/api/mode", `{"mode":"armed"}`},
	{"POST", "/api/abort", ""},
}

// TestUnconfiguredServerRejectsProtectedEndpoints is the regression test for
// the most serious defect this code has shipped: requireAuth admitted every
// request when no password was configured, so a default install exposed the
// endpoint that arms the executor to anyone who could reach the port.
func TestUnconfiguredServerRejectsProtectedEndpoints(t *testing.T) {
	s, _ := newTestServer(t)

	for _, ep := range protectedEndpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			rec := do(t, s, ep.method, ep.path, ep.body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401 — endpoint is reachable without a password (body: %s)",
					rec.Code, rec.Body.String())
			}
			if got := decode(t, rec)["setup_required"]; got != true {
				t.Errorf("setup_required = %v, want true; the UI relies on this to show the setup screen", got)
			}
		})
	}
}

func TestConfiguredServerRejectsUnauthenticatedRequests(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d, body %s", rec.Code, rec.Body.String())
	}

	for _, ep := range protectedEndpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			rec := do(t, s, ep.method, ep.path, ep.body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d, want 401 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAuthStatusReportsSetupThenLogin(t *testing.T) {
	s, _ := newTestServer(t)

	// Fresh install: setup required, not authenticated.
	got := decode(t, do(t, s, "GET", "/api/auth/status", ""))
	if got["setup_required"] != true {
		t.Errorf("fresh install: setup_required = %v, want true", got["setup_required"])
	}
	if got["authenticated"] != false {
		t.Errorf("fresh install: authenticated = %v, want false", got["authenticated"])
	}

	// After setup: no longer setup, still not authenticated.
	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d, body %s", rec.Code, rec.Body.String())
	}
	got = decode(t, do(t, s, "GET", "/api/auth/status", ""))
	if got["setup_required"] != false {
		t.Errorf("after setup: setup_required = %v, want false", got["setup_required"])
	}
	if got["authenticated"] != false {
		t.Errorf("after setup: authenticated = %v, want false", got["authenticated"])
	}

	// After login: authenticated.
	cookie := sessionCookie(t, s)
	got = decode(t, do(t, s, "GET", "/api/auth/status", "", cookie))
	if got["authenticated"] != true {
		t.Errorf("after login: authenticated = %v, want true", got["authenticated"])
	}
}

func TestAuthenticatedRequestsSucceed(t *testing.T) {
	s, _ := newTestServer(t)

	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d, body %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, s)

	for _, ep := range protectedEndpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			rec := do(t, s, ep.method, ep.path, ep.body, cookie)
			// /api/abort legitimately 400s when no sequence is running; what
			// matters here is that it is not rejected as unauthenticated.
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("authenticated request rejected: %s", rec.Body.String())
			}
		})
	}
}

func TestSetupIsRefusedOncePasswordExists(t *testing.T) {
	s, _ := newTestServer(t)

	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("first setup: got %d", rec.Code)
	}

	rec := do(t, s, "POST", "/api/auth/setup", `{"password":"attacker-chosen-password"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("second setup: got %d, want 403 — an attacker could otherwise reset the admin password", rec.Code)
	}

	// The original password must still work.
	sessionCookie(t, s)
}

func TestSetupRejectsShortPassword(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", "/api/auth/setup", `{"password":"short"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}

	// Setup must remain available after a rejected attempt.
	if got := decode(t, do(t, s, "GET", "/api/auth/status", ""))["setup_required"]; got != true {
		t.Errorf("setup_required = %v after a rejected password, want true", got)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s, _ := newTestServer(t)

	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d", rec.Code)
	}

	rec := do(t, s, "POST", "/api/auth/login", `{"password":"wrong-password-entirely"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Error("a failed login issued a session cookie")
		}
	}
}

func TestForgedSessionCookieIsRejected(t *testing.T) {
	s, _ := newTestServer(t)

	if rec := do(t, s, "POST", "/api/auth/setup", `{"password":"`+testPassword+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("setup: got %d", rec.Code)
	}

	forged := &http.Cookie{Name: sessionCookieName, Value: strings.Repeat("0", sessionTokenBytes*2)}
	rec := do(t, s, "GET", "/api/status", "", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for a forged session token", rec.Code)
	}
}

func TestHealthIsPublic(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "GET", "/api/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; the container healthcheck depends on this", rec.Code)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"bearer scheme", "Bearer abc123", "abc123"},
		{"bare token", "abc123", "abc123"},
		{"bearer with padding", "  Bearer   abc123  ", "abc123"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			if got := bearerToken(req); got != tt.want {
				t.Errorf("bearerToken() = %q, want %q", got, tt.want)
			}
		})
	}
}
