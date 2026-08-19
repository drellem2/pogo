package client

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// fakeMG scripts the three mg invocations CloseMGWorkItemAtMerge makes — show,
// claim, done — and records every one of them in order, so a test can assert
// both the outcome and the fact that a step did or did not run.
type fakeMG struct {
	// show is the JSON `mg show --json` prints; empty makes the probe fail.
	show string
	// showAfterDone, when set, is what a SECOND `mg show` prints — the
	// classification probe that runs after a refused `mg done`.
	showAfterDone string
	claimFails    bool
	doneFails     bool
	// list is the NDJSON `mg list --all --json` prints when the close searches
	// the store for the successor a declares-remainder item never named.
	// listFails makes that search fail instead.
	list      string
	listFails bool

	shows int
	ran   []string
}

func (f *fakeMG) install(t *testing.T) {
	t.Helper()
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		f.ran = append(f.ran, strings.Join(append([]string{name}, args...), " "))
		switch {
		case len(args) > 0 && args[0] == "show":
			f.shows++
			out := f.show
			if f.shows > 1 && f.showAfterDone != "" {
				out = f.showAfterDone
			}
			if out == "" {
				return exec.Command("false")
			}
			return exec.Command("printf", "%s", out)
		case len(args) > 0 && args[0] == "list":
			if f.listFails {
				return exec.Command("false")
			}
			return exec.Command("printf", "%s", f.list)
		case len(args) > 0 && args[0] == "claim":
			if f.claimFails {
				return exec.Command("false")
			}
			return exec.Command("true")
		case len(args) > 0 && args[0] == "done":
			if f.doneFails {
				return exec.Command("false")
			}
			return exec.Command("true")
		}
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = old })
}

