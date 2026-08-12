package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/testtmp"
)

// ClaimReleaser releases the macguffin claim a polecat holds on its work item,
// returning the item to available/ so dispatch can hand it to someone else.
//
// This exists because stopping a polecat used to leak the claim (mg-fb13).
// Registry.Stop had no claim-release path at all: the claim was released only
// by the polecat's own `mg done`/`mg unclaim`, so a polecat stopped MID-FLIGHT
// left work/claimed/<id>.md.<pid> behind under a pid that no longer existed.
// Claimed items are never dispatched, and the stall watcher only scans
// available/ (internal/stallwatch), so the stranded item was invisible to every
// recovery path — a silent failure whose only symptom is absence.
type ClaimReleaser interface {
	// ReleaseClaim releases the claim on workItemID. The bool reports whether a
	// held claim was actually released, so a caller can tell a real release from
	// a no-op.
	//
	// An item that is NOT claimed is not an error — it returns (false, nil).
	// That is the normal completion route: pogod's merge reaper records `mg done`
	// and then stops the polecat (gh #35), so by the time Stop runs the item has
	// usually already left claimed/. Errors are reserved for a claim that is held
	// and could not be released, which is the case worth paging about.
	ReleaseClaim(workItemID string) (bool, error)
}

// MGClaimReleaser is the production ClaimReleaser: it shells out to `mg unclaim`.
//
// `mg unclaim` deliberately ignores the recorded claimant pid — the pid in the
// claim filename is often the short-lived `mg claim` subprocess rather than the
// owning agent, so releasing by pid can release a live worker's claim. Targeting
// is therefore by work-item id, which is exactly the scope we want: the id of the
// polecat being stopped, and nothing else. This is a targeted release, never a
// sweep of whatever else happens to be sitting in claimed/.
type MGClaimReleaser struct {
	// Root overrides the macguffin store location, passed to mg as --root.
	// Empty resolves via storeRoot, which under a test binary is a throwaway
	// temp store rather than the live one.
	Root string
	// Bin is the mg binary. Empty means "mg" on PATH.
	Bin string
}

// testStoreRoot memoises one temp store per test binary.
var (
	testStoreOnce sync.Once
	testStoreDir  string
)

// storeRoot returns the macguffin store root to hand mg. It always returns a
// concrete path rather than deferring to mg's own default, so the claimed/ read
// below and the `mg unclaim` that follows cannot disagree about which store they
// are talking about.
//
// Under a test binary with no explicit Root it returns a per-binary temp
// directory — NEVER the live ~/.macguffin. That is a test-safe DEFAULT, not an
// opt-in helper, and the distinction is the lesson of mg-da48: a sandbox a test
// has to remember to ask for is only ever remembered by the tests whose subject
// IS the store. Here the blast radius of forgetting is releasing a live agent's
// claim out from under it, so the default has to be the safe one.
func (m MGClaimReleaser) storeRoot() string {
	return macguffinStoreRoot(m.Root)
}

// macguffinStoreRoot resolves the macguffin store root, honouring an explicit
// override first. It carries the rationale documented on
// MGClaimReleaser.storeRoot above, and is package-level because the dispatch
// gate (dispatchgate.go) reads the same store and must inherit the same
// test-safe default — a second copy of this resolution would be free to
// disagree, and the way it would disagree is by reaching the live ~/.macguffin
// from a test binary.
func macguffinStoreRoot(override string) string {
	if override != "" {
		return override
	}
	if testing.Testing() {
		testStoreOnce.Do(func() {
			dir, err := testtmp.Dir("claimrelease")
			if err != nil {
				// Every fallback must lead somewhere that is not the live store:
				// a temp path we failed to create yields "nothing claimed", which
				// is a harmless no-op, while $HOME/.macguffin would silently
				// unclaim real work.
				dir = filepath.Join(os.TempDir(), "pogo-claimrelease-test-store-fallback")
			}
			testStoreDir = dir
		})
		return testStoreDir
	}
	if root := os.Getenv("MG_ROOT"); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin")
}

func (m MGClaimReleaser) bin() string {
	if m.Bin != "" {
		return m.Bin
	}
	return "mg"
}

// ReleaseClaim implements ClaimReleaser.
func (m MGClaimReleaser) ReleaseClaim(workItemID string) (bool, error) {
	if workItemID == "" {
		return false, nil
	}
	root := m.storeRoot()
	if root == "" {
		return false, fmt.Errorf("cannot resolve the macguffin store root (no MG_ROOT, no home directory)")
	}

	held, err := claimHeld(root, workItemID)
	if err != nil {
		return false, err
	}
	if !held {
		// Already done, or already unclaimed. Nothing to release, and nothing to
		// complain about — notably, `mg unclaim` on a done item is an error that
		// would otherwise read as a failure on the happy path.
		return false, nil
	}

	out, err := exec.Command(m.bin(), "--root", root, "unclaim", workItemID).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return false, fmt.Errorf("%s unclaim %s: %s", m.bin(), workItemID, msg)
	}
	return true, nil
}

