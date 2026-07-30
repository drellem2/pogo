package ghintake

import (
	"strings"
	"testing"
	"time"
)

var scanTime = time.Date(2026, 7, 30, 5, 0, 0, 0, time.UTC)

func issue(repo string, n int, ago time.Duration) Issue {
	return Issue{
		Repo: repo, Number: n,
		Title:     "something is broken",
		Author:    "CloverRoss",
		CreatedAt: scanTime.Add(-ago),
		URL:       "https://github.com/" + repo + "/issues/1",
	}
}

func carrier(id, ref string) CarrierRef {
	return CarrierRef{ItemID: id, Status: "claimed", Ref: NormalizeRef(ref)}
}

// inv builds an Inventory that is NOT blind: ItemsScanned is set to a plausible
// non-zero count, because a fixture with ItemsScanned=0 exercises the blind-scan
// path rather than the reconciliation, and that mistake would make every other
// assertion in this file vacuous.
func inv(issues []Issue, carriers []CarrierRef, repos ...string) Inventory {
	if len(repos) == 0 {
		repos = []string{"drellem2/pogo"}
	}
	return Inventory{
		Issues: issues, Carriers: carriers, ItemsScanned: 2047,
		Statuses: mgStatuses, Repos: repos,
	}
}

// TestUncarriedIssueIsReported_PositiveControl is the positive arm of the
// acceptance: an open issue with no `gh:` carrier IS reported.
//
// It reconstructs the measured failure. drellem2/pogo#99 was filed at 18:53:58Z
// on 2026-07-29 and had no carrier until 05:44Z the next morning; #100, filed 19
// minutes later, got mg-2fcc. So the fixture holds both, carries only #100, and
// asserts the split pair is exactly what the report shows.
func TestUncarriedIssueIsReported_PositiveControl(t *testing.T) {
	i99 := issue("drellem2/pogo", 99, 10*time.Hour+6*time.Minute)
	i100 := issue("drellem2/pogo", 100, 9*time.Hour+47*time.Minute)

	rep := Detect(inv(
		[]Issue{i99, i100},
		[]CarrierRef{carrier("mg-2fcc", "drellem2/pogo#100")},
	), scanTime, DefaultGrace)

	if !rep.Actionable() {
		t.Fatal("an open issue with no carrier must be actionable; got a clean report")
	}
	if len(rep.Uncarried) != 1 {
		t.Fatalf("want exactly 1 uncarried finding, got %d: %+v", len(rep.Uncarried), rep.Uncarried)
	}
	if got := rep.Uncarried[0].Issue.Ref(); got != "drellem2/pogo#99" {
		t.Errorf("uncarried ref = %q, want drellem2/pogo#99", got)
	}
	if rep.Carried != 1 {
		t.Errorf("carried = %d, want 1 (#100 has mg-2fcc)", rep.Carried)
	}
	if rep.Scanned != 2 {
		t.Errorf("scanned = %d, want 2", rep.Scanned)
	}

	// The age must appear: "uncarried" and "uncarried for ten hours" are the same
	// finding with very different urgency, and the ten hours are the whole story.
	body := rep.Render()
	if !strings.Contains(body, "10h6m") {
		t.Errorf("render must state how long the issue has been uncarried; got:\n%s", body)
	}
	if !strings.Contains(body, "drellem2/pogo#99") {
		t.Errorf("render must name the uncarried issue; got:\n%s", body)
	}
	if strings.Contains(body, "drellem2/pogo#100") {
		t.Errorf("render must NOT list the carried issue as a finding; got:\n%s", body)
	}
	if subj := rep.MailSubject(); !strings.Contains(subj, "drellem2/pogo#99") {
		t.Errorf("mail subject must name the issue, got %q", subj)
	}
}

