package main

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	nutmod "github.com/nkcx/canarium/modules/nut"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildTransportsCoversEveryDocumentedTransport(t *testing.T) {
	transports := buildTransports(&config.Config{}, discardLogger())

	// Every transport the README and example config name.
	for _, want := range []string{
		"ssh", "wol", "exec", "rest", "proxmox", "truenas", "opnsense",
		"nut", "snmp-poe",
	} {
		if _, ok := transports[want]; !ok {
			t.Errorf("transport %q is not registered", want)
		}
	}
}

func TestTransportNamesAreSorted(t *testing.T) {
	names := transportNames(&config.Config{}, discardLogger())

	if !slices.IsSorted(names) {
		t.Errorf("transportNames() = %v, want sorted for stable error messages", names)
	}
	if len(names) != len(buildTransports(&config.Config{}, discardLogger())) {
		t.Error("transportNames() does not cover every registered transport")
	}
}

func TestSourceTypesMatchBuildSource(t *testing.T) {
	store := facts.NewStore()

	for _, typ := range sourceTypes() {
		t.Run(typ, func(t *testing.T) {
			// An empty config block should fail for a *content* reason, not
			// because the type is unrecognised — that is what proves
			// sourceTypes() and buildSource() agree.
			_, err := buildSource(config.SourceConfig{Name: "s", Type: typ}, store, discardLogger())
			if err == nil {
				t.Fatalf("buildSource accepted a %s source with no configuration", typ)
			}
			if strings.Contains(err.Error(), "unknown source type") {
				t.Errorf("sourceTypes() lists %q but buildSource does not handle it", typ)
			}
		})
	}
}

// TestUnknownSourceTypeIsRejected is the regression test for registerSources
// having no default case: a `type: gpio` source was skipped in silence and
// its plans could never trigger.
func TestUnknownSourceTypeIsRejected(t *testing.T) {
	_, err := buildSource(
		config.SourceConfig{Name: "s", Type: "modbus"}, facts.NewStore(), discardLogger())
	if err == nil {
		t.Fatal("buildSource accepted an unknown source type")
	}
	if !strings.Contains(err.Error(), "unknown source type") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

// TestDecodeSourceConfigReadsEveryField is the regression test for the
// hand-written map plucking that dropped the NUT username and password:
// they were documented in examples/basic.yaml and never read.
func TestDecodeSourceConfigReadsEveryField(t *testing.T) {
	raw := map[string]any{
		"instances": []any{
			map[string]any{
				"name":          "rack_ups",
				"host":          "nut.lan",
				"port":          3493,
				"ups":           "ups0",
				"username":      "canarium",
				"password":      "s3cret",
				"poll_interval": "20s",
			},
		},
	}

	var cfg nutmod.Config
	if err := decodeSourceConfig(raw, &cfg); err != nil {
		t.Fatalf("decodeSourceConfig: %v", err)
	}
	if len(cfg.Instances) != 1 {
		t.Fatalf("decoded %d instances, want 1", len(cfg.Instances))
	}

	got := cfg.Instances[0]
	for _, tc := range []struct {
		field, got, want string
	}{
		{"name", got.Name, "rack_ups"},
		{"host", got.Host, "nut.lan"},
		{"ups", got.UPS, "ups0"},
		{"username", got.Username, "canarium"},
		{"password", got.Password, "s3cret"},
		{"poll_interval", got.PollInterval, "20s"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if got.Port != 3493 {
		t.Errorf("port = %d, want 3493", got.Port)
	}
}

// TestDecodeSourceConfigRejectsUnknownFields: a misspelled key inside a
// source's config block must not be silently ignored.
func TestDecodeSourceConfigRejectsUnknownFields(t *testing.T) {
	raw := map[string]any{
		"instances": []any{
			map[string]any{"name": "rack_ups", "poll_intervall": "20s"},
		},
	}

	var cfg nutmod.Config
	if err := decodeSourceConfig(raw, &cfg); err == nil {
		t.Fatal("decodeSourceConfig accepted a misspelled field")
	}
}

func TestBuildRegistryDescribesTheDaemon(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients: []config.ClientConfig{
			{Name: "nas", Transport: "ssh"},
			{Name: "pve", Transport: "proxmox"},
		},
	}

	store := facts.NewStore()
	store.RegisterSource("ups", 0, []facts.FactDeclaration{{Name: "status", Type: "set"}})

	reg := buildRegistry(cfg, store, discardLogger())

	if !slices.Contains(reg.Transports, "ssh") {
		t.Errorf("registry transports = %v, missing ssh", reg.Transports)
	}
	if !slices.Contains(reg.SourceTypes, "nut") {
		t.Errorf("registry source types = %v, missing nut", reg.SourceTypes)
	}
	if !slices.Contains(reg.Facts, "ups.status") {
		t.Errorf("registry facts = %v, missing the declared ups.status", reg.Facts)
	}
	// Derived facts must be present or conditions referencing them would be
	// reported as unknown by validation.
	for _, want := range []string{"client.nas.threatened", "client.pve.threatened"} {
		if !slices.Contains(reg.Facts, want) {
			t.Errorf("registry facts = %v, missing derived fact %q", reg.Facts, want)
		}
	}
	if !slices.IsSorted(reg.Facts) {
		t.Errorf("registry facts are not sorted: %v", reg.Facts)
	}
}

func TestNewSourceManagerRejectsABadSource(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Sources:  []config.SourceConfig{{Name: "bad", Type: "nope"}},
	}

	if _, err := NewSourceManager(cfg, facts.NewStore(), discardLogger()); err == nil {
		t.Error("NewSourceManager accepted an unknown source type")
	}
}

func TestSourceManagerStopIsSafeWithoutStart(t *testing.T) {
	cfg := &config.Config{Canarium: config.DefaultCanariumConfig()}

	m, err := NewSourceManager(cfg, facts.NewStore(), discardLogger())
	if err != nil {
		t.Fatalf("NewSourceManager: %v", err)
	}
	m.Stop() // must not panic on a nil cancel func
}
