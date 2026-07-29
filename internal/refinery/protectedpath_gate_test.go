package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// defaultProtectedList is the list this repo ships. The tests use it verbatim
// so a change to the real file that this matcher cannot honour fails here
// rather than in production.
const defaultProtectedList = `# comment line
internal/agent/prompts/**
internal/agent/templates/**
.protected-paths
`

// protectedRepo builds an origin bare repo plus a working clone whose main has
// a prompt template, a passing build gate, and the protected-path list. Returns
// (originDir, workDir). Callers branch off workDir.
func protectedRepo(t *testing.T, list string) (string, string) {
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

	writeFile(t, workDir, "build.sh", "#!/bin/sh\nexit 0\n")
	os.Chmod(filepath.Join(workDir, "build.sh"), 0755)
	writeFile(t, workDir, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, workDir, "internal/agent/prompts/mayor.md", "# mayor prompt\n")
	writeFile(t, workDir, "internal/agent/templates/pm-template.md", "# pm template\n")
	if list != "" {
		writeFile(t, workDir, protectedPathsFile, list)
	}
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")
	return originDir, workDir
}

// writeFile writes repo-relative path under dir, creating parents.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// branchWith creates a branch off main in workDir, applies mutate, commits and
// pushes it. The clone is left on the branch.
func branchWith(t *testing.T, workDir, branch string, mutate func()) {
	t.Helper()
	run(t, workDir, "git", "checkout", "main")
	run(t, workDir, "git", "checkout", "-b", branch)
	mutate()
	run(t, workDir, "git", "add", "-A")
	run(t, workDir, "git", "commit", "-m", "feat: change ("+branch+")")
	run(t, workDir, "git", "push", "origin", branch)
}

// TestProtectedPathGateRefusesMayorPromptEndToEnd is THE positive control, and
// it runs the whole pipeline rather than the gate function: r.processNext() is
// the same entry point that merged mr-d9kmdoqtjv1m5em5h9og (branch
// polecat-1935) at 03:39Z on 2026-07-29, landing a +38-line edit to
// internal/agent/prompts/mayor.md on main with nothing objecting.
//
// A green run of a gate that never fires and a green run of a gate that
// correctly found nothing are the same observation, so this test replays the
// breach and requires the refusal — and then requires that origin/main is
// UNCHANGED, because the whole claim of this placement is that it prevents
// rather than detects.
func TestProtectedPathGateRefusesMayorPromptEndToEnd(t *testing.T) {
	useTempEventLog(t)
	originDir, workDir := protectedRepo(t, defaultProtectedList)

	branchWith(t, workDir, "polecat-1935", func() {
		writeFile(t, workDir, "internal/agent/prompts/mayor.md",
			"# mayor prompt\n\n## Section 3b: a thing nobody authorised\n")
	})
	mainBefore := revParse(t, originDir, "main")

	r := newTestRefinery(t)
	id, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: "polecat-1935", TargetRef: "main", Author: "mg-1935",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found after processing")
	}
	if mr.Status != StatusFailed {
		t.Fatalf("MISS: the refinery would have merged the mayor-prompt edit again (status=%s)", mr.Status)
	}
	for _, want := range []string{"internal/agent/prompts/mayor.md", "no bypass"} {
		if !strings.Contains(mr.Error, want) {
			t.Errorf("failure message omits %q — an author cannot act on it:\n%s", want, mr.Error)
		}
	}
	if after := revParse(t, originDir, "main"); after != mainBefore {
		t.Errorf("origin/main moved from %s to %s — the gate detected but did not prevent", mainBefore, after)
	}
}

