package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The commit-msg hook is a shell script, so it is tested by running it. That
// is the point: mg-2627 shipped the detector with good unit coverage and a
// hook that failed on every commit, because nothing ever executed the hook
// itself. `go test ./cmd/pogo` is where a Go developer looks, so it lives here
// rather than in a shell suite nobody runs.
//
// The regression under test (mg-d1f7): the hook resolved `pogo` by PRESENCE on
// PATH. A pogo predating check-commit-body was installed, so that arm won and
// died with `unknown command` — for benign and hazardous bodies alike — while
// the `go run ./cmd/pogo` arm that would have worked sat unreachable below it.
//
// The two tests split along the seam that matters. TestCommitMsgHookStale...
// runs in a throwaway git repo where every route is controlled, and proves a
// stale binary does not satisfy the gate. TestCommitMsgHookVerdicts runs in the
// real tree and proves the gate reaches BOTH verdicts. Neither depends on which
// arm the other took — an earlier draft asserted fallthrough against the real
// repo and silently stopped testing anything once ./build.sh produced bin/pogo,
// since that arm is an absolute path no PATH manipulation can reach.

const citationOnlyBody = `feat: thing

Refs drellem2/pogo#89

Ordinary prose about the change.
`

// The real specimen. e83f394 wrote "nobody closed" at the end of a line and
// began the next with the reference; GitHub joined them and shut an external
// contributor's issue.
const wrappedRefSpecimen = "../../internal/closingref/testdata/e83f394-commit-body.txt"

func hookRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func hookToolDir(t *testing.T, tool string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s not on PATH: %v", tool, err)
	}
	return filepath.Dir(p)
}

// writeStalePogoShim writes a `pogo` that behaves exactly like the 249f349 build:
// it exists, it runs, and it has never heard of check-commit-body.
func writeStalePogoShim(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
echo 'Error: unknown command "check-commit-body" for "pogo"' >&2
exit 1
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for shim: %v", err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stale pogo shim: %v", err)
	}
}

func writeHookMsg(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func wrappedRefBody(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(wrappedRefSpecimen)
	if err != nil {
		t.Fatalf("read hazard specimen: %v", err)
	}
	return string(data)
}

// execCommitMsgHook executes the hook at hookPath with working directory dir (which is
// what `git rev-parse --show-toplevel` resolves against) and PATH set to
// exactly pathDirs. Returns exit code and combined output.
func execCommitMsgHook(t *testing.T, hookPath, dir, msgFile string, pathDirs []string) (int, string) {
	t.Helper()
	cmd := exec.Command(hookPath, msgFile)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+strings.Join(pathDirs, string(os.PathListSeparator)))
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run hook: %v (output: %s)", err, out)
		}
		code = exitErr.ExitCode()
	}
	return code, string(out)
}

// TestCommitMsgHookStaleBinariesDoNotSatisfyTheGate is the mg-d1f7 regression,
// run in a throwaway repo so that every route is under the test's control.
//
// Both candidate binaries are stale — one at $repo_root/bin/pogo, one on PATH
// — and there is no Go toolchain. Under the old presence check the PATH binary
// won and every commit died on `unknown command`. Probing capability instead,
// both fall through, and the hook degrades: it PASSES and says why, naming the
// deploy state rather than the commit message.
func TestCommitMsgHookStaleBinariesDoNotSatisfyTheGate(t *testing.T) {
	git := hookToolDir(t, "git")
	tmp := t.TempDir()

	if out, err := exec.Command("git", "-C", tmp, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	// A stale built binary in the first arm's slot, and a stale one on PATH.
	writeStalePogoShim(t, filepath.Join(tmp, "bin", "pogo"))
	shimDir := filepath.Join(tmp, "shim")
	writeStalePogoShim(t, filepath.Join(shimDir, "pogo"))

	hook := filepath.Join(tmp, "commit-msg")
	src, err := os.ReadFile(filepath.Join(hookRepoRoot(t), "hooks", "commit-msg"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if err := os.WriteFile(hook, src, 0o755); err != nil {
		t.Fatalf("write hook copy: %v", err)
	}

	// git and the stale shim only — no Go toolchain, so no source route.
	path := []string{shimDir, git}

	// Both bodies, because a gate that is not running must be shown to permit
	// as well as refuse. Under the bug these two failed identically, which is
	// what made the breakage read as a content problem.
	for _, tc := range []struct{ name, body string }{
		{"benign", citationOnlyBody},
		{"hazard", wrappedRefBody(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := writeHookMsg(t, tmp, tc.name+".txt", tc.body)
			code, out := execCommitMsgHook(t, hook, tmp, msg, path)
			if code != 0 {
				t.Errorf("stale checker blocked the commit: exit %d\n%s", code, out)
			}
			if strings.Contains(out, "unknown command") {
				t.Errorf("a stale binary won the route instead of falling through:\n%s", out)
			}
			if !strings.Contains(out, "UNVERIFIED") {
				t.Errorf("degraded silently — no warning that nothing was checked:\n%s", out)
			}
			// The old failure text pointed the reader at their own commit
			// body. The warning has to point at the layer actually broken.
			if !strings.Contains(out, "DEPLOY-STATE") {
				t.Errorf("warning does not name the deploy state:\n%s", out)
			}
			// Acceptance requires the hazardous case be disclosed as
			// unchecked rather than quietly permitted.
			if !strings.Contains(out, "would pass right now too") {
				t.Errorf("warning does not admit a hazardous body would pass:\n%s", out)
			}
		})
	}
}

// TestCommitMsgHookVerdicts runs the hook as installed, in the real tree, and
// requires it to reach both verdicts. Whichever route wins here is whichever
// route a developer on this checkout actually gets.
//
// Asserting only that the hazard is refused is how mg-2627 shipped: a gate that
// refuses everything passes that assertion. The benign case is the load-bearing
// one.
func TestCommitMsgHookVerdicts(t *testing.T) {
	if testing.Short() {
		t.Skip("may compile cmd/pogo via go run")
	}
	root := hookRepoRoot(t)
	hook := filepath.Join(root, "hooks", "commit-msg")
	tmp := t.TempDir()
	path := []string{hookToolDir(t, "git"), hookToolDir(t, "go")}

	t.Run("benign body passes", func(t *testing.T) {
		msg := writeHookMsg(t, tmp, "benign.txt", citationOnlyBody)
		code, out := execCommitMsgHook(t, hook, root, msg, path)
		if code != 0 {
			t.Errorf("ordinary body with a Refs citation rejected: exit %d\n%s", code, out)
		}
	})

	t.Run("wrapped hazard body is rejected", func(t *testing.T) {
		msg := writeHookMsg(t, tmp, "hazard.txt", wrappedRefBody(t))
		code, out := execCommitMsgHook(t, hook, root, msg, path)
		if code == 0 {
			t.Errorf("wrapped closing ref accepted: exit 0\n%s", out)
		}
		if !strings.Contains(out, "WRAPPED") {
			t.Errorf("rejected, but not for the wrap:\n%s", out)
		}
	})
}
