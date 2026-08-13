package strandwatch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests below run against REAL git repositories for the same reason
// internal/strandedwork's do: every fact this package reports is downstream of
// git's own patch-identity arithmetic, and a faked command runner would only
// prove the parser reads strings the author already believed git emits. What is
// faked is the BOARD and the FLEET — the work items, the running agents, the
// refinery queue — because those are the inputs whose interesting values (an
// item that is available while its work is on main; a polecat that is running
// right now) are states, not commands.

// repo is a throwaway git repository with an origin, built per test.
type repo struct {
	t      *testing.T
	dir    string
	origin string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	r := &repo{t: t, dir: filepath.Join(root, "work"), origin: filepath.Join(root, "origin.git")}

	run(t, root, "git", "init", "--bare", "--initial-branch=main", r.origin)
	run(t, root, "git", "init", "--initial-branch=main", r.dir)
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.git("remote", "add", "origin", r.origin)

	r.commit("README.md", "chore: initial commit")
	r.git("push", "-q", "origin", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	return run(r.t, r.dir, "git", args...)
}

func (r *repo) commit(file, subject string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, file)
	prev, _ := os.ReadFile(path)
	body := string(prev)
	for i := 0; i < 8; i++ {
		body += fmt.Sprintf("%s — substantive content line %02d\n", subject, i)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		r.t.Fatalf("write %s: %v", path, err)
	}
	r.git("add", file)
	r.git("commit", "-q", "-m", subject)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

func (r *repo) branch(name, ref string) {
	r.t.Helper()
	r.git("checkout", "-q", "-b", name, ref)
}

func (r *repo) checkout(ref string) {
	r.t.Helper()
	r.git("checkout", "-q", ref)
}

func (r *repo) push(branch string) {
	r.t.Helper()
	r.git("push", "-q", "origin", branch)
}

// landCleanly replays a branch onto main the way the refinery does — rebase then
// fast-forward — so its commits arrive under NEW shas. Every merged branch in
// production looks like this, and any detector that reports it is unusable.
//
// MAIN IS ADVANCED FIRST, and that is not decoration. If nothing landed in
// between, the rebase is a no-op, the fast-forward moves main to the branch's
// own sha, and the two refs become identical — a state in which `git cherry`
// reports NOTHING, not even an equivalence, because there are no commits unique
// to either side. See classify's note on that limit. The refinery's real target
// is a busy shared branch, so the interesting fixture is the one where it moved.
func (r *repo) landCleanly(branch string) {
	r.t.Helper()
	r.checkout("main")
	r.commit("other.md", "chore: an unrelated change that landed first")
	r.push("main")
	r.checkout(branch)
	r.git("rebase", "-q", "main")
	r.checkout("main")
	r.git("merge", "-q", "--ff-only", branch)
	r.push("main")
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// board builds an Items source over a fixed list.
func board(items ...Item) func() ([]Item, error) {
	return func() ([]Item, error) { return items, nil }
}

// fleet builds a LiveAgents source over a fixed set of names.
func fleet(names ...string) func() (map[string]bool, error) {
	return func() (map[string]bool, error) {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m, nil
	}
}

func rowFor(rep Report, itemID string) (Row, bool) {
	for _, r := range rep.Rows {
		if r.Item.ID == itemID {
			return r, true
		}
	}
	return Row{}, false
}

// --- The row the ticket was filed for ---------------------------------------

// TestStrandedRowOnAnAvailableItem is mg-9a19's shape seen from the sweep rather
// than from the dispatch gate: the polecat pushed finished work, died, and the
// item went back to available/ describing itself as unstarted.
func TestStrandedRowOnAnAvailableItem(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery, all five cases caught (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir, Title: "drift battery"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-9a19")
	if !ok {
		t.Fatalf("no row for mg-9a19; the item is available and its work is pushed and unmerged.\n%s",
			Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Errorf("Kind = %q, want %q", row.Kind, KindStranded)
	}
	if row.Unmerged != 1 {
		t.Errorf("Unmerged = %d, want 1", row.Unmerged)
	}
	if !row.Pushed {
		t.Errorf("Pushed = false; the work is on origin and the remedy depends on that")
	}
	if !strings.Contains(row.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q, want it to name `pogo refinery submit`", row.Remedy())
	}
	if !rep.Actionable() {
		t.Error("Actionable() = false with a stranded row")
	}
}

// TestLandedNotClosedRowIsTheSecondHalf is the mayor's appended case, and the
// one nothing in the fleet could see: the branch MERGED, the item is still
// available, and the spawn-time guard correctly stops refusing the moment the
// merge lands. Remedy is `mg done`, not a resubmit.
func TestLandedNotClosedRowIsTheSecondHalf(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q6c90", "main")
	r.commit("fix.md", "fix(test): two load-sensitive assertions become RELATIVE (mg-6c90)")
	r.push("polecat-q6c90")
	r.landCleanly("polecat-q6c90")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6c90", Status: "available", Repo: r.dir, Title: "load-sensitive assertions"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-6c90")
	if !ok {
		t.Fatalf("no row for mg-6c90; its branch is MERGED and the item is still available — "+
			"this is the state priority-wake advertised four minutes after b9e1d1b landed.\n%s",
			Render(rep, true))
	}
	if row.Kind != KindLandedNotClosed {
		t.Errorf("Kind = %q, want %q", row.Kind, KindLandedNotClosed)
	}
	if row.Unmerged != 0 {
		t.Errorf("Unmerged = %d, want 0 on a landed branch", row.Unmerged)
	}
	if row.Equivalent == 0 {
		t.Errorf("Equivalent = 0; the rebase rewrote the commit and it must still be counted, " +
			"or an empty branch is indistinguishable from a landed one")
	}
	if !strings.Contains(row.Remedy(), "mg done") {
		t.Errorf("Remedy() = %q, want it to name `mg done` — the OPPOSITE action from the "+
			"stranded row, which is why the two are separate kinds", row.Remedy())
	}
}

// TestConflictRebaseIsNeitherRemedy is the third kind, on the fixture that
// reproduces the `git cherry` blind spot. It must not say "resubmit" (the branch
// is merged) and it must not say "close" (the evidence is a heuristic and being
// wrong that way discards the branch).
func TestConflictRebaseIsNeitherRemedy(t *testing.T) {
	r := newRepo(t)
	// Enough distinct lines that the content measure clears its confidence floor.
	var base []string
	for i := 0; i < 40; i++ {
		base = append(base, fmt.Sprintf("the original line number %02d of the shared document", i))
	}
	writeFile(t, r, "doc.md", strings.Join(base, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "chore: the shared document")
	r.push("main")
	mainBase := strings.TrimSpace(r.git("rev-parse", "HEAD"))

	var work []string
	for i := 0; i < 25; i++ {
		work = append(work, fmt.Sprintf("the branch contributes substantial line %02d of real work", i))
	}
	branchLines := append(append([]string{}, base[:20]...), append(append([]string{}, work...), base[20:]...)...)
	r.branch("polecat-qcf01", mainBase)
	writeFile(t, r, "doc.md", strings.Join(branchLines, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): the branch's contribution (mg-cf01)")
	r.push("polecat-qcf01")

	r.checkout("main")
	var other []string
	for i := 0; i < 6; i++ {
		other = append(other, fmt.Sprintf("meanwhile main inserted unrelated line %02d right here", i))
	}
	mainLines := append(append([]string{}, base[:20]...), append(append([]string{}, other...), base[20:]...)...)
	writeFile(t, r, "doc.md", strings.Join(mainLines, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): main's unrelated change")
	r.push("main")

	// Resolve by keeping both, which is what resolving this conflict means.
	resolved := append(append([]string{}, mainLines[:26]...), append(append([]string{}, work...), mainLines[26:]...)...)
	writeFile(t, r, "doc.md", strings.Join(resolved, "\n")+"\n")
	r.git("add", "doc.md")
	r.git("commit", "-q", "-m", "feat(doc): the branch's contribution (mg-cf01)")
	r.push("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-cf01", Status: "available", Repo: r.dir, Title: "the contribution"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-cf01")
	if !ok {
		t.Fatalf("no row for mg-cf01 at all; `git cherry` should still be calling this unmerged.\n%s",
			Render(rep, true))
	}
	if row.Kind != KindConflictSuspect {
		t.Fatalf("Kind = %q, want %q. The branch landed through a conflict: %s\n%s",
			row.Kind, KindConflictSuspect, row.Presence.Describe(), Render(rep, true))
	}
	if strings.Contains(row.Remedy(), "refinery submit") || strings.Contains(row.Remedy(), "mg done") {
		t.Errorf("Remedy() = %q, and it must name NEITHER action: the instruments disagree, and "+
			"one of the two remedies throws the branch away", row.Remedy())
	}
	out := Render(rep, false)
	if !strings.Contains(out, "VERIFY BEFORE ACTING") {
		t.Errorf("the rendered report does not tell the reader to verify:\n%s", out)
	}
}

func writeFile(t *testing.T, r *repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// --- The exclusions, each of which looks identical to a finding --------------

// TestRunningPolecatIsExcluded is the noise case the ticket named: polecat-qfa70
// was mid-flight during the mayor's manual sweep and was indistinguishable from
// a strand. Every live polecat has unmerged commits on a claimed item, because
// that is what work in progress is.
func TestRunningPolecatIsExcluded(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-qfa70", "main")
	r.commit("wip.md", "wip: halfway through (mg-fa70)")
	r.push("polecat-qfa70")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-fa70", Status: "claimed", Repo: r.dir, Title: "in flight"}),
		LiveAgents: fleet("qfa70"),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := rowFor(rep, "mg-fa70"); ok {
		t.Errorf("a RUNNING polecat's branch was reported as stranded; every live worker in the "+
			"fleet would appear in this report and the reader would learn to skip it\n%s", Render(rep, true))
	}
	if len(rep.Excluded) != 1 {
		t.Fatalf("Excluded = %d entries, want 1: a suppression nobody can see is "+
			"indistinguishable from a miss", len(rep.Excluded))
	}
	if !strings.Contains(rep.Excluded[0].Reason, "running") {
		t.Errorf("exclusion reason = %q, want it to say the polecat is running", rep.Excluded[0].Reason)
	}
}

// TestRunningPolecatIsExcludedWhenItsBranchNameDiffers covers the other half of
// the liveness test: pogod hands out a prefixed agent name when the bare suffix
// is taken, so the branch of a live polecat need not be the branch the item's id
// would predict.
func TestRunningPolecatIsExcludedWhenItsBranchNameDiffers(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-b468", "main")
	r.commit("wip.md", "wip: halfway through (mg-b468)")
	r.push("polecat-b468")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-b468", Status: "claimed", Repo: r.dir}),
		LiveAgents: fleet("wb468"), // same item, different agent name
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := rowFor(rep, "mg-b468"); ok {
		t.Errorf("polecat wb468 is running on mg-b468 and an older branch of the same item was "+
			"still reported\n%s", Render(rep, true))
	}
}

// TestQueuedBranchIsExcluded: the remedy for a stranded branch is to submit it,
// and it is already submitted. The refinery has no dedup, so a second submit is
// a duplicate MR.
func TestQueuedBranchIsExcluded(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q56ac", "main")
	r.commit("fix.md", "fix(deploy): bounded git steps (mg-56ac)")
	r.push("polecat-q56ac")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-56ac", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		QueuedBranches: func() (map[string]bool, error) {
			return map[string]bool{QueueKey(r.dir, "polecat-q56ac"): true}, nil
		},
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := rowFor(rep, "mg-56ac"); ok {
		t.Errorf("a branch already in the refinery queue was reported, and its remedy is to "+
			"submit it again\n%s", Render(rep, true))
	}
}

// TestQueueKeyIsPerRepo: a bare branch name is ambiguous across the three repos
// this fleet works, and polecat branch names come from 4-hex work-item ids.
func TestQueueKeyIsPerRepo(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q56ac", "main")
	r.commit("fix.md", "fix(deploy): bounded git steps (mg-56ac)")
	r.push("polecat-q56ac")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-56ac", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		QueuedBranches: func() (map[string]bool, error) {
			return map[string]bool{QueueKey("/some/other/repo", "polecat-q56ac"): true}, nil
		},
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if _, ok := rowFor(rep, "mg-56ac"); !ok {
		t.Errorf("a same-named branch queued in a DIFFERENT repository suppressed this finding\n%s",
			Render(rep, true))
	}
}

// --- The healthy inputs a detector must stay silent on -----------------------

// TestCleanlyMergedBranchOnAClosedItemIsSilent is the negative control that
// `rev-list target..branch` fails: the refinery rebases, so every landed
// branch's commits are absent by sha forever. The item is not open either, so
// there is nothing to say at all.
func TestCleanlyMergedBranchOnAClosedItemIsSilent(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-qaaa1", "main")
	r.commit("done.md", "feat: finished and merged (mg-aaa1)")
	r.push("polecat-qaaa1")
	r.landCleanly("polecat-qaaa1")

	// The board holds no open item for it — the ordinary end state.
	rep, err := Scan(Options{
		Items:      board(),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Rows) != 0 {
		t.Errorf("a merged branch on a closed item produced %d row(s)\n%s", len(rep.Rows), Render(rep, true))
	}
}

// TestEmptyBranchIsNotLanded: a polecat spawned thirty seconds ago has a branch
// with nothing on it. Without the Equivalent>0 term, every open item that ever
// had a polecat spawned at it reports as work waiting to be closed — which would
// be ~every claimed item in the fleet.
func TestEmptyBranchIsNotLanded(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-qbbb2", "main")
	r.push("polecat-qbbb2")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-bbb2", Status: "claimed", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if row, ok := rowFor(rep, "mg-bbb2"); ok {
		t.Errorf("an EMPTY branch was reported as %q; a freshly spawned polecat has one, and "+
			"the report would name every claimed item in the fleet\n%s", row.Kind, Render(rep, true))
	}
}

// TestItemWithNoBranchIsSilent: the overwhelming majority of open items.
func TestItemWithNoBranchIsSilent(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-ffff", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Rows) != 0 {
		t.Errorf("an item with no branch produced %d row(s)\n%s", len(rep.Rows), Render(rep, true))
	}
}

// --- Blindness, ranking, coverage --------------------------------------------

// TestNoLivenessIsFatal. Without it every running polecat reads as a strand, so
// the report would name the whole live fleet and tell the reader to resubmit
// half-finished branches. A detector that fires on healthy input teaches its
// readers to skip the line the real stranding surfaces on.
func TestNoLivenessIsFatal(t *testing.T) {
	r := newRepo(t)
	_, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir}),
		LiveAgents: func() (map[string]bool, error) { return nil, errors.New("pogod unreachable") },
		Target:     "main",
	})
	if !errors.Is(err, ErrNoLiveness) {
		t.Fatalf("Scan error = %v, want ErrNoLiveness: an unreachable registry must not produce a report", err)
	}
}

// TestUnreadableQueueIsStatedNotFatal. The refinery being down is common, and
// the rest of the answer is still worth having — but the report must SAY the
// exclusion could not be applied, or a reader takes a possibly-already-submitted
// row at face value.
func TestUnreadableQueueIsStatedNotFatal(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:          board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir}),
		LiveAgents:     fleet(),
		QueuedBranches: func() (map[string]bool, error) { return nil, errors.New("connection refused") },
		Target:         "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v — an unreadable queue must not be fatal", err)
	}
	if rep.QueueUnreadable == "" {
		t.Error("QueueUnreadable is empty after the queue source failed")
	}
	if out := Render(rep, false); !strings.Contains(out, "UNREADABLE") {
		t.Errorf("the report does not disclose that the queue could not be consulted:\n%s", out)
	}
}

