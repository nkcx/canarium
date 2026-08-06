package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

type Executor struct {
	cfg        *config.Config
	store      *facts.Store
	evaluator  *conditions.Evaluator
	db         *state.DB
	transports map[string]Transport
	mode       Mode
	logger     *slog.Logger

	mu             sync.RWMutex
	activeSequence *ActiveSequence
	clientStates   map[string]ClientState
	listeners      []EventListener

	ctx    context.Context
	cancel context.CancelFunc
}

type ActiveSequence struct {
	Sequence  *state.Sequence
	Plan      *config.PlanConfig
	Aborted   bool
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
		clientStates: make(map[string]ClientState),
		ctx:          ctx,
		cancel:       cancel,
	}
}

func (e *Executor) RegisterTransport(name string, t Transport) {
	e.transports[name] = t
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

func (e *Executor) GetActiveSequence() *ActiveSequence {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activeSequence
}

func (e *Executor) Start() error {
	e.logger.Info("executor starting", "mode", e.mode)

	if err := e.restoreState(); err != nil {
		e.logger.Error("failed to restore state", "error", err)
	}

	go e.probeLoop()
	go e.policyLoop()

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
		if plan != nil {
			e.mu.Lock()
			e.activeSequence = &ActiveSequence{Sequence: seq, Plan: plan}
			e.mu.Unlock()
			go e.resumeSequence()
		}
	}

	return nil
}

