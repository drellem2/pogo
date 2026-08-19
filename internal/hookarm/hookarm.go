// Package hookarm answers one question about one agent: is the dead-recipient
// mail warning actually IN FORCE for it?
//
// mg-d924 shipped the warning as a Claude Code PostToolUse hook that pogod
// installs at spawn. That means the fix protects an agent only if the agent's
// LIVE PROCESS is running it — and the fleet running when the fix merged was
// protected by none of it. `pogo hook mail-recipient --self-check` could answer
// for the agent that ran it, which made the answer exist exactly once per
// person who remembered to ask. Nothing polled, so "which running agents are
// armed?" had no answer at all (mg-503d).
//
// # Two facts, deliberately not one
//
// REGISTERED is a fact about the agent's settings file: the hook is named under
// PostToolUse in <dir>/.claude/settings.local.json. It is necessary and it is
// not sufficient — a harness reads its hooks when the session starts, so a
// registration written after that is a promise the running process never heard,
// and a registration naming a binary that has since moved is a hook that runs
// and fails. Both look identical on disk to a working one.
//
// FIRED is a fact about the live process: the hook wrote a stamp file, from
// inside this agent's session, after this agent's process started. Nothing but
// the harness executing the hook can produce it.
//
// So armed means both. Registered-without-fired is reported as its own state
// rather than folded into either neighbour, because it is the state that says
// "the tree has the control and the process may not" — the shape this whole
// ticket is about, and the one mg-385f found by a different route.
package hookarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// SettingsRelPath is where Claude Code's per-project settings live, relative to
// an agent's working directory. Owned here rather than in internal/claude
// because pogod must read what the installer wrote, and internal/claude imports
// internal/agent — so the shared knowledge has to sit below both of them. One
// definition is the point: a second parser of this file would drift from the
// writer, which is the defect this package reports on, one level up.
var SettingsRelPath = filepath.Join(".claude", "settings.local.json")

// MailRecipientMarker identifies pogo's mail-recipient hook entry by the tail
// of its command, so the binary can move between spawns without the entry
// looking like somebody else's.
const MailRecipientMarker = "hook mail-recipient"

// StampName is the file the hook touches on every invocation. It sits beside
// the settings file, inside .claude/, which every worktree and this repo
// already keep out of git — a liveness stamp must not turn up in a diff.
const StampName = "pogo-mailhook.stamp"

// State is how far along the chain from "merged" to "protecting this agent"
// the warning has actually got.
type State string

const (
	// StateArmed: registered, and observed firing inside this process's
	// lifetime. The only value that means a send from this agent to a stopped
	// recipient will be warned about.
	StateArmed State = "armed"
	// StatePending: registered, but no firing observed since this process
	// started. Either the agent has made no Bash tool call yet, or the harness
	// never loaded the registration, or the command it names does not run.
	StatePending State = "pending"
	// StateOff: nothing registered. Sends from this agent behave exactly as
	// they did before mg-d924 — Delivered, silent, whether or not anyone is
	// reading.
	StateOff State = "off"
	// StateUnknown: the check itself could not run. Never collapse this into
	// armed; a report that cannot measure must say so.
	StateUnknown State = "unknown"
)

// Registered reports the mail-recipient hook command named under PostToolUse in
// dir's settings, and whether one is named at all.
func Registered(dir string) (string, bool, error) {
	if dir == "" {
		return "", false, errors.New("no directory to check")
	}
	settings, err := readSettings(filepath.Join(dir, SettingsRelPath))
	if err != nil {
		return "", false, err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["PostToolUse"].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			cmd, _ := entry["command"].(string)
			if hasSuffix(cmd, MailRecipientMarker) {
				return cmd, true, nil
			}
		}
	}
	return "", false, nil
}

// readSettings loads a settings.local.json, returning an empty object when
// there is none. A file that exists but does not parse is an error: it is a
// human's file, and "unreadable" must not read as "no hook" — that would report
// off, which is a definite claim, from a check that measured nothing.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// StampPath returns the liveness stamp for an agent working in dir.
func StampPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, filepath.Dir(SettingsRelPath), StampName)
}

// RecordFire writes the stamp for an agent working in dir. Called from the hook
// process on EVERY invocation — including the overwhelming majority that print
// nothing — because the thing being recorded is that the harness ran the hook,
// not that the hook had something to say.
//
// It is a truncating write of one timestamp rather than an append: only the
// most recent firing is ever read, and an unbounded file in an agent's worktree
// for a signal of size one would be a leak with no reader.
func RecordFire(dir string) error {
	path := StampPath(dir)
	if path == "" {
		return errors.New("no directory to stamp")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(time.Now().Format(time.RFC3339Nano)+"\n"), 0o644)
}

// LastFire returns when the hook last ran in dir, or the zero time if it has
// not (or the stamp cannot be read).
func LastFire(dir string) (time.Time, error) {
	path := StampPath(dir)
	if path == "" {
		return time.Time{}, errors.New("no directory to check")
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// Resolve answers "is the warning in force for the agent running in dir, whose
// process started at start?", with a one-line reason a reader can act on.
//
// start is what makes a stamp evidence rather than archaeology. A crew agent's
// working directory outlives its process, so a stamp left by yesterday's
// architect is still there when today's starts — and reading it as armed would
// be the report claiming a live control from a dead one's leftovers, which is
// precisely the mistake this package exists to prevent. A stamp no newer than
// the process is worth nothing and is treated as none.
func Resolve(dir string, start time.Time) (State, string) {
	if dir == "" {
		return StateUnknown, "pogod has no working directory recorded for this agent, so neither the registration nor the stamp can be found"
	}

	cmd, registered, err := Registered(dir)
	switch {
	case err != nil:
		return StateUnknown, fmt.Sprintf("could not read the hook registration: %v", err)
	case !registered:
		return StateOff, "no mail-recipient hook registered in " + filepath.Join(dir, SettingsRelPath) +
			" — mail from this agent to a stopped recipient reports Delivered with no warning, exactly as before mg-d924"
	}

	fired, err := LastFire(dir)
	if err != nil {
		return StateUnknown, fmt.Sprintf("hook registered (%s) but the stamp could not be read: %v", cmd, err)
	}
	if fired.IsZero() {
		return StatePending, "hook registered (" + cmd + ") but it has never been seen running here"
	}
	if !fired.After(start) {
		return StatePending, "hook registered (" + cmd + ") and the only stamp predates this process (" +
			fired.Format(time.RFC3339) + " vs start " + start.Format(time.RFC3339) +
			") — it belongs to an earlier agent in this directory, not to the one running now"
	}
	return StateArmed, "hook ran in this session at " + fired.Format(time.RFC3339)
}
