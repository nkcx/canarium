package engine

import (
	"context"
	"log/slog"
	"slices"
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

const (
	// defaultWakeProbePort is used to verify a wake when the client's own
	// transport cannot probe. SSH is the most likely thing to be listening
	// on a machine that has just booted.
	defaultWakeProbePort = 22

	// defaultProbeTimeout bounds a reachability check when the client
	// specifies none.
	//
	// It is deliberately longer than the kernel's ARP resolution window
	// (three solicitations roughly a second apart). A host that has powered
	// off on a directly attached subnet is reported EHOSTUNREACH once ARP
	// gives up, which is positive evidence it is down; a shorter timeout
	// would cut that short and yield an inconclusive result instead.
	defaultProbeTimeout = 5 * time.Second
)

// modeKey is the key-value entry holding the operating mode selected at
// runtime.
const modeKey = "mode"

// dwellRetention is how long a dwell tracker survives without being
// evaluated. A condition still in the config is touched on every policy
// tick, so anything untouched for a day belongs to one that has gone.
const dwellRetention = 24 * time.Hour

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

	// clientStateSince records when each client last changed state, so the
	// wake path can credit time a host has already spent settled rather
	// than sleeping the full guard period for every client in turn.
	clientStateSince map[string]time.Time
	listeners        []EventListener

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
		cfg:              cfg,
		store:            store,
		evaluator:        evaluator,
		db:               db,
		transports:       make(map[string]Transport),
		mode:             ParseMode(cfg.Canarium.Mode),
		logger:           logger,
		timings:          DefaultTimings(),
		clientStates:     make(map[string]ClientState),
		clientStateSince: make(map[string]time.Time),
		ctx:              ctx,
		cancel:           cancel,
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
	previous, existed := e.clientStates[name]
	e.clientStates[name] = s
	if !existed || previous != s {
		e.clientStateSince[name] = time.Now()
	}
	e.mu.Unlock()

	// A lost client-state write means the executor's view and the journal
	// disagree, and a restart would resume from the stale one.
	if err := e.db.SaveClientState(e.ctx, name, s.String(), seqID); err != nil {
		e.logger.Error("persisting client state",
			"client", name, "state", s.String(), "error", err)
	}
	e.emit(Event{
		Type:      "client_state_changed",
		Timestamp: time.Now(),
		Data:      map[string]string{"client": name, "state": s.String()},
	})
}

