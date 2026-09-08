package client

import (
	"errors"
	"strings"
	"testing"
)

// TestReopenMGWorkItemArchivedIsNotAFailure covers the third benign refusal
// (mg-4d21). It is the mechanism behind the only known NEGATIVE instance of a
// failed submit flipping a closed work item: mg-3ba8 was archived four minutes
// after its merge, and the docs follow-up that failed afterwards left it alone
// because `mg reopen` moves items out of done/ ONLY.
//
// The exact string is what the live binary printed on 2026-09-08, remediation
// line included — the still-claimed refusal carries no such line, so a matcher
// modelled on that one would never fire here.
func TestReopenMGWorkItemArchivedIsNotAFailure(t *testing.T) {
	fakeMGReopen(t, "Error: mg-3ba8: is archived, not done.\n  → Run 'mg unarchive mg-3ba8' to restore it.")

	err := ReopenMGWorkItem("mg-3ba8")
	if err == nil {
		t.Fatal("expected an error so the caller can still see the outcome")
	}
	if !errors.Is(err, ErrMGWorkItemArchived) {
		t.Errorf("the measured archived refusal is not classified benign: %v", err)
	}
	if errors.Is(err, ErrMGWorkItemNotDone) {
		t.Errorf("archived must not collapse into the still-claimed sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "is archived, not done") {
		t.Errorf("error dropped mg's output: %v", err)
	}
}

// TestReopenMGWorkItemArchivedNeighboursStayFailures is the same acceptance
// criterion the still-claimed sentinel carries: near misses must still read as
// failures, or the classification is a wildcard rather than a match.
func TestReopenMGWorkItemArchivedNeighboursStayFailures(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{"archived wording for a DIFFERENT id",
			"Error: mg-9999: is archived, not done.\n  → Run 'mg unarchive mg-9999' to restore it."},
		{"Error line only, remediation dropped", "Error: mg-3ba8: is archived, not done."},
		{"archived, but not the reopen refusal", "Error: mg-3ba8: already archived."},
		{"archived wording plus a second problem",
			"Error: mg-3ba8: is archived, not done.\n  → Run 'mg unarchive mg-3ba8' to restore it.\nError: partition unreadable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeMGReopen(t, tc.out)
			err := ReopenMGWorkItem("mg-3ba8")
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrMGWorkItemArchived) {
				t.Errorf("classified benign, so it would be demoted: %v", err)
			}
			if !strings.Contains(err.Error(), "mg reopen failed") {
				t.Errorf("failure lost its reporting: %v", err)
			}
		})
	}
}
