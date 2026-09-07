package carrierdrift

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/ghteardown"
)

var now = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

// ago builds a time d before the fixed now.
func ago(d time.Duration) time.Time { return now.Add(-d) }

// carrier builds a live carrier at a stage, filed d ago.
func carrier(id, ref, stage string, d time.Duration) Carrier {
	repo, number, err := ParseRef(ref)
	if err != nil {
		panic(err)
	}
	return Carrier{
		ID: id, Title: "triage: " + id, Status: "available", Stage: stage,
		Repo: repo, Number: number, Created: ago(d),
	}
}

// table binds a fixed set of snapshots, and FAILS the test on a ref nobody
// staged — a silent zero Snapshot would look like a StateUnknown and quietly
// turn a mis-wired test green in the blocked bucket.
func table(t *testing.T, m map[string]Snapshot) SnapshotFunc {
	t.Helper()
	return func(repo string, number int) (Snapshot, error) {
		key := fmt.Sprintf("%s#%d", repo, number)
		s, ok := m[key]
		if !ok {
			t.Fatalf("snapshot requested for unstaged ref %s", key)
		}
		return s, nil
	}
}

func openSnap(filed time.Duration, acked bool, comments int) Snapshot {
	s := Snapshot{State: StateOpen, Created: ago(filed), Comments: comments}
	if acked {
		s.Acknowledged = true
		s.AcknowledgedAt = ago(filed / 2)
	}
	return s
}

// TestDetectFindsTheThreeFoundingInstances is the whole ticket in one table.
// Each row is one of the three symptoms found on 2026-09-07, and the point of
// running them together is that ONE re-read produces all three: a fix scoped to
// whichever was noticed first would leave the other two to be rediscovered.
func TestDetectFindsTheThreeFoundingInstances(t *testing.T) {
	carriers := []Carrier{
		// #127: carried against an issue closed the same day, still dispatchable.
		carrier("mg-e605", "drellem2/pogo#127", "triage", 31*24*time.Hour),
		// #159: carried, and the reporter has heard nothing.
		carrier("mg-0802", "drellem2/pogo#159", "gated", 4*24*time.Hour),
		// #156: carried and still at the stage it was filed at, 17 days later.
		carrier("mg-edc2", "drellem2/pogo#156", "triage", 17*24*time.Hour),
		// A carrier with nothing wrong with it, so "found three" is a
		// discrimination and not an indiscriminate alarm.
		carrier("mg-0000", "drellem2/pogo#200", "build", 2*time.Hour),
	}
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#127": {State: StateClosed, Created: ago(31 * 24 * time.Hour),
			ClosedAt: ago(31 * 24 * time.Hour), Acknowledged: true, Comments: 3},
		"drellem2/pogo#159": openSnap(16*24*time.Hour, false, 0),
		"drellem2/pogo#156": openSnap(17*24*time.Hour, true, 2),
		"drellem2/pogo#200": openSnap(2*time.Hour, true, 1),
	})

	rep := Detect(carriers, snaps, now, Windows{})

	if len(rep.Closed) != 1 || rep.Closed[0].Carrier.ID != "mg-e605" {
		t.Fatalf("closed-issue finding: got %+v", rep.Closed)
	}
	if len(rep.Unacknowledged) != 1 || rep.Unacknowledged[0].Carrier.ID != "mg-0802" {
		t.Fatalf("unacknowledged finding: got %+v", rep.Unacknowledged)
	}
	if len(rep.StuckStage) != 1 || rep.StuckStage[0].Carrier.ID != "mg-edc2" {
		t.Fatalf("stuck-stage finding: got %+v", rep.StuckStage)
	}
	if rep.Scanned != 4 {
		t.Fatalf("scanned = %d, want 4", rep.Scanned)
	}
	// The healthy carrier, and #127 whose issue is closed, are the two that
	// produce no finding — but only one of them is "current".
	if rep.Current != 1 {
		t.Fatalf("current = %d, want 1 (only the healthy carrier)", rep.Current)
	}
	if !rep.Actionable() {
		t.Fatal("report with three findings is not Actionable")
	}
	if rep.InstrumentFailure() {
		t.Fatal("a pass with three verdicts reported InstrumentFailure")
	}
}

