package agent

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/strandedwork"
)

// strandedRepo builds a repo with an origin and returns its path. The fixture is
// a real git repository for the reason given at the head of
// internal/strandedwork/strandedwork_test.go: every fact under test is a fact
// about git's patch-identity arithmetic.
func strandedRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	origin := filepath.Join(root, "origin.git")
	gitRun(t, root, "init", "--bare", "--initial-branch=main", origin)
	gitRun(t, root, "init", "--initial-branch=main", work)
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	gitRun(t, work, "remote", "add", "origin", origin)
	writeCommit(t, work, "README.md", "chore: initial commit")
	gitRun(t, work, "push", "-q", "origin", "main")
	return work
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func writeCommit(t *testing.T, repo, file, subject string) string {
	t.Helper()
	path := filepath.Join(repo, file)
	prev, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(prev, []byte(subject+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", file)
	gitRun(t, repo, "commit", "-q", "-m", subject)
	return strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
}

// pushBranch creates branch off main, commits subject, and pushes it.
func pushBranch(t *testing.T, repo, branch, file, subject string) string {
	t.Helper()
	gitRun(t, repo, "checkout", "-q", "-b", branch, "main")
	sha := writeCommit(t, repo, file, subject)
	gitRun(t, repo, "push", "-q", "origin", branch)
	gitRun(t, repo, "checkout", "-q", "main")
	return sha
}

// --- The dispatch refusal, both cases ----------------------------------------

// TestSpawnPolecatRefusedForStrandedPushedWork is the mg-9a19 shape reproduced
// through the handler: the polecat pushed finished work, was stopped, its item
// went back to available/, and a fresh polecat was dispatched at it three
// minutes later. Before mg-b468 that spawn succeeded and spent 144s re-deriving
// 1026 lines. It must now be refused.
func TestSpawnPolecatRefusedForStrandedPushedWork(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): the whole battery (mg-9a19)")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "a9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn onto an item with pushed unmerged work: status = %d, want 409 — "+
			"this is the mg-9a19 re-dispatch reproduced", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"mg-9a19", "polecat-9a19", "PUSHED, UNMERGED", "stranded-override"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not mention %q; got: %s", want, body)
		}
	}
	if reg.Get("a9a19") != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
}

// TestSpawnPolecatRefusedForPreRegistrationBranch is the mg-f3ff / mg-fcb2
// shape, and the expensive one. A worker dispatched here starts from the target,
// writes its predictions after seeing the results, and produces an artifact
// indistinguishable from a valid one — the corruption is silent and survives
// review. Six items were one dispatch away from this on 2026-08-05.
//
// The refusal must not merely fire: it must say something DIFFERENT from the
// resubmit case, because "there is a branch, go look at it" is advice a reader
// discharges by starting a fresh worktree from main.
func TestSpawnPolecatRefusedForPreRegistrationBranch(t *testing.T) {
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-f3ff", "predictions.md",
		"predictions: three of the six checks will fail")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "af3ff", Id: "mg-f3ff", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn onto an item with an unmerged pre-registration commit: status = %d, want 409", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"PRE-REGISTRATION", "never amend", sha[:12], "polecat-f3ff"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not mention %q — a reader acting on it would branch from the "+
				"target and destroy the control; got: %s", want, body)
		}
	}
	// The distinguishing property: this refusal is NOT the resubmit refusal.
	if strings.Contains(body, "mg-9a19 lost 1026 lines") {
		t.Error("the pre-registration case rendered the ordinary resubmit refusal; the two cases " +
			"need opposite handling and a shared message gives the wrong one")
	}
}

// --- The doctor insight, encoded ---------------------------------------------

// TestStrandedWorkRefusalIgnoresRunningPolecat is the finding doctor wrote into
// mg-b468 after missing three of the six affected items:
//
//	"a polecat is running" is NOT evidence that an item has no stranded pushed
//	work — it is the PRECONDITION for it, because the re-dispatch IS the
//	running polecat.
//
// So: a live polecat is registered against the item, and the gate must still
// refuse. A check that took liveness as a reason to stop looking would pass this
// test only by not asking the question.
func TestStrandedWorkRefusalIgnoresRunningPolecat(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := newDrainTestRegistry(t)
	// The re-dispatch that is already in flight, on this very item.
	running := livePolecat("a9a19", "mg-9a19")
	running.SourceRepo = repo
	reg.agents["a9a19"] = running

	refusal := reg.strandedWorkRefusal("mg-9a19", repo, "main")
	if refusal == "" {
		t.Fatal("the gate went quiet because a polecat is running on the item — that is the " +
			"precondition for stranded work, not evidence against it")
	}
	if !strings.Contains(refusal, "polecat-9a19") {
		t.Errorf("refusal does not name the stranded branch: %s", refusal)
	}
}

