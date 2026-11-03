package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs slice 7 (mg-1fe2b): landing through a REAL sandboxed enthd —
// a clean land, a conflict, a stale_tip re-run, a result_mismatch, an answer
// lost mid-land and replayed, a pogod restart mid-land and replayed, the
// already-merged rule, pr_mode refused, a gated tree equal to the landed
// tree, and the git control beside them.

// enthLandFixture is an enthd, a project path recorded as enth, and a gate
// that records what it gated in a log dir outside every tree.
type enthLandFixture struct {
	s    *enthdtest.Server
	repo string // the project path (an enth read checkout)
	log  string // gate log dir
	ctl  string // wrapper control dir
}

// gate script: records the checkout's base (the Change it gates) and a.txt;
// when $LOG/race-id exists, lands that Change on the trunk first (another
// merge winning the race while this one gates).
const enthGateScript = `#!/bin/sh
sed -n '1s/.*"base":"\([0-9a-f]*\)".*/\1/p' .enth/checkout >> "$LOG/gated"
cat a.txt > "$LOG/gated-a.txt"
if [ -f "$LOG/race-id" ]; then
  "$ENTH" --remote "$URL" --repo "$REPO" land $(cat "$LOG/race-id") > /dev/null || exit 9
  rm -f "$LOG/race-id"
fi
[ -f "$LOG/gate-fails" ] && { echo "gate says no"; exit 1; }
exit 0
`

// wrapper enth: passes everything through to the real enth, except that a
// REAL land (not --dry-run) can be made to
//   - mismatch-once: send a wrong --expect-new (→ result_mismatch)
//   - lose-answer-once: land for real, then fail with no answer, as a dropped
//     connection does (outcome unknown to the caller)
const enthWrapperScript = `#!/bin/sh
real=0
case " $* " in *" land "*) case " $* " in *" --dry-run "*) ;; *) real=1 ;; esac ;; esac
if [ $real = 1 ] && [ -f "$CTL/mismatch-once" ]; then
  rm -f "$CTL/mismatch-once"
  n=$#; prev=""
  for a in "$@"; do
    b="$a"
    [ "$prev" = "--expect-new" ] && b="$(cat "$CTL/bogus")"
    set -- "$@" "$b"
    prev="$a"
  done
  shift $n
fi
if [ $real = 1 ] && [ -f "$CTL/lose-answer-once" ]; then
  rm -f "$CTL/lose-answer-once"
  "$REAL" "$@" > /dev/null 2>&1
  echo "connection reset by peer" >&2
  exit 4
fi
exec "$REAL" "$@"
`

