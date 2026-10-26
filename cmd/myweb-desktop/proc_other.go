//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// newSession lets cmd outlive this process and its terminal.
func newSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
