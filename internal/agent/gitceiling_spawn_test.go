package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/gitceiling"
)

// The ceiling's delivery to a real spawned agent, proven through the real spawn
// path (mg-ca7d).
//
// WHY THIS TEST IS HERE AND NOT IN internal/gitceiling. That package proves what
// git does when the ceiling is set, and that Ensure sets it on this process.
// Neither says the ceiling ever REACHES a polecat. The reach depends on this
// file: Spawn builds the child's environment at `cmd.Env = append(os.Environ(),
// ...)` and hands it to pty.StartWithSize. That is the seam the whole fix rests
// on — pogod calls gitceiling.Ensure once at startup and every agent it spawns
// is supposed to inherit the bound.
//
// Nothing couples those two facts but this test. gitceiling cannot import agent
// to check, and Spawn has no reason to mention a git variable. So the claim
// "pogod's ceiling reaches the polecats" gets proven where the environment is
// actually built, against a real spawned process and a real git.
//
// This is also the test that would have caught the mistake made while building
// this fix: a sandbox check that appeared to show the ceiling missing from a
// spawned agent was in fact measuring a different daemon entirely. An in-repo
// test against the real spawn path has no such ambiguity about what it ran.

// fleetUnderTest builds a throwaway POGO_HOME containing a git repo (the stand-in
// for the fleet's config repo at ~/.pogo) and a nested polecat worktree with NO
// .git — a worktree that has lost it, which is the state whose lookup escapes.
// Returns the pogo home and the nested worktree.
func fleetUnderTest(t *testing.T) (pogoHome, worktree string) {
	t.Helper()
	root := t.TempDir()
	// macOS temp dirs are symlinked (/var -> /private/var) and git resolves
	// symlinks when matching ceiling entries; compare resolved paths or the
	// result would be an artifact of the temp dir rather than of the guard.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	pogoHome = filepath.Join(root, ".pogo")
	worktree = filepath.Join(pogoHome, "polecats", "cat")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", pogoHome)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", pogoHome, err, out)
	}
	return pogoHome, worktree
}

// spawnGitLookup spawns a REAL agent through the registry whose command performs
// a git repository lookup from worktree, and returns what git said. The agent is
// an ordinary polecat-type spawn — the same code path a real polecat takes.
func spawnGitLookup(t *testing.T, worktree string) string {
	t.Helper()

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	out := filepath.Join(t.TempDir(), "lookup.txt")
	script := filepath.Join(t.TempDir(), "lookup.sh")
	// The agent reports what git resolved, or git's own error. Writing to a file
	// rather than reading the PTY keeps the assertion on git's output instead of
	// on terminal framing.
	body := "#!/bin/sh\n" +
		"cd '" + worktree + "' || exit 1\n" +
		"git rev-parse --show-toplevel > '" + out + "' 2>&1\n" +
		"sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.Spawn(SpawnRequest{
		Name:    "ceiling-probe",
		Type:    TypePolecat,
		Command: []string{"/bin/sh", script},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(out); err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("spawned agent never reported a git lookup result")
	return ""
}

// TestSpawnedAgent_GitCannotEscapePogoHome is the acceptance case, driven
// through the real spawn path: a polecat worktree with no .git, a git invocation
// made by an actual spawned agent, and a loud failure instead of a silent
// success on the fleet's own repo.
func TestSpawnedAgent_GitCannotEscapePogoHome(t *testing.T) {
	pogoHome, worktree := fleetUnderTest(t)

	// Stand in for pogod's startup: POGO_HOME is the sandbox, and Ensure bounds
	// this process exactly as cmd/pogod/main.go does before it spawns anything.
	t.Setenv("POGO_HOME", pogoHome)
	t.Setenv(gitceiling.EnvVar, "")
	if err := gitceiling.Ensure(); err != nil {
		t.Fatalf("gitceiling.Ensure: %v", err)
	}

	got := spawnGitLookup(t, worktree)

	if got == pogoHome {
		t.Fatalf("a spawned agent's git escaped into POGO_HOME (%s) — the ceiling did not reach it", pogoHome)
	}
	if !strings.Contains(got, "not a git repository") {
		t.Fatalf("expected a loud 'not a git repository' failure from the spawned agent, got: %s", got)
	}
	t.Logf("spawned agent's git refused to escape: %s", got)
}

// TestSpawnedAgent_GitEscapesWithoutTheCeiling is the control, and the reason
// the test above means anything. Without Ensure, the identical spawn walks up
// and silently resolves the fleet's config repo. If this ever stops escaping,
// the test above is passing for some other reason and is no longer evidence.
func TestSpawnedAgent_GitEscapesWithoutTheCeiling(t *testing.T) {
	pogoHome, worktree := fleetUnderTest(t)

	t.Setenv("POGO_HOME", pogoHome)
	t.Setenv(gitceiling.EnvVar, "") // explicitly unguarded

	got := spawnGitLookup(t, worktree)

	if got != pogoHome {
		t.Fatalf("expected the unguarded spawn to escape into %s, got: %s", pogoHome, got)
	}
	t.Logf("unguarded control: a spawned agent's git silently resolved %s", got)
}
