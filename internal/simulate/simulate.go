// Package simulate replays a scripted fact timeline against a plan's policy.
//
// # What this models
//
// Simulation covers the decisions Canarium makes from facts: when a plan's
// trigger fires, when each stage's entry condition is satisfied, when the
// abort condition wins, when the point of no return is crossed, and when the
// wake gate opens. It uses the same condition evaluator, the same fact store
// with the same staleness rules, and the same client reference resolution as
// the daemon, so the answers to "would this config have acted, and when"
// match what the executor would decide.
//
// # What this does not model
//
// It does not execute anything, so it says nothing about how long a host
// takes to shut down, whether a transport authenticates, or whether a probe
// confirms a host is down. Per-client budgets, guard periods, wake stagger
// and boot deadlines are not simulated — they concern execution, not policy.
// A plan that simulates cleanly can still fail on a bad credential; that is
// what `canarium doctor` is for.
package simulate

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// SimulationResult records what the plan would have done.
type SimulationResult struct {
	Plan string `json:"plan"`

	Triggers  []Event `json:"triggers"`
	Stages    []Event `json:"stages"`
	Aborts    []Event `json:"aborts"`
	PONR      []Event `json:"ponr"`
	WakeGates []Event `json:"wake_gates"`
	Skipped   []Event `json:"skipped"`

	// Clients that would have been shut down, in stage order.
	ShutdownOrder []string `json:"shutdown_order"`

	// NotShutDown lists clients belonging to stages that were skipped
	// because their entry condition never held.
	NotShutDown []string `json:"not_shut_down"`

	// Completed reports whether the simulation reached the wake gate.
	Completed bool `json:"completed"`
}

// Event is something the plan did, at a point in simulated time.
type Event struct {
	At      time.Duration `json:"at"`
	Stage   string        `json:"stage,omitempty"`
	Detail  string        `json:"detail,omitempty"`
	Clients []string      `json:"clients,omitempty"`
}

// Run replays a timeline against one plan.
func Run(cfg *config.Config, timeline *Timeline, planName string, logger *slog.Logger) (*SimulationResult, error) {
	plan := findPlan(cfg, planName)
	if plan == nil {
		return nil, fmt.Errorf("plan %q not found in config", planName)
	}

	store, err := buildStore(cfg, timeline)
	if err != nil {
		return nil, err
	}

	evaluator := conditions.NewEvaluator(store)
	sim := &simulation{
		cfg:       cfg,
		plan:      plan,
		timeline:  timeline,
		store:     store,
		evaluator: evaluator,
		logger:    logger,
		result:    &SimulationResult{Plan: planName},
	}

	return sim.run(), nil
}

// buildStore creates a fact store registering every fact the timeline
// touches, typed from the value supplied.
//
// The previous implementation registered every fact as "number" regardless
// of its value and computed its instance name by slicing the key, which
// panicked on a key with no dot. Set-valued facts such as ups.status arrive
// from JSON as []any, which the evaluator then could not match against
// `contains:` — so simulating the shipped example's trigger silently never
// fired.
func buildStore(cfg *config.Config, timeline *Timeline) (*facts.Store, error) {
	store := facts.NewStore()

	// Group declarations by source instance so each is registered once with
	// a single poll interval.
	type decl struct {
		interval time.Duration
		entries  map[string]facts.FactDeclaration
	}
	sources := make(map[string]*decl)

	for _, evt := range timeline.Events {
		source, name, ok := splitFactKey(evt.Fact)
		if !ok {
			return nil, fmt.Errorf("fact key %q is not qualified as <source>.<name>", evt.Fact)
		}

		s, exists := sources[source]
		if !exists {
			s = &decl{
				interval: sourcePollInterval(cfg, source),
				entries:  make(map[string]facts.FactDeclaration),
			}
			sources[source] = s
		}

		if evt.Value == nil {
			// A silence marker carries no type; the declaration comes from
			// whichever event set the fact in the first place.
			if _, known := s.entries[name]; !known {
				s.entries[name] = facts.FactDeclaration{Name: name, Type: "string"}
			}
			continue
		}
		s.entries[name] = facts.FactDeclaration{Name: name, Type: factTypeOf(evt.Value)}
	}

	for source, s := range sources {
		entries := make([]facts.FactDeclaration, 0, len(s.entries))
		for _, e := range s.entries {
			entries = append(entries, e)
		}
		store.RegisterSource(source, s.interval, entries)
	}

	return store, nil
}

