package config

import (
	"net"
	"strings"
	"testing"
)

func localIdentity() *LocalIdentity {
	return &LocalIdentity{
		Hostnames: []string{"canarium-host", "canarium-host.lan"},
		Addresses: []net.IP{net.ParseIP("10.0.10.5")},
	}
}

func selfPreservationConfig(clientAddress string, inStage bool) *Config {
	stage := StageConfig{
		Name: "compute", When: ConditionConfig{Condition: "true"},
		Clients: []string{"other"},
	}
	if inStage {
		stage.Clients = append(stage.Clients, "canary")
	}

	return &Config{
		Canarium: DefaultCanariumConfig(),
		Clients: []ClientConfig{
			{Name: "canary", Transport: "ssh", Address: clientAddress,
				FeedPolicy: "any", WakePolicy: "power_state"},
			{Name: "other", Transport: "ssh", Address: "10.0.10.99",
				FeedPolicy: "any", WakePolicy: "power_state"},
		},
		Plans: []PlanConfig{{
			Name:     "outage",
			Trigger:  ConditionConfig{Condition: "true"},
			Shutdown: ShutdownConfig{Stages: []StageConfig{stage}},
			Wake:     WakeConfig_{Gate: ConditionConfig{Condition: "true"}},
		}},
	}
}

// TestShuttingDownOwnHostIsRejected covers SPEC §8.3, which nothing
// implemented. Canarium shutting itself down partway through a sequence
// leaves the rest of the fleet running on a UPS with nothing watching it.
func TestShuttingDownOwnHostIsRejected(t *testing.T) {
	for _, address := range []string{"10.0.10.5", "canarium-host", "canarium-host.lan"} {
		t.Run(address, func(t *testing.T) {
			cfg := selfPreservationConfig(address, true)

			result := &ValidationResult{}
			validateSelfPreservation(cfg, result, localIdentity())

			if !errorsContaining(result, "running on") {
				t.Fatalf("no error for a config that shuts down Canarium's own host; "+
					"errors were %v", result.Errors)
			}
			if !errorsContaining(result, "outage") || !errorsContaining(result, "compute") {
				t.Error("the error does not name the plan and stage responsible")
			}
		})
	}
}

