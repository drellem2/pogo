package refinery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mg-fb7df: host-local extra gates, configured per repo outside the repo's own
// tree, run after the in-tree gates with the merge's commit range in env.

// extraGateRepo builds a worktree whose origin/main sits one commit behind
// HEAD, the shape the refinery gates after its rebase. The branch commit adds
// a file with content and a commit message of the caller's choosing.
func extraGateRepo(t *testing.T, content, message string) (wtDir, base, head string) {
	t.Helper()
	wtDir = t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wtDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(wtDir, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-q", "-m", "base")
	base = git("rev-parse", "HEAD")
	git("update-ref", "refs/remotes/origin/main", base)
	if err := os.WriteFile(filepath.Join(wtDir, "change.txt"), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "change.txt")
	git("commit", "-q", "-m", message)
	head = git("rev-parse", "HEAD")
	return wtDir, base, head
}

// scanGate writes a host gate OUTSIDE the worktree that fails when the merge's
// added lines or commit messages contain the placeholder word, and records the
// range it was handed.
func scanGate(t *testing.T) (script, envLog string) {
	t.Helper()
	dir := t.TempDir()
	envLog = filepath.Join(dir, "env.log")
	script = filepath.Join(dir, "scan.sh")
	body := `#!/bin/sh
echo "host scan gate: checking $POGO_GATE_BASE..$POGO_GATE_HEAD"
echo "$POGO_GATE_BASE $POGO_GATE_HEAD $POGO_GATE_TARGET" > ` + envLog + `
[ -n "$POGO_GATE_BASE" ] && [ -n "$POGO_GATE_HEAD" ] || { echo "no range"; exit 2; }
if git diff "$POGO_GATE_BASE" "$POGO_GATE_HEAD" | grep '^+' | grep -qiw forbiddenword; then
  echo "refused: diff"; exit 1
fi
if git log --format=%B "$POGO_GATE_BASE..$POGO_GATE_HEAD" | grep -qiw forbiddenword; then
  echo "refused: message"; exit 1
fi
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, envLog
}

func extraGateRefinery(t *testing.T, gates map[string][]string) *Refinery {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	r.cfg.ExtraGates = func(repoPath string) []string {
		return gates[filepath.Base(repoPath)]
	}
	return r
}

func TestExtraGateFailureFailsTheMerge(t *testing.T) {
	for _, tc := range []struct{ name, content, message string }{
		{"diff", "a FORBIDDENWORD line", "feat: ordinary"},
		{"message", "ordinary line", "feat: mentions forbiddenword"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wtDir, base, head := extraGateRepo(t, tc.content, tc.message)
			writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo in-tree-ran\"]\n")
			script, envLog := scanGate(t)
			r := extraGateRefinery(t, map[string][]string{filepath.Base(wtDir): {script}})
			mr := &MergeRequest{ID: "mr-extra-" + tc.name, Status: StatusProcessing, TargetRef: "main"}
			r.byID[mr.ID] = mr

			out, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
			if err == nil {
				t.Fatalf("an extra gate exiting 1 must fail the merge, got pass:\n%s", out)
			}
			if len(ran) != 2 || ran[0] != "echo in-tree-ran" || ran[1] != script {
				t.Errorf("gates run = %v, want the in-tree gate then the extra gate", ran)
			}
			if !strings.Contains(out, "in-tree-ran") || !strings.Contains(out, "refused: "+tc.name) {
				t.Errorf("output should show both gates and the refusal:\n%s", out)
			}
			if !strings.Contains(out, "(host extra gate)") {
				t.Errorf("output should label the host gate:\n%s", out)
			}
			got, _ := os.ReadFile(envLog)
			if want := base + " " + head + " main"; strings.TrimSpace(string(got)) != want {
				t.Errorf("gate env = %q, want %q", strings.TrimSpace(string(got)), want)
			}
		})
	}
}

// The positive control for the test above: the same gate on a clean change
// passes, so its failure there is about the content and not the wiring.
func TestExtraGateCleanChangePasses(t *testing.T) {
	wtDir, _, _ := extraGateRepo(t, "ordinary line", "feat: ordinary")
	script, _ := scanGate(t)
	r := extraGateRefinery(t, map[string][]string{filepath.Base(wtDir): {script}})
	mr := &MergeRequest{ID: "mr-extra-clean", Status: StatusProcessing, TargetRef: "main"}
	r.byID[mr.ID] = mr

	// No in-tree gates at all: the extra gate still runs.
	out, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err != nil {
		t.Fatalf("clean change should pass: %v\n%s", err, out)
	}
	if len(ran) != 1 || ran[0] != script || !strings.Contains(out, "host scan gate: checking") {
		t.Errorf("the extra gate should have run alone: ran=%v\n%s", ran, out)
	}
}

// A failing in-tree gate stops the run before any extra gate.
func TestExtraGateRunsAfterInTreeGates(t *testing.T) {
	wtDir, _, _ := extraGateRepo(t, "ordinary line", "feat: ordinary")
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"exit 1\"]\n")
	script, envLog := scanGate(t)
	r := extraGateRefinery(t, map[string][]string{filepath.Base(wtDir): {script}})
	mr := &MergeRequest{ID: "mr-extra-order", Status: StatusProcessing, TargetRef: "main"}
	r.byID[mr.ID] = mr

	_, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("in-tree gate failure should fail the merge")
	}
	if len(ran) != 1 {
		t.Errorf("gates run = %v, want only the failing in-tree gate", ran)
	}
	if _, serr := os.Stat(envLog); serr == nil {
		t.Error("the extra gate ran after an in-tree failure")
	}
}

// With no host config the gate list is exactly the in-tree one: the content
// the scan gate would refuse merges, because nothing scans it.
func TestNoHostConfigLeavesGatesUnchanged(t *testing.T) {
	for _, name := range []string{"nil-func", "no-entry"} {
		t.Run(name, func(t *testing.T) {
			wtDir, _, _ := extraGateRepo(t, "a forbiddenword line", "feat: ordinary")
			writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo in-tree-ran\"]\n")
			var r *Refinery
			if name == "nil-func" {
				r = newProgressTestRefinery(t, 10*time.Millisecond)
			} else {
				script, _ := scanGate(t)
				r = extraGateRefinery(t, map[string][]string{"some-other-repo": {script}})
			}
			mr := &MergeRequest{ID: "mr-extra-none-" + name, Status: StatusProcessing, TargetRef: "main"}
			r.byID[mr.ID] = mr
			out, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
			if err != nil {
				t.Fatalf("should pass: %v\n%s", err, out)
			}
			if len(ran) != 1 || ran[0] != "echo in-tree-ran" {
				t.Errorf("gates run = %v, want only the in-tree gate", ran)
			}
			if strings.Contains(out, "host extra gate") {
				t.Errorf("no host gate should be mentioned:\n%s", out)
			}
		})
	}
	// And with no gates anywhere, the historic pass-by-default stands.
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	out, ran, err := r.runQualityGates(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil || ran != nil || out != "(no quality gates configured)" {
		t.Errorf("no gates: out=%q ran=%v err=%v", out, ran, err)
	}
}
