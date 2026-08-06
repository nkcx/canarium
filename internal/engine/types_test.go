package engine

import "testing"

func TestClientStateString(t *testing.T) {
	tests := []struct {
		state ClientState
		want  string
	}{
		{StateUnknown, "unknown"},
		{StateUp, "up"},
		{StateShuttingDown, "shutting_down"},
		{StateDown, "down"},
		{StateDownUnverified, "down_unverified"},
		{StateWaking, "waking"},
		{StateFailed, "failed"},
	}

	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Errorf("ClientState(%d).String() = %s, want %s", tt.state, got, tt.want)
		}
	}
}

func TestParseClientState(t *testing.T) {
	tests := []struct {
		input string
		want  ClientState
	}{
		{"up", StateUp},
		{"down", StateDown},
		{"shutting_down", StateShuttingDown},
		{"down_unverified", StateDownUnverified},
		{"waking", StateWaking},
		{"failed", StateFailed},
		{"unknown", StateUnknown},
		{"garbage", StateUnknown},
	}

	for _, tt := range tests {
		got := ParseClientState(tt.input)
		if got != tt.want {
			t.Errorf("ParseClientState(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestModeString(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeDisarmed, "disarmed"},
		{ModeDryRun, "dry-run"},
		{ModeArmed, "armed"},
	}

	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("Mode(%d).String() = %s, want %s", tt.mode, got, tt.want)
		}
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"disarmed", ModeDisarmed},
		{"dry-run", ModeDryRun},
		{"dryrun", ModeDryRun},
		{"dry_run", ModeDryRun},
		{"armed", ModeArmed},
		{"invalid", ModeDisarmed},
	}

	for _, tt := range tests {
		if got := ParseMode(tt.input); got != tt.want {
			t.Errorf("ParseMode(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestActionTypeString(t *testing.T) {
	tests := []struct {
		action ActionType
		want   string
	}{
		{ActionShutdown, "shutdown"},
		{ActionWake, "wake"},
		{ActionProbe, "probe"},
		{ActionPoeOff, "poe_off"},
		{ActionPoeOn, "poe_on"},
		{ActionOutletOff, "outlet_off"},
		{ActionOutletOn, "outlet_on"},
	}

	for _, tt := range tests {
		if got := tt.action.String(); got != tt.want {
			t.Errorf("ActionType(%d).String() = %s, want %s", tt.action, got, tt.want)
		}
	}
}

func TestFeedPolicyString(t *testing.T) {
	if FeedPolicyAny.String() != "any" {
		t.Errorf("FeedPolicyAny = %s, want any", FeedPolicyAny)
	}
	if FeedPolicyAll.String() != "all" {
		t.Errorf("FeedPolicyAll = %s, want all", FeedPolicyAll)
	}
}

func TestWakePolicyString(t *testing.T) {
	if WakePolicyPowerState.String() != "power_state" {
		t.Errorf("WakePolicyPowerState = %s, want power_state", WakePolicyPowerState)
	}
	if WakePolicyRetainState.String() != "retain_state" {
		t.Errorf("WakePolicyRetainState = %s, want retain_state", WakePolicyRetainState)
	}
}