func newEnthLandFixture(t *testing.T) *enthLandFixture {
	t.Helper()
	s := enthdtest.Start(t)
	fx := &enthLandFixture{s: s, repo: t.TempDir(), log: t.TempDir(), ctl: t.TempDir()}
	gate := filepath.Join(fx.ctl, "gate.sh")
	if err := os.WriteFile(gate, []byte(enthGateScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fx.writeConfig(t, "quality_gate = \""+gate+"\"\n")
	wrapper := filepath.Join(fx.ctl, "enth")
	if err := os.WriteFile(wrapper, []byte(enthWrapperScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOG", fx.log)
	t.Setenv("CTL", fx.ctl)
	t.Setenv("REAL", s.Enth())
	t.Setenv("ENTH", s.Enth())
	t.Setenv("URL", s.URL)
	t.Setenv("REPO", s.Repo)

	e := s.EnthRepo()
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == fx.repo {
			return vcs.Enth, &e
		}
		return vcs.Git, nil
	})
	old := enthWork
	enthWork = func(e vcs.EnthRepo) enthvcs.Backend { return enthvcs.Backend{Repo: e, Bin: wrapper} }
	saved := networkRetryBackoff
	networkRetryBackoff = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { SetRepoVCS(nil); enthWork = old; networkRetryBackoff = saved })
	return fx
}

func (fx *enthLandFixture) writeConfig(t *testing.T, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(fx.repo, ".pogo"), 0o755)
	if err := os.WriteFile(filepath.Join(fx.repo, ".pogo", "refinery.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fx *enthLandFixture) gated(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fx.log, "gated"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func (fx *enthLandFixture) submit(t *testing.T, r *Refinery, item string) string {
	t.Helper()
	id, err := r.Submit(MergeRequest{RepoPath: fx.repo, Item: item, TargetRef: "main", Author: item})
	if err != nil {
		t.Fatalf("submit %s: %v", item, err)
	}
	return id
}

func (fx *enthLandFixture) process(t *testing.T, r *Refinery, id string) *MergeRequest {
	t.Helper()
	r.processNext()
	mr := r.Get(id)
	if mr == nil {
		t.Fatalf("MR %s vanished", id)
	}
	return mr
}

// fileAt reads path at rev through enthd.
func (fx *enthLandFixture) fileAt(t *testing.T, rev, path string) string {
	t.Helper()
	b, err := enthvcs.Backend{Repo: fx.s.EnthRepo(), Bin: fx.s.Enth()}.Cat(t.Context(), vcs.Repo{}, rev, path)
	if err != nil {
		t.Fatalf("cat %s at %s: %v", path, rev, err)
	}
	return string(b)
}

func TestEnthLandCleanAndGatedTreeIsLandedTree(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, cl := newCallbackRefinery(t)

	// N = submitted: the work's base is the tip.
	c1, _ := fx.s.Work(t, "mg-1", "p1", "one", map[string]string{"x.txt": "x\n"})
	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusMerged || mr.Error != "" {
		t.Fatalf("MR = %s %q; want merged", mr.Status, mr.Error)
	}
	tip := fx.s.Tip(t)
	if mr.Enth == nil || mr.Enth.New != tip || mr.Enth.Old != fx.s.RootID || len(mr.Enth.Changes) != 1 ||
		mr.Enth.Changes[0].Submitted != c1 || mr.Enth.Changes[0].Landed != c1 {
		t.Fatalf("land record = %+v; want %s -> %s with %s landed as itself", mr.Enth, fx.s.RootID, tip, c1)
	}
	if mr.MergedSHA != "" {
		t.Errorf("merged_sha = %q; an enth MR leaves it empty (§7)", mr.MergedSHA)
	}
	if mr.Enth.RequestID != mr.ID+"/1" || mr.EnthPending != nil {
		t.Errorf("request id %q pending %+v; want %s/1 and nothing pending", mr.Enth.RequestID, mr.EnthPending, mr.ID)
	}
	if g := fx.gated(t); len(g) != 1 || g[0] != tip {
		t.Errorf("gated %v; want exactly the landed tip %s", g, tip)
	}
	if len(cl.merged) != 1 {
		t.Errorf("OnMerged fired %d times", len(cl.merged))
	}

	// N ≠ submitted (E1): the trunk moves on after the work was made, so the
	// land rebases it; the gate must have run on the rebased result, which
	// exists only because the dry run stored it.
	c2, _ := fx.s.Work(t, "mg-2", "p2", "two", map[string]string{"a.txt": "a0\na2\n"})
	c3, _ := fx.s.Work(t, "mg-3", "p3", "three", map[string]string{"y.txt": "y\n"})
	fx.s.Land(t, c3)
	mr = fx.process(t, r, fx.submit(t, r, "mg-2"))
	if mr.Status != StatusMerged {
		t.Fatalf("rebased land: %s %q", mr.Status, mr.Error)
	}
	tip = fx.s.Tip(t)
	landed := mr.Enth.Changes[0].Landed
	if landed == c2 || landed != tip || mr.Enth.New != tip {
		t.Fatalf("rebased land record %+v; want c2 landed as a NEW id equal to the tip %s", mr.Enth, tip)
	}
	g := fx.gated(t)
	if g[len(g)-1] != tip {
		t.Errorf("the gate ran on %s, the land landed %s: the gated tree is not the landed tree", g[len(g)-1], tip)
	}
	gatedA, _ := os.ReadFile(filepath.Join(fx.log, "gated-a.txt"))
	if got := fx.fileAt(t, tip, "a.txt"); string(gatedA) != got || got != "a0\na2\n" {
		t.Errorf("a.txt gated %q, landed %q; want both a0/a2", gatedA, got)
	}
	if fx.fileAt(t, tip, "y.txt") != "y\n" {
		t.Error("the land dropped the trunk's own change")
	}
}

func TestEnthLandConflictIsADefectAndLandsNothing(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, cl := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"a.txt": "a1\nMINE\n"})
	theirs, _ := fx.s.Work(t, "mg-9", "p9", "theirs", map[string]string{"a.txt": "a1\nTHEIRS\n"})
	fx.s.Land(t, theirs)
	before := fx.s.Tip(t)

	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusFailed || mr.FailureClass != ClassDefect || len(mr.Attempts) != 1 {
		t.Fatalf("MR = %s class %s attempts %d; want one failed defect attempt", mr.Status, mr.FailureClass, len(mr.Attempts))
	}
	a := mr.Attempts[0]
	if a.Stage != "rebase" || a.Signal != "enth conflict" || a.Retried || !strings.Contains(mr.Error, "a.txt") {
		t.Errorf("attempt = %+v err %q; want stage rebase, enth conflict naming a.txt, not retried", a, mr.Error)
	}
	if fx.s.Tip(t) != before || len(fx.gated(t)) != 0 {
		t.Error("a conflicting MR moved the trunk or reached the gate")
	}
	if len(cl.failed) != 1 || !countsAgainstAuthor(mr.FailureClass) {
		t.Error("a conflict must fail and count against the author, as a git rebase conflict does")
	}
}

