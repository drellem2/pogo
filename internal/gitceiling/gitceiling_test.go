package gitceiling

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitTopleveled runs `git rev-parse --show-toplevel` against dir with the given
// GIT_CEILING_DIRECTORIES and reports the repo git resolved, or "" if it refused.
//
// The environment is built explicitly rather than from os.Environ() so a
// ceiling (or a HOME) leaking in from the developer's shell or from a parent
// test cannot decide the outcome. These tests assert on git's real behavior, so
// what git is told has to be exactly what the test says it is.
func gitTopleveled(t *testing.T, dir, ceiling, home string) (string, string) {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		// Keep the probe honest: the real machine's git config must not reach
		// this git, and neither must a repo discovered via the environment
		// rather than the walk under test.
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig-absent"),
	}
	if ceiling != "" {
		cmd.Env = append(cmd.Env, EnvVar+"="+ceiling)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", strings.TrimSpace(string(out))
	}
	return strings.TrimSpace(string(out)), ""
}

// fleet builds a throwaway $HOME containing a git repo at $HOME/.pogo (standing
// in for the fleet's live config repo) and a nested, .git-less directory under
// it (standing in for a polecat worktree that lost its .git — the state that
// makes the walk escape). Returns the throwaway home and the nested dir.
//
// $HOME is a mktemp dir, never the developer's real home: initHome asks these
// tests to prove behavior when $HOME IS a git repo, and `git init`ing a real
// home directory is not a thing a test may do to the machine it runs on.
func fleet(t *testing.T, initHome bool) (home, pogoHome, nested string) {
	t.Helper()
	home = t.TempDir()
	// macOS hands out /var/... symlinked to /private/var. Git resolves symlinks
	// while matching ceiling entries, so compare against the resolved path or a
	// pass/fail here would be an artifact of the temp dir, not of the guard.
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	pogoHome = filepath.Join(home, ".pogo")
	nested = filepath.Join(pogoHome, "polecats", "cat")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, pogoHome)
	if initHome {
		gitInit(t, home)
	}
	return home, pogoHome, nested
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
}

// TestUnguarded_WalkEscapesIntoPogoHome is the bug, reproduced. It asserts no
// guard — it pins the behavior the guard exists to stop, so that if some future
// git stops walking out on its own, the tests below stop proving anything and
// this one says so first.
func TestUnguarded_WalkEscapesIntoPogoHome(t *testing.T) {
	home, pogoHome, nested := fleet(t, false)

	top, gitErr := gitTopleveled(t, nested, "", home)
	if gitErr != "" {
		t.Fatalf("unguarded git failed unexpectedly: %s", gitErr)
	}
	if top != pogoHome {
		t.Fatalf("expected the unguarded walk to escape into %s, got %q", pogoHome, top)
	}
	t.Logf("unguarded: a .git-less %s silently resolves to %s", nested, top)
}

// TestGuarded_WalkRefusesToEscape proves the guard FIRES: same escape, ceiling
// set, and git must fail loudly rather than quietly succeed on the wrong repo.
func TestGuarded_WalkRefusesToEscape(t *testing.T) {
	home, pogoHome, nested := fleet(t, false)

	top, gitErr := gitTopleveled(t, nested, pogoHome, home)
	if top != "" {
		t.Fatalf("guard did not fire: walk resolved to %q, expected refusal", top)
	}
	if !strings.Contains(gitErr, "not a git repository") {
		t.Fatalf("expected a loud 'not a git repository' failure, got: %s", gitErr)
	}
	t.Logf("guarded: %s", gitErr)
}

// TestGuarded_WalkRefusesToEscape_WhenHomeIsARepo is the acceptance case, and
// the whole reason this fix exists rather than relying on relocation.
//
// Relocating polecats/ to a sibling of ~/.pogo makes the escaping walk hit
// $HOME and stop — but only because $HOME has no .git. Here $HOME IS a repo, so
// that mechanism's walk would find it and silently succeed. The ceiling does not
// care what is above it: it never gets that far.
func TestGuarded_WalkRefusesToEscape_WhenHomeIsARepo(t *testing.T) {
	home, pogoHome, nested := fleet(t, true)

	// The premise: without the guard, a git-init'd $HOME is reachable from the
	// nested dir. (~/.pogo is nearer, so it wins here — the point is that the
	// walk is loose and terminating it depends on what happens to be above.)
	if top, _ := gitTopleveled(t, nested, "", home); top == "" {
		t.Fatalf("premise broken: expected the unguarded walk to resolve somewhere")
	}

	// Point the walk at a dir whose only ancestors with a .git are ~/.pogo and
	// the git-init'd $HOME above it. The ceiling must stop it before both.
	top, gitErr := gitTopleveled(t, nested, pogoHome, home)
	if top != "" {
		t.Fatalf("guard did not fire with a git-init'd $HOME: resolved to %q", top)
	}
	if !strings.Contains(gitErr, "not a git repository") {
		t.Fatalf("expected a loud failure with a git-init'd $HOME, got: %s", gitErr)
	}
	t.Logf("guarded, $HOME is a repo: %s", gitErr)
}

