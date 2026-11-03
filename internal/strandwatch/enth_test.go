package strandwatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs slice 6b (mg-28cbc): check-stranded lists enth work by its
// Pogo-Item trailer. The unit cases feed the sweep a synthetic listing; the
// enthd case at the bottom reads a real one.

const enthRepoPath = "/repos/enthproj"

func change(id, base, item, worker string) vcs.Change {
	c := vcs.Change{ID: id, Base: base, Created: time.Unix(1700000000, 0).UTC()}
	if item != "" {
		c.Trailers = append(c.Trailers, vcs.Trailer{Key: vcs.TrailerItem, Value: item})
	}
	if worker != "" {
		c.Trailers = append(c.Trailers, vcs.Trailer{Key: vcs.TrailerWorker, Value: worker})
	}
	return c
}

func enthOpts(items []Item, live map[string]bool, list func() ([]vcs.Change, error), closed map[string]Item) Options {
	return Options{
		Items:      func() ([]Item, error) { return items, nil },
		LiveAgents: func() (map[string]bool, error) { return live, nil },
		Enth: func() (map[string]EnthSource, error) {
			return map[string]EnthSource{enthRepoPath: {
				Remote: "http://enthd.test", Repo: "alice/proj", Trunk: vcs.DefaultEnthTrunk, Unlanded: list,
			}}, nil
		},
		ItemByID: func(id string) (Item, error) {
			if it, ok := closed[id]; ok {
				return it, nil
			}
			return Item{}, errors.New("mg-x: no such work item")
		},
	}
}

func enthRowFor(rep Report, id string) *Row {
	for i := range rep.Rows {
		if rep.Rows[i].Item.ID == id {
			return &rep.Rows[i]
		}
	}
	return nil
}

func TestEnthUnlandedWorkOfDoneAndAvailableItemsIsStranded(t *testing.T) {
	items := []Item{{ID: "mg-a1", Status: "available", Repo: enthRepoPath, Title: "avail"}}
	closed := map[string]Item{"mg-d1": {ID: "mg-d1", Status: "done", Repo: enthRepoPath, Title: "done one"}}
	list := func() ([]vcs.Change, error) {
		return []vcs.Change{
			change("aaaa1111", "root", "mg-a1", "pa1"),
			change("dddd1111", "root", "mg-d1", "pd1"),
			change("dddd2222", "dddd1111", "mg-d1", "pd1"),
			change("eeee0000", "root", "", ""), // no trailer
		}, nil
	}
	rep, err := Scan(enthOpts(items, map[string]bool{}, list, closed))
	if err != nil {
		t.Fatal(err)
	}
	a, d := enthRowFor(rep, "mg-a1"), enthRowFor(rep, "mg-d1")
	if a == nil || a.Kind != KindStranded || a.Enth == nil {
		t.Fatalf("available item's unlanded work: row %+v, want stranded with enth work", a)
	}
	if d == nil || d.Kind != KindStranded || len(d.Enth.Changes) != 2 || d.Enth.Stacks != 1 {
		t.Fatalf("done item's unlanded work: row %+v, want stranded, 2 changes, 1 stack", d)
	}
	if !rep.Actionable() {
		t.Error("report with stranded enth work is not actionable")
	}
	if rep.EnthClosedItems != 1 {
		t.Errorf("EnthClosedItems = %d, want 1", rep.EnthClosedItems)
	}
	if len(rep.Repos) != 1 || rep.Repos[0].VCS != "enth" || rep.Repos[0].EnthUnlanded != 4 || rep.Repos[0].EnthUntrailed != 1 {
		t.Errorf("coverage = %+v, want one enth repo with 4 unlanded, 1 untrailed", rep.Repos)
	}
	// No merge queue on enth (mg-5b904): the remedy lands the stack by hand
	// under a request id of its own and records it with pogo land.
	if got := a.Remedy(); !strings.Contains(got, " land "+strings.Join(a.Enth.Changes, " ")+" --request-id mg-a1/recover-1 > /tmp/mg-a1-land.out && pogo land --item=mg-a1 --repo="+enthRepoPath+" --landed-file=/tmp/mg-a1-land.out") ||
		!strings.Contains(got, "do NOT dispatch at mg-a1") || strings.Contains(got, "refinery submit") {
		t.Errorf("available remedy: %s", got)
	}
	if got := d.Remedy(); !strings.Contains(got, "pogo land --item=mg-d1") || !strings.Contains(got, "DONE and this work NEVER LANDED") {
		t.Errorf("done remedy = %s", got)
	}
	out := Render(rep, true)
	for _, s := range []string{"stranded", "mg-d1", "dddd2222", "The item is DONE", "enth: 1 item(s), 4 unlanded Change(s)"} {
		if !strings.Contains(out, s) {
			t.Errorf("render lacks %q:\n%s", s, out)
		}
	}
}