// --- Negative controls -------------------------------------------------------

// TestSpawnPolecatAllowedWhenBranchIsMerged. The refinery merges by rebase, so
// every healthy merged branch has its commits upstream under different shas. A
// gate that refused those would refuse essentially every dispatch in a repo with
// history — and would be disarmed within the day.
func TestSpawnPolecatAllowedWhenBranchIsMerged(t *testing.T) {
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")
	// main takes the patch under a new sha, exactly as a rebase-then-ff merge does.
	writeCommit(t, repo, "other.md", "chore: main moved on")
	gitRun(t, repo, "cherry-pick", sha)
	gitRun(t, repo, "push", "-q", "origin", "main")

	reg := newDrainTestRegistry(t)
	if refusal := reg.strandedWorkRefusal("mg-9a19", repo, "main"); refusal != "" {
		t.Fatalf("the gate refused a dispatch whose branch is already merged: %s", refusal)
	}
}

// TestSpawnPolecatAllowedForUnrelatedItem. A repo full of other polecats'
// stranded branches must not refuse a dispatch onto an item none of them belong
// to.
func TestSpawnPolecatAllowedForUnrelatedItem(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")
	pushBranch(t, repo, "polecat-f3ff", "predictions.md", "predictions: two of six fail")

	reg := newDrainTestRegistry(t)
	if refusal := reg.strandedWorkRefusal("mg-7c31", repo, "main"); refusal != "" {
		t.Fatalf("the gate refused a dispatch onto an unrelated item: %s", refusal)
	}
}

// TestStrandedWorkGateFailsOpen. No id, no repo, and a repo that is not a git
// repository at all must all dispatch. A gate that refused whenever it could not
// answer would halt the fleet over a git error — and `--id` is optional by
// design, so failing closed there would refuse every id-less spawn outright.
func TestStrandedWorkGateFailsOpen(t *testing.T) {
	reg := newDrainTestRegistry(t)
	notARepo := t.TempDir()
	for _, tc := range []struct{ name, id, repo string }{
		{"no id", "", "/repo"},
		{"no repo", "mg-9a19", ""},
		{"not a git repo", "mg-9a19", notARepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if refusal := reg.strandedWorkRefusal(tc.id, tc.repo, ""); refusal != "" {
				t.Errorf("gate refused when it could not answer: %s", refusal)
			}
		})
	}
}

// TestStrandedWorkGateStaysArmedWhenOriginIsUnreachable. The gate refreshes
// remote-tracking refs before it scans, and the refresh is best-effort on
// purpose: the incident was a network outage, the polecats were stopped while it
// was still on, and a gate that stands down without a successful fetch is off in
// the one window it was built for.
func TestStrandedWorkGateStaysArmedWhenOriginIsUnreachable(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")
	gitRun(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	reg := newDrainTestRegistry(t)
	if refusal := reg.strandedWorkRefusal("mg-9a19", repo, "main"); refusal == "" {
		t.Fatal("the gate went quiet because origin was unreachable — that is the condition " +
			"the whole incident happened under")
	}
}

// --- The override ------------------------------------------------------------

// TestStrandedOverrideDispatches. Attribution is heuristic — a branch name or a
// commit-subject id — so this gate can be wrong about whose work a branch is. A
// refusal with no way past it becomes a wedge, and a wedge under time pressure
// is resolved by disarming the gate rather than by overriding it. The override
// costs a written reason and is recorded beside the refusal it bypassed.
func TestStrandedOverrideDispatches(t *testing.T) {
	logPath := useTempEventLog(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := newDrainTestRegistry(t)
	// Same item, same repo, same gate as TestSpawnPolecatRefusedForStrandedPushedWork
	// — which asserts this exact spawn is refused. The only difference is the
	// override, so a pass here cannot be the gate having gone quiet.
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "a9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
		StrandedOverride: "branch is a stale duplicate; the real work merged as 9072f34",
	})
	if rr.Code == http.StatusConflict {
		t.Fatalf("--stranded-override did not get past the stranded-work gate: %s", rr.Body.String())
	}

	ev := findEvent(readEventLines(t, logPath), "dispatch_stranded_work_overridden", "cat-a9a19")
	if ev == nil {
		t.Fatal("the override left no dispatch_stranded_work_overridden event: " +
			"an unrecorded override is the silent bypass this gate exists to end")
	}
	details, _ := ev["details"].(map[string]any)
	if reason, _ := details["reason"].(string); !strings.Contains(reason, "stale duplicate") {
		t.Errorf("event details.reason = %q, want the operator's stated reason", reason)
	}
	if refusal, _ := details["refusal"].(string); !strings.Contains(refusal, "PUSHED, UNMERGED") {
		t.Errorf("event details.refusal = %q, want the bypassed refusal verbatim", refusal)
	}
}

