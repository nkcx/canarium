package conditions

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

type Evaluator struct {
	store      *facts.Store
	dwellState map[string]*DwellTracker
	mu         sync.RWMutex
}

type DwellTracker struct {
	ConditionHash string
	Required      time.Duration
	FirstTrue     time.Time
	LastTrue      time.Time
	Satisfied     bool
	MonoStart     int64 // monotonic nanoseconds
}

func NewEvaluator(store *facts.Store) *Evaluator {
	return &Evaluator{
		store:      store,
		dwellState: make(map[string]*DwellTracker),
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
		return e.evaluateTemplate(cond)
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
	if quality == facts.QualityUnknown || val == nil {
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
	if quality == facts.QualityUnknown || val == nil {
		return facts.Unavailable
	}

	if cond.Contains != "" {
		switch v := val.(type) {
		case []string:
			for _, s := range v {
				if s == cond.Contains {
					return facts.True
				}
			}
			return facts.False
		case string:
			return facts.BoolToTrilean(v == cond.Contains)
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

func (e *Evaluator) evaluateTemplate(cond *config.ConditionConfig) facts.Trilean {
	expr := cond.Value
	if expr == "" {
		return facts.Unavailable
	}

	result, err := e.evaluateExpr(expr)
	if err != nil {
		return facts.Unavailable
	}

	switch v := result.(type) {
	case bool:
		return facts.BoolToTrilean(v)
	case nil:
		return facts.Unavailable
	default:
		return facts.Unavailable
	}
}

func (e *Evaluator) applyDwell(cond *config.ConditionConfig, current facts.Trilean, required time.Duration, now time.Time) facts.Trilean {
	key := conditionKey(cond)

	e.mu.Lock()
	defer e.mu.Unlock()

	tracker, ok := e.dwellState[key]
	if !ok {
		tracker = &DwellTracker{
			Required: required,
		}
		e.dwellState[key] = tracker
	}

	if current == facts.True {
		if tracker.FirstTrue.IsZero() {
			tracker.FirstTrue = now
			tracker.MonoStart = monoNow()
		}
		tracker.LastTrue = now

		elapsed := time.Duration(monoNow()-tracker.MonoStart) * time.Nanosecond
		if elapsed >= required {
			tracker.Satisfied = true
			return facts.True
		}
		return facts.False
	}

	tracker.FirstTrue = time.Time{}
	tracker.MonoStart = 0
	tracker.Satisfied = false

	if current == facts.Unavailable {
		return facts.Unavailable
	}
	return facts.False
}

func (e *Evaluator) ResetDwell(cond *config.ConditionConfig) {
	key := conditionKey(cond)
	e.mu.Lock()
	delete(e.dwellState, key)
	e.mu.Unlock()
}

func (e *Evaluator) GetDwellState() map[string]*DwellTracker {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make(map[string]*DwellTracker, len(e.dwellState))
	for k, v := range e.dwellState {
		cp := *v
		result[k] = &cp
	}
	return result
}

func conditionKey(cond *config.ConditionConfig) string {
	var parts []string
	if cond.Condition != "" {
		parts = append(parts, cond.Condition)
	}
	if cond.Fact != "" {
		parts = append(parts, cond.Fact)
	}
	if cond.Above != nil {
		parts = append(parts, fmt.Sprintf("above:%v", *cond.Above))
	}
	if cond.Below != nil {
		parts = append(parts, fmt.Sprintf("below:%v", *cond.Below))
	}
	if cond.Is != "" {
		parts = append(parts, fmt.Sprintf("is:%s", cond.Is))
	}
	if cond.Contains != "" {
		parts = append(parts, fmt.Sprintf("contains:%s", cond.Contains))
	}
	if cond.Value != "" {
		parts = append(parts, fmt.Sprintf("tmpl:%s", cond.Value))
	}
	if cond.For != "" {
		parts = append(parts, fmt.Sprintf("for:%s", cond.For))
	}
	return strings.Join(parts, "|")
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
