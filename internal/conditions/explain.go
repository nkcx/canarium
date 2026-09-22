package conditions

import (
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

// Explanation is a read-only account of how a condition evaluates right now,
// for display.
//
// It exists because Evaluate is not read-only. A condition with `for:`
// advances its dwell timer on every call and persists it, so the only
// correct caller is the policy loop, which samples every condition on a
// fixed tick. An API handler calling Evaluate whenever someone opened a page
// would sample sporadically, miss a flap that happened between two page
// views, and over-credit the timer -- changing when a plan fires because
// somebody looked at it.
//
// Explain therefore never touches dwell state. It reports the instantaneous
// result, and for dwell conditions reads the timer the policy loop is
// already maintaining.
type Explanation struct {
	// Condition is the resolved condition type: numeric, state, and, or,
	// not, template, true or false.
	Condition string `json:"condition"`

	Fact     string   `json:"fact,omitempty"`
	Above    *float64 `json:"above,omitempty"`
	Below    *float64 `json:"below,omitempty"`
	Equals   any      `json:"equals,omitempty"`
	Is       string   `json:"is,omitempty"`
	IsNot    string   `json:"is_not,omitempty"`
	In       []string `json:"in,omitempty"`
	Contains string   `json:"contains,omitempty"`
	Value    string   `json:"value,omitempty"`
	For      string   `json:"for,omitempty"`

	// Result is what the policy loop would currently see: the instantaneous
	// result with any dwell requirement applied.
	Result string `json:"result"`

	// Instant is the result ignoring `for:`. It differs from Result only
	// while a dwell timer is running, which is the case worth showing: "the
	// condition is true, and has been for 12 of the required 30 seconds".
	Instant string `json:"instant"`

	// Dwell is progress toward `for:`, when the condition has one.
	Dwell *DwellExplanation `json:"dwell,omitempty"`

	// FactValue and FactQuality are the reading a leaf condition decided
	// on, so the UI can say "rack_ups.status is OL" rather than just
	// "false".
	FactValue   any    `json:"fact_value,omitempty"`
	FactQuality string `json:"fact_quality,omitempty"`

	Conditions []Explanation `json:"conditions,omitempty"`
}

// DwellExplanation is read-only progress toward a `for:` requirement.
type DwellExplanation struct {
	RequiredSeconds float64 `json:"required_seconds"`
	ElapsedSeconds  float64 `json:"elapsed_seconds"`

	// Tracked is false when the policy loop is not currently evaluating
	// this condition -- a stage entry condition while no sequence is
	// running, for instance. Elapsed is then zero because nothing is
	// measuring it, not because the condition has been false.
	Tracked bool `json:"tracked"`
}

// Explain describes how a condition evaluates now, without side effects.
func (e *Evaluator) Explain(cond *config.ConditionConfig, now time.Time) Explanation {
	if cond == nil {
		return Explanation{Condition: "true", Result: facts.Unavailable.String(), Instant: facts.Unavailable.String()}
	}

	ex := Explanation{
		Condition: inferConditionType(cond),
		Fact:      cond.Fact,
		Above:     cond.Above,
		Below:     cond.Below,
		Equals:    cond.Equals,
		Is:        cond.Is,
		IsNot:     cond.IsNot,
		In:        cond.In,
		Contains:  cond.Contains,
		Value:     cond.Value,
		For:       cond.For,
	}

	var instant facts.Trilean
	switch ex.Condition {
	case "and":
		instant = facts.True
		for i := range cond.Conditions {
			child := e.Explain(&cond.Conditions[i], now)
			ex.Conditions = append(ex.Conditions, child)
			instant = facts.And(instant, parseTrilean(child.Result))
		}
	case "or":
		instant = facts.False
		for i := range cond.Conditions {
			child := e.Explain(&cond.Conditions[i], now)
			ex.Conditions = append(ex.Conditions, child)
			instant = facts.Or(instant, parseTrilean(child.Result))
		}
	case "not":
		instant = facts.Unavailable
		if len(cond.Conditions) > 0 {
			child := e.Explain(&cond.Conditions[0], now)
			ex.Conditions = append(ex.Conditions, child)
			instant = facts.Not(parseTrilean(child.Result))
		}
	default:
		// Leaf conditions are pure reads of the fact store, so the
		// ordinary implementation is safe to reuse.
		instant = e.evaluateInner(cond, now)
		if cond.Fact != "" {
			value, quality, _ := e.store.Get(cond.Fact)
			ex.FactValue = value
			ex.FactQuality = quality.String()
		}
	}

	ex.Instant = instant.String()
	ex.Result = ex.Instant

	if cond.For != "" {
		required, err := config.ParseDuration(cond.For)
		if err == nil && required > 0 {
			ex.Dwell, ex.Result = e.explainDwell(cond, instant, required, now)
		}
	}

	return ex
}

// explainDwell reads the policy loop's timer for a condition.
func (e *Evaluator) explainDwell(
	cond *config.ConditionConfig,
	instant facts.Trilean,
	required time.Duration,
	now time.Time,
) (*DwellExplanation, string) {
	dwell := &DwellExplanation{RequiredSeconds: required.Seconds()}

	e.mu.RLock()
	tracker, tracked := e.dwellState[conditionKey(cond)]
	var elapsed time.Duration
	if tracked {
		elapsed = tracker.Elapsed(now)
	}
	e.mu.RUnlock()

	dwell.Tracked = tracked

	// Dwell only accrues while the condition holds. If it does not hold
	// now, the timer the policy loop keeps is about to reset, and showing
	// its stale credit would claim progress that no longer exists.
	if instant != facts.True {
		return dwell, instant.String()
	}

	dwell.ElapsedSeconds = elapsed.Seconds()
	if tracked && elapsed >= required {
		return dwell, facts.True.String()
	}
	return dwell, facts.False.String()
}

func parseTrilean(s string) facts.Trilean {
	switch s {
	case "true":
		return facts.True
	case "false":
		return facts.False
	default:
		return facts.Unavailable
	}
}
