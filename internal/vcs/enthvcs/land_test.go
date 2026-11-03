package enthvcs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs §6 as decided (mg-5b904): the worker lands its own work.

func TestParseLanded(t *testing.T) {
	landed := `{"type":"landed","ref":"refs/heads/main","entry":7,"old":"T","new":"N","user":"u","changes":[{"submitted":"C","landed":"N"}]}` + "\n"
	rec, err := ParseLanded([]byte(landed))
	if err != nil || rec.Old != "T" || rec.New != "N" || rec.Entry != 7 || rec.Ref != "refs/heads/main" || len(rec.Changes) != 1 || rec.Changes[0].Landed != "N" {
		t.Fatalf("landed = %+v %v", rec, err)
	}
	created := `{"type":"landed","ref":"refs/heads/x","entry":3,"old":null,"new":"N","changes":[{"submitted":"C","landed":"N"}]}`
	if rec, err := ParseLanded([]byte(created)); err != nil || rec.Old != "" {
		t.Fatalf("a land that created the ref = %+v %v; want old \"\"", rec, err)
	}
	for name, out := range map[string]string{
		"empty":    "",
		"dry run":  `{"type":"dry_run","ref":"refs/heads/main","old":"T","new":"N","changes":[{"submitted":"C","landed":"L"}]}`,
		"conflict": `{"type":"conflict","change":"C","path":"a.txt","kind":"content"}`,
		"two":      landed + landed,
		"no new":   `{"type":"landed","ref":"refs/heads/main","entry":7,"old":"T","new":"","changes":[{"submitted":"C","landed":"N"}]}`,
		"garbage":  "landed!",
	} {
		if _, err := ParseLanded([]byte(out)); err == nil {
			t.Errorf("%s: parsed as a land", name)
		}
	}
}

// The refinery's enth land path is gone: the backend refuses every Lander
// step with the pointer to the worker's own land.
func TestLanderRefuses(t *testing.T) {
	b := Backend{Repo: vcs.EnthRepo{Repo: "o/r"}}
	if _, err := b.Prepare(context.Background(), vcs.MR{Changes: []string{"c"}}); !errors.Is(err, ErrSelfLanded) {
		t.Errorf("Prepare = %v; want ErrSelfLanded", err)
	}
	if _, err := b.Land(context.Background(), vcs.Prepared{New: "n"}); !errors.Is(err, ErrSelfLanded) {
		t.Errorf("Land = %v; want ErrSelfLanded", err)
	}
	if !strings.Contains(ErrSelfLanded.Error(), "pogo land --item") {
		t.Errorf("the refusal does not point at pogo land: %v", ErrSelfLanded)
	}
}

// A worker's own land, as the template runs it, read back by ParseLanded;
// the request id replays; and a re-made Change supersedes the one that
// conflicted, before and after it lands.
func TestSelfLandAgainstEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	b := Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	ctx := t.Context()
	land := func(id, req string) (string, error) {
		return s.Try("enth", append(s.Remote(), "land", id, "--request-id", req)...)
	}

	mine, _ := s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	other, _ := s.Work(t, "mg-2", "p2", "other", map[string]string{"z.txt": "z\n"})
	s.Land(t, other)
	t1 := s.Tip(t)
	out, err := land(mine, "mg-1/1")
	if err != nil {
		t.Fatalf("land: %v\n%s", err, out)
	}
	rec, err := ParseLanded([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	// What RecordSelfLand relies on: old is the tip it landed on, new is the
	// trunk's new tip and the top Change's landed id, and the submitted id is
	// the Change the worker made.
	if rec.Old != t1 || rec.New != s.Tip(t) || rec.Changes[0].Submitted != mine || rec.Changes[0].Landed != rec.New || rec.Ref != b.LandRef("main") {
		t.Fatalf("record = %+v; tip was %s, now %s", rec, t1, s.Tip(t))
	}
	all, err := b.ListChanges(ctx, "mg-1", "", false)
	if err != nil || len(all) != 1 || all[0].Landed != rec.New {
		t.Fatalf("listing after the land = %+v %v; want the land record's id", all, err)
	}
	// The same request id again: the first answer, nothing landed twice.
	again, err := land(mine, "mg-1/1")
	if err != nil {
		t.Fatalf("replay: %v\n%s", err, again)
	}
	if r2, err := ParseLanded([]byte(again)); err != nil || r2.Entry != rec.Entry || s.Tip(t) != rec.New {
		t.Fatalf("replay = %+v %v; want entry %d and the trunk unmoved", r2, err, rec.Entry)
	}

	// A conflict, then the re-made work superseding it.
	theirs, _ := s.Work(t, "mg-9", "p9", "theirs", map[string]string{"a.txt": "a1\nTHEIRS\n"})
	old, _ := s.Work(t, "mg-3", "p3", "mine", map[string]string{"a.txt": "a1\nMINE\n"})
	s.Land(t, theirs)
	if out, err := land(old, "mg-3/1"); err == nil || !strings.Contains(out, `"conflict"`) {
		t.Fatalf("conflicting land = %v\n%s; want a conflict", err, out)
	}
	dir := filepath.Join(t.TempDir(), "remake")
	s.Checkout(t, "main", dir)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a1\nTHEIRS\nMINE\n"), 0o644)
	remade := s.Change(t, dir, "mine, re-made\n\nPogo-Item: mg-3\nPogo-Worker: p3\nPogo-Supersedes: "+old+"\n")
	s3, err := b.FindStack(ctx, "mg-3", "p3")
	if err != nil || len(s3.Revs) != 1 || s3.Revs[0] != remade {
		t.Fatalf("FindStack with a superseded Change = %+v %v; want only the re-made one", s3.Revs, err)
	}
	if _, err := land(remade, "mg-3/2"); err != nil {
		t.Fatalf("land of the re-made Change: %v", err)
	}
	un, err := b.ListChanges(ctx, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range un {
		if c.ID == old || c.ID == remade {
			t.Errorf("unlanded listing still holds %s after its item landed its re-made work", c.ID)
		}
	}
	// The control: another item cannot retire this one's work.
	victim, _ := s.Work(t, "mg-4", "p4", "victim", map[string]string{"v.txt": "v\n"})
	thief := filepath.Join(t.TempDir(), "thief")
	s.Checkout(t, "main", thief)
	os.WriteFile(filepath.Join(thief, "w.txt"), []byte("w\n"), 0o644)
	s.Change(t, thief, "steal\n\nPogo-Item: mg-5\nPogo-Worker: p5\nPogo-Supersedes: "+victim+"\n")
	if s4, err := b.FindStack(ctx, "mg-4", ""); err != nil || len(s4.Revs) != 1 || s4.Revs[0] != victim {
		t.Fatalf("a Change of another item superseded mg-4's work: %+v %v", s4.Revs, err)
	}
}
