//go:build !windows

package ccrun

import (
	"os"
	"os/exec"
	"syscall"
)

func setupCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func cleanupCommand(cmd *exec.Cmd) {
	if cmd.ProcessState != nil && !cmd.ProcessState.Exited() && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Kill)
	}
}
