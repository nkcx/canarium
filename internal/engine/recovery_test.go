package engine

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/state"
)

// recoveryConfig builds a three-stage plan so a resume has stages left to run.
func recoveryConfig() *config.Config {
	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients: []config.ClientConfig{
			testClient("compute1"), testClient("storage1"), testClient("firewall1"),
		},
		Plans: []config.PlanConfig{testPlan("outage",
			testStage("compute", trueCondition(), "compute1"),
			testStage("storage", trueCondition(), "storage1"),
			testStage("infrastructure", trueCondition(), "firewall1"),
		)},
	}
}

// seedInterrupted writes a sequence to the database as if the daemon had died
// mid-flight, with stage 0 recorded complete.
func seedInterrupted(t *testing.T, h *harness, seqState string, completedStages int) *state.Sequence {
	t.Helper()

	seq := &state.Sequence{
		ID:           "seq-interrupted",
		PlanName:     "outage",
		State:        seqState,
		CurrentStage: completedStages,
		StartedAt:    time.Now().Add(-10 * time.Minute),
		PreSequenceState: map[string]string{
			"compute1": "up", "storage1": "up", "firewall1": "up",
		},
	}
	if err := h.db.SaveSequence(t.Context(), seq); err != nil {
		t.Fatalf("SaveSequence: %v", err)
	}

	for i := 0; i < completedStages; i++ {
		done := time.Now().Add(-9 * time.Minute)
		if err := h.db.SaveStageRecord(t.Context(), &state.StageRecord{
			SequenceID: seq.ID, StageIndex: i, StageName: "stage",
			StartedAt: done, CompletedAt: &done,
		}); err != nil {
			t.Fatalf("SaveStageRecord: %v", err)
		}
	}

	return seq
}

// TestResumeDoesNotResurrectAnAbortedSequence is the regression test for the
// worst crash-recovery defect: resumeSequence ignored the persisted state and
// always ran the shutdown stages. A sequence aborted during stage 0 has no
// records for stages 1..N, so resume computed "start at stage 1" and shut
// down every host the operator had just explicitly spared.
func TestResumeDoesNotResurrectAnAbortedSequence(t *testing.T) {
	h := newHarness(t, recoveryConfig())
	h.transport.setProbeState("compute1", StateDown)
	h.transport.setProbeState("storage1", StateUp)
	h.transport.setProbeState("firewall1", StateUp)

	seq := seedInterrupted(t, h, SeqStateAborting, 1)
	seq.Aborted = true

	as := newActiveSequence(seq, &h.cfg.Plans[0])
	if !h.exec.setActiveSequence(as) {
		t.Fatal("could not claim the sequence")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.exec.resumeSequence(as)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("resume did not finish")
	}

	for _, client := range []string{"storage1", "firewall1"} {
		if contains(h.transport.CallsFor(ActionShutdown), client) {
			t.Errorf("resuming an aborted sequence shut down %q — "+
				"a host the operator had explicitly spared", client)
		}
	}
}

// TestResumeAtWakeGateDoesNotRerunPostShutdown is the regression test for the
// second crash-recovery defect: a sequence past its shutdown stages fell
// through the empty stage loop straight into executePostShutdown, telling the
// UPS to cut its outlets a second time while the fleet was booting on
// returning mains.
func TestResumeAtWakeGateDoesNotRerunPostShutdown(t *testing.T) {
	cfg := recoveryConfig()
	cfg.Plans[0].Shutdown.PostShutdown = &config.PostShutdownConfig{
		Action: "upscmd", Command: "shutdown.return", UPS: "rack_ups", Host: "127.0.0.1",
	}

	h := newHarness(t, cfg)

	// A NUT transport that records whether it was asked to cut power.
	nut := newFakeTransport("nut", ActionOutletOff, ActionOutletOn)
	h.exec.RegisterTransport("nut", nut)

	seq := seedInterrupted(t, h, SeqStateWakeGate, 3)
	seq.PostShutdownRun = true

	as := newActiveSequence(seq, &h.cfg.Plans[0])
	if !h.exec.setActiveSequence(as) {
		t.Fatal("could not claim the sequence")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.exec.resumeSequence(as)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("resume did not finish")
	}

	if calls := nut.Calls(); len(calls) != 0 {
		t.Errorf("resuming at the wake gate re-issued the post-shutdown action: %+v; "+
			"this cuts UPS outlets to a fleet that is already booting", calls)
	}
	if contains(h.transport.CallsFor(ActionShutdown), "compute1") {
		t.Error("resuming at the wake gate re-ran the shutdown stages")
	}
}

// TestPostShutdownRunsExactlyOnce covers the flag directly.
func TestPostShutdownRunsExactlyOnce(t *testing.T) {
	seq := &state.Sequence{ID: "s", PlanName: "p", StartedAt: time.Now()}
	as := newActiveSequence(seq, &config.PlanConfig{})

	if !as.MarkPostShutdownRun() {
		t.Fatal("the first claim was refused")
	}
	if as.MarkPostShutdownRun() {
		t.Error("a second claim succeeded; post-shutdown would run twice")
	}
	if !seq.PostShutdownRun {
		t.Error("the flag was not recorded on the sequence for persistence")
	}
}

