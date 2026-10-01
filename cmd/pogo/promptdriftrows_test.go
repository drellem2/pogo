package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// rowsNamed returns the rows whose Name is name.
func rowsNamed(rows []doctorRow, name string) []doctorRow {
	var out []doctorRow
	for _, r := range rows {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

const bodyDiffersRow = "agent prompts up-to-date (body differs from stamp)"

// TestPromptDriftRows_BodyEditedUnderCurrentStampWarns pins the PR #228
// round-2 fix (drellem2/pogo#125): a prompt whose body was rewritten under a
// stamp for THIS binary's embed is a WARN row and contributes no fail row, so
// `doctor --check` is not turned red by it. Install skips such a file and
// writes no .dist; a fail here would leave --check red with no remedy short
// of discarding a deliberate edit. Round 1 shipped exactly that fail().
//
// It drives the real CheckPromptDrift against a sandboxed install rather than
// a hand-built PromptDrift, so it also breaks if the detector stops tagging
// the case EmbedCurrent.
func TestPromptDriftRows_BodyEditedUnderCurrentStampWarns(t *testing.T) {
	home := testsandbox.Isolate(t).Home

	if _, err := agent.InstallPrompts(agent.InstallOpts{}); err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}
	// Control: a fresh install has no drift, so doctor would print its pass row.
	if drift, err := agent.CheckPromptDrift(); err != nil {
		t.Fatalf("CheckPromptDrift: %v", err)
	} else if len(drift) != 0 {
		t.Fatalf("fresh install already drifted: %+v — the control is broken", drift)
	}

	mayorPath := filepath.Join(home, ".pogo", "agents", "mayor.md")
	data, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatal(err)
	}
	stampLine, _, ok := strings.Cut(string(data), "\n")
	if !ok || !strings.Contains(stampLine, "embed=sha256:") {
		t.Fatalf("installed mayor.md has no v1 stamp line: %q", stampLine)
	}
	if err := os.WriteFile(mayorPath, []byte(stampLine+"\n# A deliberate local mayor prompt\n"), 0644); err != nil {
		t.Fatal(err)
	}

	drift, err := agent.CheckPromptDrift()
	if err != nil {
		t.Fatalf("CheckPromptDrift: %v", err)
	}
	rows := promptDriftRows(drift)

	got := rowsNamed(rows, bodyDiffersRow)
	if len(got) != 1 {
		t.Fatalf("want exactly one %q row, got rows %+v", bodyDiffersRow, rows)
	}
	if got[0].Status != "warn" {
		t.Errorf("%q status = %q, want warn — a deliberate edit under the current stamp must not fail doctor --check (drellem2/pogo#125)", bodyDiffersRow, got[0].Status)
	}
	if !strings.Contains(got[0].Detail, "mayor.md") {
		t.Errorf("row detail does not name the edited file: %q", got[0].Detail)
	}
	for _, r := range rows {
		if r.Status == "fail" {
			t.Errorf("a body edited under the current stamp produced a fail row %q (%s) — doctor --check would exit non-zero", r.Name, r.Detail)
		}
	}
}

// TestPromptDriftRows_StatusPerShape pins the status of every drift shape side
// by side, so the warn above cannot be "fixed" by demoting the rows that SHOULD
// fail: install-fixable drift and an edit under an advanced embed both have a
// remedy, and both stay red.
func TestPromptDriftRows_StatusPerShape(t *testing.T) {
	rows := promptDriftRows([]agent.PromptDrift{
		{Path: "stale.md", Reason: "stale"},
		{Path: "reconcile.md", Reason: "edited", EmbedCurrent: false},
		{Path: "rewritten.md", Reason: "edited", EmbedCurrent: true},
	})
	want := map[string]string{
		"agent prompts up-to-date":               "fail",
		"agent prompts up-to-date (local edits)": "fail",
		bodyDiffersRow:                           "warn",
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for _, r := range rows {
		w, ok := want[r.Name]
		if !ok {
			t.Errorf("unexpected row %q", r.Name)
			continue
		}
		if r.Status != w {
			t.Errorf("%q status = %q, want %q", r.Name, r.Status, w)
		}
	}
}