func TestEnthLandStaleTipReRunsFromTheDryRun(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, _ := newCallbackRefinery(t)
	mine, _ := fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	other, _ := fx.s.Work(t, "mg-8", "p8", "other", map[string]string{"z.txt": "z\n"})
	// While attempt 1 gates, another merge lands.
	os.WriteFile(filepath.Join(fx.log, "race-id"), []byte(other), 0o644)

	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusMerged {
		t.Fatalf("MR = %s %q", mr.Status, mr.Error)
	}
	if len(mr.Attempts) != 1 || mr.Attempts[0].Signal != "enth stale_tip" || mr.Attempts[0].Class != ClassContention ||
		!mr.Attempts[0].Retried || mr.RecoveredOnAttempt != 2 {
		t.Fatalf("attempts = %+v recovered on %d; want one retried contention (stale_tip), landed on 2", mr.Attempts, mr.RecoveredOnAttempt)
	}
	tip := fx.s.Tip(t)
	g := fx.gated(t)
	// The first gate ran on mine-on-root, which never landed; the re-run
	// re-planned on the new tip and gated THAT (§6.4: a new tip is a new N).
	if len(g) != 2 || g[0] == g[1] || g[1] != tip {
		t.Errorf("gated %v; want two runs, the second on the landed tip %s", g, tip)
	}
	if mr.Enth.Old == fx.s.RootID || mr.Enth.Changes[0].Submitted != mine || mr.Enth.RequestID != mr.ID+"/2" {
		t.Errorf("land record %+v; want landed on the raced tip under request %s/2", mr.Enth, mr.ID)
	}
	if fx.fileAt(t, tip, "z.txt") != "z\n" || fx.fileAt(t, tip, "x.txt") != "x\n" {
		t.Error("the landed tip lacks one of the two lands")
	}
}

func TestEnthLandResultMismatchRetriesAndReusesTheGate(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, _ := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	os.WriteFile(filepath.Join(fx.ctl, "mismatch-once"), nil, 0o644)
	os.WriteFile(filepath.Join(fx.ctl, "bogus"), []byte(fx.s.RootID), 0o644)

	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusMerged {
		t.Fatalf("MR = %s %q", mr.Status, mr.Error)
	}
	if len(mr.Attempts) != 1 || mr.Attempts[0].Signal != "enth result_mismatch" || mr.Attempts[0].Class != ClassContention {
		t.Fatalf("attempts = %+v; want one contention (result_mismatch)", mr.Attempts)
	}
	// Same tip, same Changes → same N: the passed gate is held, not re-run.
	if g := fx.gated(t); len(g) != 1 || g[0] != fx.s.Tip(t) {
		t.Errorf("gated %v; want one run (held across the retry) on the landed tip", g)
	}
	if !strings.Contains(mr.GateOutput, "NOT re-run on attempt 2") {
		t.Errorf("gate output does not say the verdict was held:\n%s", mr.GateOutput)
	}
}

func TestEnthLandLostAnswerIsReplayedNotLandedTwice(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, _ := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	os.WriteFile(filepath.Join(fx.ctl, "lose-answer-once"), nil, 0o644)

	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusMerged {
		t.Fatalf("MR = %s %q", mr.Status, mr.Error)
	}
	if len(mr.Attempts) != 1 || mr.Attempts[0].Class != ClassInfrastructure || !mr.Attempts[0].Retried {
		t.Fatalf("attempts = %+v; want one retried infrastructure failure (the lost answer)", mr.Attempts)
	}
	// Attempt 2 replayed request /1 — which had landed — instead of
	// re-planning under /2, which would have landed the Change again.
	if mr.Enth.RequestID != mr.ID+"/1" {
		t.Errorf("recorded request %q; want the replayed %s/1", mr.Enth.RequestID, mr.ID)
	}
	if mr.Enth.Old != fx.s.RootID || mr.Enth.New != fx.s.Tip(t) {
		t.Errorf("record %+v; the trunk must hold exactly one land on the root (tip %s)", mr.Enth, fx.s.Tip(t))
	}
	if g := fx.gated(t); len(g) != 1 {
		t.Errorf("gated %d times; a replay does not re-gate", len(g))
	}
}

