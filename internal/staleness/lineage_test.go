package staleness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// orgFixtureRepo is an org-template-shaped reference: the corpus lives at
// agents/, not at drellem2/pogo's internal/agent/prompts (drellem2/pogo#125).
func orgFixtureRepo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	for rel, data := range files {
		writeFile(t, filepath.Join(repo, "agents", filepath.FromSlash(rel)), data)
	}
	git(t, repo, "add", "--", "agents")
	git(t, repo, "commit", "-q", "-m", "org corpus")
	return repo
}

// TestCheckPromptsNonDefaultSubtree: the subtree is a parameter. An org
// template keeping its corpus at agents/ was inexpressible while it was a
// constant, so even --repo could not point the check at the real upstream.
// Positive control first — the default subtree finds NOTHING in this repo and
// says so, naming the path it looked under — then the declared subtree finds
// the corpus and judges it.
func TestCheckPromptsNonDefaultSubtree(t *testing.T) {
	mayor := lines(1226, "org mayor")
	repo := orgFixtureRepo(t, map[string][]byte{"mayor.md": mayor})
	installed := t.TempDir()
	writeFile(t, filepath.Join(installed, "mayor.md"), stamped(mayor))

	dflt := CheckPrompts(context.Background(), PromptOptions{Repo: repo, Ref: "main", InstalledRoot: installed, SkipRemote: true})
	if dflt.Err == "" {
		t.Fatal("the default subtree found a corpus in a repo that keeps it at agents/ — the control is broken")
	}
	if !strings.Contains(dflt.Err, DefaultPromptsSubtree) {
		t.Errorf("the empty-subtree error does not name the subtree it read: %s", dflt.Err)
	}

	for _, sub := range []string{"agents", "agents/", "/agents"} {
		rep := CheckPrompts(context.Background(), PromptOptions{Repo: repo, Ref: "main", Subtree: sub, InstalledRoot: installed, SkipRemote: true})
		if rep.Err != "" {
			t.Fatalf("subtree %q: %s", sub, rep.Err)
		}
		if !rep.Clean() || rep.Shipped != 1 {
			t.Errorf("subtree %q: clean=%v shipped=%d deltas=%+v, want a clean 1-file match", sub, rep.Clean(), rep.Shipped, rep.Deltas)
		}
		if rep.Reference.Subtree != "agents" {
			t.Errorf("subtree %q: report names %q, want the normalized agents", sub, rep.Reference.Subtree)
		}
	}

	// And RED through the same subtree: one org file edited.
	writeFile(t, filepath.Join(installed, "mayor.md"), stamped(lines(1006, "generic mayor")))
	rep := CheckPrompts(context.Background(), PromptOptions{Repo: repo, Ref: "main", Subtree: "agents", InstalledRoot: installed, SkipRemote: true})
	if len(rep.Deltas) != 1 || rep.Deltas[0].Path != "mayor.md" {
		t.Fatalf("deltas = %+v, want mayor.md", rep.Deltas)
	}
}

