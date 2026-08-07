package agent

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// Tests for the stranded spawn-time claim (mg-790f). See spawnclaimadopt.go for
// the two failures these are about: one spawn left the item in claimed/ under
// pogod's own pid with no worker on it, the other left it unclaimed, and from
// outside they were the same event.
//
// mgNewStrandedUnderPogod, below, is the fixture that matters. It builds the
// mg-6f5e state EXACTLY: a claim file at work/claimed/<id>.md.<our pid>, no
// agent, no dispatch in flight. Under a test binary os.Getpid() is what
// MGWorkItemClaimer stamps, so "our pid" here is "pogod's pid" there — the
// fixture is the real state, not a stand-in for it.

// mgNewStrandedUnderPogod creates a work item and strands it in claimed/ under
// the given pid with nothing running. Pass os.Getpid() for the state a dispatch
// of this daemon's own left behind; pass anything else for a claim this daemon
// must not touch.
func mgNewStrandedUnderPogod(t *testing.T, root, title string, pid int) string {
	t.Helper()
	id := mgNewAvailable(t, root, title)
	out, err := exec.Command("mg", "--root", root, "claim", id,
		"--pid", strconv.Itoa(pid)).CombinedOutput()
	if err != nil {
		t.Fatalf("mg claim %s --pid %d: %v: %s", id, pid, err, out)
	}
	if got := mgStatus(t, root, id); got != "claimed" {
		t.Fatalf("fixture %s is in %q, want claimed — the state under test is a claim "+
			"nothing owns, so there has to be a claim", id, got)
	}
	if got := mgClaimFilePID(t, root, id); got != pid {
		t.Fatalf("fixture claim on %s names pid %d, want %d", id, got, pid)
	}
	return id
}

// mgClaimFilePID reads the pid off the claim file for id. It goes to the file
// rather than to `mg show` on purpose: the pid IS the discriminator the
// stranded-claim check turns on, and a test that read it back through the same
// prose the production code refuses to parse would be pinning nothing.
func mgClaimFilePID(t *testing.T, root, id string) int {
	t.Helper()
	pid, held, err := claimPID(root, id)
	if err != nil {
		t.Fatalf("read claim pid for %s: %v", id, err)
	}
	if !held {
		t.Fatalf("no claim file for %s in %s", id, filepath.Join(root, "work", "claimed"))
	}
	return pid
}

// mgClaimFile returns the full path of the claim file for id, or "" if none.
func mgClaimFile(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "work", "claimed")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id+".md") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// TestStrandedSpawnClaimIsAdopted is the headline guard. The mg-6f5e state —
// item in claimed/ under pogod's pid, no agent anywhere, `pogo agent list` empty
// — must not refuse the retry forever. It is residue, and a dispatch takes it
// over.
//
// The observed cost of NOT doing this is on the ticket: an item nothing can
// dispatch does not fail loudly, it just never gets worked, and the queue grows.
// Mayor is the only agent that dispatches, so there is no second party to notice.
func TestStrandedSpawnClaimIsAdopted(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewStrandedUnderPogod(t, root, "work a dead spawn stranded", os.Getpid())

	reg := newClaimTestRegistry(t, root)
	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-adopt", Id: id, Template: claimTestTemplate(t),
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("dispatch onto a claim pogod stranded: status = %d, want 201 — the item is "+
			"undispatchable forever and nothing reports it. body=%s", rr.Code, rr.Body.String())
	}
	if a := reg.Get("cat-adopt"); a == nil {
		t.Fatal("adopted the claim but registered no agent")
	}
	if got := mgStatus(t, root, id); got != "claimed" {
		t.Errorf("work item %s is in %q after adoption, want claimed — a live polecat's item "+
			"must not be visible to a second dispatch", id, got)
	}
}