// TestFilingACarrierSilencesTheCheck is the negative arm of the acceptance, and
// it is the arm that makes this a check rather than a claim: with a carrier for
// the same issue, the identical scan goes quiet.
//
// A check that has only ever been seen to report zero is not a check — that is
// precisely the state gh#99 was in for ten hours.
func TestFilingACarrierSilencesTheCheck(t *testing.T) {
	i99 := issue("drellem2/pogo", 99, 10*time.Hour)
	issues := []Issue{i99}

	before := Detect(inv(issues, nil), scanTime, DefaultGrace)
	if len(before.Uncarried) != 1 {
		t.Fatalf("setup: want 1 uncarried before the carrier is filed, got %d", len(before.Uncarried))
	}

	// mayor files mg-d764 with `gh: drellem2/pogo#99` in its body.
	after := Detect(inv(issues, []CarrierRef{carrier("mg-d764", "drellem2/pogo#99")}), scanTime, DefaultGrace)

	if after.Actionable() {
		t.Fatalf("filing a carrier must silence the check; still actionable: %+v", after.Uncarried)
	}
	if len(after.Uncarried) != 0 {
		t.Errorf("uncarried = %d, want 0", len(after.Uncarried))
	}
	if after.Carried != 1 {
		t.Errorf("carried = %d, want 1", after.Carried)
	}
	if subj := after.MailSubject(); subj != "" {
		t.Errorf("a clean report has no mail subject, got %q", subj)
	}
	// The quiet report must still state what it looked at: "0 uncarried" is only
	// meaningful next to "1 open issue scanned".
	if body := after.Render(); !strings.Contains(body, "1 carried") || !strings.Contains(body, "scanned 1 open issue") {
		t.Errorf("a clean render must state its own coverage; got:\n%s", body)
	}
}

// A carrier at ANY status counts. An archived carrier still means the issue was
// seen and processed; whether it should have been archived with the issue open is
// ghteardown's question, not this one.
func TestCarrierCountsAtEveryStatus(t *testing.T) {
	for _, status := range mgStatuses {
		t.Run(status, func(t *testing.T) {
			c := CarrierRef{ItemID: "mg-aaaa", Status: status, Ref: "drellem2/pogo#88"}
			rep := Detect(inv([]Issue{issue("drellem2/pogo", 88, 48*time.Hour)}, []CarrierRef{c}), scanTime, DefaultGrace)
			if rep.Actionable() {
				t.Fatalf("a carrier at status=%s must count; report still actionable", status)
			}
		})
	}
}

// The grace window keeps the detector off the happy path: an issue filed a minute
// ago is a mail in flight, not a dropped one.
func TestFreshIssueIsListedButNotAlarmed(t *testing.T) {
	rep := Detect(inv([]Issue{issue("drellem2/pogo", 101, 46*time.Second)}, nil), scanTime, DefaultGrace)

	if rep.Actionable() {
		t.Fatal("an issue inside the grace window must not be actionable")
	}
	if len(rep.Fresh) != 1 {
		t.Fatalf("want 1 fresh finding, got %d", len(rep.Fresh))
	}
	if len(rep.Uncarried) != 0 {
		t.Fatalf("want 0 uncarried, got %d", len(rep.Uncarried))
	}
	// Listed, though — one grace window away from being a finding.
	if body := rep.Render(); !strings.Contains(body, "drellem2/pogo#101") {
		t.Errorf("a fresh issue must still be listed; got:\n%s", body)
	}

	// And the moment it ages out, it is a finding.
	aged := Detect(inv([]Issue{issue("drellem2/pogo", 101, DefaultGrace+time.Minute)}, nil), scanTime, DefaultGrace)
	if len(aged.Uncarried) != 1 {
		t.Fatalf("past the grace window the same issue must be a finding, got %d", len(aged.Uncarried))
	}
}

// A negative grace disables the window entirely — the documented off switch.
func TestNegativeGraceAlarmsImmediately(t *testing.T) {
	rep := Detect(inv([]Issue{issue("drellem2/pogo", 101, 5*time.Second)}, nil), scanTime, -1)
	if len(rep.Uncarried) != 1 {
		t.Fatalf("grace<0 must alarm immediately, got %d uncarried", len(rep.Uncarried))
	}
}

