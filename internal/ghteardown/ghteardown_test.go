package ghteardown

import (
	"errors"
	"strings"
	"testing"
)

// carrier07ba reconstructs mg-07ba as it stood on 2026-07-18..20: the gh-issue
// carrier for drellem2/pogo#89, status=done, stage: merge, with #89 still open
// upstream. This is a FIXTURE, not the live work item — the real mg-07ba is an
// active carrier under a human gate, and constructing this state in the live
// store would corrupt a carrier mid-workflow.
func carrier07ba() Carrier {
	return Carrier{
		ID:     "mg-07ba",
		Title:  "triage: should a deliberate 'pogo agent stop' suppress restart_on_crash respawn? (drellem2/pogo#89)",
		Status: "done",
		Stage:  "merge",
		Repo:   "drellem2/pogo",
		Number: 89,
	}
}

func lookupTable(t *testing.T, states map[string]IssueState) LookupFunc {
	t.Helper()
	return func(repo string, number int) (IssueState, error) {
		key := refKey(repo, number)
		st, ok := states[key]
		if !ok {
			t.Fatalf("test lookup called for unexpected ref %s", key)
		}
		if st == StateUnknown {
			return StateUnknown, errors.New("simulated lookup failure")
		}
		return st, nil
	}
}

func refKey(repo string, number int) string {
	c := Carrier{Repo: repo, Number: number}
	return c.String()
}

// THE POSITIVE CONTROL.
//
// This is the test the ticket demands, and the reason it is demanded: a
// detector for a silent failure that has only ever been observed passing has
// not been tested, it has been assumed. It replays the exact founding state —
// mg-07ba done/stage:merge, drellem2/pogo#89 open — and asserts the detector
// FIRES, naming both the carrier and the issue.
//
// Had this detector existed on 2026-07-17, this is the assertion that would
// have failed loudly instead of #89 sitting open for four days.
func TestDetectorFiresOnTheFoundingCase(t *testing.T) {
	c := carrier07ba()
	rep := Detect([]Carrier{c}, lookupTable(t, map[string]IssueState{
		"drellem2/pogo#89": StateOpen,
	}))

	if len(rep.Misses) != 1 {
		t.Fatalf("positive control FAILED TO FIRE: want 1 teardown miss, got %d (report: %+v)", len(rep.Misses), rep)
	}
	got := rep.Misses[0]
	if got.Carrier.ID != "mg-07ba" {
		t.Errorf("miss names carrier %q, want mg-07ba", got.Carrier.ID)
	}
	if got.Carrier.String() != "drellem2/pogo#89" {
		t.Errorf("miss names issue %q, want drellem2/pogo#89", got.Carrier.String())
	}
	if got.Kind != KindMiss {
		t.Errorf("kind = %q, want %q", got.Kind, KindMiss)
	}
	if !rep.Actionable() {
		t.Error("report with a teardown miss must be actionable")
	}

	// The report must name both, so a human reading only the alert can act.
	body := rep.Render()
	for _, want := range []string{"mg-07ba", "drellem2/pogo#89", "TEARDOWN MISS"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered report missing %q:\n%s", want, body)
		}
	}
	if subj := rep.MailSubject(); !strings.Contains(subj, "drellem2/pogo#89") {
		t.Errorf("mail subject %q does not name the issue", subj)
	}
}

// THE NEGATIVE CONTROL. A carrier whose issue is genuinely closed must stay
// silent. Without this, a detector that simply always fires would pass the
// positive control — and a detector that always fires gets muted, which is
// indistinguishable from having no detector at all.
func TestDetectorSilentWhenIssueIsClosed(t *testing.T) {
	c := carrier07ba()
	rep := Detect([]Carrier{c}, lookupTable(t, map[string]IssueState{
		"drellem2/pogo#89": StateClosed,
	}))

	if len(rep.Misses) != 0 {
		t.Fatalf("CRIED WOLF: closed issue produced %d miss(es): %+v", len(rep.Misses), rep.Misses)
	}
	if rep.Actionable() {
		t.Error("a clean scan must not be actionable")
	}
	if rep.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1 — a clean scan must still record that it looked", rep.Scanned)
	}
	if body := rep.Render(); !strings.Contains(body, "every issue confirmed closed") {
		t.Errorf("clean report should say so explicitly:\n%s", body)
	}
}

// A failed lookup must NOT read as "closed, teardown ran". This is the failure
// mode that would make the detector report the reassuring answer exactly when
// it has lost the ability to tell — the detector failing in the same silent way
// as the thing it detects.
func TestFailedLookupIsIndeterminateNotClean(t *testing.T) {
	c := carrier07ba()
	rep := Detect([]Carrier{c}, func(string, int) (IssueState, error) {
		return StateUnknown, errors.New("API rate limit exceeded")
	})

	if len(rep.Misses) != 0 {
		t.Errorf("an unknown state must not be reported as a confirmed miss")
	}
	if len(rep.Indeterminate) != 1 {
		t.Fatalf("want 1 indeterminate finding, got %d — a failed lookup was swallowed", len(rep.Indeterminate))
	}
	if !rep.Actionable() {
		t.Error("INDETERMINATE MUST BE ACTIONABLE: a detector that cannot see is itself the finding")
	}
	if d := rep.Indeterminate[0].Detail; !strings.Contains(d, "rate limit") {
		t.Errorf("detail %q loses the underlying cause", d)
	}
	if body := rep.Render(); !strings.Contains(body, "NOT clean") {
		t.Errorf("report must state indeterminate is not clean:\n%s", body)
	}
}