func TestEnthLandCrashRecoveryReplays(t *testing.T) {
	// "landed before the crash": enthd carried the land out, pogod never saw
	// the answer. "never landed, tip moved": the land never reached enthd and
	// another merge landed meanwhile, so the replay is refused stale_tip.
	// (A land that never reached enthd with the tip unmoved simply lands on
	// replay: it was persisted only after its gates passed on that N.)
	for _, landedBeforeCrash := range []bool{true, false} {
		name := "landed before the crash"
		if !landedBeforeCrash {
			name = "never landed, tip moved"
		}
		t.Run(name, func(t *testing.T) {
			fx := newEnthLandFixture(t)
			statePath := filepath.Join(t.TempDir(), "refinery-state.json")
			r1 := newPersistent(t, statePath)
			c1, _ := fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
			id := fx.submit(t, r1, "mg-1")
			mr := r1.Get(id)

			// What attemptEnthLand does up to the land: dry run, then persist
			// the pending land — and then pogod dies, before or after enthd
			// carried it out.
			b := enthvcs.Backend{Repo: fx.s.EnthRepo(), Bin: fx.s.Enth()}
			plan, err := b.DryRun(t.Context(), "main", []string{c1})
			if err != nil {
				t.Fatal(err)
			}
			mr.Status = StatusProcessing
			mr.EnthPending = &EnthPendingLand{RequestID: id + "/1", Target: "main", Tip: plan.Old, New: plan.New,
				Changes: []string{c1}, Attempt: 1, IssuedAt: time.Now()}
			if landedBeforeCrash {
				if _, err := b.LandExpect(t.Context(), "main", []string{c1}, plan.Old, plan.New, id+"/1"); err != nil {
					t.Fatal(err)
				}
			} else {
				other, _ := fx.s.Work(t, "mg-8", "p8", "other", map[string]string{"z.txt": "z\n"})
				fx.s.Land(t, other)
			}
			writeInFlightState(t, statePath, mr)
			afterCrash := fx.s.Tip(t)

			r2 := newPersistent(t, statePath)
			var merged []string
			r2.SetOnMerged(func(m *MergeRequest) { merged = append(merged, m.ID) })
			r2.resolveRecovered()

			if landedBeforeCrash {
				got := r2.Get(id)
				if got == nil || got.Status != StatusMerged || got.Enth == nil || got.Enth.New != afterCrash || got.Enth.RequestID != id+"/1" {
					t.Fatalf("recovered MR = %+v; want merged with the replayed record", got)
				}
				if len(merged) != 1 || fx.s.Tip(t) != afterCrash || len(fx.gated(t)) != 0 {
					t.Errorf("OnMerged %v, tip moved %v, gated %d; want one OnMerged, no second land, no gate",
						merged, fx.s.Tip(t) != afterCrash, len(fx.gated(t)))
				}
				return
			}
			q := r2.Queue()
			if len(q) != 1 || q[0].ID != id || q[0].EnthPending != nil {
				t.Fatalf("queue = %+v; want the MR re-queued with nothing pending", q)
			}
			got := fx.process(t, r2, id)
			if got.Status != StatusMerged || fx.s.Tip(t) == afterCrash || got.Enth.Old != afterCrash {
				t.Fatalf("re-run = %s %q record %+v; want one fresh land", got.Status, got.Error, got.Enth)
			}
		})
	}
}

func TestEnthLandAlreadyMergedRule(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, cl := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	first := fx.submit(t, r, "mg-1")
	second := fx.submit(t, r, "mg-1") // a resubmit after losing track
	if mr := fx.process(t, r, first); mr.Status != StatusMerged {
		t.Fatalf("first = %s %q", mr.Status, mr.Error)
	}
	tip := fx.s.Tip(t)
	mr := fx.process(t, r, second)
	if mr.Status != StatusMerged || !mr.AlreadyMerged || mr.Enth == nil || mr.Enth.New != tip {
		t.Fatalf("resubmit = %s already=%v record %+v; want already_merged carrying the first land", mr.Status, mr.AlreadyMerged, mr.Enth)
	}
	if fx.s.Tip(t) != tip || len(fx.gated(t)) != 1 {
		t.Error("the resubmit landed or gated again")
	}

	// Landed out of band, with no merged MR on record: nothing this MR
	// delivers — a defect, not a merge.
	c2, _ := fx.s.Work(t, "mg-2", "p2", "two", map[string]string{"q.txt": "q\n"})
	id := fx.submit(t, r, "mg-2")
	fx.s.Land(t, c2)
	mr = fx.process(t, r, id)
	if mr.Status != StatusFailed || mr.FailureClass != ClassDefect || mr.AlreadyMerged || mr.Attempts[0].Stage != stageEmptyBranch {
		t.Fatalf("out-of-band = %s class %s already=%v attempts %+v; want a defect at %s", mr.Status, mr.FailureClass, mr.AlreadyMerged, mr.Attempts, stageEmptyBranch)
	}
	if !strings.Contains(mr.Error, "NOTHING WAS DELIVERED") {
		t.Errorf("error %q does not say nothing was delivered", mr.Error)
	}
	if len(cl.failed) != 1 {
		t.Errorf("OnFailed fired %d times", len(cl.failed))
	}
}

