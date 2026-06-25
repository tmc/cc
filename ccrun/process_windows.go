//go:build windows

package ccrun

import "os/exec"

func setupCommand(cmd *exec.Cmd) {}

func cleanupCommand(cmd *exec.Cmd) {}
