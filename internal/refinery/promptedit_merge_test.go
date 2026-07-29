package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBranchTouchingShippedPromptsMerges is the positive control for mg-8cfe,
// and it is the reason this file exists rather than the removal being a pure
// deletion.
//
// mg-6c4b taught the refinery to refuse any branch whose diff touched
// internal/agent/prompts/** or internal/agent/templates/**, on the premise that
// shipped prompts were a red line only the repo owner could cross. That premise
// was never checked with him; asked, he said there is no such red line and the
// gate was an overcorrection. It is gone.
//
// A removal verified only by "the tests that asserted the refusal no longer
// exist" is not verified — deleting a test is not the same observation as
// watching the thing it forbade succeed. So this drives the whole pipeline
// (r.processNext, the same entry point the gate hooked) on a branch that edits
// internal/agent/prompts/mayor.md, and requires that it MERGES and that the
// edit is on main afterwards.
//
// What it proves and what it does not, stated because the difference matters:
// it proves the merge pipeline carries no refusal keyed on these paths. It does
// NOT prove a general immunity to path gating, because the mg-6c4b gate read
// its list from a file in the repo under merge and this fixture repo has no
// such file — a reintroduced gate of exactly that design would find nothing to
// read here and let this pass. The check that catches that one is the absence
// of the list from the pogo repository itself, which is a property of the tree
// and not of any test.
func TestBranchTouchingShippedPromptsMerges(t *testing.T) {
	useTempEventLog(t)
	originDir, workDir := promptFixtureRepo(t)

	promptBranch(t, workDir, "polecat-prompt-edit", func() {
		writePromptFixtureFile(t, workDir, "internal/agent/prompts/mayor.md",
			"# mayor prompt\n\n## A section an agent added on its own authority\n")
		writePromptFixtureFile(t, workDir, "internal/agent/templates/pm-template.md",
			"# pm template\n\nsources = [\"open-prs\"]\n")
	})

	r := newPromptTestRefinery(t)
	id, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: "polecat-prompt-edit", TargetRef: "main", Author: "mg-8cfe",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found after processing")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("a branch touching internal/agent/prompts/** was refused: status=%s error=%s\n"+
			"There is no red line on prompt files; edits are owned by the coordinator with pm-pogo as SME, "+
			"not refused at the merge boundary (mg-8cfe).", mr.Status, mr.Error)
	}

	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	got, err := os.ReadFile(filepath.Join(verifyDir, "internal/agent/prompts/mayor.md"))
	if err != nil {
		t.Fatalf("the prompt edit is not on main after a merged MR: %v", err)
	}
	if !strings.Contains(string(got), "on its own authority") {
		t.Errorf("main carries the old prompt — the MR reported merged but the edit did not land:\n%s", got)
	}
}

// promptFixtureRepo builds a bare origin plus a working clone whose main
// carries a shipped prompt, a shipped template and a passing build gate.
// Returns (originDir, workDir).
func promptFixtureRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	writePromptFixtureFile(t, workDir, "build.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(workDir, "build.sh"), 0755); err != nil {
		t.Fatal(err)
	}
	writePromptFixtureFile(t, workDir, "main.go", "package main\nfunc main() {}\n")
	writePromptFixtureFile(t, workDir, "internal/agent/prompts/mayor.md", "# mayor prompt\n")
	writePromptFixtureFile(t, workDir, "internal/agent/templates/pm-template.md", "# pm template\n")
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")
	return originDir, workDir
}

// writePromptFixtureFile writes a repo-relative path under dir, creating parents.
func writePromptFixtureFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// promptBranch creates a branch off main in workDir, applies mutate, commits
// and pushes it.
func promptBranch(t *testing.T, workDir, branch string, mutate func()) {
	t.Helper()
	run(t, workDir, "git", "checkout", "main")
	run(t, workDir, "git", "checkout", "-b", branch)
	mutate()
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "feat: edit the shipped prompt ("+branch+")")
	run(t, workDir, "git", "push", "origin", branch)
}

// newPromptTestRefinery returns a refinery with a temp worktree dir and manual
// processing (no poll loop).
func newPromptTestRefinery(t *testing.T) *Refinery {
	t.Helper()
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
