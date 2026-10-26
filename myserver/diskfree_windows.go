package myserver

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// DiskSpace returns the free (for this user) and total bytes of the disk
// that holds dir.
func DiskSpace(dir string) (free, total uint64, err error) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0, err
	}
	var all uint64
	r, _, e := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&all)))
	if r == 0 {
		return 0, 0, e
	}
	return free, total, nil
}
