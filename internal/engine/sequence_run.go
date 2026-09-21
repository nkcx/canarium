package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
	"github.com/nkcx/canarium/internal/netutil"
	"github.com/nkcx/canarium/internal/state"
)

// executeSequence runs a plan from the beginning.
// newSequenceFor builds the sequence record for a plan, without any work
// that could block.
//
// Kept separate from executeSequence so the caller can claim the active-
// sequence slot synchronously, before the slow parts run.
func (e *Executor) newSequenceFor(plan *config.PlanConfig) *ActiveSequence {
	now := time.Now()

	preState := make(map[string]string, len(e.cfg.Clients))
	for _, c := range e.cfg.Clients {
		preState[c.Name] = e.GetClientState(c.Name).String()
	}

	return newActiveSequence(&state.Sequence{
		ID:               fmt.Sprintf("seq_%d", now.UnixNano()),
		PlanName:         plan.Name,
		State:            SeqStateShuttingDown,
		CurrentStage:     0,
		StartedAt:        now,
		PreSequenceState: preState,
	}, plan)
}

// executeSequence runs a plan that has already claimed the active slot.
func (e *Executor) executeSequence(as *ActiveSequence) {
	// Pin addresses while the network is still whole. A power event often
	// takes out the DNS server too, and by wake time the names in the config
	// may no longer resolve. This can take seconds per unresolvable name,
	// which is why the slot was claimed before we got here.
	as.SetResolvedAddrs(e.resolveClientAddresses(e.ctx))

	// Record the configuration in effect, so the journal says what the
	// daemon was actually running when it acted.
	as.SetConfigSnapshot(e.configSnapshot())

	e.saveSequence(as)
	e.logger.Info("sequence started", "sequence", as.ID(), "plan", as.PlanName())

	e.runShutdownStages(as)
}

// resumeSequence continues a sequence that was interrupted by a restart,
// picking up at the first stage with no recorded completion.
func (e *Executor) resumeSequence(as *ActiveSequence) {
	// Where a sequence resumes depends on what phase it was in, not only on
	// which stages have records.
	//
	// An earlier version ignored the persisted state entirely and always
	// called runShutdownStages. Two ways that went wrong:
	//
	//   - A sequence aborted during stage 0 has no records for stages 1..N,
	//     so resume computed "start at stage 1" and shut down every host the
	//     operator had just explicitly spared.
	//
	//   - A sequence already past its shutdown stages fell straight through
	//     the (empty) stage loop into executePostShutdown, telling the UPS to
	//     cut its outlets a second time — while the fleet was booting on
	//     returning mains.
	switch as.State() {
	case SeqStateAborting, SeqStateAborted:
		// The operator, or the plan's own abort condition, called this off
		// before the restart. Honour that: settle anything still shutting
		// down and hand over to the wake plan.
		e.logger.Info("resuming an aborted sequence; no further stages will run",
			"sequence", as.ID(), "plan", as.PlanName())
		e.resumeAbort(as)
		return

	case SeqStateWakeGate, SeqStateWaking:
		// Shutdown is finished and post-shutdown has already run. Rejoin at
		// the wake gate.
		e.logger.Info("resuming at the wake gate", "sequence", as.ID(), "plan", as.PlanName())
		e.runWake(as)
		return
	}

	completed, err := e.db.GetCompletedStages(e.ctx, as.ID())
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

	// Reconcile the intent journal before acting. An intent still marked
	// "dispatching" means the daemon died between recording the intent and
	// the transport returning, so we do not know whether the command was
	// actually delivered. SPEC §8.4 calls for probing before deciding.
	e.reconcileIntents(as)

	e.logger.Info("resuming sequence", "sequence", as.ID(), "stage", resumeAt)
	e.runShutdownStages(as)
}

// resumeAbort continues an abort that was interrupted by a restart.
func (e *Executor) resumeAbort(as *ActiveSequence) {
	e.waitForShuttingDown(as)

	as.SetState(SeqStateWakeGate)
	e.saveSequence(as)
	e.runWake(as)
}