// claimHeld reports whether the store holds a claim file for id. The claim file
// is named <id>.md.<pid>, so it does not end in ".md" — which is also why
// workitem.ListFrom cannot be used here: its ".md" suffix filter skips every
// entry in claimed/.
func claimHeld(root, id string) (bool, error) {
	entries, err := os.ReadDir(filepath.Join(root, "work", "claimed"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read claimed/ in %s: %w", root, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id+".md") {
			return true, nil
		}
	}
	return false, nil
}

// SetClaimReleaser installs the work-item claim releaser used when a polecat is
// stopped. Passing nil restores the default, which is MGClaimReleaser{} — the
// default is functional, not a no-op, so pogod needs no wiring for the release to
// happen and a forgotten wire-up cannot silently reintroduce the leak.
func (r *Registry) SetClaimReleaser(c ClaimReleaser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimReleaser = c
}

func (r *Registry) getClaimReleaser() ClaimReleaser {
	r.mu.RLock()
	c := r.claimReleaser
	r.mu.RUnlock()
	if c == nil {
		return MGClaimReleaser{}
	}
	return c
}

// ReleaseClaimAfterExit returns the work item of a polecat that ended its OWN
// process to available/, and reports whether a held claim was actually released.
//
// Registry.Stop already covers the polecats pogod tears down (mg-fb13), but a
// polecat that exits by itself never goes through Stop. That door did not matter
// much while a merged polecat was auto-stopped at merge; since gh #92 the
// deferred/PR-flow lane — where the polecat is deliberately left running to end
// its own lifecycle — is the DEFAULT path, so a polecat that dies after its
// merge lands but before `mg done` is now an ordinary way to strand a claim in
// claimed/ under a dead pid. Same leak as mg-fb13, through the one exit mg-fb13
// did not close (mg-c8d5).
//
// The caller does not have to establish that the item is still claimed: the
// releaser probes claimed/ first, so calling this for a polecat that completed
// normally is a (false, nil) no-op.
func (r *Registry) ReleaseClaimAfterExit(a *Agent) (bool, error) {
	return r.releasePolecatClaim(a, "agent_exited_before_done")
}

// releasePolecatClaim returns a stopped polecat's work item to available/.
//
// Called from Registry.Stop on the paths that actually tear the agent down, and
// deliberately NOT on the path that leaves a restart_on_crash agent in the
// registry for the supervisor to respawn: that agent comes back on the same work
// item, so releasing its claim would hand the item to a second worker while the
// first restarts. ReleaseClaimAfterExit above adds the self-exit door.
//
// Best-effort by design. The process is already gone by the time this runs, so a
// failure here must not fail the stop — that would trade a stranded work item for
// a stranded registry entry. It is made loud instead, in the log and in the event
// stream, because the whole failure mode this fixes was one nobody could see.
// The (released, err) pair is returned for callers that decide something further
// on the outcome; Stop ignores it and relies on the logging and events here.
func (r *Registry) releasePolecatClaim(a *Agent, reason string) (bool, error) {
	if a == nil || a.Type != TypePolecat || a.WorkItemID == "" {
		return false, nil
	}

	// Before the item goes anywhere, say what is on its branch (mg-b468). The
	// release below returns the item to available/, where it is indistinguishable
	// from work nobody has started — and for a polecat that pushed before it
	// wedged, that description is false. This does not block the release and must
	// not: refusing to release would trade this failure for mg-fb13's, an item
	// stranded in claimed/ under a dead pid where neither dispatch nor stall-watch
	// can see it. The item returns to the pool; what stops is the pool being
	// silent about the branch. The refusal that acts on this lives at dispatch —
	// see strandedWorkRefusal.
	//
	// It runs BEFORE the release rather than after, so the report exists even if
	// the release fails and returns early below.
	r.reportStrandedWorkOnRelease(a, reason)

	released, err := r.getClaimReleaser().ReleaseClaim(a.WorkItemID)
	if err != nil {
		log.Printf("agent %s: FAILED to release the claim on work item %s: %v — "+
			"the item is now stranded in claimed/ under a dead pid, where dispatch will not "+
			"see it and stall-watch does not look; release it by hand with `mg unclaim %s`",
			a.Name, a.WorkItemID, err, a.WorkItemID)
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_claim_release_failed",
			Agent:      a.eventAgent(),
			WorkItemID: a.WorkItemID,
			Repo:       a.SourceRepo,
			Details: map[string]any{
				"pid":   a.PID,
				"error": err.Error(),
			},
		})
		return false, err
	}
	if !released {
		// Not claimed at stop time: the polecat completed its own lifecycle, or
		// pogod recorded `mg done` on its behalf before stopping it. Nothing to do.
		return false, nil
	}
	log.Printf("agent %s: released the claim on work item %s; it is back in available/ (mg-fb13)", a.Name, a.WorkItemID)
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_claim_released",
		Agent:      a.eventAgent(),
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details: map[string]any{
			"pid":    a.PID,
			"reason": reason,
		},
	})
	return true, nil
}
