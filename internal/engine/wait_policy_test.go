package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// waitPolicyConfig builds a two-stage plan whose first stage will time out.
func waitPolicyConfig(policy string) *config.Config {
	first := testStage("compute", chargeBelow(10), "nas")
	first.WaitTimeout = "30ms"
	first.WaitPolicy = policy

	second := testStage("storage", trueCondition(), "storage")

	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas"), testClient("storage")},
		Plans:    []config.PlanConfig{testPlan("outage", first, second)},
	}
}

// TestSkippedStageIsRecordedAndAnnounced: skipping means those hosts are
// never shut down. That is the documented behaviour of the default policy,
// but it used to happen entirely silently — no event, and the journal
// recorded nothing, so an operator could not tell half the rack had been
// left running.
func TestSkippedStageIsRecordedAndAnnounced(t *testing.T) {
	h := newHarness(t, waitPolicyConfig(config.WaitPolicySkip))
	h.transport.setProbeState("nas", StateDown)
	h.transport.setProbeState("storage", StateDown)

	// Charge well above the first stage's threshold, so it times out.
	h.setFact("ups.battery.charge", 90.0)

	h.runSequence("outage", 10*time.Second)

	// The skipped stage's client must not have been shut down...
	if got := h.transport.CallsFor(ActionShutdown); contains(got, "nas") {
		t.Errorf("a skipped stage still dispatched shutdown: %v", got)
	}
	// ...but the following stage must have run.
	if got := h.transport.CallsFor(ActionShutdown); !contains(got, "storage") {
		t.Errorf("the stage after a skip did not run: %v", got)
	}

	if !h.hasEvent("stage_skipped") {
		t.Errorf("no stage_skipped event was emitted; events were %v", h.EventTypes())
	}

	// The journal must record the skip, naming the clients left running.
	stages, err := h.db.GetCompletedStages(h.lastSequenceID(t))
	if err != nil {
		t.Fatalf("GetCompletedStages: %v", err)
	}
	if !containsInt(stages, 0) {
		t.Error("the skipped stage was not recorded in the journal")
	}
}

func TestEscalatePolicyEmitsTimeoutAndSkips(t *testing.T) {
	h := newHarness(t, waitPolicyConfig(config.WaitPolicyEscalate))
	h.transport.setProbeState("nas", StateDown)
	h.transport.setProbeState("storage", StateDown)
	h.setFact("ups.battery.charge", 90.0)

	h.runSequence("outage", 10*time.Second)

	if !h.hasEvent("stage_timeout") {
		t.Errorf("escalate did not emit stage_timeout; events were %v", h.EventTypes())
	}
	if !h.hasEvent("stage_skipped") {
		t.Errorf("escalate did not emit stage_skipped; events were %v", h.EventTypes())
	}
}

// TestHoldPolicyWaitsAndStaysAbortable: hold must not wedge the sequence.
func TestHoldPolicyWaitsAndStaysAbortable(t *testing.T) {
	h := newHarness(t, waitPolicyConfig(config.WaitPolicyHold))
	h.transport.setProbeState("nas", StateDown)
	h.transport.setProbeState("storage", StateDown)
	h.setFact("ups.battery.charge", 90.0)

	done := h.startSequence("outage")

	// Wait for the stage to enter its hold.
	waitFor(t, 3*time.Second, "stage to report itself held", func() bool {
		as := h.exec.ActiveSequence()
		return as != nil && as.HeldStage() == "compute"
	})

	if !h.hasEvent("stage_held") {
		t.Errorf("no stage_held event; events were %v", h.EventTypes())
	}

	// It must still be abortable while holding.
	if err := h.exec.AbortSequence("operator gave up waiting"); err != nil {
		t.Fatalf("AbortSequence while holding: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a held stage ignored the abort and never finished")
	}

	if got := h.transport.CallsFor(ActionShutdown); contains(got, "nas") {
		t.Errorf("a held-then-aborted stage still dispatched shutdown: %v", got)
	}
}

// TestHoldPolicyReleasedByOperator covers the manual intervention SPEC §7.2
// calls for.
func TestHoldPolicyReleasedByOperator(t *testing.T) {
	h := newHarness(t, waitPolicyConfig(config.WaitPolicyHold))
	h.transport.setProbeState("nas", StateDown)
	h.transport.setProbeState("storage", StateDown)
	h.setFact("ups.battery.charge", 90.0)

	done := h.startSequence("outage")

	waitFor(t, 3*time.Second, "stage to report itself held", func() bool {
		as := h.exec.ActiveSequence()
		return as != nil && as.HeldStage() == "compute"
	})

	if err := h.exec.ForceStage("operator confirmed the rack is safe to shut down"); err != nil {
		t.Fatalf("ForceStage: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the held stage was not released by ForceStage")
	}

	if got := h.transport.CallsFor(ActionShutdown); !contains(got, "nas") {
		t.Errorf("the forced stage did not dispatch shutdown: %v", got)
	}
	if !h.hasEvent("stage_forced") {
		t.Errorf("no stage_forced event; events were %v", h.EventTypes())
	}
}

// TestHoldPolicyProceedsWhenConditionFinallyHolds: the normal happy path out
// of a hold is the condition arriving.
func TestHoldPolicyProceedsWhenConditionFinallyHolds(t *testing.T) {
	h := newHarness(t, waitPolicyConfig(config.WaitPolicyHold))
	h.transport.setProbeState("nas", StateDown)
	h.transport.setProbeState("storage", StateDown)
	h.setFact("ups.battery.charge", 90.0)

	done := h.startSequence("outage")

	waitFor(t, 3*time.Second, "stage to report itself held", func() bool {
		as := h.exec.ActiveSequence()
		return as != nil && as.HeldStage() == "compute"
	})

	// The battery finally drops below the threshold.
	h.setFact("ups.battery.charge", 5.0)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the held stage did not proceed once its condition held")
	}

	if got := h.transport.CallsFor(ActionShutdown); !contains(got, "nas") {
		t.Errorf("the released stage did not dispatch shutdown: %v", got)
	}
}

func TestForceStageWithNoActiveSequenceIsAnError(t *testing.T) {
	h := newHarness(t, waitPolicyConfig(config.WaitPolicySkip))

	if err := h.exec.ForceStage("nothing running"); err == nil {
		t.Error("ForceStage succeeded with no sequence running")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func containsInt(haystack []int, needle int) bool {
	for _, n := range haystack {
		if n == needle {
			return true
		}
	}
	return false
}