func (f *fakeMG) didRun(prefix string) bool {
	for _, c := range f.ran {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func item(status, assignee string) string {
	return `{"id":"mg-479c","status":"` + status + `","assignee":"` + assignee + `"}`
}

// declaredItem is an item that DECLARES A REMAINDER and names no successor —
// the state in which `mg done` refuses, and the state every declares-remainder
// polecat leaves its item in at merge (mg-27c0).
func declaredItem(status string) string {
	return `{"id":"mg-479c","status":"` + status + `","assignee":"","declares_remainder":true,"successor":[]}`
}

// child is one `mg list --all --json` line: an item naming pred as its
// predecessor, which is the edge `mg done --successor` writes on the far end.
func child(id, pred string) string {
	return `{"id":"` + id + `","status":"available","predecessor":["` + pred + `"]}`
}

// unrelated is an NDJSON line for an item that names nobody — the bulk of the
// store the resolver has to scan past.
func unrelated(id string) string {
	return `{"id":"` + id + `","status":"available","predecessor":[]}`
}

// THE mg-be37 CASE, MADE TO WORK. A hand-submitted branch's item is unclaimed,
// and `mg done` refuses an unclaimed item (exit 4) — so the close-at-merge path
// failed by construction in exactly the case it was added for. Claim, then done.
func TestCloseAtMergeClaimsAnUnclaimedDispatchableItemFirst(t *testing.T) {
	f := &fakeMG{show: item("available", "")}
	f.install(t)

	if err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`); err != nil {
		t.Fatalf("CloseMGWorkItemAtMerge: %v", err)
	}
	if !f.didRun("mg claim mg-479c --pid ") {
		t.Errorf("an unclaimed item must be claimed before `mg done`, or the close cannot apply: %v", f.ran)
	}
	if !f.didRun("mg done mg-479c") {
		t.Errorf("the close never ran: %v", f.ran)
	}
	// Order matters, not just presence.
	if len(f.ran) < 3 || !strings.HasPrefix(f.ran[1], "mg claim") || !strings.HasPrefix(f.ran[2], "mg done") {
		t.Errorf("expected show -> claim -> done, got %v", f.ran)
	}
}

// FIX DIRECTION 4. A gated item nobody holds is work somebody stopped on
// purpose. pogod is one `mg claim` away from being able to close it and
// declines — which is what actually happened to mg-479c, minus the lie.
func TestCloseAtMergeDeclinesAGatedUnclaimedItem(t *testing.T) {
	for _, assignee := range []string{"parked", "human", "blocked:mayor", " Parked "} {
		t.Run(assignee, func(t *testing.T) {
			f := &fakeMG{show: item("available", assignee)}
			f.install(t)

			err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
			if !errors.Is(err, ErrMGWorkItemGated) {
				t.Fatalf("expected a gated refusal, got %v", err)
			}
			if f.didRun("mg claim") || f.didRun("mg done") {
				t.Errorf("a declined close must not touch the item at all: %v", f.ran)
			}
			if !strings.Contains(err.Error(), strings.TrimSpace(assignee)) {
				t.Errorf("the refusal must name what gated it: %v", err)
			}
		})
	}
}

// A gated item that IS claimed has a worker on it, and that worker's merge is
// an ordinary completion. The gate is about unowned work, not about the
// assignee field on its own.
func TestCloseAtMergeClosesAClaimedGatedItem(t *testing.T) {
	f := &fakeMG{show: item("claimed", "parked")}
	f.install(t)

	if err := CloseMGWorkItemAtMerge("mg-479c", ""); err != nil {
		t.Fatalf("CloseMGWorkItemAtMerge: %v", err)
	}
	if f.didRun("mg claim") {
		t.Errorf("a claimed item must not be re-claimed: %v", f.ran)
	}
	if !f.didRun("mg done mg-479c") {
		t.Errorf("the close never ran: %v", f.ran)
	}
}

// An item that is already terminal is reported as closed — by somebody else —
// and never written to. `mg done` refuses a second done rather than overwriting
// the first, and the worker's own verdict is the better record.
func TestCloseAtMergeReportsAnAlreadyTerminalItemAsClosed(t *testing.T) {
	for _, status := range []string{"done", "archived"} {
		t.Run(status, func(t *testing.T) {
			f := &fakeMG{show: item(status, "")}
			f.install(t)

			err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
			if !errors.Is(err, ErrMGWorkItemAlreadyDone) {
				t.Fatalf("expected an already-done report, got %v", err)
			}
			if f.didRun("mg done") {
				t.Errorf("a terminal item must not be written to: %v", f.ran)
			}
		})
	}
}

// THE RACE, CLASSIFIED BY THE STORE AND NOT BY THE MESSAGE. The polecat closes
// its own item between the probe and the write. "already done" and "not
// claimed" are both exit 4, so only the store can tell them apart.
func TestCloseAtMergeAsksTheStoreWhenTheWriteIsRefused(t *testing.T) {
	f := &fakeMG{show: item("claimed", ""), doneFails: true, showAfterDone: item("done", "")}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if !errors.Is(err, ErrMGWorkItemAlreadyDone) {
		t.Fatalf("the item is done in the store, so the refusal means the worker won the race: %v", err)
	}
}

// The defect's own shape: the write is refused and the item is NOT closed. This
// must never be classifiable as a completion, because everything downstream —
// the filer's mail, pogod's summary line — is conditional on it.
func TestARefusedWriteAgainstAnOpenItemIsNotACompletion(t *testing.T) {
	f := &fakeMG{show: item("available", ""), claimFails: true, doneFails: true, showAfterDone: item("available", "")}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("a refused close must be reported as one")
	}
	if errors.Is(err, ErrMGWorkItemAlreadyDone) {
		t.Fatalf("the item is still available; nothing closed it: %v", err)
	}
	if !strings.Contains(err.Error(), "claiming it first also failed") {
		t.Errorf("a close that failed because the claim failed must say so — that is the actionable half: %v", err)
	}
}

// An unreadable store is not a licence to skip the close. The probe fails, the
// write is attempted anyway, and its own outcome is what gets reported.
func TestCloseAtMergeStillWritesWhenTheProbeFails(t *testing.T) {
	f := &fakeMG{show: ""}
	f.install(t)

	if err := CloseMGWorkItemAtMerge("mg-479c", ""); err != nil {
		t.Fatalf("CloseMGWorkItemAtMerge: %v", err)
	}
	if !f.didRun("mg done mg-479c") {
		t.Errorf("the close must be attempted even when the item could not be read: %v", f.ran)
	}
}

// THE REMEDY IS SUBJECT TO THE DEFECT IT REMEDIES. Claiming an item in order to
// close it, and then failing to close it, leaves the item in claimed/ under
// pogod's pid with no worker — invisible to dispatch and to stall-watch, which
// is strictly worse than the open item this started with. The claim is rolled
// back, and only the claim this call took.
func TestAClaimTakenOnlyToCloseIsReleasedWhenTheCloseFails(t *testing.T) {
	f := &fakeMG{show: item("available", ""), doneFails: true, showAfterDone: item("claimed", "")}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the close failed and must be reported as failed")
	}
	if !f.didRun("mg unclaim mg-479c") {
		t.Fatalf("the claim taken to close the item was not released; it is stranded in claimed/: %v", f.ran)
	}
	if !strings.Contains(err.Error(), "back in available/") {
		t.Errorf("the report must say the item is where it started: %v", err)
	}
}

// A claim this call did NOT take is not ours to release — releasing it is how a
// live worker loses its item mid-flight.
func TestAClaimSomebodyElseHoldsIsNotReleased(t *testing.T) {
	f := &fakeMG{show: item("claimed", ""), doneFails: true, showAfterDone: item("claimed", "")}
	f.install(t)

	if err := CloseMGWorkItemAtMerge("mg-479c", ""); err == nil {
		t.Fatal("the close failed and must be reported as failed")
	}
	if f.didRun("mg unclaim") {
		t.Errorf("pogod released a claim it never took: %v", f.ran)
	}
}

// A rollback that itself fails is the one case that leaves real residue, so it
// says so in the terms an operator can act on rather than reporting the tidy
// outcome it did not achieve.
func TestAFailedRollbackIsReportedAsStranded(t *testing.T) {
	f := &fakeMG{show: item("available", ""), doneFails: true, showAfterDone: item("claimed", "")}
	f.install(t)
	// Make only the unclaim fail, by wrapping the installed fake.
	inner := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "unclaim" {
			f.ran = append(f.ran, "mg unclaim "+strings.Join(args[1:], " "))
			return exec.Command("false")
		}
		return inner(name, args...)
	}

	err := CloseMGWorkItemAtMerge("mg-479c", "")
	if err == nil {
		t.Fatal("the close failed and must be reported as failed")
	}
	if !strings.Contains(err.Error(), "STRANDED") || !strings.Contains(err.Error(), "mg unclaim mg-479c") {
		t.Errorf("a failed rollback must name the residue and the command that clears it: %v", err)
	}
}

// THE mg-27c0 CASE. Four declares-remainder items merged on 2026-08-13 and all
// four bounced back to available/ carrying a successor that already existed:
// the WORKER files the child before it submits and then exits, and POGOD does
// the close at merge time without ever being told the id. Neither half is
// wrong; the id is simply in the store and nobody looks it up.
func TestCloseAtMergeResolvesTheSuccessorTheAuthorAlreadyFiled(t *testing.T) {
	f := &fakeMG{
		show: declaredItem("claimed"),
		list: strings.Join([]string{unrelated("mg-1111"), child("mg-d928", "mg-479c"), unrelated("mg-2222")}, "\n"),
	}
	f.install(t)

	if err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`); err != nil {
		t.Fatalf("CloseMGWorkItemAtMerge: %v", err)
	}
	if !f.didRun("mg list --all --json") {
		t.Errorf("the store was never searched for the successor: %v", f.ran)
	}
	var done string
	for _, c := range f.ran {
		if strings.HasPrefix(c, "mg done") {
			done = c
		}
	}
	if !strings.Contains(done, "--successor=mg-d928") {
		t.Errorf("mg done = %q, want it to carry --successor=mg-d928 resolved from the store", done)
	}
}

// THE RESOLUTION IS RECORDED, because a link pogod inferred and one the author
// stated leave the store looking identical and only the inferred half can be
// wrong.
func TestAResolvedSuccessorIsRecordedInTheResultSidecar(t *testing.T) {
	f := &fakeMG{show: declaredItem("claimed"), list: child("mg-d928", "mg-479c")}
	f.install(t)

	if err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b","completed_by":"refinery"}`); err != nil {
		t.Fatalf("CloseMGWorkItemAtMerge: %v", err)
	}
	var done string
	for _, c := range f.ran {
		if strings.HasPrefix(c, "mg done") {
			done = c
		}
	}
	for _, want := range []string{`"successor_resolved_by":"refinery"`, `"successor_resolved":"mg-d928"`, `"branch":"b"`} {
		if !strings.Contains(done, want) {
			t.Errorf("mg done = %q, want the result to carry %s", done, want)
		}
	}
}