// TestUnreadableBranchIsUnjudgedNotClean is the trap the coordinator flagged
// mid-build, measured on the sibling ticket mg-b6d1: the natural shape of this
// predicate — `git cherry <target> <branch> | grep -q '^+'` — answers LANDED
// whenever git FAILS, because a failed git prints nothing and "no output" is how
// that predicate spells clean.
//
// For a sweep that is the dangerous direction. `landed`/clean means "this branch
// needs nothing", so one transient git failure converts a stranded branch into a
// silent all-clear over work sitting unmerged — this ticket's own defect rebuilt
// inside its own remedy. The fleet has measured ~40-minute connectivity waves, so
// it is not hypothetical.
//
// The target is unresolvable here, which is the same failure qb6d1 measured.
func TestUnreadableBranchIsUnjudgedNotClean(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "no-such-target-ref",
	})
	if err != nil {
		t.Fatalf("Scan: %v — one unreadable branch must not abort the sweep", err)
	}
	row, ok := rowFor(rep, "mg-9a19")
	if !ok {
		t.Fatalf("the branch vanished from the report when it could not be judged; a sweep that "+
			"drops what it cannot read reports all-clear over stranded work\n%s", Render(rep, true))
	}
	if row.Kind != KindUnjudged {
		t.Errorf("Kind = %q, want %q", row.Kind, KindUnjudged)
	}
	if row.Error == "" {
		t.Error("Error is empty; the row must carry why it could not be judged")
	}
	if !rep.Actionable() {
		t.Error("Actionable() = false with an unjudged row; the exit code would be 0 and a " +
			"schedule would read the run as clean")
	}
	out := Render(rep, false)
	for _, want := range []string{"UNJUDGED", "NOT a clean row"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mark the row as unjudged (%q missing):\n%s", want, out)
		}
	}
	if strings.Contains(out, "mg done") {
		t.Error("the report recommends closing an item whose branch nobody could read")
	}
}

