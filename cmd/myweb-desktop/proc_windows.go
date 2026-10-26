package main

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps cmd from opening a console window.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// newSession: on Windows a process outlives the one that started it anyway.
func newSession(*exec.Cmd) {}
