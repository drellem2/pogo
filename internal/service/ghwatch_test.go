package service

import (
	"fmt"
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
	if !sched.Decoded || !sched.RunAtLoad {
		t.Errorf("schedule = %s (RunAtLoad=%t); want decoded and at load", sched, sched.RunAtLoad)
	}
}

// The job fires on StartCalendarInterval at :00/:15/:30/:45, never on
// StartInterval (mg-d8160). On the reference box a StartInterval job ran once
// at load and never again (`pended nondemand spawn = interval`) while the
// calendar jobs beside it kept firing, which left gh-issue intake, teardown and
// carrier re-read dark. The fires are pinned as the audit decodes them, so what
// is asserted is what launchd schedules, not a substring of the template.
func TestGHWatchPlistFiresOnTheQuarterHourByCalendar(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pogo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	rendered, _, err := renderGHWatchPlist()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "<key>StartInterval</key>") {
		t.Error("plist uses StartInterval: on this box that class never fires after the load (mg-d8160)")
	}
	sched := parseLaunchSchedule([]byte(rendered))
	want := LaunchSchedule{Decoded: true, RunAtLoad: true}
	for _, m := range []int{0, 15, 30, 45} {
		want.Calendar = append(want.Calendar, CalendarFire{Minute: m, Hour: -1, Day: -1, Weekday: -1, Month: -1})
	}
	if !sched.Equal(want) {
		t.Errorf("schedule = %s (RunAtLoad=%t); want %s and at load", sched, sched.RunAtLoad, want)
	}
}

// A box that still has the StartInterval plist installed must read as schedule
// drift in the nightly audit, naming the reinstall — that is how the operator
// learns the fix has not reached launchd yet.
func TestAuditGHWatchSeesTheOldIntervalPlistAsScheduleDrift(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pogo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	rendered, _, err := renderGHWatchPlist()
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(rendered, "    <key>StartCalendarInterval</key>")
	j := strings.Index(rendered, "    <key>RunAtLoad</key>")
	if i < 0 || j < i {
		t.Fatal("could not locate the calendar block in the rendered plist")
	}
	old := rendered[:i] + "    <key>StartInterval</key>\n    <integer>900</integer>\n" + rendered[j:]

	path := filepath.Join(t.TempDir(), ghWatchLabel+".plist")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	res := auditLaunchAgent(ghWatchLabel, path, "pogo service install-gh-watch", rendered, nil)
	if res.Status != LaunchAgentStale || !res.ScheduleDrift {
		t.Errorf("status=%q drift=%t; want stale with schedule drift", res.Status, res.ScheduleDrift)
	}
	if !strings.Contains(res.Detail, "install-gh-watch") {
		t.Errorf("detail does not name the remedy: %s", res.Detail)
	}

	// Positive control: the rendered plist itself audits clean.
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := auditLaunchAgent(ghWatchLabel, path, "pogo service install-gh-watch", rendered, nil); res.Status != LaunchAgentOK {
		t.Errorf("rendered plist audits %q, want ok: %s", res.Status, res.Detail)
	}
}

func TestCalendarEveryMinutes(t *testing.T) {
	for n, want := range map[int]string{15: "0 15 30 45", 30: "0 30", 60: "0", 20: "0 20 40"} {
		got := strings.Trim(fmt.Sprint(calendarEveryMinutes(n)), "[]")
		if got != want {
			t.Errorf("calendarEveryMinutes(%d) = %s, want %s", n, got, want)
		}
	}
	for _, bad := range []int{0, -5, 7, 45, 90} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("calendarEveryMinutes(%d) did not panic: a gap across the hour would differ from the rest", bad)
				}
			}()
			calendarEveryMinutes(bad)
		}()
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
