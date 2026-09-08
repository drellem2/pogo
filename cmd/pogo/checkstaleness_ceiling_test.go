package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/staleness"
)

// TestPromptCeilingSourcesNamesThreeInstallers — the three rows answer three
// different questions and collapsing any two loses the finding mg-1e8e was
// filed over, so their presence and their order are asserted rather than left
// to whoever edits the slice next.
func TestPromptCeilingSourcesNamesThreeInstallers(t *testing.T) {
	got := promptCeilingSources("/repo", "aaaa", "bbbb", "/bin/pogod", "cccc")
	if len(got) != 3 {
		t.Fatalf("want 3 installers, got %d", len(got))
	}
	want := []struct{ name, rev string }{
		{"this pogo binary", "aaaa"},
		{"the running pogod", "bbbb"},
		{"the pogod on disk", "cccc"},
	}
	for i, w := range want {
		if got[i].Name != w.name {
			t.Errorf("row %d: name %q, want %q", i, got[i].Name, w.name)
		}
		if got[i].Revision != w.rev {
			t.Errorf("row %d: revision %q, want %q", i, got[i].Revision, w.rev)
		}
		if got[i].How == "" {
			t.Errorf("row %d (%s): no `how` — a ceiling a reader cannot act on", i, w.name)
		}
	}
	// The on-disk row must name the path it read, because "the next boot" is
	// only checkable if you know which file that boot execs.
	if !strings.Contains(got[2].How, "/bin/pogod") {
		t.Errorf("the on-disk row does not name the binary: %q", got[2].How)
	}
}

func TestPromptCeilingsSkipFlagYieldsNone(t *testing.T) {
	if got := promptCeilings("/repo", true); got != nil {
		t.Fatalf("--skip-ceilings must take no readings, got %d sources", len(got))
	}
}

// TestPrintCeilingsSeparatesTheThreeCells is the printer's own positive
// control. The live defect was a report whose remedy read the same over an
// installer that could help and one that could not, so the assertion is that
// the three rows READ differently — not merely that the block appears.
func TestPrintCeilingsSeparatesTheThreeCells(t *testing.T) {
	out := captureStdout(t, func() {
		printCeilings([]staleness.InstallerCeiling{
			{Name: "the pogod on disk", How: "next boot", Revision: "499eb8a3c4da1111",
				Closes: []string{"mayor.md"}},
			{Name: "the running pogod", How: "every boot", Revision: "7edd223c9a6b2222",
				Frozen: []string{"mayor.md"}},
			{Name: "this pogo binary", How: "prompt install", Revision: "112dedf0f4193333",
				Third: []string{"mayor.md"}},
			{Name: "a dead pogod", How: "never", Unknown: "pogod did not answer /version"},
		})
	})

	for _, want := range []string{
		"INSTALLER CEILING",
		"NOT a prediction",
		"the pogod on disk",
		"CAN close",
		"the running pogod",
		"no-op",
		"this pogo binary",
		"THIRD",
		"a dead pogod",
		"UNKNOWN",
		"pogod did not answer /version",
		// The revisions are the part a reader re-derives by hand.
		"499eb8a3c4da",
		"7edd223c9a6b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ceiling block does not mention %q:\n%s", want, out)
		}
	}

	// An unknown row must not be dropped, and must not be printed as though it
	// had decided anything.
	if strings.Contains(out, "a dead pogod") && strings.Count(out, "UNKNOWN") != 1 {
		t.Errorf("want exactly one UNKNOWN row:\n%s", out)
	}
}

// TestPrintCeilingsIsSilentWithNothingToSay — a report with no ceilings must
// print no block at all, so a clean run does not grow a header over an empty
// list. The positive control is the test above: same function, non-empty input,
// which is how this negative is known to be about the input and not about a
// printer that never fires.
func TestPrintCeilingsIsSilentWithNothingToSay(t *testing.T) {
	if out := captureStdout(t, func() { printCeilings(nil) }); out != "" {
		t.Fatalf("want no output, got:\n%s", out)
	}
}

// TestCeilingRemedyReachesTheFixLine joins the derived remedy to the printer
// the way printPromptWitness does: an all-frozen fleet must not be told to
// reinstall, which is the exact advice the old fixed line gave on 2026-09-08.
func TestCeilingRemedyReachesTheFixLine(t *testing.T) {
	frozen := []staleness.InstallerCeiling{
		{Name: "the running pogod", How: "every boot", Frozen: []string{"mayor.md"}},
	}
	remedy := staleness.CeilingRemedy(frozen)
	if remedy == "" {
		t.Fatal("a read ceiling must produce a remedy")
	}
	if !strings.Contains(remedy, "NOT an install") {
		t.Errorf("remedy still prescribes an install over a frozen fleet: %q", remedy)
	}
	// POSITIVE CONTROL — the same function over a capable installer names it
	// instead, so the refusal above is about the reading and not a constant.
	capable := []staleness.InstallerCeiling{
		{Name: "the pogod on disk", How: "the NEXT pogod boot", Closes: []string{"mayor.md"}},
	}
	if got := staleness.CeilingRemedy(capable); !strings.Contains(got, "the NEXT pogod boot") {
		t.Errorf("a capable installer was not named: %q", got)
	}
}
