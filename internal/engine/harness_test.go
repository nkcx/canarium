package engine

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

// fakeTransport records every call and returns scripted results.
//
// Probe returns whatever state the test last set for the client, so a test
// can simulate a host going down, staying up, or being unreachable.
type fakeTransport struct {
	mu sync.Mutex

	name  string
	caps  []Capability
	calls []transportCall

	// probeStates maps client name to the state Probe should report.
	probeStates map[string]ClientState

	// probeErr, if set, is returned by every Probe.
	probeErr error

	// execErr, if set, is returned by every Execute.
	execErr error

	// onExecute, if set, runs before Execute returns.
	onExecute func(client *Client, action ActionType)
}

type transportCall struct {
	Client string
	Action ActionType
	At     time.Time
}

func newFakeTransport(name string, actions ...ActionType) *fakeTransport {
	caps := make([]Capability, 0, len(actions))
	for _, a := range actions {
		caps = append(caps, Capability{Action: a, Idempotent: true, Timeout: time.Second})
	}
	return &fakeTransport{
		name:        name,
		caps:        caps,
		probeStates: make(map[string]ClientState),
	}
}

func (f *fakeTransport) Name() string               { return f.name }
func (f *fakeTransport) Capabilities() []Capability { return f.caps }

func (f *fakeTransport) Execute(ctx context.Context, client *Client, action ActionType) (*ActionResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, transportCall{Client: client.Name, Action: action, At: time.Now()})
	onExecute := f.onExecute
	err := f.execErr
	f.mu.Unlock()

	if onExecute != nil {
		onExecute(client, action)
	}
	if err != nil {
		return nil, err
	}
	return &ActionResult{Success: true, Message: "ok"}, nil
}

func (f *fakeTransport) Probe(ctx context.Context, client *Client) (ClientState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.probeErr != nil {
		return StateUnknown, f.probeErr
	}
	if s, ok := f.probeStates[client.Name]; ok {
		return s, nil
	}
	return StateUp, nil
}

func (f *fakeTransport) setProbeState(client string, s ClientState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probeStates[client] = s
}

// Calls returns a copy of the recorded calls.
func (f *fakeTransport) Calls() []transportCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]transportCall(nil), f.calls...)
}

// CallsFor returns the client names called with the given action, in order.
func (f *fakeTransport) CallsFor(action ActionType) []string {
	var out []string
	for _, c := range f.Calls() {
		if c.Action == action {
			out = append(out, c.Client)
		}
	}
	return out
}

// harness wires up an executor over a real database and fact store.
type harness struct {
	t         *testing.T
	exec      *Executor
	store     *facts.Store
	db        *state.DB
	cfg       *config.Config
	transport *fakeTransport

	eventsMu sync.Mutex
	events   []Event
}

// testTimings compresses every interval so sequence tests finish quickly.
func testTimings() Timings {
	return Timings{
		Probe:          time.Hour, // driven manually
		Policy:         time.Hour,
		QualityRefresh: time.Hour,
		StagePoll:      5 * time.Millisecond,
		ShutdownProbe:  5 * time.Millisecond,
		WakeGatePoll:   5 * time.Millisecond,
		DryRunStep:     time.Millisecond,
	}
}

func newHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()

	db, err := state.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("opening state database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := facts.NewStore()
	store.RegisterSource("ups", time.Second, []facts.FactDeclaration{
		{Name: "battery.charge", Type: "percent"},
		{Name: "status", Type: "set"},
	})

	evaluator := conditions.NewEvaluator(store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	exec := NewExecutor(cfg, store, evaluator, db, logger)
	exec.SetTimings(testTimings())

	transport := newFakeTransport("fake", ActionShutdown, ActionWake, ActionProbe)
	exec.RegisterTransport("fake", transport)

	h := &harness{
		t:         t,
		exec:      exec,
		store:     store,
		db:        db,
		cfg:       cfg,
		transport: transport,
	}

	exec.AddListener(func(e Event) {
		h.eventsMu.Lock()
		h.events = append(h.events, e)
		h.eventsMu.Unlock()
	})

	t.Cleanup(exec.Stop)
	return h
}

// setFact updates a fact and refreshes quality so it reads as good.
func (h *harness) setFact(key string, value any) {
	now := time.Now()
	h.store.Update(key, value, now)
	h.store.RefreshQuality(now)
}

// Events returns a copy of the emitted events.
func (h *harness) Events() []Event {
	h.eventsMu.Lock()
	defer h.eventsMu.Unlock()
	return append([]Event(nil), h.events...)
}

// EventTypes returns the emitted event types in order.
func (h *harness) EventTypes() []string {
	var out []string
	for _, e := range h.Events() {
		out = append(out, e.Type)
	}
	return out
}

// hasEvent reports whether an event of the given type was emitted.
func (h *harness) hasEvent(typ string) bool {
	for _, e := range h.Events() {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// runSequence executes the named plan to completion, failing on timeout.
func (h *harness) runSequence(planName string, timeout time.Duration) {
	h.t.Helper()

	var plan *config.PlanConfig
	for i := range h.cfg.Plans {
		if h.cfg.Plans[i].Name == planName {
			plan = &h.cfg.Plans[i]
		}
	}
	if plan == nil {
		h.t.Fatalf("plan %q not found", planName)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.exec.executeSequence(plan)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		h.t.Fatalf("sequence %q did not finish within %v", planName, timeout)
	}
}

// startSequence begins a plan and returns a channel closed when it finishes.
func (h *harness) startSequence(planName string) <-chan struct{} {
	h.t.Helper()

	var plan *config.PlanConfig
	for i := range h.cfg.Plans {
		if h.cfg.Plans[i].Name == planName {
			plan = &h.cfg.Plans[i]
		}
	}
	if plan == nil {
		h.t.Fatalf("plan %q not found", planName)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.exec.executeSequence(plan)
	}()
	return done
}

// lastSequenceID returns the most recent sequence's id from the database.
func (h *harness) lastSequenceID(t *testing.T) string {
	t.Helper()

	if as := h.exec.ActiveSequence(); as != nil {
		return as.ID()
	}

	seq, err := h.db.LastSequence(t.Context())
	if err != nil {
		t.Fatalf("LastSequence: %v", err)
	}
	if seq == nil {
		t.Fatal("no sequence was recorded")
	}
	return seq.ID
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- config builders -------------------------------------------------------

func floatPtr(f float64) *float64 { return &f }

// trueCondition is a condition that always holds.
func trueCondition() config.ConditionConfig {
	return config.ConditionConfig{Condition: "true"}
}

// chargeBelow builds a condition on the test UPS battery charge.
func chargeBelow(v float64) config.ConditionConfig {
	return config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.battery.charge",
		Below:     floatPtr(v),
	}
}

// testClient builds a client using the fake transport.
func testClient(name string, tags ...string) config.ClientConfig {
	return config.ClientConfig{
		Name:           name,
		Transport:      "fake",
		Address:        "10.0.0.1",
		Tags:           tags,
		ShutdownBudget: "1s",
		GuardPeriod:    "1ms",
		FeedPolicy:     "any",
		WakePolicy:     "power_state",
	}
}

// testPlan builds a plan with the given shutdown stages.
func testPlan(name string, stages ...config.StageConfig) config.PlanConfig {
	return config.PlanConfig{
		Name:     name,
		Trigger:  trueCondition(),
		Shutdown: config.ShutdownConfig{Stages: stages},
		Wake: config.WakeConfig_{
			Gate:          trueCondition(),
			Stagger:       "1ms",
			ProbeInterval: "1ms",
			BootDeadline:  "50ms",
			Retries:       0,
		},
	}
}

// testStage builds a shutdown stage.
func testStage(name string, when config.ConditionConfig, clients ...string) config.StageConfig {
	return config.StageConfig{
		Name:        name,
		When:        when,
		Clients:     clients,
		Budget:      "1s",
		WaitTimeout: "1s",
		WaitPolicy:  "skip",
	}
}