// --- Attribution -------------------------------------------------------------

func TestAttributableTo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		f      strandedwork.Finding
		id     string
		want   bool
		reason string
	}{
		{
			name: "commit subject names the item",
			f:    strandedwork.Finding{Branch: "polecat-zzzz", WorkItemID: "mg-9a19"},
			id:   "mg-9a19", want: true,
			reason: "the commit convention is the reliable route for finished work",
		},
		{
			name: "branch name carries the id suffix",
			f:    strandedwork.Finding{Branch: "polecat-f3ff"},
			id:   "mg-f3ff", want: true,
			reason: "a pre-registration commit's subject is a prediction and names no item",
		},
		{
			name: "branch name carries a prefixed id suffix",
			f:    strandedwork.Finding{Branch: "polecat-wb468"},
			id:   "mg-b468", want: true,
			reason: "pogod hands out prefixed names when the bare suffix is taken",
		},
		{
			name: "unrelated branch",
			f:    strandedwork.Finding{Branch: "polecat-7c31", WorkItemID: "mg-7c31"},
			id:   "mg-9a19", want: false,
		},
		{
			name: "empty id matches nothing",
			f:    strandedwork.Finding{Branch: "polecat-9a19", WorkItemID: "mg-9a19"},
			id:   "", want: false,
		},
		{
			name: "a two-character suffix does not match everything",
			f:    strandedwork.Finding{Branch: "polecat-9a19"},
			id:   "mg-a1", want: false,
			reason: "a gate that refuses every branch in the repo gets disarmed, not obeyed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AttributableTo(tc.f, tc.id); got != tc.want {
				t.Errorf("AttributableTo(%q, %q) = %v, want %v — %s",
					tc.f.Branch, tc.id, got, tc.want, tc.reason)
			}
		})
	}
}

// --- The stop path -----------------------------------------------------------

// stubReleaser records whether the release was attempted.
type stubReleaser struct {
	called   bool
	released bool
	err      error
}

func (s *stubReleaser) ReleaseClaim(string) (bool, error) {
	s.called = true
	return s.released, s.err
}

