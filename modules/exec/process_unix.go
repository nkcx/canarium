//go:build unix

package exec

import (
	"os/exec"
	"syscall"
)

// isolateProcess puts the command in its own process group and arranges for
// the whole group to be killed on cancellation.
//
// Without this, cancelling only kills the direct child — the `sh` wrapper —
// while its descendants keep running and keep the inherited stdout pipe
// open. CombinedOutput then blocks until those descendants exit of their own
// accord, so a command that hangs outlives its budget entirely and holds up
// the stage that dispatched it.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID signals the whole process group.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// Fall back to the direct child if the group is already gone.
			return cmd.Process.Kill()
		}
		return nil
	}
}