func (e *Executor) probeLoop() {
	ticker := time.NewTicker(30 * time.Second)
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

func (e *Executor) policyLoop() {
	ticker := time.NewTicker(5 * time.Second)
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
	e.store.RefreshQuality(now)

	for i := range e.cfg.Plans {
		plan := &e.cfg.Plans[i]

		if e.GetActiveSequence() != nil {
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

func (e *Executor) executeSequence(plan *config.PlanConfig) {
	seqID := fmt.Sprintf("seq_%d", time.Now().UnixNano())

	preState := make(map[string]string)
	for _, c := range e.cfg.Clients {
		preState[c.Name] = e.GetClientState(c.Name).String()
	}

	seq := &state.Sequence{
		ID:               seqID,
		PlanName:         plan.Name,
		State:            "shutting_down",
		CurrentStage:     0,
		PonrCrossed:      false,
		StartedAt:        time.Now(),
		PreSequenceState: preState,
	}

	e.db.SaveSequence(seq)
	e.mu.Lock()
	e.activeSequence = &ActiveSequence{Sequence: seq, Plan: plan}
	e.mu.Unlock()

	e.runShutdownStages(plan, seq)
}

func (e *Executor) resumeSequence() {
	as := e.GetActiveSequence()
	if as == nil {
		return
	}

	completed, _ := e.db.GetCompletedStages(as.Sequence.ID)
	completedSet := make(map[int]bool)
	for _, idx := range completed {
		completedSet[idx] = true
	}

	as.Sequence.CurrentStage = 0
	for idx := range as.Plan.Shutdown.Stages {
		if !completedSet[idx] {
			as.Sequence.CurrentStage = idx
			break
		}
	}

	e.runShutdownStages(as.Plan, as.Sequence)
}

func (e *Executor) runShutdownStages(plan *config.PlanConfig, seq *state.Sequence) {
	for i := seq.CurrentStage; i < len(plan.Shutdown.Stages); i++ {
		stage := &plan.Shutdown.Stages[i]
		seq.CurrentStage = i
		e.db.SaveSequence(seq)

		if stage.PointOfNoReturn && !seq.PonrCrossed {
			seq.PonrCrossed = true
			e.db.SaveSequence(seq)
			e.emit(Event{Type: "ponr_crossed", Timestamp: time.Now(), Data: stage.Name})
		}

		if !seq.PonrCrossed && plan.Abort != nil {
			if e.evaluator.Evaluate(plan.Abort, time.Now()) == facts.True {
				e.handleAbort(plan, seq)
				return
			}
		}

		waitTimeout, _ := config.ParseDuration(stage.WaitTimeout)
		if waitTimeout == 0 {
			waitTimeout = config.DefaultWaitTimeout()
		}
		waitDeadline := time.Now().Add(waitTimeout)

		conditionMet := false
		for time.Now().Before(waitDeadline) {
			if e.evaluator.Evaluate(&stage.When, time.Now()) == facts.True {
				conditionMet = true
				break
			}

			if !seq.PonrCrossed && plan.Abort != nil {
				if e.evaluator.Evaluate(plan.Abort, time.Now()) == facts.True {
					e.handleAbort(plan, seq)
					return
				}
			}

			select {
			case <-e.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}

		if !conditionMet {
			e.logger.Warn("stage wait timeout", "stage", stage.Name, "policy", stage.WaitPolicy)
			switch stage.WaitPolicy {
			case "hold":
				i--
				continue
			case "escalate":
				e.emit(Event{Type: "stage_timeout", Timestamp: time.Now(), Data: stage.Name})
				continue
			default:
				continue
			}
		}

		e.emit(Event{Type: "stage_start", Timestamp: time.Now(), Data: stage.Name})
		e.executeStage(plan, seq, stage, i)
		e.emit(Event{Type: "stage_complete", Timestamp: time.Now(), Data: stage.Name})
	}

	if plan.Shutdown.PostShutdown != nil {
		e.executePostShutdown(plan.Shutdown.PostShutdown)
	}

	seq.State = "wake_gate"
	e.db.SaveSequence(seq)
	e.runWake(plan, seq)
}

func (e *Executor) executeStage(plan *config.PlanConfig, seq *state.Sequence, stage *config.StageConfig, stageIdx int) {
	clients := config.ResolveClientRefs(stage.Clients, e.cfg)
	budget, _ := config.ParseDuration(stage.Budget)
	if budget == 0 {
		budget = config.DefaultShutdownBudget()
	}

	stageRecord := &state.StageRecord{
		SequenceID: seq.ID,
		StageIndex: stageIdx,
		StageName:  stage.Name,
		StartedAt:  time.Now(),
		Clients:    make(map[string]state.ClientResult),
	}
	e.db.SaveStageRecord(stageRecord)

	var wg sync.WaitGroup
	for _, clientName := range clients {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			e.shutdownClient(name, seq, budget)
		}(clientName)
	}
	wg.Wait()

	now := time.Now()
	stageRecord.CompletedAt = &now
	e.db.SaveStageRecord(stageRecord)
}

func (e *Executor) shutdownClient(name string, seq *state.Sequence, budget time.Duration) {
	clientCfg := e.findClientConfig(name)
	if clientCfg == nil {
		return
	}

	t, ok := e.transports[clientCfg.Transport]
	if !ok {
		e.logger.Error("transport not found", "client", name, "transport", clientCfg.Transport)
		return
	}

	acquired, _ := e.db.AcquireClientLock(name, seq.ID)
	if !acquired {
		e.logger.Warn("client locked by another sequence", "client", name)
		return
	}

	intent := &state.Intent{
		ID:         fmt.Sprintf("int_%s_%d", name, time.Now().UnixNano()),
		SequenceID: seq.ID,
		ClientName: name,
		Action:     "shutdown",
		Timestamp:  time.Now(),
		Status:     "dispatching",
	}
	e.db.SaveIntent(intent)

	e.setClientState(name, StateShuttingDown, &seq.ID)

	if e.Mode() == ModeDryRun {
		e.logger.Info("[dry-run] would shutdown", "client", name)
		intent.Status = "dispatched"
		intent.Result = &state.ActionResult{Success: true, Message: "dry-run"}
		e.db.SaveIntent(intent)
		time.Sleep(1 * time.Second)
		e.setClientState(name, StateDown, &seq.ID)
		return
	}

	client := e.buildClient(clientCfg)
	ctx, cancel := context.WithTimeout(e.ctx, budget)
	defer cancel()

	result, err := t.Execute(ctx, client, ActionShutdown)
	intent.Status = "dispatched"
	if err != nil {
		intent.Result = &state.ActionResult{Success: false, Message: err.Error()}
		e.logger.Error("shutdown failed", "client", name, "error", err)
	} else {
		intent.Result = &state.ActionResult{Success: result.Success, Message: result.Message}
	}
	e.db.SaveIntent(intent)

	clientBudget, _ := config.ParseDuration(clientCfg.ShutdownBudget)
	if clientBudget == 0 {
		clientBudget = budget
	}
	deadline := time.Now().Add(clientBudget)

	for time.Now().Before(deadline) {
		probeState, probeErr := t.Probe(ctx, client)
		if probeErr == nil && probeState == StateDown {
			e.setClientState(name, StateDown, &seq.ID)
			return
		}
		select {
		case <-ctx.Done():
			guardPeriod, _ := config.ParseDuration(clientCfg.GuardPeriod)
			if guardPeriod == 0 {
				guardPeriod = config.DefaultGuardPeriod()
			}
			e.setClientState(name, StateDownUnverified, &seq.ID)
			return
		case <-time.After(5 * time.Second):
		}
	}

	e.setClientState(name, StateDownUnverified, &seq.ID)
}

func (e *Executor) handleAbort(plan *config.PlanConfig, seq *state.Sequence) {
	e.logger.Info("sequence aborted", "plan", plan.Name)
	seq.State = "aborted"
	e.db.SaveSequence(seq)
	e.emit(Event{Type: "abort", Timestamp: time.Now(), Data: plan.Name})

	e.waitForShuttingDown(seq)

	seq.State = "wake_gate"
	e.db.SaveSequence(seq)
	e.runWake(plan, seq)
}

func (e *Executor) waitForShuttingDown(seq *state.Sequence) {
	for _, c := range e.cfg.Clients {
		state := e.GetClientState(c.Name)
		if state != StateShuttingDown {
			continue
		}

		budget, _ := config.ParseDuration(c.ShutdownBudget)
		if budget == 0 {
			budget = config.DefaultShutdownBudget()
		}
		deadline := time.Now().Add(budget)

		for time.Now().Before(deadline) {
			current := e.GetClientState(c.Name)
			if current == StateDown || current == StateDownUnverified {
				break
			}
			time.Sleep(5 * time.Second)
		}

		if e.GetClientState(c.Name) == StateShuttingDown {
			e.setClientState(c.Name, StateDownUnverified, &seq.ID)
		}
	}
}

func (e *Executor) runWake(plan *config.PlanConfig, seq *state.Sequence) {
	for {
		result := e.evaluator.Evaluate(&plan.Wake.Gate, time.Now())
		if result == facts.True {
			break
		}
		select {
		case <-e.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}

	e.emit(Event{Type: "wake_gate_satisfied", Timestamp: time.Now(), Data: plan.Name})
	seq.State = "waking"
	e.db.SaveSequence(seq)

	stagger, _ := config.ParseDuration(plan.Wake.Stagger)
	if stagger == 0 {
		stagger = config.DefaultStagger()
	}

	wakeOrder := e.computeWakeOrder(plan, seq)

	for _, clientName := range wakeOrder {
		clientCfg := e.findClientConfig(clientName)
		if clientCfg == nil {
			continue
		}

		currentState := e.GetClientState(clientName)

		if currentState == StateUp {
			continue
		}

		if clientCfg.WakePolicy == "retain_state" {
			if preState, ok := seq.PreSequenceState[clientName]; ok {
				if preState != "up" {
					e.logger.Info("skipping wake (retain_state, was not up)", "client", clientName)
					continue
				}
			}
		}

		if !e.dependenciesMet(clientName) {
			e.logger.Warn("skipping wake (dependency failed)", "client", clientName)
			e.setClientState(clientName, StateFailed, &seq.ID)
			continue
		}

		if currentState == StateDownUnverified {
			guardPeriod, _ := config.ParseDuration(clientCfg.GuardPeriod)
			if guardPeriod == 0 {
				guardPeriod = config.DefaultGuardPeriod()
			}
			time.Sleep(guardPeriod)
		}

		e.wakeClient(clientName, plan, seq)

		if stagger > 0 {
			time.Sleep(stagger)
		}
	}

	now := time.Now()
	seq.State = "completed"
	seq.CompletedAt = &now
	e.db.SaveSequence(seq)
	e.db.ReleaseSequenceLocks(seq.ID)

	e.mu.Lock()
	e.activeSequence = nil
	e.mu.Unlock()

	e.emit(Event{Type: "sequence_completed", Timestamp: time.Now(), Data: plan.Name})
}

func (e *Executor) wakeClient(name string, plan *config.PlanConfig, seq *state.Sequence) {
	clientCfg := e.findClientConfig(name)
	if clientCfg == nil {
		return
	}

	wakeTransport := clientCfg.Transport
	if clientCfg.Wake != nil && clientCfg.Wake.Transport != "" {
		wakeTransport = clientCfg.Wake.Transport
	}

	t, ok := e.transports[wakeTransport]
	if !ok {
		e.logger.Error("wake transport not found", "client", name, "transport", wakeTransport)
		e.setClientState(name, StateFailed, &seq.ID)
		return
	}

	e.setClientState(name, StateWaking, &seq.ID)

	retries := plan.Wake.Retries
	if retries == 0 {
		retries = config.DefaultRetries()
	}
	bootDeadline, _ := config.ParseDuration(plan.Wake.BootDeadline)
	if bootDeadline == 0 {
		bootDeadline = config.DefaultBootDeadline()
	}
	probeInterval, _ := config.ParseDuration(plan.Wake.ProbeInterval)
	if probeInterval == 0 {
		probeInterval = config.DefaultProbeInterval()
	}

	client := e.buildClient(clientCfg)

	for attempt := 0; attempt <= retries; attempt++ {
		if e.Mode() == ModeDryRun {
			e.logger.Info("[dry-run] would wake", "client", name, "attempt", attempt)
			e.setClientState(name, StateUp, &seq.ID)
			return
		}

		_, err := t.Execute(e.ctx, client, ActionWake)
		if err != nil {
			e.logger.Error("wake command failed", "client", name, "error", err, "attempt", attempt)
		}

		deadline := time.Now().Add(bootDeadline)
		for time.Now().Before(deadline) {
			probeT, ok := e.transports[clientCfg.Transport]
			if ok {
				probeState, probeErr := probeT.Probe(e.ctx, client)
				if probeErr == nil && probeState == StateUp {
					e.setClientState(name, StateUp, &seq.ID)
					e.emit(Event{
						Type:      "client_wake_success",
						Timestamp: time.Now(),
						Data:      name,
					})
					return
				}
			}
			time.Sleep(probeInterval)
		}
	}

	e.setClientState(name, StateFailed, &seq.ID)
	e.emit(Event{
		Type:      "client_wake_failed",
		Timestamp: time.Now(),
		Data:      name,
	})
}

func (e *Executor) executePostShutdown(ps *config.PostShutdownConfig) {
	e.logger.Info("executing post-shutdown action", "action", ps.Action, "command", ps.Command)
	if e.Mode() == ModeDryRun {
		e.logger.Info("[dry-run] would execute post-shutdown", "command", ps.Command)
		return
	}

	nutTransport, ok := e.transports["nut"]
	if !ok {
		e.logger.Error("NUT transport not available for post-shutdown")
		return
	}

	client := &Client{
		Name:    ps.UPS,
		Address: ps.UPS,
		TransportConfig: map[string]any{
			"command": ps.Command,
			"delay":   ps.Delay,
		},
	}

	_, err := nutTransport.Execute(e.ctx, client, ActionOutletOff)
	if err != nil {
		e.logger.Error("post-shutdown action failed", "error", err)
	}
}

func (e *Executor) computeWakeOrder(plan *config.PlanConfig, seq *state.Sequence) []string {
	if plan.Wake.Order == "reverse" || plan.Wake.Order == "" {
		var order []string
		for i := len(plan.Shutdown.Stages) - 1; i >= 0; i-- {
			clients := config.ResolveClientRefs(plan.Shutdown.Stages[i].Clients, e.cfg)
			order = append(order, clients...)
		}
		return order
	}

	if len(plan.Wake.Stages) > 0 {
		var order []string
		for _, s := range plan.Wake.Stages {
			clients := config.ResolveClientRefs(s.Clients, e.cfg)
			order = append(order, clients...)
		}
		return order
	}

	var order []string
	for i := len(plan.Shutdown.Stages) - 1; i >= 0; i-- {
		clients := config.ResolveClientRefs(plan.Shutdown.Stages[i].Clients, e.cfg)
		order = append(order, clients...)
	}
	return order
}

func (e *Executor) dependenciesMet(clientName string) bool {
	clientCfg := e.findClientConfig(clientName)
	if clientCfg == nil {
		return true
	}
	for _, dep := range clientCfg.DependsOn {
		depState := e.GetClientState(dep)
		if depState != StateUp {
			return false
		}
	}
	return true
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
		timeout, _ := config.ParseDuration(c.Probe.Timeout)
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

func (e *Executor) AbortSequence() error {
	e.mu.Lock()
	as := e.activeSequence
	e.mu.Unlock()

	if as == nil {
		return fmt.Errorf("no active sequence")
	}

	as.Aborted = true
	return nil
}