// TestProtectedPathGateMergesUnprotectedChange is the other required half. A
// check only observed refusing has not been shown to permit, and a gate that
// blocks ordinary work gets disabled within a week. Same harness, same list,
// one ordinary file.
func TestProtectedPathGateMergesUnprotectedChange(t *testing.T) {
	useTempEventLog(t)
	originDir, workDir := protectedRepo(t, defaultProtectedList)

	branchWith(t, workDir, "polecat-ok", func() {
		writeFile(t, workDir, "internal/refinery/feature.go", "package refinery\n")
	})

	r := newTestRefinery(t)
	id, err := r.Submit(MergeRequest{
		RepoPath: originDir, Branch: "polecat-ok", TargetRef: "main", Author: "mg-ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusMerged {
		t.Fatalf("ordinary change refused: status=%s error=%s", mr.Status, mr.Error)
	}
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "internal/refinery/feature.go")); err != nil {
		t.Errorf("merged file missing from main: %v", err)
	}
}

// TestProtectedPathGateReadsListFromTargetNotBranch is the hole that would
// make the whole gate decorative: if the list were read from the branch under
// merge, a branch could delete the list in its first commit and edit a
// protected path in its second. The list is read from origin/<target>, which
// the branch cannot reach — and the deletion is itself refused, since the list
// protects its own path.
func TestProtectedPathGateReadsListFromTargetNotBranch(t *testing.T) {
	originDir, workDir := protectedRepo(t, defaultProtectedList)

	branchWith(t, workDir, "polecat-evade", func() {
		if err := os.Remove(filepath.Join(workDir, filepath.FromSlash(protectedPathsFile))); err != nil {
			t.Fatal(err)
		}
		writeFile(t, workDir, "internal/agent/prompts/mayor.md", "# rewritten\n")
	})

	wt := cloneForGate(t, originDir, "polecat-evade")
	err := checkProtectedPaths(wt, "main", "polecat-evade")
	if err == nil {
		t.Fatal("MISS: deleting the list from the branch disarmed the gate")
	}
	for _, want := range []string{protectedPathsFile, "internal/agent/prompts/mayor.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal omits %q:\n%s", want, err)
		}
	}
}

// TestProtectedPathGateProtectsListWithoutSelfEntry: the list file is
// protected implicitly, so removing its own line from the file does not make
// the file editable. Main's list here deliberately omits the self-entry.
func TestProtectedPathGateProtectsListWithoutSelfEntry(t *testing.T) {
	originDir, workDir := protectedRepo(t, "internal/agent/prompts/**\n")

	branchWith(t, workDir, "polecat-relist", func() {
		writeFile(t, workDir, protectedPathsFile, "# nothing is protected any more\n")
	})

	wt := cloneForGate(t, originDir, "polecat-relist")
	if err := checkProtectedPaths(wt, "main", "polecat-relist"); err == nil {
		t.Fatal("MISS: a branch rewrote the protected-path list and the gate allowed it")
	}
}

// TestProtectedPathGateCatchesRenameAway is why changedPaths passes
// --no-renames. `git mv internal/agent/prompts/mayor.md docs/mayor.md` empties
// the protected directory just as effectively as deleting the file, but with
// rename detection on, --name-only reports only the unprotected destination.
func TestProtectedPathGateCatchesRenameAway(t *testing.T) {
	originDir, workDir := protectedRepo(t, defaultProtectedList)

	branchWith(t, workDir, "polecat-mv", func() {
		run(t, workDir, "git", "mv", "internal/agent/prompts/mayor.md", "docs-mayor.md")
	})

	wt := cloneForGate(t, originDir, "polecat-mv")
	err := checkProtectedPaths(wt, "main", "polecat-mv")
	if err == nil {
		t.Fatal("MISS: moving a protected file out of its directory was allowed")
	}
	if !strings.Contains(err.Error(), "internal/agent/prompts/mayor.md") {
		t.Errorf("refusal does not name the protected source path:\n%s", err)
	}
}

