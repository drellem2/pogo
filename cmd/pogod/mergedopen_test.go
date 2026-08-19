package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
)

// captureMergedOpenAlerts swaps both process-global sinks for the duration of a
// test and restores TestMain's stubs afterwards, so one test's fixture cannot
// leave the next one talking to the live store.
func captureMergedOpenAlerts(t *testing.T, done func(string) (bool, error)) *[]mergedOpenAlert {
	t.Helper()
	prevMail, prevDone := mergedOpenAlertMail, mergedOpenItemDone
	var got []mergedOpenAlert
	mergedOpenAlertMail = func(a mergedOpenAlert) { got = append(got, a) }
	mergedOpenItemDone = done
	t.Cleanup(func() { mergedOpenAlertMail, mergedOpenItemDone = prevMail, prevDone })
	return &got
}

func landedMR() *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID:        "mr-d9ugdoitjv1ohvj2fd20",
		RepoPath:  "/Users/daniel/dev/pogo",
		Branch:    "polecat-ac0c",
		TargetRef: "main",
		Author:    "mg-ac0c",
		Status:    refinery.StatusMerged,
		MergedSHA: "abc123def4567890",
	}
}

// The mg-9d4e case: `mg done` was refused and the item is still open. This is the
// one that used to produce a single ambiguous log line and nothing else.
func TestReportMergedButOpenAlertsWhenTheItemDidNotClose(t *testing.T) {
	got := captureMergedOpenAlerts(t, func(string) (bool, error) { return false, nil })

	reportMergedButOpen(landedMR(), "ac0c", errors.New(
		"mg done failed: refusing: mg-ac0c declares a remainder and names no successor (exit status 4)"))

	if len(*got) != 1 {
		t.Fatalf("wanted one alert for a merged item that did not close, got %d", len(*got))
	}
	a := (*got)[0]
	if a.WorkItemID != "mg-ac0c" || a.Worker != "ac0c" || a.Branch != "polecat-ac0c" {
		t.Errorf("alert does not identify the merge: %+v", a)
	}
	if !strings.Contains(a.CloseError, "declares a remainder") {
		t.Errorf("the alert dropped what mg actually said, which is the only thing naming the "+
			"reason: %q", a.CloseError)
	}
	if a.StatusUnknown {
		t.Error("the status was read successfully; the alert should not hedge")
	}
}

// The benign half stays silent. A worker that won the race wrote its own verdict
// and the item is closed — an alert there would be noise on the healthy path,
// and a detector that fires on healthy input teaches its readers to skip it.
func TestReportMergedButOpenIsSilentWhenTheWorkerWonTheRace(t *testing.T) {
	got := captureMergedOpenAlerts(t, func(string) (bool, error) { return true, nil })

	reportMergedButOpen(landedMR(), "ac0c", errors.New("mg done failed: already done (exit status 4)"))

	if len(*got) != 0 {
		t.Errorf("an already-done item raised an alert: %+v", *got)
	}
}

// A FAILED PROBE ALERTS. "The item is closed" is the only reading that
// suppresses this, and an unreadable store is not that reading — treating it as
// one would silence the alert in exactly the window where mg is also what failed
// the close.
func TestReportMergedButOpenAlertsWhenTheStatusCannotBeRead(t *testing.T) {
	got := captureMergedOpenAlerts(t, func(string) (bool, error) {
		return false, errors.New("mg show mg-ac0c failed: exit status 3")
	})

	reportMergedButOpen(landedMR(), "ac0c", errors.New("mg done failed: exit status 4"))

	if len(*got) != 1 {
		t.Fatalf("an unreadable store suppressed the alert (%d raised)", len(*got))
	}
	if !(*got)[0].StatusUnknown {
		t.Error("the alert claims to know the item is open when the probe failed")
	}
	_, body := (*got)[0].Message()
	if !strings.Contains(body, "COULD NOT BE READ") {
		t.Errorf("the body does not tell the reader the status is unverified:\n%s", body)
	}
}

// A successful close reaches nothing, and neither does a merge with no author to
// close.
func TestReportMergedButOpenIgnoresWhatIsNotAFailedClose(t *testing.T) {
	got := captureMergedOpenAlerts(t, func(string) (bool, error) {
		t.Error("the store was probed for a merge that needs no alert")
		return false, nil
	})

	reportMergedButOpen(landedMR(), "ac0c", nil)
	authorless := landedMR()
	authorless.Author = ""
	reportMergedButOpen(authorless, "", errors.New("boom"))
	reportMergedButOpen(nil, "", errors.New("boom"))

	if len(*got) != 0 {
		t.Errorf("alerts raised for merges that did not need one: %+v", *got)
	}
}

