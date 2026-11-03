package enthvcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// The listing → stack cases of enth-vcs slice 6 (mg-30da0, §16 row 6): one,
// none, ambiguous, trailer-less, against a sandboxed enthd running real enth.

// workerChange edits file in dir and makes a Change of it, with Pogo-Item /
// Pogo-Worker trailers when item / worker are set, as the polecat template
// writes it. It returns the new Change id (the checkout's new base).
func (e *enthd) workerChange(t *testing.T, dir, file, text, msg, item, worker string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"change", "-m", msg}
	if item != "" {
		args = append(args, "--trailer", vcs.TrailerItem+"="+item)
	}
	if worker != "" {
		args = append(args, "--trailer", vcs.TrailerWorker+"="+worker)
	}
	e.cmd(t, "enth", append(args, dir)...)
	st, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Base
}

func (e *enthd) checkout(t *testing.T, b Backend, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if _, err := b.Create(context.Background(), vcs.WorkspaceSpec{Dir: dir}); err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return dir
}

func TestFindStackOneNoneAmbiguous(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	ctx := context.Background()

	// NONE: an item nobody worked on is ErrNoWork, not an empty success.
	if _, err := b.FindStack(ctx, "mg-1", ""); !errors.Is(err, vcs.ErrNoWork) {
		t.Fatalf("FindStack with no Changes = %v, want ErrNoWork", err)
	}

	// ONE: p1 makes a stack of two for mg-1.
	d1 := e.checkout(t, b, "p1")
	c1 := e.workerChange(t, d1, "a.txt", "a1\nP1\n", "first (mg-1)", "mg-1", "p1")
	c2 := e.workerChange(t, d1, "n.txt", "n\n", "second (mg-1)", "mg-1", "p1")
	// Another item's work is not listed.
	other := e.checkout(t, b, "other")
	e.workerChange(t, other, "o.txt", "o\n", "other (mg-2)", "mg-2", "q1")

	s, err := b.FindStack(ctx, "mg-1", "")
	if err != nil {
		t.Fatalf("FindStack one: %v", err)
	}
	if ids(s.Changes) != c1+">"+c2 || s.Ref != c2 || s.Worker != "p1" || s.Item != "mg-1" {
		t.Fatalf("FindStack = %+v, want the stack %s>%s by p1", s, c1, c2)
	}
	if s.Changes[0].Base != e.rootID || s.Changes[0].AuthorEmail != "worker@example.com" || s.Changes[0].Created.IsZero() {
		t.Errorf("listing fields = %+v", s.Changes[0])
	}

	// AMBIGUOUS: a respawn that did not adopt, p2, also produced work for mg-1.
	d2 := e.checkout(t, b, "p2")
	x := e.workerChange(t, d2, "src/b.txt", "b1\nP2\n", "redo (mg-1)", "mg-1", "p2")
	_, err = b.FindStack(ctx, "mg-1", "")
	var amb *vcs.AmbiguousWorkError
	if !errors.As(err, &amb) || len(amb.Stacks) != 2 {
		t.Fatalf("FindStack with two workers = %v, want an AmbiguousWorkError of 2 stacks", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "worker p1, 2 Change(s), top "+c2) || !strings.Contains(msg, "worker p2, 1 Change(s), top "+x) {
		t.Errorf("the refusal must name each stack's worker:\n%s", msg)
	}
	// Filtering by worker too is the normal case, and resolves it.
	p2, err := b.FindStack(ctx, "mg-1", "p2")
	if err != nil || p2.Ref != x {
		t.Fatalf("FindStack(mg-1, p2) = %+v, %v; want top %s", p2, err, x)
	}

	// STATE via land records: p1's stack lands; it is landed, p2's is not.
	st, err := b.State(ctx, vcs.Repo{}, p2, "")
	if err != nil || st.Landed || len(st.Ahead) != 1 || st.Ahead[0].Rev != x || st.Ahead[0].Subject != "redo (mg-1)" {
		t.Fatalf("State before land = %+v, %v", st, err)
	}
	e.cmd(t, "enth", "--remote", e.url, "--repo", e.repo, "land", c1, c2)
	if left, err := b.FindStack(ctx, "mg-1", ""); err != nil || left.Ref != x {
		t.Fatalf("after p1's land, the unlanded listing is %+v, %v; want only p2's %s", left, err, x)
	}
	landed := vcs.Submission{Item: "mg-1", Revs: []string{c1, c2}}
	st, err = b.State(ctx, vcs.Repo{}, landed, "")
	if err != nil || !st.Landed || st.Equivalent != 2 || len(st.Ahead) != 0 {
		t.Errorf("State of a landed stack = %+v, %v; want landed, 2 equivalent", st, err)
	}
	// A Change the listing does not know is an error, never "landed" (E3).
	if _, err := b.State(ctx, vcs.Repo{}, vcs.Submission{Item: "mg-1", Revs: []string{strings.Repeat("0", 64)}}, ""); err == nil {
		t.Error("State of an unknown Change answered; want an error")
	}
	// Once every stack lands, the item has no work: NONE again.
	e.cmd(t, "enth", "--remote", e.url, "--repo", e.repo, "land", x)
	if _, err := b.FindStack(ctx, "mg-1", ""); !errors.Is(err, vcs.ErrNoWork) {
		t.Errorf("FindStack after every land = %v, want ErrNoWork", err)
	}
}

func TestUntrailedBase(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	ctx := context.Background()
	dir := e.checkout(t, b, "forgot")

	// A fresh checkout's base is the trunk tip: not a Change from enth change.
	wb, base, err := WorkspaceBase(dir, b.Bin)
	if err != nil || base != e.rootID || wb.Repo.Remote != e.url || wb.Repo.Repo != e.repo {
		t.Fatalf("WorkspaceBase = %+v %s %v", wb, base, err)
	}
	if err := wb.UntrailedBase(ctx, dir, base); err != nil {
		t.Errorf("fresh checkout: UntrailedBase = %v, want nil", err)
	}

	// A trailered Change is found by the listing, and is not the signal.
	c1 := e.workerChange(t, dir, "a.txt", "a1\nW\n", "with trailers (mg-7)", "mg-7", "w")
	if err := wb.UntrailedBase(ctx, dir, c1); err != nil {
		t.Errorf("trailered base: UntrailedBase = %v, want nil", err)
	}

	// The worker forgets the trailers on its second Change: the item listing
	// is blind to it (positive control for the guard)…
	c2 := e.workerChange(t, dir, "n.txt", "n\n", "forgot", "", "")
	s, err := b.FindStack(ctx, "mg-7", "")
	if err != nil || s.Ref != c1 {
		t.Fatalf("FindStack = %+v, %v; want only %s (the trailer-less %s is invisible to it)", s, err, c1, c2)
	}
	// …and the workspace's base names it.
	err = wb.UntrailedBase(ctx, dir, c2)
	var ue *UntrailedBaseError
	if !errors.As(err, &ue) || ue.Change.ID != c2 || !strings.Contains(err.Error(), "this Change has no Pogo-Item trailer") {
		t.Fatalf("UntrailedBase = %v, want an UntrailedBaseError for %s with the remedy", err, c2)
	}

	// Once it lands it is no longer stranded work.
	e.cmd(t, "enth", "--remote", e.url, "--repo", e.repo, "land", c1, c2)
	if err := wb.UntrailedBase(ctx, dir, c2); err != nil {
		t.Errorf("landed trailer-less base: UntrailedBase = %v, want nil", err)
	}
}

func TestMessagesDiffCat(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	ctx := context.Background()
	dir := e.checkout(t, b, "w")
	os.Remove(filepath.Join(dir, "src", "b.txt"))
	c := e.workerChange(t, dir, "a.txt", "a1\nCHANGED\n", "feat: thing (mg-9)\n\nWhy it is.", "mg-9", "w")
	s := vcs.Submission{Item: "mg-9", Revs: []string{c}}

	msgs, err := b.Messages(ctx, vcs.Repo{}, s)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Messages = %v, %v", msgs, err)
	}
	if body := msgs[0].Body; !strings.HasPrefix(body, "Why it is.") || !strings.Contains(body, "Pogo-Item: mg-9") || strings.Contains(body, "feat: thing") {
		t.Errorf("body = %q, want the message without its subject, trailers included", body)
	}
	d, err := b.Diff(ctx, vcs.Repo{}, s)
	if err != nil || strings.Join(d.Paths, ",") != "a.txt,src/b.txt" {
		t.Errorf("Diff = %v, %v; want a.txt and the deleted src/b.txt", d.Paths, err)
	}
	got, err := b.Cat(ctx, vcs.Repo{}, c, "a.txt")
	if err != nil || string(got) != "a1\nCHANGED\n" {
		t.Errorf("Cat at the Change = %q, %v", got, err)
	}
	if got, err := b.Cat(ctx, vcs.Repo{}, e.rootID, "a.txt"); err != nil || string(got) != "a1\na2\n" {
		t.Errorf("Cat at the root = %q, %v", got, err)
	}
	_, err = b.Cat(ctx, vcs.Repo{}, c, "src/b.txt")
	var re *vcs.RefusedError
	if !errors.As(err, &re) || re.Code != "not_found" {
		t.Errorf("Cat of a deleted path = %v, want refused not_found", err)
	}
	if err := b.Fetch(ctx, vcs.Repo{}); err != nil {
		t.Errorf("Fetch = %v, want nil (reads are live)", err)
	}
}