// TestClosedIssueShortCircuitsTheOtherTwoChecks pins the ordering decision.
// Once the issue is closed, "nobody acknowledged it" and "triage never
// finished" are no longer the news and no longer the remedy — the carrier is.
// Reporting all three would triple one finding.
func TestClosedIssueShortCircuitsTheOtherTwoChecks(t *testing.T) {
	c := carrier("mg-1111", "drellem2/pogo#127", "triage", 30*24*time.Hour)
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#127": {State: StateClosed, Created: ago(30 * 24 * time.Hour),
			ClosedAt: ago(29 * 24 * time.Hour)},
	})

	rep := Detect([]Carrier{c}, snaps, now, Windows{})

	if len(rep.Closed) != 1 {
		t.Fatalf("closed findings = %d, want 1", len(rep.Closed))
	}
	if len(rep.Unacknowledged) != 0 || len(rep.StuckStage) != 0 {
		t.Fatalf("closed issue also produced %d unack / %d stuck findings",
			len(rep.Unacknowledged), len(rep.StuckStage))
	}
	// And the age is measured from the CLOSE, not from the carrier or the issue.
	if got := rep.Closed[0].Age; got != 29*24*time.Hour {
		t.Fatalf("closed age = %s, want 29 days since closedAt", got)
	}
}

// TestOneCarrierCanProduceTwoIndependentFindings is the mirror of the
// short-circuit: an open issue whose reporter has heard nothing AND whose
// triage has not moved is two facts with two different remedies, and collapsing
// them would hide whichever was noticed second.
func TestOneCarrierCanProduceTwoIndependentFindings(t *testing.T) {
	c := carrier("mg-2222", "drellem2/pogo#160", "triage", 10*24*time.Hour)
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#160": openSnap(10*24*time.Hour, false, 0),
	})

	rep := Detect([]Carrier{c}, snaps, now, Windows{})

	if len(rep.Unacknowledged) != 1 || len(rep.StuckStage) != 1 {
		t.Fatalf("want one finding of each kind, got %d unack / %d stuck",
			len(rep.Unacknowledged), len(rep.StuckStage))
	}
	// The two findings must have DIFFERENT keys, or the watcher's per-finding
	// ageing would treat them as one and the second would never escalate.
	if rep.Unacknowledged[0].Key() == rep.StuckStage[0].Key() {
		t.Fatalf("both findings share key %q", rep.StuckStage[0].Key())
	}
	// Scanned counts carriers, not findings.
	if rep.Scanned != 1 || rep.Current != 0 {
		t.Fatalf("scanned=%d current=%d, want 1/0", rep.Scanned, rep.Current)
	}
}

// TestAcknowledgementIsMeasuredFromTheReportersFiling pins the clock that
// matters. The reporter cannot see our carrier and does not know one exists, so
// their wait starts when THEY filed. A carrier filed minutes ago against an
// issue that has been open unanswered for a fortnight is still a reporter who
// has heard nothing.
func TestAcknowledgementIsMeasuredFromTheReportersFiling(t *testing.T) {
	c := carrier("mg-3333", "drellem2/pogo#159", "gated", 5*time.Minute)
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#159": openSnap(16*24*time.Hour, false, 0),
	})

	rep := Detect([]Carrier{c}, snaps, now, Windows{})

	if len(rep.Unacknowledged) != 1 {
		t.Fatalf("a five-minute-old carrier on a 16-day-unanswered issue produced "+
			"%d unacknowledged findings, want 1", len(rep.Unacknowledged))
	}
	if got := rep.Unacknowledged[0].Age; got != 16*24*time.Hour {
		t.Fatalf("age = %s, want the ISSUE's 16 days and not the carrier's five minutes", got)
	}
}

// TestStuckStageOnlyCoversTheFilingStages is the coverage boundary made
// explicit. mg records no stage-change timestamp, so for any stage a carrier
// was NOT filed at, the carrier's age is a lower bound rather than the stage's
// age — and reporting a lower bound as a measurement is the shape of mistake
// this package exists to stop.
func TestStuckStageOnlyCoversTheFilingStages(t *testing.T) {
	old := 30 * 24 * time.Hour
	carriers := []Carrier{
		carrier("mg-triage", "drellem2/pogo#1", "triage", old),
		carrier("mg-nostage", "drellem2/pogo#2", "", old),
		carrier("mg-gated", "drellem2/pogo#3", "gated", old),
		carrier("mg-build", "drellem2/pogo#4", "build", old),
		carrier("mg-review", "drellem2/pogo#5", "review", old),
	}
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#1": openSnap(old, true, 1),
		"drellem2/pogo#2": openSnap(old, true, 1),
		"drellem2/pogo#3": openSnap(old, true, 1),
		"drellem2/pogo#4": openSnap(old, true, 1),
		"drellem2/pogo#5": openSnap(old, true, 1),
	})

	rep := Detect(carriers, snaps, now, Windows{})

	var got []string
	for _, f := range rep.StuckStage {
		got = append(got, f.Carrier.ID)
	}
	want := []string{"mg-nostage", "mg-triage"} // tie on age, ordered by key
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stuck stages = %v, want exactly the filing stages %v", got, want)
	}

	// Widening the set is a deliberate act, and it must work — the boundary is a
	// default, not a wall.
	wide := Detect(carriers, snaps, now, Windows{Stages: []string{"gated"}})
	if len(wide.StuckStage) != 1 || wide.StuckStage[0].Carrier.ID != "mg-gated" {
		t.Fatalf("explicit --stage gated: got %+v", wide.StuckStage)
	}
}

