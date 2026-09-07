package carrierdrift

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/ghteardown"
)

// TestRenderNamesTheRemedyForEachKind. The three findings have three different
// fixes, and a report that listed them under one heading would send its reader
// at one of them. Each section must name its own remedy and its own declaration
// key.
func TestRenderNamesTheRemedyForEachKind(t *testing.T) {
	carriers := []Carrier{
		carrier("mg-e605", "drellem2/pogo#127", "triage", 31*24*time.Hour),
		carrier("mg-0802", "drellem2/pogo#159", "gated", 4*24*time.Hour),
		carrier("mg-edc2", "drellem2/pogo#156", "triage", 17*24*time.Hour),
	}
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#127": {State: StateClosed, Created: ago(31 * 24 * time.Hour),
			ClosedAt: ago(30 * 24 * time.Hour), Acknowledged: true, Comments: 3},
		"drellem2/pogo#159": openSnap(16*24*time.Hour, false, 0),
		"drellem2/pogo#156": openSnap(17*24*time.Hour, true, 2),
	})
	out := Detect(carriers, snaps, now, Windows{}).Render()

	for _, want := range []string{
		"ISSUE CLOSED", "gh-closed:", "DISPATCHABLE",
		"NOT ACKNOWLEDGED", "gh-ack:", "indistinguishable from uncarried",
		"STAGE NOT ADVANCING", "gh-parked:", "no stage-change",
		"mg-e605", "mg-0802", "mg-edc2",
		"drellem2/pogo#127", "drellem2/pogo#159", "drellem2/pogo#156",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderStatesItsOwnCoverage. A report that printed only findings would let
// "nothing found" and "nothing examined" render identically — which is the
// mistake `check-intake` reading "44 carried, 0 uncarried" was NOT making, and
// this detector must not start.
func TestRenderStatesItsOwnCoverage(t *testing.T) {
	rep := Detect(nil, table(t, nil), now, Windows{})
	rep.Statuses = []string{"available", "claimed", "pending"}
	rep.StoreItems = 175
	out := rep.Render()

	for _, want := range []string{
		"re-read 0 live carrier(s) from 175 work item(s)",
		"available claimed pending",
		"windows:", "ack 1d0h", "stage 3d0h", "6h0m grace",
		"(none) triage", // the covered stage set, with the empty stage named
	} {
		if !strings.Contains(out, want) {
			t.Errorf("coverage line is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderLeadsWithTheBannerWhenNothingWasMeasured. Everything below the
// banner is a list of carriers that were NOT checked; a reader who skims the
// findings and stops must not come away thinking a pass ran.
func TestRenderLeadsWithTheBannerWhenNothingWasMeasured(t *testing.T) {
	carriers := []Carrier{
		carrier("mg-a", "drellem2/pogo#1", "build", time.Hour),
		carrier("mg-b", "drellem2/pogo#2", "build", time.Hour),
	}
	snap := func(string, int) (Snapshot, error) {
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureNetwork, Msg: "no such host"}
	}
	rep := Detect(carriers, snap, now, Windows{})
	out := rep.Render()

	if !strings.HasPrefix(out, "NO CARRIER WAS RE-READ") {
		t.Fatalf("banner is not first:\n%s", out)
	}
	if !strings.Contains(out, "measured NOTHING") {
		t.Errorf("banner does not say the pass measured nothing:\n%s", out)
	}
	if !strings.Contains(out, "not\nevidence that anything reported earlier has cleared") {
		t.Errorf("banner does not warn that this is not evidence of clearing:\n%s", out)
	}

	// And the subject says so too, without also counting the same carriers a
	// second time as ordinary blocked findings.
	subj := rep.MailSubject()
	if !strings.Contains(subj, "NO CARRIER RE-READ") {
		t.Fatalf("subject = %q", subj)
	}
	if strings.Contains(subj, "NOT re-read") {
		t.Fatalf("subject double-counts the outage as findings: %q", subj)
	}
}

// TestMailSubjectNamesTheKindNotJustACount. The subject is the part that
// travels: it is what a reader skims, forwards and files a ticket from, and "3
// drifted carriers" would send them looking for one thing when there are three
// different remedies in the body.
func TestMailSubjectNamesTheKindNotJustACount(t *testing.T) {
	carriers := []Carrier{
		carrier("mg-e605", "drellem2/pogo#127", "triage", 31*24*time.Hour),
		carrier("mg-0802", "drellem2/pogo#159", "gated", 4*24*time.Hour),
		carrier("mg-edc2", "drellem2/pogo#156", "triage", 17*24*time.Hour),
	}
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#127": {State: StateClosed, Created: ago(31 * 24 * time.Hour),
			ClosedAt: ago(30 * 24 * time.Hour), Acknowledged: true},
		"drellem2/pogo#159": openSnap(16*24*time.Hour, false, 0),
		"drellem2/pogo#156": openSnap(17*24*time.Hour, true, 2),
	})
	subj := Detect(carriers, snaps, now, Windows{}).MailSubject()

	for _, want := range []string{
		"CLOSED issues", "drellem2/pogo#127",
		"NO acknowledgement", "drellem2/pogo#159",
		"stuck at their filing stage", "drellem2/pogo#156",
	} {
		if !strings.Contains(subj, want) {
			t.Errorf("subject is missing %q: %q", want, subj)
		}
	}
}

// TestHumanWindowNamesAnOffCheck. A report printing "-1s" for a disabled check
// would leave its reader deducing what that meant — and a reader who deduces
// wrong concludes the check ran.
func TestHumanWindowNamesAnOffCheck(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{-1, "off"},
		{0, "immediate"},
		{24 * time.Hour, "1d0h"},
		{30 * time.Minute, "30m"},
		{90 * time.Minute, "1h30m"},
	} {
		if got := humanWindow(tc.in); got != tc.want {
			t.Errorf("humanWindow(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
