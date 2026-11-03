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

// fakeCheckout writes a .enth/checkout in enth #9's format (version 1).
func fakeCheckout(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if len(lines) == 0 {
		lines = []string{
			`{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"https://e.example","repo":"o/r"},"paths":[]}`,
			`{"path":"a.txt","mode":"file","blob":"x","size":1,"mtime":1,"ino":1}`,
			`{"path":{"hex":"ff2e74"},"mode":"file","blob":"y","size":1,"mtime":1,"ino":2}`,
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".enth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateRelPath), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadState(t *testing.T) {
	dir := t.TempDir()
	fakeCheckout(t, dir)
	st, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Base != "b0" || st.Origin.Remote != "https://e.example" || st.Origin.Repo != "o/r" || len(st.Paths) != 0 {
		t.Errorf("header = %+v", st)
	}
	if st.Entries["a.txt"] != "file" || st.Entries["\xff.t"] != "file" || len(st.Entries) != 2 {
		t.Errorf("entries = %q (want a.txt and the hex-encoded path)", st.Entries)
	}

	store := t.TempDir()
	fakeCheckout(t, store, `{"format":"enth-checkout","version":1,"base":"b1","origin":{"store":"/s"},"paths":["src"]}`)
	if st, err := ReadState(store); err != nil || st.Origin.Store != "/s" || st.Paths[0] != "src" {
		t.Errorf("store origin = %+v, %v", st, err)
	}

	bad := t.TempDir()
	fakeCheckout(t, bad, `{"format":"enth-checkout","version":2,"base":"b","origin":null,"paths":[]}`)
	if _, err := ReadState(bad); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("version 2 = %v, want an unsupported refusal", err)
	}
	if _, err := ReadState(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing state = %v, want ErrNotExist", err)
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()
	gitRepo := filepath.Join(root, "g")
	os.MkdirAll(filepath.Join(gitRepo, ".git"), 0o755)
	// An enth checkout INSIDE a git repo: the nearest marker wins (every
	// polecat's case, ~/.pogo being a git repo).
	nested := filepath.Join(gitRepo, "polecats", "p1")
	fakeCheckout(t, nested)
	os.MkdirAll(filepath.Join(nested, "src", "deep"), 0o755)

	cases := []struct {
		dir      string
		kind     vcs.Kind
		root     string
		ambiguos bool
	}{
		{filepath.Join(nested, "src", "deep"), vcs.Enth, nested, false},
		{nested, vcs.Enth, nested, false},
		{filepath.Join(gitRepo, "polecats"), vcs.Git, gitRepo, false},
		{gitRepo, vcs.Git, gitRepo, false},
	}
	for _, c := range cases {
		k, r, err := Detect(c.dir)
		if err != nil || k != c.kind || r != c.root {
			t.Errorf("Detect(%s) = %s %s %v, want %s %s", c.dir, k, r, err, c.kind, c.root)
		}
	}

	both := filepath.Join(root, "both")
	fakeCheckout(t, both)
	os.MkdirAll(filepath.Join(both, ".git"), 0o755)
	var ae *AmbiguousError
	if _, _, err := Detect(filepath.Join(both)); !errors.As(err, &ae) {
		t.Errorf("both markers = %v, want *AmbiguousError", err)
	}

	// A .enth directory without a state file is not a checkout.
	half := filepath.Join(root, "half")
	os.MkdirAll(filepath.Join(half, ".enth"), 0o755)
	if k, _, err := Detect(half); err != nil || k != vcs.Git {
		t.Errorf("Detect(.enth with no checkout file) = %s %v, want git (no marker)", k, err)
	}
}

func TestExcludeAndRemoveRefuseANonCheckout(t *testing.T) {
	ctx := context.Background()
	var b Backend
	plain := t.TempDir()
	os.WriteFile(filepath.Join(plain, "keep"), []byte("k"), 0o644)
	if err := b.Exclude(ctx, plain, "# g", "x"); err == nil {
		t.Error("Exclude wrote into a directory that is not an enth checkout")
	}
	if err := b.Remove(ctx, vcs.Repo{}, plain); err == nil {
		t.Error("Remove deleted a directory that is not an enth checkout")
	}
	if _, err := os.Stat(filepath.Join(plain, "keep")); err != nil {
		t.Fatalf("the non-checkout lost its file: %v", err)
	}

	co := t.TempDir()
	fakeCheckout(t, co)
	for i := 0; i < 2; i++ {
		if err := b.Exclude(ctx, co, "# pogo persona", "AGENTS.override.md"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(co, ExcludeRelPath))
	if string(data) != "# pogo persona\n/AGENTS.override.md\n" {
		t.Errorf("exclude = %q, want one header and one anchored pattern", data)
	}

	var ue *vcs.UnnamedRepoError
	if _, err := b.Describe(ctx, co); !errors.As(err, &ue) || !strings.Contains(ue.Error(), "o/r on https://e.example") {
		t.Errorf("Describe without a record = %v, want an UnnamedRepoError naming the origin", err)
	}
	if r := b.Refresh(ctx, co); r.Status != vcs.RefreshDeclinedNoUpstream {
		t.Errorf("Refresh without a record = %s, want declined_no_upstream", r.Status)
	}
	if r := b.Refresh(ctx, plain); r.Status != vcs.RefreshSkipped {
		t.Errorf("Refresh of a non-checkout = %s, want skipped", r.Status)
	}

	if err := b.Remove(ctx, vcs.Repo{}, co); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(co); !os.IsNotExist(err) {
		t.Error("Remove left the checkout")
	}
}

func TestResolveBase(t *testing.T) {
	b := Backend{Repo: vcs.EnthRepo{Trunk: "refs/heads/trunk"}}
	for in, want := range map[string]string{"": "refs/heads/trunk", "main": "refs/heads/main", "refs/heads/rel": "refs/heads/rel"} {
		if got := b.ResolveBase(context.Background(), vcs.Repo{}, in); got != want {
			t.Errorf("ResolveBase(%q) = %q, want %q", in, got, want)
		}
	}
	if got := (Backend{}).ResolveBase(context.Background(), vcs.Repo{}, ""); got != vcs.DefaultEnthTrunk {
		t.Errorf("no trunk recorded: %q", got)
	}
}