func TestEnthLandedWorkIsNotReported(t *testing.T) {
	// A landed Change is absent from the unlanded listing: nothing to report,
	// and the repo still counts as covered.
	items := []Item{{ID: "mg-a1", Status: "available", Repo: enthRepoPath}}
	rep, err := Scan(enthOpts(items, map[string]bool{}, func() ([]vcs.Change, error) { return nil, nil }, nil))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Actionable() || rep.Blind() != "" {
		t.Fatalf("rows=%+v blind=%q, want clean", rep.Rows, rep.Blind())
	}
	if rep.ItemsChecked != 1 {
		t.Errorf("ItemsChecked = %d, want 1", rep.ItemsChecked)
	}
}

func TestEnthExclusionsAndEdgeRows(t *testing.T) {
	items := []Item{
		{ID: "mg-l1", Status: "claimed", Repo: enthRepoPath},   // live worker
		{ID: "mg-q1", Status: "claimed", Repo: enthRepoPath},   // queued, claimed
		{ID: "mg-q2", Status: "available", Repo: enthRepoPath}, // queued, available
		{ID: "mg-s1", Status: "available", Repo: enthRepoPath}, // two stacks
	}
	closed := map[string]Item{"mg-r1": {ID: "mg-r1", Status: "archived"}}
	list := func() ([]vcs.Change, error) {
		return []vcs.Change{
			change("11110000", "root", "mg-l1", "pl1"),
			change("22220000", "root", "mg-q1", "pq1"),
			change("33330000", "root", "mg-q2", "pq2"),
			change("44440000", "root", "mg-s1", "ps1"),
			change("55550000", "root", "mg-s1", "ps1b"),
			change("66660000", "root", "mg-r1", "pr1"),
			change("77770000", "root", "mg-gone", "pg"),
		}, nil
	}
	opts := enthOpts(items, map[string]bool{"pl1": true}, list, closed)
	opts.QueuedBranches = func() (map[string]QueuedRequest, error) {
		return map[string]QueuedRequest{
			ItemQueueKey(enthRepoPath, "mg-q1"): {MR: "mr-q1", Status: "pending"},
			ItemQueueKey(enthRepoPath, "mg-q2"): {MR: "mr-q2", Status: "pending"},
		}, nil
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	excluded := map[string]string{}
	for _, e := range rep.Excluded {
		excluded[e.ItemID] = e.Reason
	}
	for _, id := range []string{"mg-l1", "mg-q1", "mg-r1"} {
		if excluded[id] == "" {
			t.Errorf("%s not excluded (excluded=%v)", id, excluded)
		}
		if enthRowFor(rep, id) != nil {
			t.Errorf("%s has a row", id)
		}
	}
	if r := enthRowFor(rep, "mg-q2"); r == nil || r.Kind != KindInFlight || !strings.Contains(r.Remedy(), "pogo refinery show mr-q2") {
		t.Errorf("queued available item: %+v", r)
	}
	if r := enthRowFor(rep, "mg-s1"); r == nil || r.Kind != KindStranded || r.Enth.Stacks != 2 ||
		strings.Contains(r.Remedy(), "pogo land") || !strings.Contains(r.Remedy(), "2 STACKS") {
		t.Errorf("ambiguous item: %+v remedy=%q", r, r.Remedy())
	}
	if r := enthRowFor(rep, "mg-gone"); r == nil || r.Kind != KindUnjudged || !strings.Contains(r.Error, "no such work item") {
		t.Errorf("unreadable item: %+v", r)
	}
}

func TestEnthListingFailureIsARowPerItem(t *testing.T) {
	items := []Item{{ID: "mg-a1", Status: "available", Repo: enthRepoPath}}
	rep, err := Scan(enthOpts(items, map[string]bool{}, func() ([]vcs.Change, error) {
		return nil, errors.New("connection refused")
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	r := enthRowFor(rep, "mg-a1")
	if r == nil || r.Kind != KindRepoUnreadable || !strings.HasPrefix(r.Remedy(), "enth --remote http://enthd.test --repo alice/proj changes --unlanded") {
		t.Fatalf("row = %+v", r)
	}
	if rep.ItemsChecked != 0 {
		t.Errorf("ItemsChecked = %d, want 0", rep.ItemsChecked)
	}
}

func TestEnthNotConsultedIsStatedInTheFrame(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      func() ([]Item, error) { return []Item{{ID: "mg-a1", Status: "available", Repo: r.dir}}, nil },
		LiveAgents: func() (map[string]bool, error) { return map[string]bool{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rep.Frame, "\n"), "Enth-backed repositories were NOT asked for") {
		t.Errorf("frame does not state enth was not consulted:\n%s", strings.Join(rep.Frame, "\n"))
	}
}

// TestEnthGitControl: consulting enth with no enth repositories leaves a git
// sweep's report exactly as it was without the enth source, frame included.
func TestEnthGitControl(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q9a19", "main")
	r.commit("work.txt", "feat: work (mg-9a19)")
	r.push("polecat-q9a19")
	r.checkout("main")
	base := Options{
		Target:     "main",
		Items:      func() ([]Item, error) { return []Item{{ID: "mg-9a19", Status: "available", Repo: r.dir}}, nil },
		LiveAgents: func() (map[string]bool, error) { return map[string]bool{}, nil },
		Enth:       func() (map[string]EnthSource, error) { return map[string]EnthSource{}, nil },
	}
	with, err := Scan(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Enth = nil
	without, err := Scan(base)
	if err != nil {
		t.Fatal(err)
	}
	if with.Count(KindStranded) != 1 {
		t.Fatalf("git control found %d stranded rows, want 1", with.Count(KindStranded))
	}
	trim := func(frame []string) []string {
		var out []string
		for _, l := range frame {
			if !strings.HasPrefix(l, "Enth-backed repositories were NOT asked for") {
				out = append(out, l)
			}
		}
		return out
	}
	if a, b := strings.Join(with.Frame, "\n"), strings.Join(trim(without.Frame), "\n"); a != b {
		t.Errorf("frame differs with an empty enth source:\n%s\n---\n%s", a, b)
	}
	if a, b := fmt.Sprintf("%+v", with.Rows), fmt.Sprintf("%+v", without.Rows); a != b {
		t.Errorf("rows differ with an empty enth source:\n%s\n---\n%s", a, b)
	}
	if wr := Render(with, true); strings.Contains(wr, "enth:") || strings.Contains(wr, "Enth-backed") || strings.Contains(wr, "Pogo-Item") {
		t.Errorf("git-only render mentions enth:\n%s", wr)
	}
}

// TestEnthStrandedThroughEnthd reads a real enthd: an unlanded trailered
// Change whose item is done, and one whose item is available, are reported
// stranded; once landed, neither is.
func TestEnthStrandedThroughEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	cd, _ := s.Work(t, "mg-d1", "pd1", "feat: done item's work", map[string]string{"d.txt": "d\n"})
	ca, _ := s.Work(t, "mg-a1", "pa1", "feat: available item's work", map[string]string{"a2.txt": "a\n"})

	b := enthvcs.Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	repoPath := filepath.Join(t.TempDir(), "proj")
	opts := Options{
		Items: func() ([]Item, error) {
			return []Item{{ID: "mg-a1", Status: "available", Repo: repoPath}}, nil
		},
		LiveAgents: func() (map[string]bool, error) { return map[string]bool{}, nil },
		Enth: func() (map[string]EnthSource, error) {
			return map[string]EnthSource{repoPath: {Remote: s.URL, Repo: s.Repo, Trunk: vcs.DefaultEnthTrunk,
				Unlanded: func() ([]vcs.Change, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					return b.ListChanges(ctx, "", "", true)
				}}}, nil
		},
		ItemByID: func(id string) (Item, error) {
			if id == "mg-d1" {
				return Item{ID: id, Status: "done", Repo: repoPath}, nil
			}
			return Item{}, errors.New("no such item")
		},
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	for id, c := range map[string]string{"mg-d1": cd, "mg-a1": ca} {
		r := enthRowFor(rep, id)
		if r == nil || r.Kind != KindStranded || r.Enth == nil || len(r.Enth.Changes) != 1 || r.Enth.Changes[0] != c {
			t.Fatalf("%s: row %+v, want STRANDED carrying %s\n%s", id, r, c, Render(rep, true))
		}
	}

	s.Land(t, cd, ca)
	rep, err = Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Actionable() {
		t.Fatalf("after landing, rows = %+v\n%s", rep.Rows, Render(rep, true))
	}
}
