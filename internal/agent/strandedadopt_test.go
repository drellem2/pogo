package agent

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/strandedwork"
)

// The mg-ba32 tests: the stranded gate's SECOND exit.
//
// The gate refuses two populations that want opposite handling — "this branch is
// spent, start over" and "this branch is good, go land it" — and had one flag,
// whose help asserted the first. Everything below is about keeping the two
// distinguishable at the decision, in the mechanism, and in the record.
//
// The fixture is the same one TestSpawnPolecatRefusedForStrandedPushedWork uses,
// deliberately: that test asserts this exact spawn is REFUSED, so a pass here
// cannot be the gate having gone quiet.

// adoptTemplate installs a minimal polecat template in the sandbox and returns
// its name. The spawn tests here assert on the WORKTREE and the PROMPT, so they
// have to get past template expansion — BuildWorkerTemplate names the shipped
// template, which is not installed under the test sandbox's home, and a spawn
// that 404s on it never reaches the worktree code at all.
func adoptRegistry(t *testing.T) *Registry {
	t.Helper()
	// shortSocketDir, not t.TempDir(): the per-agent attach socket lives under the
	// registry dir, and a macOS temp path named after one of these tests overruns
	// sockaddr_un's 104-byte limit — the spawn then fails at 500 with "bind:
	// invalid argument", which is not a fact about anything under test.
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// `cat` instead of the harness binary: these tests drive the real spawn path
	// all the way through, and the assertions are about the WORKTREE and the
	// PROMPT, not about anything a model does with them.
	reg.SetCommandConfig(catCommandConfig{})
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	return reg
}

func adoptTemplate(t *testing.T) string {
	t.Helper()
	writeTemplate(t, "adoptwt", "+++\nworktree = true\n+++\n# polecat for {{.Id}}\n")
	return "adoptwt"
}

// carriesCommit reports whether branch in repo contains sha.
func carriesCommit(t *testing.T, repo, branch, sha string) bool {
	t.Helper()
	err := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", sha, branch).Run()
	return err == nil
}

// --- The mechanism: adopt bases the worktree ON the branch ---------------------

// TestStrandedAdoptBasesTheWorktreeOnTheStrandedBranch is the whole point of the
// flag. A dispatch that merely got past the gate under a friendlier name would
// re-commit the defect mg-ba32 is about: a word that asserts an adoption the
// mechanism never performs.
func TestStrandedAdoptBasesTheWorktreeOnTheStrandedBranch(t *testing.T) {
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "a9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "the branch is finished and only needs a rebase onto main; land it",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("--stranded-adopt dispatch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	a := reg.Get("a9a19")
	if a == nil {
		t.Fatal("the adopt dispatch registered no agent")
	}
	if !carriesCommit(t, repo, "polecat-a9a19", sha) {
		t.Fatalf("the adopting worker's branch polecat-a9a19 does NOT carry %s: the worktree was based "+
			"on the target after all, so the flag named an adoption that did not happen — which is the "+
			"defect mg-ba32 was filed about, reproduced under its own remedy", sha[:12])
	}
	// And the file itself, because "the sha is an ancestor" is a fact about refs
	// and the worker's claim is about the tree it wakes up in.
	if _, err := os.Stat(a.WorktreeDir + "/audit.md"); err != nil {
		t.Errorf("the adopted work is not in the worker's tree: %v", err)
	}
}

// TestStrandedOverrideStartsFromTheTarget is the NEGATIVE CONTROL on the test
// above, on the same fixture with the same gate: without it, "the worktree
// carries the commit" could just be what every spawn in this repo does.
//
// It is also the assertion that --stranded-override's help is now true. Its help
// says the branch is left behind and nothing on it is inherited; this is that
// sentence measured.
func TestStrandedOverrideStartsFromTheTarget(t *testing.T) {
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "b9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedOverride: "branch is a stale duplicate; the real work merged as 9072f34",
	})
	if rr.Code != http.StatusCreated {
		// Asserted rather than assumed: a 500 leaves no polecat-b9a19 branch at
		// all, and "the branch does not carry the commit" would then pass for the
		// wrong reason. This is the control on the control.
		t.Fatalf("--stranded-override dispatch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if carriesCommit(t, repo, "polecat-b9a19", sha) {
		t.Fatalf("an OVERRIDE dispatch inherited %s: override means the branch is spent and is left "+
			"behind, so the two exits would be the same mechanism under two names", sha[:12])
	}
}

