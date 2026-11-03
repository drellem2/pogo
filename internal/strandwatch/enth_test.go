package strandwatch

import (
	"context"
	"encoding/json"
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
				Remote: "http://enthd.test", Repo: "alice/proj", Trunk: vcs.DefaultEnthTrunk, Changes: list,
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
	if r == nil || r.Kind != KindRepoUnreadable || !strings.HasPrefix(r.Remedy(), "enth --remote http://enthd.test --repo alice/proj changes") {
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
// stranded; once landed, the done item is clean and the available one is
// landed_not_closed (mg-ca1ab).
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
			return map[string]EnthSource{repoPath: enthdSource(s, b)}, nil
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
	if r := enthRowFor(rep, "mg-d1"); r != nil {
		t.Errorf("done item whose work landed has a row: %+v", r)
	}
	r := enthRowFor(rep, "mg-a1")
	if r == nil || r.Kind != KindLandedNotClosed || len(r.Enth.Landed) != 1 || r.Enth.Landed[0] != ca {
		t.Fatalf("after landing, mg-a1 row = %+v, want landed_not_closed\n%s", r, Render(rep, true))
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("after landing, rows = %+v, want only mg-a1's landed_not_closed", rep.Rows)
	}
}

// enthdSource is the CLI's EnthSource for a sandboxed enthd: the WHOLE
// listing, landed Changes included.
func enthdSource(s *enthdtest.Server, b enthvcs.Backend) EnthSource {
	return EnthSource{Remote: s.URL, Repo: s.Repo, Trunk: vcs.DefaultEnthTrunk,
		Changes: func() ([]vcs.Change, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return b.ListChanges(ctx, "", "", false)
		}}
}

// TestEnthRowTypesThroughEnthd is mg-ca1ab's acceptance: one case per enth
// row type, each read from a real (sandboxed) enthd, with the negative
// controls beside them. The git control is TestEnthGitControl, unchanged.
func TestEnthRowTypesThroughEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	// stranded, open: unlanded work, no worker running.
	cOpen, _ := s.Work(t, "mg-so", "pso", "feat: open work", map[string]string{"so.txt": "so\n"})
	// stranded, done: unlanded work, nothing of the item landed.
	cDone, _ := s.Work(t, "mg-sd", "psd", "feat: lost work", map[string]string{"sd.txt": "sd\n"})
	// stranded, done, beside LANDED work of the same item: the abandoned-by-a-
	// respawn shape whose frequency this slice measures.
	cAbandoned, _ := s.Work(t, "mg-ab", "pab1", "feat: first worker", map[string]string{"ab1.txt": "1\n"})
	cAdopted, _ := s.Work(t, "mg-ab", "pab2", "feat: respawned worker", map[string]string{"ab2.txt": "2\n"})
	// landed_not_closed: an open item every Change of which landed.
	cLNC, _ := s.Work(t, "mg-ln", "pln", "feat: landed, never closed", map[string]string{"ln.txt": "ln\n"})
	// control: the same, but its worker is still running (mid-close).
	cLive, _ := s.Work(t, "mg-lv", "plv", "feat: landing right now", map[string]string{"lv.txt": "lv\n"})
	// control: open, one Change landed and one not — stranded, never landed_not_closed.
	cMixL, _ := s.Work(t, "mg-mx", "pmx", "feat: part one", map[string]string{"mx1.txt": "1\n"})
	cMixU, _ := s.Work(t, "mg-mx", "pmx", "feat: part two", map[string]string{"mx2.txt": "2\n"})
	// control: a done item every Change of which landed is the normal case.
	cDoneLanded, _ := s.Work(t, "mg-dl", "pdl", "feat: normal close", map[string]string{"dl.txt": "dl\n"})
	// unjudged: a trailer naming an item nobody can read.
	cGone, _ := s.Work(t, "mg-gone", "pg", "feat: orphan trailer", map[string]string{"g.txt": "g\n"})
	s.Land(t, cAdopted, cLNC, cLive, cMixL, cDoneLanded)

	b := enthvcs.Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	repoPath := filepath.Join(t.TempDir(), "proj")
	open := []Item{
		{ID: "mg-so", Status: "available", Repo: repoPath},
		{ID: "mg-ln", Status: "available", Repo: repoPath},
		{ID: "mg-lv", Status: "claimed", Repo: repoPath},
		{ID: "mg-mx", Status: "available", Repo: repoPath},
	}
	closed := map[string]Item{
		"mg-sd": {ID: "mg-sd", Status: "done", Repo: repoPath},
		"mg-ab": {ID: "mg-ab", Status: "done", Repo: repoPath},
		"mg-dl": {ID: "mg-dl", Status: "done", Repo: repoPath},
	}
	opts := Options{
		Items:      func() ([]Item, error) { return open, nil },
		LiveAgents: func() (map[string]bool, error) { return map[string]bool{"plv": true}, nil },
		Enth: func() (map[string]EnthSource, error) {
			return map[string]EnthSource{repoPath: enthdSource(s, b)}, nil
		},
		ItemByID: func(id string) (Item, error) {
			if it, ok := closed[id]; ok {
				return it, nil
			}
			return Item{}, errors.New("mg show " + id + ": no such work item")
		},
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	out := Render(rep, true)

	for _, tc := range []struct {
		item    string
		kind    Kind
		changes []string
		landed  []string
	}{
		{"mg-so", KindStranded, []string{cOpen}, nil},
		{"mg-sd", KindStranded, []string{cDone}, nil},
		{"mg-ab", KindStranded, []string{cAbandoned}, []string{cAdopted}},
		{"mg-ln", KindLandedNotClosed, nil, []string{cLNC}},
		{"mg-mx", KindStranded, []string{cMixU}, []string{cMixL}},
		{"mg-gone", KindUnjudged, []string{cGone}, nil},
	} {
		r := enthRowFor(rep, tc.item)
		if r == nil || r.Kind != tc.kind || r.Enth == nil {
			t.Errorf("%s: row %+v, want %s\n%s", tc.item, r, tc.kind, out)
			continue
		}
		if fmt.Sprint(r.Enth.Changes) != fmt.Sprint(tc.changes) || fmt.Sprint(r.Enth.Landed) != fmt.Sprint(tc.landed) {
			t.Errorf("%s: changes=%v landed=%v, want %v / %v", tc.item, r.Enth.Changes, r.Enth.Landed, tc.changes, tc.landed)
		}
	}
	// The controls produce no row.
	for _, id := range []string{"mg-lv", "mg-dl"} {
		if r := enthRowFor(rep, id); r != nil {
			t.Errorf("control %s has a row: %+v", id, r)
		}
	}
	excludedLive := false
	for _, e := range rep.Excluded {
		excludedLive = excludedLive || e.ItemID == "mg-lv"
	}
	if !excludedLive {
		t.Errorf("mg-lv (landed, worker running) was not excluded: %+v", rep.Excluded)
	}

	ln := enthRowFor(rep, "mg-ln")
	if got := ln.Remedy(); !strings.HasPrefix(got, "mg done mg-ln ") || !strings.Contains(got, cLNC) ||
		strings.Contains(got, "enth --remote") || strings.Contains(got, "pogo land") {
		t.Errorf("landed_not_closed remedy = %s", got)
	}
	var result struct {
		VCS    string   `json:"vcs"`
		Landed []string `json:"landed"`
	}
	raw := ln.Remedy()
	raw = raw[strings.Index(raw, "'")+1 : strings.LastIndex(raw, "'")]
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result.VCS != "enth" || fmt.Sprint(result.Landed) != fmt.Sprint([]string{cLNC}) {
		t.Errorf("landed_not_closed --result is not the JSON it claims (%v): %s", err, raw)
	}

	// THE MEASUREMENT (slice 6b's noise risk): two done items hold unlanded
	// work, and exactly one of them holds landed work beside it.
	if rep.EnthClosedItems != 2 || rep.EnthDoneLandedElsewhere != 1 {
		t.Errorf("EnthClosedItems=%d EnthDoneLandedElsewhere=%d, want 2 and 1", rep.EnthClosedItems, rep.EnthDoneLandedElsewhere)
	}
	// The coverage count is EXACT, but what it counts includes one Change the
	// test did not make: the fixture's hand-stored root (enthdtest.Start), which
	// an enth before b89e32f (mg-c964d, "a land records a change entry for a
	// member stored without one") never listed and an enth from it on lists as
	// landed. The host's enth is whatever was last built onto PATH, so a
	// hardcoded total couples this test to that build: it went red at a 16:07Z
	// rebuild with no pogo change (mg-f99ff). So the expectation is the 5
	// Changes landed above, plus the root only when the same listing actually
	// names it — and then it must be landed and untrailed, or it would have
	// produced a row the table above does not expect.
	wantLanded := 5
	listed, err := enthdSource(s, b).Changes()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range listed {
		if c.ID != s.RootID {
			continue
		}
		if c.Landed == "" || len(c.Trailers) != 0 {
			t.Errorf("fixture root %s is listed but not as a landed, untrailed Change: %+v", s.RootID, c)
		}
		wantLanded++
	}
	if len(rep.Repos) != 1 || rep.Repos[0].EnthUnlanded != 5 || rep.Repos[0].EnthLanded != wantLanded {
		t.Errorf("coverage = %+v, want 5 unlanded and %d landed (5 made by this test, plus the fixture root if listed)", rep.Repos, wantLanded)
	}
	for _, want := range []string{
		"landed_not_closed",
		"HAVE LANDED:",
		"The work is ON the trunk and the item is still asking for it.",
		"1 OTHER Change(s) of this item DID land (" + cAdopted + ")",
		"(1 of them beside LANDED work of the same item",
		fmt.Sprintf("%d landed,", wantLanded),
		"1 landed-but-not-closed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}

// TestEnthRepoUnreadableThroughEnthd: the repo_unreadable row, from a listing
// that genuinely failed — a real enth binary against an enthd that is not
// listening — beside the positive control of the same binary on a live one.
func TestEnthRepoUnreadableThroughEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	s.Work(t, "mg-a1", "pa1", "feat: work", map[string]string{"a.txt": "a\n"})
	repoPath := filepath.Join(t.TempDir(), "proj")
	for _, tc := range []struct {
		name   string
		remote string
		want   Kind
	}{
		{"enthd down", "http://127.0.0.1:1", KindRepoUnreadable},
		{"enthd up (control)", s.URL, KindStranded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			er := s.EnthRepo()
			er.Remote = tc.remote
			b := enthvcs.Backend{Repo: er, Bin: s.Enth()}
			src := enthdSource(s, b)
			src.Remote = tc.remote
			rep, err := Scan(Options{
				Items:      func() ([]Item, error) { return []Item{{ID: "mg-a1", Status: "available", Repo: repoPath}}, nil },
				LiveAgents: func() (map[string]bool, error) { return map[string]bool{}, nil },
				Enth:       func() (map[string]EnthSource, error) { return map[string]EnthSource{repoPath: src}, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			r := enthRowFor(rep, "mg-a1")
			if r == nil || r.Kind != tc.want {
				t.Fatalf("row = %+v, want %s\n%s", r, tc.want, Render(rep, true))
			}
			if tc.want == KindRepoUnreadable && !strings.HasPrefix(r.Remedy(), "enth --remote "+tc.remote+" --repo "+s.Repo+" changes") {
				t.Errorf("remedy = %s", r.Remedy())
			}
		})
	}
}