// TestOwnHostNotInAStageIsOnlyAWarning: declaring the local host as a client
// is legitimate if nothing shuts it down.
func TestOwnHostNotInAStageIsOnlyAWarning(t *testing.T) {
	cfg := selfPreservationConfig("10.0.10.5", false)

	result := &ValidationResult{}
	validateSelfPreservation(cfg, result, localIdentity())

	if result.HasErrors() {
		t.Errorf("a self-reference outside any shutdown stage was an error: %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Error("no warning for a client that appears to be this host")
	}
}

func TestUnrelatedHostsPass(t *testing.T) {
	cfg := selfPreservationConfig("10.0.10.77", true)

	result := &ValidationResult{}
	validateSelfPreservation(cfg, result, localIdentity())

	if result.HasErrors() {
		t.Errorf("an unrelated client was flagged: %v", result.Errors)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("an unrelated client produced warnings: %v", result.Warnings)
	}
}

func TestSelfPreservationSkippedWithoutIdentity(t *testing.T) {
	cfg := selfPreservationConfig("10.0.10.5", true)

	result := &ValidationResult{}
	validateSelfPreservation(cfg, result, nil)

	if result.HasErrors() {
		t.Errorf("checks ran with no local identity: %v", result.Errors)
	}
}

// TestTagExpansionIsCovered: a stage referencing the host by tag is the same
// mistake, and must be caught the same way.
func TestSelfPreservationSeesTagReferences(t *testing.T) {
	cfg := selfPreservationConfig("10.0.10.5", false)
	cfg.Clients[0].Tags = []string{"compute"}
	cfg.Plans[0].Shutdown.Stages[0].Clients = []string{"tag:compute"}

	result := &ValidationResult{}
	validateSelfPreservation(cfg, result, localIdentity())

	if !errorsContaining(result, "running on") {
		t.Errorf("a tag-expanded self-reference was not caught; errors were %v", result.Errors)
	}
}

func TestLocalIdentityMatches(t *testing.T) {
	id := localIdentity()

	tests := []struct {
		address string
		want    bool
	}{
		{"10.0.10.5", true},
		{"canarium-host", true},
		{"CANARIUM-HOST", true},
		{"canarium-host.lan", true},
		{"10.0.10.5:22", true},
		{"10.0.10.6", false},
		{"other-host", false},
		{"", false},
		{"   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if got := id.Matches(tt.address); got != tt.want {
				t.Errorf("Matches(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestDetectLocalIdentityFindsSomething(t *testing.T) {
	id := DetectLocalIdentity()
	if id == nil {
		t.Fatal("DetectLocalIdentity returned nil")
	}
	if len(id.Hostnames) == 0 {
		t.Error("no hostname was detected")
	}
}

// --- post_shutdown validation ---

func validPostShutdown() *PostShutdownConfig {
	return &PostShutdownConfig{
		Action: "upscmd", Command: "shutdown.return", UPS: "rack_ups",
		Host: "nut.lan", Username: "canarium", Password: "secret",
	}
}

func TestValidPostShutdownPasses(t *testing.T) {
	result := &ValidationResult{}
	validatePostShutdown("outage", validPostShutdown(), result)

	if result.HasErrors() {
		t.Errorf("a valid post_shutdown block was rejected: %v", result.Errors)
	}
}

// TestPostShutdownIsValidated covers a block that was skipped entirely, so a
// mistake surfaced only at the end of a real outage — after every server was
// down, when nothing could be done about it.
func TestPostShutdownIsValidated(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PostShutdownConfig)
		want   string
	}{
		{"no command", func(p *PostShutdownConfig) { p.Command = "" }, "command"},
		{"no ups", func(p *PostShutdownConfig) { p.UPS = "" }, "ups"},
		{"bad action", func(p *PostShutdownConfig) { p.Action = "explode" }, "action"},
		{"negative delay", func(p *PostShutdownConfig) { p.Delay = -5 }, "delay"},
		{"port out of range", func(p *PostShutdownConfig) { p.Port = 99999 }, "port"},
		{"half credentials", func(p *PostShutdownConfig) { p.Password = "" }, "together"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := validPostShutdown()
			tt.mutate(ps)

			result := &ValidationResult{}
			validatePostShutdown("outage", ps, result)

			if !errorsContaining(result, tt.want) {
				t.Errorf("no error mentioning %q; errors were %v", tt.want, result.Errors)
			}
		})
	}
}

func TestPostShutdownWithoutCredentialsWarns(t *testing.T) {
	ps := validPostShutdown()
	ps.Username = ""
	ps.Password = ""

	result := &ValidationResult{}
	validatePostShutdown("outage", ps, result)

	if result.HasErrors() {
		t.Errorf("missing credentials were an error: %v", result.Errors)
	}

	var found bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "ACCESS-DENIED") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning that NUT will refuse this; warnings were %v", result.Warnings)
	}
}

// --- negative durations ---

// TestNegativeDurationsAreRejected: time.ParseDuration accepts them happily,
// and a negative budget produces a context that has already expired — so a
// shutdown gets no time at all and every host ends up down_unverified.
func TestNegativeDurationsAreRejected(t *testing.T) {
	cfg := baseConfig()
	cfg.Clients[0].ShutdownBudget = "-10s"
	cfg.Plans[0].Shutdown.Stages[0].Budget = "-1m"
	cfg.Plans[0].Wake.Stagger = "-30s"

	result := ValidateWith(cfg, registryFixture())

	for _, want := range []string{"shutdown_budget", "budget", "wake.stagger"} {
		if !errorsContaining(result, want) {
			t.Errorf("no error for a negative %s; errors were %v", want, result.Errors)
		}
	}
}

func TestPositiveDurationsPass(t *testing.T) {
	cfg := baseConfig()
	cfg.Clients[0].ShutdownBudget = "3m"
	cfg.Plans[0].Shutdown.Stages[0].Budget = "5m"
	cfg.Plans[0].Wake.Stagger = "45s"

	result := ValidateWith(cfg, registryFixture())

	if errorsContaining(result, "negative") {
		t.Errorf("a valid duration was rejected: %v", result.Errors)
	}
}
