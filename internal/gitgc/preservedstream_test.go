package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder captures a scan's events in the order they arrive, along with how
// much of the listing had been DELIVERED at each point.
//
// The delivery count is the load-bearing part. Every assertion below is about
// ORDER — "this row reached the caller before that tree was touched" — and a
// recorder that only kept the events would let a scan that buffered everything
// and replayed it at the end pass unchanged, which is the exact defect under
// test.
type recorder struct {
	events    []PreservedScanEvent
	delivered []int // trees delivered before events[i]
	trees     int
}

func (r *recorder) fn(ev PreservedScanEvent) {
	r.delivered = append(r.delivered, r.trees)
	r.events = append(r.events, ev)
	if ev.Phase == "done" && ev.Tree != nil && ev.Disposition == "retained" {
		r.trees++
	}
}

func (r *recorder) find(t *testing.T, phase, owner string) int {
	t.Helper()
	for i, ev := range r.events {
		if ev.Phase == phase && ev.Owner == owner {
			return i
		}
	}
	t.Fatalf("no %q event for %q in %s", phase, owner, r.render())
	return -1
}

func (r *recorder) render() string {
	var b strings.Builder
	for i, ev := range r.events {
		fmt.Fprintf(&b, "\n  %d: %s %s %s (delivered=%d)", i, ev.Phase, ev.Owner, ev.Disposition, r.delivered[i])
	}
	return b.String()
}

// threeRetained builds a polecats dir holding three retained trees in one repo,
// plus one directory that is not a linked worktree at all.
func threeRetained(t *testing.T) (polecats string, repo *testRepo, owners []string) {
	t.Helper()
	polecats = sharedPolecats(t)
	repo = newTestRepo(t)
	owners = []string{"aaa1", "bbb2", "ccc3"}
	for _, o := range owners {
		repo.branch("polecat-" + o)
		wt := addWorktree(t, repo, polecats, o, "polecat-"+o)
		dirty(t, wt, "only-copy.go", "package p // exists nowhere else\n")
	}
	if err := os.MkdirAll(filepath.Join(polecats, "zzz9-orphan"), 0o755); err != nil {
		t.Fatal(err)
	}
	return polecats, repo, owners
}

func tickets(owners ...string) TicketIndex {
	idx := TicketIndex{}
	for _, o := range owners {
		idx["mg-"+o] = TicketArchived
	}
	return idx
}

// TestScanPreservedDeliversEachTreeBeforeTheNextIsTouched is the bar for
// drellem2/pogo#158.
//
// The command wrote its first byte only after the whole scan finished, so a
// scan that was merely slow was indistinguishable from a hung one and named no
// tree. Streaming is not a rendering preference here: it is the difference
// between an operator who learns about the first four of their retained trees
// and one who learns about none, and their next command after killing this one
// is `--apply --force`, which discards uncommitted work.
//
// So the assertion is not "events were emitted". It is that tree N's FULL
// RECORD had already reached the caller before tree N+1 was even entered — the
// one property a buffered scan cannot have, however many events it replays at
// the end.
func TestScanPreservedDeliversEachTreeBeforeTheNextIsTouched(t *testing.T) {
	polecats, _, owners := threeRetained(t)

	var rec recorder
	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats,
		Tickets:     tickets(owners...),
		Progress:    rec.fn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RetainedCount != 3 {
		t.Fatalf("fixture: want 3 retained, got %d", rep.RetainedCount)
	}

	// Scan order is the directory order, which sorts: aaa1, bbb2, ccc3, zzz9.
	for i := 1; i < len(owners); i++ {
		prevDone := rec.find(t, "done", owners[i-1])
		thisEnter := rec.find(t, "enter", owners[i])
		if prevDone > thisEnter {
			t.Errorf("tree %s resolved AFTER %s was entered — the scan is not streaming%s",
				owners[i-1], owners[i], rec.render())
		}
		if rec.delivered[thisEnter] < i {
			t.Errorf("only %d tree(s) had been delivered when %s was entered, want %d. "+
				"A listing that arrives all at once at the end is the defect drellem2/pogo#158 "+
				"reports: a killed scan leaves NOTHING behind.%s",
				rec.delivered[thisEnter], owners[i], i, rec.render())
		}
	}

	// The record delivered mid-scan is the WHOLE record, not a teaser for one.
	// A streamed row a reader cannot act on would move the defect rather than
	// fix it: they would still have to wait for the end to learn what is in
	// the tree.
	done := rec.events[rec.find(t, "done", "aaa1")]
	if done.Tree == nil {
		t.Fatalf("no record delivered with the first tree's done event%s", rec.render())
	}
	if done.Tree.Outcome != "preserved" || done.Tree.Untracked != 1 || len(done.Tree.Files) != 1 {
		t.Errorf("streamed record = %+v, want the same facts the final report carries "+
			"(outcome preserved, 1 untracked, the file list)", *done.Tree)
	}
	if got := findTree(t, rep.Retained, "aaa1"); got.Path != done.Tree.Path || got.Outcome != done.Tree.Outcome {
		t.Errorf("streamed record %+v disagrees with the final report's %+v — one scan must not "+
			"describe one tree two ways", *done.Tree, got)
	}
}

