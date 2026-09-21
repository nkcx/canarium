package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnmarshalYAML accepts a condition written either as a mapping or as a
// bare string.
//
// The mapping is the canonical form:
//
//	when:
//	  condition: numeric
//	  fact: rack_ups.battery.charge
//	  below: 50
//
// The string form is shorthand. "true" and "false" are the literal
// conditions; anything else is a template expression, which is what a bare
// expression means everywhere else in the config:
//
//	when: "true"
//	when: 'fact("rack_ups.battery.charge") > 60'
//
// This existed in the documentation before it existed in the parser. The
// guide's shutdown-stages example was written with `when: "true"`, which
// produced "cannot unmarshal !!str `true` into config.ConditionConfig" --
// a message that names a Go type and does not say what to write instead.
// The shorthand is the obvious reading of that YAML, so it now works.
func (c *ConditionConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return fmt.Errorf("line %d: a condition must be a mapping or a string: %w",
				node.Line, err)
		}
		*c = conditionFromString(s)
		return nil
	}

	// A named type is required here: decoding into *ConditionConfig would
	// call this method again and recurse forever.
	type plain ConditionConfig
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*c = ConditionConfig(p)
	return nil
}

// conditionFromString maps the shorthand onto a condition.
func conditionFromString(s string) ConditionConfig {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return ConditionConfig{Condition: "true"}
	case "false":
		return ConditionConfig{Condition: "false"}
	default:
		return ConditionConfig{Condition: "template", Value: s}
	}
}
