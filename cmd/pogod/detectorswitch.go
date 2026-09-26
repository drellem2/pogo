package main

import (
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/synthwatch"
)

// The on/off switches for three detectors pogod used to arm unconditionally —
// [synth_watch], [refusal_watch] and [turn_watch] (drellem2/pogo#185). Each
// defaults to on. A detector an operator switched off is the only signal for its
// failure class gone, so "off" is said LOUDLY at startup, in words that name the
// config key that did it: a detector that is silently off reads exactly like one
// that is running and finding nothing.

// synthWatchNotArmedLine is logged when [synth_watch] enabled = false. It must
// say that the respawn gate is still active, because that is the half of
// synthwatch the switch deliberately does NOT reach (see armSynthWatch).
const synthWatchNotArmedLine = "pogod: synthetic-failure-turn detector pager NOT armed (config: [synth_watch] enabled = false) — " +
	"nobody will be paged when an agent answers every nudge locally and fails it. " +
	"The RESPAWN GATE IS STILL ACTIVE: an agent whose transcript shows it failing every turn is still NOT restarted (mg-18d0)"

// refusalWatchNotArmedLine is logged when [refusal_watch] enabled = false.
const refusalWatchNotArmedLine = "pogod: consecutive-refusal alarm NOT armed (config: [refusal_watch] enabled = false) — " +
	"a run of consecutive failing turns will reach no sink. `pogo check-refusals` still answers on demand"

// turnWatchNotArmedLine is logged when [turn_watch] enabled = false.
const turnWatchNotArmedLine = "pogod: turn-watch NOT armed (config: [turn_watch] enabled = false) — " +
	"nothing is watching whether crew agents complete turns, and a coordinator outage has no pogod-resident reader"

// transcriptScannerSetter is the one registry method armSynthWatch needs; an
// interface so the wiring can be tested without a live registry.
type transcriptScannerSetter interface {
	SetTranscriptScanner(agent.TranscriptScanner)
}

// armSynthWatch installs synthwatch's transcript scanner as the registry's
// respawn gate and returns the watcher the heartbeat should Check — nil when
// [synth_watch] enabled = false.
//
// The scanner is installed UNCONDITIONALLY. enabled = false is page-only: it
// stops the periodic scan that pages `human`, and leaves ShouldRespawnAgent and
// `pogo agent diagnose` reading transcripts as before. With the watcher never
// Checked it holds no verdicts, so synthScanner falls through to a direct
// transcript read — the gate judges on evidence either way. Dropping it with the
// page would re-open mg-18d0's restart loop (~66 restarts over 23.5h).
func armSynthWatch(enabled bool, reg transcriptScannerSetter, w *synthwatch.Watcher) (pager *synthwatch.Watcher) {
	if reg != nil {
		reg.SetTranscriptScanner(synthScanner{w: w})
	}
	if !enabled {
		return nil
	}
	return w
}
