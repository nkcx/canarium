package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConditionShorthandParses covers the form the guide documented before
// the parser accepted it. `when: "true"` produced "cannot unmarshal !!str
// `true` into config.ConditionConfig", which names a Go type and does not
// say what to write instead.
func TestConditionShorthandParses(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ConditionConfig
	}{
		{"literal true", `when: "true"`, ConditionConfig{Condition: "true"}},
		{"literal false", `when: "false"`, ConditionConfig{Condition: "false"}},
		{"case insensitive", `when: "TRUE"`, ConditionConfig{Condition: "true"}},
		{"surrounding space", `when: " true "`, ConditionConfig{Condition: "true"}},
		{
			"expression becomes a template",
			`when: 'fact("ups.charge") > 60'`,
			ConditionConfig{Condition: "template", Value: `fact("ups.charge") > 60`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc struct {
				When ConditionConfig `yaml:"when"`
			}
			if err := yaml.Unmarshal([]byte(tt.input), &doc); err != nil {
				t.Fatalf("parsing %s: %v", tt.input, err)
			}
			if doc.When.Condition != tt.want.Condition {
				t.Errorf("condition = %q, want %q", doc.When.Condition, tt.want.Condition)
			}
			if doc.When.Value != tt.want.Value {
				t.Errorf("value = %q, want %q", doc.When.Value, tt.want.Value)
			}
		})
	}
}

// TestConditionMappingStillParses: the shorthand must not displace the
// canonical form, and the custom unmarshaller must not recurse into itself.
func TestConditionMappingStillParses(t *testing.T) {
	const input = `
when:
  condition: numeric
  fact: rack_ups.battery.charge
  below: 50
  for: 30s
`
	var doc struct {
		When ConditionConfig `yaml:"when"`
	}
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatalf("parsing: %v", err)
	}

	if doc.When.Condition != "numeric" {
		t.Errorf("condition = %q, want numeric", doc.When.Condition)
	}
	if doc.When.Fact != "rack_ups.battery.charge" {
		t.Errorf("fact = %q", doc.When.Fact)
	}
	if doc.When.Below == nil || *doc.When.Below != 50 {
		t.Errorf("below = %v, want 50", doc.When.Below)
	}
	if doc.When.For != "30s" {
		t.Errorf("for = %q, want 30s", doc.When.For)
	}
}

// TestNestedConditionsAcceptShorthand: the recursive case, where the
// shorthand appears inside a composite condition.
func TestNestedConditionsAcceptShorthand(t *testing.T) {
	const input = `
when:
  condition: or
  conditions:
    - "true"
    - condition: numeric
      fact: ups.charge
      below: 20
`
	var doc struct {
		When ConditionConfig `yaml:"when"`
	}
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatalf("parsing: %v", err)
	}

	if len(doc.When.Conditions) != 2 {
		t.Fatalf("got %d nested conditions, want 2", len(doc.When.Conditions))
	}
	if doc.When.Conditions[0].Condition != "true" {
		t.Errorf("nested shorthand = %q, want true", doc.When.Conditions[0].Condition)
	}
	if doc.When.Conditions[1].Fact != "ups.charge" {
		t.Errorf("nested mapping = %q", doc.When.Conditions[1].Fact)
	}
}

func TestConditionRejectsAList(t *testing.T) {
	var doc struct {
		When ConditionConfig `yaml:"when"`
	}
	err := yaml.Unmarshal([]byte("when: [a, b]"), &doc)
	if err == nil {
		t.Fatal("a list was accepted as a condition")
	}
	if strings.Contains(err.Error(), "stack overflow") {
		t.Fatalf("the unmarshaller recursed: %v", err)
	}
}
