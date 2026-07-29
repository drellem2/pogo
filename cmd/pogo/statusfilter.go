package main

// Assignee filtering for `pogo status` (mg-589d).
//
// The dashboard's work-item section is the rendered text of `mg list`. The
// filter is applied HERE, to that text, rather than by passing --assignee
// through to mg: filtering is a display concern, so the same bytes are
// fetched with or without the flag, and the matching rules below (case
// insensitivity, and an explicit spelling for "unassigned") are ours to state
// rather than mg's to happen to have.
//
// The assignee is recovered from the rendered line rather than from a second
// machine-readable fetch. `mg list` renders exactly three styled runs after
// the title — tags, assignee, snooze marker — and tags and snooze are both
// bracketed, so the assignee is the only unbracketed one. See macguffin's
// cmd/mg/listwidth.go:renderListLine.

import (
	"os"
	"os/user"
	"regexp"
	"strings"
)

// assigneeUnassigned is the spelling that selects work items carrying no
// assignee at all. The empty string cannot serve: an absent --assignee reads
// as "" too, so `--assignee=` would be indistinguishable from no flag at all.
// An item whose assignee is literally the word "none" is therefore not
// selectable — no such value is in use, and the unassigned majority is what
// anyone typing it means.
const assigneeUnassigned = "none"

// assigneeHuman is the canonical label for "the human at this machine". mg
// renders both the literal assignee `human` and an assignee equal to the
// current OS username as the same blue "human", so both canonicalize here to
// the same thing and `--assignee=human` finds either.
const assigneeHuman = "human"

// styledRun matches one ANSI-styled run as `mg list` emits it: an SGR
// introducer, content containing no further escapes, and the reset.
var styledRun = regexp.MustCompile("\x1b\\[[0-9;]*m([^\x1b]*)\x1b\\[0m")

// assigneeOfListLine returns the assignee rendered on one `mg list` line, or
// "" when the item has none. Tags render as "[a, b]" and the snooze marker as
// "[snoozed …]"; the assignee is the only styled run that is not bracketed.
func assigneeOfListLine(line string) string {
	for _, m := range styledRun.FindAllStringSubmatch(line, -1) {
		if strings.HasPrefix(m[1], "[") {
			continue
		}
		return m[1]
	}
	return ""
}

// currentOSUser returns the current username, or "" if it cannot be
// determined. It mirrors macguffin's own resolution so that "human" means the
// same person on both sides.
func currentOSUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// canonicalAssignee normalizes an assignee for comparison: trimmed, lowercased,
// and with the current OS username folded onto "human" so that an item
// assigned to `daniel` answers to `--assignee=human` (which is how mg renders
// it) as well as to `--assignee=daniel`.
func canonicalAssignee(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	if v == assigneeHuman {
		return assigneeHuman
	}
	// Checked before the username fold so that a machine whose OS user
	// happens to be called "none" does not lose the unassigned spelling.
	if v == assigneeUnassigned {
		return assigneeUnassigned
	}
	if u := currentOSUser(); u != "" && v == strings.ToLower(u) {
		return assigneeHuman
	}
	return v
}

// filterWorkItemsByAssignee keeps only the item lines of a `mg list` text block
// whose assignee matches, along with each status header ("available:",
// "claimed:", …) that still has at least one item under it. It returns the
// filtered block and the number of item lines kept.
//
// Item lines are indented by mg; headers and mg's own notices ("No work
// items.") are not, which is the whole distinction this needs. A header with
// nothing left under it is dropped, so an empty result is empty rather than a
// list of bare group names.
func filterWorkItemsByAssignee(block, assignee string) (string, int) {
	want := canonicalAssignee(assignee)
	wantUnassigned := want == assigneeUnassigned

	var kept []string
	var pendingHeader string
	matched := 0

	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			pendingHeader = line
			continue
		}
		got := canonicalAssignee(assigneeOfListLine(line))
		keep := got == want
		if wantUnassigned {
			keep = got == ""
		}
		if !keep {
			continue
		}
		if pendingHeader != "" {
			kept = append(kept, pendingHeader)
			pendingHeader = ""
		}
		kept = append(kept, line)
		matched++
	}

	return strings.Join(kept, "\n"), matched
}
