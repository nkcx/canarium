package config

import (
	"strings"
	"testing"
)

func registryFixture() *Registry {
	return &Registry{
		Transports:  []string{"ssh", "wol", "proxmox"},
		SourceTypes: []string{"nut", "snmp", "gpio"},
		Facts:       []string{"ups.battery.charge", "ups.battery.runtime", "ups.status"},
	}
}

func baseConfig() *Config {
	cfg := &Config{
		Canarium: DefaultCanariumConfig(),
		Clients: []ClientConfig{{
			Name: "nas", Transport: "ssh", Address: "10.0.0.1",
			FeedPolicy: "any", WakePolicy: "power_state",
		}},
		Plans: []PlanConfig{{
			Name:    "outage",
			Trigger: ConditionConfig{Condition: "true"},
			Shutdown: ShutdownConfig{Stages: []StageConfig{{
				Name: "s", When: ConditionConfig{Condition: "true"}, Clients: []string{"nas"},
			}}},
			Wake: WakeConfig_{Gate: ConditionConfig{Condition: "true"}},
		}},
	}
	return cfg
}

func errorsContaining(result *ValidationResult, substr string) bool {
	for _, e := range result.Errors {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// TestUnknownTransportIsAnError is the regression test for a typo that was
// invisible until an outage: shutdownClient logs "transport not found" and
// returns, so the host is never shut down while the sequence reports success.
func TestUnknownTransportIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Clients[0].Transport = "sshh"

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "unknown transport") {
		t.Errorf("no error for an unknown transport; errors were %v", result.Errors)
	}
	if !errorsContaining(result, "ssh") {
		t.Error("the error does not list the available transports")
	}
}

func TestUnknownWakeTransportIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Clients[0].Wake = &WakeConfig{Transport: "magic-packets"}

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "unknown wake transport") {
		t.Errorf("no error for an unknown wake transport; errors were %v", result.Errors)
	}
}

func TestKnownTransportPasses(t *testing.T) {
	result := ValidateWith(baseConfig(), registryFixture())
	if errorsContaining(result, "transport") {
		t.Errorf("a valid transport was rejected: %v", result.Errors)
	}
}

// TestUnknownSourceTypeIsAnError: registerSources had no default case, so a
// config declaring a GPIO flood sensor started cleanly and never produced
// the fact its plans depended on.
func TestUnknownSourceTypeIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Sources = []SourceConfig{{Name: "s", Type: "modbus"}}

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "unknown source type") {
		t.Errorf("no error for an unknown source type; errors were %v", result.Errors)
	}
}

func TestDuplicateSourceNameIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Sources = []SourceConfig{
		{Name: "ups", Type: "nut"},
		{Name: "ups", Type: "snmp"},
	}

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "duplicate source name") {
		t.Errorf("duplicate source names accepted; errors were %v", result.Errors)
	}
}

// TestUnknownFactIsAnError: a typo'd fact evaluates to unavailable forever,
// so the plan simply never triggers and nothing says why.
func TestUnknownFactIsAnError(t *testing.T) {
	cfg := baseConfig()
	below := 50.0
	cfg.Plans[0].Trigger = ConditionConfig{
		Condition: "numeric", Fact: "ups.battery.charg", Below: &below,
	}

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "unknown fact") {
		t.Errorf("no error for an unknown fact; errors were %v", result.Errors)
	}
	if !errorsContaining(result, "ups.battery.charge") {
		t.Error("the error does not suggest the likely intended fact")
	}
}

func TestKnownFactPasses(t *testing.T) {
	cfg := baseConfig()
	below := 50.0
	cfg.Plans[0].Trigger = ConditionConfig{
		Condition: "numeric", Fact: "ups.battery.charge", Below: &below,
	}

	result := ValidateWith(cfg, registryFixture())
	if errorsContaining(result, "unknown fact") {
		t.Errorf("a declared fact was rejected: %v", result.Errors)
	}
}

func TestInvalidWaitPolicyIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Plans[0].Shutdown.Stages[0].WaitPolicy = "hold-on"

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "invalid wait_policy") {
		t.Errorf("no error for an invalid wait_policy; errors were %v", result.Errors)
	}
}

func TestValidWaitPoliciesPass(t *testing.T) {
	for _, policy := range ValidWaitPolicies {
		t.Run(policy, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Plans[0].Shutdown.Stages[0].WaitPolicy = policy

			result := ValidateWith(cfg, registryFixture())
			if errorsContaining(result, "wait_policy") {
				t.Errorf("valid policy %q rejected: %v", policy, result.Errors)
			}
		})
	}
}

func TestUnknownWakeStageClientIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Plans[0].Wake.Stages = []StageConfig{{Name: "w", Clients: []string{"ghost"}}}

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "wake stage") {
		t.Errorf("wake stages were not validated; errors were %v", result.Errors)
	}
}

func TestInvalidCommsLossAssumesIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Clients[0].CommsLossAssumes = "maybe"

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "comms_loss_assumes") {
		t.Errorf("no error for invalid comms_loss_assumes; errors were %v", result.Errors)
	}
}

func TestNegativeWakeRetriesIsAnError(t *testing.T) {
	cfg := baseConfig()
	cfg.Plans[0].Wake.Retries = -1

	result := ValidateWith(cfg, registryFixture())
	if !errorsContaining(result, "retries") {
		t.Errorf("no error for negative retries; errors were %v", result.Errors)
	}
}

func TestNilRegistrySkipsRegistryChecks(t *testing.T) {
	cfg := baseConfig()
	cfg.Clients[0].Transport = "anything-at-all"

	result := ValidateWith(cfg, nil)
	if errorsContaining(result, "unknown transport") {
		t.Error("registry checks ran with a nil registry")
	}
}
