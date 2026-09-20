package engine

import (
	"context"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/state"
)

// TestLiteralAddressesArePinnedAsThemselves covers the common case.
func TestLiteralAddressesArePinnedAsThemselves(t *testing.T) {
	client := testClient("nas")
	client.Address = "10.0.10.20"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	resolved := h.exec.resolveClientAddresses(context.Background())

	addr, ok := resolved["nas"]
	if !ok {
		t.Fatal("no resolved address recorded for nas")
	}
	if addr.IP != "10.0.10.20" {
		t.Errorf("IP = %q, want 10.0.10.20", addr.IP)
	}
	if addr.Hostname != "10.0.10.20" {
		t.Errorf("Hostname = %q, want the configured address", addr.Hostname)
	}
	if addr.ResolvedAt == "" {
		t.Error("ResolvedAt is empty")
	}
}

// TestUnresolvableNameDoesNotBlockTheSequence: refusing to shut anything
// down because of a DNS hiccup is the wrong trade.
func TestUnresolvableNameDoesNotBlockTheSequence(t *testing.T) {
	client := testClient("nas")
	client.Address = "does-not-exist.invalid"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	resolved := h.exec.resolveClientAddresses(context.Background())

	addr, ok := resolved["nas"]
	if !ok {
		t.Fatal("an unresolvable client was omitted entirely")
	}
	if addr.Hostname != "does-not-exist.invalid" {
		t.Errorf("Hostname = %q, want the configured name", addr.Hostname)
	}
	// IP is empty; addressFor falls back to the configured name.
}

// TestAddressForPrefersPinnedIP is the point of the whole mechanism: by wake
// time the DNS server may be on the UPS that just ran out.
func TestAddressForPrefersPinnedIP(t *testing.T) {
	client := testClient("nas")
	client.Address = "nas.lan"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)

	seq := &state.Sequence{
		ID:        "seq-1",
		PlanName:  "outage",
		StartedAt: time.Now(),
		ResolvedAddrs: map[string]state.ResolvedAddr{
			"nas": {IP: "10.0.10.20", Hostname: "nas.lan"},
		},
	}
	as := newActiveSequence(seq, &cfg.Plans[0])

	if got := h.exec.addressFor(as, &cfg.Clients[0]); got != "10.0.10.20" {
		t.Errorf("addressFor = %q, want the pinned IP 10.0.10.20", got)
	}

	built := h.exec.buildClientFor(as, &cfg.Clients[0])
	if built.Address != "10.0.10.20" {
		t.Errorf("built client address = %q, want the pinned IP", built.Address)
	}
}

func TestAddressForFallsBackWhenUnpinned(t *testing.T) {
	client := testClient("nas")
	client.Address = "nas.lan"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)

	seq := &state.Sequence{ID: "seq-1", StartedAt: time.Now()}
	as := newActiveSequence(seq, &cfg.Plans[0])

	if got := h.exec.addressFor(as, &cfg.Clients[0]); got != "nas.lan" {
		t.Errorf("addressFor = %q, want the configured name", got)
	}
	if got := h.exec.addressFor(nil, &cfg.Clients[0]); got != "nas.lan" {
		t.Errorf("addressFor(nil) = %q, want the configured name", got)
	}
}

// TestSequenceRecordsConfigSnapshot: the first question after an outage
// behaves unexpectedly is what the daemon was actually running.
func TestSequenceRecordsConfigSnapshot(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	h.transport.setProbeState("nas", StateDown)

	h.runSequence("outage", 10*time.Second)

	seq, err := h.db.LastSequence(t.Context())
	if err != nil {
		t.Fatalf("LastSequence: %v", err)
	}
	if seq == nil {
		t.Fatal("no sequence was recorded")
	}
	if len(seq.ConfigSnapshot) == 0 {
		t.Fatal("the sequence recorded no config snapshot")
	}
	if !containsSubstring(string(seq.ConfigSnapshot), "nas") {
		t.Errorf("the config snapshot does not mention the configured client:\n%s",
			seq.ConfigSnapshot)
	}
}

// TestSequenceRecordsResolvedAddresses checks the column is actually
// persisted and read back.
func TestSequenceRecordsResolvedAddresses(t *testing.T) {
	client := testClient("nas")
	client.Address = "10.0.10.20"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	h.transport.setProbeState("nas", StateDown)

	h.runSequence("outage", 10*time.Second)

	seq, err := h.db.LastSequence(t.Context())
	if err != nil {
		t.Fatalf("LastSequence: %v", err)
	}
	addr, ok := seq.ResolvedAddrs["nas"]
	if !ok {
		t.Fatal("no resolved address was persisted")
	}
	if addr.IP != "10.0.10.20" {
		t.Errorf("persisted IP = %q, want 10.0.10.20", addr.IP)
	}
}

// TestBuildClientPopulatesEveryField guards against fields the transports
// read silently staying at their zero values, as FeedPolicy, WakePolicy,
// ShutdownBudget, GuardPeriod, DependsOn, After and Before all did.
func TestBuildClientPopulatesEveryField(t *testing.T) {
	c := config.ClientConfig{
		Name:           "nas",
		Transport:      "fake",
		Address:        "10.0.0.1",
		MAC:            "aa:bb:cc:dd:ee:ff",
		Credentials:    "secret",
		Tags:           []string{"storage"},
		Feeds:          []string{"ups_a"},
		FeedPolicy:     "all",
		WakePolicy:     "retain_state",
		ShutdownBudget: "4m",
		GuardPeriod:    "90s",
		DependsOn:      []string{"switch"},
		After:          []string{"hypervisor"},
		Before:         []string{"firewall"},
	}

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{c},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	built := h.exec.buildClient(&cfg.Clients[0])

	if built.FeedPolicy != FeedPolicyAll {
		t.Errorf("FeedPolicy = %v, want all", built.FeedPolicy)
	}
	if built.WakePolicy != WakePolicyRetainState {
		t.Errorf("WakePolicy = %v, want retain_state", built.WakePolicy)
	}
	if built.ShutdownBudget != 4*time.Minute {
		t.Errorf("ShutdownBudget = %v, want 4m", built.ShutdownBudget)
	}
	if built.GuardPeriod != 90*time.Second {
		t.Errorf("GuardPeriod = %v, want 90s", built.GuardPeriod)
	}
	if len(built.DependsOn) != 1 || built.DependsOn[0] != "switch" {
		t.Errorf("DependsOn = %v, want [switch]", built.DependsOn)
	}
	if len(built.After) != 1 || len(built.Before) != 1 {
		t.Errorf("After = %v, Before = %v; want both populated", built.After, built.Before)
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle ||
			len(needle) == 0 ||
			indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
