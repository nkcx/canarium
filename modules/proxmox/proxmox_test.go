package proxmox

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nkcx/canarium/internal/engine"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// hostPort splits an httptest server URL into its host and port.
func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %s: %v", rawURL, err)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing port from %s: %v", rawURL, err)
	}
	return u.Hostname(), port
}

// TestTLSVerificationIsOnByDefault is the regression test for
// InsecureSkipVerify being hardcoded true, which sent the PVEAPIToken over a
// connection any on-path attacker could impersonate.
func TestTLSVerificationIsOnByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:            "pve",
		Address:         host,
		Credentials:     "user@pam!id=secret",
		TransportConfig: map[string]any{"port": port},
	}, engine.ActionShutdown)

	// httptest's certificate is not in the system roots, so verification
	// must fail rather than silently succeeding.
	if err == nil && result != nil && result.Success {
		t.Error("connected to a server with an untrusted certificate; " +
			"TLS verification is not enforced")
	}
}

func TestInsecureSkipVerifyOptIn(t *testing.T) {
	var gotAuth string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	result, err := tr.Execute(context.Background(), &engine.Client{
		Name:        "pve",
		Address:     host,
		Credentials: "user@pam!id=secret",
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
	if !strings.HasPrefix(gotAuth, "PVEAPIToken=") {
		t.Errorf("Authorization = %q, want a PVEAPIToken header", gotAuth)
	}
}

func TestShutdownTargetsTheNodeStatusEndpoint(t *testing.T) {
	var gotPath, gotBody string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	if _, err := tr.Execute(context.Background(), &engine.Client{
		Name:    "steel",
		Address: host,
		TransportConfig: map[string]any{
			"port":                     port,
			"tls_insecure_skip_verify": true,
		},
	}, engine.ActionShutdown); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if gotPath != "/api2/json/nodes/steel/status" {
		t.Errorf("path = %q, want /api2/json/nodes/steel/status", gotPath)
	}
	if !strings.Contains(gotBody, "command=shutdown") {
		t.Errorf("body = %q, want command=shutdown", gotBody)
	}
}

func TestNodeNameOverride(t *testing.T) {
	var gotPath string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, port := hostPort(t, srv.URL)

	tr := New(discardLogger())
	if _, err := tr.Execute(context.Background(), &engine.Client{
		Name:    "client-name",
		Address: host,
		TransportConfig: map[string]any{
			"port":                     port,
			"node":                     "pve-node-1",
			"tls_insecure_skip_verify": true,
		},
	}, engine.ActionShutdown); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(gotPath, "pve-node-1") {
		t.Errorf("path = %q, want the configured node name", gotPath)
	}
}

func TestUnsupportedActionIsRejected(t *testing.T) {
	tr := New(discardLogger())

	if _, err := tr.Execute(context.Background(),
		&engine.Client{Name: "pve"}, engine.ActionWake); err == nil {
		t.Error("the proxmox transport accepted a wake action")
	}
}

func TestHTTPClientsAreCachedPerTLSConfig(t *testing.T) {
	tr := New(discardLogger())

	a, err := tr.httpClientFor(&engine.Client{Name: "a"})
	if err != nil {
		t.Fatalf("httpClientFor: %v", err)
	}
	b, err := tr.httpClientFor(&engine.Client{Name: "b"})
	if err != nil {
		t.Fatalf("httpClientFor: %v", err)
	}
	if a != b {
		t.Error("clients with identical TLS settings did not share an http.Client")
	}

	insecure, err := tr.httpClientFor(&engine.Client{
		Name:            "c",
		TransportConfig: map[string]any{"tls_insecure_skip_verify": true},
	})
	if err != nil {
		t.Fatalf("httpClientFor: %v", err)
	}
	if insecure == a {
		t.Error("a client with different TLS settings reused the same http.Client")
	}

	transport, ok := insecure.Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected transport type")
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("the opt-out was not applied to the http.Client")
	}
}