// TestUnjudgedOutranksLandedInTheOrdering: an unjudged row might BE a stranded
// one, so it must not be sorted below the rows that are known-harmless.
func TestUnjudgedOutranksLandedInTheOrdering(t *testing.T) {
	if KindUnjudged.Rank() >= KindLandedNotClosed.Rank() {
		t.Errorf("unjudged (%d) sorts at or below landed_not_closed (%d)",
			KindUnjudged.Rank(), KindLandedNotClosed.Rank())
	}
	if KindStranded.Rank() >= KindUnjudged.Rank() {
		t.Errorf("stranded (%d) does not lead unjudged (%d)", KindStranded.Rank(), KindUnjudged.Rank())
	}
}

// TestRowsRankOnItemStatus is the ticket's explicit instruction: "an unmerged
// branch whose item is archived is mostly harmless, while one whose item is
// available or claimed is work about to be redone. Rank on item status, not on
// branch count."
func TestRowsRankOnItemStatus(t *testing.T) {
	r := newRepo(t)
	for _, b := range []struct{ branch, item string }{
		{"polecat-qc111", "mg-c111"}, // claimed
		{"polecat-qa222", "mg-a222"}, // available
	} {
		r.branch(b.branch, "main")
		r.commit(b.item+".md", "feat: work ("+b.item+")")
		r.push(b.branch)
		r.checkout("main")
	}

	rep, err := Scan(Options{
		Items: board(
			Item{ID: "mg-c111", Status: "claimed", Repo: r.dir},
			Item{ID: "mg-a222", Status: "available", Repo: r.dir},
		),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("Rows = %d, want 2\n%s", len(rep.Rows), Render(rep, true))
	}
	if rep.Rows[0].Item.Status != "available" {
		t.Errorf("first row is %q; `available` must lead, because that is the status "+
			"priority-wake advertises and dispatch is what destroys the branch", rep.Rows[0].Item.Status)
	}
}

// TestArchivedItemsAreNotScanned pins the scope decision that keeps the report
// readable: the same store that yields 57 branch-first rows yields a handful
// item-first, and the difference is almost entirely archived items.
func TestArchivedItemsAreNotScanned(t *testing.T) {
	for _, status := range []string{"archived", "shelved", "done"} {
		for _, open := range OpenStatuses {
			if status == open {
				t.Errorf("%q is in OpenStatuses; the sweep would report the archived majority "+
					"the ticket explicitly bounded out", status)
			}
		}
	}
}

// TestCoverageIsReportedOnACleanRun. "Nothing to report" and "nothing looked at"
// are the two readings this whole detector exists to keep apart, so the counts
// print even when there are no findings.
func TestCoverageIsReportedOnACleanRun(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items: board(
			Item{ID: "mg-ffff", Status: "available", Repo: r.dir},
			Item{ID: "mg-eeee", Status: "available"}, // no repo at all
		),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.ItemsScanned != 2 {
		t.Errorf("ItemsScanned = %d, want 2", rep.ItemsScanned)
	}
	if rep.ItemsWithoutRepo != 1 {
		t.Errorf("ItemsWithoutRepo = %d, want 1: an item naming no repo is a coverage gap, "+
			"not a clean verdict", rep.ItemsWithoutRepo)
	}
	out := Render(rep, false)
	for _, want := range []string{"2 open work item(s) scanned", "name no repo", "No open work item has work"} {
		if !strings.Contains(out, want) {
			t.Errorf("clean report is missing %q:\n%s", want, out)
		}
	}
}

// TestBlindRunIsNotACleanRun: zero items scanned must not read as "nothing
// stranded". That conflation is the exact failure mode of the board this
// detector reads.
func TestBlindRunIsNotACleanRun(t *testing.T) {
	rep, err := Scan(Options{Items: board(), LiveAgents: fleet()})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if out := Render(rep, false); !strings.Contains(out, "blind run") {
		t.Errorf("a run that scanned zero items rendered as clean:\n%s", out)
	}
}

// TestRepoFilterRestrictsTheSweep.
func TestRepoFilterRestrictsTheSweep(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Repos:      []string{"/somewhere/else"},
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Rows) != 0 {
		t.Errorf("--repo filter did not exclude %s\n%s", r.dir, Render(rep, true))
	}
}

