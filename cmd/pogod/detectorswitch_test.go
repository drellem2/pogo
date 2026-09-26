package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/synthfail"
	"github.com/drellem2/pogo/internal/synthwatch"
)

type recordingScannerSetter struct {
	got   agent.TranscriptScanner
	calls int
}

func (r *recordingScannerSetter) SetTranscriptScanner(s agent.TranscriptScanner) {
	r.got = s
	r.calls++
}

// [synth_watch] enabled = false is PAGE-ONLY (drellem2/pogo#185). The respawn
// gate must keep its transcript scanner, or mg-18d0's restart loop returns: a
// registry with no scanner respawns every restart_on_crash agent unconditionally.
func TestArmSynthWatch_DisabledKeepsTheRespawnGate(t *testing.T) {
	w := synthwatch.New(synthwatch.Options{})
	reg := &recordingScannerSetter{}

	pager := armSynthWatch(false, reg, w)

	if pager != nil {
		t.Error("enabled = false returned a pager; the heartbeat would still Check it and page")
	}
	if reg.calls != 1 || reg.got == nil {
		t.Fatalf("SetTranscriptScanner called %d time(s) with %v; the respawn gate was dropped with the page", reg.calls, reg.got)
	}
	if s, ok := reg.got.(synthScanner); !ok || s.w != w {
		t.Errorf("installed scanner = %#v, want synthScanner over the watcher", reg.got)
	}
}

func TestArmSynthWatch_EnabledArmsBoth(t *testing.T) {
	w := synthwatch.New(synthwatch.Options{})
	reg := &recordingScannerSetter{}

	if pager := armSynthWatch(true, reg, w); pager != w {
		t.Errorf("pager = %p, want the watcher %p", pager, w)
	}
	if reg.calls != 1 || reg.got == nil {
		t.Errorf("SetTranscriptScanner called %d time(s); want exactly one install", reg.calls)
	}
}

// The not-armed lines are the only signal that an operator switched a detector
// off, so each must name the config key that did it — and synthwatch's must say
// the respawn gate is still active, which is the one thing a reader would
// otherwise wrongly assume went with it.
func TestNotArmedLinesNameTheirSwitch(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{synthWatchNotArmedLine, []string{"NOT armed", "[synth_watch] enabled = false", "RESPAWN GATE IS STILL ACTIVE"}},
		{refusalWatchNotArmedLine, []string{"NOT armed", "[refusal_watch] enabled = false"}},
		{turnWatchNotArmedLine, []string{"NOT armed", "[turn_watch] enabled = false"}},
	} {
		for _, w := range tc.want {
			if !strings.Contains(tc.line, w) {
				t.Errorf("line %q lacks %q", tc.line, w)
			}
		}
	}
}

// The gating main.go runs for [refusal_watch] and [turn_watch]: off means the
// constructor never runs and the not-armed line is logged; on means it runs and
// nothing claims otherwise (mg-5dfb — the switches were first verified only in
// a sandbox daemon).
func TestArmDetector_Gating(t *testing.T) {
	type det struct{}
	for _, tc := range []struct {
		name string
		line string
	}{
		{"refusal_watch", refusalWatchNotArmedLine},
		{"turn_watch", turnWatchNotArmedLine},
	} {
		t.Run(tc.name+"/disabled", func(t *testing.T) {
			var logged []string
			built := 0
			got := armDetector(false, tc.line, func(s string) { logged = append(logged, s) }, func() *det { built++; return &det{} })
			if got != nil || built != 0 {
				t.Errorf("disabled detector was built (%d call(s), got %v)", built, got)
			}
			if len(logged) != 1 || logged[0] != tc.line {
				t.Errorf("logged %q, want exactly the not-armed line", logged)
			}
		})
		t.Run(tc.name+"/enabled", func(t *testing.T) {
			var logged []string
			want := &det{}
			got := armDetector(true, tc.line, func(s string) { logged = append(logged, s) }, func() *det { return want })
			if got != want {
				t.Errorf("enabled detector = %v, want the built watcher", got)
			}
			if len(logged) != 0 {
				t.Errorf("logged %q for an ENABLED detector; a not-armed line there is a false alarm", logged)
			}
		})
	}
	// build may decline (turnwatch with no registry) — nil passes through.
	if got := armDetector(true, "x", func(string) { t.Error("logged the switch line for a build that declined") }, func() *struct{} { return nil }); got != nil {
		t.Errorf("got %v, want nil from a declining build", got)
	}
}

// The event half of mg-5dfb: with [synth_watch] enabled = false the watcher is
// never Checked, so its scan cache is empty — and restart_suppressed must still
// be emitted from the respawn gate's own verdict, marked pager_armed=false, with
// a log line that does not claim a human was paged.
func TestSuppressedRespawn_EmitsWithThePagerOff(t *testing.T) {
	for _, armed := range []bool{false, true} {
		var evs []events.Event
		w := synthwatch.New(synthwatch.Options{Emit: func(e events.Event) { evs = append(evs, e) }})
		var line string
		a := &agent.Agent{Name: "pm-pogo", Type: agent.TypeCrew}
		rep := synthfail.Report{State: synthfail.StateFailing, Reason: synthfail.ReasonAuthFailed, Count: 4}

		suppressedRespawn(func(f string, args ...any) { line = fmt.Sprintf(f, args...) }, w, armed, a, rep)

		if len(evs) != 1 || evs[0].EventType != synthwatch.EventRestartSuppressed {
			t.Fatalf("armed=%v: emitted %v, want exactly one %s", armed, evs, synthwatch.EventRestartSuppressed)
		}
		if got, _ := evs[0].Details["pager_armed"].(bool); got != armed {
			t.Errorf("armed=%v: pager_armed = %v", armed, evs[0].Details["pager_armed"])
		}
		if !strings.Contains(line, "SUPPRESSING respawn") {
			t.Errorf("armed=%v: log line %q does not say the respawn was suppressed", armed, line)
		}
		if paged := strings.Contains(line, "A human has been paged"); paged != armed {
			t.Errorf("armed=%v: log line %q misstates whether a human was paged", armed, line)
		}
	}
}
