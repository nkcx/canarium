package conditions

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// newStoreWithFact builds a store holding a single declared fact whose source
// polls every second.
func newStoreWithFact(t *testing.T, name, factType string) *facts.Store {
	t.Helper()

	store := facts.NewStore()
	store.RegisterSource("ups", time.Second, []facts.FactDeclaration{
		{Name: name, Type: factType},
	})
	return store
}

func floatPtr(f float64) *float64 { return &f }

// TestStaleFactNeverSatisfiesNumericCondition is the regression test for the
// safety property the README leads with: "losing contact with a sensor never
// triggers a shutdown".
//
// Previously only QualityUnknown was rejected. A stale fact kept its last
// observed value, so a UPS source that died just after reporting 25% charge
// left the trigger permanently true.
func TestStaleFactNeverSatisfiesNumericCondition(t *testing.T) {
	store := newStoreWithFact(t, "battery.charge", "percent")
	ev := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Below:     floatPtr(50),
	}

	now := time.Now()
	store.Update("ups.battery.charge", 25.0, now)
	store.RefreshQuality(now)

	if got := ev.Evaluate(cond, now); got != facts.True {
		t.Fatalf("fresh fact: got %v, want true", got)
	}

	// The source stops reporting. Well past two poll intervals, the fact is
	// stale and must no longer decide anything.
	later := now.Add(1 * time.Hour)
	store.RefreshQuality(later)

	if _, q, _ := store.Get("ups.battery.charge"); q != facts.QualityStale {
		t.Fatalf("quality = %v, want stale", q)
	}
	if got := ev.Evaluate(cond, later); got != facts.Unavailable {
		t.Errorf("stale fact: got %v, want unavailable — a dead sensor would trigger a shutdown", got)
	}
}

func TestStaleFactNeverSatisfiesStateCondition(t *testing.T) {
	store := newStoreWithFact(t, "status", "set")
	ev := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "state",
		Fact:      "ups.status",
		Contains:  "OB",
	}

	now := time.Now()
	store.Update("ups.status", []string{"OB", "DISCHRG"}, now)
	store.RefreshQuality(now)

	if got := ev.Evaluate(cond, now); got != facts.True {
		t.Fatalf("fresh fact: got %v, want true", got)
	}

	later := now.Add(time.Hour)
	store.RefreshQuality(later)

	if got := ev.Evaluate(cond, later); got != facts.Unavailable {
		t.Errorf("stale fact: got %v, want unavailable", got)
	}
}

// TestStaleFactBlocksWakeGate: waking early on stale data energises hardware
// before the battery has recovered.
func TestStaleFactBlocksWakeGate(t *testing.T) {
	store := newStoreWithFact(t, "battery.charge", "percent")
	ev := NewEvaluator(store)

	gate := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Above:     floatPtr(60),
	}

	now := time.Now()
	store.Update("ups.battery.charge", 80.0, now)
	store.RefreshQuality(now)
	if got := ev.Evaluate(gate, now); got != facts.True {
		t.Fatalf("fresh gate: got %v, want true", got)
	}

	later := now.Add(time.Hour)
	store.RefreshQuality(later)
	if got := ev.Evaluate(gate, later); got != facts.Unavailable {
		t.Errorf("stale gate: got %v, want unavailable", got)
	}
}

func TestUnknownFactIsUnavailable(t *testing.T) {
	store := newStoreWithFact(t, "battery.charge", "percent")
	ev := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Below:     floatPtr(50),
	}

	// Declared but never updated.
	if got := ev.Evaluate(cond, time.Now()); got != facts.Unavailable {
		t.Errorf("got %v, want unavailable for a fact that has never reported", got)
	}
}

func TestUndeclaredFactIsUnavailable(t *testing.T) {
	store := facts.NewStore()
	ev := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "typo.in.fact.name",
		Below:     floatPtr(50),
	}

	if got := ev.Evaluate(cond, time.Now()); got != facts.Unavailable {
		t.Errorf("got %v, want unavailable for an unknown fact", got)
	}
}

