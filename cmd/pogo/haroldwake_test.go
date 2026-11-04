package main

import (
	"os"
	"path/filepath"
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

// The shim pins the roots this install runs under. They are made absolute
// because harold runs the shim from /, and an unset MG_ROOT stays unset so the
// shim clears harold's rather than inventing a store.
func TestHaroldWakeShimEnvIsTheInstallTimeRoots(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("POGO_HOME", home)
	t.Setenv("MG_ROOT", "")
	env, err := haroldWakeShimEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env.PogoHome != home || env.MGRoot != "" {
		t.Errorf("shim env %+v, want POGO_HOME %s and no MG_ROOT", env, home)
	}

	t.Setenv("POGO_HOME", "rel-pogo")
	t.Setenv("MG_ROOT", "rel-mg")
	env, err = haroldWakeShimEnv()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, "rel-pogo"); env.PogoHome != want {
		t.Errorf("POGO_HOME pinned as %q, want %q", env.PogoHome, want)
	}
	if want := filepath.Join(cwd, "rel-mg"); env.MGRoot != want {
		t.Errorf("MG_ROOT pinned as %q, want %q", env.MGRoot, want)
	}
}