// TestAbortedSequenceIsRecordedAsAborted: an aborted run previously finished
// as "completed", so the journal claimed a clean shutdown for a sequence the
// operator had called off.
func TestAbortedSequenceIsRecordedAsAborted(t *testing.T) {
	stage := testStage("compute", chargeBelow(10), "compute1")
	stage.WaitTimeout = "10s"

	cfg := &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Clients:  []config.ClientConfig{testClient("compute1")},
		Plans:    []config.PlanConfig{testPlan("outage", stage)},
	}

	h := newHarness(t, cfg)
	h.setFact("ups.battery.charge", 90.0)

	done := h.startSequence("outage")

	waitFor(t, 3*time.Second, "sequence to become active", func() bool {
		return h.exec.ActiveSequence() != nil
	})

	if err := h.exec.AbortSequence("operator called it off"); err != nil {
		t.Fatalf("AbortSequence: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sequence did not finish")
	}

	seq, err := h.db.LastSequence(t.Context())
	if err != nil {
		t.Fatalf("LastSequence: %v", err)
	}
	if seq.State != SeqStateAborted {
		t.Errorf("terminal state = %q, want %q — the journal reports a clean run "+
			"for a sequence that was aborted", seq.State, SeqStateAborted)
	}
	if !seq.Aborted {
		t.Error("the aborted flag was not persisted")
	}
}

// TestOrphanedSequenceReleasesItsLocks is the regression test for permanent
// lockout: a sequence whose plan was renamed during a restart could not be
// resumed, stayed in the table, and held its client locks forever. Because
// acquisition is re-entrant only for the holding sequence, no future sequence
// could ever act on those clients again.
func TestOrphanedSequenceReleasesItsLocks(t *testing.T) {
	h := newHarness(t, recoveryConfig())
	ctx := t.Context()

	seq := seedInterrupted(t, h, SeqStateShuttingDown, 1)

	for _, client := range []string{"compute1", "storage1"} {
		ok, err := h.db.AcquireClientLock(ctx, client, seq.ID)
		if err != nil || !ok {
			t.Fatalf("AcquireClientLock(%s): ok=%v err=%v", client, ok, err)
		}
	}

	// The plan is gone from the configuration.
	h.cfg.Plans = nil

	if err := h.exec.restoreState(); err != nil {
		t.Fatalf("restoreState: %v", err)
	}

	for _, client := range []string{"compute1", "storage1"} {
		holder, err := h.db.ClientLockHolder(ctx, client)
		if err != nil {
			t.Fatalf("ClientLockHolder: %v", err)
		}
		if holder != "" {
			t.Errorf("%s is still locked by %q; no future sequence could act on it", client, holder)
		}
	}

	recovered, err := h.db.GetActiveSequence(ctx)
	if err != nil {
		t.Fatalf("GetActiveSequence: %v", err)
	}
	if recovered != nil {
		t.Errorf("the orphaned sequence is still active: %+v", recovered)
	}
}

// TestAbortedSequenceIsNotRecoveredOnRestart: a terminal sequence must not be
// picked up again.
func TestAbortedSequenceIsNotRecoveredOnRestart(t *testing.T) {
	h := newHarness(t, recoveryConfig())

	seedInterrupted(t, h, SeqStateAborted, 1)

	recovered, err := h.db.GetActiveSequence(t.Context())
	if err != nil {
		t.Fatalf("GetActiveSequence: %v", err)
	}
	if recovered != nil {
		t.Errorf("an aborted sequence was recovered as active: %+v", recovered)
	}
}

// TestPendingIntentsAreReconciled covers SPEC §8.4: the intents table was
// written since the first commit and never read, so an action in flight when
// the daemon died left no trace in recovery.
func TestPendingIntentsAreReconciled(t *testing.T) {
	h := newHarness(t, recoveryConfig())
	ctx := t.Context()

	seq := seedInterrupted(t, h, SeqStateShuttingDown, 1)

	if err := h.db.SaveIntent(ctx, &state.Intent{
		ID:         "intent-1",
		SequenceID: seq.ID,
		ClientName: "storage1",
		Action:     "shutdown",
		Timestamp:  time.Now().Add(-5 * time.Minute),
		Status:     state.IntentDispatching,
	}); err != nil {
		t.Fatalf("SaveIntent: %v", err)
	}

	pending, err := h.db.PendingIntents(ctx, seq.ID)
	if err != nil {
		t.Fatalf("PendingIntents: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("found %d pending intents, want 1", len(pending))
	}

	// The host turns out to be down: the command did land before the crash.
	h.transport.setProbeState("storage1", StateDown)

	as := newActiveSequence(seq, &h.cfg.Plans[0])
	h.exec.reconcileIntents(as)

	if got := h.exec.GetClientState("storage1"); got != StateDown {
		t.Errorf("client state = %v after reconciliation, want down", got)
	}

	stillPending, err := h.db.PendingIntents(ctx, seq.ID)
	if err != nil {
		t.Fatalf("PendingIntents: %v", err)
	}
	if len(stillPending) != 0 {
		t.Errorf("%d intents are still pending after reconciliation", len(stillPending))
	}
}