// A PREDECESSOR EDGE IS NOT PROOF OF SUCCESSION, and this is not hypothetical:
// measured over the live store on 2026-08-14, 10 of 41 items named as a
// predecessor are named by TWO children, and the parent's own successor field
// picks exactly one of the two. A resolver that took the first match would be
// wrong about half the time in a quarter of the population, so it declines and
// says what it was choosing between.
func TestCloseAtMergeRefusesToGuessBetweenTwoCandidates(t *testing.T) {
	f := &fakeMG{
		show: declaredItem("claimed"),
		list: strings.Join([]string{child("mg-365a", "mg-479c"), child("mg-c15e", "mg-479c")}, "\n"),
		// mg refuses the close, exactly as it did before this fix.
		doneFails:     true,
		showAfterDone: declaredItem("claimed"),
	}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the item is not closed and must not be reported as closed")
	}
	if !errors.Is(err, ErrMGRemainderAmbiguousSuccessor) {
		t.Errorf("err = %v, want it to carry ErrMGRemainderAmbiguousSuccessor", err)
	}
	if errors.Is(err, ErrMGRemainderNoSuccessorFiled) {
		t.Errorf("an ambiguous close must NOT read as one where nothing was filed: %v", err)
	}
	for _, want := range []string{"mg-365a", "mg-c15e"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name candidate %s so a human can pick", err, want)
		}
	}
	for _, c := range f.ran {
		if strings.HasPrefix(c, "mg done") && strings.Contains(c, "--successor") {
			t.Errorf("pogod guessed a successor: %q", c)
		}
	}
}