// TestStrandedAdoptCarriesThePreRegistrationCommitAsAnAncestor is the expensive
// case. A pre-registration branch is the one the refusal has always said must be
// CONTINUED rather than restarted, and until mg-ba32 the only way to do that was
// a hand-typed `git worktree add` outside the dispatch path entirely.
//
// The commit must arrive as an ANCESTOR of the worker's own branch — not as the
// branch it is standing on — so that nothing it does can amend predictions made
// before the results were known.
func TestStrandedAdoptCarriesThePreRegistrationCommitAsAnAncestor(t *testing.T) {
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-f3ff", "predictions.md",
		"predictions: three of the six checks will fail")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "af3ff", Id: "mg-f3ff", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "the predictions are recorded and the analysis has to be finished on top of them",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("--stranded-adopt on a pre-registration branch: status = %d, want 201: %s",
			rr.Code, rr.Body.String())
	}
	if !carriesCommit(t, repo, "polecat-af3ff", sha) {
		t.Fatalf("the worker's branch does not carry the pre-registration commit %s — it was dispatched "+
			"from the target, which is the silent corruption the refusal exists to prevent", sha[:12])
	}
	if head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "polecat-f3ff")); head != sha {
		t.Errorf("the adopted branch polecat-f3ff moved (now %s, was %s): an adoption creates the "+
			"worker its OWN branch and must never rewrite the one it inherited", head[:12], sha[:12])
	}
}

// --- The record: two exits, two events ----------------------------------------

// TestStrandedAdoptEmitsItsOwnEvent is the durable half of mg-ba32. Once both
// dispositions share one event type, nothing afterwards can tell "a worker was
// sent to continue this branch" from "a worker was sent past it".
func TestStrandedAdoptEmitsItsOwnEvent(t *testing.T) {
	logPath := useTempEventLog(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "e9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "finished work, needs a rebase; adopting to land it",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("adopt dispatch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	lines := readEventLines(t, logPath)
	ev := findEvent(lines, "dispatch_stranded_work_adopted", "cat-e9a19")
	if ev == nil {
		t.Fatal("the adopt dispatch left no dispatch_stranded_work_adopted event: an unrecorded exit " +
			"from this gate is the silent bypass it exists to end")
	}
	if over := findEvent(lines, "dispatch_stranded_work_overridden", "cat-e9a19"); over != nil {
		t.Error("an ADOPT dispatch also recorded an OVERRIDE: the two dispositions are back to being " +
			"indistinguishable in the log, which is exactly what mg-ba32 was filed about")
	}
	details, _ := ev["details"].(map[string]any)
	if branch, _ := details["adopted_branch"].(string); branch != "polecat-9a19" {
		t.Errorf("details.adopted_branch = %q, want polecat-9a19 — a record that does not name the "+
			"branch cannot answer what happened to it", branch)
	}
	if reason, _ := details["reason"].(string); !strings.Contains(reason, "needs a rebase") {
		t.Errorf("details.reason = %q, want the operator's stated reason", reason)
	}
	if refusal, _ := details["refusal"].(string); !strings.Contains(refusal, "UNMERGED") {
		t.Errorf("details.refusal = %q, want the bypassed refusal verbatim", refusal)
	}
}

// --- The decision: the refusal names both, and asserts neither ----------------

// TestRefusalOffersBothExitsWithoutAssertingAReason is the sentence-level half of
// the ticket. The old refusal ended "dispatch anyway with --stranded-override if
// this branch is genuinely spent" — an offer of one exit plus a claim about the
// branch that the gate cannot make and the operator may not hold.
func TestRefusalOffersBothExitsWithoutAssertingAReason(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := newDrainTestRegistry(t)
	refusal := reg.strandedWorkRefusal("mg-9a19", repo, "main")
	if refusal == "" {
		t.Fatal("no refusal on a branch with pushed unmerged work")
	}
	for _, want := range []string{"--stranded-adopt", "--stranded-override"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal does not offer %s; a reader holding that disposition has to type the "+
				"other flag and say the opposite of what they mean. Got: %s", want, refusal)
		}
	}
	if strings.Contains(refusal, "genuinely spent") || strings.Contains(refusal, "if this branch is spent") {
		t.Errorf("the refusal still asserts the branch is spent as the reason to proceed. That is a "+
			"claim about the branch the gate cannot make: it fires just as hard on a branch that is "+
			"GOOD and needs landing. Got: %s", refusal)
	}
}

