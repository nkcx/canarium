package simulate

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeTimeline(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "timeline.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing timeline: %v", err)
	}
	return path
}

func floatPtr(f float64) *float64 { return &f }

// outageConfig mirrors the shape of examples/basic.yaml: a status-driven
// trigger with a dwell, charge-driven stages, and a dwelled wake gate.
func outageConfig() *config.Config {
	return &config.Config{
		Canarium: config.DefaultCanariumConfig(),
		Sources: []config.SourceConfig{{
			Name: "ups", Type: "nut",
			Config: map[string]any{
				"instances": []any{
					map[string]any{"name": "rack_ups", "poll_interval": "15s"},
				},
			},
		}},
		Clients: []config.ClientConfig{
			{Name: "compute1", Transport: "ssh", Tags: []string{"compute"},
				FeedPolicy: "any", WakePolicy: "power_state"},
			{Name: "storage1", Transport: "ssh", Tags: []string{"storage"},
				FeedPolicy: "any", WakePolicy: "power_state"},
		},
		Plans: []config.PlanConfig{{
			Name: "outage",
			Trigger: config.ConditionConfig{
				Condition: "state", Fact: "rack_ups.status", Contains: "OB", For: "30s",
			},
			Abort: &config.ConditionConfig{
				Condition: "state", Fact: "rack_ups.status", Contains: "OL", For: "60s",
			},
			Shutdown: config.ShutdownConfig{Stages: []config.StageConfig{
				{
					Name:        "compute",
					When:        config.ConditionConfig{Condition: "numeric", Fact: "rack_ups.battery.charge", Below: floatPtr(50)},
					Clients:     []string{"tag:compute"},
					WaitTimeout: "1h", WaitPolicy: "skip",
				},
				{
					Name:        "storage",
					When:        config.ConditionConfig{Condition: "numeric", Fact: "rack_ups.battery.charge", Below: floatPtr(35)},
					Clients:     []string{"tag:storage"},
					WaitTimeout: "1h", WaitPolicy: "skip",
				},
			}},
			Wake: config.WakeConfig_{Gate: config.ConditionConfig{
				Condition: "numeric", Fact: "rack_ups.battery.charge", Above: floatPtr(60), For: "5m",
			}},
		}},
	}
}