// TestStrandedClaimWithoutTheHolderCheckIsRefusedForever IS THE POSITIVE
// CONTROL. It runs the same fixture against a claimer that reports the conflict
// but not the holder — which is pogod exactly as it was, since ClaimVerdict
// carried no HolderPID at all before this change — and asserts the harm ARISES:
// a permanent 409 on an item no one owns.
//
// Without it, the test above would still pass if adoption fired for some
// incidental reason, or if the fixture drifted into leaving the item available,
// and nothing would say so.
func TestStrandedClaimWithoutTheHolderCheckIsRefusedForever(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewStrandedUnderPogod(t, root, "work no pre-fix dispatch can reach", os.Getpid())

	reg := newClaimTestRegistry(t, root)
	reg.SetWorkItemClaimer(holderBlindClaimer{root: root}) // pogod before mg-790f

	for attempt := 1; attempt <= 2; attempt++ {
		rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
			Name: "cat-blind", Id: id, Template: claimTestTemplate(t),
		})
		if rr.Code != http.StatusConflict {
			t.Fatalf("control did not reproduce the defect on attempt %d: status = %d, want 409. "+
				"Something other than the holder check is letting the dispatch through, so the "+
				"guard test above proves nothing. body=%s", attempt, rr.Code, rr.Body.String())
		}
	}
	if mgDispatchable(t, root, id) {
		t.Error("control did not reproduce the defect: the item is dispatchable, so retrying " +
			"would have worked on its own and there is nothing to fix")
	}
}

// holderBlindClaimer is pogod BEFORE this change: it detects the conflict and
// cannot say who holds the claim. Not a convenience stub — it is the pre-fix
// verdict, which had no HolderPID field to fill.
type holderBlindClaimer struct{ root string }

func (c holderBlindClaimer) ClaimForSpawn(id string) ClaimVerdict {
	v := (MGWorkItemClaimer{Root: c.root}).ClaimForSpawn(id)
	v.HolderPID = 0
	return v
}

// TestAdoptionRefusesEveryClaimItCannotProveIsStranded pins the three conditions
// the check requires. Each subtest is a way of being wrong that would be worse
// than the bug: stealing a human's claim, dispatching twice onto one item, or
// adopting the claim of a spawn that is merely slow.
func TestAdoptionRefusesEveryClaimItCannotProveIsStranded(t *testing.T) {
	testsandbox.Isolate(t)

	t.Run("claim held by a pid that is not pogod's", func(t *testing.T) {
		root := mgSandbox(t)
		// A human's `mg claim`, or an older daemon's. Indistinguishable from here,
		// and stealing either is worse than making an operator run `mg unclaim`.
		id := mgNewStrandedUnderPogod(t, root, "somebody else's claim", os.Getpid()+1)
		before := mgClaimFile(t, root, id)

		reg := newClaimTestRegistry(t, root)
		rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
			Name: "cat-foreign", Id: id, Template: claimTestTemplate(t),
		})
		if rr.Code != http.StatusConflict {
			t.Fatalf("dispatch onto a foreign claim: status = %d, want 409; body=%s",
				rr.Code, rr.Body.String())
		}
		if got := mgClaimFile(t, root, id); got != before {
			t.Errorf("the refused dispatch moved the claim file: %q -> %q", before, got)
		}
	})

	t.Run("claim held for a live polecat that has not re-stamped", func(t *testing.T) {
		root := mgSandbox(t)
		id := mgNewAvailable(t, root, "one item, two dispatches")
		tmpl := claimTestTemplate(t)
		reg := newClaimTestRegistry(t, root)

		if rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
			Name: "cat-live", Id: id, Template: tmpl,
		}); rr.Code != http.StatusCreated {
			t.Fatalf("first dispatch: status = %d, body=%s", rr.Code, rr.Body.String())
		}
		// The claim is pogod's pid and no dispatch is in flight any more — the
		// first two conditions hold. Only the live agent stops adoption here, which
		// is why mg-7254's guard would have been silently repealed without it.
		rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
			Name: "cat-second", Id: id, Template: tmpl,
		})
		if rr.Code != http.StatusConflict {
			t.Fatalf("second dispatch onto a live polecat's item: status = %d, want 409 — "+
				"two polecats on one item means duplicated branches and a merge race; body=%s",
				rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "cat-live") {
			t.Errorf("the refusal does not name the live polecat holding the item: %q", body)
		}
	})

	t.Run("claim held by a dispatch still in flight", func(t *testing.T) {
		root := mgSandbox(t)
		id := mgNewStrandedUnderPogod(t, root, "a spawn that has not returned", os.Getpid())

		reg := newClaimTestRegistry(t, root)
		// The state a wedged spawn leaves inside pogod: claim taken, no agent yet,
		// handler still running. Driven directly because the point is the LEDGER —
		// a real wedge would need a spawn that never returns, which is a hang, not
		// a test.
		reg.beginSpawnClaim(id, "cat-wedged")

		rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
			Name: "cat-racer", Id: id, Template: claimTestTemplate(t),
		})
		if rr.Code != http.StatusConflict {
			t.Fatalf("dispatch onto an in-flight claim: status = %d, want 409 — adopting it "+
				"rebuilds the double dispatch on top of the fix for it; body=%s",
				rr.Code, rr.Body.String())
		}
		// The sentence that would have ended the investigation in one command.
		body := rr.Body.String()
		for _, want := range []string{"cat-wedged", "has not returned"} {
			if !strings.Contains(body, want) {
				t.Errorf("the refusal does not report the wedged dispatch (%q missing); a bare "+
					"pid is what cost half an hour: %q", want, body)
			}
		}
		// And once that dispatch finishes, the same item is adoptable.
		reg.endSpawnClaim(id)
		if rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
			Name: "cat-after", Id: id, Template: claimTestTemplate(t),
		}); rr.Code != http.StatusCreated {
			t.Fatalf("dispatch after the in-flight window closed: status = %d, want 201; body=%s",
				rr.Code, rr.Body.String())
		}
	})
}

