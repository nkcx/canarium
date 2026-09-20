package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// TestInconclusiveProbeDoesNotMarkClientDown is the regression test for the
// central design flaw: every transport reported a dial failure as
// StateDown, so a network partition to a running host was recorded as a
// completed shutdown and the sequence moved on.
func TestInconclusiveProbeDoesNotMarkClientDown(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)

	// Establish a known state: the host is up.
	h.transport.setProbeState("nas", StateUp)
	h.exec.probeAllClients()
	if got := h.exec.GetClientState("nas"); got != StateUp {
		t.Fatalf("client state = %v, want up", got)
	}

	// Now the network breaks: the probe cannot establish anything.
	h.transport.mu.Lock()
	h.transport.probeErr = errors.New("dial tcp 10.0.0.1:22: i/o timeout")
	h.transport.mu.Unlock()

	h.exec.probeAllClients()

	if got := h.exec.GetClientState("nas"); got == StateDown {
		t.Error("an inconclusive probe marked the client down; " +
			"a network partition would read as a completed shutdown")
	}
	if got := h.exec.GetClientState("nas"); got != StateUp {
		t.Errorf("client state = %v after an inconclusive probe, want it left at up", got)
	}
}

// TestShutdownWithInconclusiveProbeEndsUnverified: if we cannot confirm a
// host went down, the sequence must say so rather than claiming success.
func TestShutdownWithInconclusiveProbeEndsUnverified(t *testing.T) {
	client := testClient("nas")
	client.ShutdownBudget = "80ms"

	stage := testStage("s", trueCondition(), "nas")
	stage.Budget = "150ms"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", stage)},
	}

	h := newHarness(t, cfg)
	h.transport.mu.Lock()
	h.transport.probeErr = errors.New("dial tcp: i/o timeout")
	h.transport.mu.Unlock()

	h.runSequence("outage", 10*time.Second)

	if got := h.exec.GetClientState("nas"); got != StateDownUnverified && got != StateFailed {
		t.Errorf("client state = %v, want down_unverified — the shutdown was never confirmed", got)
	}
}

// TestShutdownWithConfirmedDownProbeIsVerified is the positive case.
func TestShutdownWithConfirmedDownProbeIsVerified(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	h.transport.setProbeState("nas", StateDown)

	done := h.startSequence("outage")

	waitFor(t, 3*time.Second, "client to be confirmed down", func() bool {
		return h.exec.GetClientState("nas") == StateDown ||
			h.exec.GetClientState("nas") == StateWaking ||
			h.exec.GetClientState("nas") == StateUp
	})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sequence did not finish")
	}
}