// The subject carries the STATE and the remedy, not the error. A reader skimming
// "mg done failed" goes looking for a broken tool; what actually happened is that
// a work item is open with its work already merged.
func TestMergedOpenAlertMessageCarriesStateAndRemedy(t *testing.T) {
	a := mergedOpenAlert{
		WorkItemID: "mg-ac0c",
		Worker:     "ac0c",
		Repo:       "/Users/daniel/dev/pogo",
		Branch:     "polecat-ac0c",
		MR:         "mr-2",
		Target:     "main",
		MergedSHA:  "abc123def4567890",
		CloseError: "mg done failed: declares a remainder and names no successor",
	}
	subject, body := a.Message()

	for _, want := range []string{"mg-ac0c", "MERGED", "successor"} {
		if !strings.Contains(subject, want) {
			t.Errorf("subject %q does not carry %q — the subject is the half that travels", subject, want)
		}
	}
	for _, want := range []string{
		"mg done mg-ac0c --successor=",
		"declares-remainder",
		"DO NOT WEAKEN THE GUARD",
		"mr-2",
		"abc123def4567890",
		"work_item_merged_not_closed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not carry %q:\n%s", want, body)
		}
	}
}

// A merge with no live polecat says so rather than leaving a blank field: a
// hand-submitted branch (mg-be37) reaches this path too, and "who was working on
// this" is the first thing the reader asks.
func TestMergedOpenAlertMessageNamesAnAbsentWorker(t *testing.T) {
	a := mergedOpenAlert{WorkItemID: "mg-ac0c", Target: "main"}
	if _, body := a.Message(); !strings.Contains(body, "none was running at merge") {
		t.Errorf("body does not say that no worker was running:\n%s", body)
	}
}

// THE REMEDY IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT. This alert exists
// because a fact established inside pogod reached no reader; the alert's own
// delivery must not be able to fail the same way. The event is emitted before the
// mail, so a machine with no mg on PATH loses the improvement and not the record.
func TestReportMergedButOpenEmitsBeforeItMails(t *testing.T) {
	prevMail, prevDone := mergedOpenAlertMail, mergedOpenItemDone
	t.Cleanup(func() { mergedOpenAlertMail, mergedOpenItemDone = prevMail, prevDone })
	mergedOpenItemDone = func(string) (bool, error) { return false, nil }

	var eventsAtMailTime int
	mergedOpenAlertMail = func(mergedOpenAlert) {
		eventsAtMailTime = countMergedNotClosedEvents(t)
	}
	reportMergedButOpen(landedMR(), "ac0c", errors.New("mg done failed: exit status 4"))

	if eventsAtMailTime == 0 {
		t.Error("the mail sink ran before the event was on the spine; a failed send would leave " +
			"no record at all, which is the defect this alert exists to close")
	}
}

// countMergedNotClosedEvents reads the throwaway event log TestMain installed.
func countMergedNotClosedEvents(t *testing.T) int {
	t.Helper()
	if testEventLogPath == "" {
		t.Skip("no throwaway event log was installed; TestMain could not make a temp dir")
	}
	raw, err := os.ReadFile(testEventLogPath)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "work_item_merged_not_closed")
}

// A GATED item is the one not-closed case that raises no alert, and this pins
// the decision made when the mg-9d4e rescue was rebased onto a main that had
// grown the gated path underneath it (mg-2b71).
//
// The two are both "the merge landed and the item is open", so nothing in the
// rescued code distinguished them — it predates the case existing. They are not
// the same state. A gated item is one pogod COULD have closed and DECLINED to,
// because `parked`/`human`/`blocked:` is work somebody stopped on purpose; and
// being gated is precisely what makes it undispatchable, so the harm this alert
// exists to warn about — a worker sent at merged work — cannot arise. Mailing
// the coordinator "file its successor, do not dispatch" there is advice about a
// remainder that does not exist, at an item nobody can be sent to.
//
// It is driven through reapMergedPolecat rather than reportMergedButOpen because
// the exclusion lives at the CALL SITE: the function under test here would
// happily alert on a gated error, and asserting on it directly would pass while
// the branch that matters was deleted.
func TestAGatedItemRaisesNoMergedNotClosedAlert(t *testing.T) {
	got := captureMergedOpenAlerts(t, func(string) (bool, error) { return false, nil })
	mr := &refinery.MergeRequest{ID: "mr-1", Branch: "polecat-c479c", Author: "mg-479c", MergedSHA: "1a0240a"}

	gated := fmt.Errorf("%w: mg-479c is unclaimed and assigned to %q", client.ErrMGWorkItemGated, "parked")
	reapMergedPolecat(&fakeReaper{}, mr, func(string, string) error { return gated }, postMergeVerdict{}, nil,
		&capturingFiler{})
	if len(*got) != 0 {
		t.Errorf("a deliberately gated item raised a merged-not-closed alert: %+v", *got)
	}

	// The negative control, so a call site that alerted on NOTHING would fail
	// here rather than pass the assertion above. Same merge, same open item, a
	// refusal that is not a gate.
	refused := errors.New("mg done failed: mg-479c declares a remainder and names no successor (exit status 4)")
	reapMergedPolecat(&fakeReaper{}, mr, func(string, string) error { return refused }, postMergeVerdict{}, nil,
		&capturingFiler{})
	if len(*got) != 1 {
		t.Fatalf("a genuine declares-remainder refusal raised %d alerts, want 1 — the exclusion above is "+
			"suppressing the case the alert exists for", len(*got))
	}
	if (*got)[0].WorkItemID != "mg-479c" {
		t.Errorf("the alert does not identify the item: %+v", (*got)[0])
	}
}