// TestReleasePolecatClaimReportsStrandedWork. The stop is where the item's
// description goes wrong: it re-enters available/ indistinguishable from work
// nobody started. The claim release still has to happen — refusing it would
// trade this failure for mg-fb13's, an item stranded in claimed/ under a dead
// pid where neither dispatch nor stall-watch can see it — so the requirement is
// that the stop STOPS BEING SILENT, not that it stops.
func TestReleasePolecatClaimReportsStrandedWork(t *testing.T) {
	logPath := useTempEventLog(t)
	logs := captureLog(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := newDrainTestRegistry(t)
	rel := &stubReleaser{released: true}
	reg.SetClaimReleaser(rel)

	a := livePolecat("9a19", "mg-9a19")
	a.SourceRepo = repo
	released, err := reg.releasePolecatClaim(a, "agent_stopped")
	if err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	if !rel.called || !released {
		t.Fatalf("the report blocked the claim release (called=%v released=%v) — that trades "+
			"this defect for mg-fb13's, which is worse", rel.called, released)
	}

	ev := findEvent(readEventLines(t, logPath), "work_item_stranded_push", "cat-9a19")
	if ev == nil {
		t.Fatal("stopping a polecat with pushed unmerged work emitted no work_item_stranded_push " +
			"event: the item went back to available/ describing itself as unstarted")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["branch"].(string); got != "polecat-9a19" {
		t.Errorf("event details.branch = %q, want polecat-9a19", got)
	}
	if got, _ := details["disposition"].(string); got != string(strandedwork.DispositionResubmit) {
		t.Errorf("event details.disposition = %q, want %q", got, strandedwork.DispositionResubmit)
	}
	if pushed, _ := details["pushed"].(bool); !pushed {
		t.Error("event details.pushed = false; the work was on origin")
	}
	if out := logs(); !strings.Contains(out, "PUSHED WORK BEHIND IT") {
		t.Errorf("the stop was silent in the log; got: %s", out)
	}
}

// TestReleasePolecatClaimSilentWhenNothingStranded. The other half of the
// control: a polecat whose branch merged (or who never pushed anything) must not
// emit the alarm. An event on every stop is an event nobody reads.
func TestReleasePolecatClaimSilentWhenNothingStranded(t *testing.T) {
	logPath := useTempEventLog(t)
	repo := strandedRepo(t)

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("9a19", "mg-9a19") // no branch was ever created
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		if ev := findEvent(readEventLines(t, logPath), "work_item_stranded_push", "cat-9a19"); ev != nil {
			t.Fatalf("a polecat with no branch emitted work_item_stranded_push: %v", ev)
		}
	}
}

// TestReleasePolecatClaimSaysSoWhenItCannotCheck. "Could not check" must never
// be logged as "nothing was stranded" — that mapping is the shape of the defect
// this whole change closes, one level up.
func TestReleasePolecatClaimSaysSoWhenItCannotCheck(t *testing.T) {
	logs := captureLog(t)
	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("9a19", "mg-9a19")
	a.SourceRepo = t.TempDir() // not a git repository
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	out := logs()
	if !strings.Contains(out, "could NOT check") {
		t.Errorf("an unanswerable check logged nothing distinguishable from a clean one; got: %s", out)
	}
	if !strings.Contains(out, "NOT a report that it is unstarted") {
		t.Errorf("the log does not say what the silence means; got: %s", out)
	}
}

// TestStrandedFindingsSurfacesScanErrors. A repo that cannot be scanned at all
// must produce an error the caller can log, not an empty "nothing stranded".
func TestStrandedFindingsSurfacesScanErrors(t *testing.T) {
	_, err := GitStrandedWorkGate{}.StrandedFindings("mg-9a19", t.TempDir(), "")
	if err == nil {
		t.Fatal("scanning a non-repository returned no error; the caller cannot tell " +
			"'could not look' from 'looked and found nothing'")
	}
	if !strings.Contains(err.Error(), "stranded polecat branches") {
		t.Errorf("error does not say what failed: %v", err)
	}
}

// TestSpawnAtAReviewerItemIsNotRefusedForThePointerBranch is the third reader of
// the same defect (mg-1af2). The dispatch gate attributes a branch to an item by
// NAME as well as by commit subject, so polecat-p1c60 is attributed to mg-1c60
// even though every commit on it names mg-aaf6. Before the carried disposition
// that meant a review item could never be dispatched at a second time: the
// reviewer's own pointer branch refused it, and the refusal's advice — resubmit
// that branch — would have double-submitted the builder's work.
func TestSpawnAtAReviewerItemIsNotRefusedForThePointerBranch(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-paaf6", "workitem.go",
		"feat(workitem): a review ticket DECLARES the build item it reviews (mg-aaf6)")
	gitRun(t, repo, "branch", "polecat-p1c60", "polecat-paaf6")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "r1c60", Id: "mg-1c60", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code == http.StatusConflict {
		t.Fatalf("spawn at the REVIEW item was refused because of its own pointer branch: %s", rr.Body.String())
	}

	// The builder's item is still refused, which is the case the gate exists for.
	rr = spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "raaf6", Id: "mg-aaf6", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn at the BUILDER's item with pushed unmerged work: status = %d, want 409 — "+
			"the mg-1af2 fix must not disarm the mg-9a19 refusal", rr.Code)
	}
}

// --- The LOCAL-ONLY case: covered by the scan, misreported by the message -----

// worktreeBranch reproduces how a polecat's branch actually comes into being:
// `git worktree add -b polecat-<name>` in the SOURCE repo. The branch head
// therefore lives in the source repo's ref namespace even though the commits
// were made in a directory somewhere else entirely, which is the fact the whole
// local-only question turns on. Nothing is pushed.
func worktreeBranch(t *testing.T, repo, branch, file, subject string) (sha, tree string) {
	t.Helper()
	tree = filepath.Join(t.TempDir(), branch)
	gitRun(t, repo, "worktree", "add", "-q", "-b", branch, tree, "main")
	return writeCommit(t, tree, file, subject), tree
}