// TestTemplateAgreesWithStructuredConditionOnStaleness: a template and an
// equivalent structured condition must not disagree about whether a stale
// fact is usable.
func TestTemplateAgreesWithStructuredConditionOnStaleness(t *testing.T) {
	store := newStoreWithFact(t, "battery.charge", "percent")
	ev := NewEvaluator(store)

	structured := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Below:     floatPtr(50),
	}
	template := &config.ConditionConfig{
		Condition: "template",
		Value:     `fact("ups.battery.charge") != nil && float(fact("ups.battery.charge")) < 50.0`,
	}

	now := time.Now()
	store.Update("ups.battery.charge", 25.0, now)
	store.RefreshQuality(now)

	if a, b := ev.Evaluate(structured, now), ev.Evaluate(template, now); a != b {
		t.Errorf("fresh: structured = %v, template = %v", a, b)
	}

	later := now.Add(time.Hour)
	store.RefreshQuality(later)

	a, b := ev.Evaluate(structured, later), ev.Evaluate(template, later)
	if a != facts.Unavailable {
		t.Errorf("stale structured: got %v, want unavailable", a)
	}
	if b != facts.Unavailable {
		t.Errorf("stale template: got %v, want unavailable", b)
	}
}

// TestContainsOnStringIsSubstring: `contains: OB` must match a status string
// of "OB LB", the shape a UPS actually reports.
func TestContainsOnStringIsSubstring(t *testing.T) {
	store := newStoreWithFact(t, "status", "string")
	ev := NewEvaluator(store)

	now := time.Now()
	store.Update("ups.status", "OB LB", now)
	store.RefreshQuality(now)

	cond := &config.ConditionConfig{Condition: "state", Fact: "ups.status", Contains: "OB"}
	if got := ev.Evaluate(cond, now); got != facts.True {
		t.Errorf("contains OB against %q: got %v, want true", "OB LB", got)
	}

	missing := &config.ConditionConfig{Condition: "state", Fact: "ups.status", Contains: "RB"}
	if got := ev.Evaluate(missing, now); got != facts.False {
		t.Errorf("contains RB against %q: got %v, want false", "OB LB", got)
	}
}

// TestContainsHandlesAnySlice covers facts decoded from JSON, where a set
// arrives as []any rather than []string.
func TestContainsHandlesAnySlice(t *testing.T) {
	store := newStoreWithFact(t, "status", "set")
	ev := NewEvaluator(store)

	now := time.Now()
	store.Update("ups.status", []any{"OB", "DISCHRG"}, now)
	store.RefreshQuality(now)

	cond := &config.ConditionConfig{Condition: "state", Fact: "ups.status", Contains: "OB"}
	if got := ev.Evaluate(cond, now); got != facts.True {
		t.Errorf("got %v, want true for []any{\"OB\", ...}", got)
	}

	missing := &config.ConditionConfig{Condition: "state", Fact: "ups.status", Contains: "OL"}
	if got := ev.Evaluate(missing, now); got != facts.False {
		t.Errorf("got %v, want false", got)
	}
}

// TestZeroPollIntervalDoesNotMarkEverythingStale: a source declaring no poll
// interval would otherwise have its facts judged stale the instant after any
// update, permanently disabling every condition reading them.
func TestZeroPollIntervalDoesNotMarkEverythingStale(t *testing.T) {
	store := facts.NewStore()
	store.RegisterSource("gpio", 0, []facts.FactDeclaration{{Name: "flood", Type: "bool"}})

	now := time.Now()
	store.Update("gpio.flood", true, now)
	store.RefreshQuality(now)

	if _, q, _ := store.Get("gpio.flood"); q != facts.QualityGood {
		t.Errorf("quality = %v immediately after update, want good", q)
	}
}