// An issue with no CreatedAt cannot be aged, and must NOT be swallowed by the
// grace window. Unknown age is not evidence of youth.
func TestMissingCreatedAtIsNotTreatedAsFresh(t *testing.T) {
	i := issue("drellem2/pogo", 77, 0)
	i.CreatedAt = time.Time{}
	rep := Detect(inv([]Issue{i}, nil), scanTime, DefaultGrace)
	if len(rep.Uncarried) != 1 {
		t.Fatalf("an issue with no createdAt must still be reported, got %d uncarried / %d fresh",
			len(rep.Uncarried), len(rep.Fresh))
	}
}

// A repo whose issue list failed is a finding, not a clean repo. This is the
// mirror of ghteardown's law: an empty list from a failed call must never read as
// "no open issues, nothing to reconcile, all clear".
func TestUnreadableRepoIsAFindingNotSilence(t *testing.T) {
	in := inv([]Issue{issue("drellem2/pogo", 88, 72*time.Hour)},
		[]CarrierRef{carrier("mg-aaaa", "drellem2/pogo#88")},
		"drellem2/pogo", "drellem2/macguffin")
	in.RepoErrors = []RepoError{{Repo: "drellem2/macguffin", Detail: "gh: HTTP 401 Bad credentials"}}

	rep := Detect(in, scanTime, DefaultGrace)
	if !rep.Actionable() {
		t.Fatal("a repo we could not read must be actionable; every issue we DID see was carried, which is exactly the trap")
	}
	if len(rep.Uncarried) != 0 {
		t.Errorf("uncarried = %d, want 0 — the finding here is the blind repo", len(rep.Uncarried))
	}
	body := rep.Render()
	if !strings.Contains(body, "UNREADABLE") || !strings.Contains(body, "drellem2/macguffin") {
		t.Errorf("render must name the unreadable repo; got:\n%s", body)
	}
	if !strings.Contains(body, "Bad credentials") {
		t.Errorf("render must carry the failure detail; got:\n%s", body)
	}
	if subj := rep.MailSubject(); !strings.Contains(subj, "unreadable") {
		t.Errorf("mail subject must mention the unreadable repo, got %q", subj)
	}
}

// A carrier scan that examined zero work items must report BLINDNESS, not a wall
// of uncarried issues. This is the loud-and-wrong failure mode: joining against
// an empty carrier set classifies every open issue as a miss, which looks like a
// catastrophe, is entirely an artefact of the scan, and would get the detector
// muted before the run that matters.
func TestZeroItemsScannedReportsBlindnessNotMaximalNoise(t *testing.T) {
	in := inv([]Issue{
		issue("drellem2/pogo", 88, 72*time.Hour),
		issue("drellem2/pogo", 91, 48*time.Hour),
		issue("drellem2/pogo", 99, 10*time.Hour),
	}, nil)
	in.ItemsScanned = 0

	rep := Detect(in, scanTime, DefaultGrace)
	if !rep.BlindStore {
		t.Fatal("ItemsScanned=0 must set BlindStore")
	}
	if !rep.Actionable() {
		t.Fatal("a blind scan is itself the finding and must be actionable")
	}
	if len(rep.Uncarried) != 0 {
		t.Fatalf("a blind scan must NOT emit uncarried findings, got %d: %+v", len(rep.Uncarried), rep.Uncarried)
	}
	body := rep.Render()
	if !strings.Contains(body, "BLIND SCAN") {
		t.Errorf("render must say the scan was blind; got:\n%s", body)
	}
	if !strings.Contains(rep.MailSubject(), "BLIND SCAN") {
		t.Errorf("mail subject must say the scan was blind, got %q", rep.MailSubject())
	}
}

