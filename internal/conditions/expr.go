package conditions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// evaluateExpr runs a template expression against the current fact store.
//
// sawUnavailable reports whether any fact() call during evaluation returned
// nil because the fact was unknown or stale. SPEC §4.6: "A nil result from
// fact() in any comparison evaluates to unavailable under three-valued
// logic." The expression language itself has only two-valued booleans, so a
// comparison against a missing fact yields false — which would be read as a
// definite "condition not met" rather than "we do not know". Tracking the
// flag lets the caller promote the result to Unavailable and keeps templates
// consistent with structured conditions.
//
// quality() and age() deliberately do not set the flag: their entire purpose
// is to inspect availability, so an expression like
// `quality("ups.status") == "stale"` must still produce a definite answer.
func (e *Evaluator) evaluateExpr(expression string) (result any, sawUnavailable bool, err error) {
	var unavailable bool
	env := e.buildExprEnv(&unavailable)

	program, err := e.compile(expression, env)
	if err != nil {
		return nil, false, err
	}

	result, err = expr.Run(program, env)
	if err != nil {
		return nil, false, fmt.Errorf("evaluating expression: %w", err)
	}

	return result, unavailable, nil
}

// compile returns a compiled program for expression, reusing a previously
// compiled one when possible.
//
// Templates are evaluated on every policy tick — every five seconds, for
// every plan — and compilation is far more expensive than execution. The
// cache is keyed by source text and bounded by the number of distinct
// expressions in the config, which is fixed at load time.
func (e *Evaluator) compile(expression string, env map[string]any) (*vm.Program, error) {
	e.programMu.RLock()
	cached, ok := e.programs[expression]
	e.programMu.RUnlock()
	if ok {
		return cached, nil
	}

	program, err := expr.Compile(expression, expr.Env(env))
	if err != nil {
		return nil, fmt.Errorf("compiling expression: %w", err)
	}

	e.programMu.Lock()
	e.programs[expression] = program
	e.programMu.Unlock()

	return program, nil
}

// buildExprEnv constructs the function environment for one evaluation.
//
// unavailable is set if any fact() lookup finds an unknown or stale fact.
func (e *Evaluator) buildExprEnv(unavailable *bool) map[string]any {
	env := make(map[string]any)

	env["fact"] = func(key string) any {
		v := e.store.FactValue(key)
		if v == nil {
			*unavailable = true
		}
		return v
	}

	env["quality"] = func(key string) string {
		return e.store.FactQuality(key)
	}

	env["age"] = func(key string) float64 {
		return e.store.FactAge(key)
	}

	env["contains"] = func(set any, value string) bool {
		switch v := set.(type) {
		case []string:
			return slices.Contains(v, value)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok && s == value {
					return true
				}
			}
		case string:
			return strings.Contains(v, value)
		}
		return false
	}

	return env
}

// ValidateExpr type-checks a template expression without evaluating it.
//
// Used by `canarium validate` so a malformed expression is a config error
// rather than a condition that silently evaluates to unavailable forever.
func ValidateExpr(expression string) error {
	env := map[string]any{
		"fact":     func(key string) any { return nil },
		"quality":  func(key string) string { return "unknown" },
		"age":      func(key string) float64 { return 0 },
		"contains": func(set any, value string) bool { return false },
	}

	if _, err := expr.Compile(expression, expr.Env(env)); err != nil {
		return fmt.Errorf("invalid expression: %w", err)
	}
	return nil
}