// sourcePollInterval finds a source's configured poll interval, so staleness
// behaves in simulation as it does in production.
func sourcePollInterval(cfg *config.Config, source string) time.Duration {
	const fallback = 15 * time.Second

	for _, s := range cfg.Sources {
		if s.Name == source {
			if d, err := config.Duration(s.PollInterval, fallback); err == nil {
				return d
			}
			return fallback
		}

		// Sources declare their instances inside their own config block, and
		// facts are keyed by instance name rather than source name.
		instances, ok := s.Config["instances"].([]any)
		if !ok {
			continue
		}
		for _, raw := range instances {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := m["name"].(string); name != source {
				continue
			}
			if interval, _ := m["poll_interval"].(string); interval != "" {
				if d, err := config.Duration(interval, fallback); err == nil {
					return d
				}
			}
			return fallback
		}
	}

	return fallback
}

// factTypeOf infers a fact's declared type from a timeline value.
func factTypeOf(value any) string {
	switch value.(type) {
	case bool:
		return "bool"
	case float64, int, int64:
		return "number"
	case []any, []string:
		return "set"
	default:
		return "string"
	}
}

type simulation struct {
	cfg       *config.Config
	plan      *config.PlanConfig
	timeline  *Timeline
	store     *facts.Store
	evaluator *conditions.Evaluator
	logger    *slog.Logger
	result    *SimulationResult

	// base is the wall-clock instant simulated time is measured from.
	//
	// Simulated timestamps are produced with base.Add(elapsed), which
	// preserves Go's monotonic clock reading, so dwell tracking advances
	// with simulated time rather than with the microseconds this loop
	// actually takes. Before dwell was fixed to measure from the supplied
	// timestamp, every `for:` condition was unreachable in simulation.
	base time.Time
}