// TestProtectedPathGateSurvivesSkipOnRetry pins the placement decision. Gates
// are bypassed on attempt > 1 when [gates] skip_on_retry is set; this check is
// not a gate for exactly that reason. The call passes skipGates=true, which is
// what attempt 2 of a skip_on_retry repo does, and the branch must still be
// refused — a check the retry path can bypass is a check the retry path will
// eventually bypass on the commit that matters.
func TestProtectedPathGateSurvivesSkipOnRetry(t *testing.T) {
	useTempEventLog(t)
	originDir, workDir := protectedRepo(t, defaultProtectedList)

	branchWith(t, workDir, "polecat-retry", func() {
		writeFile(t, workDir, "internal/agent/templates/pm-template.md", "# rewritten\n")
	})
	mainBefore := revParse(t, originDir, "main")

	r := newTestRefinery(t)
	mr := &MergeRequest{
		ID: "mr-test", RepoPath: originDir, Branch: "polecat-retry", TargetRef: "main",
	}
	wt, err := r.ensureWorktree(originDir)
	if err != nil {
		t.Fatal(err)
	}
	_, stage, _, err := r.attemptMerge(wt, mr, 2, true /* skipGates */, false)
	if err == nil {
		t.Fatal("MISS: skip_on_retry's attempt-2 path merged a protected-path change")
	}
	if stage != "protected-path-check" {
		t.Errorf("stage = %q, want protected-path-check (something else failed first)", stage)
	}
	if after := revParse(t, originDir, "main"); after != mainBefore {
		t.Error("origin/main moved — the retry path pushed a protected-path change")
	}
}

// TestProtectedPathGateNoListProceeds: a repo with no list declares no
// protected paths, and that is a configured absence rather than an unreadable
// answer. It is also the bootstrap case — the commit that introduces the list
// merges through a target that does not have one yet.
func TestProtectedPathGateNoListProceeds(t *testing.T) {
	originDir, workDir := protectedRepo(t, "")

	branchWith(t, workDir, "polecat-bootstrap", func() {
		writeFile(t, workDir, protectedPathsFile, defaultProtectedList)
		writeFile(t, workDir, "internal/refinery/gate.go", "package refinery\n")
	})

	wt := cloneForGate(t, originDir, "polecat-bootstrap")
	if err := checkProtectedPaths(wt, "main", "polecat-bootstrap"); err != nil {
		t.Fatalf("a repo with no list must merge normally: %v", err)
	}
}

// TestProtectedPathGateRefusesUnparseableList: an unreadable answer and an
// all-clear must not be indistinguishable. A list the matcher cannot honour
// fails closed — the alternative is a file that reads as protection and
// silently provides none, which is the defect this gate exists to remove.
func TestProtectedPathGateRefusesUnparseableList(t *testing.T) {
	originDir, workDir := protectedRepo(t, "internal/**/prompts/**\n")

	branchWith(t, workDir, "polecat-anything", func() {
		writeFile(t, workDir, "harmless.go", "package main\n")
	})

	wt := cloneForGate(t, originDir, "polecat-anything")
	err := checkProtectedPaths(wt, "main", "polecat-anything")
	if err == nil {
		t.Fatal("a list with an unsupported pattern must fail the merge, not be skipped")
	}
	if !strings.Contains(err.Error(), "unusable") {
		t.Errorf("refusal should say the list is unusable:\n%s", err)
	}
}

// TestProtectedPathGateRefusesOnEnumerationFailure: same principle applied to
// the diff. A branch git cannot resolve produces no path list, and an empty
// path list must never be read as "changes nothing protected".
func TestProtectedPathGateRefusesOnEnumerationFailure(t *testing.T) {
	originDir, _ := protectedRepo(t, defaultProtectedList)
	wt := cloneForGate(t, originDir, "")

	err := checkProtectedPaths(wt, "main", "polecat-does-not-exist")
	if err == nil {
		t.Fatal("an unreadable diff must not be treated as a clean bill of health")
	}
	if !strings.Contains(err.Error(), "could not enumerate") {
		t.Errorf("refusal should name the enumeration failure:\n%s", err)
	}
}