// TestNegativeWindowDisablesOneCheckWithoutDisablingTheOthers pins the
// zero-versus-negative contract that keeps a config omitting a key different
// from one turning a check off.
func TestNegativeWindowDisablesOneCheckWithoutDisablingTheOthers(t *testing.T) {
	c := carrier("mg-4444", "drellem2/pogo#160", "triage", 10*24*time.Hour)
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#160": openSnap(10*24*time.Hour, false, 0),
	})

	off := Detect([]Carrier{c}, snaps, now, Windows{Ack: -1})
	if len(off.Unacknowledged) != 0 {
		t.Fatalf("Ack=-1 still produced %d unacknowledged findings", len(off.Unacknowledged))
	}
	if len(off.StuckStage) != 1 {
		t.Fatalf("disabling the ack check also disabled the stage check: %d stuck", len(off.StuckStage))
	}
	if off.Windows.Ack >= 0 {
		t.Fatalf("resolved Ack window = %s, want the negative preserved", off.Windows.Ack)
	}
	// And the defaults must still be applied to the fields left at zero.
	if off.Windows.Stage != DefaultStageWindow || off.Windows.Closed != DefaultClosedGrace {
		t.Fatalf("zero fields did not take defaults: %+v", off.Windows)
	}
}

// TestClosedGraceCountsAsCurrentRatherThanDisappearing pins that a carrier
// inside the grace window is COUNTED. A finding that is neither reported nor
// counted is a carrier the scan silently forgot, which is a smaller version of
// the defect itself.
func TestClosedGraceCountsAsCurrentRatherThanDisappearing(t *testing.T) {
	c := carrier("mg-5555", "drellem2/pogo#170", "merge", 3*24*time.Hour)
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#170": {State: StateClosed, Created: ago(3 * 24 * time.Hour),
			ClosedAt: ago(10 * time.Minute), Acknowledged: true},
	})

	rep := Detect([]Carrier{c}, snaps, now, Windows{})
	if len(rep.Closed) != 0 {
		t.Fatalf("a close ten minutes ago fired inside the %s grace", DefaultClosedGrace)
	}
	if rep.Scanned != 1 || rep.Current != 1 {
		t.Fatalf("scanned=%d current=%d, want 1/1", rep.Scanned, rep.Current)
	}

	// A negative grace is the documented "report immediately" setting.
	immediate := Detect([]Carrier{c}, snaps, now, Windows{Closed: -1})
	if len(immediate.Closed) != 1 {
		t.Fatalf("Closed=-1 did not report immediately: %+v", immediate.Closed)
	}
}

// TestUnknownAgeAbstainsRatherThanFiring is the direction that matters for a
// missing timestamp. An unparseable `created` must never become an OLD created:
// that would manufacture a finding out of a store-read defect.
func TestUnknownAgeAbstainsRatherThanFiring(t *testing.T) {
	c := carrier("mg-6666", "drellem2/pogo#180", "triage", time.Hour)
	c.Created = time.Time{} // as parseTime returns for an absent stamp
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#180": {State: StateOpen, Comments: 0}, // no issue createdAt either
	})

	rep := Detect([]Carrier{c}, snaps, now, Windows{})
	if len(rep.StuckStage) != 0 || len(rep.Unacknowledged) != 0 {
		t.Fatalf("zero timestamps produced findings: %d stuck, %d unack",
			len(rep.StuckStage), len(rep.Unacknowledged))
	}
	if rep.Current != 1 {
		t.Fatalf("current = %d, want the abstained carrier counted", rep.Current)
	}
}

