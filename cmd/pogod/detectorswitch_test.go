package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
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
