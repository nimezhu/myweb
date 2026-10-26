//go:build linux || darwin

package myserver

import "syscall"

// DiskSpace returns the free (for this user) and total bytes of the disk
// that holds dir.
func DiskSpace(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), nil
}
