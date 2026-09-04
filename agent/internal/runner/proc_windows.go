//go:build windows

package runner

import (
	"os/exec"
)

// setProcessGroup is a no-op on Windows (Job objects would be needed for trees).
func setProcessGroup(cmd *exec.Cmd) error { return nil }

// killProcess kills the process on Windows.
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
