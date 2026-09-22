package conditions

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// TestExplainNeverTouchesDwellState is the reason Explain exists. Evaluate
// advances and persists dwell timers, so an API handler calling it whenever
// someone opened a page would sample sporadically, miss a flap between two
// views, and over-credit the timer -- changing when a plan fires because
// somebody looked at it.
func TestExplainNeverTouchesDwellState(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OB"}, time.Now())
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB", For: "30s",
	}

	now := time.Now()
	for i := range 10 {
		eval.Explain(cond, now.Add(time.Duration(i)*time.Minute))
	}

	if progress := eval.DwellProgress(now); len(progress) != 0 {
		t.Fatalf("Explain created %d dwell tracker(s); it must be read-only", len(progress))
	}
}

// TestExplainDoesNotAdvanceAnExistingTimer: once the policy loop is timing
// a condition, looking at it must not move the clock.
func TestExplainDoesNotAdvanceAnExistingTimer(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OB"}, time.Now())
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB", For: "30s",
	}

	start := time.Now()
	eval.Evaluate(cond, start) // the policy loop starts the timer

	before := eval.DwellProgress(start)
	eval.Explain(cond, start.Add(10*time.Minute))
	after := eval.DwellProgress(start)

	for key, b := range before {
		if a := after[key]; a != b {
			t.Errorf("Explain changed the timer: before %+v, after %+v", b, a)
		}
	}
}

func TestExplainReportsDwellProgress(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OB"}, time.Now())
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB", For: "30s",
	}

	start := time.Now()
	eval.Evaluate(cond, start)

	ex := eval.Explain(cond, start.Add(12*time.Second))
	if ex.Instant != "true" {
		t.Errorf("instant = %s, want true: the condition holds", ex.Instant)
	}
	if ex.Result != "false" {
		t.Errorf("result = %s, want false: 12s of a 30s dwell is not satisfied", ex.Result)
	}
	if ex.Dwell == nil || !ex.Dwell.Tracked {
		t.Fatalf("dwell = %+v, want a tracked timer", ex.Dwell)
	}
	if ex.Dwell.ElapsedSeconds < 11 || ex.Dwell.ElapsedSeconds > 13 {
		t.Errorf("elapsed = %.1fs, want about 12s", ex.Dwell.ElapsedSeconds)
	}
	if ex.Dwell.RequiredSeconds != 30 {
		t.Errorf("required = %.1fs, want 30s", ex.Dwell.RequiredSeconds)
	}

	done := eval.Explain(cond, start.Add(31*time.Second))
	if done.Result != "true" {
		t.Errorf("after 31s result = %s, want true", done.Result)
	}
}

// TestExplainDoesNotShowStaleCredit: a timer the policy loop is about to
// reset must not read as progress.
func TestExplainDoesNotShowStaleCredit(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OB"}, time.Now())
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB", For: "30s",
	}
	start := time.Now()
	eval.Evaluate(cond, start)

	store.Update("ups.status", []string{"OL"}, time.Now())
	ex := eval.Explain(cond, start.Add(20*time.Second))

	if ex.Result != "false" {
		t.Errorf("result = %s, want false: power is back", ex.Result)
	}
	if ex.Dwell.ElapsedSeconds != 0 {
		t.Errorf("elapsed = %.1fs while the condition is false, want 0", ex.Dwell.ElapsedSeconds)
	}
}

func TestExplainUntrackedDwellIsSaidSo(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 80.0, time.Now())
	eval := NewEvaluator(store)

	above := 60.0
	cond := &config.ConditionConfig{
		Condition: "numeric", Fact: "ups.charge", Above: &above, For: "5m",
	}

	ex := eval.Explain(cond, time.Now())
	if ex.Dwell == nil {
		t.Fatal("no dwell reported for a condition with for:")
	}
	if ex.Dwell.Tracked {
		t.Error("an unevaluated condition reported a tracked timer")
	}
	if ex.Result != "false" {
		t.Errorf("result = %s, want false: nothing has timed the dwell", ex.Result)
	}
}

func TestExplainIncludesTheReadingDecidedOn(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OL"}, time.Now())
	eval := NewEvaluator(store)

	ex := eval.Explain(&config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB",
	}, time.Now())

	if ex.Result != "false" {
		t.Errorf("result = %s, want false", ex.Result)
	}
	if ex.FactQuality != "good" {
		t.Errorf("fact quality = %q, want good", ex.FactQuality)
	}
	vals, ok := ex.FactValue.([]string)
	if !ok || len(vals) != 1 || vals[0] != "OL" {
		t.Errorf("fact value = %#v, want [OL]", ex.FactValue)
	}
}

func TestExplainCompositeMatchesEvaluate(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OB"}, time.Now())
	store.Update("ups.charge", 45.0, time.Now())
	eval := NewEvaluator(store)

	below := 50.0
	cond := &config.ConditionConfig{
		Condition: "and",
		Conditions: []config.ConditionConfig{
			{Condition: "state", Fact: "ups.status", Contains: "OB"},
			{Condition: "numeric", Fact: "ups.charge", Below: &below},
		},
	}

	now := time.Now()
	ex := eval.Explain(cond, now)
	if want := eval.Evaluate(cond, now).String(); ex.Result != want {
		t.Errorf("Explain = %s, Evaluate = %s; they must agree without dwell", ex.Result, want)
	}
	if len(ex.Conditions) != 2 {
		t.Fatalf("got %d child explanations, want 2", len(ex.Conditions))
	}
	for i, c := range ex.Conditions {
		if c.Result != "true" {
			t.Errorf("child %d = %s, want true", i, c.Result)
		}
	}
}

// TestExplainUnknownFactIsUnavailable: three-valued logic has to survive
// into the explanation, or the UI would show "false" for "we don't know".
func TestExplainUnknownFactIsUnavailable(t *testing.T) {
	eval := NewEvaluator(setupStore())
	ex := eval.Explain(&config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB",
	}, time.Now())
	if ex.Result != "unavailable" {
		t.Errorf("result = %s, want unavailable", ex.Result)
	}
}

func TestExplainNilCondition(t *testing.T) {
	ex := NewEvaluator(setupStore()).Explain(nil, time.Now())
	if ex.Result != "unavailable" {
		t.Errorf("nil condition result = %s, want unavailable", ex.Result)
	}
}
