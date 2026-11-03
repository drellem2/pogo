package strandedwork

import (
	"errors"
	"strings"
	"testing"
)

// enth-vcs slice 9 (mg-2e9fc): an enth repo has no PR flow, so the open-PR
// probe's answer is replaced by "the item has an open MR in the refinery"
// (docs/design/enth-vcs.md §11). These pin CheckOpenMR to CheckOpenPR's
// contract: it can only ever remove an alert, and a failed probe removes none.

func resubmitFinding() Finding {
	return Finding{Branch: "enth:mg-9", Target: "refs/heads/main", Disposition: DispositionResubmit,
		Unmerged: []Commit{{SHA: "c1", Subject: "feat: thing (mg-9)"}}}
}

func TestCheckOpenMRMarksAwaitingReview(t *testing.T) {
	f := resubmitFinding()
	var asked string
	f.CheckOpenMR("mg-9", func(item string) (string, error) { asked = item; return "an open MR authored by mg-9", nil })
	if asked != "mg-9" {
		t.Errorf("probe asked about %q, want mg-9", asked)
	}
	if f.Disposition != DispositionAwaitingReview || f.OpenMR == "" || f.Stranded() {
		t.Fatalf("disposition=%q open_mr=%q stranded=%v, want awaiting_review", f.Disposition, f.OpenMR, f.Stranded())
	}
	if f.PR != 0 || f.PRBranch != "" {
		t.Errorf("PR=%d PRBranch=%q set by the MR check; an enth repo has no PR", f.PR, f.PRBranch)
	}
	if sum := f.Summary(); !strings.Contains(sum, "refinery already holds them") || strings.Contains(sum, "pull request") {
		t.Errorf("Summary does not name the MR (or still names a PR): %s", sum)
	}
}

func TestCheckOpenMRNoMRStaysStranded(t *testing.T) {
	f := resubmitFinding()
	f.CheckOpenMR("mg-9", func(string) (string, error) { return "", nil })
	if f.Disposition != DispositionResubmit || !f.Stranded() || f.PRProbeError != "" || f.OpenMR != "" {
		t.Errorf("no MR: %+v, want resubmit and nothing recorded", f)
	}
}

// The direction pin, as for CheckOpenPR: an unreadable refinery must alert.
func TestCheckOpenMRProbeFailureKeepsTheAlert(t *testing.T) {
	f := resubmitFinding()
	f.CheckOpenMR("mg-9", func(string) (string, error) { return "x", errors.New("refinery queue not readable") })
	if f.Disposition != DispositionResubmit || !f.Stranded() {
		t.Fatalf("a failed MR probe changed the verdict to %q — it must alert, never suppress", f.Disposition)
	}
	if f.PRProbeError != "refinery queue not readable" || f.OpenMR != "" {
		t.Errorf("PRProbeError=%q OpenMR=%q, want the error recorded and no MR", f.PRProbeError, f.OpenMR)
	}
}

func TestCheckOpenMRLeavesOtherDispositionsAndNoItemAlone(t *testing.T) {
	called := false
	probe := func(string) (string, error) { called = true; return "mr", nil }
	for _, d := range []Disposition{DispositionPreRegistration, DispositionCarried, DispositionAwaitingReview} {
		f := resubmitFinding()
		f.Disposition = d
		f.CheckOpenMR("mg-9", probe)
		if f.Disposition != d || f.OpenMR != "" {
			t.Errorf("%s reclassified to %q", d, f.Disposition)
		}
	}
	f := resubmitFinding()
	f.CheckOpenMR(" ", probe)
	if called || f.Disposition != DispositionResubmit {
		t.Errorf("probe called=%v disposition=%q: with no item there is nothing to ask about", called, f.Disposition)
	}
	// Positive control: the same probe does fire on a resubmit finding.
	f.CheckOpenMR("mg-9", probe)
	if !called || f.Disposition != DispositionAwaitingReview {
		t.Errorf("control: probe called=%v disposition=%q", called, f.Disposition)
	}
}
