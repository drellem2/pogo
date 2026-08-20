package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// barenameRegistry builds a registry whose cap is armed, with n workers already
// in a REAL directory, and a resolver that answers for that directory under its
// basename. It is the mg-cd4a situation reproduced in miniature: the fleet is in
// `<tmp>/pogo`, and the work item says `pogo`.
func barenameRegistry(t *testing.T, workers int, wireResolver bool) (*Registry, string) {
	t.Helper()
	sandboxWitness(t)
	root := t.TempDir()
	repo := filepath.Join(root, "pogo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())
	for i := 0; i < workers; i++ {
		name := string(rune('a'+i)) + "-bare"
		a := livePolecat(name, "mg-"+name)
		a.SourceRepo = repo
		reg.agents[name] = a
	}
	if wireResolver {
		reg.SetRepoResolver(RepoResolverFunc(func(name string) (string, bool) {
			return MatchRepoName(name, []string{repo})
		}))
	}
	return reg, repo
}

// TestBareRepoNameAtCapIsNotReportedEmpty is mg-cd4a's headline. The
// repository holds its full allowance of workers; the work item spells it by
// NAME. Before the fix this returned Count 0 and WouldRefuse false — a
// saturated repository reported as empty — and stall-watch built "claim or
// dispatch them" on top of it, for a dispatch that was then refused.
func TestBareRepoNameAtCapIsNotReportedEmpty(t *testing.T) {
	reg, repo := barenameRegistry(t, config.DefaultMaxPolecatsPerRepo, true)

	occ := reg.RepoOccupancyFor("pogo")

	if occ.Unresolvable != "" {
		t.Fatalf("a resolvable name was reported unresolvable: %s", occ.Unresolvable)
	}
	if occ.Repo != repo {
		t.Errorf("Repo = %q, want the RESOLVED path %q — a report that echoes the name back "+
			"leaves the reader to do the lookup that just succeeded", occ.Repo, repo)
	}
	if occ.Count != config.DefaultMaxPolecatsPerRepo {
		t.Errorf("Count = %d, want %d: the workers are in this repository, whatever the item calls it",
			occ.Count, config.DefaultMaxPolecatsPerRepo)
	}
	if !occ.WouldRefuse {
		t.Error("WouldRefuse = false for a repository at its cap — this is the reported defect: " +
			"the spawn point refuses, and the report said it would not")
	}
	// The occupants must be named. Without them "you cannot dispatch" is an
	// instruction with no next step, which is what mg-dd77 exists to prevent.
	if len(occ.Polecats) != config.DefaultMaxPolecatsPerRepo {
		t.Errorf("Polecats = %v, want the %d occupants named", occ.Polecats, config.DefaultMaxPolecatsPerRepo)
	}
}

// TestBareRepoNameAndFullPathAgree: the two spellings are the same repository —
// 42 items use one and 883 the other — so the two reports must be identical
// except for nothing at all. A fix that made the name merely "not wrong" while
// still answering differently from the path would leave the split in place.
func TestBareRepoNameAndFullPathAgree(t *testing.T) {
	reg, repo := barenameRegistry(t, config.DefaultMaxPolecatsPerRepo, true)

	byName := reg.RepoOccupancyFor("pogo")
	byPath := reg.RepoOccupancyFor(repo)

	if byName.Repo != byPath.Repo || byName.Count != byPath.Count || byName.Cap != byPath.Cap ||
		byName.WouldRefuse != byPath.WouldRefuse || strings.Join(byName.Polecats, ",") != strings.Join(byPath.Polecats, ",") {
		t.Errorf("the two spellings of one repository disagree:\n  by name: %+v\n  by path: %+v", byName, byPath)
	}
}

// TestUnresolvableRepoNameIsNotZeroWorkers is the failure direction. When the
// name cannot be resolved the count was never TAKEN, and reporting it as zero
// is the same fabrication with a different cause. It must say so — and it must
// still fail OPEN, because a cap that jams shut on a name it cannot parse would
// halt dispatch for a reason no caller can clear.
func TestUnresolvableRepoNameIsNotZeroWorkers(t *testing.T) {
	for _, tc := range []struct {
		what     string
		resolver bool
		wantWord string
	}{
		{"no resolver wired at all", false, "no name resolver wired"},
		{"a name this host does not know", true, "matches no single repository"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			reg, _ := barenameRegistry(t, 3, tc.resolver)
			occ := reg.RepoOccupancyFor("union_closed")
			if occ.Unresolvable == "" {
				t.Fatal("an unresolvable name reported a clean occupancy — Count 0 here is a guess, not a count")
			}
			if !strings.Contains(occ.Unresolvable, tc.wantWord) {
				t.Errorf("reason = %q, want it to mention %q", occ.Unresolvable, tc.wantWord)
			}
			if occ.WouldRefuse {
				t.Error("the cap refused on a name it could not resolve — this gate fails OPEN")
			}
			// Cap 0 is this struct's signal for "the cap is disarmed". An
			// unresolvable report must not accidentally send it.
			if occ.Cap == 0 {
				t.Error("Cap = 0 on an unresolvable report, which reads as `the cap is disarmed`")
			}
		})
	}
}

// TestAbsolutePathThatIsNotThereIsAlsoUnresolvable closes the same hole one
// spelling further along: an absolute path naming nothing gets the identical
// confident zero. Not a spelling the store actually contains (pm-pogo's census
// found bare names and empties, not bogus paths) — pinned because it is the
// same defect and the fix is the same branch.
func TestAbsolutePathThatIsNotThereIsAlsoUnresolvable(t *testing.T) {
	reg, repo := barenameRegistry(t, 1, true)
	occ := reg.RepoOccupancyFor(filepath.Join(filepath.Dir(repo), "no-such-repo"))
	if occ.Unresolvable == "" {
		t.Error("a path that is not on this host reported a clean zero occupancy")
	}
	if occ.WouldRefuse {
		t.Error("fails open, like every other missing-information branch here")
	}
}

// TestOccupiedRepoIsNeverDemotedByAStatFailure is the remedy checked against
// the defect it remedies. The directory probe above must never turn a SATURATED
// repository into "could not be determined" — that would drop the at-cap
// guidance for exactly the repositories that need it, which is mg-cd4a
// re-entered through mg-cd4a's fix. Live workers are themselves proof the
// repository is real, so the probe is guarded on a zero count.
func TestOccupiedRepoIsNeverDemotedByAStatFailure(t *testing.T) {
	reg, repo := barenameRegistry(t, config.DefaultMaxPolecatsPerRepo, true)
	// Remove the directory out from under the count. The workers' SourceRepo
	// still names it, which is the state a deleted-but-occupied repo is in.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	occ := reg.RepoOccupancyFor(repo)
	if occ.Unresolvable != "" {
		t.Fatalf("a repository holding %d workers was demoted to unresolvable: %s", occ.Count, occ.Unresolvable)
	}
	if !occ.WouldRefuse {
		t.Error("the at-cap verdict was lost with the directory — the guidance goes missing " +
			"for the repositories that most need it")
	}
}
