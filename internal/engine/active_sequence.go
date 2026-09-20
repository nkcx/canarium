package engine

import (
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/state"
)

// Sequence states, as persisted in the sequences table.
const (
	SeqStateShuttingDown = "shutting_down"
	SeqStateAborting     = "aborting"
	SeqStateAborted      = "aborted"
	SeqStateWakeGate     = "wake_gate"
	SeqStateWaking       = "waking"
	SeqStateCompleted    = "completed"
	SeqStateFailed       = "failed"
)

// ActiveSequence is the executor's view of the sequence currently running.
//
// Every field is guarded. The executor goroutine mutates stage progress and
// the PONR flag while HTTP handlers read them to render /api/status, and the
// previous implementation shared a bare *state.Sequence between the two with
// no synchronisation at all — a data race on the value that decides whether
// a shutdown can still be called off.
//
// Callers outside this file must go through the accessors; the embedded
// *state.Sequence is never exposed.
type ActiveSequence struct {
	mu   sync.RWMutex
	seq  *state.Sequence
	plan *config.PlanConfig

	// abortRequested records an operator-initiated abort. It is separate
	// from the plan's automatic abort condition: an operator may call off a
	// sequence whose abort condition has not fired.
	abortRequested bool
	abortReason    string

	// proceedRequested lets an operator release a stage that is holding on
	// an entry condition which will not arrive. SPEC §7.2 specifies that
	// wait_policy: hold "requires manual intervention"; this is it.
	proceedRequested bool
	proceedReason    string

	// heldStage names the stage currently holding, for the status API.
	heldStage string
}

// RequestProceed releases a stage that is holding on its entry condition.
func (a *ActiveSequence) RequestProceed(reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.proceedRequested = true
	a.proceedReason = reason
}

// ConsumeProceed reports whether an operator has asked the current stage to
// proceed, clearing the request so it applies to one stage only.
func (a *ActiveSequence) ConsumeProceed() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.proceedRequested {
		return false, ""
	}
	reason := a.proceedReason
	a.proceedRequested = false
	a.proceedReason = ""
	return true, reason
}

// SetHeldStage records which stage is waiting past its timeout, or clears it
// when passed the empty string.
func (a *ActiveSequence) SetHeldStage(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.heldStage = name
}

// HeldStage returns the stage currently holding past its wait timeout.
func (a *ActiveSequence) HeldStage() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.heldStage
}

func newActiveSequence(seq *state.Sequence, plan *config.PlanConfig) *ActiveSequence {
	return &ActiveSequence{seq: seq, plan: plan}
}

// Plan returns the plan being executed.
//
// The configuration is immutable for the daemon's lifetime, so the pointer
// may be read without further synchronisation.
func (a *ActiveSequence) Plan() *config.PlanConfig { return a.plan }

func (a *ActiveSequence) ID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.seq.ID
}

func (a *ActiveSequence) PlanName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.seq.PlanName
}

func (a *ActiveSequence) CurrentStage() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.seq.CurrentStage
}

func (a *ActiveSequence) SetCurrentStage(i int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq.CurrentStage = i
}

func (a *ActiveSequence) State() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.seq.State
}

func (a *ActiveSequence) SetState(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq.State = s
}

// MarkCompleted records the terminal state and completion time together.
func (a *ActiveSequence) MarkCompleted(s string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq.State = s
	a.seq.CompletedAt = &at
}

func (a *ActiveSequence) PonrCrossed() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.seq.PonrCrossed
}

// CrossPonr marks the point of no return. It reports whether this call was
// the one that crossed it, so the caller emits exactly one event.
func (a *ActiveSequence) CrossPonr() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seq.PonrCrossed {
		return false
	}
	a.seq.PonrCrossed = true
	return true
}

// ResolvedAddr returns the address pinned for a client when the sequence
// started.
func (a *ActiveSequence) ResolvedAddr(client string) (state.ResolvedAddr, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	addr, ok := a.seq.ResolvedAddrs[client]
	return addr, ok
}

// PreSequenceState returns a client's state as recorded when the sequence
// began, used by the retain_state wake policy.
func (a *ActiveSequence) PreSequenceState(client string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.seq.PreSequenceState[client]
	return s, ok
}

// RequestAbort records an operator-initiated abort request.
//
// Returns false if the sequence has already passed the point of no return,
// where aborting is by definition not possible: hosts are already shutting
// down and stopping half way would leave the fleet in an unknown state.
func (a *ActiveSequence) RequestAbort(reason string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.seq.PonrCrossed {
		return false
	}

	a.abortRequested = true
	a.abortReason = reason
	return true
}

// AbortRequested reports whether an operator has asked to abort.
func (a *ActiveSequence) AbortRequested() (bool, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.abortRequested, a.abortReason
}

// persistable returns a copy safe to hand to the state layer.
//
// The state layer must never receive the live struct: it would read fields
// concurrently with the executor writing them.
func (a *ActiveSequence) persistable() *state.Sequence {
	a.mu.RLock()
	defer a.mu.RUnlock()

	cp := *a.seq
	cp.PreSequenceState = make(map[string]string, len(a.seq.PreSequenceState))
	for k, v := range a.seq.PreSequenceState {
		cp.PreSequenceState[k] = v
	}
	cp.ResolvedAddrs = make(map[string]state.ResolvedAddr, len(a.seq.ResolvedAddrs))
	for k, v := range a.seq.ResolvedAddrs {
		cp.ResolvedAddrs[k] = v
	}
	if a.seq.CompletedAt != nil {
		completed := *a.seq.CompletedAt
		cp.CompletedAt = &completed
	}
	return &cp
}

// SequenceSnapshot is an immutable view of a sequence for the API.
type SequenceSnapshot struct {
	ID             string     `json:"id"`
	Plan           string     `json:"plan"`
	State          string     `json:"state"`
	CurrentStage   int        `json:"current_stage"`
	StageName      string     `json:"stage_name,omitempty"`
	TotalStages    int        `json:"total_stages"`
	PonrCrossed    bool       `json:"ponr_crossed"`
	Abortable      bool       `json:"abortable"`
	AbortRequested bool       `json:"abort_requested"`
	AbortReason    string     `json:"abort_reason,omitempty"`
	HeldStage      string     `json:"held_stage,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// Snapshot returns a consistent view of the sequence taken under one lock
// acquisition, so the API never renders a half-updated state.
func (a *ActiveSequence) Snapshot() SequenceSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	snap := SequenceSnapshot{
		ID:             a.seq.ID,
		Plan:           a.seq.PlanName,
		State:          a.seq.State,
		CurrentStage:   a.seq.CurrentStage,
		TotalStages:    len(a.plan.Shutdown.Stages),
		PonrCrossed:    a.seq.PonrCrossed,
		Abortable:      !a.seq.PonrCrossed,
		AbortRequested: a.abortRequested,
		AbortReason:    a.abortReason,
		HeldStage:      a.heldStage,
		StartedAt:      a.seq.StartedAt,
	}

	if a.seq.CurrentStage >= 0 && a.seq.CurrentStage < len(a.plan.Shutdown.Stages) {
		snap.StageName = a.plan.Shutdown.Stages[a.seq.CurrentStage].Name
	}
	if a.seq.CompletedAt != nil {
		completed := *a.seq.CompletedAt
		snap.CompletedAt = &completed
	}

	return snap
}
