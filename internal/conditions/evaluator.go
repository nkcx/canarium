package conditions

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/expr-lang/expr/vm"
	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

type Evaluator struct {
	store      *facts.Store
	dwellState map[string]*DwellTracker
	mu         sync.RWMutex

	// programs caches compiled template expressions. Compilation dominates
	// evaluation cost and templates are re-evaluated on every policy tick.
	programMu sync.RWMutex
	programs  map[string]*vm.Program

	// dwellStore persists dwell progress across restarts. Nil means
	// in-memory only, which is the behaviour under test and in simulation.
	dwellStore DwellStore

	// onPersistError is called when a dwell write fails. Persistence is
	// best-effort: losing it costs accumulated dwell credit on the next
	// restart, which is not worth failing an evaluation over.
	onPersistError func(key string, err error)
}

// SetPersistErrorHandler registers a callback for dwell persistence failures.
func (e *Evaluator) SetPersistErrorHandler(fn func(key string, err error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onPersistError = fn
}

func NewEvaluator(store *facts.Store) *Evaluator {
	return &Evaluator{
		store:      store,
		dwellState: make(map[string]*DwellTracker),
		programs:   make(map[string]*vm.Program),
	}
}

func (e *Evaluator) Evaluate(cond *config.ConditionConfig, now time.Time) facts.Trilean {
	result := e.evaluateInner(cond, now)

	if cond.For != "" {
		dur, err := config.ParseDuration(cond.For)
		if err != nil || dur == 0 {
			return result
		}
		return e.applyDwell(cond, result, dur, now)
	}

	return result
}

func (e *Evaluator) evaluateInner(cond *config.ConditionConfig, now time.Time) facts.Trilean {
	condType := inferConditionType(cond)

	switch condType {
	case "numeric":
		return e.evaluateNumeric(cond)
	case "state":
		return e.evaluateState(cond)
	case "and":
		return e.evaluateAnd(cond, now)
	case "or":
		return e.evaluateOr(cond, now)
	case "not":
		return e.evaluateNot(cond, now)
	case "template":
		return e.evaluateTemplate(cond, now)
	case "true":
		return facts.True
	case "false":
		return facts.False
	default:
		return facts.Unavailable
	}
}

func (e *Evaluator) evaluateNumeric(cond *config.ConditionConfig) facts.Trilean {
	val, quality, _ := e.store.Get(cond.Fact)
	if !usable(quality, val) {
		return facts.Unavailable
	}

	num, ok := toFloat64(val)
	if !ok {
		return facts.Unavailable
	}

	if cond.Above != nil {
		if num <= *cond.Above {
			return facts.False
		}
	}
	if cond.Below != nil {
		if num >= *cond.Below {
			return facts.False
		}
	}
	if cond.Equals != nil {
		target, ok := toFloat64(cond.Equals)
		if !ok {
			return facts.Unavailable
		}
		if num != target {
			return facts.False
		}
	}

	return facts.True
}

func (e *Evaluator) evaluateState(cond *config.ConditionConfig) facts.Trilean {
	val, quality, _ := e.store.Get(cond.Fact)
	if !usable(quality, val) {
		return facts.Unavailable
	}

	if cond.Contains != "" {
		switch v := val.(type) {
		case []string:
			return facts.BoolToTrilean(slices.Contains(v, cond.Contains))
		case []any:
			// Facts loaded from JSON (simulation timelines) or from generic
			// YAML decode to []any rather than []string.
			for _, item := range v {
				if s, ok := item.(string); ok && s == cond.Contains {
					return facts.True
				}
			}
			return facts.False
		case string:
			// Substring, not equality. The expression-language contains()
			// helper has always meant substring for strings, and a condition
			// written `contains: OB` against a status string of "OB LB" must
			// match — which is precisely the case a UPS produces.
			return facts.BoolToTrilean(strings.Contains(v, cond.Contains))
		default:
			return facts.Unavailable
		}
	}

	strVal := fmt.Sprintf("%v", val)

	if cond.Is != "" {
		return facts.BoolToTrilean(strVal == cond.Is)
	}
	if cond.IsNot != "" {
		return facts.BoolToTrilean(strVal != cond.IsNot)
	}
	if len(cond.In) > 0 {
		for _, allowed := range cond.In {
			if strVal == allowed {
				return facts.True
			}
		}
		return facts.False
	}

	return facts.Unavailable
}

func (e *Evaluator) evaluateAnd(cond *config.ConditionConfig, now time.Time) facts.Trilean {
	result := facts.True
	for i := range cond.Conditions {
		val := e.Evaluate(&cond.Conditions[i], now)
		result = facts.And(result, val)
		if result == facts.False {
			return facts.False
		}
	}
	return result
}

func (e *Evaluator) evaluateOr(cond *config.ConditionConfig, now time.Time) facts.Trilean {
	result := facts.False
	for i := range cond.Conditions {
		val := e.Evaluate(&cond.Conditions[i], now)
		result = facts.Or(result, val)
		if result == facts.True {
			return facts.True
		}
	}
	return result
}

func (e *Evaluator) evaluateNot(cond *config.ConditionConfig, now time.Time) facts.Trilean {
	if len(cond.Conditions) == 0 {
		return facts.Unavailable
	}
	return facts.Not(e.Evaluate(&cond.Conditions[0], now))
}

func (e *Evaluator) evaluateTemplate(cond *config.ConditionConfig, now time.Time) facts.Trilean {
	expression := cond.Value
	if expression == "" {
		return facts.Unavailable
	}

	result, sawUnavailable, err := e.evaluateExpr(expression, now)
	if err != nil {
		// A template that cannot be evaluated tells us nothing about the
		// world; it must not read as a definite false.
		return facts.Unavailable
	}

	// An expression that consulted an unknown or stale fact produces an
	// answer we cannot trust, even if the language gave us a clean boolean.
	if sawUnavailable {
		return facts.Unavailable
	}

	if b, ok := result.(bool); ok {
		return facts.BoolToTrilean(b)
	}
	return facts.Unavailable
}

func inferConditionType(cond *config.ConditionConfig) string {
	if cond.Condition != "" {
		return cond.Condition
	}
	if cond.Value != "" {
		return "template"
	}
	if cond.Fact != "" {
		if cond.Above != nil || cond.Below != nil || cond.Equals != nil {
			return "numeric"
		}
		return "state"
	}
	return "true"
}

// usable reports whether a fact may be used to decide a condition.
//
// Only QualityGood counts. A stale fact still holds its last observed value,
// and an earlier version accepted it — so if the UPS source died moments
// after reporting "on battery, 25% charge", that reading stayed true forever
// and would fire a shutdown from data that no longer described reality.
//
// SPEC §4.4: "Unknown or stale facts produce `unavailable` in condition
// evaluation, which never satisfies triggers, gates, or aborts." Under
// three-valued logic Unavailable never satisfies anything, so treating
// staleness this way is fail-safe everywhere it appears: a stale trigger
// does not fire, a stale wake gate does not wake, and a stale stage
// condition waits rather than acting.
func usable(quality facts.Quality, val any) bool {
	return quality == facts.QualityGood && val != nil
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	default:
		return 0, false
	}
}