// TestScanPreservedNamesEveryDirectoryBeforeReadingIt keeps the trace a CENSUS
// of the directory rather than a log of its interesting half.
//
// This is what makes a stall diagnosable. The last "enter" with no matching
// "done" IS the tree that is hanging — but only if every directory produces an
// enter, including the ones that will turn out to be clean or not worktrees at
// all. A trace that skipped those would leave the reader's last line naming a
// tree the scan had already finished with, which is worse than no line: it
// points at an innocent directory.
func TestScanPreservedNamesEveryDirectoryBeforeReadingIt(t *testing.T) {
	polecats, _, owners := threeRetained(t)

	var rec recorder
	if _, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats,
		Tickets:     tickets(owners...),
		Progress:    rec.fn,
	}); err != nil {
		t.Fatal(err)
	}

	var start *PreservedScanEvent
	entered := map[string]bool{}
	resolved := map[string]string{}
	for i := range rec.events {
		ev := rec.events[i]
		switch ev.Phase {
		case "start":
			start = &rec.events[i]
		case "enter":
			entered[ev.Owner] = true
		case "done":
			if !entered[ev.Owner] {
				t.Errorf("%s resolved without ever being announced — a stall on it would name "+
					"whichever tree came before%s", ev.Owner, rec.render())
			}
			resolved[ev.Owner] = ev.Disposition
		}
	}
	if start == nil || start.Total != 4 {
		t.Fatalf("want a start event announcing 4 directories, got %v%s", start, rec.render())
	}
	if len(entered) != start.Total {
		t.Errorf("announced %d directories and entered %d — the denominator an operator waits "+
			"against must be the population actually walked%s", start.Total, len(entered), rec.render())
	}
	// The orphan dir contributes no row, and is announced anyway.
	if got := resolved["zzz9-orphan"]; got != "not-a-worktree" {
		t.Errorf("zzz9-orphan resolved %q, want not-a-worktree%s", got, rec.render())
	}
}

// TestStreamedListingSaysWhenItIsPartial is pm-pogo's acceptance condition for
// this ticket, and it is not a nicety.
//
// The reader of this output is deciding what to delete, and their alternative
// is a repo-scoped `--apply --force`. A listing that stopped early but reads as
// complete is WORSE than the hang it replaced — the hang at least announced
// itself by never returning. So a finished listing ends with a sentinel the
// header names in advance, and NOTHING else prints that sentinel.
func TestStreamedListingSaysWhenItIsPartial(t *testing.T) {
	polecats, _, owners := threeRetained(t)

	rep, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: tickets(owners...)})
	if err != nil {
		t.Fatal(err)
	}

	header := StreamedHeader(polecats, "", 4)
	if !strings.Contains(header, streamCompleteSentinel) {
		t.Errorf("the header must NAME the sentinel a complete listing ends with, or its absence "+
			"means nothing to the reader:\n%s", header)
	}
	if !strings.Contains(header, "PARTIAL") {
		t.Errorf("the header must say in as many words what a truncated listing is:\n%s", header)
	}

	// A scan killed after two trees: header plus two rows, and no tail.
	truncated := header + PreservedPreamble() +
		StreamedTree(rep.Retained[0]) + StreamedTree(rep.Retained[1])
	if strings.Count(truncated, streamCompleteSentinel) != 1 {
		t.Errorf("a truncated listing must carry the sentinel exactly once — in the header's "+
			"WARNING about it — and never as a claim of completeness:\n%s", truncated)
	}
	if strings.Contains(truncated, "3 retained") {
		t.Errorf("a truncated listing must not carry the population counts; they are the "+
			"sentence that makes a partial read look whole:\n%s", truncated)
	}

	full := truncated + StreamedTree(rep.Retained[2]) + rep.StreamedTail()
	if !strings.Contains(full, streamCompleteSentinel+" — 3 retained tree(s) printed above") {
		t.Errorf("a complete listing must claim completeness once, with the count:\n%s", full)
	}

	// The preamble reaches the reader BEFORE the rows it qualifies, not after.
	if i, j := strings.Index(full, "NOTHING BELOW IS A VERDICT"), strings.Index(full, rep.Retained[0].Path); i < 0 || i > j {
		t.Errorf("the preamble must precede the first tree (preamble at %d, first tree at %d) — "+
			"a refusal-to-judge delivered after the reader has judged is not one", i, j)
	}
}