func ids(cs []vcs.Change) string {
	var s []string
	for _, c := range cs {
		s = append(s, c.ID)
	}
	return strings.Join(s, ">")
}

func TestPresubmitAgainstEnthd(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	ctx := context.Background()
	dir := e.checkout(t, b, "p1")
	refuses := func(what, want string) {
		t.Helper()
		_, err := Presubmit(ctx, dir, "mg-5", "p1", b.Bin)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: Presubmit = %v, want a refusal containing %q", what, err, want)
		}
	}

	if _, err := Presubmit(ctx, dir, "", "p1", b.Bin); err == nil || !strings.Contains(err.Error(), "pass --item") {
		t.Errorf("no --item: %v", err)
	}
	refuses("a fresh checkout (NONE)", "no unlanded Change carries Pogo-Item=mg-5 Pogo-Worker=p1")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edit\n"), 0o644)
	refuses("an uncommitted edit", "uncommitted work in this checkout")
	c1 := e.workerChange(t, dir, "a.txt", "edit\n", "work (mg-5)", "mg-5", "p1")

	// ONE stack, whose top is the base: passes.
	msg, err := Presubmit(ctx, dir, "mg-5", "p1", b.Bin)
	if err != nil || !strings.Contains(msg, "pre-submit OK: 1 Change(s) for mg-5, top "+c1) {
		t.Fatalf("Presubmit = %q, %v; want OK", msg, err)
	}

	// TRAILER-LESS: a second Change without trailers is refused with the remedy.
	e.workerChange(t, dir, "n.txt", "n\n", "forgot", "", "")
	refuses("a trailer-less base", "this Change has no Pogo-Item trailer")

	// The base is the item's work, but not the top of its stack: a fresh
	// checkout of p1's top, then another p1 Change elsewhere.
	d2 := filepath.Join(t.TempDir(), "p1b")
	if _, err := b.Create(ctx, vcs.WorkspaceSpec{Dir: d2, Base: c1}); err != nil {
		t.Fatal(err)
	}
	c3 := e.workerChange(t, d2, "src/b.txt", "x\n", "more (mg-5)", "mg-5", "p1")
	dir = filepath.Join(t.TempDir(), "p1c")
	if _, err := b.Create(ctx, vcs.WorkspaceSpec{Dir: dir, Base: c1}); err != nil {
		t.Fatal(err)
	}
	refuses("a base under the top", "is not the top of mg-5's stack ("+c3+")")

	// AMBIGUOUS: without a worker filter, p2's stack makes two.
	d3 := e.checkout(t, b, "p2")
	e.workerChange(t, d3, "z.txt", "z\n", "p2 (mg-5)", "mg-5", "p2")
	if _, err := Presubmit(ctx, d2, "mg-5", "", b.Bin); err == nil || !strings.Contains(err.Error(), "stacks, not one") {
		t.Errorf("two workers, no filter: Presubmit = %v, want the ambiguity refusal", err)
	}
	if msg, err := Presubmit(ctx, d2, "mg-5", "p1", b.Bin); err != nil {
		t.Errorf("filtered by worker, p1's own checkout at its top: %v (%s)", err, msg)
	}
}
