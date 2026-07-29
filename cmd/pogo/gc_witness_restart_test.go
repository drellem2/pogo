package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/gitgc"
)

// The BAR for mg-1403, driven through the REAL `pogo gc --apply` binary.
//
// mg-0130 fixed pogod's sweep: after a restart the in-memory registry is empty
// permanently (no adopt/reattach path), so the live set was unioned with the
// persisted polecat witness, which survives the restart. `pogo gc` — the manual
// entry point to the same gitgc.Sweep — never got that union and kept building
// its live set from pogod's /agents alone, so mg-0130's exact shape survived one
// caller over.
//
// The scenario below is that caller's version of it, and it is a polecat's
// NORMAL end state: ticket done (`mg done`), process still running while it
// awaits the mayor's stop, pogod restarted since it spawned. Worktree removal
// has no merge gate, so the live set is the tree's SOLE guard.
//
// WHY THE FULL BINARY AND NOT gcLivePolecats DIRECTLY. The defect was never in
// arithmetic on a map — it was in which sources one command consulted. Testing
// the extracted helper would assert the union exists; only running `pogo gc
// --apply` asserts that the command the ticket names is wired to it, all the way
// down to a directory that is or is not still on disk afterwards. And under the
// test binary agent.WitnessPath() deliberately resolves to a private sandbox
// rather than POGO_HOME (mg-a558), so an in-process test cannot even model "the
// witness pogod left on disk" the way a child process reads it.

// gcFixture is a sandboxed world for one `pogo gc` run: a git repo, a polecat
// worktree laid out exactly as pogod builds it, a fake `mg` supplying the ticket
// index, and a POGO_HOME the child resolves its witness and polecats dir from.
type gcFixture struct {
	t        *testing.T
	home     string // child's HOME
	pogoHome string // child's POGO_HOME
	repo     string // the git repo gc sweeps
	worktree string // <pogoHome>/polecats/<name>
	name     string // polecat name
	binDir   string // holds the fake `mg`
}

// newGCFixture builds that world for a polecat whose work item is DONE.
func newGCFixture(t *testing.T, name string) *gcFixture {
	t.Helper()
	home := t.TempDir()
	// Resolve symlinks so paths built here match what `git worktree list`
	// reports — on macOS t.TempDir() lives under /var, a symlink to /private/var.
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	f := &gcFixture{
		t:        t,
		home:     home,
		pogoHome: filepath.Join(home, ".pogo"),
		repo:     filepath.Join(home, "repo"),
		name:     name,
		binDir:   filepath.Join(home, "bin"),
	}
	f.worktree = filepath.Join(f.pogoHome, "polecats", name)

	for _, dir := range []string{f.repo, f.binDir, filepath.Join(f.pogoHome, "polecats")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	f.git("init", "-q", "-b", "main")
	f.git("config", "user.name", "test")
	f.git("config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(f.repo, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git("add", "seed.txt")
	f.git("commit", "-q", "-m", "seed")

	// The polecat's branch, checked out in a worktree at <polecats>/<name>.
	// The basename is not decorative: since gh #94 the sweep keys its liveness
	// exclusion on the worktree's PATH, so a fixture parked anywhere else would
	// be making claims about a layout production never builds.
	f.git("branch", gitgc.BranchPrefix+name)
	f.git("worktree", "add", "-q", f.worktree, gitgc.BranchPrefix+name)

	// `mg list --all --json`, faked: the polecat's ticket has CONCLUDED. That is
	// the whole premise — a done ticket is what makes the tree eligible for
	// removal, and the live set is all that stands between it and `--apply`.
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"id\":\"mg-%s\",\"status\":\"done\"}'\n", name)
	if err := os.WriteFile(filepath.Join(f.binDir, "mg"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mg: %v", err)
	}
	return f
}

func (f *gcFixture) git(args ...string) {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.repo}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// witnessLivePolecat records a witness for a REAL running process and plants it
// in the fixture's POGO_HOME, which is what a pogod that has since restarted
// leaves behind.
//
// The record is produced by the real writer (agent.RecordPolecatWitness, exactly
// as Spawn calls it) into this test binary's private witness sandbox, then
// COPIED to the path the child resolves — the child is not a test binary, so it
// reads POGO_HOME like production does. Writing the JSON by hand here would
// couple the test to the store's on-disk format and to how procStart renders a
// start time; going through the writer couples it to neither.
func (f *gcFixture) witnessLivePolecat() int {
	f.t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		f.t.Fatalf("start sleep: %v", err)
	}
	f.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	pid := cmd.Process.Pid
	if err := agent.RecordPolecatWitness(f.name, pid, "mg-"+f.name); err != nil {
		f.t.Fatalf("RecordPolecatWitness: %v", err)
	}
	data, err := os.ReadFile(agent.WitnessPath())
	if err != nil {
		f.t.Fatalf("read witness written by RecordPolecatWitness: %v", err)
	}
	dst := filepath.Join(f.pogoHome, "polecat-witness.json")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		f.t.Fatalf("plant witness at %s: %v", dst, err)
	}
	return pid
}

