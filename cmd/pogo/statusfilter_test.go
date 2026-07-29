package main

// Unit tests for the `pogo status --assignee` display filter (mg-589d).
//
// The fixture lines are verbatim `mg list` output shapes, ANSI and all: an
// item with tags and a blue "human" assignee, an item with tags and a dim
// assignee, an item with an assignee but no tags, an item with tags but no
// assignee, and an item carrying a snooze marker after the assignee. Parsing
// the rendered line is the mechanism, so the fixtures have to be the rendered
// line rather than a tidied version of it.

import (
	"strings"
	"testing"
)

const (
	lineHuman    = "  mg-2a50    task     RED LINE BREACHED \x1b[2m[pogo, red-line]\x1b[0m \x1b[34mhuman\x1b[0m"
	lineParked   = "  mg-0ffc    task     FOLLOW-UP from mg-4938 \x1b[2m[pogo, ops]\x1b[0m \x1b[2mparked\x1b[0m"
	lineNoTags   = "  mg-5551    task     48 test suites still read live state \x1b[2mparked\x1b[0m"
	lineNoAssign = "  mg-09ea    task     INDEPENDENT AUDIT of mg-2c34 \x1b[2m[onethird, audit]\x1b[0m"
	linePlain    = "  mg-1111    task     no tags and no assignee at all"
	lineSnoozed  = "  mg-765a    task     mg schedule reports a snoozed item \x1b[2m[macguffin]\x1b[0m \x1b[34mhuman\x1b[0m \x1b[2m[snoozed 2026-08-01T00:00:00Z]\x1b[0m"
	lineArchitec = "  mg-12be    task     Bedtime notification swarm \x1b[2m[pogo, dx]\x1b[0m \x1b[2marchitect\x1b[0m"
)

// The whole filter rests on this: the assignee is the only styled run on the
// line that is not bracketed. If mg ever renders it bracketed, or renders a
// fourth styled run, this is the test that says so.
func TestAssigneeOfListLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"tags then human", lineHuman, "human"},
		{"tags then dim assignee", lineParked, "parked"},
		{"assignee with no tags", lineNoTags, "parked"},
		{"tags but no assignee", lineNoAssign, ""},
		{"neither tags nor assignee", linePlain, ""},
		{"snooze marker after assignee", lineSnoozed, "human"},
		{"group header is not an item", "available:", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := assigneeOfListLine(tc.line); got != tc.want {
				t.Errorf("assigneeOfListLine(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// Matching is exact and case-insensitive, never substring: "park" must not
// find "parked", and "arch" must not find "architect".
func TestFilterWorkItemsByAssignee_ExactCaseInsensitive(t *testing.T) {
	block := strings.Join([]string{
		"available:",
		lineHuman,
		lineParked,
		lineNoTags,
		"claimed:",
		lineNoAssign,
		lineArchitec,
	}, "\n")

	cases := []struct {
		filter    string
		wantIDs   []string
		wantCount int
	}{
		{"human", []string{"mg-2a50"}, 1},
		{"HUMAN", []string{"mg-2a50"}, 1},
		{"  Human  ", []string{"mg-2a50"}, 1},
		{"parked", []string{"mg-0ffc", "mg-5551"}, 2},
		{"architect", []string{"mg-12be"}, 1},
		{"park", nil, 0},
		{"arch", nil, 0},
		{"uman", nil, 0},
		{"nobody-at-all", nil, 0},
		{"none", []string{"mg-09ea"}, 1},
		{"NONE", []string{"mg-09ea"}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.filter, func(t *testing.T) {
			got, count := filterWorkItemsByAssignee(block, tc.filter)
			if count != tc.wantCount {
				t.Errorf("filter %q matched %d items, want %d (output %q)", tc.filter, count, tc.wantCount, got)
			}
			for _, id := range tc.wantIDs {
				if !strings.Contains(got, id) {
					t.Errorf("filter %q dropped %s, which should have matched:\n%q", tc.filter, id, got)
				}
			}
			// Every id NOT expected must be absent — a filter that keeps
			// everything would pass a contains-only assertion.
			for _, id := range []string{"mg-2a50", "mg-0ffc", "mg-5551", "mg-09ea", "mg-12be"} {
				expected := false
				for _, want := range tc.wantIDs {
					if want == id {
						expected = true
					}
				}
				if !expected && strings.Contains(got, id) {
					t.Errorf("filter %q kept %s, which should not have matched:\n%q", tc.filter, id, got)
				}
			}
		})
	}
}

// A status header survives only when something under it does. Otherwise an
// empty result reads as a list of group names with nothing in them.
func TestFilterWorkItemsByAssignee_DropsEmptyHeaders(t *testing.T) {
	block := strings.Join([]string{
		"available:",
		lineParked,
		"claimed:",
		lineHuman,
		"done:",
		lineNoAssign,
	}, "\n")

	got, count := filterWorkItemsByAssignee(block, "human")
	if count != 1 {
		t.Fatalf("matched %d, want 1 (output %q)", count, got)
	}
	if strings.Contains(got, "available:") || strings.Contains(got, "done:") {
		t.Errorf("emptied group headers survived the filter:\n%q", got)
	}
	if !strings.Contains(got, "claimed:") {
		t.Errorf("the header of the surviving item was dropped:\n%q", got)
	}
}

// A filter matching nothing yields an empty block and a zero count — not the
// unfiltered block, which is the failure mode the caller's "0 matching" line
// exists to make visible.
func TestFilterWorkItemsByAssignee_NoMatchIsEmpty(t *testing.T) {
	block := "available:\n" + lineHuman + "\n" + lineParked
	got, count := filterWorkItemsByAssignee(block, "nobody")
	if count != 0 || got != "" {
		t.Errorf("filter with no matches = (%q, %d), want (\"\", 0)", got, count)
	}
}

// mg's own "No work items." notice is unindented, so it is treated as a
// header with nothing under it and disappears rather than being counted as a
// matching item.
func TestFilterWorkItemsByAssignee_EmptyAndNoticeInput(t *testing.T) {
	for _, block := range []string{"", "No work items.", "No claimed work items."} {
		got, count := filterWorkItemsByAssignee(block, "human")
		if count != 0 || got != "" {
			t.Errorf("filter over %q = (%q, %d), want (\"\", 0)", block, got, count)
		}
	}
}

// mg renders an item assigned to the current OS username as the same blue
// "human" it renders a literal `human` assignee as, so both spellings of the
// filter have to find it. Without this fold, `--assignee=$USER` would report a
// confident zero.
func TestCanonicalAssignee_FoldsCurrentUserOntoHuman(t *testing.T) {
	if got := canonicalAssignee("human"); got != assigneeHuman {
		t.Errorf("canonicalAssignee(\"human\") = %q, want %q", got, assigneeHuman)
	}
	u := currentOSUser()
	if u == "" {
		t.Skip("no resolvable OS username in this environment")
	}
	if got := canonicalAssignee(u); got != assigneeHuman {
		t.Errorf("canonicalAssignee(%q) = %q, want %q", u, got, assigneeHuman)
	}
	if got := canonicalAssignee(strings.ToUpper(u)); got != assigneeHuman {
		t.Errorf("canonicalAssignee(%q) = %q, want %q", strings.ToUpper(u), got, assigneeHuman)
	}
	if got := canonicalAssignee(""); got != "" {
		t.Errorf("canonicalAssignee(\"\") = %q, want \"\"", got)
	}
	if got := canonicalAssignee("parked"); got != "parked" {
		t.Errorf("canonicalAssignee(\"parked\") = %q, want \"parked\"", got)
	}
}
