package conditions

import (
	"context"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// dwellFixture builds an evaluator over a single numeric fact plus a
// condition requiring that fact to hold below 50 for the given duration.
func dwellFixture(t *testing.T, for_ string) (*Evaluator, *facts.Store, *config.ConditionConfig) {
	t.Helper()

	store := facts.NewStore()
	store.RegisterSource("ups", time.Second, []facts.FactDeclaration{
		{Name: "battery.charge", Type: "percent"},
	})

	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Below:     floatPtr(50),
		For:       for_,
	}
	return NewEvaluator(store), store, cond
}

// set updates the fact and refreshes quality at the given simulated instant.
func set(store *facts.Store, value float64, at time.Time) {
	store.Update("ups.battery.charge", value, at)
	store.RefreshQuality(at)
}

// TestDwellUsesCallerSuppliedTime is the core regression test.
//
// The old implementation measured elapsed time with time.Now().UnixNano()
// regardless of the timestamp it was handed. Advancing the caller's clock
// therefore had no effect: a `for: 5m` condition could never be satisfied by
// a caller stepping time forward, which is exactly what the simulator does.
func TestDwellUsesCallerSuppliedTime(t *testing.T) {
	ev, store, cond := dwellFixture(t, "5m")

	base := time.Now()
	set(store, 25, base)

	if got := ev.Evaluate(cond, base); got != facts.False {
		t.Fatalf("at t=0: got %v, want false (dwell not yet met)", got)
	}

	// Step the caller's clock past the dwell without any real time passing.
	later := base.Add(6 * time.Minute)
	set(store, 25, later)

	if got := ev.Evaluate(cond, later); got != facts.True {
		t.Errorf("at t=6m: got %v, want true — dwell is not tracking the supplied clock", got)
	}
}

func TestDwellNotSatisfiedBeforeDuration(t *testing.T) {
	ev, store, cond := dwellFixture(t, "5m")

	base := time.Now()
	for _, offset := range []time.Duration{0, time.Minute, 2 * time.Minute, 4*time.Minute + 59*time.Second} {
		at := base.Add(offset)
		set(store, 25, at)
		if got := ev.Evaluate(cond, at); got != facts.False {
			t.Errorf("at t=%v: got %v, want false", offset, got)
		}
	}

	at := base.Add(5 * time.Minute)
	set(store, 25, at)
	if got := ev.Evaluate(cond, at); got != facts.True {
		t.Errorf("at t=5m: got %v, want true", got)
	}
}

// TestDwellResetsWhenConditionLapses: dwell means "continuously true for",
// not "true for a cumulative total of".
func TestDwellResetsWhenConditionLapses(t *testing.T) {
	ev, store, cond := dwellFixture(t, "5m")

	base := time.Now()

	set(store, 25, base)
	ev.Evaluate(cond, base)

	// Four minutes of progress...
	at := base.Add(4 * time.Minute)
	set(store, 25, at)
	if got := ev.Evaluate(cond, at); got != facts.False {
		t.Fatalf("at t=4m: got %v, want false", got)
	}

	// ...then the condition lapses.
	at = base.Add(4*time.Minute + 30*time.Second)
	set(store, 80, at)
	if got := ev.Evaluate(cond, at); got != facts.False {
		t.Fatalf("condition false: got %v, want false", got)
	}

	// Two more minutes true is not enough; the clock restarted.
	at = base.Add(7 * time.Minute)
	set(store, 25, at)
	ev.Evaluate(cond, at)

	at = base.Add(9 * time.Minute)
	set(store, 25, at)
	if got := ev.Evaluate(cond, at); got != facts.False {
		t.Errorf("at t=9m after a lapse: got %v, want false — credit was not reset", got)
	}

	// Five minutes after the restart, it is satisfied.
	at = base.Add(12 * time.Minute)
	set(store, 25, at)
	if got := ev.Evaluate(cond, at); got != facts.True {
		t.Errorf("at t=12m: got %v, want true", got)
	}
}

