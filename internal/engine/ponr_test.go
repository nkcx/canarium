package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// ponrConfig builds a plan whose only stage is marked point_of_no_return and
// gated on the battery falling below 30 — the shape of the shipped example
// config, where PONR sits on the first stage.
func ponrConfig() *config.Config {
	stage := testStage("compute", chargeBelow(30), "nas")
	stage.PointOfNoReturn = true

	plan := testPlan("outage", stage)
	plan.Abort = &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Above:     floatPtr(80),
	}
	// Long enough that the test controls when the stage proceeds.
	plan.Shutdown.Stages[0].WaitTimeout = "10s"

	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{plan},
	}
}

// TestPonrIsNotCrossedWhileWaitingForStageCondition is the regression test
// for the defect that disabled abort entirely on the shipped example config.
//
// PONR used to be crossed when the loop *reached* a stage, before its entry
// condition was even evaluated. With point_of_no_return on the first stage,
// abort was therefore disabled the instant the plan triggered — so mains
// power returning a second later would not stop the shutdown, defeating the
// "abort and resume" feature the README leads with.
func TestPonrIsNotCrossedWhileWaitingForStageCondition(t *testing.T) {
	cfg := ponrConfig()
	h := newHarness(t, cfg)

	// Battery at 50: above the stage's threshold of 30, so the stage waits.
	h.setFact("ups.battery.charge", 50.0)

	done := h.startSequence("outage")

	// Give the stage loop time to reach the wait.
	waitFor(t, 2*time.Second, "sequence to become active", func() bool {
		return h.exec.ActiveSequence() != nil
	})
	time.Sleep(50 * time.Millisecond)

	as := h.exec.ActiveSequence()
	if as == nil {
		t.Fatal("no active sequence")
	}
	if as.PonrCrossed() {
		t.Fatal("PONR was crossed while still waiting for the stage's entry condition; " +
			"abort is disabled for the whole wait, which defaults to an hour")
	}
	if !as.Snapshot().Abortable {
		t.Error("sequence reports itself unabortable while waiting")
	}

	// Mains power returns: the abort condition fires.
	h.setFact("ups.battery.charge", 95.0)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sequence did not finish after the abort condition fired")
	}

	if !h.hasEvent("abort") {
		t.Errorf("no abort event was emitted; events were %v", h.EventTypes())
	}
	if got := h.transport.CallsFor(ActionShutdown); len(got) != 0 {
		t.Errorf("shutdown was dispatched to %v despite the abort", got)
	}
}

// TestPonrIsCrossedWhenStageDispatches verifies the flag still does its job:
// once a PONR stage actually begins, abort must be ignored.
func TestPonrIsCrossedWhenStageDispatches(t *testing.T) {
	cfg := ponrConfig()
	h := newHarness(t, cfg)

	// Hold the shutdown open so the test can observe the crossed state.
	release := make(chan struct{})
	h.transport.onExecute = func(client *Client, action ActionType) {
		if action == ActionShutdown {
			<-release
		}
	}
	h.transport.setProbeState("nas", StateDown)

	// Battery below the stage threshold: the stage proceeds immediately.
	h.setFact("ups.battery.charge", 20.0)

	done := h.startSequence("outage")

	waitFor(t, 3*time.Second, "shutdown to be dispatched", func() bool {
		return len(h.transport.CallsFor(ActionShutdown)) > 0
	})

	as := h.exec.ActiveSequence()
	if as == nil {
		t.Fatal("no active sequence")
	}
	if !as.PonrCrossed() {
		t.Error("PONR was not crossed when the stage dispatched")
	}
	if as.Snapshot().Abortable {
		t.Error("sequence reports itself abortable after crossing PONR")
	}

	// An abort request past PONR must be refused.
	if err := h.exec.AbortSequence("operator changed their mind"); err == nil {
		t.Error("AbortSequence succeeded after the point of no return")
	}

	close(release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sequence did not finish")
	}

	if !h.hasEvent("ponr_crossed") {
		t.Errorf("no ponr_crossed event; events were %v", h.EventTypes())
	}
}

// TestSkippedPonrStageLeavesSequenceAbortable: a stage whose entry condition
// never held was never dispatched, so it must not have burned the PONR.
func TestSkippedPonrStageLeavesSequenceAbortable(t *testing.T) {
	stage := testStage("compute", chargeBelow(10), "nas")
	stage.PointOfNoReturn = true
	stage.WaitTimeout = "20ms"
	stage.WaitPolicy = "skip"

	second := testStage("storage", trueCondition(), "storage")

	plan := testPlan("outage", stage, second)

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas"), testClient("storage")},
		Plans:    []config.PlanConfig{plan},
	}

	h := newHarness(t, cfg)
	h.transport.setProbeState("nas", StateDown)
	h.transport.setProbeState("storage", StateDown)

	// Charge stays well above the first stage's threshold, so it times out.
	h.setFact("ups.battery.charge", 90.0)

	var crossedDuringSecondStage bool
	h.transport.onExecute = func(client *Client, action ActionType) {
		if action == ActionShutdown && client.Name == "storage" {
			if as := h.exec.ActiveSequence(); as != nil {
				crossedDuringSecondStage = as.PonrCrossed()
			}
		}
	}

	h.runSequence("outage", 10*time.Second)

	if crossedDuringSecondStage {
		t.Error("PONR was crossed by a stage that was skipped and never dispatched")
	}
	if h.hasEvent("ponr_crossed") {
		t.Error("a skipped PONR stage emitted ponr_crossed")
	}
}

// TestOperatorAbortStopsSequence covers the abort endpoint's real effect:
// the previous AbortSequence set a field nothing read, so the API reported
// success while the shutdown continued.
func TestOperatorAbortStopsSequence(t *testing.T) {
	stage := testStage("compute", chargeBelow(10), "nas")
	stage.WaitTimeout = "10s"

	plan := testPlan("outage", stage)

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{plan},
	}

	h := newHarness(t, cfg)
	// Charge above the threshold: the stage waits, giving us a window.
	h.setFact("ups.battery.charge", 90.0)

	done := h.startSequence("outage")

	waitFor(t, 2*time.Second, "sequence to become active", func() bool {
		return h.exec.ActiveSequence() != nil
	})

	if err := h.exec.AbortSequence("operator pressed abort"); err != nil {
		t.Fatalf("AbortSequence: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sequence ignored the operator abort and kept running")
	}

	if !h.hasEvent("abort") {
		t.Errorf("no abort event; events were %v", h.EventTypes())
	}
	if got := h.transport.CallsFor(ActionShutdown); len(got) != 0 {
		t.Errorf("shutdown dispatched to %v after an abort", got)
	}
}

// TestAbortWithNoActiveSequenceIsAnError guards the API contract.
func TestAbortWithNoActiveSequenceIsAnError(t *testing.T) {
	cfg := ponrConfig()
	h := newHarness(t, cfg)

	if err := h.exec.AbortSequence("nothing running"); err == nil {
		t.Error("AbortSequence succeeded with no sequence running")
	}
}
