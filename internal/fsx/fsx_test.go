package fsx

import (
	"errors"
	"testing"
)

func TestRenameRetries(t *testing.T) {
	defer func(r func(string, string) error, w bool) { osRename, retry = r, w }(osRename, retry)
	fails := 0
	osRename = func(string, string) error {
		if fails < 3 {
			fails++
			return errors.New("used by another process")
		}
		return nil
	}
	retry = true
	if err := Rename("a", "b"); err != nil || fails != 3 {
		t.Fatalf("windows: transient failures should be retried: %v after %d", err, fails)
	}
	fails = 0
	retry = false
	if err := Rename("a", "b"); err == nil || fails != 1 {
		t.Fatalf("elsewhere: fail at once: %v after %d", err, fails)
	}
	osRename = func(string, string) error { return errors.New("denied") }
	retry = true
	if err := Rename("a", "b"); err == nil {
		t.Fatal("a lasting failure must still be returned")
	}
}
