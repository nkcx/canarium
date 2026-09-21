package config

import "testing"

func TestValidateMinimalConfig(t *testing.T) {
	cfg := &Config{
		Canarium: DefaultCanariumConfig(),
	}

	result := Validate(cfg)
	if result.HasErrors() {
		t.Errorf("empty config should not have errors: %v", result.Errors)
	}
}

func TestValidateDuplicateClientNames(t *testing.T) {
	cfg := &Config{
		Clients: []ClientConfig{
			{Name: "alpha", Transport: "ssh", FeedPolicy: "any", WakePolicy: "power_state", ShutdownBudget: "3m", GuardPeriod: "60s"},
			{Name: "alpha", Transport: "ssh", FeedPolicy: "any", WakePolicy: "power_state", ShutdownBudget: "3m", GuardPeriod: "60s"},
		},
	}

	result := Validate(cfg)
	if !result.HasErrors() {
		t.Error("duplicate client names should produce an error")
	}
}

func TestValidateDependencyCycle(t *testing.T) {
	cfg := &Config{
		Clients: []ClientConfig{
			{Name: "a", Transport: "ssh", DependsOn: []string{"b"}, FeedPolicy: "any", WakePolicy: "power_state", ShutdownBudget: "3m", GuardPeriod: "60s"},
			{Name: "b", Transport: "ssh", DependsOn: []string{"a"}, FeedPolicy: "any", WakePolicy: "power_state", ShutdownBudget: "3m", GuardPeriod: "60s"},
		},
	}

	result := Validate(cfg)
	if !result.HasErrors() {
		t.Error("dependency cycle should produce an error")
	}
}

func TestValidateSameStageDepends(t *testing.T) {
	cfg := &Config{
		Clients: []ClientConfig{
			{Name: "a", Transport: "ssh", DependsOn: []string{"b"}, FeedPolicy: "any", WakePolicy: "power_state", ShutdownBudget: "3m", GuardPeriod: "60s"},
			{Name: "b", Transport: "ssh", FeedPolicy: "any", WakePolicy: "power_state", ShutdownBudget: "3m", GuardPeriod: "60s"},
		},
		Plans: []PlanConfig{
			{
				Name:    "test",
				Trigger: ConditionConfig{Value: "true"},
				Shutdown: ShutdownConfig{
					Stages: []StageConfig{
						{Name: "s1", When: ConditionConfig{Value: "true"}, Clients: []string{"a", "b"}, WaitTimeout: "1h", WaitPolicy: "skip"},
					},
				},
				Wake: WakeConfig_{
					Gate: ConditionConfig{Value: "true"},
				},
			},
		},
	}

	result := Validate(cfg)
	hasError := false
	for _, e := range result.Errors {
		if contains(e, "same stage") {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("client and its dependency in same stage should produce an error")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// minimalValidConfig is one client in one single-stage plan: enough to
// validate cleanly, so a test can change one thing and see only its effect.
func minimalValidConfig() *Config {
	return &Config{
		Canarium: DefaultCanariumConfig(),
		Clients: []ClientConfig{{
			Name: "nas", Transport: "ssh", Address: "10.0.0.5",
			FeedPolicy: "any", WakePolicy: "power_state",
			ShutdownBudget: "3m", GuardPeriod: "60s",
		}},
		Plans: []PlanConfig{{
			Name:    "outage",
			Trigger: ConditionConfig{Condition: "true"},
			Shutdown: ShutdownConfig{
				Stages: []StageConfig{{
					Name: "all", When: ConditionConfig{Condition: "true"},
					Clients: []string{"nas"}, WaitTimeout: "1h", WaitPolicy: "skip",
				}},
			},
			Wake: WakeConfig_{
				Order: "reverse",
				Gate:  ConditionConfig{Condition: "true"},
			},
		}},
	}
}

func hasWarningContaining(result *ValidationResult, substr string) bool {
	for _, w := range result.Warnings {
		if contains(w, substr) {
			return true
		}
	}
	return false
}

func floatPtr(v float64) *float64 { return &v }

// TestMissingWakeGateWarns guards against the loop an absent gate creates:
// the sequence finishes, everything wakes, the trigger is still true
// because nothing has recovered, and the plan fires again -- drawing the
// fleet's boot current from the battery on every cycle.
func TestMissingWakeGateWarns(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Plans[0].Wake = WakeConfig_{Order: "reverse"}

	result := Validate(cfg)
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	if !hasWarningContaining(result, "no wake gate") {
		t.Errorf("a plan with no wake gate produced no warning: %v", result.Warnings)
	}
}

// TestExplicitTrueWakeGateIsAccepted: an operator who writes gate: "true"
// has said they mean it, and should not be nagged.
func TestExplicitTrueWakeGateIsAccepted(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Plans[0].Wake = WakeConfig_{
		Order: "reverse",
		Gate:  ConditionConfig{Condition: "true"},
	}

	result := Validate(cfg)
	if hasWarningContaining(result, "no wake gate") {
		t.Errorf("an explicit gate still warned: %v", result.Warnings)
	}
}

func TestRealWakeGateDoesNotWarn(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Plans[0].Wake = WakeConfig_{
		Order: "reverse",
		Gate: ConditionConfig{
			Condition: "numeric",
			Fact:      "rack_ups.battery.charge",
			Above:     floatPtr(60),
			For:       "5m",
		},
	}

	result := Validate(cfg)
	if hasWarningContaining(result, "no wake gate") {
		t.Errorf("a configured gate warned: %v", result.Warnings)
	}
}
