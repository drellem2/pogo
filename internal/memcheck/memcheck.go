// Package memcheck detects when an auto-memory MEMORY.md index is approaching
// the harness read cliff — the point past which the file stops loading at all.
//
// MEMORY.md fails TOTALLY, not gradually. The harness Read tool returns at most
// a fixed number of bytes for a single file; a MEMORY.md that grows past that
// cap is not truncated-but-useful, it stops being read ENTIRELY, and every
// memory it indexes vanishes at once — silently, with no error. Because our
// records are an unwitnessed population (nobody re-counts them), a whole index
// can disappear and go unnoticed until a human happens to look.
//
// This package converts that silent, size-triggered cliff into a standing
// signal: it reports when a MEMORY.md crosses a warn threshold BELOW the cap,
// and names the fattest index lines so the fix has a target. It DETECTS ONLY.
// It never rewrites MEMORY.md — compaction is a destructive rewrite of the
// shared durable record and stays a deliberate, human-verified judgment call
// (see mg-15c0). CheckFile opens the file read-only and returns data; it has no
// path that writes.
package memcheck

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HarnessReadCapBytes is the maximum size (in bytes) the harness Read tool will
// return for a single file. A MEMORY.md larger than this stops loading in full,
// taking every memory it indexes down with it.
//
// This is an EXTERNAL invariant: it is a property of the harness, not of pogo,
// and it lives outside this codebase. It is not discoverable at runtime (the
// harness does not expose it), so it is pinned here as a SINGLE named constant.
// Everything downstream derives from it — see WarnThresholdBytes. Do not
// sprinkle this number anywhere else, and do not read it as a measurement of
// "how big the file was the day this was written": it is a statement of where
// the cliff is.
//
// = harness Read cap (~24.4KB); update THIS line when the harness read cap
// changes, and the warn point moves with it.
const HarnessReadCapBytes = 25000

// WarnFraction is the fraction of the read cap at which memcheck warns. We warn
// well before the cliff (0.8 => at 80% of the cap) so there is headroom to
// compact deliberately rather than discovering the loss after the fact.
const WarnFraction = 0.8

// WarnThresholdBytes is the derived warn point in bytes. It TRACKS the cap by
// construction: change HarnessReadCapBytes and this moves with it. It is never
// a hardcoded byte count.
func WarnThresholdBytes() int {
	return int(float64(HarnessReadCapBytes) * WarnFraction)
}

// Line is one index line of a MEMORY.md, paired with its byte length.
type Line struct {
	Text  string
	Bytes int
}

// Result is the outcome of checking one MEMORY.md file.
type Result struct {
	Path           string
	SizeBytes      int
	ThresholdBytes int
	CapBytes       int
	// Approaching is true when the file is at or past the warn threshold — i.e.
	// approaching the read cliff. It is the signal the doctor turns into a warn.
	Approaching bool
	// FattestLines holds the longest index lines (longest first), populated only
	// when Approaching. These are the actionable target: hooks that grew into
	// paragraphs are what push the index toward the cliff.
	FattestLines []Line
}

// CheckFile reads path (read-only) and evaluates it against the warn threshold.
// It never modifies the file.
func CheckFile(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	return Check(path, data), nil
}

// Check evaluates already-read file contents. It is pure — no I/O — so a fixture
// can be checked without touching the filesystem. numFattest controls how many
// of the longest lines are reported on firing.
func Check(path string, data []byte) Result {
	r := Result{
		Path:           path,
		SizeBytes:      len(data),
		ThresholdBytes: WarnThresholdBytes(),
		CapBytes:       HarnessReadCapBytes,
	}
	if r.SizeBytes >= r.ThresholdBytes {
		r.Approaching = true
		r.FattestLines = fattestLines(data, 3)
	}
	return r
}

// fattestLines returns the n longest non-blank lines, longest first. Ties keep
// source order (stable sort) for deterministic output.
func fattestLines(data []byte, n int) []Line {
	var lines []Line
	for _, raw := range strings.Split(string(data), "\n") {
		t := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(t) == "" {
			continue
		}
		lines = append(lines, Line{Text: t, Bytes: len(t)})
	}
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Bytes > lines[j].Bytes
	})
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// Locate returns the auto-memory MEMORY.md index paths to check under home.
// It globs the two roots where these indexes live:
//   - ~/.claude/projects/<slug>/memory/MEMORY.md  (Claude Code auto-memory)
//   - ~/.pogo/agents/<type>/<name>/memory/MEMORY.md  (pogo agent memory)
//
// Missing roots simply contribute nothing; a glob error on one root does not
// stop the others. The result is sorted for deterministic output.
func Locate(home string) []string {
	patterns := []string{
		filepath.Join(home, ".claude", "projects", "*", "memory", "MEMORY.md"),
		filepath.Join(home, ".pogo", "agents", "*", "*", "memory", "MEMORY.md"),
	}
	var found []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			continue
		}
		found = append(found, matches...)
	}
	sort.Strings(found)
	return found
}