// TestPreRegistrationRefusalOffersBothExitsToo — the disposition whose advice
// must not be crowded out is also the one where adopting is most obviously
// right, so it is the one that would hurt most to leave out.
func TestPreRegistrationRefusalOffersBothExits(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-f3ff", "predictions.md",
		"predictions: three of the six checks will fail")

	reg := newDrainTestRegistry(t)
	refusal := reg.strandedWorkRefusal("mg-f3ff", repo, "main")
	for _, want := range []string{"PRE-REGISTRATION", "--stranded-adopt", "--stranded-override", "never amend"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the pre-registration refusal does not mention %q; got: %s", want, refusal)
		}
	}
}

// TestBothStrandedExitsIsRefused. They are the two halves of a decision that has
// to have been made, so resolving the pair by precedence would record a decision
// nobody took, in the one field whose whole value is saying which was taken.
func TestBothStrandedExitsIsRefused(t *testing.T) {
	logPath := useTempEventLog(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "x9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt:    "the branch is good",
		StrandedOverride: "the branch is spent",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("passing both exits: status = %d, want 409", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "OPPOSITE") {
		t.Errorf("the refusal does not say the two flags mean opposite things; got: %s", body)
	}
	if reg.Get("x9a19") != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
	for _, et := range []string{"dispatch_stranded_work_adopted", "dispatch_stranded_work_overridden"} {
		if findEvent(readEventLines(t, logPath), et, "cat-x9a19") != nil {
			t.Errorf("a refused dispatch recorded a %s: the record now claims a disposition that was "+
				"never chosen", et)
		}
	}
}

// --- Selection and the unadoptable case ---------------------------------------

func TestAdoptableFindingPrefersPreRegistration(t *testing.T) {
	findings := []strandedwork.Finding{
		{Branch: "polecat-a", Ref: "refs/remotes/origin/polecat-a", Found: true,
			Disposition: strandedwork.DispositionResubmit},
		{Branch: "polecat-b", Ref: "refs/heads/polecat-b", Found: true,
			Disposition: strandedwork.DispositionPreRegistration},
	}
	f, ok := adoptableFinding(findings)
	if !ok || f.Branch != "polecat-b" {
		t.Fatalf("adoptableFinding = (%q, %v), want polecat-b — pre-registration outranks resubmit "+
			"whatever the scan order, matching the refusal's own ordering", f.Branch, ok)
	}
	if others := otherBranches(findings, f); len(others) != 1 || others[0] != "polecat-a" {
		t.Errorf("otherBranches = %v, want [polecat-a]: a dispatch that adopted one of two branches "+
			"left one behind, and the record has to say so", others)
	}
}

// TestAdoptableFindingSkipsWhatItCannotBaseAWorktreeOn. Falling back to the
// target here would produce a worktree that adopted nothing under a flag whose
// name says it did.
func TestAdoptableFindingSkipsWhatItCannotBaseAWorktreeOn(t *testing.T) {
	if _, ok := adoptableFinding([]strandedwork.Finding{
		{Branch: "polecat-a", Ref: "", Found: true},
		{Branch: "polecat-b", Ref: "refs/heads/polecat-b", Found: false},
	}); ok {
		t.Error("adoptableFinding accepted a finding with no usable ref; the spawn would have been " +
			"based on the target and called an adoption")
	}
}

// TestStrandedAdoptRefusedWhenNothingCanBeAdopted is the same property through
// the handler, with a stub gate — the production gate refuses only on a branch
// positively read from disk, so this shape needs one built by hand.
func TestStrandedAdoptRefusedWhenNothingCanBeAdopted(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetStrandedWorkGate(fixedStrandedGate{findings: []strandedwork.Finding{
		{Branch: "polecat-ghost", Ref: "", Found: false, Target: "main",
			Disposition: strandedwork.DispositionResubmit,
			Unmerged:    []strandedwork.Commit{{SHA: "deadbeef", Subject: "x (mg-9a19)"}}},
	}})
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "n9a19", Id: "mg-9a19", Repo: strandedRepo(t), Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "adopt it",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("adopt with nothing adoptable: status = %d, want 409 — the alternative is a worktree "+
			"based on the target wearing the adopt flag's name", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "nothing to adopt") {
		t.Errorf("refusal does not say there was nothing to adopt; got: %s", body)
	}
}

// TestStrandedAdoptRefusedWithoutAWorktree. --no-worktree creates no checkout at
// all, so there is nothing for the branch to be adopted INTO — and the prelude
// would still tell the worker it inherited commits it cannot see. Degrading the
// flag into an ordinary spawn there would be the flag asserting something false,
// which is the whole defect.
func TestStrandedAdoptRefusedWithoutAWorktree(t *testing.T) {
	logPath := useTempEventLog(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "w9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		NoWorktree:    true,
		StrandedAdopt: "adopt and rebase it",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("--stranded-adopt with --no-worktree: status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "NO worktree") {
		t.Errorf("the refusal does not say there is no worktree to adopt into; got: %s", body)
	}
	if findEvent(readEventLines(t, logPath), "dispatch_stranded_work_adopted", "cat-w9a19") != nil {
		t.Error("a refused adopt dispatch recorded dispatch_stranded_work_adopted: the record claims " +
			"a branch was continued by a worker that was never dispatched")
	}
}

// TestStrandedAdoptRefusedWhenItWouldEatTheBranchItAdopts. The new worker's
// branch is polecat-<its name>, so naming it after the stranded agent makes the
// two the same ref — and the spawn's own stale-branch reclamation would then
// judge that branch spent against ITSELF (nothing is unmerged relative to your
// own tip) and delete it. For a branch that is not on origin, that deletion is
// the only copy of the work the adopt flag exists to rescue.
func TestStrandedAdoptRefusedWhenItWouldEatTheBranchItAdopts(t *testing.T) {
	repo := strandedRepo(t)
	sha, _ := worktreeBranch(t, repo, "polecat-e0fc6", "work.md", "feat: local-only work (mg-0fc6)")
	// Detach the worktree's hold on the branch, so the reclamation path is
	// reachable at all: with a worktree still checked out, git refuses the delete
	// for an unrelated reason and this test would pass without measuring anything.
	gitRun(t, repo, "worktree", "prune")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "e0fc6", Id: "mg-0fc6", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "put a worker back on that branch",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("adopt into the adopted branch's own name: status = %d, want 409: %s",
			rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "IS the branch being adopted") {
		t.Errorf("the refusal does not say the new branch would be the adopted one; got: %s", body)
	}
	// The branch, and the work on it, must still be there.
	if !carriesCommit(t, repo, "polecat-e0fc6", sha) {
		t.Fatalf("the adopted branch no longer carries %s: the spawn destroyed the work it was "+
			"dispatched to rescue", sha[:12])
	}
}

// fixedStrandedGate returns a canned answer, for the shapes the production gate
// cannot be made to produce from a real repository.
type fixedStrandedGate struct {
	findings []strandedwork.Finding
	err      error
}

func (g fixedStrandedGate) StrandedFindings(workItemID, repo, target string) ([]strandedwork.Finding, error) {
	return g.findings, g.err
}

// --- The worker's own view ----------------------------------------------------

// TestStrandedAdoptPreludeReachesThePrompt. An adopting worker wakes up on a
// branch carrying somebody else's commits, in a worktree whose base is not the
// target, while every polecat prompt in this tree tells it the branch was created
// for it. Without the block its likeliest reading of `git log` is that main
// already has this work — the one conclusion that makes it abandon the branch it
// was sent to land.
func TestStrandedAdoptPreludeReachesThePrompt(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "p9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "rebase it onto main and resubmit; the work itself is done",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("adopt dispatch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	a := reg.Get("p9a19")
	if a == nil || a.PromptFile == "" {
		t.Fatal("no prompt file recorded for the adopting worker")
	}
	data, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatalf("reading the worker's prompt: %v", err)
	}
	prompt := string(data)
	for _, want := range []string{
		"THIS IS AN ADOPTION",
		"polecat-9a19",
		"rebase it onto main and resubmit", // the operator's reason, verbatim
		"Do not re-derive",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the adopting worker's prompt does not contain %q", want)
		}
	}
	// And it is FIRST: everything else in the prompt is read against what tree
	// the worker believes it is standing in.
	if !strings.HasPrefix(prompt, "# ⚠ THIS IS AN ADOPTION") {
		t.Errorf("the adoption block is not the first thing in the prompt; got: %.120q", prompt)
	}
}

