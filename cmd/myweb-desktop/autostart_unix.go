//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
)

func autostartEnabled() bool {
	f, err := autostartFile(runtime.GOOS)
	if err != nil {
		return false
	}
	_, err = os.Stat(f)
	return err == nil
}

// setAutostart writes or removes the entry; written again, it follows the
// program to where it is now.
func setAutostart(on bool) error {
	f, err := autostartFile(runtime.GOOS)
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(autostartEntry(runtime.GOOS, exe)), 0o644)
}
