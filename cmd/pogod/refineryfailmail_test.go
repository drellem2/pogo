package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/refinery"
)

// The mail reaches a coordinator who is not at a terminal, so it carries the
// same evidence `pogo refinery show` does (mg-e5c2).

func TestSubjectClassIsVisibleWithoutOpeningTheMail(t *testing.T) {
	infra := &refinery.MergeRequest{Status: refinery.StatusFailed, FailureClass: refinery.ClassInfrastructure}
	defect := &refinery.MergeRequest{Status: refinery.StatusFailed, FailureClass: refinery.ClassDefect}
	if refineryFailureClassLabel(infra) == refineryFailureClassLabel(defect) {
		t.Fatal("both classes give the same subject label — thirty-one identical MERGE FAILED subjects is what invited thirty-one dispatches on 2026-08-05")
	}
	if got := refineryFailureClassLabel(&refinery.MergeRequest{Status: refinery.StatusFailed}); got != string(refinery.ClassUnclassified) {
		t.Errorf("an unclassified failure labels as %q, want a stable subject shape", got)
	}
}

func TestAttemptSummaryDistinguishesOnceFromSeveral(t *testing.T) {
	once := refineryAttemptSummary(&refinery.MergeRequest{AttemptCount: 1})
	many := refineryAttemptSummary(&refinery.MergeRequest{AttemptCount: 5})
	if !strings.Contains(once, "failed ONCE and was not retried") {
		t.Errorf("single-attempt summary = %q", once)
	}
	if !strings.Contains(many, "failed after 5 attempts") {
		t.Errorf("multi-attempt summary = %q", many)
	}
	if once == many {
		t.Error("the mail cannot distinguish 'failed once' from 'failed after 5' — the exact gap mg-e5c2 names")
	}
}

func TestAttemptSummaryCarriesWhyThereWasNoRetry(t *testing.T) {
	got := refineryAttemptSummary(&refinery.MergeRequest{
		AttemptCount:     1,
		NotRetriedReason: "not retryable: the test gate ran on this tree and returned a verdict",
	})
	if !strings.Contains(got, "No further retry: not retryable: the test gate ran") {
		t.Errorf("summary drops the reason: %q", got)
	}
}

// TestMailCarriesBothTransportsVerbatim is the regression for the reading error
// that cost several hours: 20 ssh failures and 11 https failures in the same
// bursts, and only the https half named the cause.
func TestMailCarriesBothTransportsVerbatim(t *testing.T) {
	mr := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 2, FailureClass: refinery.ClassInfrastructure,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "ssh", Retried: true, BackoffSeconds: 2,
				Command: "git fetch origin", RawError: "ssh: connect to host github.com port 22: Undefined error: 0"},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "https",
				Command: "git fetch origin", NotRetriedReason: "not retryable: the network retry budget is spent",
				RawError: "fatal: unable to access 'https://github.com/drellem2/pogo/': Could not resolve host: github.com"},
		},
	}
	body := refineryAttemptDetail(mr)
	for _, want := range []string{
		"transport=ssh", "transport=https",
		"Undefined error: 0", "Could not resolve host: github.com",
		"git fetch origin",
		"retried: yes, after 2s of backoff",
		"retried: NO — not retryable: the network retry budget is spent",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "normalised") && !strings.Contains(body, "never a normalised") {
		t.Error("the body claims to be a normalised summary")
	}
}

func TestNoAttemptDetailWhenThereAreNoAttempts(t *testing.T) {
	if got := refineryAttemptDetail(&refinery.MergeRequest{}); got != "" {
		t.Errorf("empty attempt detail rendered %q", got)
	}
}

// TestTriageNoteTellsACoordinatorWhatNotToDo: the note is the actionable half
// of the class, and the infrastructure one has to say "do not dispatch".
func TestTriageNoteTellsACoordinatorWhatNotToDo(t *testing.T) {
	if !strings.Contains(refinery.ClassInfrastructure.TriageNote(), "do NOT dispatch") {
		t.Errorf("infrastructure note: %q", refinery.ClassInfrastructure.TriageNote())
	}
	if !strings.Contains(refinery.ClassDefect.TriageNote(), "fix is warranted") {
		t.Errorf("defect note: %q", refinery.ClassDefect.TriageNote())
	}
}

// TestMailSaysHowLongItActuallyWaited is the mg-c3b7 requirement, and the
// distinction it exists to make: a reader must be able to tell "the network was
// down for longer than anyone could wait" from "we did not really wait".
//
// The 2026-08-10 mail could not. It reported the attempt count and the budget's
// own wording, and the fact that the whole retry campaign had lasted 52 seconds
// — against an outage since measured at 15m26s — was only in the refinery log.
func TestMailSaysHowLongItActuallyWaited(t *testing.T) {
	// The incident's own shape: 5 network attempts, 52s of backoff, spent.
	spent := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 5, FailureClass: refinery.ClassInfrastructure,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 2},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 5},
			{Attempt: 3, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 15},
			{Attempt: 4, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 30},
			{Attempt: 5, Stage: "fetch", Class: refinery.ClassInfrastructure, NotRetriedReason: "not retryable: the network retry budget is spent"},
		},
	}
	got := refineryAttemptSummary(spent)
	if !strings.Contains(got, "Waited: 52s") {
		t.Errorf("the mail does not state the time actually slept, so a 52-second wait against a 15-minute outage reads the same as a patient one:\n%s", got)
	}
	if !strings.Contains(got, "4 retried attempt(s)") {
		t.Errorf("the mail does not say how many attempts the wait was spread over:\n%s", got)
	}

	// A failure that was never retried must not borrow the wording of one that
	// waited. Reporting "0s" as though it were a measured patience is the same
	// ambiguity in the other direction.
	none := refineryAttemptSummary(&refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 1,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "test", Class: refinery.ClassDefect, NotRetriedReason: "not retryable: the test gate ran on this tree and returned a verdict"},
		},
	})
	if !strings.Contains(none, "nothing here was retried") {
		t.Errorf("an unretried failure does not say so:\n%s", none)
	}
	if got == none {
		t.Error("a 52-second wait and no wait at all produce the same sentence")
	}
}
