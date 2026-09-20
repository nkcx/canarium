package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

// Timings groups the executor's polling intervals.
//
// They are a struct rather than constants so tests can compress them: the
// production values are chosen for a daemon that runs for months, and a test
// asserting stage ordering should not have to wait two seconds per poll.
// Nothing in production overrides DefaultTimings.
type Timings struct {
	// Probe is how often client reachability is sampled.
	Probe time.Duration

	// Policy is how often plan triggers are evaluated.
	Policy time.Duration

	// QualityRefresh is how often fact freshness is recomputed. It is
	// shorter than the shortest realistic source poll interval so a fact is
	// marked stale promptly after its source stops reporting.
	QualityRefresh time.Duration

	// StagePoll is how often a stage's entry condition and the plan's abort
	// condition are re-evaluated while waiting.
	StagePoll time.Duration

	// ShutdownProbe is how often a client is probed while waiting for it to
	// go down.
	ShutdownProbe time.Duration

	// WakeGatePoll is how often the wake gate is re-evaluated.
	WakeGatePoll time.Duration

	// DryRunStep stands in for the time a real action would take, so a dry
	// run exercises sequencing at roughly realistic pacing.
	DryRunStep time.Duration
}

// DefaultTimings returns the production polling intervals.
func DefaultTimings() Timings {
	return Timings{
		Probe:          30 * time.Second,
		Policy:         5 * time.Second,
		QualityRefresh: 5 * time.Second,
		StagePoll:      2 * time.Second,
		ShutdownProbe:  5 * time.Second,
		WakeGatePoll:   10 * time.Second,
		DryRunStep:     1 * time.Second,
	}
}

type Executor struct {
	cfg        *config.Config
	store      *facts.Store
	evaluator  *conditions.Evaluator
	db         *state.DB
	transports map[string]Transport
	mode       Mode
	logger     *slog.Logger
	timings    Timings

	mu             sync.RWMutex
	activeSequence *ActiveSequence
	clientStates   map[string]ClientState
	listeners      []EventListener

	ctx    context.Context
	cancel context.CancelFunc
}

type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data,omitempty"`
}

type EventListener func(Event)

func NewExecutor(
	cfg *config.Config,
	store *facts.Store,
	evaluator *conditions.Evaluator,
	db *state.DB,
	logger *slog.Logger,
) *Executor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Executor{
		cfg:          cfg,
		store:        store,
		evaluator:    evaluator,
		db:           db,
		transports:   make(map[string]Transport),
		mode:         ParseMode(cfg.Canarium.Mode),
		logger:       logger,
		timings:      DefaultTimings(),
		clientStates: make(map[string]ClientState),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// SetTimings overrides the polling intervals. Intended for tests; call it
// before Start.
func (e *Executor) SetTimings(t Timings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.timings = t
}

func (e *Executor) RegisterTransport(name string, t Transport) {
	e.transports[name] = t
}

func remapAction(t Transport, action ActionType) ActionType {
	if r, ok := t.(ActionRemapper); ok {
		return r.RemapAction(action)
	}
	return action
}

func (e *Executor) AddListener(l EventListener) {
	e.mu.Lock()
	e.listeners = append(e.listeners, l)
	e.mu.Unlock()
}

func (e *Executor) emit(evt Event) {
	e.mu.RLock()
	listeners := make([]EventListener, len(e.listeners))
	copy(listeners, e.listeners)
	e.mu.RUnlock()
	for _, l := range listeners {
		l(evt)
	}
}

func (e *Executor) Mode() Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

func (e *Executor) SetMode(m Mode) {
	e.mu.Lock()
	e.mode = m
	e.mu.Unlock()
	e.emit(Event{Type: "mode_changed", Timestamp: time.Now(), Data: m.String()})
}

func (e *Executor) GetClientState(name string) ClientState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.clientStates[name]
}

func (e *Executor) GetAllClientStates() map[string]ClientState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make(map[string]ClientState, len(e.clientStates))
	for k, v := range e.clientStates {
		result[k] = v
	}
	return result
}

func (e *Executor) setClientState(name string, s ClientState, seqID *string) {
	e.mu.Lock()
	e.clientStates[name] = s
	e.mu.Unlock()

	e.db.SaveClientState(name, s.String(), seqID)
	e.emit(Event{
		Type:      "client_state_changed",
		Timestamp: time.Now(),
		Data:      map[string]string{"client": name, "state": s.String()},
	})
}

// ActiveSequence returns the sequence currently running, or nil.
func (e *Executor) ActiveSequence() *ActiveSequence {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activeSequence
}

// setActiveSequence installs the running sequence, refusing to replace one
// that is already in flight.
//
// Returns false if a sequence is already active. evaluatePolicies checks for
// an active sequence and then starts one in a new goroutine, so without this
// guard two plans triggering on the same tick — or the same plan triggering
// on two consecutive ticks before the first goroutine had installed itself —
// would both proceed, shutting the fleet down twice concurrently.
func (e *Executor) setActiveSequence(as *ActiveSequence) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.activeSequence != nil {
		return false
	}
	e.activeSequence = as
	return true
}

// clearActiveSequence releases the running sequence so a later trigger can
// start a new one.
//
// Every terminal path must call this. Previously only the successful end of
// runWake did, so any early return — context cancellation, a missing wake
// transport, an abort — left activeSequence permanently non-nil and the
// executor would never act again until restarted.
func (e *Executor) clearActiveSequence() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeSequence = nil
}

