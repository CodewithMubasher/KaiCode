//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// setupProcess hides the console window (the child gets its own hidden
// console, so changing its code page cannot disturb Kai's terminal) and makes
// a timeout or cancel kill the whole process tree.
func setupProcess(cmd *exec.Cmd, k shellKind, command string) {
	attr := &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if k.flavor == flavorCmd {
		attr.CmdLine = `"` + k.path + `" /D /S /C "` + command + `"`
	}
	cmd.SysProcAttr = attr
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		_ = kill.Run()
		return cmd.Process.Kill()
	}
}
