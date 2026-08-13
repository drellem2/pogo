package gitceiling

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive real git, like the rest of this package's, because the whole
// subject is what git actually does with GIT_CEILING_DIRECTORIES. A fake would
// pin this code's belief about git rather than git.

// guarded points THIS PROCESS's environment at a throwaway home and ceiling for
// the duration of a test.
//
// The sibling helper gitTopleveled seals a child environment instead, and must:
// it measures raw git and has to control exactly what git is told. This one
// cannot. ResolveWorkTree deliberately takes no environment — it inherits, which
// is the only way the ceiling reaches git, and the module-wide check in
// inherit_test.go fails any subprocess that does otherwise. So a test that wants
// to decide what the lookup sees has to set the process's own environment, and
// that is also the more faithful test: it exercises the inheritance the guard's
// whole reach rests on.
//
// Pass ceiling "" to run deliberately unguarded.
func guarded(t *testing.T, home, ceiling string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig-absent"))
	t.Setenv(EnvVar, ceiling)
	// A variable already pointing git at a particular repository would settle
	// these outcomes without the walk under test ever running. Unset rather than
	// blanked: git distinguishes an empty GIT_DIR from an absent one.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG"} {
		if old, ok := os.LookupEnv(key); ok {
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Setenv(key, old) })
		}
	}
}

