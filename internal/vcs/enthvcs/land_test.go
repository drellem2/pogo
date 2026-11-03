package enthvcs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// enth-vcs slice 7 (mg-1fe2b): the enth Lander.

func TestParseLand(t *testing.T) {
	dry := `{"type":"dry_run","ref":"refs/heads/main","old":"T","new":"N","user":"u","changes":[{"submitted":"C","landed":"L"}]}` + "\n"
	rec, conf, _, err := parseLand([]byte(dry), "dry_run")
	if err != nil || len(conf) != 0 || rec.Old != "T" || rec.New != "N" || rec.Ref != "refs/heads/main" || len(rec.Changes) != 1 || rec.Changes[0].Landed != "L" {
		t.Fatalf("dry run = %+v %v %v", rec, conf, err)
	}
	created := `{"type":"landed","ref":"refs/heads/x","entry":3,"old":null,"new":"N","changes":[]}`
	if rec, _, _, err := parseLand([]byte(created), "landed"); err != nil || rec.Old != "" || rec.Entry != 3 {
		t.Fatalf("a land that created the ref = %+v %v; want old \"\"", rec, err)
	}
	conflict := `{"type":"conflict","change":"C","path":"a.txt","kind":"content"}` + "\n" +
		`{"type":"conflict","change":"C","path":{"hex":"ff"},"kind":"content"}` + "\n"
	_, conf, lines, err := parseLand([]byte(conflict), "dry_run")
	if err != nil || len(conf) != 2 || conf[0].Path != "a.txt" || conf[1].Path != "\xff" || len(lines) != 2 {
		t.Fatalf("conflicts = %+v %v", conf, err)
	}
	if _, _, _, err := parseLand([]byte(""), "landed"); err == nil {
		t.Error("an empty answer parsed as a land")
	}
	if _, _, _, err := parseLand([]byte(dry), "landed"); err == nil {
		t.Error("a dry_run line was taken for a landed one")
	}
}

func TestRemoveGateDirRefusesAGitTree(t *testing.T) {
	d := t.TempDir()
	os.Mkdir(filepath.Join(d, ".git"), 0o755)
	if err := RemoveGateDir(d); err == nil {
		t.Fatal("a directory holding .git was removed")
	}
	if _, err := os.Stat(d); err != nil {
		t.Fatal("the git tree is gone")
	}
	half := filepath.Join(t.TempDir(), "gate")
	os.MkdirAll(filepath.Join(half, "src"), 0o755) // a checkout cut short: no .enth/checkout
	if err := RemoveGateDir(half); err != nil {
		t.Fatalf("a half-written gate dir: %v", err)
	}
	if err := RemoveGateDir(half); err != nil {
		t.Fatalf("an absent gate dir: %v", err)
	}
}

func TestLanderAgainstEnthd(t *testing.T) {
	s := enthdtest.Start(t)
	b := Backend{Repo: s.EnthRepo(), Bin: s.Enth()}
	ctx := t.Context()
	mine, _ := s.Work(t, "mg-1", "p1", "mine", map[string]string{"x.txt": "x\n"})
	other, _ := s.Work(t, "mg-2", "p2", "other", map[string]string{"z.txt": "z\n"})
	s.Land(t, other)
	t1 := s.Tip(t)

	// Prepare: a dry run on the moved tip (N ≠ submitted) and a checkout of
	// N, which exists only because the dry run stored it (E1).
	gate := filepath.Join(t.TempDir(), "gate")
	mr := vcs.MR{ID: "mr-1", Target: "main", Dir: gate, Changes: []string{mine}, RequestID: "mr-1/1"}
	p, err := b.Prepare(ctx, mr)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if p.Tip != t1 || p.New == "" || p.New == mine || len(p.Plan) != 1 || p.Plan[0].Submitted != mine {
		t.Fatalf("Prepared = %+v; want T=%s and a rebased N", p, t1)
	}
	st, err := ReadState(gate)
	if err != nil || st.Base != p.New {
		t.Fatalf("gate checkout base = %v %v, want N %s", st.Base, err, p.New)
	}
	for _, f := range []string{"x.txt", "z.txt", "a.txt"} {
		if _, err := os.Stat(filepath.Join(gate, f)); err != nil {
			t.Errorf("gate checkout lacks %s", f)
		}
	}

	// result_mismatch: the right tip, the wrong N. Nothing lands.
	bad := p
	bad.New = t1
	if _, err := b.Land(ctx, bad); RefusalCode(err) != "result_mismatch" {
		t.Fatalf("Land with a wrong N = %v; want result_mismatch", err)
	}
	// The land, bound to T and N.
	l, err := b.Land(ctx, p)
	if err != nil || l.Record == nil || l.Record.New != p.New || l.Record.Old != t1 || l.Record.RequestID != "mr-1/1" || l.SHA != "" {
		t.Fatalf("Land = %+v %v", l.Record, err)
	}
	if s.Tip(t) != p.New {
		t.Fatal("the trunk is not at the gated N")
	}
	// The same request again replays — even though the tip has moved to N,
	// which would otherwise be stale_tip — and lands nothing.
	again, err := b.Land(ctx, p)
	if err != nil || again.Record.Entry != l.Record.Entry || s.Tip(t) != p.New {
		t.Fatalf("replay = %+v %v; want the first answer and no new entry", again.Record, err)
	}
	// stale_tip: a fresh plan against an old tip.
	third, _ := s.Work(t, "mg-3", "p3", "third", map[string]string{"y.txt": "y\n"})
	if _, err := b.LandExpect(ctx, "main", []string{third}, t1, p.New, "mr-3/1"); RefusalCode(err) != "stale_tip" {
		t.Fatalf("land on an old tip = %v; want stale_tip", err)
	}

	// A conflict: typed, naming the path, nothing landed.
	c1, _ := s.Work(t, "mg-4", "p4", "a", map[string]string{"a.txt": "a1\nONE\n"})
	c2, _ := s.Work(t, "mg-5", "p5", "b", map[string]string{"a.txt": "a1\nTWO\n"})
	s.Land(t, c1)
	before := s.Tip(t)
	_, err = b.Prepare(ctx, vcs.MR{ID: "mr-5", Target: "main", Dir: filepath.Join(t.TempDir(), "g"), Changes: []string{c2}})
	var ce *LandConflictError
	if !errors.As(err, &ce) || len(ce.Conflicts) != 1 || ce.Conflicts[0].Path != "a.txt" || RefusalCode(err) != "conflict" {
		t.Fatalf("conflicting Prepare = %v; want a LandConflictError on a.txt", err)
	}
	if s.Tip(t) != before {
		t.Error("a conflicting dry run moved the trunk")
	}
}

func TestLandedNothing(t *testing.T) {
	refused := func(code string) error {
		return &vcs.CommandError{Err: &vcs.RefusedError{Code: code}}
	}
	for _, c := range []string{"stale_tip", "result_mismatch", "conflict", "unauthorized"} {
		if !LandedNothing(refused(c)) {
			t.Errorf("%s: want landed-nothing", c)
		}
	}
	// A dropped connection, an unknown code, a bare failure: outcome unknown.
	for _, err := range []error{refused("unreachable"), refused("something_new"), errors.New("exit status 4")} {
		if LandedNothing(err) {
			t.Errorf("%v: an unknown outcome was read as nothing landed", err)
		}
	}
}