// TestDwellConditionsFireInSimulation is the headline regression test.
//
// Dwell used to be measured with time.Now() regardless of the simulated
// timestamp, so real elapsed time in this loop is microseconds and every
// `for:` condition was unreachable. Both the trigger and the wake gate in
// the shipped example config use `for:`, so simulating it reported that
// nothing would ever happen.
func TestDwellConditionsFireInSimulation(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "60m",
	  "step": "10s",
	  "events": [
	    {"at": "0s",  "fact": "rack_ups.status", "value": ["OL"]},
	    {"at": "0s",  "fact": "rack_ups.battery.charge", "value": 100},
	    {"at": "1m",  "fact": "rack_ups.status", "value": ["OB", "DISCHRG"]},
	    {"at": "5m",  "fact": "rack_ups.battery.charge", "value": 45},
	    {"at": "10m", "fact": "rack_ups.battery.charge", "value": 30},
	    {"at": "15m", "fact": "rack_ups.status", "value": ["OL", "CHRG"]},
	    {"at": "20m", "fact": "rack_ups.battery.charge", "value": 80}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(outageConfig(), tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Triggers) == 0 {
		t.Fatal("the plan never triggered; a `for:` condition is unreachable in simulation")
	}

	// The status goes to OB at 1m and the trigger requires 30s of dwell, so
	// it should fire at roughly 1m30s, not immediately and not never.
	at := result.Triggers[0].At
	if at < 80*time.Second || at > 110*time.Second {
		t.Errorf("trigger fired at %s, want about 1m30s (dwell is not tracking simulated time)", at)
	}
}

// TestSetValuedFactsMatchContains: status arrives from JSON as []any, which
// the evaluator could not match against `contains:` — so the shipped
// example's trigger silently never fired in simulation.
func TestSetValuedFactsMatchContains(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "10m",
	  "step": "10s",
	  "events": [
	    {"at": "0s", "fact": "rack_ups.status", "value": ["OB", "DISCHRG"]},
	    {"at": "0s", "fact": "rack_ups.battery.charge", "value": 20}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(outageConfig(), tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Triggers) == 0 {
		t.Error("a set-valued status fact did not match a contains: condition")
	}
}

func TestStagesFireInOrder(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "30m",
	  "step": "10s",
	  "events": [
	    {"at": "0s",  "fact": "rack_ups.status", "value": ["OB"]},
	    {"at": "0s",  "fact": "rack_ups.battery.charge", "value": 100},
	    {"at": "5m",  "fact": "rack_ups.battery.charge", "value": 45},
	    {"at": "10m", "fact": "rack_ups.battery.charge", "value": 30}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(outageConfig(), tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Stages) != 2 {
		t.Fatalf("ran %d stages, want 2: %+v", len(result.Stages), result.Stages)
	}
	if result.Stages[0].Stage != "compute" || result.Stages[1].Stage != "storage" {
		t.Errorf("stage order = %q, %q; want compute, storage",
			result.Stages[0].Stage, result.Stages[1].Stage)
	}
	if result.Stages[0].At >= result.Stages[1].At {
		t.Error("stages did not fire in increasing time order")
	}

	want := []string{"compute1", "storage1"}
	if len(result.ShutdownOrder) != len(want) {
		t.Fatalf("shutdown order = %v, want %v", result.ShutdownOrder, want)
	}
	for i := range want {
		if result.ShutdownOrder[i] != want[i] {
			t.Errorf("shutdown order = %v, want %v", result.ShutdownOrder, want)
			break
		}
	}
}

func TestAbortWhenPowerReturns(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "30m",
	  "step": "10s",
	  "events": [
	    {"at": "0s", "fact": "rack_ups.status", "value": ["OB"]},
	    {"at": "0s", "fact": "rack_ups.battery.charge", "value": 90},
	    {"at": "3m", "fact": "rack_ups.status", "value": ["OL", "CHRG"]},
	    {"at": "5m", "fact": "rack_ups.battery.charge", "value": 95}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(outageConfig(), tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Aborts) == 0 {
		t.Fatal("power returning did not abort the sequence")
	}
	if len(result.Stages) != 0 {
		t.Errorf("stages ran despite the abort: %+v", result.Stages)
	}
}

// TestSkippedStageIsReported: which hosts would have been left running is
// the single most useful thing a simulation can tell an operator.
func TestSkippedStageIsReported(t *testing.T) {
	cfg := outageConfig()
	cfg.Plans[0].Shutdown.Stages[0].WaitTimeout = "2m"

	timeline := writeTimeline(t, `{
	  "duration": "20m",
	  "step": "10s",
	  "events": [
	    {"at": "0s", "fact": "rack_ups.status", "value": ["OB"]},
	    {"at": "0s", "fact": "rack_ups.battery.charge", "value": 90}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(cfg, tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Skipped) == 0 {
		t.Fatal("a stage whose condition never held was not reported as skipped")
	}
	found := false
	for _, c := range result.NotShutDown {
		if c == "compute1" {
			found = true
		}
	}
	if !found {
		t.Errorf("NotShutDown = %v, want it to include compute1", result.NotShutDown)
	}
}

// TestSilentSourceStopsSatisfyingConditions mirrors the daemon: a source
// that goes quiet must not keep a condition true. A null value in the
// timeline models the source going silent.
func TestSilentSourceStopsSatisfyingConditions(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "40m",
	  "step": "10s",
	  "events": [
	    {"at": "0s",  "fact": "rack_ups.status", "value": ["OB"]},
	    {"at": "0s",  "fact": "rack_ups.battery.charge", "value": 20},
	    {"at": "5m",  "fact": "rack_ups.status", "value": ["OL", "CHRG"]},
	    {"at": "6m",  "fact": "rack_ups.battery.charge", "value": 95},
	    {"at": "7m",  "fact": "rack_ups.battery.charge", "value": null}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(outageConfig(), tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Triggers) == 0 {
		t.Fatal("never triggered")
	}
	// The wake gate needs charge above 60 for 5 minutes. The charge fact
	// goes silent one minute in, so the gate must never open.
	if result.Completed {
		t.Error("the wake gate opened from a source that had gone silent; " +
			"a dead sensor must not be able to satisfy a gate")
	}
}

// TestFactsStayFreshWhileUnchanged: a timeline event means the fact holds
// that value from then on, as a real source re-reporting it would. Without
// this, any dwell longer than two poll intervals was unsatisfiable.
func TestFactsStayFreshWhileUnchanged(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "40m",
	  "step": "10s",
	  "events": [
	    {"at": "0s",  "fact": "rack_ups.status", "value": ["OB"]},
	    {"at": "0s",  "fact": "rack_ups.battery.charge", "value": 20},
	    {"at": "10m", "fact": "rack_ups.status", "value": ["OL", "CHRG"]},
	    {"at": "12m", "fact": "rack_ups.battery.charge", "value": 95}
	  ]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	result, err := Run(outageConfig(), tl, "outage", discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The wake gate requires charge above 60 for 5 minutes, held from 12m.
	// It should open at about 17m, which is only reachable if the charge
	// fact stays fresh between events.
	if !result.Completed {
		t.Fatal("the wake gate never opened; facts are going stale between events")
	}
	at := result.WakeGates[0].At
	if at < 16*time.Minute || at > 18*time.Minute {
		t.Errorf("wake gate opened at %s, want about 17m", at)
	}
}

func TestPlanNotFound(t *testing.T) {
	timeline := writeTimeline(t, `{
	  "duration": "1m",
	  "events": [{"at": "0s", "fact": "rack_ups.status", "value": ["OL"]}]
	}`)

	tl, err := LoadTimeline(timeline)
	if err != nil {
		t.Fatalf("LoadTimeline: %v", err)
	}

	if _, err := Run(outageConfig(), tl, "nonexistent", discardLogger()); err == nil {
		t.Error("Run accepted a plan name that is not in the config")
	}
}
