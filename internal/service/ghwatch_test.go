package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The job's defining property (mg-257a8): it runs `pogo gh-watch` through a
// LOGIN shell, so the watchers get the user's GitHub credential from
// ~/.zshenv on every fire instead of pogod holding one. A plist that exec'd
// pogo directly would parse, load, fire on schedule — and run every gh call
// with no credential.
func TestGHWatchPlistRunsThroughALoginShell(t *testing.T) {
	bin := t.TempDir()
	pogo := filepath.Join(bin, "pogo")
	if err := os.WriteFile(pogo, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	rendered, data, err := renderGHWatchPlist()
	if err != nil {
		t.Fatal(err)
	}
	args := "<string>/bin/zsh</string>\n        <string>-c</string>\n        <string>-l</string>"
	if !strings.Contains(rendered, args) {
		t.Errorf("ProgramArguments do not start `/bin/zsh -c -l`:\n%s", rendered)
	}
	if want := "exec '" + pogo + "' gh-watch --oneline"; data.Command != want {
		t.Errorf("command = %q, want %q", data.Command, want)
	}

	sched := parseLaunchSchedule([]byte(rendered))
	if !sched.Decoded || sched.Interval != 900 || !sched.RunAtLoad || len(sched.Calendar) != 0 {
		t.Errorf("schedule = %s (RunAtLoad=%t); want every 900s and at load", sched, sched.RunAtLoad)
	}
}

// With no pogo on PATH the plist cannot be rendered — an error, so the audit
// reports the job UNKNOWN rather than comparing against a plist with no program.
func TestGHWatchPlistRefusesWithoutPogo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, err := renderGHWatchPlist(); err == nil {
		t.Error("rendered a gh-watch plist with no pogo binary to run")
	}
}

func TestGHWatchIsAudited(t *testing.T) {
	for _, a := range managedLaunchAgents() {
		if a.Label == ghWatchLabel {
			if a.Remedy != "pogo service install-gh-watch" {
				t.Errorf("remedy = %q", a.Remedy)
			}
			return
		}
	}
	t.Error("com.pogo.ghwatch is not in managedLaunchAgents(); the nightly audit cannot see it (mg-79c6a)")
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}
