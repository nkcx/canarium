package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// dualFeedConfig models the case the spec is written around: a dual-PSU
// machine fed by two UPSes, alongside a single-PSU machine on one.
func dualFeedConfig(policy, commsLoss string) *config.Config {
	dual := testClient("hypervisor")
	dual.Feeds = []string{"ups_a", "ups_b"}
	dual.FeedPolicy = policy
	dual.CommsLossAssumes = commsLoss

	single := testClient("nas")
	single.Feeds = []string{"ups_a"}
	single.FeedPolicy = "any"
	single.CommsLossAssumes = commsLoss

	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{dual, single},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}
}

// feedHarness builds a harness with two UPS sources declared.
func feedHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()

	h := newHarness(t, cfg)
	for _, name := range []string{"ups_a", "ups_b"} {
		h.store.RegisterSource(name, time.Second, []facts.FactDeclaration{
			{Name: "status", Type: "set"},
			{Name: "battery.charge", Type: "percent"},
		})
	}
	h.exec.registerDerivedFacts()
	return h
}

func threatened(t *testing.T, h *harness, client string) bool {
	t.Helper()

	value, quality, _ := h.store.Get("client." + client + ".threatened")
	if quality != facts.QualityGood {
		t.Fatalf("client.%s.threatened quality = %v, want good", client, quality)
	}
	b, ok := value.(bool)
	if !ok {
		t.Fatalf("client.%s.threatened = %T, want bool", client, value)
	}
	return b
}

func setStatus(h *harness, feed string, flags ...string) {
	now := time.Now()
	h.store.Update(feed+".status", flags, now)
	h.store.RefreshQuality(now)
}

// TestFeedPolicyAll is the case redundant power exists for: a dual-fed
// machine must not shut down because one side failed.
func TestFeedPolicyAll(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("all", ""))

	setStatus(h, "ups_a", "OB", "DISCHRG")
	setStatus(h, "ups_b", "OL")
	h.exec.updateDerivedFacts(time.Now())

	if threatened(t, h, "hypervisor") {
		t.Error("a dual-fed client with feed_policy: all was threatened by one " +
			"failing feed; redundant power would be pointless")
	}
	if !threatened(t, h, "nas") {
		t.Error("a single-fed client on the failing UPS was not threatened")
	}

	// Both sides now failing.
	setStatus(h, "ups_b", "OB")
	h.exec.updateDerivedFacts(time.Now())

	if !threatened(t, h, "hypervisor") {
		t.Error("a dual-fed client was not threatened with both feeds failing")
	}
}

func TestFeedPolicyAny(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("any", ""))

	setStatus(h, "ups_a", "OB")
	setStatus(h, "ups_b", "OL")
	h.exec.updateDerivedFacts(time.Now())

	if !threatened(t, h, "hypervisor") {
		t.Error("feed_policy: any was not threatened by one failing feed")
	}
}

func TestAllFeedsHealthyIsNotThreatened(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("any", ""))

	setStatus(h, "ups_a", "OL")
	setStatus(h, "ups_b", "OL", "CHRG")
	h.exec.updateDerivedFacts(time.Now())

	for _, client := range []string{"hypervisor", "nas"} {
		if threatened(t, h, client) {
			t.Errorf("%s was threatened with every feed on line power", client)
		}
	}
}

// TestCommsLossDefaultsToSafe is the README's headline safety property:
// losing contact with a sensor must never initiate a shutdown.
func TestCommsLossDefaultsToSafe(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("any", ""))

	// Report once, then let both sources go stale.
	setStatus(h, "ups_a", "OL")
	setStatus(h, "ups_b", "OL")
	h.store.RefreshQuality(time.Now().Add(time.Hour))

	h.exec.updateDerivedFacts(time.Now())

	for _, client := range []string{"hypervisor", "nas"} {
		if threatened(t, h, client) {
			t.Errorf("%s became threatened when its UPS source went quiet; "+
				"losing a sensor must not initiate a shutdown", client)
		}
	}
}

