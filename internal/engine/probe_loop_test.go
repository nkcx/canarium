package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

func probeLoopConfig() *config.Config {
	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}
}

// TestProbeLoopNoticesAHostGoingAway is the regression test for a background
// probe that could only ever move a client out of unknown or unverified. A
// host that died out-of-band — a failed PSU, an engineer pulling it — stayed
// recorded as "up" forever.
func TestProbeLoopNoticesAHostGoingAway(t *testing.T) {
	h := newHarness(t, probeLoopConfig())

	h.transport.setProbeState("nas", StateUp)
	h.exec.probeAllClients()
	if got := h.exec.GetClientState("nas"); got != StateUp {
		t.Fatalf("client state = %v, want up", got)
	}

	// The machine dies out-of-band.
	h.transport.setProbeState("nas", StateDown)
	h.exec.probeAllClients()

	if got := h.exec.GetClientState("nas"); got != StateDown {
		t.Errorf("client state = %v after the host went away, want down — "+
			"the probe loop cannot track reality", got)
	}
}

// TestProbeLoopNoticesAHostComingBack is the same defect in the other
// direction: a machine an engineer powered back on stayed "down".
func TestProbeLoopNoticesAHostComingBack(t *testing.T) {
	h := newHarness(t, probeLoopConfig())

	h.transport.setProbeState("nas", StateDown)
	h.exec.probeAllClients()
	if got := h.exec.GetClientState("nas"); got != StateDown {
		t.Fatalf("client state = %v, want down", got)
	}

	h.transport.setProbeState("nas", StateUp)
	h.exec.probeAllClients()

	if got := h.exec.GetClientState("nas"); got != StateUp {
		t.Errorf("client state = %v after the host came back, want up", got)
	}
}

// TestProbeLoopDoesNotUnwindASequenceInFlight: while a sequence is driving a
// client, a host mid-shutdown that still answers is not "up".
func TestProbeLoopDoesNotUnwindASequenceInFlight(t *testing.T) {
	h := newHarness(t, probeLoopConfig())

	h.exec.setClientState("nas", StateShuttingDown, nil)
	h.transport.setProbeState("nas", StateUp)

	h.exec.probeAllClients()

	if got := h.exec.GetClientState("nas"); got != StateShuttingDown {
		t.Errorf("client state = %v, want shutting_down — a host that is on its "+
			"way down must not be reported as up", got)
	}
}

func TestProbeLoopConfirmsShutdownCompletion(t *testing.T) {
	h := newHarness(t, probeLoopConfig())

	h.exec.setClientState("nas", StateShuttingDown, nil)
	h.transport.setProbeState("nas", StateDown)

	h.exec.probeAllClients()

	if got := h.exec.GetClientState("nas"); got != StateDown {
		t.Errorf("client state = %v, want down", got)
	}
}

// TestModeIsRestoredAcrossRestart is the regression test for a mode that was
// written to the database on every change and never read back: an operator
// who armed Canarium through the UI and then rebooted came back disarmed,
// with nothing to say so.
func TestModeIsRestoredAcrossRestart(t *testing.T) {
	cfg := probeLoopConfig()
	cfg.Canarium.Mode = "disarmed"

	h := newHarness(t, cfg)

	// The operator arms it at runtime, as handleSetMode does.
	if err := h.db.SetKV(t.Context(), modeKey, "armed"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}

	h.exec.restoreMode()

	if got := h.exec.Mode(); got != ModeArmed {
		t.Errorf("mode after restart = %v, want armed — the fleet would be "+
			"unprotected for the next outage", got)
	}
}

func TestConfigReadonlyIgnoresThePersistedMode(t *testing.T) {
	cfg := probeLoopConfig()
	cfg.Canarium.Mode = "disarmed"
	cfg.Canarium.ConfigReadonly = true

	h := newHarness(t, cfg)

	if err := h.db.SetKV(t.Context(), modeKey, "armed"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}

	h.exec.restoreMode()

	if got := h.exec.Mode(); got != ModeDisarmed {
		t.Errorf("mode = %v with config_readonly set, want the file's disarmed", got)
	}
}

func TestUnrecognisedPersistedModeFallsBack(t *testing.T) {
	cfg := probeLoopConfig()
	cfg.Canarium.Mode = "dry-run"

	h := newHarness(t, cfg)

	if err := h.db.SetKV(t.Context(), modeKey, "nonsense"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}

	h.exec.restoreMode()

	if got := h.exec.Mode(); got != ModeDryRun {
		t.Errorf("mode = %v, want the configured dry-run", got)
	}
}

// TestDuplicateTriggersAreNotEmitted is the regression test for claiming the
// active-sequence slot after pinning addresses: during the DNS lookups
// ActiveSequence() stayed nil, so every policy tick logged another trigger,
// notified every webhook again, and spawned another goroutine.
func TestDuplicateTriggersAreNotEmitted(t *testing.T) {
	h := newHarness(t, probeLoopConfig())
	h.transport.setProbeState("nas", StateDown)

	h.exec.SetMode(ModeArmed)

	// Several ticks in quick succession, as the policy loop would produce
	// while a sequence is starting up.
	for i := 0; i < 5; i++ {
		h.exec.evaluatePolicies()
	}

	var triggers int
	for _, e := range h.Events() {
		if e.Type == "trigger" {
			triggers++
		}
	}

	if triggers > 1 {
		t.Errorf("emitted %d trigger events for one outage; every webhook would "+
			"have been notified that many times", triggers)
	}

	waitFor(t, 5*time.Second, "the sequence to finish", func() bool {
		return h.exec.ActiveSequence() == nil
	})
}