func (s *simulation) run() *SimulationResult {
	s.base = time.Now()

	var (
		triggered      bool
		aborted        bool
		ponrCrossed    bool
		currentStage   int
		stageEnteredAt time.Duration
	)

	stages := s.plan.Shutdown.Stages
	eventIdx := 0
	step := s.timeline.Step.Duration()

	// current holds each fact's value as of the most recent timeline event
	// for it.
	//
	// A timeline event means "this fact holds this value from here on", not
	// "one sample arrived and the source then went quiet". A real source
	// re-reports every poll interval, so values are re-asserted on every
	// tick and stay fresh. Without this, any fact left unchanged for longer
	// than two poll intervals went stale mid-simulation and silently stopped
	// satisfying conditions — which made every dwell longer than the poll
	// interval unsatisfiable and, in the shipped example config, meant no
	// stage ever ran.
	//
	// A null value models the opposite case explicitly: the source has gone
	// silent, so the fact stops being refreshed and goes stale on schedule.
	current := make(map[string]any)

	for elapsed := time.Duration(0); elapsed <= s.timeline.Duration.Duration(); elapsed += step {
		now := s.base.Add(elapsed)

		// Apply every timeline event due by this instant.
		for eventIdx < len(s.timeline.Events) &&
			s.timeline.Events[eventIdx].At.Duration() <= elapsed {
			evt := s.timeline.Events[eventIdx]
			if evt.Value == nil {
				delete(current, evt.Fact)
			} else {
				current[evt.Fact] = normaliseValue(evt.Value)
			}
			eventIdx++
		}

		for key, value := range current {
			s.store.Update(key, value, now)
		}

		// Freshness is evaluated exactly as the daemon does, so a source
		// that has gone silent goes stale here too and its conditions stop
		// being satisfiable.
		s.store.RefreshQuality(now)

		if !triggered {
			if s.evaluator.Evaluate(&s.plan.Trigger, now) == facts.True {
				triggered = true
				stageEnteredAt = elapsed
				s.record(&s.result.Triggers, Event{At: elapsed, Detail: s.plan.Name})
				s.logger.Info("TRIGGER", "plan", s.plan.Name, "at", elapsed)
			}
			continue
		}

		// Abort is live until the point of no return is crossed — the same
		// ordering runShutdownStages uses.
		if !aborted && !ponrCrossed && s.plan.Abort != nil {
			if s.evaluator.Evaluate(s.plan.Abort, now) == facts.True {
				aborted = true
				s.record(&s.result.Aborts, Event{At: elapsed, Detail: s.plan.Name})
				s.logger.Info("ABORT", "plan", s.plan.Name, "at", elapsed)
			}
		}

		if !aborted && currentStage < len(stages) {
			stage := &stages[currentStage]

			if s.evaluator.Evaluate(&stage.When, now) == facts.True {
				// PONR is crossed when the stage dispatches, not when it is
				// entered — matching the executor.
				if stage.PointOfNoReturn && !ponrCrossed {
					ponrCrossed = true
					s.record(&s.result.PONR, Event{At: elapsed, Stage: stage.Name})
					s.logger.Info("PONR", "stage", stage.Name, "at", elapsed)
				}

				clients := config.ResolveClientRefs(stage.Clients, s.cfg)
				s.record(&s.result.Stages, Event{At: elapsed, Stage: stage.Name, Clients: clients})
				s.result.ShutdownOrder = append(s.result.ShutdownOrder, clients...)
				s.logger.Info("STAGE", "plan", s.plan.Name, "stage", stage.Name,
					"at", elapsed, "clients", clients)

				currentStage++
				stageEnteredAt = elapsed
			} else if s.stageTimedOut(stage, elapsed, stageEnteredAt) {
				clients := config.ResolveClientRefs(stage.Clients, s.cfg)
				s.record(&s.result.Skipped, Event{
					At: elapsed, Stage: stage.Name, Clients: clients,
					Detail: "entry condition not met within wait_timeout",
				})
				s.result.NotShutDown = append(s.result.NotShutDown, clients...)
				s.logger.Warn("STAGE SKIPPED", "stage", stage.Name, "at", elapsed,
					"clients", clients)

				currentStage++
				stageEnteredAt = elapsed
			}
		}

		if aborted || currentStage >= len(stages) {
			if s.evaluator.Evaluate(&s.plan.Wake.Gate, now) == facts.True {
				s.record(&s.result.WakeGates, Event{At: elapsed, Detail: s.plan.Name})
				s.logger.Info("WAKE_GATE", "plan", s.plan.Name, "at", elapsed)
				s.result.Completed = true
				return s.result
			}
		}
	}

	return s.result
}

// stageTimedOut reports whether a stage has waited past its wait_timeout.
//
// hold waits indefinitely, so it never times out here. skip and escalate
// both abandon the stage, which the result records as clients that would not
// have been shut down — the outcome most worth seeing in a simulation.
func (s *simulation) stageTimedOut(stage *config.StageConfig, elapsed, enteredAt time.Duration) bool {
	if stage.WaitPolicy == config.WaitPolicyHold {
		return false
	}

	timeout, err := config.Duration(stage.WaitTimeout, config.DefaultWaitTimeout())
	if err != nil {
		timeout = config.DefaultWaitTimeout()
	}
	return elapsed-enteredAt >= timeout
}

func (s *simulation) record(dst *[]Event, e Event) { *dst = append(*dst, e) }

// normaliseValue converts a JSON-decoded value into what a real source would
// have produced, so conditions behave identically.
func normaliseValue(value any) any {
	if list, ok := value.([]any); ok {
		strs := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				// Mixed list: leave it alone rather than losing information.
				return value
			}
			strs = append(strs, s)
		}
		return strs
	}
	return value
}

func findPlan(cfg *config.Config, name string) *config.PlanConfig {
	for i := range cfg.Plans {
		if cfg.Plans[i].Name == name {
			return &cfg.Plans[i]
		}
	}
	return nil
}