// TestCommsLossThreatenedOptsIn covers the conservative setting.
func TestCommsLossThreatenedOptsIn(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("any", config.CommsLossThreatened))

	setStatus(h, "ups_a", "OL")
	setStatus(h, "ups_b", "OL")
	h.store.RefreshQuality(time.Now().Add(time.Hour))

	h.exec.updateDerivedFacts(time.Now())

	if !threatened(t, h, "nas") {
		t.Error("comms_loss_assumes: threatened did not treat a silent feed as threatened")
	}
}

// TestNeverReportedFeedIsNotThreatened: a source that has never produced a
// value is unknown, not failing.
func TestNeverReportedFeedIsNotThreatened(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("any", ""))

	h.exec.updateDerivedFacts(time.Now())

	if threatened(t, h, "nas") {
		t.Error("a feed that has never reported was treated as threatened")
	}
}

func TestThreatFlags(t *testing.T) {
	tests := []struct {
		flags []string
		want  bool
	}{
		{[]string{"OL"}, false},
		{[]string{"OL", "CHRG"}, false},
		{[]string{"OB"}, true},
		{[]string{"OB", "DISCHRG"}, true},
		{[]string{"LB"}, true},
		{[]string{"ALARM"}, true},
		{[]string{"RB"}, true},
		{[]string{"OL", "RB"}, true},
		{[]string{"BYPASS"}, false},
	}

	for _, tt := range tests {
		t.Run(flagLabel(tt.flags), func(t *testing.T) {
			h := feedHarness(t, dualFeedConfig("any", ""))
			setStatus(h, "ups_a", tt.flags...)
			h.exec.updateDerivedFacts(time.Now())

			if got := threatened(t, h, "nas"); got != tt.want {
				t.Errorf("status %v: threatened = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

// TestEmptyFeedsUsesAllUPSSources covers the single-UPS case, which the spec
// says should need no feeds configuration at all.
func TestEmptyFeedsUsesAllUPSSources(t *testing.T) {
	client := testClient("nas")
	client.Feeds = nil
	client.FeedPolicy = "any"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := feedHarness(t, cfg)

	setStatus(h, "ups_a", "OB")
	setStatus(h, "ups_b", "OL")
	h.exec.updateDerivedFacts(time.Now())

	if !threatened(t, h, "nas") {
		t.Error("a client with no feeds configured was not associated with the declared UPS sources")
	}
}

// TestThreatenedFactIsUsableInAConditionfeeds the derived fact back into the
// condition evaluator, which is the point of deriving it.
func TestThreatenedFactIsUsableInACondition(t *testing.T) {
	h := feedHarness(t, dualFeedConfig("any", ""))

	setStatus(h, "ups_a", "OB")
	h.exec.updateDerivedFacts(time.Now())
	h.store.RefreshQuality(time.Now())

	cond := &config.ConditionConfig{
		Condition: "state",
		Fact:      "client.nas.threatened",
		Is:        "true",
	}

	if got := h.exec.evaluator.Evaluate(cond, time.Now()); got != facts.True {
		t.Errorf("condition on client.nas.threatened = %v, want true", got)
	}
}

func TestDerivedFactKeys(t *testing.T) {
	cfg := dualFeedConfig("any", "")

	keys := DerivedFactKeys(cfg)
	want := map[string]bool{
		"client.hypervisor.threatened": true,
		"client.nas.threatened":        true,
	}

	if len(keys) != len(want) {
		t.Fatalf("DerivedFactKeys() = %v, want %d keys", keys, len(want))
	}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("unexpected derived key %q", k)
		}
	}
}

func flagLabel(flags []string) string {
	out := ""
	for i, f := range flags {
		if i > 0 {
			out += "_"
		}
		out += f
	}
	if out == "" {
		return "empty"
	}
	return out
}
