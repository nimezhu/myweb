//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach lets cmd outlive this process and its terminal.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
