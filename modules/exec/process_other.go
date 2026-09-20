//go:build !unix

package exec

import "os/exec"

// isolateProcess is a no-op on platforms without process groups; the default
// cancellation behaviour applies.
func isolateProcess(cmd *exec.Cmd) {}
