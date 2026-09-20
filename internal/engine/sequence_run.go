package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/state"
)

// executeSequence runs a plan from the beginning.
func (e *Executor) executeSequence(plan *config.PlanConfig) {
	now := time.Now()

	preState := make(map[string]string, len(e.cfg.Clients))
	for _, c := range e.cfg.Clients {
		preState[c.Name] = e.GetClientState(c.Name).String()
	}

	seq := &state.Sequence{
		ID:               fmt.Sprintf("seq_%d", now.UnixNano()),
		PlanName:         plan.Name,
		State:            SeqStateShuttingDown,
		CurrentStage:     0,
		StartedAt:        now,
		PreSequenceState: preState,
	}

	as := newActiveSequence(seq, plan)
	if !e.setActiveSequence(as) {
		e.logger.Warn("declining to start a sequence; one is already running",
			"plan", plan.Name)
		return
	}

	e.saveSequence(as)
	e.logger.Info("sequence started", "sequence", as.ID(), "plan", plan.Name)

	e.runShutdownStages(as)
}

// resumeSequence continues a sequence that was interrupted by a restart,
// picking up at the first stage with no recorded completion.
func (e *Executor) resumeSequence(as *ActiveSequence) {
	completed, err := e.db.GetCompletedStages(as.ID())
	if err != nil {
		e.logger.Error("reading completed stages; resuming from the recorded stage",
			"sequence", as.ID(), "error", err)
	}

	completedSet := make(map[int]bool, len(completed))
	for _, idx := range completed {
		completedSet[idx] = true
	}

	stages := as.Plan().Shutdown.Stages
	resumeAt := len(stages)
	for idx := range stages {
		if !completedSet[idx] {
			resumeAt = idx
			break
		}
	}
	as.SetCurrentStage(resumeAt)

	e.logger.Info("resuming sequence", "sequence", as.ID(), "stage", resumeAt)
	e.runShutdownStages(as)
}

