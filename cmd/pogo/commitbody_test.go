package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/closingref"
)

// TestStripGitCommentsKeepsTheMessage — the wrap must survive stripping. If
// comment removal collapsed the message, the hook would stop seeing the one
// adjacency shape it exists to catch.
func TestStripGitCommentsKeepsTheMessage(t *testing.T) {
	raw := "feat: land the detector\n" +
		"# Please enter the commit message for your changes.\n" +
		"\n" +
		"The work completed but nobody closed\n" +
		"drellem2/pogo#89, and it sat OPEN.\n" +
		"# On branch fix-89\n" +
		"# Changes to be committed:\n"

	findings := closingref.Check(stripGitComments(raw))
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	if !findings[0].Wrapped || findings[0].Ref != "drellem2/pogo#89" {
		t.Errorf("stripping lost the wrap: %+v", findings[0])
	}
}

// TestStripGitCommentsDropsTheVerboseDiff — `git commit -v` appends the whole
// diff below a scissors line. Judging it would fail an author's commit over a
// string in code they are merely touching, which is the fastest possible route
// to --no-verify becoming muscle memory.
func TestStripGitCommentsDropsTheVerboseDiff(t *testing.T) {
	raw := "fix: unrelated change\n" +
		"\n" +
		"Refs drellem2/pogo#89\n" +
		"# ------------------------ >8 ------------------------\n" +
		"# Do not modify or remove the line above.\n" +
		"diff --git a/x.go b/x.go\n" +
		"+// this fixes #89 in the sample fixture\n"

	stripped := stripGitComments(raw)
	if strings.Contains(stripped, "diff --git") {
		t.Errorf("verbose diff survived stripping:\n%s", stripped)
	}
	if f := closingref.Check(stripped); len(f) != 0 {
		t.Errorf("false positive from the verbose diff: %+v", f)
	}
}

// TestStripGitCommentsLeavesCleanMessagesAlone.
func TestStripGitCommentsLeavesCleanMessagesAlone(t *testing.T) {
	msg := "feat: something\n\nA body.\n\nRefs drellem2/pogo#89\n"
	if got := stripGitComments(msg); got != msg {
		t.Errorf("stripGitComments mangled a comment-free message:\n%q", got)
	}
}