func TestEnthLandGateFailureLandsNothing(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, _ := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	os.WriteFile(filepath.Join(fx.log, "gate-fails"), nil, 0o644)
	mr := fx.process(t, r, fx.submit(t, r, "mg-1"))
	if mr.Status != StatusFailed || mr.FailureClass != ClassDefect || fx.s.Tip(t) != fx.s.RootID || mr.EnthPending != nil {
		t.Fatalf("MR = %s class %s pending %+v; want a defect and an unmoved trunk", mr.Status, mr.FailureClass, mr.EnthPending)
	}
}

func TestEnthPRModeRefused(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, _ := newCallbackRefinery(t)
	c1, _ := fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	fx.writeConfig(t, "pr_mode = true\n")
	if _, err := r.Submit(MergeRequest{RepoPath: fx.repo, Item: "mg-1", TargetRef: "main"}); err == nil || !strings.Contains(err.Error(), "pr_mode") {
		t.Fatalf("Submit with pr_mode = %v; want refused", err)
	}
	// An MR already queued when pr_mode appeared is refused at land time.
	mr := &MergeRequest{ID: "mr-pr", RepoPath: fx.repo, Item: "mg-1", TargetRef: "main", EnthSubmitted: []string{c1}}
	if _, err := r.processMerge(mr); err == nil || !strings.Contains(err.Error(), "pr_mode") {
		t.Fatalf("processMerge with pr_mode = %v; want refused", err)
	}
	if mr.FailureClass != ClassDefect || fx.s.Tip(t) != fx.s.RootID {
		t.Errorf("class %s, tip moved %v", mr.FailureClass, fx.s.Tip(t) != fx.s.RootID)
	}
}

// THE GIT CONTROL: with the enth lookup installed and an enth repo landing
// beside it, a git MR goes through the git pipeline exactly as before — a
// merged_sha, no enth record, its gate run in the git clone.
func TestEnthLandGitControl(t *testing.T) {
	fx := newEnthLandFixture(t)
	r, _ := newCallbackRefinery(t)
	fx.s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	if mr := fx.process(t, r, fx.submit(t, r, "mg-1")); mr.Status != StatusMerged || mr.Enth == nil {
		t.Fatalf("enth MR = %s %q", mr.Status, mr.Error)
	}

	gx := newEmptyBranchFixture(t)
	head := gx.branchWithWork(t, "polecat-p1", "a.txt")
	mr := submitAndProcess(t, r, gx.origin, "polecat-p1", "p1")
	if mr.Status != StatusMerged || mr.Enth != nil || mr.EnthPending != nil || mr.MergedSHA == "" || mr.MergedSHA != gx.mainTip(t) {
		t.Fatalf("git MR = %s %q merged_sha %q enth %+v; want a plain git merge", mr.Status, mr.Error, mr.MergedSHA, mr.Enth)
	}
	if head != mr.MergedSHA || gx.gateRuns(t) != 1 {
		t.Errorf("git: head %s merged %s, gate ran %d; want a fast-forward of the head and one gate", head, mr.MergedSHA, gx.gateRuns(t))
	}
}

// The gate lock of an enth gate checkout lives in .enth/ (enth skips it, so it
// is never part of a Change); a git clone's stays in .git/ — including one
// that somehow also holds a .enth.
func TestGateLockPathPerVCS(t *testing.T) {
	enth, git, both := t.TempDir(), t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(enth, ".enth"), 0o755)
	os.Mkdir(filepath.Join(git, ".git"), 0o755)
	os.Mkdir(filepath.Join(both, ".git"), 0o755)
	os.Mkdir(filepath.Join(both, ".enth"), 0o755)
	if got := gateLockPath(enth); got != filepath.Join(enth, ".enth", gateLockFile) {
		t.Errorf("enth gate lock = %s", got)
	}
	for _, d := range []string{git, both} {
		if got := gateLockPath(d); got != filepath.Join(d, ".git", gateLockFile) {
			t.Errorf("git gate lock = %s, want it under .git as before", got)
		}
	}
}