// runShutdownStages walks a plan's shutdown stages in order.
func (e *Executor) runShutdownStages(as *ActiveSequence) {
	plan := as.Plan()

	for i := as.CurrentStage(); i < len(plan.Shutdown.Stages); i++ {
		stage := &plan.Shutdown.Stages[i]

		as.SetCurrentStage(i)
		e.saveSequence(as)

		if e.shouldAbort(as) {
			e.handleAbort(as)
			return
		}

		outcome := e.waitForStage(as, stage)
		switch outcome {
		case stageProceed:
		case stageAborted:
			e.handleAbort(as)
			return
		case stageCancelled:
			// The daemon is stopping. Leave the sequence marked in-progress
			// so it resumes on restart, but drop the in-memory handle.
			e.logger.Info("sequence interrupted by shutdown", "sequence", as.ID())
			e.clearActiveSequence()
			return
		case stageTimedOut:
			e.logger.Warn("stage wait timed out",
				"stage", stage.Name, "policy", stage.WaitPolicy)
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

		// The point of no return is crossed here — once the stage's entry
		// condition has been met and we are about to dispatch — not when the
		// loop reaches the stage.
		//
		// Crossing it on entry meant abort was disabled for the entire wait,
		// which defaults to an hour. With point_of_no_return on the first
		// stage, as the shipped example config has it, abort was disabled the
		// instant the plan triggered: mains power returning a second later
		// would not stop the shutdown, defeating the feature the README
		// leads with. SPEC §7.4 is explicit that abort is ignored only once
		// a PONR stage *begins*.
		//
		// A stage skipped by wait_policy never reaches this point, so
		// skipping a PONR stage correctly leaves the sequence abortable.
		if stage.PointOfNoReturn && as.CrossPonr() {
			e.saveSequence(as)
			e.logger.Info("point of no return crossed", "stage", stage.Name, "sequence", as.ID())
			e.emit(Event{Type: "ponr_crossed", Timestamp: time.Now(), Data: stage.Name})
		}

		e.emit(Event{Type: "stage_start", Timestamp: time.Now(), Data: stage.Name})
		e.executeStage(as, stage, i)
		e.emit(Event{Type: "stage_complete", Timestamp: time.Now(), Data: stage.Name})
	}

	if plan.Shutdown.PostShutdown != nil {
		e.executePostShutdown(plan.Shutdown.PostShutdown)
	}

	as.SetState(SeqStateWakeGate)
	e.saveSequence(as)
	e.runWake(as)
}

// stageOutcome is the result of waiting for a stage's entry condition.
type stageOutcome int

const (
	stageProceed stageOutcome = iota
	stageTimedOut
	stageAborted
	stageCancelled
)

// waitForStage blocks until a stage's `when` condition holds, the plan's
// abort condition fires, the wait budget expires, or the daemon shuts down.
func (e *Executor) waitForStage(as *ActiveSequence, stage *config.StageConfig) stageOutcome {
	waitTimeout := e.duration(stage.WaitTimeout, config.DefaultWaitTimeout(),
		"wait_timeout", "stage", stage.Name)
	deadline := time.Now().Add(waitTimeout)

	for {
		if e.evaluator.Evaluate(&stage.When, time.Now()) == facts.True {
			return stageProceed
		}
		if e.shouldAbort(as) {
			return stageAborted
		}
		if !time.Now().Before(deadline) {
			return stageTimedOut
		}

		select {
		case <-e.ctx.Done():
			return stageCancelled
		case <-time.After(e.timings.StagePoll):
		}
	}
}

// shouldAbort reports whether the sequence must stop before acting further.
func (e *Executor) shouldAbort(as *ActiveSequence) bool {
	if as.PonrCrossed() {
		return false
	}

	if requested, reason := as.AbortRequested(); requested {
		e.logger.Info("abort requested by operator",
			"sequence", as.ID(), "reason", reason)
		return true
	}

	abort := as.Plan().Abort
	if abort == nil {
		return false
	}
	return e.evaluator.Evaluate(abort, time.Now()) == facts.True
}

// executeStage dispatches shutdown to every client in a stage, concurrently,
// and waits for them all to settle.
func (e *Executor) executeStage(as *ActiveSequence, stage *config.StageConfig, stageIdx int) {
	clients := config.ResolveClientRefs(stage.Clients, e.cfg)

	budget := e.duration(stage.Budget, config.DefaultShutdownBudget(),
		"budget", "stage", stage.Name)

	record := &state.StageRecord{
		SequenceID: as.ID(),
		StageIndex: stageIdx,
		StageName:  stage.Name,
		StartedAt:  time.Now(),
		Clients:    make(map[string]state.ClientResult, len(clients)),
	}
	if err := e.db.SaveStageRecord(record); err != nil {
		e.logger.Error("persisting stage record", "stage", stage.Name, "error", err)
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, name := range clients {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			result := e.shutdownClient(name, as, budget)

			mu.Lock()
			record.Clients[name] = result
			mu.Unlock()
		}(name)
	}
	wg.Wait()

	now := time.Now()
	record.CompletedAt = &now
	if err := e.db.SaveStageRecord(record); err != nil {
		e.logger.Error("persisting stage completion", "stage", stage.Name, "error", err)
	}
}

// shutdownClient shuts one client down and waits for it to be verifiably off.
func (e *Executor) shutdownClient(name string, as *ActiveSequence, budget time.Duration) state.ClientResult {
	started := time.Now()
	result := state.ClientResult{StartedAt: started.Format(time.RFC3339Nano)}

	finish := func(s ClientState, errMsg string) state.ClientResult {
		result.State = s.String()
		result.CompletedAt = time.Now().Format(time.RFC3339Nano)
		result.Error = errMsg
		return result
	}

	clientCfg := e.findClientConfig(name)
	if clientCfg == nil {
		e.logger.Error("stage references an unknown client", "client", name)
		return finish(StateUnknown, "client not found in config")
	}

	t, ok := e.transports[clientCfg.Transport]
	if !ok {
		e.logger.Error("transport not found", "client", name, "transport", clientCfg.Transport)
		e.setClientState(name, StateFailed, ptr(as.ID()))
		return finish(StateFailed, "unknown transport "+clientCfg.Transport)
	}

	seqID := as.ID()

	// Claim the client so two sequences cannot act on it at once. The lock
	// is re-entrant for the sequence that already holds it, so a resumed
	// sequence reclaims its own locks rather than skipping every client.
	acquired, err := e.db.AcquireClientLock(name, seqID)
	if err != nil {
		e.logger.Error("acquiring client lock", "client", name, "error", err)
		return finish(StateFailed, "could not acquire client lock: "+err.Error())
	}
	if !acquired {
		holder, _ := e.db.ClientLockHolder(name)
		e.logger.Warn("client is locked by another sequence; skipping",
			"client", name, "holder", holder)
		return finish(e.GetClientState(name), "locked by sequence "+holder)
	}

	intent := &state.Intent{
		ID:         fmt.Sprintf("int_%s_%d", name, time.Now().UnixNano()),
		SequenceID: seqID,
		ClientName: name,
		Action:     "shutdown",
		Timestamp:  time.Now(),
		Status:     "dispatching",
	}
	e.saveIntent(intent)

	e.setClientState(name, StateShuttingDown, &seqID)

	if e.Mode() == ModeDryRun {
		e.logger.Info("[dry-run] would shut down", "client", name)
		intent.Status = "dispatched"
		intent.Result = &state.ActionResult{Success: true, Message: "dry-run"}
		e.saveIntent(intent)

		e.sleep(e.timings.DryRunStep)
		e.setClientState(name, StateDown, &seqID)
		return finish(StateDown, "")
	}

	client := e.buildClient(clientCfg)

	ctx, cancel := context.WithTimeout(e.ctx, budget)
	defer cancel()

	actionResult, execErr := t.Execute(ctx, client, remapAction(t, ActionShutdown))
	intent.Status = "dispatched"
	switch {
	case execErr != nil:
		intent.Result = &state.ActionResult{Success: false, Message: execErr.Error()}
		e.logger.Error("shutdown command failed", "client", name, "error", execErr)
	case actionResult != nil:
		intent.Result = &state.ActionResult{Success: actionResult.Success, Message: actionResult.Message}
	default:
		intent.Result = &state.ActionResult{Success: false, Message: "transport returned no result"}
	}
	e.saveIntent(intent)

	return e.awaitClientDown(ctx, name, clientCfg, t, client, &seqID, budget, finish)
}

// awaitClientDown polls a client until it is verifiably down or the budget
// expires.
func (e *Executor) awaitClientDown(
	ctx context.Context,
	name string,
	clientCfg *config.ClientConfig,
	t Transport,
	client *Client,
	seqID *string,
	stageBudget time.Duration,
	finish func(ClientState, string) state.ClientResult,
) state.ClientResult {
	clientBudget := e.duration(clientCfg.ShutdownBudget, stageBudget,
		"shutdown_budget", "client", name)
	if clientBudget > stageBudget {
		// The stage budget bounds the context, so a longer per-client budget
		// could never be honoured anyway.
		e.logger.Warn("client shutdown_budget exceeds the stage budget; capping",
			"client", name, "shutdown_budget", clientBudget, "stage_budget", stageBudget)
		clientBudget = stageBudget
	}
	deadline := time.Now().Add(clientBudget)

	for time.Now().Before(deadline) {
		probeState, probeErr := t.Probe(ctx, client)
		if probeErr == nil && probeState == StateDown {
			e.setClientState(name, StateDown, seqID)
			return finish(StateDown, "")
		}

		select {
		case <-ctx.Done():
			e.setClientState(name, StateDownUnverified, seqID)
			return finish(StateDownUnverified, "budget expired before the host was confirmed down")
		case <-time.After(e.timings.ShutdownProbe):
		}
	}

	e.setClientState(name, StateDownUnverified, seqID)
	return finish(StateDownUnverified, "budget expired before the host was confirmed down")
}

// handleAbort stops a sequence before the point of no return, lets in-flight
// shutdowns finish, and hands over to the wake plan.
func (e *Executor) handleAbort(as *ActiveSequence) {
	_, reason := as.AbortRequested()
	e.logger.Info("sequence aborted", "sequence", as.ID(), "plan", as.PlanName(), "reason", reason)

	as.SetState(SeqStateAborting)
	e.saveSequence(as)
	e.emit(Event{Type: "abort", Timestamp: time.Now(), Data: as.PlanName()})

	// Hosts already told to shut down will do so regardless; wait for them
	// rather than trying to wake a machine that is still going down.
	e.waitForShuttingDown(as)

	as.SetState(SeqStateWakeGate)
	e.saveSequence(as)
	e.runWake(as)
}

// waitForShuttingDown waits for every client mid-shutdown to reach a settled
// state, so the wake plan does not race a shutdown still in progress.
func (e *Executor) waitForShuttingDown(as *ActiveSequence) {
	seqID := as.ID()

	for _, c := range e.cfg.Clients {
		if e.GetClientState(c.Name) != StateShuttingDown {
			continue
		}

		budget := e.duration(c.ShutdownBudget, config.DefaultShutdownBudget(),
			"shutdown_budget", "client", c.Name)
		deadline := time.Now().Add(budget)

		for time.Now().Before(deadline) {
			switch e.GetClientState(c.Name) {
			case StateDown, StateDownUnverified, StateFailed:
				deadline = time.Time{} // settled
			}
			if deadline.IsZero() {
				break
			}
			if !e.sleep(e.timings.ShutdownProbe) {
				return
			}
		}

		if e.GetClientState(c.Name) == StateShuttingDown {
			e.setClientState(c.Name, StateDownUnverified, &seqID)
		}
	}
}

// runWake waits for the wake gate, then brings clients back up in order.
func (e *Executor) runWake(as *ActiveSequence) {
	plan := as.Plan()

	for {
		if e.evaluator.Evaluate(&plan.Wake.Gate, time.Now()) == facts.True {
			break
		}
		if !e.sleep(e.timings.WakeGatePoll) {
			e.logger.Info("wake gate wait interrupted by shutdown", "sequence", as.ID())
			e.clearActiveSequence()
			return
		}
	}

	e.emit(Event{Type: "wake_gate_satisfied", Timestamp: time.Now(), Data: plan.Name})
	as.SetState(SeqStateWaking)
	e.saveSequence(as)

	// An explicit "0s" means wake everything at once and is honoured as such.
	stagger := e.duration(plan.Wake.Stagger, config.DefaultStagger(),
		"wake.stagger", "plan", plan.Name)

	order := e.computeWakeOrder(plan)
	seqID := as.ID()

	for i, name := range order {
		clientCfg := e.findClientConfig(name)
		if clientCfg == nil {
			e.logger.Error("wake order references an unknown client", "client", name)
			continue
		}

		if e.GetClientState(name) == StateUp {
			continue
		}

		if clientCfg.WakePolicy == "retain_state" {
			if pre, ok := as.PreSequenceState(name); ok && pre != StateUp.String() {
				e.logger.Info("skipping wake: retain_state and the host was not up beforehand",
					"client", name, "pre_sequence_state", pre)
				continue
			}
		}

		if !e.dependenciesMet(name) {
			e.logger.Warn("skipping wake: a dependency is not up", "client", name)
			e.setClientState(name, StateFailed, &seqID)
			continue
		}

		if e.GetClientState(name) == StateDownUnverified {
			guard := e.duration(clientCfg.GuardPeriod, config.DefaultGuardPeriod(),
				"guard_period", "client", name)
			// The host may still be completing its shutdown; waking it now
			// could interrupt that and leave it in an unknown state.
			e.logger.Info("waiting out the guard period before waking an unverified host",
				"client", name, "guard_period", guard)
			if !e.sleep(guard) {
				e.clearActiveSequence()
				return
			}
		}

		e.wakeClient(name, as)

		if stagger > 0 && i < len(order)-1 {
			if !e.sleep(stagger) {
				e.clearActiveSequence()
				return
			}
		}
	}

	e.completeSequence(as)
}

// completeSequence records terminal state and releases the sequence.
func (e *Executor) completeSequence(as *ActiveSequence) {
	as.MarkCompleted(SeqStateCompleted, time.Now())
	e.saveSequence(as)
	e.releaseSequence(as)

	e.emit(Event{Type: "sequence_completed", Timestamp: time.Now(), Data: as.PlanName()})
	e.logger.Info("sequence completed", "sequence", as.ID(), "plan", as.PlanName())
}

// releaseSequence drops the sequence's client locks and clears it as active.
//
// Every terminal path calls this. Locks were previously released only on the
// successful completion of runWake, so a sequence interrupted by shutdown or
// by an abort left rows in client_locks forever — and because acquisition
// was not re-entrant, every subsequent sequence then skipped those clients
// with "locked by another sequence".
func (e *Executor) releaseSequence(as *ActiveSequence) {
	if err := e.db.ReleaseSequenceLocks(as.ID()); err != nil {
		e.logger.Error("releasing client locks", "sequence", as.ID(), "error", err)
	}
	e.clearActiveSequence()
}

// wakeClient wakes one client and verifies it came up.
func (e *Executor) wakeClient(name string, as *ActiveSequence) {
	clientCfg := e.findClientConfig(name)
	if clientCfg == nil {
		return
	}
	plan := as.Plan()
	seqID := as.ID()

	wakeTransport := clientCfg.Transport
	if clientCfg.Wake != nil && clientCfg.Wake.Transport != "" {
		wakeTransport = clientCfg.Wake.Transport
	}

	t, ok := e.transports[wakeTransport]
	if !ok {
		e.logger.Error("wake transport not found",
			"client", name, "transport", wakeTransport)
		e.setClientState(name, StateFailed, &seqID)
		return
	}

	e.setClientState(name, StateWaking, &seqID)

	if e.Mode() == ModeDryRun {
		e.logger.Info("[dry-run] would wake", "client", name)
		e.sleep(e.timings.DryRunStep)
		e.setClientState(name, StateUp, &seqID)
		return
	}

	retries := plan.Wake.Retries
	if retries < 0 {
		retries = 0
	}

	bootDeadline := e.duration(plan.Wake.BootDeadline, config.DefaultBootDeadline(),
		"wake.boot_deadline", "plan", plan.Name)

	probeInterval := e.duration(plan.Wake.ProbeInterval, config.DefaultProbeInterval(),
		"wake.probe_interval", "plan", plan.Name)
	if probeInterval <= 0 {
		// A zero probe interval would spin the CPU until the boot deadline.
		probeInterval = config.DefaultProbeInterval()
	}

	client := e.buildClient(clientCfg)
	probeTransport, hasProbe := e.transports[clientCfg.Transport]

	for attempt := 0; attempt <= retries; attempt++ {
		if _, err := t.Execute(e.ctx, client, remapAction(t, ActionWake)); err != nil {
			e.logger.Error("wake command failed",
				"client", name, "attempt", attempt, "error", err)
		}

		if !hasProbe {
			// Nothing can confirm the host came up; treat dispatch as
			// success rather than looping pointlessly to the boot deadline.
			e.logger.Warn("no probe-capable transport; wake cannot be verified",
				"client", name)
			e.setClientState(name, StateUp, &seqID)
			return
		}

		deadline := time.Now().Add(bootDeadline)
		for time.Now().Before(deadline) {
			probeState, probeErr := probeTransport.Probe(e.ctx, client)
			if probeErr == nil && probeState == StateUp {
				e.setClientState(name, StateUp, &seqID)
				e.emit(Event{Type: "client_wake_success", Timestamp: time.Now(), Data: name})
				return
			}
			if !e.sleep(probeInterval) {
				return
			}
		}
	}

	e.setClientState(name, StateFailed, &seqID)
	e.emit(Event{Type: "client_wake_failed", Timestamp: time.Now(), Data: name})
}

// executePostShutdown runs the plan's post-shutdown action, typically telling
// the UPS to cut its outlets once everything is down.
func (e *Executor) executePostShutdown(ps *config.PostShutdownConfig) {
	e.logger.Info("executing post-shutdown action",
		"action", ps.Action, "command", ps.Command, "ups", ps.UPS)

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
		Address: ps.Host,
		TransportConfig: map[string]any{
			"command":  ps.Command,
			"delay":    ps.Delay,
			"ups":      ps.UPS,
			"port":     ps.Port,
			"username": ps.Username,
			"password": ps.Password,
		},
	}

	if _, err := nutTransport.Execute(e.ctx, client, ActionOutletOff); err != nil {
		e.logger.Error("post-shutdown action failed", "error", err)
	}
}