// TestPreRegistrationIsCarriedIntoTheRow. The disposition whose advice must not
// be crowded out: a worker branching from the target writes its predictions
// after seeing the results, and the artifact is indistinguishable from a valid
// one.
func TestPreRegistrationIsCarriedIntoTheRow(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("prereg.md", "predictions: three named outcomes before the analysis (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-9a19")
	if !ok {
		t.Fatalf("no row\n%s", Render(rep, true))
	}
	if row.PreRegistration == nil {
		t.Fatal("PreRegistration is nil on a branch whose unmerged commit is a pre-registration")
	}
	if out := Render(rep, false); !strings.Contains(out, "PRE-REGISTRATION") {
		t.Errorf("the report does not surface the pre-registration commit:\n%s", out)
	}
}

// TestReviewerPointerBranchIsExcluded is the mg-1af2 shape as this sweep sees
// it. A review polecat reviews by checking the branch under review out, so its
// own worktree branch points at the builder's head — every commit on it is work
// the target does not have, and every one of them is the builder's. Reported as
// `stranded`, the remedy this sweep prints (`refinery submit polecat-p1c60`)
// submits mg-aaf6's work a second time under mg-1c60's authorship.
//
// It is EXCLUDED rather than dropped, for the same reason a running polecat is:
// a suppression nobody can see is indistinguishable from a miss.
func TestReviewerPointerBranchIsExcluded(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-paaf6", "main")
	r.commit("workitem.go", "feat(workitem): a review ticket DECLARES the build item it reviews (mg-aaf6)")
	r.push("polecat-paaf6")
	r.branch("polecat-p1c60", "polecat-paaf6")
	r.checkout("main")

	rep, err := Scan(Options{
		Items: board(
			Item{ID: "mg-1c60", Status: "available", Repo: r.dir, Title: "review gh#131 part 3"},
			Item{ID: "mg-aaf6", Status: "available", Repo: r.dir, Title: "build gh#131 part 3"},
		),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if row, ok := rowFor(rep, "mg-1c60"); ok {
		t.Errorf("the REVIEWER's pointer branch was reported as %q; its remedy (%s) submits the "+
			"builder's work a second time under the reviewer's authorship\n%s",
			row.Kind, row.Remedy(), Render(rep, true))
	}
	if len(rep.Excluded) != 1 || !strings.Contains(rep.Excluded[0].Reason, "polecat-paaf6") {
		t.Fatalf("Excluded = %+v, want one entry naming polecat-paaf6 as the owner", rep.Excluded)
	}

	// And the half that must not move: the BUILDER's branch is genuinely
	// stranded, and a reviewer having checked it out is not a reason to go quiet.
	row, ok := rowFor(rep, "mg-aaf6")
	if !ok {
		t.Fatalf("the BUILDER's stranded branch vanished because a reviewer pointed at it — "+
			"that is the mg-9a19 case, which is why this sweep exists\n%s", Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Errorf("builder row Kind = %q, want %q", row.Kind, KindStranded)
	}
}

// TestLocalOnlyStrandedRowPrescribesAPushFirst is the sweep's half of mg-bfe0.
//
// Render() has always labelled the branch line "LOCAL ONLY — not on origin, and
// git-gc reaps the worktree", but the `-> remedy` line underneath it was the
// bare `pogo refinery submit`, which the refinery REFUSES for a branch that is
// not on origin (mg-586d). A prose warning two lines above a runnable command
// loses to the command, so the push belongs IN the command.
func TestLocalOnlyStrandedRowPrescribesAPushFirst(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p0fc6", "main")
	r.commit("predictions.md", "predictions: three of the six scoping checks will fail (mg-0fc6)")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-0fc6", Status: "available", Repo: r.dir, Title: "scope compression2"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-0fc6")
	if !ok {
		t.Fatalf("no row for mg-0fc6; its work exists only on a local head, which is the URGENT "+
			"case and not the lesser one.\n%s", Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Fatalf("Kind = %q, want %q", row.Kind, KindStranded)
	}
	if row.Pushed {
		t.Fatalf("Pushed = true for a branch that was never pushed")
	}
	remedy := row.Remedy()
	if !strings.Contains(remedy, "push origin polecat-p0fc6 && pogo refinery submit") {
		t.Errorf("Remedy() = %q — submit alone is refused for a branch that is not on origin", remedy)
	}
	// The rendered report must carry it too; the remedy is what gets pasted.
	if !strings.Contains(Render(rep, true), "push origin polecat-p0fc6 &&") {
		t.Errorf("the rendered report does not carry the push:\n%s", Render(rep, true))
	}
}

// TestPushedStrandedRowStillPrescribesABareSubmit is the negative control: the
// common case must not have grown a push it does not need.
func TestPushedStrandedRowStillPrescribesABareSubmit(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-9a19", Status: "available", Repo: r.dir, Title: "drift battery"}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-9a19")
	if !ok {
		t.Fatalf("no row for mg-9a19\n%s", Render(rep, true))
	}
	if strings.Contains(row.Remedy(), "push origin") {
		t.Errorf("Remedy() = %q tells a reader to push a branch already on origin", row.Remedy())
	}
	if !strings.Contains(row.Remedy(), "pogo refinery submit polecat-q9a19") {
		t.Errorf("Remedy() = %q lost the submit command", row.Remedy())
	}
}
