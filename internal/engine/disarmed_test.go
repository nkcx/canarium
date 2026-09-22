package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// disarmedConfig triggers on the UPS going on battery for 30s.
func disarmedConfig() *config.Config {
	plan := testPlan("outage", testStage("s", trueCondition(), "nas"))
	plan.Trigger = config.ConditionConfig{
		Condition: "state", Fact: "ups.status", Contains: "OB", For: "30s",
	}
	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{plan},
	}
}

func countEvents(h *harness, typ string) int {
	n := 0
	for _, e := range h.Events() {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestDisarmedEvaluatesButNeverExecutes: disarmed is documented as "sources
// poll, conditions evaluate, nothing executes", and exists so an operator
// can verify conditions evaluate as expected. evaluatePolicies returned
// before evaluating anything, so a real outage while disarmed gave no
// signal at all that the plan would have fired.
func TestDisarmedEvaluatesButNeverExecutes(t *testing.T) {
	h := newHarness(t, disarmedConfig())
	h.setFact("ups.status", []string{"OB"})

	// The dwell has to be observed: tick the policy loop across it.
	start := time.Now()
	h.exec.evaluator.Evaluate(&h.cfg.Plans[0].Trigger, start.Add(-time.Minute))
	for range 3 {
		h.exec.evaluatePolicies()
	}

	if h.exec.ActiveSequence() != nil {
		t.Fatal("a sequence started while disarmed")
	}
	if n := len(h.transport.Calls()); n != 0 {
		t.Fatalf("%d transport calls while disarmed; nothing may execute", n)
	}
	if n := countEvents(h, "would_trigger"); n != 1 {
		t.Errorf("got %d would_trigger events, want exactly one for the episode", n)
	}
	if n := countEvents(h, "trigger"); n != 0 {
		t.Errorf("got %d trigger events while disarmed", n)
	}
}

// TestDisarmedReportsEachEpisodeOnce: one outage is one notification, but
// a second outage is a second one.
func TestDisarmedReportsEachEpisodeOnce(t *testing.T) {
	h := newHarness(t, disarmedConfig())
	satisfy := func() {
		h.exec.evaluator.Evaluate(&h.cfg.Plans[0].Trigger, time.Now().Add(-time.Minute))
	}

	h.setFact("ups.status", []string{"OB"})
	satisfy()
	h.exec.evaluatePolicies()
	h.exec.evaluatePolicies()

	h.setFact("ups.status", []string{"OL"})
	h.exec.evaluatePolicies()

	h.setFact("ups.status", []string{"OB"})
	satisfy()
	h.exec.evaluatePolicies()

	if n := countEvents(h, "would_trigger"); n != 2 {
		t.Errorf("got %d would_trigger events across two outages, want 2", n)
	}
}

// TestDisarmedTimesTheTrigger: the Plans page shows dwell progress, which
// only exists if the policy loop is timing the condition.
func TestDisarmedTimesTheTrigger(t *testing.T) {
	h := newHarness(t, disarmedConfig())
	h.setFact("ups.status", []string{"OB"})

	h.exec.evaluatePolicies()

	ex := h.exec.ExplainCondition(&h.cfg.Plans[0].Trigger, time.Now())
	if ex.Dwell == nil || !ex.Dwell.Tracked {
		t.Errorf("trigger dwell = %+v while disarmed; nothing is timing it", ex.Dwell)
	}
}

// TestArmingRestartsTheTriggerTimer: because disarmed now times triggers,
// arming during an outage that had already held the trigger for its full
// dwell would start the sequence on the next tick. The click that says
// "arm" must not also be the click that shuts machines down.
func TestArmingRestartsTheTriggerTimer(t *testing.T) {
	h := newHarness(t, disarmedConfig())
	h.transport.setProbeState("nas", StateDown)
	h.setFact("ups.status", []string{"OB"})

	// Satisfied long ago, while disarmed.
	h.exec.evaluator.Evaluate(&h.cfg.Plans[0].Trigger, time.Now().Add(-10*time.Minute))
	h.exec.evaluatePolicies()

	h.exec.SetMode(ModeArmed)
	h.exec.evaluatePolicies()

	if h.exec.ActiveSequence() != nil {
		t.Fatal("arming started the sequence immediately on a dwell satisfied while disarmed")
	}

	// It does still fire once the trigger has held again while armed. The
	// armed tick above started a fresh timer at now; simulate a minute of
	// armed observation by restarting it a minute in the past.
	h.exec.evaluator.ResetDwell(&h.cfg.Plans[0].Trigger)
	h.exec.evaluator.Evaluate(&h.cfg.Plans[0].Trigger, time.Now().Add(-time.Minute))
	h.exec.evaluatePolicies()
	if h.exec.ActiveSequence() == nil && countEvents(h, "trigger") == 0 {
		t.Error("the plan never fired after the trigger held for its dwell while armed")
	}
	waitFor(t, 5*time.Second, "the sequence to finish", func() bool {
		return h.exec.ActiveSequence() == nil
	})
}
