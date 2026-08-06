package homevcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit makes dir a repository with a committed file, so `rev-parse
// --show-toplevel` resolves and `status` has an index to compare against.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	write(t, filepath.Join(dir, "README.md"), "seed\n")
	run("add", "README.md")
	run("commit", "-qm", "seed")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestManagedPathsComeFromTheEmbed is the guard against the failure this whole
// detector exists to catch: a list that stops covering the newest file. The set
// is derived from the shipped corpus, so a prompt added to
// internal/agent/prompts is examined the day it ships.
func TestManagedPathsComeFromTheEmbed(t *testing.T) {
	managed := ManagedPaths()
	if len(managed) < 2 {
		t.Fatalf("ManagedPaths() = %v; the embedded corpus alone ships more than one prompt", managed)
	}
	byRel := map[string]string{}
	for _, m := range managed {
		byRel[m.Rel] = m.Writer
	}
	// The three named in mg-3610 as tracked install output.
	for _, want := range []string{"agents/mayor.md", "agents/templates/polecat.md", "agents/crew/doctor.md"} {
		if byRel[want] != WriterInstall {
			t.Errorf("ManagedPaths() missing %s as install output; got writer %q", want, byRel[want])
		}
	}
	if byRel["projects.json"] != WriterDaemon {
		t.Errorf("projects.json writer = %q, want %q — pogod rewrites it, so versioning it re-dirties on every discovery", byRel["projects.json"], WriterDaemon)
	}
	for _, m := range managed {
		if strings.HasPrefix(m.Rel, "agents/prompts/") {
			t.Errorf("managed path %q kept the corpus's own %q prefix; the installed layout is $POGO_HOME/agents/<rel>", m.Rel, "prompts/")
		}
	}
}

// TestAuditUnversionedHomeIsClean: the ordinary case. A $POGO_HOME nobody has
// git-inited has no dual-tracking hazard, and must not be reported as one.
func TestAuditUnversionedHomeIsClean(t *testing.T) {
	home := t.TempDir()
	rep := auditHome(context.Background(), home, ManagedPaths())
	if rep.Versioned {
		t.Errorf("Versioned = true for %s, which is not inside a git work tree", home)
	}
	if rep.Unknown != "" {
		t.Errorf("Unknown = %q; a plain directory is a decided answer, not an undecidable one", rep.Unknown)
	}
	if len(rep.Tracked) != 0 {
		t.Errorf("Tracked = %v, want none", rep.Tracked)
	}
	if rep.Examined == 0 {
		t.Error("Examined = 0; the population must render even on a clean report, or 'nothing tracked' cannot be told from 'nothing looked at'")
	}
}

// TestAuditVersionedHomeTrackingOnlyLocalFilesIsClean is the state the decision
// in docs/pogo-home-version-control.md asks for: a repo in $POGO_HOME that
// versions the prompts with no upstream and none of the install output.
func TestAuditVersionedHomeTrackingOnlyLocalFilesIsClean(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	write(t, filepath.Join(home, "agents", "crew", "pm-pogo.md"), "a crew prompt with no upstream\n")
	gitRun(t, home, "add", "agents/crew/pm-pogo.md")
	gitRun(t, home, "commit", "-qm", "crew prompts")
	// Install output exists on disk but is untracked — the whole point.
	write(t, filepath.Join(home, "agents", "mayor.md"), "installed\n")

	rep := auditHome(context.Background(), home, ManagedPaths())
	if !rep.Versioned {
		t.Fatalf("Versioned = false for a git-inited %s", home)
	}
	if len(rep.Tracked) != 0 {
		t.Errorf("Tracked = %+v, want none: an untracked mayor.md is exactly the target state", rep.Tracked)
	}
	if rep.Unknown != "" {
		t.Errorf("Unknown = %q, want empty", rep.Unknown)
	}
}

