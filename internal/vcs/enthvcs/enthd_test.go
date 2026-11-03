package enthvcs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// A sandboxed enthd serving one enth-written (enth-native) repo, for the
// tests that drive the real enth (enth #9: checkout through enthd, status,
// change). The enth binaries come from $POGO_ENTH_BIN_DIR, else PATH; an enth
// that predates #9 (no .enth/checkout, no status verb) skips the test, and
// POGO_ENTH_REQUIRE=1 turns that skip into a failure.

type enthd struct {
	bin    string // directory holding enth, enthd, enth-store, enth-change, enth-land
	root   string
	url    string
	token  string
	repo   string
	rootID string
}

func enthBinDir(t *testing.T) string {
	t.Helper()
	skip := func(why string) string {
		if os.Getenv("POGO_ENTH_REQUIRE") == "1" {
			t.Fatalf("POGO_ENTH_REQUIRE=1: %s", why)
		}
		t.Skip(why)
		return ""
	}
	dir := os.Getenv("POGO_ENTH_BIN_DIR")
	if dir == "" {
		p, err := exec.LookPath("enthd")
		if err != nil {
			return skip("no enthd on PATH and POGO_ENTH_BIN_DIR unset")
		}
		dir = filepath.Dir(p)
	}
	for _, b := range []string{"enth", "enthd", "enth-store", "enth-change", "enth-land"} {
		if _, err := os.Stat(filepath.Join(dir, b)); err != nil {
			return skip(fmt.Sprintf("%s has no %s", dir, b))
		}
	}
	help, _ := exec.Command(filepath.Join(dir, "enth"), "--help").CombinedOutput()
	if !bytes.Contains(help, []byte("status DIR")) {
		return skip(fmt.Sprintf("%s/enth predates enth #9 (no status verb)", dir))
	}
	return dir
}

var hashRE = regexp.MustCompile(`"hash":"([0-9a-f]+)"`)
var tokenRE = regexp.MustCompile(`"token":"(enth_[0-9a-f]+)"`)
var addrRE = regexp.MustCompile(`"addr":"([^"]+)"`)

func (e *enthd) cmd(t *testing.T, name string, args ...string) string {
	t.Helper()
	c := exec.Command(filepath.Join(e.bin, name), args...)
	c.Env = append(os.Environ(), "ENTH_TOKEN="+e.token,
		"ENTH_AUTHOR_NAME=worker", "ENTH_AUTHOR_EMAIL=worker@example.com",
		"ENTH_COMMITTER_NAME=worker", "ENTH_COMMITTER_EMAIL=worker@example.com")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (e *enthd) put(t *testing.T, store, file string) string {
	t.Helper()
	m := hashRE.FindStringSubmatch(e.cmd(t, "enth-store", "--store", store, "put", file))
	if m == nil {
		t.Fatalf("enth-store put %s: no hash", file)
	}
	return m[1]
}

// startEnthd creates alice/proj, an enth-written repo whose trunk holds
// a.txt and src/b.txt, and serves it. Every enth the test runs gets
// ENTH_TOKEN through the process environment, as pogod's children do.
func startEnthd(t *testing.T) *enthd {
	t.Helper()
	e := &enthd{bin: enthBinDir(t), root: filepath.Join(t.TempDir(), "R"), repo: "alice/proj"}
	m := tokenRE.FindStringSubmatch(e.cmd(t, "enthd", "--root", e.root, "user", "add", "alice", "--email", "alice@example.com"))
	if m == nil {
		t.Fatal("enthd user add printed no token")
	}
	e.token = m[1]
	e.cmd(t, "enthd", "--root", e.root, "repo", "create", e.repo, "--owner", "alice")
	store := filepath.Join(e.root, "repos", "alice~proj")
	w := t.TempDir()
	os.WriteFile(filepath.Join(w, "a"), []byte("a1\na2\n"), 0o644)
	os.WriteFile(filepath.Join(w, "b"), []byte("b1\n"), 0o644)
	ha, hb := e.put(t, store, filepath.Join(w, "a")), e.put(t, store, filepath.Join(w, "b"))
	spec := fmt.Sprintf(`{"parents":[],"author":{"name":"ann","email":"ann@example.com","time":1700000000,"tz":"+0000"},"committer":{"name":"ann","email":"ann@example.com","time":1700000000,"tz":"+0000"},"message":"root\n","ops":[{"path":"a.txt","op":"put","blob":"%s","mode":"file"},{"path":"src/b.txt","op":"put","blob":"%s","mode":"file"}]}`, ha, hb)
	os.WriteFile(filepath.Join(w, "spec"), []byte(spec), 0o644)
	e.cmd(t, "enth-change", "build", "--record", filepath.Join(w, "rec"), filepath.Join(w, "spec"))
	e.rootID = e.put(t, store, filepath.Join(w, "rec"))
	e.cmd(t, "enth-land", "--store", store, "--user", "operator", e.rootID)

	srv := exec.Command(filepath.Join(e.bin, "enthd"), "--root", e.root, "serve", "--listen", "127.0.0.1:0", "--follow-interval", "0")
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	srv.Stderr = nil
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Kill(); srv.Wait() })
	addr := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m := addrRE.FindStringSubmatch(sc.Text()); m != nil {
				addr <- m[1]
				break
			}
		}
		for sc.Scan() {
		}
	}()
	select {
	case a := <-addr:
		e.url = "http://" + a
	case <-time.After(20 * time.Second):
		t.Fatal("enthd did not report listening")
	}
	t.Setenv("ENTH_TOKEN", e.token)
	// Nothing in these tests may reach a default remote: every verb names its
	// remote or reads a checkout.
	t.Setenv("ENTH_REMOTE", "")
	t.Setenv("ENTH_REPO", "")
	return e
}