// TestBlockedAndIndeterminateNeverShareABucket is mg-dd22's law, restated in
// this package because it is the law a re-read detector is most tempted to
// break: "we could not ask" and "the answer is unusable" are facts about
// different things and must not render alike.
func TestBlockedAndIndeterminateNeverShareABucket(t *testing.T) {
	carriers := []Carrier{
		carrier("mg-net", "drellem2/pogo#1", "build", time.Hour),
		carrier("mg-gone", "drellem2/pogo#2", "build", time.Hour),
	}
	snap := func(repo string, number int) (Snapshot, error) {
		if number == 1 {
			return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
				Class: ghteardown.FailureNetwork, Msg: "dial tcp: lookup api.github.com: no such host"}
		}
		return Snapshot{State: StateUnknown}, &ghteardown.LookupError{
			Class: ghteardown.FailureSubject, Msg: "Could not resolve to an Issue"}
	}

	rep := Detect(carriers, snap, now, Windows{})

	if len(rep.Blocked) != 1 || rep.Blocked[0].Carrier.ID != "mg-net" {
		t.Fatalf("blocked = %+v", rep.Blocked)
	}
	if len(rep.Indeterminate) != 1 || rep.Indeterminate[0].Carrier.ID != "mg-gone" {
		t.Fatalf("indeterminate = %+v", rep.Indeterminate)
	}
	if !rep.Actionable() {
		t.Fatal("a pass that reached no verdict for either carrier is not Actionable")
	}
	// Both no-verdict, both carriers: this pass measured NOTHING.
	if !rep.InstrumentFailure() {
		t.Fatal("2 scanned, 2 without a verdict, and InstrumentFailure is false")
	}
	if got := strings.Join(rep.FailureClasses(), ","); got != "network,subject" {
		t.Fatalf("failure classes = %q", got)
	}
}

// TestInstrumentFailureNeedsTwoCarriers pins the sample-of-one rule: with one
// carrier, a no-verdict pass and a no-verdict carrier are the same observation,
// and claiming instrument failure from that would invent the distinction rather
// than detect it.
func TestInstrumentFailureNeedsTwoCarriers(t *testing.T) {
	c := carrier("mg-solo", "drellem2/pogo#1", "build", time.Hour)
	snap := func(string, int) (Snapshot, error) {
		return Snapshot{State: StateUnknown}, errors.New("no such host")
	}
	rep := Detect([]Carrier{c}, snap, now, Windows{})
	if rep.InstrumentFailure() {
		t.Fatal("one carrier without a verdict was reported as a broken instrument")
	}
	if len(rep.Blocked) != 1 {
		t.Fatalf("blocked = %d, want 1", len(rep.Blocked))
	}
}

// TestASnapshotErrorIsNeverReadAsAnAnswer is the tempting bug stated directly:
// a re-read that failed must not read as "not closed, therefore fine". Here the
// error comes back WITH a plausible-looking open state, and it must still be
// treated as no answer at all.
func TestASnapshotErrorIsNeverReadAsAnAnswer(t *testing.T) {
	c := carrier("mg-7777", "drellem2/pogo#190", "triage", 30*24*time.Hour)
	snap := func(string, int) (Snapshot, error) {
		return Snapshot{State: StateOpen, Acknowledged: true}, errors.New("HTTP 403: rate limit exceeded")
	}
	rep := Detect([]Carrier{c}, snap, now, Windows{})
	if len(rep.Blocked) != 1 {
		t.Fatalf("an errored re-read did not land in blocked: %+v", rep)
	}
	if len(rep.StuckStage) != 0 || rep.Current != 0 {
		t.Fatalf("an errored re-read produced a verdict: %d stuck, %d current",
			len(rep.StuckStage), rep.Current)
	}
}

