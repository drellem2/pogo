package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/project"
)

// liveIndex is this machine's project index as `lsp` printed it on 2026-08-20,
// trailing slashes and all. It is copied rather than invented because the
// ambiguities that matter here are the ones the real index actually contains —
// three names whose second candidate is a refinery worktree, and one whose two
// candidates are both real repositories.
var liveIndex = []string{
	"/Users/daniel/.claude/projects/-Users-daniel--pogo/memory/",
	"/Users/daniel/.pogo/",
	"/Users/daniel/.pogo/agents/pm-onethird/repo/",
	"/Users/daniel/.pogo/pogo-pa/",
	"/Users/daniel/.pogo/refinery/worktrees/one_third/",
	"/Users/daniel/.pogo/refinery/worktrees/one_third_width_three/",
	"/Users/daniel/.pogo/refinery/worktrees/union_closed/",
	"/Users/daniel/dev/bridget/",
	"/Users/daniel/dev/macguffin-pe7ff/",
	"/Users/daniel/dev/macguffin/",
	"/Users/daniel/dev/pogo-darwin/",
	"/Users/daniel/dev/pogo-private/",
	"/Users/daniel/dev/pogo-reminders/",
	"/Users/daniel/dev/pogo/",
	"/Users/daniel/files/riemann/",
	"/Users/daniel/research/one_third_width_three/",
	"/Users/daniel/research/one_third_width_three/lean/.lake/packages/mathlib/",
	"/Users/daniel/research/onethird_program/",
	"/Users/daniel/research/riemann/",
	"/Users/daniel/research/union_closed/",
}

func indexThunk(paths []string) func() []project.Project {
	return func() []project.Project {
		out := make([]project.Project, 0, len(paths))
		for i, p := range paths {
			out = append(out, project.Project{Id: i, Path: p})
		}
		return out
	}
}

// TestResolverAgainstTheLiveIndex is the measurement this fix turns on. Each
// name is a spelling pm-pogo counted in the item store on 2026-08-20, and the
// want column is what the coordinator's notice will be built from.
func TestResolverAgainstTheLiveIndex(t *testing.T) {
	t.Setenv("POGO_HOME", "/Users/daniel/.pogo")
	if got := config.PogoHome(); got != "/Users/daniel/.pogo" {
		t.Skipf("POGO_HOME resolved to %q, not the fixture; this test asserts about the fixture", got)
	}
	res := newRepoResolver(indexThunk(liveIndex))

	cases := []struct {
		name  string
		want  string
		items string
	}{
		{"pogo", "/Users/daniel/dev/pogo", "42 items — the repository the refused dispatch was for"},
		{"one_third_width_three", "/Users/daniel/research/one_third_width_three", "108 items, the largest bare population"},
		{"union_closed", "/Users/daniel/research/union_closed", "47 items"},
		{"onethird_program", "/Users/daniel/research/onethird_program", "3 items"},
		{"macguffin", "/Users/daniel/dev/macguffin", "3 items; macguffin-pe7ff is a different component, not a rival"},
		{"bridget", "/Users/daniel/dev/bridget", "1 item"},
		{"pogo-reminders", "/Users/daniel/dev/pogo-reminders", "2 items"},
	}
	for _, c := range cases {
		got, ok := res.ResolveRepo(c.name)
		if !ok || got != c.want {
			t.Errorf("ResolveRepo(%q) = (%q, %v), want %q — %s", c.name, got, ok, c.want, c.items)
		}
	}
}

// TestResolverRefusesARealAmbiguity is the other half, and it is the half that
// keeps the POGO_HOME filter from being a licence to guess. `riemann` is
// indexed twice OUTSIDE pogo's own tree — ~/files/riemann and
// ~/research/riemann — and neither is derived from the other. There is no
// answer to give, so none is given.
func TestResolverRefusesARealAmbiguity(t *testing.T) {
	t.Setenv("POGO_HOME", "/Users/daniel/.pogo")
	res := newRepoResolver(indexThunk(liveIndex))
	if got, ok := res.ResolveRepo("riemann"); ok {
		t.Errorf("ResolveRepo(\"riemann\") = %q — two unrelated repositories carry that name, "+
			"and picking one produces a confident sentence about the wrong repo's occupancy", got)
	}
	// A name nothing answers to stays unanswered too.
	if got, ok := res.ResolveRepo("no-such-project"); ok {
		t.Errorf("ResolveRepo(\"no-such-project\") = %q", got)
	}
}

// TestResolverDropsOnlyPogosOwnCheckouts pins the filter's scope. Without it
// the three worktree collisions above are ambiguous; with it they resolve —
// and POGO_HOME itself, which is a repository in its own right, is the only
// thing the filter must not swallow by accident when it is the exact path.
func TestResolverDropsOnlyPogosOwnCheckouts(t *testing.T) {
	t.Setenv("POGO_HOME", "/Users/daniel/.pogo")
	// Positive control: with pogo's own checkouts left in, the same name is
	// ambiguous. This is what the filter is buying.
	if got, ok := agent.MatchRepoName("union_closed", liveIndex); ok {
		t.Errorf("unfiltered index resolved union_closed to %q; the fixture must contain the collision "+
			"this filter exists for, or the test below proves nothing", got)
	}
	res := newRepoResolver(indexThunk(liveIndex))
	if got, ok := res.ResolveRepo("union_closed"); !ok || got != "/Users/daniel/research/union_closed" {
		t.Errorf("filtered resolve = (%q, %v), want the research checkout", got, ok)
	}

	// underDir must compare components, not string prefixes.
	if underDir("/Users/daniel/.pogo-other/x", "/Users/daniel/.pogo") {
		t.Error("`.pogo-other` was read as being under `.pogo`")
	}
	if underDir("/Users/daniel/.pogo", "/Users/daniel/.pogo") {
		t.Error("POGO_HOME itself was dropped; the filter is for what is INSIDE it")
	}
	if !underDir("/Users/daniel/.pogo/refinery/worktrees/union_closed", "/Users/daniel/.pogo") {
		t.Error("a refinery worktree was not recognised as pogo's own")
	}
}

// TestStallCapacityUnresolvableIsNotKnown is the join between the two halves:
// an occupancy that could not be TAKEN must reach stall-watch as known=false,
// with the reason attached. If this returns known=true the whole fix is inert —
// the notice goes back to reading Count 0 as free slots.
func TestStallCapacityUnresolvableIsNotKnown(t *testing.T) {
	c, known := stallCapacityFrom(agent.RepoOccupancy{
		Repo: "pogo", Cap: 3, ConfiguredCap: 3,
		Unresolvable: `"pogo" is a repository NAME, not a path`,
	})
	if known {
		t.Fatal("an unresolvable repo reported known=true — stall-watch would render Count 0 as free slots, " +
			"which is the whole of mg-cd4a")
	}
	if c.Unresolved == "" {
		t.Error("the reason was dropped, so the notice can only say `could not be determined` with no why")
	}
	if c.AtCap {
		t.Error("AtCap must stay false — the cap fails open on missing information")
	}
}
