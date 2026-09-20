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
	"gopkg.in/yaml.v3"
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

// probeTimeout bounds a single background reachability check.
const probeTimeout = 10 * time.Second

// retentionInterval is how often the journal is pruned. Retention windows
// are measured in days, so checking hourly is ample.
const retentionInterval = 1 * time.Hour

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

	// upsSourcesOnce guards the cached list of UPS-reporting source
	// instances, which is derived from the store's declarations and does not
	// change after startup.
	upsSourcesOnce   sync.Once
	cachedUPSSources []string

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

	e.registerDerivedFacts()
	e.updateDerivedFacts(time.Now())

	if err := e.restoreState(); err != nil {
		e.logger.Error("failed to restore state", "error", err)
	}

	go e.probeLoop()
	go e.policyLoop()
	go e.qualityLoop()
	go e.retentionLoop()

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
		ctx, cancel := context.WithTimeout(e.ctx, probeTimeout)
		probeState, err := t.Probe(ctx, client)
		cancel()

		if err != nil {
			// The probe established nothing — a timeout, a DNS failure, a
			// partition. Leave the recorded state alone rather than
			// inventing one: "I could not reach it" is not evidence that a
			// host is down, and treating it as such is how a switch reboot
			// gets recorded as a completed shutdown.
			e.logger.Debug("probe inconclusive; leaving client state unchanged",
				"client", c.Name, "state", e.GetClientState(c.Name), "error", err)
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

	e.refreshFacts()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.refreshFacts()
		}
	}
}

// retentionLoop enforces canarium.journal_retain.
//
// The setting has existed with a 30d default since the first commit and
// nothing ever read it, so every sequence, intent and stage record
// accumulated permanently — on an SD card, in a daemon expected to run for
// years.
func (e *Executor) retentionLoop() {
	retain, err := config.Duration(e.cfg.Canarium.JournalRetain, config.DefaultJournalRetain())
	if err != nil {
		e.logger.Error("invalid journal_retain; using the default",
			"value", e.cfg.Canarium.JournalRetain,
			"default", config.DefaultJournalRetain(), "error", err)
	}

	if retain <= 0 {
		e.logger.Info("journal retention is disabled; records are kept indefinitely")
		return
	}

	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()

	e.pruneJournal(retain)

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.pruneJournal(retain)
		}
	}
}

func (e *Executor) pruneJournal(retain time.Duration) {
	// Never prune while a sequence is running. Retention is housekeeping;
	// an outage is not the time to be holding a write transaction over the
	// journal the executor is actively appending to.
	if e.ActiveSequence() != nil {
		e.logger.Debug("skipping journal retention: a sequence is in progress")
		return
	}

	result, err := e.db.PruneJournal(time.Now().Add(-retain))
	if err != nil {
		e.logger.Error("pruning journal", "error", err)
		return
	}

	if result.Total() == 0 {
		return
	}

	e.logger.Info("pruned journal records older than the retention window",
		"retain", retain,
		"sequences", result.Sequences,
		"intents", result.Intents,
		"stage_records", result.StageRecords)

	// SQLite does not return freed pages to the filesystem on its own.
	if err := e.db.Vacuum(); err != nil {
		e.logger.Error("vacuuming database after retention", "error", err)
	}
}

// refreshFacts recomputes fact freshness and Canarium's own derived facts.
func (e *Executor) refreshFacts() {
	now := time.Now()
	e.store.RefreshQuality(now)

	// Derived facts read the freshness computed above, so the order matters:
	// a client fed by a source that has just gone stale must be evaluated
	// against that staleness, not the previous tick's.
	e.updateDerivedFacts(now)
	e.store.RefreshQuality(now)
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

// configSnapshot serialises the running configuration.
//
// The sequences table has carried a config_snapshot column since the first
// schema and nothing ever wrote to it. Recording it means the journal says
// what the daemon was actually running when it acted, which is the first
// question asked after an outage behaves unexpectedly — and the config file
// on disk may well have been edited since.
func (e *Executor) configSnapshot() []byte {
	snapshot, err := yaml.Marshal(e.cfg)
	if err != nil {
		e.logger.Error("serialising config snapshot", "error", err)
		return nil
	}
	return snapshot
}

// buildClientFor builds a transport client, using the address pinned when
// the sequence started.
func (e *Executor) buildClientFor(as *ActiveSequence, c *config.ClientConfig) *Client {
	client := e.buildClient(c)
	client.Address = e.addressFor(as, c)
	return client
}

func (e *Executor) buildClient(c *config.ClientConfig) *Client {
	// Every field the transports read is populated here. FeedPolicy,
	// WakePolicy, ShutdownBudget, GuardPeriod, DependsOn, After and Before
	// existed on this struct and were never set, so a transport reading any
	// of them saw a zero value.
	shutdownBudget := e.duration(c.ShutdownBudget, config.DefaultShutdownBudget(),
		"shutdown_budget", "client", c.Name)
	guardPeriod := e.duration(c.GuardPeriod, config.DefaultGuardPeriod(),
		"guard_period", "client", c.Name)

	feedPolicy := FeedPolicyAny
	if c.FeedPolicy == "all" {
		feedPolicy = FeedPolicyAll
	}

	wakePolicy := WakePolicyPowerState
	if c.WakePolicy == "retain_state" {
		wakePolicy = WakePolicyRetainState
	}

	client := &Client{
		Name:            c.Name,
		Description:     c.Description,
		Transport:       c.Transport,
		Address:         c.Address,
		MAC:             c.MAC,
		Credentials:     c.Credentials,
		Tags:            c.Tags,
		Feeds:           c.Feeds,
		FeedPolicy:      feedPolicy,
		WakePolicy:      wakePolicy,
		ShutdownBudget:  shutdownBudget,
		GuardPeriod:     guardPeriod,
		DependsOn:       c.DependsOn,
		After:           c.After,
		Before:          c.Before,
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