// reconcileIntents inspects the journal for actions whose outcome is unknown.
//
// The intents table has been written since the first commit and never read.
// An intent left at "dispatching" means the daemon died after recording its
// intention to act and before the transport returned — so the command may or
// may not have reached the host. Probing tells us which, and a host that
// turns out to be down does not need shutting down again.
func (e *Executor) reconcileIntents(as *ActiveSequence) {
	pending, err := e.db.PendingIntents(e.ctx, as.ID())
	if err != nil {
		e.logger.Error("reading pending intents", "sequence", as.ID(), "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	seqID := as.ID()

	for _, intent := range pending {
		clientCfg := e.findClientConfig(intent.ClientName)
		if clientCfg == nil {
			e.logger.Warn("pending intent names a client no longer in the config",
				"client", intent.ClientName, "action", intent.Action)
			continue
		}

		e.logger.Warn("an action was in flight when the daemon stopped; "+
			"probing to find out whether it took effect",
			"client", intent.ClientName, "action", intent.Action)

		transport, ok := e.transports[clientCfg.Transport]
		if !ok {
			continue
		}

		ctx, cancel := context.WithTimeout(e.ctx, probeTimeout)
		probeState, probeErr := transport.Probe(ctx, e.buildClientFor(as, clientCfg))
		cancel()

		outcome := "unknown"
		switch {
		case probeErr != nil:
			// Cannot tell. Leave the client where it is; the stage loop will
			// re-dispatch, and shutdown commands are idempotent.
			e.setClientState(intent.ClientName, StateDownUnverified, &seqID)
			outcome = "indeterminate"
		case probeState == StateDown:
			// The command landed.
			e.setClientState(intent.ClientName, StateDown, &seqID)
			outcome = "took effect"
		case probeState == StateUp:
			// Still up, so it will be re-dispatched by the stage loop.
			e.setClientState(intent.ClientName, StateUp, &seqID)
			outcome = "did not take effect"
		}

		intent.Status = state.IntentReconciled
		intent.Result = &state.ActionResult{
			Success: probeState == StateDown,
			Message: "reconciled after restart: " + outcome,
		}
		e.saveIntent(intent)

		e.logger.Info("reconciled an in-flight action",
			"client", intent.ClientName, "action", intent.Action, "outcome", outcome)
	}
}

// runShutdownStages walks a plan's shutdown stages in order.
func (e *Executor) runShutdownStages(as *ActiveSequence) {
	plan := as.Plan()

	// The index is advanced explicitly rather than by a for-post statement:
	// wait_policy "hold" has to re-enter the same stage, and expressing that
	// as `i--; continue` against an implicit `i++` is how the original code
	// arrived at a loop nobody could reason about.
	i := as.CurrentStage()
	for i < len(plan.Shutdown.Stages) {
		stage := &plan.Shutdown.Stages[i]

		as.SetCurrentStage(i)
		e.saveSequence(as)

		if e.shouldAbort(as) {
			e.handleAbort(as)
			return
		}

		outcome := e.waitForStage(as, stage)

		if outcome == stageAborted {
			e.handleAbort(as)
			return
		}
		if outcome == stageCancelled {
			// The daemon is stopping. Leave the sequence marked in-progress
			// so it resumes on restart, but drop the in-memory handle.
			e.logger.Info("sequence interrupted by shutdown", "sequence", as.ID())
			e.clearActiveSequence()
			return
		}
		if outcome == stageTimedOut {
			if e.handleStageTimeout(as, stage, i) {
				// Policy is hold: wait on this stage again.
				continue
			}
			i++
			continue
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

		i++
	}

	if plan.Shutdown.PostShutdown != nil {
		// Exactly once per sequence. Telling the UPS to cut its outlets a
		// second time — which a resume used to do — would drop power to a
		// fleet that is already booting on returning mains.
		if as.MarkPostShutdownRun() {
			e.executePostShutdown(plan.Shutdown.PostShutdown)
			e.saveSequence(as)
		} else {
			e.logger.Info("post-shutdown has already run for this sequence; skipping",
				"sequence", as.ID())
		}
	}

	as.SetState(SeqStateWakeGate)
	e.saveSequence(as)
	e.runWake(as)
}

// handleStageTimeout applies a stage's wait_policy after its entry condition
// failed to hold within wait_timeout.
//
// Returns true when the caller should wait on the same stage again, which is
// what wait_policy "hold" means.
//
// Note what skipping means: the stage's clients are never shut down, and the
// sequence carries on to the next stage as though nothing happened. That is
// the documented behaviour of the default policy, but it used to happen
// silently — no event, and escalate differed from skip only by emitting
// stage_timeout. An operator reviewing the journal could not tell that half
// the rack had been left running.
func (e *Executor) handleStageTimeout(as *ActiveSequence, stage *config.StageConfig, idx int) bool {
	policy := stage.WaitPolicy
	if policy == "" {
		policy = config.WaitPolicySkip
	}

	if policy == config.WaitPolicyHold {
		// Re-enter the wait. waitForStage polls the abort condition, the
		// operator proceed request and the daemon context, so holding stays
		// interruptible rather than wedging the sequence.
		e.logger.Warn("stage is holding: its entry condition has not been met "+
			"and wait_policy is hold; it will wait until the condition holds, "+
			"an operator forces it through, or the sequence is aborted",
			"stage", stage.Name, "sequence", as.ID())

		if as.HeldStage() != stage.Name {
			as.SetHeldStage(stage.Name)
			e.emit(Event{Type: "stage_held", Timestamp: time.Now(), Data: stage.Name})
		}
		as.SetCurrentStage(idx)
		return true // re-enter the wait on this same stage
	}

	clients := config.ResolveClientRefs(stage.Clients, e.cfg)
	e.logger.Warn("stage skipped: its entry condition was not met within wait_timeout, "+
		"so these clients will NOT be shut down",
		"stage", stage.Name,
		"policy", policy,
		"clients", clients,
		"sequence", as.ID())

	if policy == config.WaitPolicyEscalate {
		e.emit(Event{Type: "stage_timeout", Timestamp: time.Now(), Data: stage.Name})
	}

	e.emit(Event{
		Type:      "stage_skipped",
		Timestamp: time.Now(),
		Data: map[string]any{
			"stage":   stage.Name,
			"policy":  policy,
			"clients": clients,
			"reason":  "entry condition not met within wait_timeout",
		},
	})

	// Record the skip so the journal shows why these hosts were left up.
	now := time.Now()
	record := &state.StageRecord{
		SequenceID:  as.ID(),
		StageIndex:  idx,
		StageName:   stage.Name,
		StartedAt:   now,
		CompletedAt: &now,
		Clients:     make(map[string]state.ClientResult, len(clients)),
	}
	for _, name := range clients {
		record.Clients[name] = state.ClientResult{
			State:       e.GetClientState(name).String(),
			StartedAt:   now.Format(time.RFC3339Nano),
			CompletedAt: now.Format(time.RFC3339Nano),
			Error:       "stage skipped: entry condition not met within wait_timeout",
		}
	}
	if err := e.db.SaveStageRecord(e.ctx, record); err != nil {
		e.logger.Error("persisting skipped stage record", "stage", stage.Name, "error", err)
	}

	return false // move on to the next stage
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
			as.SetHeldStage("")
			return stageProceed
		}
		if forced, reason := as.ConsumeProceed(); forced {
			e.logger.Warn("stage forced through by operator despite an unmet entry condition",
				"stage", stage.Name, "reason", reason, "sequence", as.ID())
			e.emit(Event{
				Type:      "stage_forced",
				Timestamp: time.Now(),
				Data:      map[string]any{"stage": stage.Name, "reason": reason},
			})
			as.SetHeldStage("")
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
	if err := e.db.SaveStageRecord(e.ctx, record); err != nil {
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
	if err := e.db.SaveStageRecord(e.ctx, record); err != nil {
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
	acquired, err := e.db.AcquireClientLock(e.ctx, name, seqID)
	if err != nil {
		e.logger.Error("acquiring client lock", "client", name, "error", err)
		return finish(StateFailed, "could not acquire client lock: "+err.Error())
	}
	if !acquired {
		holder, _ := e.db.ClientLockHolder(e.ctx, name)
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
		Status:     state.IntentDispatching,
	}
	e.saveIntent(intent)

	e.setClientState(name, StateShuttingDown, &seqID)

	if e.Mode() == ModeDryRun {
		e.logger.Info("[dry-run] would shut down", "client", name)
		intent.Status = state.IntentDispatched
		intent.Result = &state.ActionResult{Success: true, Message: "dry-run"}
		e.saveIntent(intent)

		e.sleep(e.timings.DryRunStep)
		e.setClientState(name, StateDown, &seqID)
		return finish(StateDown, "")
	}

	client := e.buildClientFor(as, clientCfg)

	ctx, cancel := context.WithTimeout(e.ctx, budget)
	defer cancel()

	actionResult, execErr := t.Execute(ctx, client, remapAction(t, ActionShutdown))
	intent.Status = state.IntentDispatched
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
	as.MarkAborted()
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

	for e.evaluator.Evaluate(&plan.Wake.Gate, time.Now()) != facts.True {
		// An operator can call off a sequence while it waits at the gate.
		// runWake previously never looked, so the API acknowledged the abort
		// and the daemon woke the fleet anyway.
		if requested, reason := as.AbortRequested(); requested {
			e.logger.Info("abandoning the wake gate: abort requested",
				"sequence", as.ID(), "reason", reason)
			as.MarkAborted()
			e.completeSequence(as)
			return
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
		if requested, reason := as.AbortRequested(); requested {
			e.logger.Info("stopping the wake sequence: abort requested",
				"sequence", as.ID(), "reason", reason, "woken", i, "remaining", len(order)-i)
			as.MarkAborted()
			e.completeSequence(as)
			return
		}

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

			// The guard period exists because a host we could not confirm
			// down may still be completing its shutdown, and waking it
			// mid-flight leaves it in an unknown state. What matters is time
			// elapsed since it settled, not time spent waiting here.
			//
			// Sleeping the full period per client made the wait cumulative:
			// ten clients at the default 60s meant ten minutes of sequential
			// sleeping even when the outage had ended hours earlier and
			// every machine had been cold the whole time.
			remaining := guard - e.timeSinceSettled(name)
			if remaining > 0 {
				e.logger.Info("waiting out the remainder of the guard period",
					"client", name, "guard_period", guard, "remaining", remaining)
				if !e.sleep(remaining) {
					e.clearActiveSequence()
					return
				}
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
//
// An aborted sequence is recorded as aborted. It previously finished as
// "completed" regardless, so the journal claimed a clean run for a sequence
// an operator had called off — and SeqStateAborted was never written at all.
func (e *Executor) completeSequence(as *ActiveSequence) {
	finalState := SeqStateCompleted
	if as.WasAborted() {
		finalState = SeqStateAborted
	}

	as.MarkCompleted(finalState, time.Now())
	e.saveSequence(as)
	e.releaseSequence(as)

	e.emit(Event{Type: "sequence_completed", Timestamp: time.Now(),
		Data: map[string]any{"plan": as.PlanName(), "state": finalState}})
	e.logger.Info("sequence finished",
		"sequence", as.ID(), "plan", as.PlanName(), "state", finalState)
}

// releaseSequence drops the sequence's client locks and clears it as active.
//
// Every terminal path calls this. Locks were previously released only on the
// successful completion of runWake, so a sequence interrupted by shutdown or
// by an abort left rows in client_locks forever — and because acquisition
// was not re-entrant, every subsequent sequence then skipped those clients
// with "locked by another sequence".
func (e *Executor) releaseSequence(as *ActiveSequence) {
	if err := e.db.ReleaseSequenceLocks(e.ctx, as.ID()); err != nil {
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

	client := e.buildClientFor(as, clientCfg)
	probe := e.wakeProbe(clientCfg)

	for attempt := 0; attempt <= retries; attempt++ {
		if _, err := t.Execute(e.ctx, client, remapAction(t, ActionWake)); err != nil {
			e.logger.Error("wake command failed",
				"client", name, "attempt", attempt, "error", err)
		}

		if probe == nil {
			// Nothing can confirm the host came up. Report the dispatch and
			// stop, rather than looping to the boot deadline on every retry.
			e.logger.Warn("wake cannot be verified for this client; "+
				"set an address and a probe port to confirm it comes back",
				"client", name)
			e.setClientState(name, StateUp, &seqID)
			return
		}

		deadline := time.Now().Add(bootDeadline)
		for time.Now().Before(deadline) {
			if requested, _ := as.AbortRequested(); requested {
				e.logger.Info("abandoning wake verification: abort requested",
					"client", name)
				return
			}

			probeState, probeErr := probe(e.ctx, client)
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

// probeFunc reports a client's power state.
type probeFunc func(context.Context, *Client) (ClientState, error)

// wakeProbe returns something that can confirm a client came back up, or nil
// if nothing can.
//
// The client's own transport is preferred, but only when it actually
// advertises ActionProbe. An earlier version took the transport's presence in
// the registry as proof it could probe — so a client whose transport is wol
// or nut (neither of which can probe) had every wake attempt spin to the full
// boot deadline before being marked failed. With the default three retries
// and a five-minute deadline, that was fifteen minutes per client, and the
// client was marked failed regardless.
//
// When the transport cannot probe but the client has an address, fall back to
// a plain TCP check. A machine that has just been woken is expected to start
// answering on something.
func (e *Executor) wakeProbe(c *config.ClientConfig) probeFunc {
	if transport, ok := e.transports[c.Transport]; ok && transportCan(transport, ActionProbe) {
		return transport.Probe
	}

	if c.Address == "" {
		return nil
	}

	port := defaultWakeProbePort
	if c.Probe != nil && c.Probe.Port != 0 {
		port = c.Probe.Port
	}

	e.logger.Info("the configured transport cannot probe; "+
		"verifying wake with a TCP check instead",
		"client", c.Name, "port", port)

	return func(ctx context.Context, client *Client) (ClientState, error) {
		addr := netutil.HostPort(client.Address, port)

		timeout := client.ProbeConfig.Timeout
		if timeout <= 0 {
			timeout = defaultProbeTimeout
		}

		switch reach, err := netutil.ProbeTCP(ctx, addr, timeout); reach {
		case netutil.Reachable:
			return StateUp, nil
		case netutil.Unreachable:
			return StateDown, nil
		default:
			return StateUnknown, err
		}
	}
}

// transportCan reports whether a transport advertises an action.
func transportCan(t Transport, action ActionType) bool {
	for _, capability := range t.Capabilities() {
		if capability.Action == action {
			return true
		}
	}
	return false
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

	// Honour the configured action. This was hardcoded to outlet_off, so a
	// plan asking for anything else silently got a power cut instead.
	action := ActionOutletOff
	switch strings.ToLower(strings.TrimSpace(ps.Action)) {
	case "", "upscmd", "outlet_off", "load_off":
		action = ActionOutletOff
	case "outlet_on", "load_on":
		action = ActionOutletOn
	default:
		e.logger.Error("unrecognised post_shutdown action; treating it as outlet_off",
			"action", ps.Action)
	}

	if _, err := nutTransport.Execute(e.ctx, client, action); err != nil {
		e.logger.Error("post-shutdown action failed", "action", action, "error", err)
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

// ForceStage releases a stage that is waiting on an entry condition, so an
// operator can push a held sequence forward.
func (e *Executor) ForceStage(reason string) error {
	as := e.ActiveSequence()
	if as == nil {
		return fmt.Errorf("no active sequence")
	}

	as.RequestProceed(reason)
	e.logger.Info("operator requested that the current stage proceed",
		"sequence", as.ID(), "reason", reason)
	return nil
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
	if err := e.db.SaveIntent(e.ctx, intent); err != nil {
		e.logger.Error("persisting intent",
			"client", intent.ClientName, "action", intent.Action, "error", err)
	}
}

func ptr[T any](v T) *T { return &v }