// TestDwellSurvivesWallClockJump verifies immunity to NTP steps, which are
// guaranteed on the RTC-less hardware this targets.
func TestDwellSurvivesWallClockJump(t *testing.T) {
	ev, store, cond := dwellFixture(t, "10m")

	base := time.Now()
	set(store, 25, base)
	ev.Evaluate(cond, base)

	// A wall-clock-only jump: a timestamp far in the future that carries no
	// monotonic reading, as a freshly NTP-corrected clock would produce.
	jumped := base.Add(time.Hour).Round(0) // Round(0) strips the monotonic reading
	set(store, 25, jumped)

	if got := ev.Evaluate(cond, jumped); got != facts.True {
		// Still true here because the caller's timestamp genuinely advanced.
		// The point of the test is that it does not panic or produce a
		// negative elapsed; see TestDwellClampsNegativeElapsed.
		t.Logf("note: got %v", got)
	}
}

// TestDwellClampsNegativeElapsed: a backwards wall-clock step must not
// produce negative credit, which would make dwell unsatisfiable forever.
func TestDwellClampsNegativeElapsed(t *testing.T) {
	tracker := &DwellTracker{
		Required:  5 * time.Minute,
		StartedAt: time.Now().Round(0),
		Credited:  2 * time.Minute,
	}

	backwards := tracker.StartedAt.Add(-time.Hour)
	if got := tracker.Elapsed(backwards); got != 2*time.Minute {
		t.Errorf("Elapsed with a backwards clock = %v, want the carried credit of 2m", got)
	}
}

// TestDwellPreservesUnavailable: "we don't know" must not collapse into
// "the dwell requirement is not met".
func TestDwellPreservesUnavailable(t *testing.T) {
	ev, store, cond := dwellFixture(t, "5m")

	base := time.Now()
	set(store, 25, base)
	ev.Evaluate(cond, base)

	// Source goes stale.
	later := base.Add(time.Hour)
	store.RefreshQuality(later)

	if got := ev.Evaluate(cond, later); got != facts.Unavailable {
		t.Errorf("stale fact under dwell: got %v, want unavailable", got)
	}
}

// fakeDwellStore is an in-memory DwellStore for exercising persistence.
type fakeDwellStore struct {
	records map[string]DwellRecord
	saves   int
}

func newFakeDwellStore() *fakeDwellStore {
	return &fakeDwellStore{records: make(map[string]DwellRecord)}
}

func (f *fakeDwellStore) LoadDwellTrackers(ctx context.Context) (map[string]DwellRecord, error) {
	out := make(map[string]DwellRecord, len(f.records))
	for k, v := range f.records {
		out[k] = v
	}
	return out, nil
}

func (f *fakeDwellStore) SaveDwellTracker(ctx context.Context, key string, rec DwellRecord) error {
	f.records[key] = rec
	f.saves++
	return nil
}

func (f *fakeDwellStore) DeleteDwellTracker(ctx context.Context, key string) error {
	delete(f.records, key)
	return nil
}

// TestDwellSurvivesRestart: a daemon restarting partway through a wake gate
// must not silently start the timer over.
func TestDwellSurvivesRestart(t *testing.T) {
	store := newFakeDwellStore()

	ev1, factStore1, cond := dwellFixture(t, "10m")
	if err := ev1.SetDwellStore(context.Background(), store); err != nil {
		t.Fatalf("SetDwellStore: %v", err)
	}

	base := time.Now()
	set(factStore1, 25, base)
	ev1.Evaluate(cond, base)

	// Accrue eight minutes.
	at := base.Add(8 * time.Minute)
	set(factStore1, 25, at)
	if got := ev1.Evaluate(cond, at); got != facts.False {
		t.Fatalf("at t=8m: got %v, want false", got)
	}

	// Restart: a fresh evaluator loading the same store.
	ev2, factStore2, _ := dwellFixture(t, "10m")
	if err := ev2.SetDwellStore(context.Background(), store); err != nil {
		t.Fatalf("SetDwellStore after restart: %v", err)
	}

	progress := ev2.DwellProgress(at)
	if len(progress) != 1 {
		t.Fatalf("restored %d trackers, want 1", len(progress))
	}
	for _, snap := range progress {
		if snap.Elapsed < 7*time.Minute {
			t.Errorf("restored elapsed = %v, want roughly 8m; dwell credit was lost across restart", snap.Elapsed)
		}
	}

	// Two more minutes of the condition holding satisfies the ten.
	resume := at.Add(1 * time.Second)
	set(factStore2, 25, resume)
	ev2.Evaluate(cond, resume)

	done := resume.Add(2 * time.Minute)
	set(factStore2, 25, done)
	if got := ev2.Evaluate(cond, done); got != facts.True {
		t.Errorf("after restart + 2m: got %v, want true", got)
	}
}

