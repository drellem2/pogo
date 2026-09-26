package main

import (
	"log"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/synthfail"
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

// logLine is armDetector's production logger.
func logLine(s string) { log.Print(s) }

// armDetector is the gate every switchable detector passes through in main:
// build runs only when enabled, and a disabled detector's not-armed line is
// logged instead. It exists so the gating main.go performs is the gating the
// tests exercise — the switches were first verified only in a sandbox daemon
// (mg-5dfb). build may itself return nil (turnwatch without a registry), and
// then says why on its own.
func armDetector[T any](enabled bool, notArmedLine string, logLine func(string), build func() *T) *T {
	if !enabled {
		logLine(notArmedLine)
		return nil
	}
	return build()
}

// suppressedRespawn is what pogod does when the respawn gate withholds a
// restart_on_crash respawn: log it, and emit the restart_suppressed event from
// the GATE'S verdict. It must not ask the watcher's scan cache instead — with
// [synth_watch] enabled = false the watcher is never Checked, holds no verdict,
// and the event silently vanished while the suppression still happened
// (mg-5dfb). The same was true, pager on, for an agent that exited before its
// first scan.
func suppressedRespawn(logf func(string, ...any), w *synthwatch.Watcher, pagerArmed bool, a *agent.Agent, rep synthfail.Report) {
	// "A human has been paged" is true only while the pager is armed; with
	// [synth_watch] enabled = false the gate still holds and nobody was told,
	// so the line must not claim otherwise.
	paged := "A human has been paged."
	if !pagerArmed {
		paged = "Nobody was paged: [synth_watch] enabled = false."
	}
	logf("agent %s (%s) exited while failing every turn (%s); SUPPRESSING respawn — "+
		"a restart cannot fix this and destroys the session's context (mg-18d0). %s",
		a.Name, a.Type, rep.Reason, paged)
	w.RestartSuppressed(a.Name, a.EventAgent(), rep, pagerArmed)
}