// TestStreamedAndGroupedRenderingsDescribeOneTree pins the two renderings
// against each other.
//
// mg-e621's defect was two components observing one population and reporting it
// differently, with only one of them speaking in universals. This ticket adds a
// SECOND rendering of the same report, which is exactly the shape that produced
// that defect — so the streamed listing and Summary() are held to naming the
// same trees, the same counts and the same reclaim consequences.
func TestStreamedAndGroupedRenderingsDescribeOneTree(t *testing.T) {
	polecats, repo, owners := threeRetained(t)

	rep, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: tickets(owners...)})
	if err != nil {
		t.Fatal(err)
	}

	var streamed strings.Builder
	streamed.WriteString(StreamedHeader(polecats, "", 4))
	streamed.WriteString(PreservedPreamble())
	for _, tr := range rep.Retained {
		streamed.WriteString(StreamedTree(tr))
	}
	streamed.WriteString(rep.StreamedTail())
	stream, grouped := streamed.String(), rep.Summary()

	for _, tr := range rep.Retained {
		if !strings.Contains(stream, tr.Path) {
			t.Errorf("streamed listing omits %s, which Summary lists — a retained tree missing "+
				"from the list of retained trees is this issue's own defect one layer down", tr.Path)
		}
		if !strings.Contains(stream, "only-copy.go") {
			t.Errorf("streamed listing omits the untracked path of %s; that file is on no branch, "+
				"in no stash and on no remote", tr.Path)
		}
	}
	for _, line := range []string{
		"3 retained: 3 holding uncommitted work, 0 unreadable",
		"of those, 3 hold UNTRACKED files",
	} {
		if !strings.Contains(stream, line) || !strings.Contains(grouped, line) {
			t.Errorf("the two renderings disagree on %q (streamed=%v grouped=%v)",
				line, strings.Contains(stream, line), strings.Contains(grouped, line))
		}
	}
	// The blast radius survives the move to the tail: it is the one thing a
	// reader must not act without.
	want := fmt.Sprintf("reclaiming ANY of these is `pogo gc --repo=%s --apply --force`", repo.dir)
	if !strings.Contains(stream, want) || !strings.Contains(grouped, want) {
		t.Errorf("both renderings must state that reclaiming is repo-scoped and forced; streamed=%v grouped=%v",
			strings.Contains(stream, want), strings.Contains(grouped, want))
	}
}

// TestStreamedFilterCountIsNotClaimedBeforeItIsKnown: a --repo listing must not
// print "0 tree(s) in other repositories not shown" before it has looked.
//
// A filtered report that reads like a full one is the same failure as a
// truncated file list that does not say it truncated, and a count printed at
// the top of a streamed listing is necessarily a count of nothing. The header
// therefore names the filter WITHOUT a figure and says where the figure will
// be; the tail carries the measured one.
func TestStreamedFilterCountIsNotClaimedBeforeItIsKnown(t *testing.T) {
	polecats, repo, owners := threeRetained(t)
	other := newTestRepo(t)
	other.branch("polecat-ddd4")
	dirty(t, addWorktree(t, other, polecats, "ddd4", "polecat-ddd4"), "elsewhere.go", "package q\n")

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats, Repo: repo.dir,
		Tickets: tickets(append(owners, "ddd4")...),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OtherRepoCount != 1 {
		t.Fatalf("fixture: want 1 tree excluded by the filter, got %d", rep.OtherRepoCount)
	}

	header := StreamedHeader(polecats, repo.dir, 5)
	if strings.Contains(header, "0 tree(s) in other repositories") {
		t.Errorf("the header claims a filter count it cannot have measured yet:\n%s", header)
	}
	if !strings.Contains(header, "counted at the end") {
		t.Errorf("the header must say the filter excluded trees AND where the count is:\n%s", header)
	}
	if tail := rep.StreamedTail(); !strings.Contains(tail, "1 tree(s) in other repositories not shown") {
		t.Errorf("the tail must carry the measured count — a narrowed listing that never says how "+
			"much of the directory it is not showing reads as a full one:\n%s", tail)
	}
}

// TestStreamedTreeNamesItsRepository: a streamed row arrives under no repo
// group header, and the reclaim command is repo-scoped.
//
// Without the repository ON the row, a reader who kills a scan halfway holds an
// inventory they cannot act on — every tree lives under ~/.pogo/polecats, so
// the path does not say which repository owns it, and `pogo gc --repo=?` has no
// answer. The grouped report gets this from the header above the trees; a
// streamed one has to carry it.
func TestStreamedTreeNamesItsRepository(t *testing.T) {
	polecats, repo, owners := threeRetained(t)
	rep, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: tickets(owners...)})
	if err != nil {
		t.Fatal(err)
	}
	row := StreamedTree(rep.Retained[0])
	if !strings.Contains(row, "repository "+repo.dir) {
		t.Errorf("streamed row does not name its repository, so the reclaim command has no --repo:\n%s", row)
	}

	// And an unresolvable pointer says so rather than going quiet — the tree is
	// listed either way (a retained tree dropped from the listing is the defect
	// this whole family is about), but the row must not read as though the
	// repository were known.
	unresolved := rep.Retained[0]
	unresolved.Repo, unresolved.RepoError = "", "not a worktree pointer"
	row = StreamedTree(unresolved)
	if !strings.Contains(row, "UNRESOLVED") || !strings.Contains(row, "not a worktree pointer") {
		t.Errorf("an unresolved repository must be stated on the row, with why:\n%s", row)
	}
}