// commitFile writes rel inside repo and commits it, so a test can ask the
// question the ticket asks: is this path, under the ceiling, reported as
// tracked?
//
// The identity goes on the command line rather than in the environment, because
// the environment is the thing the surrounding test is controlling.
func commitFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	full := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := []string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}
	for _, args := range [][]string{{"add", "--", rel}, {"commit", "-q", "-m", "add " + rel}} {
		full := append(append([]string{"-C", repo}, identity...), args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
}

// TestResolveWorkTree_AnswersForADirTheCeilingRefuses is mg-490c, reproduced and
// then fixed in one test.
//
// The premise half is the measurement from the ticket: a per-agent state dir
// under $POGO_HOME sits inside the fleet config repo's work tree, and `git -C`
// there says "not a git repository". The fix half is that ResolveWorkTree says
// which repo it is in, and says that it had to cross the ceiling to find out.
func TestResolveWorkTree_AnswersForADirTheCeilingRefuses(t *testing.T) {
	home, pogoHome, _ := fleet(t, false)
	nested := filepath.Join(pogoHome, "agents", "pm", "pm-pogo", "memory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	guarded(t, home, pogoHome)

	// The premise. If this stops holding, the fix below is answering a question
	// nobody is asking, and this line says so first.
	if top, gitErr := gitTopleveled(t, nested, pogoHome, home); top != "" {
		t.Fatalf("premise broken: raw git resolved %s to %q; expected the ceiling to refuse", nested, top)
	} else if !strings.Contains(gitErr, "not a git repository") {
		t.Fatalf("premise broken: expected 'not a git repository', got: %s", gitErr)
	}

	w, err := ResolveWorkTree(context.Background(), nested)
	if err != nil {
		t.Fatalf("ResolveWorkTree: %v", err)
	}
	if !w.Versioned() {
		t.Fatalf("reported %s as versioned by nothing; it is inside %s", nested, pogoHome)
	}
	if w.Toplevel != pogoHome {
		t.Fatalf("Toplevel = %q, want %q", w.Toplevel, pogoHome)
	}
	if !w.CeilingCrossed() {
		t.Fatalf("Ceilings is empty; the answer came from stepping onto %s and must say so", pogoHome)
	}
	if len(w.Ceilings) != 1 || w.Ceilings[0] != pogoHome {
		t.Fatalf("Ceilings = %v, want exactly [%s]", w.Ceilings, pogoHome)
	}
	if w.ResolvedFrom != pogoHome {
		t.Fatalf("ResolvedFrom = %q, want %q", w.ResolvedFrom, pogoHome)
	}
}

// TestResolveWorkTree_ReportsATrackedPathUnderTheCeilingAsTracked is the
// ticket's third candidate, made hermetic.
//
// Naming a live path under the real $POGO_HOME would make the assertion a
// measurement of this host, stale the next time somebody moves a directory. The
// property is the same either way: a file that IS tracked, in a directory the
// ceiling refuses, must come out tracked.
func TestResolveWorkTree_ReportsATrackedPathUnderTheCeilingAsTracked(t *testing.T) {
	home, pogoHome, _ := fleet(t, false)
	guarded(t, home, pogoHome)
	rel := filepath.Join("agents", "crew", "pa.md")
	commitFile(t, pogoHome, rel, "# pa\n")
	dir := filepath.Join(pogoHome, filepath.Dir(rel))

	w, err := ResolveWorkTree(context.Background(), dir)
	if err != nil {
		t.Fatalf("ResolveWorkTree: %v", err)
	}
	if !w.Versioned() {
		t.Fatalf("%s reported as under no work tree, but %s is committed in %s", dir, rel, pogoHome)
	}

	// Having a work tree root is the whole point: it is what lets the caller ask
	// the next question at all. Ask it.
	out, err := exec.Command("git", "-C", w.Toplevel, "ls-files", "--", rel).Output()
	if err != nil {
		t.Fatalf("ls-files in the resolved toplevel: %v", err)
	}
	if strings.TrimSpace(string(out)) != filepath.ToSlash(rel) {
		t.Fatalf("ls-files %s = %q, want it listed as tracked", rel, strings.TrimSpace(string(out)))
	}
}

// TestResolveWorkTree_UnversionedStaysUnversioned. The failure mode being fixed
// is a false negative, and the obvious over-correction is to answer "versioned"
// for everything under the ceiling. Nothing versions this tree, and the helper
// must still say so — with a nil error, because that is a decided answer.
func TestResolveWorkTree_UnversionedStaysUnversioned(t *testing.T) {
	home := tempHome(t)
	pogoHome := filepath.Join(home, ".pogo")
	nested := filepath.Join(pogoHome, "agents", "mayor")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	guarded(t, home, pogoHome)

	w, err := ResolveWorkTree(context.Background(), nested)
	if err != nil {
		t.Fatalf("ResolveWorkTree: %v", err)
	}
	if w.Versioned() {
		t.Fatalf("invented a work tree at %q for a directory no repository versions", w.Toplevel)
	}
	// The hop was still attempted and still recorded — "I looked past the
	// ceiling and there was genuinely nothing" is a different fact from "I
	// never looked", and only one of them is this.
	if !w.CeilingCrossed() {
		t.Errorf("Ceilings is empty; %s bounds a lookup from %s and the hop should be recorded", pogoHome, nested)
	}
}

// TestResolveWorkTree_NestedRepoWinsOverTheHop. A polecat worktree under
// $POGO_HOME has its own .git, so git answers directly and no ceiling is in
// play. If the helper hopped anyway it would report the fleet config repo for
// every worktree inside it — the guard's original failure, arrived at from the
// other direction.
func TestResolveWorkTree_NestedRepoWinsOverTheHop(t *testing.T) {
	home, pogoHome, nested := fleet(t, false)
	gitInit(t, nested)
	guarded(t, home, pogoHome)

	w, err := ResolveWorkTree(context.Background(), nested)
	if err != nil {
		t.Fatalf("ResolveWorkTree: %v", err)
	}
	if w.Toplevel != nested {
		t.Fatalf("Toplevel = %q, want the nested repo %q", w.Toplevel, nested)
	}
	if w.CeilingCrossed() {
		t.Fatalf("crossed %v to answer a question git answered directly", w.Ceilings)
	}
}

// TestResolveWorkTree_OutsideTheCeilingIsUntouched. pogod sets this ceiling
// process-wide and the refinery merges source repos that live outside ~/.pogo,
// so a helper that behaved differently for them would be worse than the raw
// command it replaces.
func TestResolveWorkTree_OutsideTheCeilingIsUntouched(t *testing.T) {
	home, pogoHome, _ := fleet(t, false)
	outside := filepath.Join(home, "dev", "proj")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, outside)
	guarded(t, home, pogoHome)

	w, err := ResolveWorkTree(context.Background(), outside)
	if err != nil {
		t.Fatalf("ResolveWorkTree: %v", err)
	}
	if w.Toplevel != outside {
		t.Fatalf("Toplevel = %q, want %q", w.Toplevel, outside)
	}
	if w.CeilingCrossed() || w.ResolvedFrom != outside {
		t.Fatalf("a ceiling that is not an ancestor must be inert: %+v", w)
	}
}

// TestResolveWorkTree_CannotLookIsAnErrorNotAnAnswer. This whole file exists
// because "I could not look" was being rendered as "there is nothing here"
// (mg-afd0, mg-4e02 are the same distinction). A directory that is not there is
// the cheapest instance of it.
func TestResolveWorkTree_CannotLookIsAnErrorNotAnAnswer(t *testing.T) {
	home, pogoHome, _ := fleet(t, false)
	absent := filepath.Join(pogoHome, "agents", "nobody-is-here")
	guarded(t, home, pogoHome)

	w, err := ResolveWorkTree(context.Background(), absent)
	if err == nil {
		t.Fatalf("a directory that does not exist resolved cleanly: %+v", w)
	}
	if w.Versioned() {
		t.Fatalf("Toplevel = %q alongside an error", w.Toplevel)
	}
	if IsNotARepo(err) {
		t.Fatalf("an absent directory was classified as 'no repository here': %v", err)
	}
}

// TestBounding is the path arithmetic on its own — the half of the fix that
// needs no git, and the half a caller running its own git can use to tell a
// ceiling refusal from an unversioned directory.
func TestBounding(t *testing.T) {
	sep := string(filepath.ListSeparator)
	abs := filepath.FromSlash

	tests := []struct {
		name    string
		dir     string
		ceiling string
		want    string
	}{
		{
			name:    "no ceiling bounds nothing",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: "",
			want:    "",
		},
		{
			name:    "an ancestor entry bounds the lookup",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: abs("/h/.pogo"),
			want:    abs("/h/.pogo"),
		},
		{
			// The measured case: $POGO_HOME itself still resolves, because the
			// ceiling never excludes the working directory.
			name:    "the ceiling directory itself is not bounded",
			dir:     abs("/h/.pogo"),
			ceiling: abs("/h/.pogo"),
			want:    "",
		},
		{
			name:    "a non-ancestor entry is inert",
			dir:     abs("/h/dev/pogo"),
			ceiling: abs("/h/.pogo"),
			want:    "",
		},
		{
			// Containment is a component comparison. /h/.pogo-old is not under
			// /h/.pogo, however the strings begin.
			name:    "a sibling sharing a path prefix is not bounded",
			dir:     abs("/h/.pogo-old/agents"),
			ceiling: abs("/h/.pogo"),
			want:    "",
		},
		{
			// The deepest entry is the one that stops the walk first, so it is
			// the one worth resuming from.
			name:    "the deepest of several ancestors wins",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: abs("/h") + sep + abs("/h/.pogo"),
			want:    abs("/h/.pogo"),
		},
		{
			name:    "order does not decide it",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: abs("/h/.pogo") + sep + abs("/h"),
			want:    abs("/h/.pogo"),
		},
		{
			// Git ignores relative entries outright; treating one as a bound
			// here would invent a hop git never made.
			name:    "a relative entry is ignored, as git ignores it",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: filepath.Join("..", ".pogo"),
			want:    "",
		},
		{
			// The empty entry is git's "the entries after this are already
			// symlink-free" marker, not a path.
			name:    "git's empty symlink marker is not a path",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: sep + abs("/h/.pogo"),
			want:    abs("/h/.pogo"),
		},
		{
			name:    "a trailing separator does not defeat the comparison",
			dir:     abs("/h/.pogo/agents/pa"),
			ceiling: abs("/h/.pogo") + string(filepath.Separator),
			want:    abs("/h/.pogo"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bounding(tc.dir, tc.ceiling); got != tc.want {
				t.Fatalf("bounding(%q, %q) = %q, want %q", tc.dir, tc.ceiling, got, tc.want)
			}
		})
	}
}

// TestBounding_ResolvesSymlinksLikeGitDoes. On macOS a temp dir is handed out as
// /var/... and is really /private/var/..., and git matches ceiling entries
// against a getcwd() with no symlinks in it. Comparing the two spellings
// literally would make the helper's answer disagree with git's on the machine
// this fleet runs on.
func TestBounding_ResolvesSymlinksLikeGitDoes(t *testing.T) {
	real := t.TempDir()
	if r, err := filepath.EvalSymlinks(real); err == nil {
		real = r
	}
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	dir := filepath.Join(link, "agents", "pa")
	if err := os.MkdirAll(filepath.Join(real, "agents", "pa"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := bounding(dir, real); got != real {
		t.Fatalf("bounding(%q, %q) = %q; the symlinked spelling must resolve to the same bound", dir, real, got)
	}
}

// tempHome is fleet's temp-dir handling without the git init, for the cases that
// need a home no repository versions.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	return home
}