// TestStrandedAdoptPreludeWarnsOffThePreRegistrationCommit — the one inherited
// commit that must never be amended, named by sha in the block that says so.
func TestStrandedAdoptPreludeWarnsOffThePreRegistrationCommit(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	block := strandedAdoptPrelude("mg-f3ff", strandedAdoption{
		Reason: "finish the analysis on top of the recorded predictions",
		Finding: strandedwork.Finding{
			Branch: "polecat-f3ff", Ref: "refs/remotes/origin/polecat-f3ff", Pushed: true,
			Target: "refs/remotes/origin/main", Found: true,
			Disposition:     strandedwork.DispositionPreRegistration,
			PreRegistration: &strandedwork.Commit{SHA: sha, Subject: "predictions: three will fail"},
			Unmerged:        []strandedwork.Commit{{SHA: sha, Subject: "predictions: three will fail"}},
		},
	})
	for _, want := range []string{"PRE-REGISTRATION", sha[:12], "Never amend"} {
		if !strings.Contains(block, want) {
			t.Errorf("the adoption prelude does not mention %q; got:\n%s", want, block)
		}
	}
}

// TestStrandedAdoptPreludeCarriesTheLocalOnlyUrgency. A local-only branch is the
// MORE urgent case — one worktree on one host, reaped by git-gc — and an adopting
// worker is the only process that can make it durable.
func TestStrandedAdoptPreludeCarriesTheLocalOnlyUrgency(t *testing.T) {
	block := strandedAdoptPrelude("mg-0fc6", strandedAdoption{
		Reason: "continue it",
		Finding: strandedwork.Finding{
			Branch: "polecat-p0fc6", Ref: "refs/heads/polecat-p0fc6", Pushed: false,
			Target: "refs/remotes/origin/main", Found: true,
			Disposition: strandedwork.DispositionResubmit,
			Unmerged:    []strandedwork.Commit{{SHA: "abc123", Subject: "work (mg-0fc6)"}},
		},
	})
	if !strings.Contains(block, strandedwork.LocalOnlyWarning) {
		t.Errorf("the adoption prelude does not carry the local-only warning; got:\n%s", block)
	}
}

// --- The remedy is subject to the defect it remedies --------------------------

// TestVerifyAdoptionRejectsAWorktreeThatDidNotAdopt is the self-check. The way
// --stranded-adopt exhibits the defect it repairs is a worktree created from the
// target anyway — a deleted ref, a silent fallback — under a flag whose name
// asserts otherwise. So the adoption is measured after the fact rather than
// assumed from having passed the argument.
func TestVerifyAdoptionRejectsAWorktreeThatDidNotAdopt(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")
	gitRun(t, repo, "branch", "not-adopted", "main")

	if err := verifyAdoption(repo, "polecat-9a19", "not-adopted"); err == nil {
		t.Fatal("verifyAdoption passed a branch that does not carry the stranded work: the check that " +
			"keeps the flag's name honest is not measuring anything")
	}
	// The positive control on the same instrument, in the same repo: without it
	// an always-erroring verifyAdoption would pass the assertion above.
	gitRun(t, repo, "branch", "adopted", "polecat-9a19")
	if err := verifyAdoption(repo, "polecat-9a19", "adopted"); err != nil {
		t.Fatalf("verifyAdoption rejected a branch that DOES carry the stranded work: %v", err)
	}
}
