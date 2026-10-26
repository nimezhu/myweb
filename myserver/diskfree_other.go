//go:build !linux && !darwin && !windows

package myserver

import "errors"

func DiskSpace(string) (uint64, uint64, error) { return 0, 0, errors.ErrUnsupported }
