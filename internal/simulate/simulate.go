package simulate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

type Timeline struct {
	Duration time.Duration `json:"duration"`
	Events   []TimelineEvent `json:"events"`
}

type TimelineEvent struct {
	At    time.Duration `json:"at"`
	Fact  string        `json:"fact"`
	Value any           `json:"value"`
}

type SimulationResult struct {
	Triggers  []TriggerEvent `json:"triggers"`
	Aborts    []AbortEvent   `json:"aborts"`
	Stages    []StageEvent   `json:"stages"`
	WakeGates []WakeGateEvent `json:"wake_gates"`
}

type TriggerEvent struct {
	Plan string        `json:"plan"`
	At   time.Duration `json:"at"`
}

type AbortEvent struct {
	Plan string        `json:"plan"`
	At   time.Duration `json:"at"`
}

type StageEvent struct {
	Plan  string        `json:"plan"`
	Stage string        `json:"stage"`
	At    time.Duration `json:"at"`
}

type WakeGateEvent struct {
	Plan string        `json:"plan"`
	At   time.Duration `json:"at"`
}

func LoadTimeline(path string) (*Timeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading timeline: %w", err)
	}

	var tl Timeline
	if err := json.Unmarshal(data, &tl); err != nil {
		return nil, fmt.Errorf("parsing timeline: %w", err)
	}

	sort.Slice(tl.Events, func(i, j int) bool {
		return tl.Events[i].At < tl.Events[j].At
	})

	return &tl, nil
}

func Run(cfg *config.Config, timeline *Timeline, planName string, logger *slog.Logger) (*SimulationResult, error) {
	store := facts.NewStore()
	evaluator := conditions.NewEvaluator(store)

	var targetPlan *config.PlanConfig
	for i := range cfg.Plans {
		if cfg.Plans[i].Name == planName {
			targetPlan = &cfg.Plans[i]
			break
		}
	}
	if targetPlan == nil {
		return nil, fmt.Errorf("plan %q not found", planName)
	}

	result := &SimulationResult{}

	startTime := time.Now()
	step := 1 * time.Second
	eventIdx := 0

	triggered := false
	aborted := false
	ponrCrossed := false
	currentStage := 0

	for elapsed := time.Duration(0); elapsed <= timeline.Duration; elapsed += step {
		simTime := startTime.Add(elapsed)

		for eventIdx < len(timeline.Events) && timeline.Events[eventIdx].At <= elapsed {
			evt := timeline.Events[eventIdx]
			store.Update(evt.Fact, evt.Value, simTime)
			eventIdx++
		}

		store.RefreshQuality(simTime)

		if !triggered {
			if evaluator.Evaluate(&targetPlan.Trigger, simTime) == facts.True {
				triggered = true
				result.Triggers = append(result.Triggers, TriggerEvent{
					Plan: planName,
					At:   elapsed,
				})
				logger.Info("TRIGGER", "plan", planName, "at", elapsed)
			}
		}

		if triggered && !aborted && !ponrCrossed && targetPlan.Abort != nil {
			if evaluator.Evaluate(targetPlan.Abort, simTime) == facts.True {
				aborted = true
				result.Aborts = append(result.Aborts, AbortEvent{
					Plan: planName,
					At:   elapsed,
				})
				logger.Info("ABORT", "plan", planName, "at", elapsed)
			}
		}

		if triggered && !aborted && currentStage < len(targetPlan.Shutdown.Stages) {
			stage := &targetPlan.Shutdown.Stages[currentStage]

			if stage.PointOfNoReturn && !ponrCrossed {
				ponrCrossed = true
				logger.Info("PONR", "stage", stage.Name, "at", elapsed)
			}

			if evaluator.Evaluate(&stage.When, simTime) == facts.True {
				result.Stages = append(result.Stages, StageEvent{
					Plan:  planName,
					Stage: stage.Name,
					At:    elapsed,
				})
				logger.Info("STAGE", "plan", planName, "stage", stage.Name, "at", elapsed)
				currentStage++
			}
		}

		if (aborted || currentStage >= len(targetPlan.Shutdown.Stages)) && triggered {
			if evaluator.Evaluate(&targetPlan.Wake.Gate, simTime) == facts.True {
				result.WakeGates = append(result.WakeGates, WakeGateEvent{
					Plan: planName,
					At:   elapsed,
				})
				logger.Info("WAKE_GATE", "plan", planName, "at", elapsed)
				break
			}
		}
	}

	return result, nil
}