// timeSinceSettled reports how long a client has been in its current state.
//
// Returns zero when the transition was not observed by this process — after
// a restart, for instance — so the caller waits the full period rather than
// assuming credit it cannot prove.
func (e *Executor) timeSinceSettled(name string) time.Duration {
	e.mu.RLock()
	defer e.mu.RUnlock()

	since, ok := e.clientStateSince[name]
	if !ok || since.IsZero() {
		return 0
	}
	return time.Since(since)
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
// saveSequenceDurable persists the sequence and waits for it to reach the
// disk. Used when crossing the point of no return: a restart that could not
// see that flag would offer an abort that is no longer safe to take.
func (e *Executor) saveSequenceDurable(as *ActiveSequence) {
	if err := e.db.SaveSequenceDurable(e.ctx, as.persistable()); err != nil {
		e.logger.Error("persisting sequence", "sequence", as.ID(), "error", err)
	}
}

func (e *Executor) saveSequence(as *ActiveSequence) {
	if err := e.db.SaveSequence(e.ctx, as.persistable()); err != nil {
		e.logger.Error("persisting sequence state", "sequence", as.ID(), "error", err)
	}
}

func (e *Executor) Start() error {
	e.restoreMode()

	e.logger.Info("executor starting", "mode", e.Mode())

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

// restoreMode reinstates the operating mode an operator last selected.
//
// handleSetMode has always persisted the mode to the key-value table and
// nothing ever read it back, so a daemon restart silently reverted to
// whatever the YAML said. An operator who armed Canarium through the web UI
// and then rebooted the host came back up disarmed, with nothing to say so —
// the fleet unprotected for the next outage.
//
// config_readonly reverses the precedence: there, the file is authoritative
// by definition and a stored mode is ignored.
func (e *Executor) restoreMode() {
	if e.cfg.Canarium.ConfigReadonly {
		return
	}

	stored, err := e.db.GetKV(e.ctx, modeKey)
	if err != nil {
		e.logger.Error("reading the persisted operating mode; using the configured one",
			"error", err)
		return
	}
	if stored == "" {
		return
	}

	mode, ok := ParseModeStrict(stored)
	if !ok {
		e.logger.Error("the persisted operating mode is not recognised; using the configured one",
			"stored", stored)
		return
	}

	configured := ParseMode(e.cfg.Canarium.Mode)
	if mode == configured {
		return
	}

	e.mu.Lock()
	e.mode = mode
	e.mu.Unlock()

	e.logger.Warn("restored the operating mode set at runtime, which differs from the config file",
		"restored", mode.String(), "config_file", configured.String(),
		"hint", "set canarium.config_readonly to make the file authoritative")
}

func (e *Executor) Stop() {
	e.cancel()
}

func (e *Executor) restoreState() error {
	states, err := e.db.GetAllClientStates(e.ctx)
	if err != nil {
		return err
	}

	// Only clients in the current configuration. client_states keeps a row
	// for every client that has ever existed, so restoring all of them
	// resurrected machines removed from the config months ago: a live
	// deployment with `clients: []` reported five clients in /api/status,
	// one of them "down", none of which Canarium would ever touch again.
	// The rows are left in place rather than deleted, so a client that is
	// put back in the config does not lose its history -- and its state is
	// re-probed within one probe interval regardless.
	configured := make(map[string]bool, len(e.cfg.Clients))
	for _, c := range e.cfg.Clients {
		configured[c.Name] = true
	}

	var orphaned []string
	e.mu.Lock()
	for name, stateStr := range states {
		if !configured[name] {
			orphaned = append(orphaned, name)
			continue
		}
		e.clientStates[name] = ParseClientState(stateStr)
	}
	e.mu.Unlock()

	if len(orphaned) > 0 {
		slices.Sort(orphaned)
		e.logger.Info("ignoring recorded state for clients no longer in the config",
			"clients", orphaned)
	}

	seq, err := e.db.GetActiveSequence(e.ctx)
	if err != nil {
		return err
	}
	if seq != nil {
		e.logger.Info("resuming active sequence", "id", seq.ID, "plan", seq.PlanName, "stage", seq.CurrentStage)
		plan := e.findPlan(seq.PlanName)
		if plan == nil {
			// The plan was renamed or removed while the daemon was down.
			// The sequence cannot be resumed, but it must not be left in
			// place: its client locks would be held forever, and because
			// acquisition is re-entrant only for the holding sequence, no
			// future sequence could ever act on those clients again.
			reason := "plan " + seq.PlanName + " is no longer in the configuration"
			e.logger.Error("cannot resume a sequence whose plan has gone; "+
				"marking it failed and releasing its client locks",
				"sequence", seq.ID, "plan", seq.PlanName)

			if err := e.db.FailOrphanedSequence(e.ctx, seq.ID, reason); err != nil {
				e.logger.Error("releasing the orphaned sequence", "sequence", seq.ID, "error", err)
			}
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

		// While a sequence is driving this client, only the transitions it
		// is waiting for count. A host mid-shutdown that still answers is
		// not "up" — it is on its way down, and saying otherwise would
		// unwind the state the sequence is tracking.
		if current == StateShuttingDown || current == StateWaking {
			if probeState == StateUp && current == StateWaking {
				e.setClientState(c.Name, StateUp, nil)
			} else if probeState == StateDown && current == StateShuttingDown {
				e.setClientState(c.Name, StateDown, nil)
			}
			continue
		}

		// Otherwise the probe is the best information available, so record
		// it. An earlier version only accepted a result when the recorded
		// state was unknown or unverified, which meant a host that died
		// out-of-band stayed "up" forever and one an engineer powered back
		// on stayed "down" — the background probe could not track reality at
		// all. Note that an inconclusive probe returns an error and never
		// reaches here.
		if probeState != current {
			e.logger.Info("client state changed outside a sequence",
				"client", c.Name, "from", current, "to", probeState)
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

	result, err := e.db.PruneJournal(e.ctx, time.Now().Add(-retain))
	if err != nil {
		e.logger.Error("pruning journal", "error", err)
		return
	}

	// Dwell trackers are keyed by a hash of the condition, so every config
	// edit that changes a condition orphans its tracker. PruneDwellTrackers
	// existed and had no caller, so they accumulated for the life of the
	// installation.
	//
	// The window is deliberately generous: a tracker not seen for this long
	// belongs to a condition that is no longer in the config, since a live
	// one is touched on every policy tick.
	// Exported journal files expire on the same schedule as the rows they
	// were derived from. An operator who wants a longer audit window copies
	// them off the device; keeping them here forever would defeat the
	// retention setting by leaving the same data on disk in another form.
	filesPruned, err := e.db.PruneJournalFiles(time.Now().Add(-retain))
	if err != nil {
		e.logger.Error("pruning exported journal files", "error", err)
	} else if filesPruned > 0 {
		e.logger.Info("pruned exported journal files", "count", filesPruned)
	}

	dwellPruned, err := e.db.PruneDwellTrackers(e.ctx, time.Now().Add(-dwellRetention))
	if err != nil {
		e.logger.Error("pruning dwell trackers", "error", err)
	} else if dwellPruned > 0 {
		e.logger.Info("pruned dwell trackers for conditions no longer in the config",
			"count", dwellPruned)
	}

	if result.Total() == 0 && dwellPruned == 0 && filesPruned == 0 {
		return
	}

	if result.Total() > 0 {
		e.logger.Info("pruned journal records older than the retention window",
			"retain", retain,
			"sequences", result.Sequences,
			"intents", result.Intents,
			"stage_records", result.StageRecords)
	}

	// SQLite does not return freed pages to the filesystem on its own.
	if err := e.db.Vacuum(e.ctx); err != nil {
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

		if e.evaluator.Evaluate(&plan.Trigger, now) != facts.True {
			continue
		}

		// Claim the slot here, synchronously, before starting the goroutine.
		//
		// executeSequence used to claim it only after pinning every client's
		// address, which performs DNS lookups with a five-second timeout
		// each. During those seconds ActiveSequence() stayed nil, so every
		// subsequent policy tick logged another trigger, emitted another
		// event to every webhook, and spawned another goroutine racing on
		// the same work. The compare-and-set inside executeSequence stopped
		// them all but one from actually running, so nothing was shut down
		// twice — but the duplicate notifications were real.
		as := e.newSequenceFor(plan)
		if !e.setActiveSequence(as) {
			continue
		}

		e.logger.Info("plan triggered", "plan", plan.Name, "sequence", as.ID())
		e.emit(Event{Type: "trigger", Timestamp: now, Data: plan.Name})

		go e.executeSequence(as)
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

// ExplainCondition describes how a condition evaluates now, without the
// side effects of Evaluate. It uses the executor's own evaluator because
// that is the one holding the dwell timers the policy loop maintains; a
// fresh evaluator would report every `for:` as untracked.
func (e *Executor) ExplainCondition(cond *config.ConditionConfig, now time.Time) conditions.Explanation {
	return e.evaluator.Explain(cond, now)
}
