package main

import (
	"strings"
	"testing"
)

func TestTakeHome(t *testing.T) {
	for in, want := range map[string]string{
		"add alice -home /t -role admin": "add alice -role admin|/t",
		"list --home=/t":                 "list|/t",
		"list":                           "list|",
	} {
		rest, dir := takeHome(strings.Fields(in))
		if got := strings.Join(rest, " ") + "|" + dir; got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