// TestAdoptionNeverPassesTheItemThroughAvailable pins that adoption is a no-op
// on disk. The claim file is already what a fresh claim-at-spawn writes, so the
// item is in claimed/ before and after with no state in between — the property
// that keeps mg-7254's duplicate-dispatch guarantee. An implementation that
// unclaimed and re-claimed would pass every other test in this file and reopen
// the window; this one fails it, because the claim file's identity would change.
func TestAdoptionNeverPassesTheItemThroughAvailable(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewStrandedUnderPogod(t, root, "adopted in place", os.Getpid())
	before := mgClaimFile(t, root, id)
	beforeInfo, err := os.Stat(before)
	if err != nil {
		t.Fatalf("stat claim file: %v", err)
	}

	reg := newClaimTestRegistry(t, root)
	if rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-inplace", Id: id, Template: claimTestTemplate(t),
	}); rr.Code != http.StatusCreated {
		t.Fatalf("dispatch: status = %d, body=%s", rr.Code, rr.Body.String())
	}

	after := mgClaimFile(t, root, id)
	if after != before {
		t.Fatalf("adoption moved the claim file (%q -> %q); if it went through available/, a "+
			"concurrent dispatch could have taken the item", before, after)
	}
	afterInfo, err := os.Stat(after)
	if err != nil {
		t.Fatalf("stat claim file after adoption: %v", err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Error("the claim file was recreated rather than left alone; adoption must write nothing")
	}
}

