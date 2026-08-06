package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := `
canarium:
  mode: disarmed
  host: 0.0.0.0:8420
sources: []
clients: []
plans: []
`
	os.WriteFile(path, []byte(data), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	if cfg.Canarium.Mode != "disarmed" {
		t.Errorf("mode = %s, want disarmed", cfg.Canarium.Mode)
	}
	if cfg.Canarium.Host != "0.0.0.0:8420" {
		t.Errorf("host = %s, want 0.0.0.0:8420", cfg.Canarium.Host)
	}
}

func TestLoadEnvVarExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	os.Setenv("TEST_CANARIUM_TOKEN", "secret123")
	defer os.Unsetenv("TEST_CANARIUM_TOKEN")

	data := `
canarium:
  mode: disarmed
clients:
  - name: test
    transport: ssh
    credentials: ${TEST_CANARIUM_TOKEN}
plans: []
`
	os.WriteFile(path, []byte(data), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	if cfg.Clients[0].Credentials != "secret123" {
		t.Errorf("credentials = %s, want secret123", cfg.Clients[0].Credentials)
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	data := `
clients:
  - name: test
    transport: ssh
plans: []
`
	os.WriteFile(path, []byte(data), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if cfg.Canarium.Mode != "disarmed" {
		t.Errorf("default mode = %s, want disarmed", cfg.Canarium.Mode)
	}
	if cfg.Canarium.DataDir != "/var/lib/canarium" {
		t.Errorf("default data_dir = %s", cfg.Canarium.DataDir)
	}
	if cfg.Clients[0].ShutdownBudget != "3m" {
		t.Errorf("default shutdown_budget = %s, want 3m", cfg.Clients[0].ShutdownBudget)
	}
	if cfg.Clients[0].FeedPolicy != "any" {
		t.Errorf("default feed_policy = %s, want any", cfg.Clients[0].FeedPolicy)
	}
	if cfg.Clients[0].WakePolicy != "power_state" {
		t.Errorf("default wake_policy = %s, want power_state", cfg.Clients[0].WakePolicy)
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("{{{{invalid yaml"), 0644)

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseDurationDays(t *testing.T) {
	d, err := ParseDuration("7d")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if d.Hours() != 168 {
		t.Errorf("7d = %v hours, want 168", d.Hours())
	}
}

func TestParseDurationEmpty(t *testing.T) {
	d, err := ParseDuration("")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if d != 0 {
		t.Errorf("empty duration = %v, want 0", d)
	}
}
