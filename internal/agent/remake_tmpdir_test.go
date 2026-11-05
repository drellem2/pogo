package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// The enth CONFLICT/RED re-make (drellem2/pogo#282). It used to be
// `REMAKE=$(mktemp -d)/w`: a full enth checkout plus its ~2.3G target/ in
// $TMPDIR, outside the polecat's tree, which no teardown and no gc sweep ever
// looks at. pm-pogo counted 19 of them on 2026-11-05. The re-make now goes
// beside the polecat's own tree, under the polecats dir, where gc's orphan-dir
// phase reaps it under the same gate as the tree.

// remakeLines extracts the shell that makes the re-make checkout from a
// rendered template: from the REMAKE assignment through the `enth checkout`.
func remakeLines(t *testing.T, rendered string) string {
	t.Helper()
	var out []string
	in := false
	for _, l := range strings.Split(rendered, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "N=1; while") || strings.HasPrefix(l, "REMAKE=") {
			in = true
		}
		if in {
			out = append(out, l)
			if strings.HasPrefix(l, "enth checkout ") {
				return strings.Join(out, "\n")
			}
		}
	}
	t.Fatalf("no re-make block in the render")
	return ""
}

// fakeEnth answers `enth checkout REF DIR` by making DIR a (one-file) enth
// checkout, and `enth status DIR` with a clean status.
func fakeEnth(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
checkout)
  [ -e "$3" ] && { echo "exists" >&2; exit 2; }
  mkdir -p "$3/.enth" || exit 1
  printf '%s\n%s\n' '{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"http://127.0.0.1:1","repo":"o/r"},"paths":[]}' \
    '{"path":"Cargo.toml","mode":"file","blob":"x","size":1,"mtime":1,"ino":1}' > "$3/.enth/checkout"
  : > "$3/Cargo.toml" ;;
status)
  echo '{"type":"status","dir":"'"$2"'","base":"b0","origin":{},"paths":[],"ops":0}' ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "enth"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runRemake runs the block twice (a second re-make), each followed by a
// stand-in ./build.sh output, and returns the two $REMAKE paths.
//
// The answer is read off $REMAKE itself, not off a private TMPDIR: macOS
// mktemp -d without a template IGNORES $TMPDIR and uses the per-user
// /var/folders/.../T, so an empty private TMPDIR would pass for the old line
// too. Measured while writing this test; the control below is what caught it.
func runRemake(t *testing.T, block string) []string {
	t.Helper()
	script := "set -e\n" + block + "\nmkdir -p target/debug && echo x > target/debug/enth\necho \"REMAKE=$REMAKE\"\n"
	var got []string
	for i := 0; i < 2; i++ {
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("re-make block: %v\n%s\n---\n%s", err, out, script)
		}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "REMAKE=") {
				got = append(got, strings.TrimPrefix(l, "REMAKE="))
			}
		}
	}
	if len(got) != 2 {
		t.Fatalf("re-make block did not report $REMAKE twice: %v", got)
	}
	return got
}

func underDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func TestEnthRemakeLeavesNothingInTMPDIR(t *testing.T) {
	fakeEnth(t)
	tmpls := embeddedPolecatTemplates(t)
	for _, name := range []string{"polecat.md", "polecat-architect.md"} {
		t.Run(name, func(t *testing.T) {
			polecats := t.TempDir()
			wt := filepath.Join(polecats, "pab12")
			if err := os.MkdirAll(wt, 0o755); err != nil {
				t.Fatal(err)
			}
			out := render(t, name, tmpls[name], TemplateVars{Task: "T", Id: "mg-ab12", Repo: "/r",
				WorktreeDir: wt, VCS: TemplateVCSEnth, EnthRemote: "https://e", EnthRepo: "o/r"})
			block := remakeLines(t, out)
			if strings.Contains(block, "mktemp") {
				t.Fatalf("the re-make still uses mktemp:\n%s", block)
			}
			for _, r := range runRemake(t, block) {
				if !underDir(polecats, r) {
					t.Fatalf("re-make %s is not beside the tree under %s", r, polecats)
				}
			}
			for _, d := range []string{"pab12-remake1", "pab12-remake2"} {
				if _, err := os.Stat(filepath.Join(polecats, d, ".enth", "checkout")); err != nil {
					t.Fatalf("re-make checkout %s not beside the tree: %v", d, err)
				}
			}
			// Teardown: the polecat's tree goes as it does at exit, and gc's
			// orphan-dir phase reaps the re-makes once the item has concluded.
			if err := os.RemoveAll(wt); err != nil {
				t.Fatal(err)
			}
			res, err := gitgc.Sweep(gitgc.Options{Repo: polecats, VCS: "enth", PolecatsDir: polecats,
				Tickets: gitgc.TicketIndex{"mg-ab12": gitgc.TicketDone}})
			if err != nil {
				t.Fatal(err)
			}
			if ents, _ := os.ReadDir(polecats); len(ents) != 0 {
				t.Fatalf("after teardown the polecats dir holds %d entries (removed %v, kept %v, errors %v)",
					len(ents), res.WorktreesRemoved, res.WorktreesKept, res.Errors)
			}
		})
	}
}

// Positive control: the old line, run the same way, DOES leave a checkout with
// its build output in the system temp dir, outside any polecats dir, where no
// sweep reaches it — so the result above is the fix, not the instrument.
func TestEnthRemakeOldLineLeavesTMPDIR(t *testing.T) {
	fakeEnth(t)
	polecats := t.TempDir()
	got := runRemake(t, "REMAKE=$(mktemp -d)/w\nenth checkout main \"$REMAKE\" && cd \"$REMAKE\"")
	for _, r := range got {
		r := r
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(r)) }) // the mktemp dir this test made
		if !strings.HasPrefix(filepath.Base(filepath.Dir(r)), "tmp.") || filepath.Base(r) != "w" {
			t.Fatalf("control: unexpected old-line path %s", r)
		}
		if _, err := os.Stat(filepath.Join(r, "target", "debug", "enth")); err != nil {
			t.Fatalf("control: no build output under the leaked checkout: %v", err)
		}
		if underDir(polecats, r) {
			t.Fatalf("control: old line landed under the polecats dir: %s", r)
		}
	}
	if _, err := gitgc.Sweep(gitgc.Options{Repo: polecats, VCS: "enth", PolecatsDir: polecats,
		Tickets: gitgc.TicketIndex{"mg-ab12": gitgc.TicketDone}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if _, err := os.Stat(r); err != nil {
			t.Fatalf("control: a sweep reached the old line's checkout %s: %v", r, err)
		}
	}
}

func TestEnthRemakeKeptWhileItemInFlight(t *testing.T) {
	fakeEnth(t)
	polecats := t.TempDir()
	wt := filepath.Join(polecats, "pab12")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpls := embeddedPolecatTemplates(t)
	out := render(t, "polecat.md", tmpls["polecat.md"], TemplateVars{Task: "T", Id: "mg-ab12", Repo: "/r",
		WorktreeDir: wt, VCS: TemplateVCSEnth, EnthRemote: "https://e", EnthRepo: "o/r"})
	runRemake(t, remakeLines(t, out))
	if _, err := gitgc.Sweep(gitgc.Options{Repo: polecats, VCS: "enth", PolecatsDir: polecats,
		Tickets: gitgc.TicketIndex{"mg-ab12": gitgc.TicketClaimed}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(polecats, "pab12-remake1")); err != nil {
		t.Fatalf("an in-flight polecat's re-make was reaped: %v", err)
	}
}
