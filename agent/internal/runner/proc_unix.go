//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child into its own process group on unix,
// so Stop can kill the whole tree.
func setProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// killProcess kills the process group on unix.
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// negative pid targets the whole group
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
