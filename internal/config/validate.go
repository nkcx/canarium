package config

import (
	"fmt"
	"strings"
)

type ValidationResult struct {
	Errors   []string
	Warnings []string
	Info     []string
}

func (r *ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r *ValidationResult) AddError(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *ValidationResult) AddWarning(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

func (r *ValidationResult) AddInfo(format string, args ...any) {
	r.Info = append(r.Info, fmt.Sprintf(format, args...))
}

func Validate(cfg *Config) *ValidationResult {
	result := &ValidationResult{}

	validateClients(cfg, result)
	validatePlans(cfg, result)
	validateDependencies(cfg, result)

	return result
}

func validateClients(cfg *Config, result *ValidationResult) {
	names := make(map[string]bool)
	for _, c := range cfg.Clients {
		if c.Name == "" {
			result.AddError("client has no name")
			continue
		}
		if names[c.Name] {
			result.AddError("duplicate client name: %s", c.Name)
		}
		names[c.Name] = true

		if c.Transport == "" {
			result.AddError("client %q has no transport", c.Name)
		}

		if _, err := ParseDuration(c.ShutdownBudget); err != nil {
			result.AddError("client %q: invalid shutdown_budget: %s", c.Name, err)
		}
		if _, err := ParseDuration(c.GuardPeriod); err != nil {
			result.AddError("client %q: invalid guard_period: %s", c.Name, err)
		}

		switch c.FeedPolicy {
		case "any", "all":
		default:
			result.AddError("client %q: invalid feed_policy: %q (must be 'any' or 'all')", c.Name, c.FeedPolicy)
		}

		switch c.WakePolicy {
		case "power_state", "retain_state":
		default:
			result.AddError("client %q: invalid wake_policy: %q", c.Name, c.WakePolicy)
		}

		for _, dep := range c.DependsOn {
			if !names[dep] && !clientExists(cfg, dep) {
				result.AddError("client %q depends_on unknown client %q", c.Name, dep)
			}
		}
	}
}

func validatePlans(cfg *Config, result *ValidationResult) {
	planNames := make(map[string]bool)
	clientNames := buildClientNameSet(cfg)
	tagClients := buildTagMap(cfg)

	for _, p := range cfg.Plans {
		if p.Name == "" {
			result.AddError("plan has no name")
			continue
		}
		if planNames[p.Name] {
			result.AddError("duplicate plan name: %s", p.Name)
		}
		planNames[p.Name] = true

		validateConditionConfig(&p.Trigger, fmt.Sprintf("plan %q trigger", p.Name), result)
		if p.Abort != nil {
			validateConditionConfig(p.Abort, fmt.Sprintf("plan %q abort", p.Name), result)
		}

		hasPonr := false
		for i, s := range p.Shutdown.Stages {
			if s.Name == "" {
				result.AddError("plan %q: stage %d has no name", p.Name, i)
			}

			validateConditionConfig(&s.When, fmt.Sprintf("plan %q stage %q when", p.Name, s.Name), result)

			if s.Budget != "" {
				if _, err := ParseDuration(s.Budget); err != nil {
					result.AddError("plan %q stage %q: invalid budget: %s", p.Name, s.Name, err)
				}
			}
			if s.WaitTimeout != "" {
				if _, err := ParseDuration(s.WaitTimeout); err != nil {
					result.AddError("plan %q stage %q: invalid wait_timeout: %s", p.Name, s.Name, err)
				}
			}

			for _, ref := range s.Clients {
				if strings.HasPrefix(ref, "tag:") {
					tag := strings.TrimPrefix(ref, "tag:")
					if _, ok := tagClients[tag]; !ok {
						result.AddWarning("plan %q stage %q: tag %q matches no clients", p.Name, s.Name, tag)
					}
				} else if !clientNames[ref] {
					result.AddError("plan %q stage %q: unknown client %q", p.Name, s.Name, ref)
				}
			}

			if s.PointOfNoReturn {
				hasPonr = true
			}
		}

		if hasPonr {
			result.AddInfo("plan %q: PONR marked on stage(s)", p.Name)
		} else {
			result.AddInfo("plan %q: no PONR set — sequence is fully abortable", p.Name)
		}

		validateConditionConfig(&p.Wake.Gate, fmt.Sprintf("plan %q wake gate", p.Name), result)
	}
}

func validateDependencies(cfg *Config, result *ValidationResult) {
	graph := make(map[string][]string)
	for _, c := range cfg.Clients {
		graph[c.Name] = c.DependsOn
	}

	visited := make(map[string]bool)
	path := make(map[string]bool)

	var hasCycle func(node string) bool
	hasCycle = func(node string) bool {
		visited[node] = true
		path[node] = true
		for _, dep := range graph[node] {
			if !visited[dep] {
				if hasCycle(dep) {
					return true
				}
			} else if path[dep] {
				result.AddError("dependency cycle detected involving %q and %q", node, dep)
				return true
			}
		}
		path[node] = false
		return false
	}

	for name := range graph {
		if !visited[name] {
			hasCycle(name)
		}
	}

	for _, p := range cfg.Plans {
		stageClients := make(map[string]int) // client -> stage index
		for i, s := range p.Shutdown.Stages {
			resolved := ResolveClientRefs(s.Clients, cfg)
			for _, name := range resolved {
				if prevStage, ok := stageClients[name]; ok {
					result.AddError("plan %q: client %q appears in stage %d and stage %d",
						p.Name, name, prevStage, i)
				}
				stageClients[name] = i
			}
		}

		for _, c := range cfg.Clients {
			clientStage, clientInPlan := stageClients[c.Name]
			if !clientInPlan {
				continue
			}
			for _, dep := range c.DependsOn {
				depStage, depInPlan := stageClients[dep]
				if !depInPlan {
					continue
				}
				if clientStage == depStage {
					result.AddError("plan %q: client %q and its dependency %q are in the same stage %q",
						p.Name, c.Name, dep, p.Shutdown.Stages[clientStage].Name)
				}
				if clientStage > depStage {
					result.AddError("plan %q: client %q (stage %d) depends_on %q (stage %d) — "+
						"dependency must shut down after dependent",
						p.Name, c.Name, clientStage, dep, depStage)
				}
			}

			for _, after := range c.After {
				afterStage, afterInPlan := stageClients[after]
				if !afterInPlan {
					continue
				}
				if clientStage < afterStage {
					result.AddWarning("plan %q: client %q (stage %d) declares after: %q (stage %d) — "+
						"stage order contradicts preference",
						p.Name, c.Name, clientStage, after, afterStage)
				}
			}
		}
	}
}

func validateConditionConfig(c *ConditionConfig, context string, result *ValidationResult) {
	if c == nil {
		result.AddError("%s: condition is nil", context)
		return
	}

	condType := c.Condition
	if condType == "" && c.Value != "" {
		condType = "template"
	}
	if condType == "" && c.Fact != "" {
		if c.Above != nil || c.Below != nil || c.Equals != nil {
			condType = "numeric"
		} else {
			condType = "state"
		}
	}

	switch condType {
	case "numeric":
		if c.Fact == "" {
			result.AddError("%s: numeric condition requires 'fact'", context)
		}
		if c.Above == nil && c.Below == nil && c.Equals == nil {
			result.AddError("%s: numeric condition requires above, below, or equals", context)
		}
	case "state":
		if c.Fact == "" {
			result.AddError("%s: state condition requires 'fact'", context)
		}
		if c.Is == "" && c.IsNot == "" && len(c.In) == 0 && c.Contains == "" {
			result.AddError("%s: state condition requires is, is_not, in, or contains", context)
		}
	case "and", "or":
		if len(c.Conditions) == 0 {
			result.AddError("%s: %s condition requires nested conditions", context, condType)
		}
		for i := range c.Conditions {
			validateConditionConfig(&c.Conditions[i], fmt.Sprintf("%s.conditions[%d]", context, i), result)
		}
	case "not":
		if len(c.Conditions) != 1 {
			result.AddError("%s: not condition requires exactly one nested condition", context)
		}
		if len(c.Conditions) > 0 {
			validateConditionConfig(&c.Conditions[0], fmt.Sprintf("%s.conditions[0]", context), result)
		}
	case "template":
		if c.Value == "" {
			result.AddError("%s: template condition requires 'value'", context)
		}
	case "", "true", "false":
		// literal conditions are always valid
	default:
		result.AddWarning("%s: unknown condition type %q", context, condType)
	}

	if c.For != "" {
		if _, err := ParseDuration(c.For); err != nil {
			result.AddError("%s: invalid 'for' duration: %s", context, err)
		}
	}
}

func clientExists(cfg *Config, name string) bool {
	for _, c := range cfg.Clients {
		if c.Name == name {
			return true
		}
	}
	return false
}

func buildClientNameSet(cfg *Config) map[string]bool {
	names := make(map[string]bool)
	for _, c := range cfg.Clients {
		names[c.Name] = true
	}
	return names
}

func buildTagMap(cfg *Config) map[string][]string {
	tags := make(map[string][]string)
	for _, c := range cfg.Clients {
		for _, t := range c.Tags {
			tags[t] = append(tags[t], c.Name)
		}
	}
	return tags
}

func ResolveClientRefs(refs []string, cfg *Config) []string {
	tagMap := buildTagMap(cfg)
	seen := make(map[string]bool)
	var result []string

	for _, ref := range refs {
		if strings.HasPrefix(ref, "tag:") {
			tag := strings.TrimPrefix(ref, "tag:")
			for _, name := range tagMap[tag] {
				if !seen[name] {
					seen[name] = true
					result = append(result, name)
				}
			}
		} else {
			if !seen[ref] {
				seen[ref] = true
				result = append(result, ref)
			}
		}
	}
	return result
}
