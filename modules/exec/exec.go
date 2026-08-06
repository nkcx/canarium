package exec

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/engine"
)

type Transport struct{}

func New() *Transport {
	return &Transport{}
}

func (t *Transport) Name() string { return "exec" }

func (t *Transport) Capabilities() []engine.Capability {
	return []engine.Capability{
		{Action: engine.ActionShutdown, Idempotent: false, Timeout: 60 * time.Second},
		{Action: engine.ActionWake, Idempotent: false, Timeout: 60 * time.Second},
		{Action: engine.ActionProbe, Idempotent: true, Timeout: 30 * time.Second},
	}
}

func (t *Transport) Execute(ctx context.Context, client *engine.Client, action engine.ActionType) (*engine.ActionResult, error) {
	cmdKey := action.String() + "_command"
	cmdStr, ok := client.TransportConfig[cmdKey].(string)
	if !ok {
		cmdStr, ok = client.TransportConfig["command"].(string)
		if !ok {
			return nil, fmt.Errorf("exec transport: no command configured for action %s (set %s or command)", action, cmdKey)
		}
	}

	cmdStr = strings.ReplaceAll(cmdStr, "{address}", client.Address)
	cmdStr = strings.ReplaceAll(cmdStr, "{name}", client.Name)
	cmdStr = strings.ReplaceAll(cmdStr, "{mac}", client.MAC)

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &engine.ActionResult{
			Success: false,
			Message: fmt.Sprintf("exit error: %v, output: %s", err, string(output)),
		}, nil
	}

	return &engine.ActionResult{
		Success: true,
		Message: strings.TrimSpace(string(output)),
	}, nil
}

func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	cmdStr, ok := client.TransportConfig["probe_command"].(string)
	if !ok {
		return engine.StateUnknown, fmt.Errorf("exec transport: no probe_command configured")
	}

	cmdStr = strings.ReplaceAll(cmdStr, "{address}", client.Address)
	cmdStr = strings.ReplaceAll(cmdStr, "{name}", client.Name)

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	err := cmd.Run()
	if err != nil {
		return engine.StateDown, nil
	}
	return engine.StateUp, nil
}
