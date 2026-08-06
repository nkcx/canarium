package conditions

import (
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/config"
	"github.com/nkcx/canarium/internal/facts"
)

func setupStore() *facts.Store {
	store := facts.NewStore()
	store.RegisterSource("ups", 10*time.Second, []facts.FactDeclaration{
		{Name: "charge", Type: "percent"},
		{Name: "runtime", Type: "number"},
		{Name: "status", Type: "set", Values: []string{"OL", "OB", "LB"}},
	})
	return store
}

func TestEvaluateNumericBelow(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 45.0, time.Now())
	eval := NewEvaluator(store)

	below50 := float64(50)
	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.charge",
		Below:     &below50,
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("45 below 50 = %s, want true", result)
	}

	store.Update("ups.charge", 55.0, time.Now())
	result = eval.Evaluate(cond, time.Now())
	if result != facts.False {
		t.Errorf("55 below 50 = %s, want false", result)
	}
}

func TestEvaluateNumericAbove(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 75.0, time.Now())
	eval := NewEvaluator(store)

	above60 := float64(60)
	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.charge",
		Above:     &above60,
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("75 above 60 = %s, want true", result)
	}
}

func TestEvaluateNumericUnknownFact(t *testing.T) {
	store := setupStore()
	eval := NewEvaluator(store)

	below50 := float64(50)
	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.charge",
		Below:     &below50,
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.Unavailable {
		t.Errorf("unknown fact = %s, want unavailable", result)
	}
}

func TestEvaluateStateContains(t *testing.T) {
	store := setupStore()
	store.Update("ups.status", []string{"OL", "CHRG"}, time.Now())
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "state",
		Fact:      "ups.status",
		Contains:  "OL",
	}
	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("[OL, CHRG] contains OL = %s, want true", result)
	}

	cond.Contains = "OB"
	result = eval.Evaluate(cond, time.Now())
	if result != facts.False {
		t.Errorf("[OL, CHRG] contains OB = %s, want false", result)
	}
}

func TestEvaluateAnd(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 45.0, time.Now())
	store.Update("ups.status", []string{"OB"}, time.Now())
	eval := NewEvaluator(store)

	below50 := float64(50)
	cond := &config.ConditionConfig{
		Condition: "and",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.charge", Below: &below50},
			{Condition: "state", Fact: "ups.status", Contains: "OB"},
		},
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("and(charge<50, status contains OB) = %s, want true", result)
	}
}

func TestEvaluateOr(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 55.0, time.Now())
	store.Update("ups.status", []string{"OB"}, time.Now())
	eval := NewEvaluator(store)

	below50 := float64(50)
	cond := &config.ConditionConfig{
		Condition: "or",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.charge", Below: &below50},
			{Condition: "state", Fact: "ups.status", Contains: "OB"},
		},
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("or(charge<50=false, status OB=true) = %s, want true", result)
	}
}

func TestEvaluateNot(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 80.0, time.Now())
	eval := NewEvaluator(store)

	below50 := float64(50)
	cond := &config.ConditionConfig{
		Condition: "not",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.charge", Below: &below50},
		},
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("not(80 < 50) = %s, want true", result)
	}
}

func TestEvaluateNotUnavailable(t *testing.T) {
	store := setupStore()
	eval := NewEvaluator(store)

	below50 := float64(50)
	cond := &config.ConditionConfig{
		Condition: "not",
		Conditions: []config.ConditionConfig{
			{Condition: "numeric", Fact: "ups.charge", Below: &below50},
		},
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.Unavailable {
		t.Errorf("not(unavailable) = %s, want unavailable", result)
	}
}

func TestEvaluateLiteral(t *testing.T) {
	store := setupStore()
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{Value: "true"}
	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("literal true = %s, want true", result)
	}
}

func TestEvaluateTemplate(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 45.0, time.Now())
	eval := NewEvaluator(store)

	cond := &config.ConditionConfig{
		Condition: "template",
		Value:     `fact("ups.charge") < 50`,
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.True {
		t.Errorf("template fact('ups.charge') < 50 with charge=45 = %s, want true", result)
	}
}

func TestDwellNotSatisfiedImmediately(t *testing.T) {
	store := setupStore()
	store.Update("ups.charge", 80.0, time.Now())
	eval := NewEvaluator(store)

	above60 := float64(60)
	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.charge",
		Above:     &above60,
		For:       "5m",
	}

	result := eval.Evaluate(cond, time.Now())
	if result != facts.False {
		t.Errorf("dwell not yet satisfied = %s, want false", result)
	}
}

func TestDwellResetOnFalse(t *testing.T) {
	store := setupStore()
	eval := NewEvaluator(store)

	above60 := float64(60)
	cond := &config.ConditionConfig{
		Condition: "numeric",
		Fact:      "ups.charge",
		Above:     &above60,
		For:       "1ms",
	}

	store.Update("ups.charge", 80.0, time.Now())
	eval.Evaluate(cond, time.Now())

	store.Update("ups.charge", 40.0, time.Now())
	result := eval.Evaluate(cond, time.Now())
	if result != facts.False {
		t.Errorf("after dwell reset = %s, want false", result)
	}
}
