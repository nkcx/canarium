package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

// TestWakeIsNotVerifiedByATransportThatCannotProbe is the regression test for
// treating registry membership as probe capability.
//
// A client whose transport is wol or nut had every wake attempt spin to the
// full boot deadline — Probe always returns an error for those — and was then
// marked failed regardless. With the default three retries and a five-minute
// deadline that is fifteen minutes per client, and the client never came out
// of it in a good state.
func TestWakeIsNotVerifiedByATransportThatCannotProbe(t *testing.T) {
	client := testClient("switch-port-device")
	client.Transport = "wol-like"
	client.Address = "" // nothing to fall back to

	plan := testPlan("outage", testStage("s", trueCondition(), "switch-port-device"))
	plan.Wake.BootDeadline = "10s" // long enough that spinning would be obvious
	plan.Wake.Retries = 2

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{plan},
	}

	h := newHarness(t, cfg)

	// A transport that can wake but not probe, like wol and nut.
	wakeOnly := newFakeTransport("wol-like", ActionWake)
	wakeOnly.probeErr = errNoProbe{}
	h.exec.RegisterTransport("wol-like", wakeOnly)

	start := time.Now()
	h.runSequence("outage", 20*time.Second)
	elapsed := time.Since(start)

	if elapsed > 8*time.Second {
		t.Errorf("wake took %v; a transport that cannot probe should not be "+
			"polled to the boot deadline", elapsed)
	}
	if got := h.exec.GetClientState("switch-port-device"); got == StateFailed {
		t.Error("a client was marked failed purely because its transport cannot probe")
	}
}

type errNoProbe struct{}

func (errNoProbe) Error() string { return "this transport does not support probe" }

// TestWakeFallsBackToTCPProbe: when the transport cannot probe but the client
// has an address, a plain TCP check can still confirm it came back.
func TestWakeFallsBackToTCPProbe(t *testing.T) {
	client := testClient("nas")
	client.Transport = "wol-like"
	client.Address = "127.0.0.1"
	client.Probe = &config.ProbeConfig{Method: "tcp", Port: 9}

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{client},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)
	wakeOnly := newFakeTransport("wol-like", ActionWake)
	wakeOnly.probeErr = errNoProbe{}
	h.exec.RegisterTransport("wol-like", wakeOnly)

	probe := h.exec.wakeProbe(&cfg.Clients[0])
	if probe == nil {
		t.Fatal("no fallback probe was provided for a client with an address")
	}
}

func TestWakeProbePrefersACapableTransport(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)

	if probe := h.exec.wakeProbe(&cfg.Clients[0]); probe == nil {
		t.Fatal("a probe-capable transport produced no probe")
	}
}

func TestTransportCan(t *testing.T) {
	probing := newFakeTransport("p", ActionShutdown, ActionProbe)
	wakeOnly := newFakeTransport("w", ActionWake)

	if !transportCan(probing, ActionProbe) {
		t.Error("a probe-capable transport was not recognised")
	}
	if transportCan(wakeOnly, ActionProbe) {
		t.Error("a wake-only transport was reported as probe-capable")
	}
}

// TestGuardPeriodCreditsTimeAlreadySettled is the regression test for the
// cumulative wake delay: the guard period was slept in full for every client
// in turn, so ten clients at the default 60s meant ten minutes of sequential
// sleeping even when every machine had been cold for hours.
func TestGuardPeriodCreditsTimeAlreadySettled(t *testing.T) {
	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{testPlan("outage", testStage("s", trueCondition(), "nas"))},
	}

	h := newHarness(t, cfg)

	// Nothing observed yet: no credit, so the full period applies.
	if got := h.exec.timeSinceSettled("nas"); got != 0 {
		t.Errorf("timeSinceSettled for an unobserved client = %v, want 0", got)
	}

	h.exec.setClientState("nas", StateDownUnverified, nil)
	time.Sleep(20 * time.Millisecond)

	elapsed := h.exec.timeSinceSettled("nas")
	if elapsed < 15*time.Millisecond {
		t.Errorf("timeSinceSettled = %v, want at least the time since the transition", elapsed)
	}

	// A state change restarts the clock.
	h.exec.setClientState("nas", StateUp, nil)
	if got := h.exec.timeSinceSettled("nas"); got > 5*time.Millisecond {
		t.Errorf("timeSinceSettled = %v after a fresh transition, want near zero", got)
	}
}

// TestAbortDuringWakeGateStopsTheSequence: runWake never checked, so the API
// acknowledged an abort and the daemon woke the fleet anyway.
func TestAbortDuringWakeGateStopsTheSequence(t *testing.T) {
	plan := testPlan("outage", testStage("s", trueCondition(), "nas"))
	// A gate that never opens, so the sequence sits there.
	plan.Wake.Gate = config.ConditionConfig{
		Condition: "numeric", Fact: "ups.battery.charge", Above: floatPtr(200),
	}

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("nas")},
		Plans:    []config.PlanConfig{plan},
	}

	h := newHarness(t, cfg)
	h.transport.setProbeState("nas", StateDown)
	h.setFact("ups.battery.charge", 50.0)

	done := h.startSequence("outage")

	waitFor(t, 5*time.Second, "sequence to reach the wake gate", func() bool {
		as := h.exec.ActiveSequence()
		return as != nil && as.State() == SeqStateWakeGate
	})

	if err := h.exec.AbortSequence("operator stopped the wake"); err != nil {
		t.Fatalf("AbortSequence: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sequence ignored an abort at the wake gate and kept waiting")
	}

	if contains(h.transport.CallsFor(ActionWake), "nas") {
		t.Error("a client was woken after the sequence was aborted")
	}
}
