package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestStallCapacityCopiesTheEnforcedVerdict: the notice must say what the spawn
// point would DO, not re-derive it from the numbers. Here Count is below Cap and
// WouldRefuse is nevertheless true — the shape a future reserve or grace rule
// produces — and a reimplemented `Count >= Cap` would confidently tell the
// coordinator to dispatch into a repo pogod is refusing.
func TestStallCapacityCopiesTheEnforcedVerdict(t *testing.T) {
	c, known := stallCapacityFrom(agent.RepoOccupancy{
		Repo: "/Users/daniel/dev/pogo", Count: 2, Cap: 3,
		Polecats: []string{"c538e", "c6d7b"}, WouldRefuse: true,
	})
	if !known {
		t.Fatal("known = false with a readable witness")
	}
	if !c.AtCap {
		t.Error("AtCap = false while the spawn point would refuse — the notice would name a refused remedy")
	}
	if c.Count != 2 || c.Cap != 3 || strings.Join(c.Polecats, ",") != "c538e,c6d7b" {
		t.Errorf("numbers not carried through: %#v", c)
	}
}

// TestUnreadableWitnessWithEmptyRegistryIsUNKNOWN. The in-memory registry is
// empty after a restart, permanently (mg-13a3), so a witness error there leaves
// zero evidence about survivors. Reporting that as free slots would be this
// ticket's own defect one layer down: a confident remedy on missing
// information.
func TestUnreadableWitnessWithEmptyRegistryIsUNKNOWN(t *testing.T) {
	_, known := stallCapacityFrom(agent.RepoOccupancy{
		Repo: "/Users/daniel/dev/pogo", Count: 0, Cap: 3,
		WitnessErr: "witness: cannot read /x/polecat-witness.json: permission denied",
	})
	if known {
		t.Error("known = true with no registry entries and an unreadable witness — that is no information, not room")
	}
}

// TestUnreadableWitnessWithLiveWorkersIsUNCERTAIN, not unknown: the registry
// still gives a lower bound, the cap FAILS OPEN on the missing part, and so
// does the notice — the caveat travels with the advice instead of replacing it.
func TestUnreadableWitnessWithLiveWorkersIsUNCERTAIN(t *testing.T) {
	c, known := stallCapacityFrom(agent.RepoOccupancy{
		Repo: "/Users/daniel/dev/pogo", Count: 1, Cap: 3, Polecats: []string{"c538e"},
		WitnessErr: "witness: cannot read /x/polecat-witness.json: permission denied",
	})
	if !known {
		t.Fatal("known = false while the registry named a live worker")
	}
	if !strings.Contains(c.Uncertain, "witness") {
		t.Errorf("Uncertain = %q, want the witness read named", c.Uncertain)
	}
}

// TestUnattributedWorkersAreReportedAsUncertain: workers the cap could not
// attribute to any repo are NOT in Count, so a repo that looks open may not be.
func TestUnattributedWorkersAreReportedAsUncertain(t *testing.T) {
	c, known := stallCapacityFrom(agent.RepoOccupancy{
		Repo: "/Users/daniel/dev/pogo", Count: 1, Cap: 3, Polecats: []string{"c538e"},
		Unattributed: []string{"cx", "cy"},
	})
	if !known {
		t.Fatal("known = false for a clean read")
	}
	for _, want := range []string{"2 live worker(s)", "cx", "cy"} {
		if !strings.Contains(c.Uncertain, want) {
			t.Errorf("Uncertain = %q, missing %q", c.Uncertain, want)
		}
	}
}

// TestCleanOccupancyCarriesNoCaveat — the ordinary case must stay silent, or
// every notice grows a hedge and the hedge stops meaning anything.
func TestCleanOccupancyCarriesNoCaveat(t *testing.T) {
	c, known := stallCapacityFrom(agent.RepoOccupancy{
		Repo: "/Users/daniel/dev/pogo", Count: 1, Cap: 3, Polecats: []string{"c538e"},
	})
	if !known || c.AtCap || c.Uncertain != "" {
		t.Errorf("clean occupancy = %#v (known=%v), want a plain dispatchable answer", c, known)
	}
}

// TestNewStallCapacityReadsTheLiveRegistry proves the closure is wired to the
// method the spawn point refuses on, not to a copy of it. An empty registry
// with the cap disarmed refuses nothing, which is the answer it must give.
//
// The repository is one this test CREATES. It used to be the literal
// "/Users/daniel/dev/pogo", which is a directory on exactly one host —
// RepoOccupancyFor stats the path and reports an absolute path that is not a
// directory as UNRESOLVABLE, so on every other machine this test failed
// instantly on `known = false`. It did, on every GitHub Actions run for five
// days (mg-d64b): twenty consecutive red runs, one package of eighty-five,
// `--- FAIL ... (0.00s)` with `Unresolved:"/Users/daniel/dev/pogo is not a
// directory on this host"`. The merge gate runs on the one host where the path
// exists, so the gate could not see it and twenty commits merged through a CI
// that was already red. See TestNewStallCapacityReportsAnAbsentRepoAsUNKNOWN
// below, which pins the half that was doing the failing.
func TestNewStallCapacityReadsTheLiveRegistry(t *testing.T) {
	reg, err := agent.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	repo := t.TempDir()
	c, known := newStallCapacity(reg).CapacityFor(repo)
	if !known {
		t.Fatalf("known = false against a live registry: %#v", c)
	}
	if c.AtCap {
		t.Errorf("AtCap = true with no workers anywhere: %#v", c)
	}
	if c.Repo != repo {
		t.Errorf("Repo = %q, want the queried path %q", c.Repo, repo)
	}
}

// TestNewStallCapacityReportsAnAbsentRepoAsUNKNOWN is the positive control for
// the test above, and it is the assertion the old spelling was making by
// accident on every host but one.
//
// A path that is not a directory here took no count, so Count 0 is not "room" —
// and the closure must say so rather than name a remedy. Pinning it explicitly
// is what keeps the two apart: with only the resolvable case under test, a
// host where the named repo happens to be absent reports the same
// `known = false` and the reader cannot tell a real UNKNOWN from a test that
// was written against somebody's home directory.
func TestNewStallCapacityReportsAnAbsentRepoAsUNKNOWN(t *testing.T) {
	reg, err := agent.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	absent := filepath.Join(t.TempDir(), "no-such-repo")
	c, known := newStallCapacity(reg).CapacityFor(absent)
	if known {
		t.Fatalf("known = true for a path that is not a directory: %#v", c)
	}
	if !strings.Contains(c.Unresolved, absent) {
		t.Errorf("Unresolved = %q, want the unresolvable path named", c.Unresolved)
	}
}