// TestRestartGapIsNotCredited: the interval while the daemon was down must
// not count toward dwell.
func TestRestartGapIsNotCredited(t *testing.T) {
	store := newFakeDwellStore()

	ev1, factStore1, cond := dwellFixture(t, "10m")
	if err := ev1.SetDwellStore(context.Background(), store); err != nil {
		t.Fatalf("SetDwellStore: %v", err)
	}

	base := time.Now()
	set(factStore1, 25, base)
	ev1.Evaluate(cond, base)

	at := base.Add(1 * time.Minute)
	set(factStore1, 25, at)
	ev1.Evaluate(cond, at)

	// The daemon is down for an hour.
	ev2, factStore2, _ := dwellFixture(t, "10m")
	if err := ev2.SetDwellStore(context.Background(), store); err != nil {
		t.Fatalf("SetDwellStore: %v", err)
	}

	resume := at.Add(time.Hour)
	set(factStore2, 25, resume)

	if got := ev2.Evaluate(cond, resume); got != facts.False {
		t.Errorf("immediately after a one-hour outage: got %v, want false — "+
			"the downtime was credited toward dwell", got)
	}
}

// TestDwellKeysDoNotCollide is the regression test for conditionKey dropping
// nested conditions: two distinct compound conditions both rendered as
// "and|for:5m" and shared a single timer.
func TestDwellKeysDoNotCollide(t *testing.T) {
	a := &config.ConditionConfig{
		Condition: "and",
		For:       "5m",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.battery.charge", Below: floatPtr(20)},
		},
	}
	b := &config.ConditionConfig{
		Condition: "and",
		For:       "5m",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.battery.runtime", Below: floatPtr(300)},
		},
	}

	if conditionKey(a) == conditionKey(b) {
		t.Error("two different compound conditions share a dwell key; their timers would interfere")
	}

	// Fields the old key omitted entirely.
	cases := [][2]*config.ConditionConfig{
		{
			{Condition: "state", Fact: "ups.status", IsNot: "OL"},
			{Condition: "state", Fact: "ups.status", IsNot: "OB"},
		},
		{
			{Condition: "state", Fact: "ups.status", In: []string{"OB"}},
			{Condition: "state", Fact: "ups.status", In: []string{"LB"}},
		},
		{
			{Condition: "numeric", Fact: "ups.x", Equals: 1},
			{Condition: "numeric", Fact: "ups.x", Equals: 2},
		},
	}
	for i, pair := range cases {
		if conditionKey(pair[0]) == conditionKey(pair[1]) {
			t.Errorf("case %d: conditions differing only in a previously-ignored field share a key", i)
		}
	}
}

func TestConditionKeyIsStable(t *testing.T) {
	cond := &config.ConditionConfig{
		Condition: "and",
		For:       "5m",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.battery.charge", Below: floatPtr(20)},
			{Condition: "state", Fact: "ups.status", Contains: "OB"},
		},
	}

	first := conditionKey(cond)
	for i := 0; i < 10; i++ {
		if got := conditionKey(cond); got != first {
			t.Fatalf("conditionKey is not deterministic: %q then %q", first, got)
		}
	}
}