// TestSpawnRefusedForLocalOnlyPreRegistration is mg-0fc6 reproduced at the
// moment BEFORE the push, which is the state mg-bfe0 was filed about.
//
// mg-bfe0's premise was that the guard is defined over pushed branches and a
// local-only pre-registration is invisible to it — that the refusal on the night
// of 2026-08-13 fired only because mayor happened to push the branch minutes
// earlier. THAT PREMISE IS FALSE, and this test is where it is nailed down so it
// cannot be re-derived: strandedwork.Scan enumerates refs/heads as well as
// refs/remotes/origin, resolveBranchRef falls back to the local head, and a
// polecat worktree's branch is in the source repo's ref namespace by
// construction. The refusal fires with nothing on origin at all.
//
// The coverage is not accidental either, which is the part worth writing down:
// git REFUSES to delete a branch that a worktree has checked out ("cannot delete
// branch 'polecat-p0fc6' used by worktree at ..."), so a PRESERVED worktree pins
// its branch ref for as long as it exists. The population mg-bfe0 was worried
// about is therefore the population whose ref is hardest to lose.
//
// What was true is one layer up, and TestLocalOnlyRefusalDoesNotClaimItIsPushed
// is that half.
func TestSpawnRefusedForLocalOnlyPreRegistration(t *testing.T) {
	repo := strandedRepo(t)
	sha, _ := worktreeBranch(t, repo, "polecat-p0fc6", "predictions.md",
		"predictions: three of the six scoping checks will fail")

	// The control: nothing whatsoever is on origin. Without this line the test
	// would still pass against a pushed-only guard.
	if remote := gitRun(t, repo, "ls-remote", "origin", "refs/heads/polecat-*"); strings.TrimSpace(remote) != "" {
		t.Fatalf("fixture pushed the branch, so this is not the local-only case: %q", remote)
	}

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "q0fc6", Id: "mg-0fc6", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn onto an item with an UNPUSHED pre-registration commit: status = %d, want 409 — "+
			"this is the population mg-bfe0 believed the gate could not see", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"PRE-REGISTRATION", "never amend", sha[:12], "polecat-p0fc6"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not mention %q; got: %s", want, body)
		}
	}
}

// TestLocalOnlyRefusalDoesNotClaimItIsPushed is the half of mg-bfe0 that was
// real, and it is a defect about what the refusal SAYS rather than whether it
// fires.
//
// Every refusal used to open "already has PUSHED, UNMERGED work" and prescribe
// `pogo refinery submit <branch>`. On a local-only branch both halves are wrong,
// and wrong in the dangerous direction:
//
//   - "PUSHED" tells the reader the work is durable and discoverable, when it
//     exists in one worktree on one host that git-gc reaps. That is the more
//     urgent case being reported as the less urgent one.
//   - `pogo refinery submit` REFUSES a branch that is not on origin (mg-586d),
//     so the one command offered cannot run. A reader who pastes it gets a
//     rejection and no instruction about what to do instead.
func TestLocalOnlyRefusalDoesNotClaimItIsPushed(t *testing.T) {
	repo := strandedRepo(t)
	worktreeBranch(t, repo, "polecat-p0fc6", "predictions.md",
		"predictions: three of the six scoping checks will fail")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "q0fc6", Id: "mg-0fc6", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	body := rr.Body.String()

	if strings.Contains(body, "PUSHED, UNMERGED") {
		t.Errorf("the refusal calls unpushed work PUSHED — a reader told that concludes the branch "+
			"is safe on origin and recoverable at leisure, which is the opposite of true; got: %s", body)
	}
	if !strings.Contains(body, "LOCAL-ONLY") {
		t.Errorf("the refusal never says the work is local-only; got: %s", body)
	}
	if !strings.Contains(body, "NOT ON ORIGIN") {
		t.Errorf("the refusal never states the urgency (git-gc reaps the worktree); got: %s", body)
	}
	// The remedy has to be runnable. A bare submit is refused by the refinery.
	wantPush := "git -C " + repo + " push origin polecat-p0fc6 && pogo refinery submit"
	if !strings.Contains(body, wantPush) {
		t.Errorf("the remedy does not push first, so the command it prints is one the refinery "+
			"refuses (mg-586d); want %q in: %s", wantPush, body)
	}
	if strings.Contains(body, "(`pogo refinery submit") {
		t.Errorf("a bare submit command survived in the refusal for an unpushed branch; got: %s", body)
	}
}

// TestPushedRefusalIsUnchanged is the negative control for the two above. The
// pushed case is the one the gate has always got right and the one the fleet
// reads most often; a provenance-aware message must not have moved it.
func TestPushedRefusalIsUnchanged(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): the whole battery (mg-9a19)")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "a9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "PUSHED, UNMERGED") {
		t.Errorf("the pushed case lost its wording; got: %s", body)
	}
	if strings.Contains(body, "LOCAL-ONLY") || strings.Contains(body, "NOT ON ORIGIN") {
		t.Errorf("a pushed branch was described as local-only; got: %s", body)
	}
	if strings.Contains(body, "push origin polecat-9a19 &&") {
		t.Errorf("the remedy tells a reader to push a branch that is already on origin; got: %s", body)
	}
}