// TestAuditFindsTrackedInstallOutput reproduces the live condition: the repo
// tracks files InstallPrompts writes, so an install leaves them permanently
// modified. Both halves are asserted — tracked AND dirty — because tracked-only
// would be a design smell while tracked-and-dirty is the observed defect.
func TestAuditFindsTrackedInstallOutput(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	write(t, filepath.Join(home, "agents", "mayor.md"), "committed generation\n")
	write(t, filepath.Join(home, "agents", "templates", "polecat.md"), "committed generation\n")
	write(t, filepath.Join(home, "projects.json"), "{}\n")
	gitRun(t, home, "add", "agents/mayor.md", "agents/templates/polecat.md", "projects.json")
	gitRun(t, home, "commit", "-qm", "track install output")
	// What an install does next.
	write(t, filepath.Join(home, "agents", "mayor.md"), "installed generation\n")

	rep := auditHome(context.Background(), home, ManagedPaths())
	if len(rep.Tracked) != 3 {
		t.Fatalf("Tracked = %+v, want the 3 managed paths this repo versions", rep.Tracked)
	}
	if rep.DirtyCount() != 1 {
		t.Errorf("DirtyCount() = %d, want 1 (mayor.md was rewritten after the commit); tracked = %+v", rep.DirtyCount(), rep.Tracked)
	}
	// Dirty findings sort first: the reader's next question is which files an
	// install has already rewritten, not the alphabet.
	if !rep.Tracked[0].Dirty || rep.Tracked[0].Rel != "agents/mayor.md" {
		t.Errorf("Tracked[0] = %+v, want the dirty agents/mayor.md first", rep.Tracked[0])
	}
	var sawDaemon bool
	for _, f := range rep.Tracked {
		if f.Writer == WriterDaemon && f.Rel == "projects.json" {
			sawDaemon = true
		}
	}
	if !sawDaemon {
		t.Errorf("Tracked = %+v; projects.json is daemon-written state and must be reported alongside install output", rep.Tracked)
	}
}

// TestAuditNamesPathsRelativeToTheWorkTreeRoot: the remedy is `git rm --cached
// <path>` run at the work tree root, so a finding that spelled its path
// relative to $POGO_HOME would be a paste that silently addresses the wrong
// file when the two differ.
func TestAuditNamesPathsRelativeToTheWorkTreeRoot(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	home := filepath.Join(root, "dot-pogo")
	write(t, filepath.Join(home, "agents", "mayor.md"), "installed\n")
	gitRun(t, root, "add", "dot-pogo/agents/mayor.md")
	gitRun(t, root, "commit", "-qm", "track a nested home")

	rep := auditHome(context.Background(), home, ManagedPaths())
	if len(rep.Tracked) != 1 {
		t.Fatalf("Tracked = %+v, want the one tracked managed path", rep.Tracked)
	}
	if got, want := rep.Tracked[0].Path, "dot-pogo/agents/mayor.md"; got != want {
		t.Errorf("Path = %q, want %q — the spelling git accepts as a pathspec", got, want)
	}
	if got, want := rep.Tracked[0].Rel, "agents/mayor.md"; got != want {
		t.Errorf("Rel = %q, want %q", got, want)
	}
	if rep.Toplevel != root {
		// macOS resolves /var -> /private/var, so compare by suffix too.
		if !strings.HasSuffix(rep.Toplevel, root) && !strings.HasSuffix(root, rep.Toplevel) {
			t.Errorf("Toplevel = %q, want the work tree root %q", rep.Toplevel, root)
		}
	}
}

// TestAuditDoesNotWriteToTheHome is the constraint that let this ship at all:
// it runs against a directory holding a live fleet's mail, schedules and
// events.log. The index's mtime standing in for "did anything write" is the
// cheapest observation that would catch a `git status` losing its
// --no-optional-locks.
func TestAuditDoesNotWriteToTheHome(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	write(t, filepath.Join(home, "agents", "mayor.md"), "committed\n")
	gitRun(t, home, "add", "agents/mayor.md")
	gitRun(t, home, "commit", "-qm", "track install output")
	write(t, filepath.Join(home, "agents", "mayor.md"), "installed\n")

	index := filepath.Join(home, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if rep := auditHome(context.Background(), home, ManagedPaths()); len(rep.Tracked) == 0 {
		t.Fatalf("audit found nothing, so this test would pass vacuously")
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the audit rewrote .git/index (%v -> %v); it may read the fleet's home and may not write to it", before.ModTime(), after.ModTime())
	}
}
