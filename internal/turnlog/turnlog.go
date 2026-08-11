// Package turnlog owns the fleet's turn-completion artifact: one append-only
// line per completed agent turn, at a path uniform across every agent.
//
// WHAT IT IS FOR, in one sentence borrowed from the ticket that created it
// (mg-a270, architect's wording, adopted verbatim as the acceptance criterion):
//
//	A liveness check must name an artifact THAT ONLY A COMPLETED TURN COULD
//	HAVE WRITTEN, with a timestamp after the bounce. If it would read green
//	over a fleet that is present and doing nothing, it is a presence check
//	wearing a liveness label — whatever it is named.
//
// On 2026-08-10/11 the fleet was inert for 22 hours while every signal at the
// pogod end read green: the processes existed, the schedules were registered,
// 140 nudges were delivered, the running revision was current. All of it was
// TRUE. All of it measured pogod's own actions. mg-8cdb's detector ran ~204
// checks over that window and emitted nothing — not because its threshold was
// wrong but because it was pointed at the wrong end of the system, and no
// threshold fixes that.
//
// The fleet was not entirely uninstrumented, and the accurate version matters
// because the overstated one invites the wrong fix. ackwatch's FLEET BLACKOUT
// arm fired 33 consecutive times across that window, each naming all five
// agents, each escalated to the human box, ~35 surfaced as macOS notifications
// (measured under mg-3cbb). Detection, routing and out-of-process delivery all
// worked; the outage still ran 22 hours and ended when a human restarted pogod
// by hand. So this package is a second witness on an existing floor — pointed
// at agent-side completion, which nothing was — rather than the first alarm.
//
// The outage was ultimately diagnosed from exactly two files: the two PM
// sweep.logs, whose last beats are three seconds apart. That is what
// established a simultaneous fleet-wide stop rather than two independent
// wedges. Three of the five running crew agents — mayor, pa, architect — wrote
// no artifact of that kind at all, and mayor is the agent every other detector
// routes through. Had the two instrumented agents been mayor and pa, the outage
// would still be undiagnosed. This package is the missing artifact.
//
// # THE TWO RULES THAT MAKE IT WORTH ANYTHING
//
// 1. THE AGENT WRITES IT. Not pogod, not launchd, not a wrapper script around
// the harness, not a nudge handler. Every one of those already existed during
// the outage and every one of them was green. An artifact written on the
// agent's behalf measures the writer, and the writer is never the thing in
// doubt. `pogo turn-done` is a command an agent runs inside its own turn; the
// moment anything else calls it the signal is worth exactly what
// nudge_delivered was worth. cmd/pogod is held to that mechanically — see
// TestPogodDoesNotWriteTheTurnLog.
//
// 2. NOTHING HERE TOUCHES THE DAEMON. Append is filesystem-only: no HTTP, no
// socket, no registry lookup. The single moment this artifact matters most is
// the moment pogod is unreachable or lying, so a writer that needed pogod would
// be absent from precisely the window it exists to describe.
//
// # PATH AND FORMAT
//
//	$POGO_HOME/agents/turnlog/<agent-name>.log
//
// Flat, keyed by agent name alone. No tier component: pm-pogo.log sits beside
// mayor.log and architect.log. That is deliberate — the PM tier is instrumented
// today only because the PM template happens to require a heartbeat for the
// coordinator's stall-watch, which is luck rather than design, and a path shape
// that encoded the tier would carry that accident forward into the convention.
// A consumer globs turnlog/*.log and needs no per-agent knowledge.
//
// Each line:
//
//	<RFC3339 UTC timestamp> <agent-name> <free-text note to end of line>
//
// Timestamp first, so lexical order is chronological and `tail -1` is the last
// completed turn. The note is optional. Agent names cannot contain spaces
// (agent.ValidateAgentName), so three fields split unambiguously.
package turnlog

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// TimeLayout is the timestamp format written and parsed. UTC, seconds
// resolution, trailing Z. Fixed-width so lexical sort is chronological — which
// is the property that lets a reader trust `tail -1` without parsing the whole
// file.
const TimeLayout = "2006-01-02T15:04:05Z"

// tailBytes bounds how much of a turnlog file Last reads. One line per turn at
// a ten-minute cadence is ~150 lines a day, so a few KB always contains the
// most recent entries; reading the tail keeps a year-old log from being pulled
// into memory by a detector that only wants the last line.
const tailBytes = 8192

// Dir returns the turnlog directory: $POGO_HOME/agents/turnlog.
//
// It mirrors agent.PromptDir()'s construction rather than calling it, because
// this package must not import internal/agent: internal/agent imports THIS
// package for the prompt clause every crew agent is rendered with.
func Dir() string {
	return filepath.Join(config.PogoHome(), "agents", "turnlog")
}

// Path returns the turnlog file for one agent under the live Dir().
func Path(agent string) string {
	return PathIn(Dir(), agent)
}