// A store with items but no carriers at all IS reconciled — that is a fact about
// the store, not about the scan, and it must not be confused with blindness.
func TestItemsScannedWithNoCarriersStillReconciles(t *testing.T) {
	in := inv([]Issue{issue("drellem2/pogo", 99, 10*time.Hour)}, nil)
	in.ItemsScanned = 2047
	rep := Detect(in, scanTime, DefaultGrace)
	if rep.BlindStore {
		t.Fatal("2047 items with zero carriers is a fact about the store, not a blind scan")
	}
	if len(rep.Uncarried) != 1 {
		t.Fatalf("want 1 uncarried, got %d", len(rep.Uncarried))
	}
}

// Oldest first: the issue that has waited longest is the one to act on, and the
// one a number-ordered report would bury.
func TestUncarriedOrderedOldestFirst(t *testing.T) {
	rep := Detect(inv([]Issue{
		issue("drellem2/pogo", 99, 10*time.Hour),
		issue("drellem2/pogo", 88, 13*24*time.Hour),
		issue("drellem2/pogo", 91, 9*24*time.Hour),
	}, nil), scanTime, DefaultGrace)

	var got []int
	for _, f := range rep.Uncarried {
		got = append(got, f.Issue.Number)
	}
	want := []int{88, 91, 99}
	if len(got) != len(want) {
		t.Fatalf("want %d findings, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (oldest first)", got, want)
		}
	}
}

// GitHub owner/repo names are case-insensitive, so a carrier filed with different
// casing still covers the issue. A case-sensitive compare would report an issue
// as uncarried while a perfectly good carrier sat in the store — a false alarm
// the coordinator could not clear by doing the right thing.
func TestRefComparisonIsCaseInsensitive(t *testing.T) {
	rep := Detect(inv(
		[]Issue{issue("drellem2/pogo", 99, 10*time.Hour)},
		[]CarrierRef{{ItemID: "mg-d764", Status: "done", Ref: NormalizeRef("Drellem2/Pogo#99")}},
	), scanTime, DefaultGrace)
	if rep.Actionable() {
		t.Fatalf("a differently-cased carrier ref must still count: %+v", rep.Uncarried)
	}
}

func TestNormalizeRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"drellem2/pogo#99", "drellem2/pogo#99"},
		{"  drellem2/pogo#99  ", "drellem2/pogo#99"},
		{"Drellem2/POGO#99", "drellem2/pogo#99"},
		{"`drellem2/pogo#99`", "drellem2/pogo#99"},
		{"https://github.com/drellem2/pogo/issues/99", "drellem2/pogo#99"},
		{"drellem2/pogo#99.", "drellem2/pogo#99"},
		// Not refs.
		{"drellem2/pogo", ""},
		{"#99", ""},
		{"drellem2/pogo#0", ""},
		{"drellem2/pogo#abc", ""},
		{"a/b/c#1", ""},
		{"", ""},
		{"see below", ""},
	}
	for _, c := range cases {
		if got := NormalizeRef(c.in); got != c.want {
			t.Errorf("NormalizeRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m"},
		{-time.Hour, "0m"},
		{46 * time.Second, "0m"},
		{20 * time.Minute, "20m"},
		{10*time.Hour + 6*time.Minute, "10h6m"},
		{25 * time.Hour, "1d1h"},
		{13 * 24 * time.Hour, "13d0h"},
	}
	for _, c := range cases {
		if got := humanAge(c.d); got != c.want {
			t.Errorf("humanAge(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// A report of unchanged state must render byte-identically, or a reader watching
// for change learns to stop reading it.
func TestRenderIsStableAcrossRuns(t *testing.T) {
	in := inv([]Issue{
		issue("drellem2/pogo", 99, 10*time.Hour),
		issue("drellem2/macguffin", 4, 3*24*time.Hour),
	}, nil, "drellem2/pogo", "drellem2/macguffin")
	a := Detect(in, scanTime, DefaultGrace).Render()
	b := Detect(in, scanTime, DefaultGrace).Render()
	if a != b {
		t.Errorf("render is not stable:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}
