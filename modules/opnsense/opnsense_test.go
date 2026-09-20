package opnsense

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/nkcx/canarium/internal/engine"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %s: %v", rawURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing port: %v", err)
	}
	return u.Hostname(), port
}

func TestParseCredentials(t *testing.T) {
	tests := []struct {
		input       string
		key, secret string
	}{
		{"key:secret", "key", "secret"},
		{"key:", "key", ""},
		{"key", "key", ""},
		{"", "", ""},
		{"key:secret:with:colons", "key", "secret:with:colons"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			key, secret := parseCredentials(tt.input)
			if key != tt.key || secret != tt.secret {
				t.Errorf("parseCredentials(%q) = (%q, %q), want (%q, %q)",
					tt.input, key, secret, tt.key, tt.secret)
			}
		})
	}
}

// TestTLSVerificationIsOnByDefault is the regression test for
// InsecureSkipVerify being hardcoded, which sent the API key and secret as
// basic auth over an unverified connection.
func TestTLSVerificationIsOnByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "fw",
		Address:         host,
		Credentials:     "key:secret",
		TransportConfig: map[string]any{"port": port},
	}, engine.ActionShutdown)

	if err == nil && result != nil && result.Success {
		t.Error("connected to a server with an untrusted certificate")
	}
}

func TestHaltUsesBasicAuth(t *testing.T) {
	var (
		gotPath   string
		gotUser   string
		gotSecret string
		gotOK     bool
	)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotSecret, gotOK = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "fw",
		Address:     host,
		Credentials: "api-key:api-secret",
		TransportConfig: map[string]any{
			"port":                     port,
			"tls_insecure_skip_verify": true,
		},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("Success = false: %s", result.Message)
	}

	if gotPath != "/api/core/system/halt" {
		t.Errorf("path = %q, want /api/core/system/halt", gotPath)
	}
	if !gotOK {
		t.Fatal("no basic auth credentials were sent")
	}
	if gotUser != "api-key" || gotSecret != "api-secret" {
		t.Errorf("credentials = (%q, %q), want (api-key, api-secret)", gotUser, gotSecret)
	}
}

func TestNonSuccessStatusIsAFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "fw",
		Address:     host,
		Credentials: "k:s",
		TransportConfig: map[string]any{
			"port":                     port,
			"tls_insecure_skip_verify": true,
		},
	}, engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("a 401 response was reported as a successful halt")
	}
}

func TestUnsupportedActionIsRejected(t *testing.T) {
	tr := New(discardLogger())

	if _, err := tr.Execute(context.Background(),
		&engine.Client{Name: "fw"}, engine.ActionWake); err == nil {
		t.Error("the opnsense transport accepted a wake action")
	}
}
