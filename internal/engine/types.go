package engine

import (
	"context"
	"strings"
	"time"
)

type ClientState int

const (
	StateUnknown ClientState = iota
	StateUp
	StateShuttingDown
	StateDown
	StateDownUnverified
	StateWaking
	StateFailed
)

func (s ClientState) String() string {
	switch s {
	case StateUp:
		return "up"
	case StateShuttingDown:
		return "shutting_down"
	case StateDown:
		return "down"
	case StateDownUnverified:
		return "down_unverified"
	case StateWaking:
		return "waking"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func ParseClientState(s string) ClientState {
	switch s {
	case "up":
		return StateUp
	case "shutting_down":
		return StateShuttingDown
	case "down":
		return StateDown
	case "down_unverified":
		return StateDownUnverified
	case "waking":
		return StateWaking
	case "failed":
		return StateFailed
	default:
		return StateUnknown
	}
}

type ActionType int

const (
	ActionShutdown ActionType = iota
	ActionWake
	ActionProbe
	ActionPoeOff
	ActionPoeOn
	ActionOutletOff
	ActionOutletOn
)

func (a ActionType) String() string {
	switch a {
	case ActionShutdown:
		return "shutdown"
	case ActionWake:
		return "wake"
	case ActionProbe:
		return "probe"
	case ActionPoeOff:
		return "poe_off"
	case ActionPoeOn:
		return "poe_on"
	case ActionOutletOff:
		return "outlet_off"
	case ActionOutletOn:
		return "outlet_on"
	default:
		return "unknown"
	}
}

type Capability struct {
	Action     ActionType
	Idempotent bool
	Timeout    time.Duration
}

type ActionResult struct {
	Success bool
	Message string
	Error   error
}

type Transport interface {
	Name() string
	Capabilities() []Capability
	Execute(ctx context.Context, client *Client, action ActionType) (*ActionResult, error)
	Probe(ctx context.Context, client *Client) (ClientState, error)
}

type ActionRemapper interface {
	RemapAction(action ActionType) ActionType
}

type Source interface {
	Name() string
	Declarations() []SourceDeclaration
	Start(ctx context.Context, updates chan<- FactUpdate) error
	Stop() error
}

type SourceDeclaration struct {
	InstanceName string
	PollInterval time.Duration
	Facts        []FactDeclEntry
}

type FactDeclEntry struct {
	Name        string
	Type        string
	Range       []float64
	Values      []string
	Description string
	Unit        string
}

type FactUpdate struct {
	Key       string
	Value     any
	Timestamp time.Time
}

type Client struct {
	Name            string
	Description     string
	Transport       string
	Address         string
	MAC             string
	Credentials     string
	Tags            []string
	Feeds           []string
	FeedPolicy      FeedPolicy
	ShutdownBudget  time.Duration
	WakePolicy      WakePolicy
	DependsOn       []string
	After           []string
	Before          []string
	ProbeConfig     ProbeConfig
	WakeConfig      *WakeConfig
	TransportConfig map[string]any
	GuardPeriod     time.Duration
}

type FeedPolicy int

const (
	FeedPolicyAny FeedPolicy = iota
	FeedPolicyAll
)

func (p FeedPolicy) String() string {
	if p == FeedPolicyAll {
		return "all"
	}
	return "any"
}

type WakePolicy int

const (
	WakePolicyPowerState WakePolicy = iota
	WakePolicyRetainState
)

func (p WakePolicy) String() string {
	if p == WakePolicyRetainState {
		return "retain_state"
	}
	return "power_state"
}

type ProbeConfig struct {
	Method  string
	Port    int
	Timeout time.Duration
}

type WakeConfig struct {
	Transport string
	MAC       string
	Broadcast string
	Config    map[string]any
}

type Mode int

const (
	ModeDisarmed Mode = iota
	ModeDryRun
	ModeArmed
)

func (m Mode) String() string {
	switch m {
	case ModeDryRun:
		return "dry-run"
	case ModeArmed:
		return "armed"
	default:
		return "disarmed"
	}
}

// ParseMode maps a mode name to a Mode, defaulting to disarmed.
//
// The default is deliberate for config loading: an unrecognised value should
// leave the system inert rather than guess. Callers that need to reject a
// typo rather than silently disarm should use ParseModeStrict.
func ParseMode(s string) Mode {
	mode, _ := ParseModeStrict(s)
	return mode
}

// ParseModeStrict maps a mode name to a Mode, reporting whether it was
// recognised.
func ParseModeStrict(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "disarmed":
		return ModeDisarmed, true
	case "dry-run", "dryrun", "dry_run":
		return ModeDryRun, true
	case "armed":
		return ModeArmed, true
	default:
		return ModeDisarmed, false
	}
}
