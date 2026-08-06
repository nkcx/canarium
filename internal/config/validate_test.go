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
				Name: "test",
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

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"5m", "5m0s"},
		{"30s", "30s"},
		{"1h", "1h0m0s"},
		{"30d", "720h0m0s"},
	}

	for _, tt := range tests {
		got, err := ParseDuration(tt.input)
		if err != nil {
			t.Errorf("ParseDuration(%q) error: %v", tt.input, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("ParseDuration(%q) = %s, want %s", tt.input, got, tt.want)
		}
	}
}