// TestGuarded_PogoHomeItselfStillResolves pins the semantic that makes one
// ceiling entry safe: the ceiling bounds the walk from BELOW, it does not
// embargo the directory. A legitimate git operation on ~/.pogo itself (the
// fleet config repo has live callers) must keep working, or this guard would
// break the very repo it protects.
func TestGuarded_PogoHomeItselfStillResolves(t *testing.T) {
	home, pogoHome, _ := fleet(t, false)

	top, gitErr := gitTopleveled(t, pogoHome, pogoHome, home)
	if gitErr != "" {
		t.Fatalf("ceiling broke a legitimate git op on POGO_HOME itself: %s", gitErr)
	}
	if top != pogoHome {
		t.Fatalf("expected %s, got %q", pogoHome, top)
	}
}

// TestGuarded_UnrelatedRepoUnaffected pins the other safety semantic: a ceiling
// that is not an ancestor of the working directory is inert. The refinery merges
// source repos that live outside ~/.pogo (~/dev/pogo), and pogod sets this
// ceiling process-wide, so an ambient ceiling MUST NOT reach them.
func TestGuarded_UnrelatedRepoUnaffected(t *testing.T) {
	home, pogoHome, _ := fleet(t, false)
	outside := filepath.Join(home, "dev", "proj")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, outside)

	top, gitErr := gitTopleveled(t, outside, pogoHome, home)
	if gitErr != "" {
		t.Fatalf("ceiling wrongly reached a repo outside POGO_HOME: %s", gitErr)
	}
	if top != outside {
		t.Fatalf("expected %s, got %q", outside, top)
	}
}

// TestEnsure_DerivesCeilingFromPogoHome asserts the invariant Ensure is supposed
// to establish — the ceiling IS POGO_HOME — rather than any particular string.
// A test that hard-coded a path (or a count of the repos under it) would be a
// measurement, stale by the next dispatch.
func TestEnsure_DerivesCeilingFromPogoHome(t *testing.T) {
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	t.Setenv(EnvVar, "")

	if err := Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got := os.Getenv(EnvVar)
	want, err := filepath.Abs(pogoHome)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ceiling = %q, want POGO_HOME (%q)", got, want)
	}
}

// TestEnsure_IsIdempotent: pogod sets the ceiling, an agent inherits it, and the
// agent's own Ensure runs again. That chain must not grow the value at each hop.
func TestEnsure_IsIdempotent(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())
	t.Setenv(EnvVar, "")

	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	first := os.Getenv(EnvVar)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	if second := os.Getenv(EnvVar); second != first {
		t.Fatalf("Ensure not idempotent: %q -> %q", first, second)
	}
}

// TestEnsure_PreservesInheritedCeiling: an operator (or a parent process) may
// have bounded a walk for reasons of their own. Widening somebody else's ceiling
// is exactly the silent-escape failure this package objects to.
func TestEnsure_PreservesInheritedCeiling(t *testing.T) {
	pogoHome := t.TempDir()
	existing := filepath.Join(t.TempDir(), "someone-elses-ceiling")
	t.Setenv("POGO_HOME", pogoHome)
	t.Setenv(EnvVar, existing)

	if err := Ensure(); err != nil {
		t.Fatal(err)
	}

	got := os.Getenv(EnvVar)
	entries := strings.Split(got, string(filepath.ListSeparator))
	if len(entries) != 2 || entries[0] != existing {
		t.Fatalf("inherited ceiling not preserved: %q", got)
	}
	abs, _ := filepath.Abs(pogoHome)
	if entries[1] != abs {
		t.Fatalf("POGO_HOME ceiling not appended: %q", got)
	}
}

func TestCompose(t *testing.T) {
	sep := string(filepath.ListSeparator)
	abs := func(p string) string { return filepath.FromSlash(p) }

	tests := []struct {
		name    string
		current string
		ceiling string
		want    string
		wantErr bool
	}{
		{
			name:    "empty current yields a bare ceiling, not a leading separator",
			current: "",
			ceiling: abs("/a/.pogo"),
			want:    abs("/a/.pogo"),
		},
		{
			name:    "already present is a no-op",
			current: abs("/a/.pogo"),
			ceiling: abs("/a/.pogo"),
			want:    abs("/a/.pogo"),
		},
		{
			name:    "already present unnormalized is still a no-op",
			current: abs("/a/./.pogo/"),
			ceiling: abs("/a/.pogo"),
			want:    abs("/a/./.pogo/"),
		},
		{
			name:    "appends without disturbing existing entries",
			current: abs("/x") + sep + abs("/y"),
			ceiling: abs("/a/.pogo"),
			want:    abs("/x") + sep + abs("/y") + sep + abs("/a/.pogo"),
		},
		{
			// An empty entry tells git the entries after it are symlink-free.
			// Rewriting the current value would change that meaning, so an
			// append must leave it exactly where the caller put it.
			name:    "preserves git's empty symlink-marker entry",
			current: abs("/x") + sep + sep + abs("/y"),
			ceiling: abs("/a/.pogo"),
			want:    abs("/x") + sep + sep + abs("/y") + sep + abs("/a/.pogo"),
		},
		{
			name:    "relative ceiling is refused, not silently ignored by git",
			current: "",
			ceiling: "relative/.pogo",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compose(tc.current, tc.ceiling)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Compose(%q, %q) = %q, want %q", tc.current, tc.ceiling, got, tc.want)
			}
		})
	}
}
