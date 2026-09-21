package config

import (
	"fmt"
	"slices"
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

// Registry describes what the running daemon actually supports, so
// validation can check a config against reality rather than against a
// hardcoded list that drifts.
type Registry struct {
	// Transports is the set of registered transport names.
	Transports []string

	// Local identifies the host Canarium is running on, so validation can
	// refuse a config that would have it shut itself down. Nil skips that
	// check.
	Local *LocalIdentity

	// SourceTypes is the set of source types the daemon can construct.
	SourceTypes []string

	// Facts is the set of fact keys the configured sources declare. Empty
	// means fact references are not checked.
	Facts []string
}

// Validate checks a configuration for errors.
//
// Passing a nil registry skips the checks that need to know what the daemon
// supports; `canarium validate` always supplies one.
func Validate(cfg *Config) *ValidationResult {
	return ValidateWith(cfg, nil)
}

// ValidateWith checks a configuration against what the daemon supports.
func ValidateWith(cfg *Config, reg *Registry) *ValidationResult {
	result := &ValidationResult{}

	validateCanarium(cfg, result)
	validateClients(cfg, result, reg)
	validateSources(cfg, result, reg)
	validatePlans(cfg, result, reg)
	validateDependencies(cfg, result)

	if reg != nil {
		validateSelfPreservation(cfg, result, reg.Local)
	}

	return result
}

// validateSources checks the sources block.
//
// registerSources in the daemon has no default case: a source whose type it
// does not recognise is skipped in silence, so a config declaring a GPIO
// flood sensor would start cleanly and simply never produce the fact its
// plans depend on.
func validateSources(cfg *Config, result *ValidationResult, reg *Registry) {
	names := make(map[string]bool)

	for i, s := range cfg.Sources {
		context := fmt.Sprintf("source %d", i)
		if s.Name != "" {
			context = fmt.Sprintf("source %q", s.Name)
		}

		if s.Name == "" {
			result.AddError("%s: has no name", context)
		} else if names[s.Name] {
			result.AddError("duplicate source name: %s", s.Name)
		}
		names[s.Name] = true

		if s.Type == "" {
			result.AddError("%s: has no type", context)
			continue
		}

		if reg != nil && len(reg.SourceTypes) > 0 && !slices.Contains(reg.SourceTypes, s.Type) {
			result.AddError("%s: unknown source type %q (available: %s)",
				context, s.Type, strings.Join(reg.SourceTypes, ", "))
		}

		if s.PollInterval != "" {
			if _, err := ParseDuration(s.PollInterval); err != nil {
				result.AddError("%s: invalid poll_interval: %s", context, err)
			}
		}
	}
}

// validateCanarium checks the top-level daemon settings.
func validateCanarium(cfg *Config, result *ValidationResult) {
	switch strings.ToLower(strings.TrimSpace(cfg.Canarium.Mode)) {
	case "disarmed", "dry-run", "dryrun", "dry_run", "armed":
	default:
		// ParseMode falls back to disarmed, so an unrecognised value would
		// otherwise produce a system that silently never acts.
		result.AddError("canarium.mode is %q (expected disarmed, dry-run or armed)",
			cfg.Canarium.Mode)
	}

	if cfg.Canarium.Host == "" {
		result.AddError("canarium.host is empty")
	}
	if cfg.Canarium.DataDir == "" {
		result.AddError("canarium.data_dir is empty")
	}

	if cfg.Canarium.JournalRetain != "" {
		checkDuration(result, cfg.Canarium.JournalRetain, "canarium.journal_retain")
		if d, err := ParseDuration(cfg.Canarium.JournalRetain); err == nil && d == 0 {
			result.AddWarning("canarium.journal_retain is zero; " +
				"sequence history will be kept indefinitely")
		}
	}

	if hash := strings.TrimSpace(cfg.Canarium.Auth.PasswordHash); hash != "" {
		if !strings.HasPrefix(hash, "$2") {
			result.AddError("canarium.auth.password_hash is not a bcrypt hash; " +
				"generate one with `canarium hash-password`")
		}
		result.AddInfo("canarium.auth.password_hash is set; the admin password " +
			"comes from this file and first-run setup is disabled")
	}

	if cfg.Canarium.ConfigReadonly {
		result.AddInfo("canarium.config_readonly is set; the API will refuse " +
			"runtime changes to settings this file declares")
	}

	for i, wh := range cfg.Canarium.Notifications.Webhooks {
		if strings.TrimSpace(wh.URL) == "" {
			result.AddError("canarium.notifications.webhooks[%d] has no url", i)
		}
	}
}

type clientDeps struct {
	client string
	deps   []string
}

func validateClients(cfg *Config, result *ValidationResult, reg *Registry) {
	names := make(map[string]bool)
	var deferredDeps []clientDeps
	for _, c := range cfg.Clients {
		if c.Name == "" {
			result.AddError("client has no name")
			continue
		}
		if names[c.Name] {
			result.AddError("duplicate client name: %s", c.Name)
		}
		names[c.Name] = true

		// A transport name the daemon does not know is fatal, not cosmetic:
		// at runtime shutdownClient logs "transport not found" and returns,
		// so the host is simply never shut down while the sequence reports
		// success and moves on.
		if c.Transport == "" {
			result.AddError("client %q has no transport", c.Name)
		} else if reg != nil && len(reg.Transports) > 0 && !slices.Contains(reg.Transports, c.Transport) {
			result.AddError("client %q: unknown transport %q (available: %s)",
				c.Name, c.Transport, strings.Join(reg.Transports, ", "))
		}

		if c.Wake != nil && c.Wake.Transport != "" &&
			reg != nil && len(reg.Transports) > 0 &&
			!slices.Contains(reg.Transports, c.Wake.Transport) {
			result.AddError("client %q: unknown wake transport %q (available: %s)",
				c.Name, c.Wake.Transport, strings.Join(reg.Transports, ", "))
		}

		if c.CommsLossAssumes != "" {
			switch c.CommsLossAssumes {
			case CommsLossThreatened, CommsLossSafe:
			default:
				result.AddError("client %q: invalid comms_loss_assumes %q (must be %q or %q)",
					c.Name, c.CommsLossAssumes, CommsLossThreatened, CommsLossSafe)
			}
		}

		checkDuration(result, c.ShutdownBudget,
			fmt.Sprintf("client %q: shutdown_budget", c.Name))
		checkDuration(result, c.GuardPeriod,
			fmt.Sprintf("client %q: guard_period", c.Name))
		if c.Probe != nil {
			checkDuration(result, c.Probe.Timeout,
				fmt.Sprintf("client %q: probe.timeout", c.Name))
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

		// Deferred: `names` is still being built, so a forward reference to
		// a client declared later would be a false positive here.
		deferredDeps = append(deferredDeps, clientDeps{client: c.Name, deps: c.DependsOn})
	}

	for _, d := range deferredDeps {
		for _, dep := range d.deps {
			if !names[dep] {
				result.AddError("client %q depends_on unknown client %q", d.client, dep)
			}
		}
	}
}

func validatePlans(cfg *Config, result *ValidationResult, reg *Registry) {
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

		validateConditionConfig(&p.Trigger, fmt.Sprintf("plan %q trigger", p.Name), result, reg)
		if p.Abort != nil {
			validateConditionConfig(p.Abort, fmt.Sprintf("plan %q abort", p.Name), result, reg)
		}

		hasPonr := false
		for i, s := range p.Shutdown.Stages {
			if s.Name == "" {
				result.AddError("plan %q: stage %d has no name", p.Name, i)
			}

			validateConditionConfig(&s.When, fmt.Sprintf("plan %q stage %q when", p.Name, s.Name), result, reg)

			checkDuration(result, s.Budget,
				fmt.Sprintf("plan %q stage %q: budget", p.Name, s.Name))
			checkDuration(result, s.WaitTimeout,
				fmt.Sprintf("plan %q stage %q: wait_timeout", p.Name, s.Name))

			// An unrecognised policy previously fell through to skip, so a
			// typo silently turned a stage that should hold into one that
			// abandons its clients.
			if s.WaitPolicy != "" && !slices.Contains(ValidWaitPolicies, s.WaitPolicy) {
				result.AddError("plan %q stage %q: invalid wait_policy %q (must be one of %s)",
					p.Name, s.Name, s.WaitPolicy, strings.Join(ValidWaitPolicies, ", "))
			}

			// skip is the default and is a reasonable choice, but it means
			// these hosts are simply never shut down. Say so out loud.
			if s.WaitPolicy == WaitPolicySkip || s.WaitPolicy == "" {
				result.AddInfo("plan %q stage %q: wait_policy is %q — if the entry condition "+
					"has not held within %s, these clients will not be shut down",
					p.Name, s.Name, WaitPolicySkip, waitTimeoutOrDefault(s.WaitTimeout))
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

		validatePostShutdown(p.Name, p.Shutdown.PostShutdown, result)

		validateConditionConfig(&p.Wake.Gate, fmt.Sprintf("plan %q wake gate", p.Name), result, reg)

		for i, s := range p.Wake.Stages {
			// Wake stages were never validated at all, so an unknown client
			// here surfaced only as a log line during a wake.
			for _, ref := range s.Clients {
				if strings.HasPrefix(ref, "tag:") {
					tag := strings.TrimPrefix(ref, "tag:")
					if _, ok := tagClients[tag]; !ok {
						result.AddWarning("plan %q wake stage %d: tag %q matches no clients",
							p.Name, i, tag)
					}
				} else if !clientNames[ref] {
					result.AddError("plan %q wake stage %d: unknown client %q", p.Name, i, ref)
				}
			}
		}

		if p.Wake.Retries < 0 {
			result.AddError("plan %q: wake.retries cannot be negative", p.Name)
		}
		for field, value := range map[string]string{
			"wake.stagger":        p.Wake.Stagger,
			"wake.probe_interval": p.Wake.ProbeInterval,
			"wake.boot_deadline":  p.Wake.BootDeadline,
		} {
			checkDuration(result, value, fmt.Sprintf("plan %q: %s", p.Name, field))
		}
	}
}

// waitTimeoutOrDefault renders a stage's wait timeout for diagnostics.
func waitTimeoutOrDefault(value string) string {
	d, err := Duration(value, DefaultWaitTimeout())
	if err != nil {
		return value
	}
	return d.String()
}

// validatePostShutdown checks a plan's post-shutdown block.
//
// This was skipped entirely, so a misspelled command or a missing UPS name
// passed validation and surfaced only at the very end of a real outage —
// after every server was down, when nothing could be done about it.
func validatePostShutdown(planName string, ps *PostShutdownConfig, result *ValidationResult) {
	if ps == nil {
		return
	}

	context := fmt.Sprintf("plan %q post_shutdown", planName)

	switch strings.ToLower(strings.TrimSpace(ps.Action)) {
	case "", "upscmd", "outlet_off", "load_off", "outlet_on", "load_on":
	default:
		result.AddError("%s: unrecognised action %q "+
			"(expected upscmd, outlet_off or outlet_on)", context, ps.Action)
	}

	if strings.TrimSpace(ps.Command) == "" {
		result.AddError("%s: no command configured. This is the NUT instant "+
			"command to send, such as shutdown.return.", context)
	}

	if strings.TrimSpace(ps.UPS) == "" {
		result.AddError("%s: no ups configured. This is the UPS name as it "+
			"appears in ups.conf, not a hostname.", context)
	}

	if ps.Port < 0 || ps.Port > 65535 {
		result.AddError("%s: port %d is out of range", context, ps.Port)
	}

	if ps.Delay < 0 {
		result.AddError("%s: delay cannot be negative", context)
	}

	// Instant commands require authentication on any NUT server that is not
	// wide open, which is the reason post_shutdown never worked before.
	if (ps.Username == "") != (ps.Password == "") {
		result.AddError("%s: username and password must be set together", context)
	}
	if ps.Username == "" && ps.Password == "" {
		result.AddWarning("%s: no credentials configured. NUT requires "+
			"authentication for instant commands, so this will be refused "+
			"with ACCESS-DENIED unless upsd is configured to allow it "+
			"unauthenticated.", context)
	}

	if strings.TrimSpace(ps.Host) == "" {
		result.AddInfo("%s: no host configured; connecting to localhost. Set "+
			"host if the NUT server runs elsewhere.", context)
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

func validateConditionConfig(c *ConditionConfig, context string, result *ValidationResult, reg *Registry) {
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
			validateConditionConfig(&c.Conditions[i], fmt.Sprintf("%s.conditions[%d]", context, i), result, reg)
		}
	case "not":
		if len(c.Conditions) != 1 {
			result.AddError("%s: not condition requires exactly one nested condition", context)
		}
		if len(c.Conditions) > 0 {
			validateConditionConfig(&c.Conditions[0], fmt.Sprintf("%s.conditions[0]", context), result, reg)
		}
	case "template":
		if c.Value == "" {
			result.AddError("%s: template condition requires 'value'", context)
			break
		}
		// ValidateExpr existed from the beginning and was never called, so a
		// malformed expression was not a config error — it simply evaluated
		// to unavailable forever, and the condition it guarded never fired.
		if err := ExprValidator(c.Value); err != nil {
			result.AddError("%s: %s", context, err)
		}
	case "", "true", "false":
		// literal conditions are always valid
	default:
		result.AddWarning("%s: unknown condition type %q", context, condType)
	}

	if c.For != "" {
		if d, err := ParseDuration(c.For); err != nil {
			result.AddError("%s: invalid 'for' duration: %s", context, err)
		} else if d < 0 {
			result.AddError("%s: 'for' duration cannot be negative", context)
		}
	}

	// Fact references are checked against what the configured sources
	// actually declare. A typo here is invisible at runtime: the condition
	// evaluates to unavailable forever and its plan never triggers.
	if c.Fact != "" && reg != nil && len(reg.Facts) > 0 && !slices.Contains(reg.Facts, c.Fact) {
		result.AddError("%s: unknown fact %q (did you mean one of: %s)",
			context, c.Fact, strings.Join(nearestFacts(c.Fact, reg.Facts), ", "))
	}
}

// checkDuration validates a configured duration.
//
// time.ParseDuration accepts a negative value quite happily, and a negative
// budget or timeout produces a context that has already expired — so a
// shutdown gets no time at all and every host ends up down_unverified.
func checkDuration(result *ValidationResult, value, context string) {
	if strings.TrimSpace(value) == "" {
		return
	}

	d, err := ParseDuration(value)
	if err != nil {
		result.AddError("%s: %s", context, err)
		return
	}
	if d < 0 {
		result.AddError("%s: %q is negative; a duration cannot run backwards",
			context, value)
	}
}

// ExprValidator type-checks a template expression.
//
// It is a variable so the config package need not import the conditions
// package, which imports config; main wires the real implementation in.
var ExprValidator = func(expression string) error { return nil }

// nearestFacts returns up to three declared facts sharing a prefix with the
// unknown one, to make the error actionable.
func nearestFacts(unknown string, known []string) []string {
	source, _, _ := strings.Cut(unknown, ".")

	var matches []string
	for _, k := range known {
		if strings.HasPrefix(k, source+".") {
			matches = append(matches, k)
		}
	}
	if len(matches) == 0 {
		matches = known
	}

	slices.Sort(matches)
	if len(matches) > 3 {
		matches = matches[:3]
	}
	return matches
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
