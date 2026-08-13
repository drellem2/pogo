package client

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
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
