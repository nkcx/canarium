package simulate

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

func TestLoadTimeline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "timeline.json")

	tl := Timeline{
		Duration: 10 * time.Minute,
		Events: []TimelineEvent{
			{At: 0, Fact: "ups.charge", Value: 100.0},
			{At: 5 * time.Minute, Fact: "ups.charge", Value: 45.0},
			{At: 8 * time.Minute, Fact: "ups.charge", Value: 20.0},
		},
	}

	data, _ := json.Marshal(tl)
	os.WriteFile(path, data, 0644)

	loaded, err := LoadTimeline(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if loaded.Duration != 10*time.Minute {
		t.Errorf("duration = %v, want 10m", loaded.Duration)
	}
	if len(loaded.Events) != 3 {
		t.Errorf("events = %d, want 3", len(loaded.Events))
	}
}

func TestLoadTimelineSorted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "timeline.json")

	tl := Timeline{
		Duration: 5 * time.Minute,
		Events: []TimelineEvent{
			{At: 3 * time.Minute, Fact: "b", Value: 2.0},
			{At: 1 * time.Minute, Fact: "a", Value: 1.0},
		},
	}

	data, _ := json.Marshal(tl)
	os.WriteFile(path, data, 0644)

	loaded, err := LoadTimeline(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if loaded.Events[0].Fact != "a" {
		t.Errorf("first event fact = %s, want a (sorted by time)", loaded.Events[0].Fact)
	}
}

func TestRunSimulation(t *testing.T) {
	cfg := &config.Config{
		Plans: []config.PlanConfig{
			{
				Name: "test",
				Trigger: config.ConditionConfig{
					Condition: "numeric",
					Fact:      "ups.charge",
					Below:     ptrFloat(50),
				},
				Shutdown: config.ShutdownConfig{
					Stages: []config.StageConfig{
						{
							Name: "s1",
							When: config.ConditionConfig{Value: "true"},
							WaitTimeout: "1h",
							WaitPolicy:  "skip",
						},
					},
				},
				Wake: config.WakeConfig_{
					Gate: config.ConditionConfig{
						Condition: "numeric",
						Fact:      "ups.charge",
						Above:     ptrFloat(80),
					},
				},
			},
		},
	}

	tl := &Timeline{
		Duration: 5 * time.Minute,
		Events: []TimelineEvent{
			{At: 0, Fact: "ups.charge", Value: 100.0},
			{At: 1 * time.Minute, Fact: "ups.charge", Value: 45.0},
			{At: 3 * time.Minute, Fact: "ups.charge", Value: 90.0},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	result, err := Run(cfg, tl, "test", logger)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(result.Triggers) == 0 {
		t.Error("expected at least one trigger event")
	}
	if len(result.Stages) == 0 {
		t.Error("expected at least one stage event")
	}
}

func TestRunSimulationPlanNotFound(t *testing.T) {
	cfg := &config.Config{}
	tl := &Timeline{Duration: time.Minute}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	_, err := Run(cfg, tl, "nonexistent", logger)
	if err == nil {
		t.Error("expected error for nonexistent plan")
	}
}

func ptrFloat(f float64) *float64 {
	return &f
}
