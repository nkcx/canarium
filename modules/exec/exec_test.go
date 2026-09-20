package exec

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nkcx/canarium/internal/engine"
)

func client(cfg map[string]any) *engine.Client {
	return &engine.Client{
		Name:            "nas",
		Address:         "10.0.10.20",
		MAC:             "aa:bb:cc:dd:ee:ff",
		TransportConfig: cfg,
	}
}

func TestExecuteRunsShutdownCommand(t *testing.T) {
	tr := New()

	result, err := tr.Execute(context.Background(),
		client(map[string]any{"shutdown_command": "echo shutting-down"}),
		engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Errorf("Success = false: %s", result.Message)
	}
	if !strings.Contains(result.Message, "shutting-down") {
		t.Errorf("Message = %q, want the command's output", result.Message)
	}
}

func TestExecuteFallsBackToGenericCommand(t *testing.T) {
	tr := New()

	result, err := tr.Execute(context.Background(),
		client(map[string]any{"command": "echo generic"}),
		engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Message, "generic") {
		t.Errorf("Message = %q, want the generic command's output", result.Message)
	}
}

func TestExecuteRequiresACommand(t *testing.T) {
	tr := New()

	if _, err := tr.Execute(context.Background(), client(nil), engine.ActionShutdown); err == nil {
		t.Error("Execute succeeded with no command configured")
	}
}

func TestExecuteReportsFailure(t *testing.T) {
	tr := New()

	result, err := tr.Execute(context.Background(),
		client(map[string]any{"command": "exit 3"}), engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute returned an error rather than a failed result: %v", err)
	}
	if result.Success {
		t.Error("a command exiting 3 was reported as success")
	}
}

func TestExpandVars(t *testing.T) {
	tr := New()

	result, err := tr.Execute(context.Background(),
		client(map[string]any{"command": "echo {name} {address} {mac}"}),
		engine.ActionShutdown)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, want := range []string{"nas", "10.0.10.20", "aa:bb:cc:dd:ee:ff"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("output %q does not contain %q", result.Message, want)
		}
	}
}

func TestExecuteHonoursContext(t *testing.T) {
	tr := New()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	result, err := tr.Execute(ctx, client(map[string]any{"command": "sleep 10"}),
		engine.ActionShutdown)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("a command killed by its context was reported as success")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute took %v; the context deadline was not honoured", elapsed)
	}
}

// TestProbeExitZeroIsUp / exit one is down follows ping's convention.
func TestProbeExitCodes(t *testing.T) {
	tr := New()

	up, err := tr.Probe(context.Background(), client(map[string]any{"probe_command": "true"}))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if up != engine.StateUp {
		t.Errorf("exit 0 = %v, want up", up)
	}

	down, err := tr.Probe(context.Background(), client(map[string]any{"probe_command": "exit 1"}))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if down != engine.StateDown {
		t.Errorf("exit 1 = %v, want down", down)
	}
}

// TestProbeOtherExitCodeIsAnError is the regression test for treating any
// non-zero exit as "down". A missing, non-executable or crashing probe
// script told us nothing about the host, and reporting it as a confirmed
// shutdown let the sequence move on.
func TestProbeOtherExitCodeIsAnError(t *testing.T) {
	tr := New()

	for _, cmd := range []string{"exit 2", "exit 127", "this-command-does-not-exist"} {
		t.Run(cmd, func(t *testing.T) {
			state, err := tr.Probe(context.Background(),
				client(map[string]any{"probe_command": cmd}))
			if err == nil {
				t.Errorf("probe_command %q returned state %v with no error; "+
					"a broken probe must not read as a confirmed shutdown", cmd, state)
			}
			if state == engine.StateDown {
				t.Errorf("probe_command %q reported the host as down", cmd)
			}
		})
	}
}

func TestProbeRequiresACommand(t *testing.T) {
	tr := New()

	if _, err := tr.Probe(context.Background(), client(nil)); err == nil {
		t.Error("Probe succeeded with no probe_command configured")
	}
}

func TestCapabilities(t *testing.T) {
	tr := New()

	var actions []engine.ActionType
	for _, c := range tr.Capabilities() {
		actions = append(actions, c.Action)
	}

	for _, want := range []engine.ActionType{
		engine.ActionShutdown, engine.ActionWake, engine.ActionProbe,
	} {
		found := false
		for _, a := range actions {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("capability %v is not advertised", want)
		}
	}
}