// runGC runs the compiled CLI's gc command against a pogod stub whose /agents
// is EMPTY — a restarted pogod's registry, which answers cheerfully and has
// forgotten every polecat that survived it. That is the case the old code was
// silent about: it warned only when pogod was UNREACHABLE.
func (f *gcFixture) runGC(args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]agent.AgentInfo{})
	}))
	f.t.Cleanup(ts.Close)
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	cmd := exec.Command(pogoBin, append([]string{"gc", "--repo=" + f.repo}, args...)...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("POGO_PORT=%d", port),
		"HOME="+f.home,
		"XDG_CONFIG_HOME="+filepath.Join(f.home, "xdg"),
		"POGO_HOME="+f.pogoHome,
		"PATH="+f.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatalf("running %s: %v (stderr=%q)", pogoBin, err, errBuf.String())
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// TestGCApply_WitnessGuardsDoneButRunningWorktreeAcrossRestart is the mg-1403
// acceptance test. It asserts BOTH directions, because a guard that cannot fail
// proves nothing: the RED control shows `pogo gc --apply` sweeping the worktree
// away when the registry is the only source, and the GREEN half shows the
// witness union keeping it. The ONLY difference between the two runs is whether
// pogod left a witness behind.
func TestGCApply_WitnessGuardsDoneButRunningWorktreeAcrossRestart(t *testing.T) {
	// --- RED control: the amnesiac registry ALONE cannot protect the tree. ---
	// No witness on disk, so the live set is exactly what the pre-fix code built:
	// pogod's (empty, post-restart) registry. The done ticket then makes the tree
	// eligible and --apply takes it. If this did NOT happen, the GREEN assertion
	// below would prove nothing.
	{
		f := newGCFixture(t, "0d0e")
		stdout, stderr, code := f.runGC("--apply")
		if code != 0 {
			t.Fatalf("control: gc --apply exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}
		if _, err := os.Stat(f.worktree); !os.IsNotExist(err) {
			t.Fatalf("control did not go RED: worktree %s survived a registry-only live set (err=%v).\n"+
				"If the guard cannot fail here, the GREEN assertion proves nothing (mg-1403).\nstdout=%s", f.worktree, err, stdout)
		}
	}

	// --- GREEN: the witness restores the guard in the CLI path. -------------
	f := newGCFixture(t, "beef")
	pid := f.witnessLivePolecat()

	stdout, stderr, code := f.runGC("--apply")
	if code != 0 {
		t.Fatalf("gc --apply exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(f.worktree); err != nil {
		t.Fatalf("REGRESSION (mg-1403): `pogo gc --apply` swept the worktree of a restart-surviving, "+
			"done-but-still-running polecat (pid %d): %v.\nWorktree removal has no merge gate — the live set "+
			"is its only guard, and pogod's registry has permanently forgotten this polecat.\nstdout=%s",
			pid, err, stdout)
	}
	// Kept for the RIGHT reason — the live-polecat guard, not some unrelated
	// classification that would mask a broken guard.
	if !strings.Contains(stdout, "live polecat "+f.name) {
		t.Errorf("worktree survived, but not as a live polecat — the guard under test may not be what kept it.\nstdout=%s", stdout)
	}
	// And the protection is NAMED, because the amnesiac-registry case is the one
	// the command used to be silent about (it warns only when pogod is
	// unreachable, and a restarted pogod answers just fine).
	if !strings.Contains(stdout, "persisted witness") || !strings.Contains(stdout, f.name) {
		t.Errorf("witness-only protection was applied silently; expected the survivor named in the output.\nstdout=%s", stdout)
	}
}

// TestGCApply_RefusesToSweepOnUnreadableWitness locks the other half of the
// witness contract at the CLI boundary. An unreadable store is not an empty
// fleet — reading it as one deletes exactly the work a live polecat is doing —
// so gc must decline the sweep, as pogod's does (mg-0130), and say why.
//
// The worktree assertion is the load-bearing one: exiting nonzero after already
// removing the tree would satisfy a code-only check and still lose the work.
func TestGCApply_RefusesToSweepOnUnreadableWitness(t *testing.T) {
	f := newGCFixture(t, "0f0f")
	dst := filepath.Join(f.pogoHome, "polecat-witness.json")
	if err := os.WriteFile(dst, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("write corrupt witness: %v", err)
	}

	stdout, stderr, code := f.runGC("--apply")
	if code == 0 {
		t.Errorf("gc --apply must exit nonzero when the witness is unreadable, got 0\nstdout=%s", stdout)
	}
	if !strings.Contains(stderr, "witness") {
		t.Errorf("expected the refusal to name the witness on stderr, got stderr=%q", stderr)
	}
	if _, err := os.Stat(f.worktree); err != nil {
		t.Fatalf("gc removed the worktree despite refusing to sweep: %v — an unreadable witness must "+
			"leave the fleet's trees alone, not just exit nonzero afterwards (mg-1403)", err)
	}
}
