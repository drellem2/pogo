package agent

import (
	"strings"
	"testing"
)

// theseRepos is the shape of this machine's project index as MatchRepoName sees
// it: a flat list of absolute paths, several of which share a prefix and two of
// which share a leading component with a DIFFERENT repository's name.
var theseRepos = []string{
	"/Users/daniel/dev/pogo",
	"/Users/daniel/dev/pogo-reminders",
	"/Users/daniel/research/one_third_width_three",
	"/Users/daniel/research/onethird_program",
	"/Users/daniel/dev/drellem2/pogo-reminders",
}

// TestMatchRepoNameResolvesTheSpellingsInTheStore walks the bare spellings
// pm-pogo counted in the item store on 2026-08-20 and pins what each resolves
// to. These are the inputs the defect was measured on, not invented ones.
func TestMatchRepoNameResolvesTheSpellingsInTheStore(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
		why  string
	}{
		{"pogo", "/Users/daniel/dev/pogo", true,
			"42 items in the store spell it this way; it is the repository the refused dispatch was for"},
		{"one_third_width_three", "/Users/daniel/research/one_third_width_three", true,
			"108 items, the largest bare population of any single name"},
		{"pogo-reminders", "", false,
			"TWO indexed repositories end in this component, so there is no single answer to give"},
		{"drellem2/pogo-reminders", "/Users/daniel/dev/drellem2/pogo-reminders", true,
			"a multi-component name disambiguates what the bare basename could not"},
		{"union_closed", "", false,
			"47 items name it and this host indexes no such repository — the honest answer is nothing"},
		{"", "", false, "an empty name resolves to nothing, and must not resolve to everything"},
	}
	for _, c := range cases {
		got, ok := MatchRepoName(c.name, theseRepos)
		if ok != c.ok || got != c.want {
			t.Errorf("MatchRepoName(%q) = (%q, %v), want (%q, %v) — %s", c.name, got, ok, c.want, c.ok, c.why)
		}
	}
}

// TestMatchRepoNameWillNotMatchAcrossAComponentBoundary is the property that
// keeps this resolver from being a worse defect than the one it repairs. A
// substring match would answer `/Users/daniel/dev/pogo-reminders` for `pogo`,
// and the notice built on it would name a real repository's real occupants —
// the wrong repository's — with no visible error anywhere.
func TestMatchRepoNameWillNotMatchAcrossAComponentBoundary(t *testing.T) {
	got, ok := MatchRepoName("pogo", []string{"/Users/daniel/dev/pogo-reminders"})
	if ok {
		t.Errorf("`pogo` matched %q — a substring, not a path component", got)
	}
	// The same boundary in the other direction: a name is not a prefix match.
	if got, ok := MatchRepoName("dev", theseRepos); ok {
		t.Errorf("`dev` matched %q — an interior component is not a repository", got)
	}
	// And a full path still resolves to itself, so a caller that hands this
	// function an already-good path is not punished for it.
	if got, ok := MatchRepoName("/Users/daniel/dev/pogo", theseRepos); !ok || got != "/Users/daniel/dev/pogo" {
		t.Errorf("an absolute path must resolve to itself, got (%q, %v)", got, ok)
	}
}

// TestMatchRepoNameIgnoresNoiseInTheIndex: the project list is read from disk
// and is not curated. Duplicates must not read as ambiguity, and relative
// entries must not become candidates — a relative entry could not disambiguate
// anything, and treating one as a hit would hand back a path that is still not
// a path.
func TestMatchRepoNameIgnoresNoiseInTheIndex(t *testing.T) {
	dupes := []string{"/Users/daniel/dev/pogo", "/Users/daniel/dev/pogo/", "/Users/daniel/dev/pogo"}
	got, ok := MatchRepoName("pogo", dupes)
	if !ok || got != "/Users/daniel/dev/pogo" {
		t.Errorf("duplicate index entries read as ambiguity: got (%q, %v)", got, ok)
	}
	if got, ok := MatchRepoName("pogo", []string{"pogo", "./pogo", ""}); ok {
		t.Errorf("a relative index entry became a resolution: %q", got)
	}
}

// TestMatchRepoNameAmbiguityAnswersNothing pins the decision rather than the
// mechanism: two candidates produce no answer, not the first one. See the doc
// comment — the consumer of this result is a sentence telling a coordinator
// what to do, and being wrong there is silent.
func TestMatchRepoNameAmbiguityAnswersNothing(t *testing.T) {
	two := []string{"/a/b/widgets", "/c/d/widgets"}
	if got, ok := MatchRepoName("widgets", two); ok {
		t.Errorf("ambiguous name resolved to %q; two candidates must resolve to none", got)
	}
	// Adding a third does not tip it into a majority either.
	if got, ok := MatchRepoName("widgets", append(two, "/e/f/widgets")); ok {
		t.Errorf("three candidates resolved to %q", got)
	}
}

// TestRepoResolverFuncIsTheInterface keeps the adapter honest — it is the shape
// cmd/pogod wires, and a signature drift there would be caught by the compiler
// only in package main.
func TestRepoResolverFuncIsTheInterface(t *testing.T) {
	var r RepoResolver = RepoResolverFunc(func(name string) (string, bool) {
		return MatchRepoName(name, theseRepos)
	})
	got, ok := r.ResolveRepo("pogo")
	if !ok || !strings.HasSuffix(got, "/dev/pogo") {
		t.Fatalf("ResolveRepo(\"pogo\") = (%q, %v)", got, ok)
	}
}
