package main

import (
	"os"
	"strings"
	"testing"
)

// The wrapper `pogo install` writes runs `pogo hook harold-wake`. If either half
// of that wiring goes, harold's notify runs exit non-zero on every ping, and
// the only trace is a line in harold's log.
func TestHaroldWakeIsWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "cmdHook.AddCommand(cmdHookPromptSubmit, cmdHookMailRecipient, newHookHaroldWakeCmd())") {
		t.Error("harold-wake is not registered under `pogo hook`, which is the command the installed wrapper execs")
	}
	if !strings.Contains(s, "installHaroldWakeShim()") {
		t.Error("`pogo install` no longer writes ~/.pogo/bin/harold-wake, the path harold's config names")
	}
	if got := newHookHaroldWakeCmd().Name(); got != "harold-wake" {
		t.Errorf("command name %q, want harold-wake: the wrapper execs `pogo hook harold-wake`", got)
	}
}