// THE OTHER CAUSE, KEPT DISTINGUISHABLE. mg-5058 produced the identical visible
// symptom — item back in available/, exit 4 — from the opposite cause: its
// worker filed no successor at all. Before mg-27c0 the only way to tell the two
// apart was reading the result sidecar by hand.
func TestCloseAtMergeSaysWhenNoSuccessorWasEverFiled(t *testing.T) {
	f := &fakeMG{
		show:          declaredItem("claimed"),
		list:          strings.Join([]string{unrelated("mg-1111"), child("mg-9999", "mg-other")}, "\n"),
		doneFails:     true,
		showAfterDone: declaredItem("claimed"),
	}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the item is not closed and must not be reported as closed")
	}
	if !errors.Is(err, ErrMGRemainderNoSuccessorFiled) {
		t.Errorf("err = %v, want it to carry ErrMGRemainderNoSuccessorFiled", err)
	}
	if errors.Is(err, ErrMGRemainderAmbiguousSuccessor) {
		t.Errorf("a never-filed successor must NOT read as an ambiguous one: %v", err)
	}
	if !strings.Contains(err.Error(), "missing work") {
		t.Errorf("err = %v, want it to say the remainder is untracked rather than merely unlinked", err)
	}
}

// THE REFUSAL IS NOT WEAKENED. Nothing in the resolution path closes an item mg
// declined to close: when mg refuses and the store agrees the item is still
// open, the close is reported as failed however the successor search went.
func TestResolutionNeverTurnsAnMGRefusalIntoASuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		list string
	}{
		{"one candidate", child("mg-d928", "mg-479c")},
		{"no candidates", unrelated("mg-1111")},
		{"two candidates", strings.Join([]string{child("mg-a", "mg-479c"), child("mg-b", "mg-479c")}, "\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMG{show: declaredItem("claimed"), list: tc.list, doneFails: true, showAfterDone: declaredItem("claimed")}
			f.install(t)
			if err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`); err == nil {
				t.Fatal("mg refused the close; the item is still open and must be reported that way")
			}
		})
	}
}

// The search runs for exactly the items whose close would otherwise be refused
// for want of one. An ordinary item is closed with no store scan at all.
func TestTheSuccessorSearchRunsOnlyForADeclaredItemThatNamesNone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		show     string
		wantList bool
	}{
		{"declares nothing", item("claimed", ""), false},
		{"declares and already names one",
			`{"id":"mg-479c","status":"claimed","declares_remainder":true,"successor":["mg-d928"]}`, false},
		{"declares and names none", declaredItem("claimed"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMG{show: tc.show, list: child("mg-d928", "mg-479c")}
			f.install(t)
			if err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`); err != nil {
				t.Fatalf("CloseMGWorkItemAtMerge: %v", err)
			}
			if got := f.didRun("mg list"); got != tc.wantList {
				t.Errorf("searched the store = %v, want %v: %v", got, tc.wantList, f.ran)
			}
		})
	}
}