func TestParseProtectedPaths(t *testing.T) {
	got, err := parseProtectedPaths("# header\n\n  internal/agent/prompts/**  \nhooks/*.sh # trailing\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"internal/agent/prompts/**", "hooks/*.sh"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pattern %d = %q, want %q", i, got[i], want[i])
		}
	}

	for _, bad := range []string{
		"/internal/agent/prompts/**", // absolute
		"internal/**/prompts",        // ** not trailing
		"**/prompts/**",              // two **
		"../outside/**",              // escapes the repo
		"internal/[unclosed",         // malformed glob
	} {
		if _, err := parseProtectedPaths(bad + "\n"); err == nil {
			t.Errorf("pattern %q was accepted; the matcher cannot honour it", bad)
		}
	}
}

func TestMatchProtected(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"internal/agent/prompts/**", "internal/agent/prompts/mayor.md", true},
		{"internal/agent/prompts/**", "internal/agent/prompts/deep/nested/x.md", true},
		{"internal/agent/prompts/**", "internal/agent/promptsX/mayor.md", false},
		{"internal/agent/prompts/**", "internal/agent/prompt.md", false},
		{".protected-paths", ".protected-paths", true},
		{".protected-paths", ".protected-paths.bak", false},
		{"hooks/*.sh", "hooks/pre-commit.sh", true},
		{"hooks/*.sh", "hooks/nested/pre-commit.sh", false},
	}
	for _, c := range cases {
		if got := matchProtected(c.pattern, c.file); got != c.want {
			t.Errorf("matchProtected(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

// TestShippedProtectedListIsParseable reads the list this repo actually ships
// and requires that the gate can use it. The file is data edited by hand, so
// the only thing standing between a typo and a silently-inert red line is this
// test.
//
// The tracked-by-git assertion is not ceremony. The gate reads the list with
// `git ls-tree origin/<target>`, never from the working tree, so an untracked
// list is invisible to it and the gate reports no protected paths while a file
// sits on disk saying otherwise. The first draft of this change put the list
// under .pogo/, which .gitignore excludes — it would have committed nothing and
// protected nothing, while every test that constructs its own fixture repo
// passed. That is the failure mode this ticket exists to remove, so it is
// pinned here rather than left to whoever next moves the file.
func TestShippedProtectedListIsParseable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repoRoot := filepath.Join("..", "..")
	cmd := exec.Command("git", "ls-files", "--error-unmatch", protectedPathsFile)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s is not tracked by git — the gate reads git, not the working "+
			"tree, so an untracked list protects nothing: %v\n%s", protectedPathsFile, err, out)
	}

	data, err := os.ReadFile(filepath.Join(repoRoot, protectedPathsFile))
	if err != nil {
		t.Fatalf("the shipped protected-path list is missing: %v", err)
	}
	patterns, err := parseProtectedPaths(string(data))
	if err != nil {
		t.Fatalf("the shipped protected-path list does not parse: %v", err)
	}
	for _, want := range []string{"internal/agent/prompts/**", "internal/agent/templates/**"} {
		found := false
		for _, p := range patterns {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s no longer protects %q — mg-2a50's red line", protectedPathsFile, want)
		}
	}
	if !matchProtected("internal/agent/prompts/**", "internal/agent/prompts/mayor.md") {
		t.Error("the shipped list would not have caught a3f0efa")
	}
}

// newTestRefinery returns a refinery with a temp worktree dir and manual
// processing (no poll loop).
func newTestRefinery(t *testing.T) *Refinery {
	t.Helper()
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// cloneForGate clones origin into a temp dir and puts it in exactly the state
// attemptMerge hands to the gate: branch checked out fresh from origin and
// rebased onto origin/main. Pass an empty branch to skip that and leave the
// clone with no local feature branch.
func cloneForGate(t *testing.T, originDir, branch string) string {
	t.Helper()
	wt := t.TempDir()
	run(t, wt, "git", "clone", originDir, ".")
	run(t, wt, "git", "fetch", "origin")
	if branch != "" {
		run(t, wt, "git", "checkout", "-B", branch, "origin/"+branch)
		run(t, wt, "git", "rebase", "origin/main")
	}
	return wt
}

// revParse returns the SHA a ref resolves to in dir.
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v\n%s", ref, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