// A state string neither OPEN nor CLOSED (a future GitHub state, a garbled
// response) must land in Indeterminate rather than being optimistically
// treated as closed.
func TestUnrecognisedStateIsIndeterminate(t *testing.T) {
	rep := Detect([]Carrier{carrier07ba()}, func(string, int) (IssueState, error) {
		return IssueState("locked"), nil
	})
	if len(rep.Indeterminate) != 1 {
		t.Fatalf("unrecognised state must be indeterminate, got report %+v", rep)
	}
	if len(rep.Misses) != 0 {
		t.Error("unrecognised state must not be a confirmed miss")
	}
}

// mg-c155 / drellem2/pogo#88: done, but the issue is open ON PURPOSE — Daniel
// asked the reporter for a format-patch and closing would retract that ask.
// Without a way to say so, this fires on every run and trains a human to ignore
// the channel before the run that matters.
func TestDeclaredOpenIsReportedButNotAMiss(t *testing.T) {
	c := Carrier{
		ID: "mg-c155", Title: "triage: refinery submit-time UnlinkWorktree strands failing polecats",
		Status: "done", Stage: "build", Repo: "drellem2/pogo", Number: 88,
		DeclaredOpenReason: "waiting on reporter for a format-patch (Daniel's ask, 2026-07-20)",
	}
	rep := Detect([]Carrier{c}, lookupTable(t, map[string]IssueState{
		"drellem2/pogo#88": StateOpen,
	}))

	if len(rep.Misses) != 0 {
		t.Errorf("a declared-open carrier must not count as a teardown miss: %+v", rep.Misses)
	}
	if rep.Actionable() {
		t.Error("a declared-open carrier must not page anyone")
	}
	if len(rep.DeclaredOpen) != 1 {
		t.Fatalf("want the carrier still LISTED under declared-open, got %d", len(rep.DeclaredOpen))
	}
	// Suppression buys silence from the alert channel, not invisibility: a
	// declaration that outlives its reason is the same silent absence this
	// package exists to catch.
	body := rep.Render()
	if !strings.Contains(body, "mg-c155") || !strings.Contains(body, "format-patch") {
		t.Errorf("declared-open carrier must remain visible with its stated reason:\n%s", body)
	}
}

// The SAME carrier without the declaration is a miss. This pins the opt-out as
// the only thing separating the two, so suppression can never happen by
// accident or inference — a human has to write down why.
func TestSameCarrierWithoutDeclarationIsAMiss(t *testing.T) {
	c := Carrier{ID: "mg-c155", Status: "done", Repo: "drellem2/pogo", Number: 88}
	rep := Detect([]Carrier{c}, lookupTable(t, map[string]IssueState{
		"drellem2/pogo#88": StateOpen,
	}))
	if len(rep.Misses) != 1 {
		t.Fatalf("an un-annotated done carrier with an open issue must be a miss, got %+v", rep)
	}
}

// Carriers that have not claimed completion are not audited — and must not be
// counted as scanned, so "scanned N" stays an honest number.
func TestInFlightCarriersAreNotAudited(t *testing.T) {
	carriers := []Carrier{
		{ID: "mg-aaaa", Status: "claimed", Repo: "drellem2/pogo", Number: 1},
		{ID: "mg-bbbb", Status: "available", Repo: "drellem2/pogo", Number: 2},
	}
	rep := Detect(carriers, func(string, int) (IssueState, error) {
		t.Fatal("lookup must not be called for a carrier still in flight")
		return StateUnknown, nil
	})
	if rep.Scanned != 0 || rep.Actionable() {
		t.Errorf("in-flight carriers must be skipped entirely, got %+v", rep)
	}
}

// Repeated scans of an unchanged store must render identically; a report that
// reshuffles looks like it changed, and a human watching for change learns to
// stop reading it.
func TestReportOrderIsStable(t *testing.T) {
	carriers := []Carrier{
		{ID: "mg-fff0", Status: "done", Repo: "drellem2/pogo", Number: 3},
		{ID: "mg-000f", Status: "done", Repo: "drellem2/pogo", Number: 1},
		{ID: "mg-aaa5", Status: "done", Repo: "drellem2/pogo", Number: 2},
	}
	lookup := func(string, int) (IssueState, error) { return StateOpen, nil }

	first := Detect(carriers, lookup).Render()
	for i := 0; i < 3; i++ {
		if got := Detect(carriers, lookup).Render(); got != first {
			t.Fatalf("report is not stable across scans:\n--- first ---\n%s\n--- run %d ---\n%s", first, i, got)
		}
	}
	ids := Detect(carriers, lookup).Misses
	if ids[0].Carrier.ID != "mg-000f" || ids[2].Carrier.ID != "mg-fff0" {
		t.Errorf("misses not sorted by carrier id: %v", ids)
	}
}

// A carrier whose gh: ref never parsed still has to be visible. Dropping it
// would hide a carrier from the audit permanently — silence produced by the
// detector itself.
func TestUnresolvableRefIsIndeterminate(t *testing.T) {
	c := Carrier{ID: "mg-dead", Status: "done", Repo: "not-a-ref", Number: 0}
	rep := Detect([]Carrier{c}, func(repo string, number int) (IssueState, error) {
		return GHLookup(repo, number) // real lookup: rejects before any network call
	})
	if len(rep.Indeterminate) != 1 {
		t.Fatalf("an unresolvable ref must surface as indeterminate, got %+v", rep)
	}
}
