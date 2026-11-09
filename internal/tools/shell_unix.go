//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setupProcess puts the command in its own process group so that a timeout or
// cancel kills the whole tree, not just the shell.
func setupProcess(cmd *exec.Cmd, _ shellKind, _ string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