// An item never resolves to ITSELF. mg refuses a self-successor, so a resolver
// that offered one would convert a fixable refusal into a confusing one — and a
// self-edge would also mask a genuine second candidate.
func TestTheItemIsNeverItsOwnSuccessor(t *testing.T) {
	f := &fakeMG{
		show:          declaredItem("claimed"),
		list:          `{"id":"mg-479c","status":"claimed","predecessor":["mg-479c"]}`,
		doneFails:     true,
		showAfterDone: declaredItem("claimed"),
	}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil || !errors.Is(err, ErrMGRemainderNoSuccessorFiled) {
		t.Fatalf("err = %v, want the self-edge ignored and the close reported as nothing-filed", err)
	}
}

// A STORE THAT CANNOT BE SEARCHED IS NOT A LICENCE TO GUESS OR TO SKIP. The
// close still runs, mg still refuses it, and the report says the search failed
// rather than asserting either cause.
func TestAFailedSuccessorSearchIsReportedAndNotGuessedAround(t *testing.T) {
	f := &fakeMG{show: declaredItem("claimed"), listFails: true, doneFails: true, showAfterDone: declaredItem("claimed")}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the item is not closed and must not be reported as closed")
	}
	if errors.Is(err, ErrMGRemainderNoSuccessorFiled) || errors.Is(err, ErrMGRemainderAmbiguousSuccessor) {
		t.Errorf("an unsearchable store must not be reported as either cause: %v", err)
	}
	if !strings.Contains(err.Error(), "could not be searched") {
		t.Errorf("err = %v, want it to say the search itself failed", err)
	}
	if !f.didRun("mg done") {
		t.Errorf("the close was skipped rather than attempted: %v", f.ran)
	}
}

// A DROPPED LINE CAN ONLY EVER TURN AN AMBIGUITY INTO A FALSE RESOLUTION, so an
// unparseable one fails the whole lookup instead of quietly shrinking the
// candidate set.
func TestAnUnparseableListLineFailsTheLookupRatherThanShrinkingIt(t *testing.T) {
	f := &fakeMG{
		show:          declaredItem("claimed"),
		list:          strings.Join([]string{child("mg-d928", "mg-479c"), "not json at all"}, "\n"),
		doneFails:     true,
		showAfterDone: declaredItem("claimed"),
	}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the item is not closed and must not be reported as closed")
	}
	if !strings.Contains(err.Error(), "could not be searched") {
		t.Errorf("err = %v, want the lookup reported as failed", err)
	}
	for _, c := range f.ran {
		if strings.HasPrefix(c, "mg done") && strings.Contains(c, "--successor") {
			t.Errorf("a successor was resolved from a partial scan: %q", c)
		}
	}
}

// The provenance note must never cost the payload it annotates.
func TestWithResolvedSuccessorLeavesAPayloadItCannotAnnotateAlone(t *testing.T) {
	for _, in := range []string{"", "   ", "not json", `["an","array"]`, "null"} {
		if got := withResolvedSuccessor(in, "mg-d928"); got != in {
			t.Errorf("withResolvedSuccessor(%q) = %q, want it returned untouched", in, got)
		}
	}
}

// childAt is a `mg list --json` line with a creation stamp, so a test can assert
// the order an ambiguous refusal presents its candidates in.
func childAt(id, pred, created string) string {
	return `{"id":"` + id + `","status":"available","created":"` + created + `","predecessor":["` + pred + `"]}`
}

// The candidates are ordered NEWEST FIRST and the refusal says why that ordering
// is only a hint. Measured over the live store on 2026-08-14, the parent's own
// successor field named the most recently created child in 10 of 10 ambiguous
// cases — but all 10 are one workflow's chains from one night, so the code
// orders and reports rather than picking.
func TestAnAmbiguousRefusalOrdersCandidatesNewestFirstWithoutPicking(t *testing.T) {
	f := &fakeMG{
		show: declaredItem("claimed"),
		list: strings.Join([]string{
			childAt("mg-c15e", "mg-479c", "2026-08-13T23:12:35Z"),
			childAt("mg-365a", "mg-479c", "2026-08-13T23:26:15Z"),
		}, "\n"),
		doneFails:     true,
		showAfterDone: declaredItem("claimed"),
	}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the item is not closed and must not be reported as closed")
	}
	msg := err.Error()
	newest, older := strings.Index(msg, "mg-365a"), strings.Index(msg, "mg-c15e")
	if newest < 0 || older < 0 || newest > older {
		t.Errorf("candidates must be listed newest first; got %q", msg)
	}
	if !strings.Contains(msg, "2026-08-13T23:26:15Z") {
		t.Errorf("the ordering must show the stamps that produced it: %q", msg)
	}
	if !strings.Contains(msg, "not a rule this code applies") {
		t.Errorf("the hint must say it is a hint: %q", msg)
	}
	for _, c := range f.ran {
		if strings.HasPrefix(c, "mg done") && strings.Contains(c, "--successor") {
			t.Errorf("the ordering was used as a tiebreak: %q", c)
		}
	}
}

