package reviewdecl

import (
	"strings"
	"testing"
	"time"
)

// TestRenderAlwaysStatesThePopulation is the requirement mg-253e stated in its
// own words: "State the population. Say how many review carriers were scanned,
// not just how many were missing — a zero over an unstated denominator is not a
// pass."
//
// It is asserted over EVERY shape of report, including the empty one, because
// the clean path is exactly where a denominator is easiest to leave out and
// exactly where its absence does the damage.
func TestRenderAlwaysStatesThePopulation(t *testing.T) {
	cases := map[string]Report{
		"empty":         Detect(nil, ConventionLandedAt),
		"clean":         Detect([]Item{review("mg-0001", after, "mg-aaf6")}, ConventionLandedAt),
		"missing":       Detect([]Item{review("mg-0001", after, "")}, ConventionLandedAt),
		"selfref":       Detect([]Item{review("mg-0001", after, "mg-0001")}, ConventionLandedAt),
		"malformed":     Detect([]Item{review("mg-0001", after, "mg-aaf6 (build)")}, ConventionLandedAt),
		"undatable":     Detect([]Item{review("mg-0001", time.Time{}, "")}, ConventionLandedAt),
		"preconvention": Detect([]Item{review("mg-0001", before, "")}, ConventionLandedAt),
		"opaque":        Detect([]Item{{ID: "mg-0001", CarrierUnreadable: true}}, ConventionLandedAt),
	}
	for name, rep := range cases {
		t.Run(name, func(t *testing.T) {
			out := rep.Render()
			if !strings.Contains(out, "review ticket(s) out of") {
				t.Errorf("report states no denominator:\n%s", out)
			}
			if !strings.Contains(out, "work item(s) read") {
				t.Errorf("report does not say how many items it read:\n%s", out)
			}
		})
	}
}

// TestRenderAlwaysStatesTheBoundary. mayor's dispatch note: "STATE the boundary
// in the report, because a detector that silently excludes part of its
// population is the failure mode in the 'state the population' note above."
//
// Asserted on every shape too — the exclusion is applied on every run, so it
// must be visible on every run, not only when something was excluded.
func TestRenderAlwaysStatesTheBoundary(t *testing.T) {
	for name, rep := range map[string]Report{
		"empty":         Detect(nil, ConventionLandedAt),
		"clean":         Detect([]Item{review("mg-0001", after, "mg-aaf6")}, ConventionLandedAt),
		"missing":       Detect([]Item{review("mg-0001", after, "")}, ConventionLandedAt),
		"preconvention": Detect([]Item{review("mg-0001", before, "")}, ConventionLandedAt),
	} {
		t.Run(name, func(t *testing.T) {
			out := rep.Render()
			if !strings.Contains(out, ConventionLandedAt.UTC().Format(time.RFC3339)) {
				t.Errorf("report does not state the convention boundary it applied:\n%s", out)
			}
			if !strings.Contains(out, "c045a9a") {
				t.Errorf("report does not name the commit the boundary comes from, so a reader "+
					"cannot check it:\n%s", out)
			}
		})
	}
}

// TestRenderCleanRunSaysSo, and says it WITH the denominator beside it. "no
// missing review declarations" on its own is the sentence a detector that
// scanned nothing also prints.
func TestRenderCleanRunSaysSo(t *testing.T) {
	out := Detect([]Item{review("mg-0001", after, "mg-aaf6")}, ConventionLandedAt).Render()
	if !strings.Contains(out, "no missing review declarations") {
		t.Errorf("a clean run does not say it is clean:\n%s", out)
	}
	if !strings.Contains(out, "scanned 1 review ticket(s) out of 1 work item(s) read") {
		t.Errorf("clean run does not carry its denominator:\n%s", out)
	}
}

// TestRenderZeroScannedIsQualified. A scan that evaluated no review tickets at
// all renders "0 missing", which is indistinguishable from a pass — and over a
// store with review work in flight it is what a broken scan looks like. The
// report has to say which it might be rather than leave the zero to be read as
// coverage.
func TestRenderZeroScannedIsQualified(t *testing.T) {
	out := Detect([]Item{{ID: "mg-0001", Stage: "build"}}, ConventionLandedAt).Render()
	if !strings.Contains(out, "evaluated ZERO review tickets") {
		t.Errorf("a zero-denominator run reads as a pass:\n%s", out)
	}
}

// TestRenderStatesItsCoverage. Archived and shelved items are outside the scan
// (D-1). A report that did not name the statuses it walked would imply it saw
// the store — 31 of the 34 `stage: review` carriers on this box are in archive/.
func TestRenderStatesItsCoverage(t *testing.T) {
	rep := Detect(nil, ConventionLandedAt)
	rep.Statuses = []string{"available", "claimed", "done", "pending"}
	out := rep.Render()
	for _, s := range rep.Statuses {
		if !strings.Contains(out, s) {
			t.Errorf("report does not name the %q directory it covered:\n%s", s, out)
		}
	}
}

// TestRenderNeverClaimsToHaveRepaired. mg-253e: "Report-only. Do not auto-write
// the line. A coordinator that skipped it may have had a reason, and a detector
// that repairs the thing it measures cannot be trusted to measure it." The
// finding text has to say so, because the reader's next question after "this
// ticket is missing a line" is "did you add it?".
func TestRenderNeverClaimsToHaveRepaired(t *testing.T) {
	out := Detect([]Item{review("mg-0001", after, "")}, ConventionLandedAt).Render()
	if !strings.Contains(out, "REPORT-ONLY") {
		t.Errorf("a finding does not tell its reader nothing was written:\n%s", out)
	}
}

// TestMailSubjectCarriesTheDenominator. A subject line is the only part of a
// notice a filtered reader still sees, so the count in it must not travel
// without the population it came from — the lesson mg-dd22 paid for when
// "13 indeterminate" arrived twice over six buried findings.
func TestMailSubjectCarriesTheDenominator(t *testing.T) {
	rep := Detect([]Item{
		review("mg-0001", after, ""),
		review("mg-0002", after, "mg-aaf6"),
		review("mg-0003", after, "mg-0003"),
	}, ConventionLandedAt)
	subj := rep.MailSubject()
	if !strings.Contains(subj, "2 review ticket(s) leave their builder unprotected") {
		t.Errorf("subject = %q, want the unprotected count", subj)
	}
	if !strings.Contains(subj, "of 3 scanned") {
		t.Errorf("subject = %q, want the denominator", subj)
	}
}

// TestMailSubjectNamesAnUndatableRunSeparately. An undatable ticket is not a
// known-unprotected builder, and folding it into that count would overstate the
// one number a reader acts on.
func TestMailSubjectNamesAnUndatableRunSeparately(t *testing.T) {
	subj := Detect([]Item{review("mg-0001", time.Time{}, "")}, ConventionLandedAt).MailSubject()
	if strings.Contains(subj, "unprotected") {
		t.Errorf("subject = %q, want no unprotected claim for an unjudged ticket", subj)
	}
	if !strings.Contains(subj, "1 undatable") {
		t.Errorf("subject = %q, want the undatable count", subj)
	}
}

// TestRenderIsStable. The mail body and the CLI output are the same text, and
// the watcher only re-mails on a fingerprint change — so a render that varied
// between identical scans would make every notice look like news.
func TestRenderIsStable(t *testing.T) {
	items := []Item{review("mg-0002", after, ""), review("mg-0001", after, "")}
	if a, b := Detect(items, ConventionLandedAt).Render(), Detect(items, ConventionLandedAt).Render(); a != b {
		t.Errorf("Render is not stable across identical scans:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}