// PathIn returns the turnlog file for one agent under an explicit root.
//
// The explicit-root variants exist for the probe (probe.go), which builds a
// throwaway turnlog tree and runs the REAL scan over it. Pointing the probe at
// a directory rather than mutating POGO_HOME is what keeps the positive control
// from being a second implementation of the thing it is meant to vouch for.
func PathIn(root, agent string) string {
	return filepath.Join(root, agent+".log")
}

// Entry is one completed turn.
type Entry struct {
	Agent string    `json:"agent"`
	At    time.Time `json:"at"`
	Note  string    `json:"note,omitempty"`
	Raw   string    `json:"raw"`
}

// FormatLine renders one turnlog line. Exported so a caller that must write the
// artifact without this binary — a shell fallback in a prompt, a test fixture —
// has one definition of the format to agree with rather than a second copy of
// it.
func FormatLine(agent string, at time.Time, note string) string {
	// Newlines in a note would forge additional turns; a note is free text
	// from an agent, so it is flattened rather than trusted.
	note = strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ").Replace(note))
	line := Stamp(at) + " " + agent
	if note != "" {
		line += " " + note
	}
	return line + "\n"
}

// Stamp renders a timestamp in the turnlog's format: UTC, seconds resolution,
// trailing Z.
//
// It exists so callers outside this package never write `t.Format(TimeLayout)`
// themselves. A layout referenced through a constant is unresolvable to the
// zone-designator check in cmd/pogo (timelayoutzone_test.go), and the check is
// right to refuse it: digits with no zone in them are digits two surfaces can
// disagree about by the host's offset. Here the UTC conversion and the literal
// Z live in the same expression, once.
func Stamp(at time.Time) string {
	return at.UTC().Format(TimeLayout)
}

// ParseLine parses one turnlog line. A line whose first field is not a
// timestamp is rejected rather than skipped over silently: garbage in this file
// is a signal that something other than a turn is writing to it.
func ParseLine(line string) (Entry, error) {
	raw := strings.TrimRight(line, "\r\n")
	fields := strings.SplitN(strings.TrimSpace(raw), " ", 3)
	if len(fields) < 2 {
		return Entry{}, fmt.Errorf("turnlog: malformed line %q: want `<timestamp> <agent> [note]`", raw)
	}
	at, err := time.Parse(TimeLayout, fields[0])
	if err != nil {
		return Entry{}, fmt.Errorf("turnlog: unparseable timestamp %q in line %q", fields[0], raw)
	}
	e := Entry{Agent: fields[1], At: at.UTC(), Raw: raw}
	if len(fields) == 3 {
		e.Note = fields[2]
	}
	return e, nil
}

// Append records one completed turn for agent.
//
// Filesystem-only by design: see rule 2 in the package comment. The write is a
// single O_APPEND write of one short line, so concurrent appends from an agent
// and, say, a mail-check turn cannot interleave into a corrupt line.
//
// An empty agent name is an error rather than a default. The one caller that
// could plausibly default it is `pogo turn-done` invoked outside an agent
// session, and a turnlog line attributed to the wrong name is worse than no
// line: it is the shape of evidence that a turn happened somewhere it did not.
func Append(agent, note string, at time.Time) error {
	return AppendIn(Dir(), agent, note, at)
}

// AppendIn is Append against an explicit root. See PathIn.
func AppendIn(root, agent, note string, at time.Time) error {
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("turnlog: no agent name")
	}
	if strings.ContainsAny(agent, " /\\\n\t") {
		return fmt.Errorf("turnlog: agent name %q contains a separator", agent)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return fmt.Errorf("turnlog: create %s: %w", root, err)
	}
	path := PathIn(root, agent)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("turnlog: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(FormatLine(agent, at, note)); err != nil {
		return fmt.Errorf("turnlog: write %s: %w", path, err)
	}
	return nil
}

// Last returns the most recent completed turn recorded for agent.
//
// A missing file returns an error satisfying errors.Is(err, os.ErrNotExist),
// and callers must treat that as RED — an agent with no turnlog is exactly the
// mayor/pa/architect state this package was built for, not a clean reading.
// An existing but empty file is the same state and returns the same error.
func Last(agent string) (Entry, error) {
	return LastIn(Dir(), agent)
}

// LastIn is Last against an explicit root. See PathIn.
func LastIn(root, agent string) (Entry, error) {
	path := PathIn(root, agent)
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Entry{}, err
	}
	off := int64(0)
	if info.Size() > tailBytes {
		off = info.Size() - tailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return Entry{}, err
	}

	var lastGood Entry
	var found bool
	var lastErr error
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		// A tail read can start mid-line; drop that fragment rather than
		// reporting it as corruption.
		if first && off > 0 {
			first = false
			continue
		}
		first = false
		if strings.TrimSpace(line) == "" {
			continue
		}
		e, err := ParseLine(line)
		if err != nil {
			lastErr = err
			continue
		}
		lastGood, found = e, true
	}
	if err := sc.Err(); err != nil {
		return Entry{}, fmt.Errorf("turnlog: read %s: %w", path, err)
	}
	if !found {
		if lastErr != nil {
			return Entry{}, lastErr
		}
		return Entry{}, fmt.Errorf("turnlog: %s has no entries: %w", path, os.ErrNotExist)
	}
	return lastGood, nil
}
