package conditions

import (
	"fmt"
	"strings"
	"time"

	"github.com/expr-lang/expr"
)

func (e *Evaluator) evaluateExpr(expression string) (any, error) {
	env := e.buildExprEnv()

	program, err := expr.Compile(expression, expr.Env(env))
	if err != nil {
		return nil, fmt.Errorf("compiling expression: %w", err)
	}

	result, err := expr.Run(program, env)
	if err != nil {
		return nil, fmt.Errorf("evaluating expression: %w", err)
	}

	return result, nil
}

func (e *Evaluator) buildExprEnv() map[string]any {
	env := make(map[string]any)

	env["fact"] = func(key string) any {
		return e.store.FactValue(key)
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
			for _, s := range v {
				if s == value {
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

func ValidateExpr(expression string, factKeys []string) error {
	env := make(map[string]any)

	env["fact"] = func(key string) any { return nil }
	env["quality"] = func(key string) string { return "unknown" }
	env["age"] = func(key string) float64 { return 0 }
	env["contains"] = func(set any, value string) bool { return false }

	_, err := expr.Compile(expression, expr.Env(env))
	if err != nil {
		return fmt.Errorf("invalid expression: %w", err)
	}
	return nil
}

func monoNow() int64 {
	return time.Now().UnixNano()
}
