package main

import (
	"os"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/providers"
	"github.com/drellem2/pogo/internal/synthfail"
	"github.com/drellem2/pogo/internal/synthwatch"
)

// This file wires the synthetic-failure-turn detector (mg-8cdb) into pogod: the
// target enumeration the watcher scans, and the agent.TranscriptScanner adapter
// that lets `pogo agent diagnose` and the respawn gate consult it.
//
// The detector reads harness session transcripts to tell a WEDGED agent (writes
// nothing) from one answering every nudge locally and failing it (writes a new
// zero-token error turn per nudge) — the class mg-18d0 identified after the
// 2026-07-22 fleet outage. See internal/synthfail.

// homeDir returns the user's home directory, or "" when it cannot be resolved.
// "" makes every transcript scan report StateUnavailable, which degrades to
// pogod's pre-detector behaviour rather than asserting health.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// synthTargets enumerates the running agents worth scanning. Exited agents are
// skipped: this class is about a LIVE agent burning its nudges, and an exited
// agent's last transcript state is the respawn gate's business, not the pager's.
func synthTargets(reg *agent.Registry) []synthwatch.Target {
	if reg == nil {
		return nil
	}
	var out []synthwatch.Target
	for _, a := range reg.List() {
		if a == nil || a.Status != agent.StatusRunning {
			continue
		}
		out = append(out, synthwatch.Target{
			Name:       a.Name,
			Identity:   a.EventAgent(),
			Workdir:    a.Dir,
			WorkItemID: a.WorkItemID,
		})
	}
	return out
}

// synthScanner adapts the watcher to agent.TranscriptScanner.
//
// It prefers the watcher's last completed scan, because that is the reading the
// page was sent from — the respawn gate and the operator's `diagnose` must
// agree with the mail in the human's inbox rather than each re-deriving a
// verdict from a transcript that may since have been overwritten by the very
// restart under discussion. Only when the watcher holds no failing verdict does
// this read the transcript directly, so an agent that has never been scanned
// (spawned seconds ago, or exiting before the first scan interval) is still
// judged on evidence rather than on a default.
type synthScanner struct {
	w *synthwatch.Watcher
}

func (s synthScanner) ScanTranscript(name, workdir string) synthfail.Report {
	if s.w == nil {
		return synthfail.Report{}
	}
	if rep, ok := s.w.Report(name); ok {
		return rep
	}
	return synthfail.Scan(homeDir(), providers.SessionTranscriptGlobs(workdir), synthfail.Options{})
}