// A candidate with no creation stamp is listed, not dropped: the ordering is a
// convenience for a reader and must never decide who is reported.
func TestACandidateWithNoCreationStampIsStillReported(t *testing.T) {
	f := &fakeMG{
		show: declaredItem("claimed"),
		list: strings.Join([]string{
			childAt("mg-365a", "mg-479c", "2026-08-13T23:26:15Z"),
			child("mg-c15e", "mg-479c"),
		}, "\n"),
		doneFails:     true,
		showAfterDone: declaredItem("claimed"),
	}
	f.install(t)

	err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
	if err == nil {
		t.Fatal("the item is not closed and must not be reported as closed")
	}
	if !strings.Contains(err.Error(), "mg-c15e (created unknown)") {
		t.Errorf("a stampless candidate must still be named: %v", err)
	}
	if !errors.Is(err, ErrMGRemainderAmbiguousSuccessor) {
		t.Errorf("two candidates are still two candidates: %v", err)
	}
}

// THE PROPERTY THE MERGED-NOT-CLOSED EXCLUSION RESTS ON (mg-f17c).
//
// cmd/pogod/reap.go suppresses the merged-not-closed coordinator alert for a
// close that returned ErrMGWorkItemGated, on the grounds that a gated item
// cannot be dispatched — so the hazard the alert warns about (a worker sent at
// work already on the target) cannot arise. That reasoning is only sound if the
// population this error names is EXACTLY the population dispatch refuses. Two
// lists that happen to agree today would make it an accident.
//
// They cannot disagree, and this pins why: the refusal above is produced by
// config.IsDispatchGated, which is the same predicate
// internal/agent.MGDispatchGate.DispatchGated refuses on and the same one
// internal/stallwatch.watchedForDispatch excludes on. One function, three
// callers. This test asserts the IFF against the predicate itself rather than
// against a literal list, so a gate value added to the vocabulary next year is
// covered without anyone remembering this file — and a local list grown here
// instead fails immediately.
//
// The `claimed` half is not decoration. The gate is about UNOWNED work: a gated
// item somebody holds has a worker on it and its merge is an ordinary
// completion, so the error must NOT appear there even though the predicate
// says gated. That asymmetry is what stops this test from passing by asserting
// the predicate against itself.
//
// A relative assertion still has one vacuous reading — a predicate that answered
// false for everything would satisfy the iff and gate nothing. That half is held
// down on the other side, by config.TestIsDispatchGatedCoversEveryDefault, which
// asserts the vocabulary absolutely. Neither test is sufficient alone and they
// are named here so the pair is discoverable rather than coincidental.
func TestTheGatedRefusalIsExactlyTheDispatchGatedPopulation(t *testing.T) {
	// A population spanning both gating rules (the sentinel vocabulary and the
	// `blocked:<agent>` shape), their case/whitespace variants, and the
	// dispatchable values that must NOT be caught by either.
	for _, assignee := range []string{
		"parked", "human", "blocked:mayor", " Parked ", "HUMAN", "blocked: pm-pogo",
		"", "mayor", "pm-pogo", "p479c", "blocked", "parked-later",
	} {
		t.Run("assignee="+assignee, func(t *testing.T) {
			wantGated := config.IsDispatchGated(assignee, nil)

			f := &fakeMG{show: item("available", assignee)}
			f.install(t)
			err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`)
			if got := errors.Is(err, ErrMGWorkItemGated); got != wantGated {
				t.Fatalf("unclaimed assignee %q: gated refusal = %t, but config.IsDispatchGated says %t — "+
					"the merged-not-closed exclusion in cmd/pogod/reap.go suppresses its alert on the "+
					"strength of these two agreeing (err = %v)", assignee, got, wantGated, err)
			}

			// Held work is never this refusal, whatever the assignee says.
			g := &fakeMG{show: item("claimed", assignee)}
			g.install(t)
			if err := CloseMGWorkItemAtMerge("mg-479c", `{"branch":"b"}`); errors.Is(err, ErrMGWorkItemGated) {
				t.Errorf("assignee %q is CLAIMED — a worker holds it and its merge is an ordinary "+
					"completion, but the close was declined as gated: %v", assignee, err)
			}
		})
	}
}
