// Package fsx has small file-system helpers shared by myweb's packages.
package fsx

import (
	"os"
	"runtime"
	"time"
)

var (
	osRename = os.Rename
	retry    = runtime.GOOS == "windows"
)

// Rename is os.Rename, retried for up to about a second on Windows, where
// antivirus and indexing services often hold a just-written file open for a
// moment ("Access is denied", "used by another process"). Elsewhere a
// failure is returned at once.
func Rename(oldpath, newpath string) error {
	err := osRename(oldpath, newpath)
	for i := 1; err != nil && retry && i <= 8; i++ {
		time.Sleep(time.Duration(i*25) * time.Millisecond)
		err = osRename(oldpath, newpath)
	}
	return err
}