func (e *enthd) backend(paths ...string) Backend {
	return Backend{
		Repo: vcs.EnthRepo{Remote: e.url, Repo: e.repo, Trunk: vcs.DefaultEnthTrunk, WorkspacePaths: paths},
		Bin:  filepath.Join(e.bin, "enth"),
	}
}

// change makes a Change of dir's edits through enthd, as a worker would.
func (e *enthd) change(t *testing.T, dir, msg string) {
	t.Helper()
	e.cmd(t, "enth", "change", "-m", msg, dir)
}

// land lands id on the trunk through enthd.
func (e *enthd) land(t *testing.T, id string) {
	t.Helper()
	e.cmd(t, "enth", "--remote", e.url, "--repo", e.repo, "land", id)
}

func TestCreateDirtyRemoveAgainstEnthd(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "pcat")
	src := vcs.Repo{Path: "/src/proj"}

	ws, err := b.Create(ctx, vcs.WorkspaceSpec{Repo: src, Dir: dir, Base: b.ResolveBase(ctx, src, "")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ws.Base != e.rootID {
		t.Errorf("base = %s, want the trunk tip %s", ws.Base, e.rootID)
	}
	for _, f := range []string{"a.txt", "src/b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("whole-repo checkout lacks %s: %v", f, err)
		}
	}
	st, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Origin.Remote != e.url || st.Origin.Repo != e.repo || len(st.Paths) != 0 {
		t.Errorf("state origin/paths = %+v %v, want %s %s and the whole tree", st.Origin, st.Paths, e.url, e.repo)
	}

	// Clean right after checkout — and still clean once pogod's own files
	// are written, because Create excluded them.
	if d, err := b.Dirty(ctx, dir); err != nil || len(d) != 0 {
		t.Fatalf("fresh checkout Dirty = %v, %v; want clean", d, err)
	}
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(dir, "src", ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", ".claude", "pogo-mailhook.stamp"), nil, 0o644)
	if d, err := b.Dirty(ctx, dir); err != nil || len(d) != 0 {
		t.Fatalf("pogod's own files made the tree dirty: %v, %v", d, err)
	}
	// Positive control for the exclude: another file under .claude/ counts.
	os.WriteFile(filepath.Join(dir, ".claude", "notes.md"), []byte("x"), 0o644)
	if d, _ := b.Dirty(ctx, dir); len(d) != 1 || d[0] != "?? .claude/notes.md" {
		t.Fatalf("Dirty = %q, want [?? .claude/notes.md] (the exclude must be exactly pogod's files)", d)
	}
	os.Remove(filepath.Join(dir, ".claude", "notes.md"))

	// Edit, add, delete: each is listed in porcelain shape.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a1\nA2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n\n"), 0o644)
	os.Remove(filepath.Join(dir, "src", "b.txt"))
	d, err := b.Dirty(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{" M a.txt", "?? new.txt", " D src/b.txt"}
	if strings.Join(d, "|") != strings.Join(want, "|") {
		t.Errorf("Dirty = %q, want %q", d, want)
	}

	// Describe: the pogo repo from the record, the base from the state.
	got, err := b.Describe(ctx, filepath.Join(dir, "src"))
	if err != nil || got.Repo.Path != src.Path || got.Base != e.rootID || got.Branch != "" {
		t.Errorf("Describe = %+v, %v; want repo %s base %s, no branch", got, err, src.Path, e.rootID)
	}
	if r, err := b.Root(ctx, filepath.Join(dir, "src")); err != nil || r != dir {
		// dir may be under a symlinked temp root; compare resolved.
		if rr, _ := filepath.EvalSymlinks(r); err != nil || rr != mustEval(t, dir) {
			t.Errorf("Root = %q, %v; want %s", r, err, dir)
		}
	}

	// A Change commits the edits: clean again, nothing lost on removal.
	e.change(t, dir, "work")
	if d, err := b.Dirty(ctx, dir); err != nil || len(d) != 0 {
		t.Fatalf("after enth change Dirty = %v, %v; want clean", d, err)
	}
	if err := b.Remove(ctx, src, dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Remove left %s behind", dir)
	}
	if err := b.Remove(ctx, src, dir); err != nil {
		t.Errorf("Remove of an absent dir = %v, want nil", err)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNarrowedWorkspaceAndOutsideSelection(t *testing.T) {
	e := startEnthd(t)
	b := e.backend("src")
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "narrow")
	if _, err := b.Create(ctx, vcs.WorkspaceSpec{Repo: vcs.Repo{Path: "/src/proj"}, Dir: dir, Base: b.ResolveBase(ctx, vcs.Repo{}, "main")}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("a narrowed checkout wrote a.txt (outside src): %v", err)
	}
	st, _ := ReadState(dir)
	if strings.Join(st.Paths, ",") != "src" {
		t.Errorf("selection = %v, want [src]", st.Paths)
	}
	rec, _ := ReadRecord(dir)
	if strings.Join(rec.Paths, ",") != "src" || rec.Ref != "refs/heads/main" {
		t.Errorf("record = %+v", rec)
	}
	// A new file outside the selection is uncommitted work, not an error.
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("t"), 0o644)
	d, err := b.Dirty(ctx, dir)
	if err != nil || len(d) != 1 || d[0] != "?? top.txt" {
		t.Fatalf("Dirty with an outside_selection file = %q, %v; want [?? top.txt]", d, err)
	}
}

func TestRefreshAgainstEnthd(t *testing.T) {
	e := startEnthd(t)
	b := e.backend()
	ctx := context.Background()
	crew := filepath.Join(t.TempDir(), "crew")
	if _, err := b.Create(ctx, vcs.WorkspaceSpec{Dir: crew}); err != nil {
		t.Fatal(err)
	}
	// An ignored file must survive a refresh.
	os.MkdirAll(filepath.Join(crew, ".claude"), 0o755)
	os.WriteFile(filepath.Join(crew, ".claude", "settings.local.json"), []byte("{}"), 0o644)

	if r := b.Refresh(ctx, crew); r.Status != vcs.RefreshAlreadyCurrent {
		t.Fatalf("Refresh at the tip = %s (%s), want already_current", r.Status, r.Detail)
	}

	// Another worker lands a change to a.txt and deletes src/b.txt.
	other := filepath.Join(t.TempDir(), "other")
	if _, err := b.Create(ctx, vcs.WorkspaceSpec{Dir: other}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(other, "a.txt"), []byte("a1\nLANDED\n"), 0o644)
	os.Remove(filepath.Join(other, "src", "b.txt"))
	os.MkdirAll(filepath.Join(other, "lib"), 0o755)
	os.WriteFile(filepath.Join(other, "lib", "c.txt"), []byte("c\n"), 0o644)
	e.change(t, other, "upstream")
	ost, _ := ReadState(other)
	e.land(t, ost.Base)

	// Dirty refuses.
	os.WriteFile(filepath.Join(crew, "scratch"), []byte("s"), 0o644)
	if r := b.Refresh(ctx, crew); r.Status != vcs.RefreshDeclinedDirty {
		t.Fatalf("dirty Refresh = %s, want declined_dirty", r.Status)
	}
	os.Remove(filepath.Join(crew, "scratch"))

	r := b.Refresh(ctx, crew)
	if r.Status != vcs.RefreshUpdated {
		t.Fatalf("Refresh = %s (%s), want updated", r.Status, r.Detail)
	}
	if data, _ := os.ReadFile(filepath.Join(crew, "a.txt")); string(data) != "a1\nLANDED\n" {
		t.Errorf("a.txt = %q after refresh", data)
	}
	if _, err := os.Stat(filepath.Join(crew, "src", "b.txt")); !os.IsNotExist(err) {
		t.Errorf("src/b.txt survived its upstream deletion")
	}
	if _, err := os.Stat(filepath.Join(crew, "lib", "c.txt")); err != nil {
		t.Errorf("lib/c.txt missing after refresh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(crew, ".claude", "settings.local.json")); err != nil {
		t.Errorf("an ignored file did not survive the refresh: %v", err)
	}
	if d, err := b.Dirty(ctx, crew); err != nil || len(d) != 0 {
		t.Errorf("refreshed tree is not clean against its new state: %v, %v", d, err)
	}
	if again := b.Refresh(ctx, crew); again.Status != vcs.RefreshAlreadyCurrent {
		t.Errorf("second Refresh = %s, want already_current", again.Status)
	}

	// A Change made in the workspace: its base is no longer the recorded one.
	os.WriteFile(filepath.Join(crew, "mine.txt"), []byte("m\n"), 0o644)
	e.change(t, crew, "mine")
	if r := b.Refresh(ctx, crew); r.Status != vcs.RefreshDeclinedDiverged {
		t.Errorf("Refresh after a local Change = %s, want declined_diverged", r.Status)
	}
}