// TestAdoptedClaimIsReleasedWhenTheSpawnAlsoFails closes the loop that would
// otherwise make a strand permanent. Adoption inherits the release obligation
// with the claim: a spawn that fails after adopting must hand the item back,
// exactly as one that fails after claiming fresh does.
func TestAdoptedClaimIsReleasedWhenTheSpawnAlsoFails(t *testing.T) {
	testsandbox.Isolate(t)
	root := mgSandbox(t)
	id := mgNewStrandedUnderPogod(t, root, "stranded twice over", os.Getpid())

	reg := newClaimTestRegistry(t, root)
	reg.SetCommandConfig(missingBinCommandConfig{})

	rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
		Name: "cat-doomed-adopt", Id: id, Template: claimTestTemplate(t),
	})
	if rr.Code == http.StatusCreated {
		t.Fatalf("spawn unexpectedly SUCCEEDED with a nonexistent binary; this test needs a "+
			"failing spawn to be meaningful. body=%s", rr.Body.String())
	}
	if got := mgStatus(t, root, id); got != "available" {
		t.Errorf("work item %s is in %q after a failed spawn that ADOPTED the claim, want "+
			"available — an adopted claim that is not released is a strand the next dispatch "+
			"adopts in turn, forever", id, got)
	}
	if !mgDispatchable(t, root, id) {
		t.Errorf("work item %s is not dispatchable after the failed spawn released its "+
			"adopted claim", id)
	}
}

// TestMgShowNeverReportsAvailableForAnItemDispatchRefuses is the ticket's
// acceptance criterion, stated as it was written: "`mg show` must not report
// `available` for an item that a dispatch cannot claim. That disagreement is the
// part that cost the time."
//
// It is asserted over every shape of claim refusal rather than over the one that
// was observed, because the cost was not the specific state — it was an operator
// holding two readings that could not both be true. A refusal on an item mg calls
// available is that state, whatever produced it.
func TestMgShowNeverReportsAvailableForAnItemDispatchRefuses(t *testing.T) {
	testsandbox.Isolate(t)
	tmpl := claimTestTemplate(t)

	cases := []struct {
		name  string
		setup func(t *testing.T, root string, reg *Registry) string
	}{
		{
			name: "claim held by another process",
			setup: func(t *testing.T, root string, reg *Registry) string {
				return mgNewStrandedUnderPogod(t, root, "held elsewhere", os.Getpid()+1)
			},
		},
		{
			name: "claim held for a live polecat",
			setup: func(t *testing.T, root string, reg *Registry) string {
				id := mgNewAvailable(t, root, "held by a worker")
				if rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
					Name: "cat-holder", Id: id, Template: tmpl,
				}); rr.Code != http.StatusCreated {
					t.Fatalf("setup dispatch: status = %d, body=%s", rr.Code, rr.Body.String())
				}
				return id
			},
		},
		{
			name: "claim held by a dispatch in flight",
			setup: func(t *testing.T, root string, reg *Registry) string {
				id := mgNewStrandedUnderPogod(t, root, "held mid-dispatch", os.Getpid())
				reg.beginSpawnClaim(id, "cat-inflight")
				return id
			},
		},
		{
			name: "item already done",
			setup: func(t *testing.T, root string, reg *Registry) string {
				id := mgNewClaimed(t, root, "already finished")
				if out, err := exec.Command("mg", "--root", root, "done", id).CombinedOutput(); err != nil {
					t.Fatalf("mg done %s: %v: %s", id, err, out)
				}
				return id
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := mgSandbox(t)
			reg := newClaimTestRegistry(t, root)
			id := tc.setup(t, root, reg)

			rr := spawnPolecatRaw(t, reg, SpawnPolecatAPIRequest{
				Name: "cat-refused", Id: id, Template: tmpl,
			})
			if rr.Code != http.StatusConflict {
				t.Skipf("this case did not refuse (status %d), so there is no disagreement to "+
					"assert about", rr.Code)
			}
			if got := mgStatus(t, root, id); got == "available" {
				t.Errorf("dispatch refused %s with 409 while the store reports it available. "+
					"These are the two commands an operator diagnoses with, and they disagree — "+
					"which is the half hour this ticket is about. refusal=%q", id, rr.Body.String())
			}
			if mgDispatchable(t, root, id) {
				t.Errorf("`mg list --status=available` offers %s while a dispatch onto it is "+
					"refused; the item reads as unstarted work anyone may pick up", id)
			}
		})
	}
}
