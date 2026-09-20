package netutil

import (
	"crypto/tls"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestTLSConfigVerifiesByDefault is the regression test for three transports
// that hardcoded InsecureSkipVerify: true, sending hypervisor and storage
// credentials over connections any on-path attacker could impersonate.
func TestTLSConfigVerifiesByDefault(t *testing.T) {
	cfg, err := TLSOptions{}.TLSConfig(discardLogger(), "nas")
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Error("certificate verification is disabled by default")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want at least TLS 1.2", cfg.MinVersion)
	}
}

func TestTLSConfigHonoursExplicitOptOut(t *testing.T) {
	cfg, err := TLSOptions{InsecureSkipVerify: true}.TLSConfig(discardLogger(), "nas")
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Error("an explicit opt-out was ignored")
	}
}

func TestTLSConfigPinsCACertificate(t *testing.T) {
	// A syntactically valid self-signed certificate is not needed here; what
	// matters is that a PEM block is accepted and installed as the root set.
	pem := generateTestCertPEM(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem, 0o600); err != nil {
		t.Fatalf("writing CA file: %v", err)
	}

	cfg, err := TLSOptions{CACertFile: path}.TLSConfig(discardLogger(), "nas")
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Error("tls_ca_cert did not install a root CA pool")
	}
	if cfg.InsecureSkipVerify {
		t.Error("pinning a CA should not disable verification")
	}
}

func TestTLSConfigRejectsMissingCAFile(t *testing.T) {
	_, err := TLSOptions{CACertFile: "/nonexistent/ca.pem"}.TLSConfig(discardLogger(), "nas")
	if err == nil {
		t.Error("a missing tls_ca_cert was accepted silently")
	}
}

func TestTLSConfigRejectsGarbageCAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("writing CA file: %v", err)
	}

	_, err := TLSOptions{CACertFile: path}.TLSConfig(discardLogger(), "nas")
	if err == nil {
		t.Error("a CA file with no certificates was accepted, which would " +
			"silently fall back to an empty root set")
	}
}

// TestTLSConfigRejectsContradictoryOptions: pinning a CA and then not
// checking it is almost certainly a mistake, and silently honouring the
// weaker setting would be the wrong resolution.
func TestTLSConfigRejectsContradictoryOptions(t *testing.T) {
	pem := generateTestCertPEM(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem, 0o600); err != nil {
		t.Fatalf("writing CA file: %v", err)
	}

	_, err := TLSOptions{CACertFile: path, InsecureSkipVerify: true}.
		TLSConfig(discardLogger(), "nas")
	if err == nil {
		t.Error("tls_ca_cert together with tls_insecure_skip_verify was accepted")
	}
}

func TestTLSOptionsFrom(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]any
		want TLSOptions
	}{
		{"nil config", nil, TLSOptions{}},
		{"empty config", map[string]any{}, TLSOptions{}},
		{
			"bool opt-out",
			map[string]any{"tls_insecure_skip_verify": true},
			TLSOptions{InsecureSkipVerify: true},
		},
		{
			// Environment substitution yields strings, not bools.
			"string opt-out",
			map[string]any{"tls_insecure_skip_verify": "true"},
			TLSOptions{InsecureSkipVerify: true},
		},
		{
			"string false",
			map[string]any{"tls_insecure_skip_verify": "false"},
			TLSOptions{},
		},
		{
			"ca and server name",
			map[string]any{"tls_ca_cert": "/etc/ca.pem", "tls_server_name": "nas.lan"},
			TLSOptions{CACertFile: "/etc/ca.pem", ServerName: "nas.lan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TLSOptionsFrom(tt.cfg); got != tt.want {
				t.Errorf("TLSOptionsFrom() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTLSConfigSetsServerName(t *testing.T) {
	cfg, err := TLSOptions{ServerName: "nas.lan"}.TLSConfig(discardLogger(), "nas")
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	if cfg.ServerName != "nas.lan" {
		t.Errorf("ServerName = %q, want nas.lan", cfg.ServerName)
	}
}