// TestDeclarationsSuppressExactlyOneKindEach pins why there are three keys and
// not one blanket one: a declaration that an issue is deliberately closed must
// not also silence a triage stuck for three weeks.
func TestDeclarationsSuppressExactlyOneKindEach(t *testing.T) {
	unack := carrier("mg-ack", "drellem2/pogo#1", "gated", 10*24*time.Hour)
	unack.DeclaredAck = "answered by email 2026-09-01"
	stuck := carrier("mg-park", "drellem2/pogo#2", "triage", 10*24*time.Hour)
	stuck.DeclaredParked = "waiting on upstream release"
	closed := carrier("mg-clos", "drellem2/pogo#3", "gated", 10*24*time.Hour)
	closed.DeclaredClosed = "public correction still owed on the closed thread"
	// The cross case: declaring the ack does NOT silence the stuck stage.
	both := carrier("mg-both", "drellem2/pogo#4", "triage", 10*24*time.Hour)
	both.DeclaredAck = "answered in the linked PR"

	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#1": openSnap(10*24*time.Hour, false, 0),
		"drellem2/pogo#2": openSnap(10*24*time.Hour, true, 1),
		"drellem2/pogo#3": {State: StateClosed, ClosedAt: ago(9 * 24 * time.Hour),
			Created: ago(10 * 24 * time.Hour), Acknowledged: true},
		"drellem2/pogo#4": openSnap(10*24*time.Hour, false, 0),
	})

	rep := Detect([]Carrier{unack, stuck, closed, both}, snaps, now, Windows{})

	if len(rep.Unacknowledged) != 0 || len(rep.Closed) != 0 {
		t.Fatalf("declarations did not suppress: %d unack, %d closed",
			len(rep.Unacknowledged), len(rep.Closed))
	}
	if len(rep.StuckStage) != 1 || rep.StuckStage[0].Carrier.ID != "mg-both" {
		t.Fatalf("gh-ack silenced a stuck stage it does not declare: %+v", rep.StuckStage)
	}
	if len(rep.Declared) != 4 {
		t.Fatalf("declared findings = %d, want 4 — a declaration buys silence, never invisibility",
			len(rep.Declared))
	}
	for _, f := range rep.Declared {
		if f.Kind != KindDeclared || f.Suppressed == "" || f.Detail == "" {
			t.Fatalf("declared finding does not say what it silences or why: %+v", f)
		}
	}
	// The only actionable content left is the stage the ack declaration does not
	// cover; a report whose findings are ALL declared is not actionable at all.
	onlyDeclared := Detect([]Carrier{unack, closed}, snaps, now, Windows{})
	if onlyDeclared.Actionable() {
		t.Fatalf("a report of nothing but declared findings is Actionable: %+v", onlyDeclared)
	}
	if len(onlyDeclared.Declared) != 2 {
		t.Fatalf("declared = %d, want 2", len(onlyDeclared.Declared))
	}
}

// TestOrderIsOldestFirstAndTotal pins the two properties a report needs to stay
// readable across samples: the longest-drifting carrier is first, and repeated
// passes over unchanged state produce byte-identical output. A report that
// reshuffles looks like it changed, and a reader watching for change learns to
// stop reading it.
func TestOrderIsOldestFirstAndTotal(t *testing.T) {
	carriers := []Carrier{
		carrier("mg-cccc", "drellem2/pogo#3", "triage", 5*24*time.Hour),
		carrier("mg-aaaa", "drellem2/pogo#1", "triage", 20*24*time.Hour),
		carrier("mg-bbbb", "drellem2/pogo#2", "triage", 5*24*time.Hour),
	}
	snaps := table(t, map[string]Snapshot{
		"drellem2/pogo#1": openSnap(20*24*time.Hour, true, 1),
		"drellem2/pogo#2": openSnap(5*24*time.Hour, true, 1),
		"drellem2/pogo#3": openSnap(5*24*time.Hour, true, 1),
	})

	first := Detect(carriers, snaps, now, Windows{})
	var got []string
	for _, f := range first.StuckStage {
		got = append(got, f.Carrier.ID)
	}
	if strings.Join(got, ",") != "mg-aaaa,mg-bbbb,mg-cccc" {
		t.Fatalf("order = %v, want oldest first then key", got)
	}

	// Re-detecting with the input shuffled must produce the identical rendering.
	shuffled := []Carrier{carriers[1], carriers[2], carriers[0]}
	second := Detect(shuffled, snaps, now, Windows{})
	if first.Render() != second.Render() {
		t.Fatalf("render is not stable under input order:\n--- first ---\n%s\n--- second ---\n%s",
			first.Render(), second.Render())
	}
}

// TestIsAcknowledgement pins the predicate at its edges. The failure direction
// that matters is the QUIET one — a comment counted as an acknowledgement when
// nobody replied leaves a reporter waiting, and nothing else would notice.
func TestIsAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"Thanks — looking into this.", true},
		{"thanks - looking into this", true},
		{"  \n Thanks — looking into this; apologies for the delayed response.", true},
		{"THANKS — LOOKING INTO THIS.", true},
		{"", false},
		{"Another day of data, from a day with unusually heavy PR-flow traffic.", false},
		// A REPORT that mentions the phrase mid-sentence is not an
		// acknowledgement. Prefix-anchoring is what keeps that true.
		{"I filed this because nobody said thanks — looking into this was never done.", false},
		{"This landed. Closing.", false},
	} {
		if got := IsAcknowledgement(tc.body); got != tc.want {
			t.Errorf("IsAcknowledgement(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
