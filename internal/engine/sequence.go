package engine

import (
	"time"
)

type SequenceState int

const (
	SequenceIdle SequenceState = iota
	SequenceTriggered
	SequenceShuttingDown
	SequenceAborted
	SequenceWakeGate
	SequenceWaking
	SequenceCompleted
	SequenceFailed
)

func (s SequenceState) String() string {
	switch s {
	case SequenceTriggered:
		return "triggered"
	case SequenceShuttingDown:
		return "shutting_down"
	case SequenceAborted:
		return "aborted"
	case SequenceWakeGate:
		return "wake_gate"
	case SequenceWaking:
		return "waking"
	case SequenceCompleted:
		return "completed"
	case SequenceFailed:
		return "failed"
	default:
		return "idle"
	}
}

type Sequence struct {
	ID               string
	PlanName         string
	State            SequenceState
	CurrentStage     int
	PonrCrossed      bool
	StartedAt        time.Time
	CompletedAt      *time.Time
	PreSequenceState map[string]ClientState // client name -> state before sequence
	ResolvedAddrs    map[string]ResolvedAddr
	ConfigSnapshot   []byte // serialized config at trigger time
}

type ResolvedAddr struct {
	IP        string
	MAC       string
	Hostname  string
	ResolvedAt time.Time
}

type IntentRecord struct {
	ID         string
	SequenceID string
	ClientName string
	Action     ActionType
	Timestamp  time.Time
	Status     IntentStatus
	Result     *ActionResult
}

type IntentStatus int

const (
	IntentDispatching IntentStatus = iota
	IntentDispatched
)

func (s IntentStatus) String() string {
	if s == IntentDispatched {
		return "dispatched"
	}
	return "dispatching"
}

type StageRecord struct {
	SequenceID string
	StageIndex int
	StageName  string
	StartedAt  time.Time
	CompletedAt *time.Time
	Clients    map[string]ClientStageResult
}

type ClientStageResult struct {
	State      ClientState
	StartedAt  time.Time
	CompletedAt *time.Time
	Error      string
}