// TestHedgedVerdict is the drellem2/pogo#125 shape: prompts differ AND the
// installed tree carries files the reference does not ship AND no lineage is
// declared. The report must hedge; declaring the lineage must remove the hedge;
// and no unjudged files (or no deltas) means nothing to hedge.
func TestHedgedVerdict(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{
		"mayor.md":             lines(1006, "generic"),
		"templates/polecat.md": lines(298, "generic"),
	})
	installed := t.TempDir()
	writeFile(t, filepath.Join(installed, "mayor.md"), stamped(lines(1226, "org")))
	writeFile(t, filepath.Join(installed, "templates", "polecat.md"), stamped(lines(298, "generic")))
	writeFile(t, filepath.Join(installed, "templates", "payit-polecat.md"), stamped(lines(50, "org-only")))

	opts := PromptOptions{Repo: repo, Ref: "main", InstalledRoot: installed, SkipRemote: true}
	rep := CheckPrompts(context.Background(), opts)
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	if len(rep.Deltas) != 1 || len(rep.Unjudged) != 1 {
		t.Fatalf("fixture: deltas=%+v unjudged=%v, want one of each", rep.Deltas, rep.Unjudged)
	}
	if !rep.Hedged() {
		t.Error("undeclared reference + differing prompts + files the reference does not ship: not hedged")
	}
	if rep.Clean() {
		t.Error("a hedged report read as clean — the hedge weakens the verdict, it does not erase the difference")
	}

	opts.Declared = true
	if declared := CheckPrompts(context.Background(), opts); declared.Hedged() {
		t.Error("a declared lineage is still hedged — the declaration is what licenses the verdict")
	} else if !declared.Reference.Declared {
		t.Error("Declared did not reach the report's Reference")
	}

	// No unjudged files: the reference ships everything installed, so there is
	// no sign it is a foreign upstream.
	os.Remove(filepath.Join(installed, "templates", "payit-polecat.md"))
	opts.Declared = false
	if rep := CheckPrompts(context.Background(), opts); rep.Hedged() {
		t.Error("hedged with no file the reference does not ship")
	}
}

// TestSizeNoteStatesNoDirection: "behind by N" claimed the reference was newer
// and "LONGER by N" invited the same reading. A hash comparison establishes
// difference only (drellem2/pogo#125).
func TestSizeNoteStatesNoDirection(t *testing.T) {
	for _, tc := range []struct {
		installed, ref int
		want           string
	}{
		{1226, 1006, "220 more lines than ref"},
		{1642, 1771, "129 fewer lines than ref"},
		{40, 40, "same length (40 lines), different content"},
	} {
		got := SizeNote(tc.installed, tc.ref)
		if !strings.Contains(got, tc.want) {
			t.Errorf("SizeNote(%d, %d) = %q, want it to contain %q", tc.installed, tc.ref, got, tc.want)
		}
		for _, banned := range []string{"behind", "LONGER", "newer", "older", "superseded"} {
			if strings.Contains(got, banned) {
				t.Errorf("SizeNote(%d, %d) = %q asserts a direction (%q)", tc.installed, tc.ref, got, banned)
			}
		}
	}
	d := PromptDelta{Kind: "differs", InstalledLines: 1226, ShippedLines: 1006}
	if d.LineNote() != SizeNote(1226, 1006) {
		t.Errorf("LineNote %q does not use SizeNote", d.LineNote())
	}
}

// TestPromptReferenceRepo: a declared repo wins and is armed only if it is a
// checkout; a declared non-checkout DISARMS rather than falling back to the
// deploy checkout, which would silently re-create the wrong-upstream verdict.
func TestPromptReferenceRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("POGO_DEPLOY_SRC", "")
	deploy := filepath.Join(home, "deploy-src")
	if err := os.MkdirAll(filepath.Join(deploy, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	if got, ok := PromptReferenceRepo(home, ""); got != deploy || !ok {
		t.Errorf("undeclared: (%q, %v), want the armed deploy checkout", got, ok)
	}
	org := orgFixtureRepo(t, map[string][]byte{"mayor.md": lines(1, "m")})
	if got, ok := PromptReferenceRepo(home, org); got != org || !ok {
		t.Errorf("declared checkout: (%q, %v), want (%q, true)", got, ok, org)
	}
	missing := filepath.Join(home, "not-a-repo")
	if got, ok := PromptReferenceRepo(home, missing); got != missing || ok {
		t.Errorf("declared non-checkout: (%q, %v), want (%q, false) — no fallback", got, ok, missing)
	}
}

// TestDefaultSubtreeAgreesWithConfig pins the one fact config duplicates.
func TestDefaultSubtreeAgreesWithConfig(t *testing.T) {
	if config.DefaultLineagePromptSubtree != DefaultPromptsSubtree {
		t.Errorf("config.DefaultLineagePromptSubtree = %q, staleness.DefaultPromptsSubtree = %q",
			config.DefaultLineagePromptSubtree, DefaultPromptsSubtree)
	}
}
