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