// saveSequence persists a snapshot of the sequence.
//
// The state layer receives a copy, never the live struct: it would otherwise
// read fields while the executor goroutine writes them.
func (e *Executor) saveSequence(as *ActiveSequence) {
	if err := e.db.SaveSequence(as.persistable()); err != nil {
		e.logger.Error("persisting sequence state", "sequence", as.ID(), "error", err)
	}
}

func (e *Executor) Start() error {
	e.logger.Info("executor starting", "mode", e.mode)

	if err := e.restoreState(); err != nil {
		e.logger.Error("failed to restore state", "error", err)
	}

	go e.probeLoop()
	go e.policyLoop()
	go e.qualityLoop()

	return nil
}

func (e *Executor) Stop() {
	e.cancel()
}

func (e *Executor) restoreState() error {
	states, err := e.db.GetAllClientStates()
	if err != nil {
		return err
	}
	e.mu.Lock()
	for name, stateStr := range states {
		e.clientStates[name] = ParseClientState(stateStr)
	}
	e.mu.Unlock()

	seq, err := e.db.GetActiveSequence()
	if err != nil {
		return err
	}
	if seq != nil {
		e.logger.Info("resuming active sequence", "id", seq.ID, "plan", seq.PlanName, "stage", seq.CurrentStage)
		plan := e.findPlan(seq.PlanName)
		if plan == nil {
			e.logger.Error("cannot resume: plan no longer exists in config",
				"sequence", seq.ID, "plan", seq.PlanName)
			return nil
		}

		as := newActiveSequence(seq, plan)
		e.setActiveSequence(as)
		go e.resumeSequence(as)
	}

	return nil
}

func (e *Executor) probeLoop() {
	ticker := time.NewTicker(e.timings.Probe)
	defer ticker.Stop()

	e.probeAllClients()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.probeAllClients()
		}
	}
}

func (e *Executor) probeAllClients() {
	for _, c := range e.cfg.Clients {
		t, ok := e.transports[c.Transport]
		if !ok {
			continue
		}

		hasCap := false
		for _, cap := range t.Capabilities() {
			if cap.Action == ActionProbe {
				hasCap = true
				break
			}
		}
		if !hasCap {
			continue
		}

		client := e.buildClient(&c)
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		probeState, err := t.Probe(ctx, client)
		cancel()

		if err != nil {
			continue
		}

		current := e.GetClientState(c.Name)
		if current == StateShuttingDown || current == StateWaking {
			if probeState == StateUp && current == StateWaking {
				e.setClientState(c.Name, StateUp, nil)
			} else if probeState == StateDown && current == StateShuttingDown {
				e.setClientState(c.Name, StateDown, nil)
			}
			continue
		}

		if current == StateUnknown || current == StateDownUnverified {
			e.setClientState(c.Name, probeState, nil)
		}
	}
}

// qualityLoop keeps fact freshness current independently of operating mode.
//
// Quality refresh used to happen only inside evaluatePolicies, which returns
// immediately when disarmed — so a disarmed daemon displayed every fact as
// "good" forever, however long its source had been dead. Freshness is an
// observation about the world, not a policy decision, so it runs always.
func (e *Executor) qualityLoop() {
	ticker := time.NewTicker(e.timings.QualityRefresh)
	defer ticker.Stop()

	e.store.RefreshQuality(time.Now())

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.store.RefreshQuality(time.Now())
		}
	}
}

func (e *Executor) policyLoop() {
	ticker := time.NewTicker(e.timings.Policy)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.evaluatePolicies()
		}
	}
}

func (e *Executor) evaluatePolicies() {
	if e.Mode() != ModeArmed && e.Mode() != ModeDryRun {
		return
	}

	now := time.Now()

	for i := range e.cfg.Plans {
		plan := &e.cfg.Plans[i]

		if e.ActiveSequence() != nil {
			break
		}

		result := e.evaluator.Evaluate(&plan.Trigger, now)
		if result == facts.True {
			e.logger.Info("plan triggered", "plan", plan.Name)
			e.emit(Event{Type: "trigger", Timestamp: now, Data: plan.Name})
			go e.executeSequence(plan)
		}
	}
}

func (e *Executor) findPlan(name string) *config.PlanConfig {
	for i := range e.cfg.Plans {
		if e.cfg.Plans[i].Name == name {
			return &e.cfg.Plans[i]
		}
	}
	return nil
}

func (e *Executor) findClientConfig(name string) *config.ClientConfig {
	for i := range e.cfg.Clients {
		if e.cfg.Clients[i].Name == name {
			return &e.cfg.Clients[i]
		}
	}
	return nil
}

func (e *Executor) buildClient(c *config.ClientConfig) *Client {
	client := &Client{
		Name:            c.Name,
		Description:     c.Description,
		Transport:       c.Transport,
		Address:         c.Address,
		MAC:             c.MAC,
		Credentials:     c.Credentials,
		Tags:            c.Tags,
		Feeds:           c.Feeds,
		TransportConfig: c.Config,
	}

	if c.Probe != nil {
		timeout := e.duration(c.Probe.Timeout, 0, "probe.timeout", "client", c.Name)
		client.ProbeConfig = ProbeConfig{
			Method:  c.Probe.Method,
			Port:    c.Probe.Port,
			Timeout: timeout,
		}
	}

	if c.Wake != nil {
		client.WakeConfig = &WakeConfig{
			Transport: c.Wake.Transport,
			MAC:       c.Wake.MAC,
			Broadcast: c.Wake.Broadcast,
			Config:    c.Wake.Config,
		}
	}

	return client
}
