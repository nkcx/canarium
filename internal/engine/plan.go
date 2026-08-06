package engine

import "time"

type Plan struct {
	Name         string
	Trigger      Condition
	Abort        *Condition
	Shutdown     ShutdownPlan
	Wake         WakePlan
	PostShutdown *PostShutdownAction
}

type ShutdownPlan struct {
	Stages []Stage
}

type Stage struct {
	Name           string
	When           Condition
	Clients        []string // resolved client names (tags expanded)
	Budget         time.Duration
	WaitTimeout    time.Duration
	WaitPolicy     WaitPolicy
	PointOfNoReturn bool
}

type WaitPolicy int

const (
	WaitPolicySkip WaitPolicy = iota
	WaitPolicyEscalate
	WaitPolicyHold
)

func (p WaitPolicy) String() string {
	switch p {
	case WaitPolicyEscalate:
		return "escalate"
	case WaitPolicyHold:
		return "hold"
	default:
		return "skip"
	}
}

type WakePlan struct {
	Gate          Condition
	Stagger       time.Duration
	Order         WakeOrder
	Stages        []Stage // explicit wake stages, if not using reverse
	ProbeInterval time.Duration
	BootDeadline  time.Duration
	Retries       int
}

type WakeOrder int

const (
	WakeOrderReverse WakeOrder = iota
	WakeOrderExplicit
)

type PostShutdownAction struct {
	Action  string // "upscmd"
	Command string // "shutdown.return"
	Delay   int    // seconds
	UPS     string // which UPS to command
}

type Condition struct {
	Type       ConditionType
	Fact       string
	Operator   string
	Value      any
	Values     []any
	For        time.Duration
	Conditions []Condition
	Template   string
}

type ConditionType int

const (
	ConditionNumeric ConditionType = iota
	ConditionState
	ConditionAnd
	ConditionOr
	ConditionNot
	ConditionTemplate
	ConditionSchedule
	ConditionClientState
	ConditionLiteral // "true" or "false"
)

func (t ConditionType) String() string {
	switch t {
	case ConditionNumeric:
		return "numeric"
	case ConditionState:
		return "state"
	case ConditionAnd:
		return "and"
	case ConditionOr:
		return "or"
	case ConditionNot:
		return "not"
	case ConditionTemplate:
		return "template"
	case ConditionSchedule:
		return "schedule"
	case ConditionClientState:
		return "client_state"
	case ConditionLiteral:
		return "literal"
	default:
		return "unknown"
	}
}
