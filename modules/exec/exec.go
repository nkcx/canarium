package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
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

	cmd := newCommand(ctx, expandVars(cmdStr, client), client)
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

// waitDelay bounds how long Wait blocks after cancellation for output pipes
// still held open by a descendant process.
const waitDelay = 2 * time.Second

// newCommand builds a shell command bounded by ctx.
//
// The command runs through `sh -c`, so it accepts pipelines and redirection
// as an operator would expect. That means the command string is shell syntax
// by design: it comes from the configuration file, which is already trusted
// to specify what gets shut down and how.
func newCommand(ctx context.Context, command string, client *engine.Client) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)

	// Inherit the daemon's environment and add the client's details, so a
	// script can reference "$CANARIUM_CLIENT_ADDRESS" instead of having the
	// value spliced into its text.
	cmd.Env = append(os.Environ(), clientEnv(client)...)

	isolateProcess(cmd)

	// Even after the process group is killed, Wait can block on output pipes.
	// WaitDelay bounds that so a hung command cannot outlive its budget.
	cmd.WaitDelay = waitDelay

	return cmd
}

// expandVars substitutes client placeholders into a command string.
//
// Retained because existing configurations use it and it reads well for
// simple cases. Prefer the CANARIUM_* environment variables that newCommand
// exports: a value containing shell metacharacters is inert there, whereas
// substituting it into the command text is not.
//
// This is defence in depth rather than a privilege boundary. Every value
// here comes from the configuration file, which already specifies arbitrary
// shell commands to run — anyone who can write it can already do anything
// this daemon's user can. But an address copied from an inventory system or
// a hostname containing a stray quote should not silently become syntax.
func expandVars(s string, client *engine.Client) string {
	r := strings.NewReplacer(
		"{address}", client.Address,
		"{name}", client.Name,
		"{mac}", client.MAC,
	)
	return r.Replace(s)
}

// clientEnv exports the client's details for the command to reference.
//
// Using "$CANARIUM_CLIENT_ADDRESS" rather than "{address}" keeps the value
// out of the command text entirely, so nothing in it can be interpreted as
// shell syntax.
func clientEnv(client *engine.Client) []string {
	return []string{
		"CANARIUM_CLIENT_NAME=" + client.Name,
		"CANARIUM_CLIENT_ADDRESS=" + client.Address,
		"CANARIUM_CLIENT_MAC=" + client.MAC,
		"CANARIUM_CLIENT_TRANSPORT=" + client.Transport,
	}
}

// Probe runs the configured probe_command.
//
// Exit status 0 means up and 1 means down, following the convention of ping
// and similar tools. Any other exit status, or a failure to run the command
// at all, is reported as an error rather than as "down": a probe script that
// is missing, not executable, or crashing tells us nothing about the host,
// and treating that as a confirmed shutdown is how a typo in a config turns
// into a fleet that is never verified off.
func (t *Transport) Probe(ctx context.Context, client *engine.Client) (engine.ClientState, error) {
	cmdStr, ok := client.TransportConfig["probe_command"].(string)
	if !ok {
		return engine.StateUnknown, fmt.Errorf("exec transport: no probe_command configured")
	}

	cmd := newCommand(ctx, expandVars(cmdStr, client), client)
	err := cmd.Run()
	if err == nil {
		return engine.StateUp, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return engine.StateDown, nil
	}

	return engine.StateUnknown, fmt.Errorf("probe_command failed: %w", err)
}
