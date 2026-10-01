package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/staleness"
)

// orgTemplatedReport is drellem2/pogo#125's report, reduced: an org's prompts
// compared against drellem2/pogo's subtree, three differing and two files the
// reference does not ship.
func orgTemplatedReport(declared bool) staleness.PromptReport {
	return staleness.PromptReport{
		InstalledRoot: "/home/op/.pogo/agents",
		Reference: staleness.Reference{
			Repo: "/home/op/.pogo/deploy-src", Ref: "origin/main", Subtree: staleness.DefaultPromptsSubtree,
			Commit: "091cd6e4a4d6708ef11e471952ef28b8d6cd52aa", CommitTime: "2026-08-07T02:52:16+01:00",
			Declared: declared,
		},
		Shipped: 9,
		Deltas: []staleness.PromptDelta{
			{Path: "mayor.md", Kind: "differs", InstalledLines: 1226, ShippedLines: 1006},
			{Path: "templates/polecat-qa.md", Kind: "differs", InstalledLines: 300, ShippedLines: 249},
			{Path: "templates/polecat.md", Kind: "differs", InstalledLines: 541, ShippedLines: 298},
		},
		Unjudged: []string{"templates/payit-polecat.md", "templates/polecat-personal.md"},
		Remote:   staleness.RemoteState{Armed: false},
	}
}

// TestPromptWitnessNeverAssertsDirection: whatever the lineage, the report says
// the prompts DIFFER, names the reference including its subtree, and never
// says which side is newer. The "superseded" line is gone.
func TestPromptWitnessNeverAssertsDirection(t *testing.T) {
	for _, declared := range []bool{false, true} {
		out := captureStdout(t, func() { printPromptWitness(orgTemplatedReport(declared)) })
		for _, banned := range []string{"superseded", "STALE", "behind by", "LONGER by"} {
			if strings.Contains(out, banned) {
				t.Errorf("declared=%v: report asserts a direction (%q):\n%s", declared, banned, out)
			}
		}
		for _, want := range []string{"DIFFERS from the reference", "subtree:      " + staleness.DefaultPromptsSubtree, "220 more lines than ref"} {
			if !strings.Contains(out, want) {
				t.Errorf("declared=%v: report lacks %q:\n%s", declared, want, out)
			}
		}
	}
}

// TestPromptWitnessHedgesUndeclaredForeignCorpus: with no lineage and files the
// reference does not ship, the report says the reference may not be the
// upstream and prescribes NO install — `pogo agent prompt install` writes this
// binary's embed and would overwrite an org's current prompts with defaults.
// Declared, the hedge goes and the ordinary remedy returns (the control).
func TestPromptWitnessHedgesUndeclaredForeignCorpus(t *testing.T) {
	hedged := captureStdout(t, func() { printPromptWitness(orgTemplatedReport(false)) })
	if !strings.Contains(hedged, "the reference may not be this corpus's upstream") {
		t.Errorf("undeclared report is not hedged:\n%s", hedged)
	}
	if strings.Contains(hedged, "pogo agent prompt install") {
		t.Errorf("hedged report still prescribes an install:\n%s", hedged)
	}
	if !strings.Contains(hedged, "[lineage]") {
		t.Errorf("hedged report does not say how to declare the upstream:\n%s", hedged)
	}

	declared := captureStdout(t, func() { printPromptWitness(orgTemplatedReport(true)) })
	if strings.Contains(declared, "may not be this corpus's upstream") {
		t.Errorf("declared report is still hedged:\n%s", declared)
	}
	if !strings.Contains(declared, "Fix: redeploy from the reference") {
		t.Errorf("declared report lost its remedy:\n%s", declared)
	}
}