// computeWakeOrder returns the order in which clients should be woken.
func (e *Executor) computeWakeOrder(plan *config.PlanConfig) []string {
	var order []string
	seen := make(map[string]bool)

	add := func(names []string) {
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}

	if len(plan.Wake.Stages) > 0 {
		for i := range plan.Wake.Stages {
			add(config.ResolveClientRefs(plan.Wake.Stages[i].Clients, e.cfg))
		}
		return order
	}

	// Default: reverse of shutdown order, so the things shut down last come
	// back first.
	for i := len(plan.Shutdown.Stages) - 1; i >= 0; i-- {
		add(config.ResolveClientRefs(plan.Shutdown.Stages[i].Clients, e.cfg))
	}
	return order
}

// dependenciesMet reports whether every client this one depends on is up.
func (e *Executor) dependenciesMet(clientName string) bool {
	clientCfg := e.findClientConfig(clientName)
	if clientCfg == nil {
		return true
	}
	for _, dep := range clientCfg.DependsOn {
		if e.GetClientState(dep) != StateUp {
			return false
		}
	}
	return true
}

// AbortSequence asks the running sequence to stop.
//
// Returns an error when there is nothing to abort, or when the sequence has
// already passed its point of no return.
func (e *Executor) AbortSequence(reason string) error {
	as := e.ActiveSequence()
	if as == nil {
		return fmt.Errorf("no active sequence")
	}

	if !as.RequestAbort(reason) {
		return fmt.Errorf("sequence %s has passed the point of no return and cannot be aborted", as.ID())
	}

	e.logger.Info("abort requested", "sequence", as.ID(), "reason", reason)
	return nil
}

// sleep waits for d, returning false if the executor was shut down first.
//
// Every wait in the sequence path goes through this. Bare time.Sleep calls
// previously made the wake path deaf to shutdown: a daemon asked to stop
// during a staggered wake would keep going for the rest of the stagger,
// guard and probe intervals.
func (e *Executor) sleep(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-e.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// duration resolves a configured duration, logging and falling back to def
// when the value cannot be parsed. An explicit "0s" is honoured as zero.
func (e *Executor) duration(value string, def time.Duration, field string, attrs ...any) time.Duration {
	d, err := config.Duration(value, def)
	if err != nil {
		args := append([]any{"field", field, "value", value, "default", def, "error", err}, attrs...)
		e.logger.Error("invalid duration in config; using the default", args...)
	}
	return d
}

func (e *Executor) saveIntent(intent *state.Intent) {
	if err := e.db.SaveIntent(intent); err != nil {
		e.logger.Error("persisting intent",
			"client", intent.ClientName, "action", intent.Action, "error", err)
	}
}

func ptr[T any](v T) *T { return &v }
