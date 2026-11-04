package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

// The rendered self_land, run in bash against a real enthd: the land is gated
// on the MERGED tree (mg-64f2e). These tests replay the 2026-11-04 10:05Z
// break. A removes f in lib.txt and B calls it in use.txt. The paths differ,
// so enth merges the two cleanly, and each is green on its own base. A lands
// WHILE B's gate is running, which is the worst case: B passed its tree, and
// that tree is no longer what would land. The control is the same race with a
// compatible A, which lands both.

// buildSh is the repo's gate: a tree that calls f must define f. When
// $RACE_LAND is set it lands that Change from inside the first run, so the
// trunk moves between B's gate and B's land.
const buildSh = `#!/bin/sh
echo run >> "$GATE_RUNS"
if [ -n "$RACE_LAND" ] && [ ! -f "$RACE_DONE" ]; then
  touch "$RACE_DONE"
  enth land "$RACE_LAND" >/dev/null || exit 99
fi
if grep -q '^call f$' use.txt 2>/dev/null; then grep -q '^def f$' lib.txt; fi
`

var selfLandAttestToken = regexp.MustCompile(`"token":"(enth_[0-9a-f]+)"`)

type selfLandRig struct {
	s       *enthdtest.Server
	bin     string // PATH prefix: the stub pogo, enth-land-gate, then the enth binaries
	runs    string
	landLog string
	fn      string // the rendered self_land function
	id      string
}

func newSelfLandRig(t *testing.T) *selfLandRig {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	s := enthdtest.Start(t)
	help, _ := exec.Command(s.Enth(), "land", "--help").CombinedOutput()
	if !bytes.Contains(help, []byte("--require")) {
		why := s.BinDir + "/enth land has no --require (enth #9 item L, mg-b729a)"
		if os.Getenv("POGO_ENTH_REQUIRE") == "1" {
			t.Fatal("POGO_ENTH_REQUIRE=1: " + why)
		}
		t.Skip(why)
	}
	m := selfLandAttestToken.FindStringSubmatch(s.Cmd(t, "enthd", "--root", s.Root, "token", "add", "alice", "--attest", "ci/selfland/build"))
	if m == nil {
		t.Fatal("enthd token add --attest printed no token")
	}
	s.Token = m[1]
	t.Setenv("ENTH_TOKEN", s.Token)

	r := &selfLandRig{s: s, bin: t.TempDir(), runs: filepath.Join(t.TempDir(), "runs"), landLog: filepath.Join(t.TempDir(), "landed")}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(r.bin, "enth-land-gate"), "../../cmd/enth-land-gate").CombinedOutput(); err != nil {
		t.Fatalf("building enth-land-gate: %v\n%s", err, out)
	}
	// The stub pogo: presubmit prints the stack under test, and land records
	// what it was handed. What is under test is the gate and the land, not pogod.
	stub := "#!/bin/sh\ncase \"$1\" in\n  presubmit) echo \"$STACK\" ;;\n  land) for a; do case \"$a\" in --landed-file=*) cat \"${a#--landed-file=}\" >> \"$LAND_LOG\" ;; esac; done ;;\n  *) exit 9 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(r.bin, "pogo"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	// The trunk holds the gate and a lib defining f.
	sd := filepath.Join(t.TempDir(), "seed")
	s.Checkout(t, "main", sd)
	if err := os.WriteFile(filepath.Join(sd, "build.sh"), []byte(buildSh), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sd, "lib.txt"), []byte("def f\n"), 0o644)
	s.Land(t, s.Change(t, sd, "seed\n"))

	r.id = fmt.Sprintf("mg-slg%d", time.Now().UnixNano()%1e9)
	t.Cleanup(func() {
		for _, sfx := range []string{"attempt", "pending", "out", "err", "gate", "gerr"} {
			os.Remove("/tmp/" + r.id + "-land." + sfx)
		}
	})
	out := render(t, "polecat.md", embeddedPolecatTemplates(t)["polecat.md"], TemplateVars{
		Id: r.id, Repo: "/r", WorktreeDir: "/w", VCS: TemplateVCSEnth, EnthRemote: s.URL, EnthRepo: s.Repo})
	i := strings.Index(out, "self_land() {")
	j := strings.Index(out[i:], "\n   }\n")
	if i < 0 || j < 0 {
		t.Fatal("no self_land in the render")
	}
	r.fn = out[i : i+j+len("\n   }\n")]
	return r
}

// selfLand runs the rendered function for stack from dir, with race (may be
// "") landed from inside the first gate run. It returns the exit code and the
// output.
func (r *selfLandRig) selfLand(t *testing.T, dir, stack, race string) (int, string) {
	t.Helper()
	script := r.fn + "\nself_land\n"
	c := exec.Command("bash", "-c", script)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"PATH="+r.bin+string(os.PathListSeparator)+r.s.BinDir+string(os.PathListSeparator)+pathWithoutLandGate(os.Getenv("PATH")),
		"ENTH_REMOTE="+r.s.URL, "ENTH_REPO="+r.s.Repo, "ENTH_TOKEN="+r.s.Token,
		"STACK="+stack, "LAND_LOG="+r.landLog, "GATE_RUNS="+r.runs,
		"RACE_LAND="+race, "RACE_DONE="+filepath.Join(filepath.Dir(r.runs), "race-done-"+stack[:8]))
	out, err := c.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(out)
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, string(out)
}

// pathWithoutLandGate drops every entry of path that holds an
// enth-land-gate. The only gate a rig may see is its own copy in r.bin. A gate
// installed on the host (~/.local/bin is on pogod's PATH) otherwise turns the
// "without the gate" case into a GATED land (mg-ca090).
func pathWithoutLandGate(path string) string {
	var keep []string
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(d, "enth-land-gate")); err == nil && !fi.IsDir() {
			continue
		}
		keep = append(keep, d)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

func (r *selfLandRig) runCount() int {
	b, _ := os.ReadFile(r.runs)
	return strings.Count(string(b), "run")
}

func TestSelfLandGateRefusesTheBrokenPair(t *testing.T) {
	r := newSelfLandRig(t)
	s := r.s
	a, _ := s.Work(t, "mg-a", "", "A: drop f", map[string]string{"lib.txt": "def g\n"})
	b, bdir := s.Work(t, "mg-b", "", "B: call f", map[string]string{"use.txt": "call f\n"})
	before := s.Tip(t)

	code, out := r.selfLand(t, bdir, b, a)
	if code != 5 || !strings.Contains(out, "RED:") {
		t.Fatalf("self_land of B: exit %d, want 5 (RED)\n%s", code, out)
	}
	// The order of events: B's first gate passed on its own merged tree, A
	// landed during it, the land was refused, and the re-gate went red.
	if !strings.Contains(out, "GATED (mg-64f2e): ") || !strings.Contains(out, "unattested_tree: the trunk moved since the gate") {
		t.Fatalf("self_land did not gate, get refused, then re-gate:\n%s", out)
	}
	if tip := s.Tip(t); tip == before {
		t.Fatal("control: A did not land during the gate, so the race was not exercised")
	}
	if got := s.Cmd(t, "enth", append(s.Remote(), "query", "ls", "-r")...); strings.Contains(got, "use.txt") {
		t.Fatalf("B landed: the trunk has use.txt\n%s", got)
	}
	if b, _ := os.ReadFile(r.landLog); len(b) != 0 {
		t.Fatalf("pogo land was handed a land: %s", b)
	}
	if n := r.runCount(); n != 2 {
		t.Fatalf("the gate ran %d times, want 2 (B's own merge, then the merge with A)", n)
	}
}

func TestSelfLandGateLandsTheCompatiblePair(t *testing.T) {
	r := newSelfLandRig(t)
	s := r.s
	a, _ := s.Work(t, "mg-a", "", "A: add g", map[string]string{"lib2.txt": "def g\n"})
	b, bdir := s.Work(t, "mg-b", "", "B: call f", map[string]string{"use.txt": "call f\n"})

	code, out := r.selfLand(t, bdir, b, a)
	if code != 0 || !strings.Contains(out, "LANDED as") {
		t.Fatalf("self_land of B: exit %d, want a land\n%s", code, out)
	}
	if !strings.Contains(out, "unattested_tree: the trunk moved since the gate") {
		t.Fatalf("control: the race was not exercised (no unattested_tree):\n%s", out)
	}
	got := s.Cmd(t, "enth", append(s.Remote(), "query", "ls", "-r")...)
	if !strings.Contains(got, "use.txt") || !strings.Contains(got, "lib2.txt") {
		t.Fatalf("both should be on the trunk:\n%s", got)
	}
	if b, _ := os.ReadFile(r.landLog); !bytes.Contains(b, []byte(`"type":"landed"`)) {
		t.Fatalf("pogo land was not handed the land: %q", b)
	}
}

// Reuse. A Change on the tip runs the gate once, and the land goes through.
func TestSelfLandGateOnTipRunsOnce(t *testing.T) {
	r := newSelfLandRig(t)
	c, cdir := r.s.Work(t, "mg-c", "", "C", map[string]string{"use.txt": "call f\n"})
	code, out := r.selfLand(t, cdir, c, "")
	if code != 0 || !strings.Contains(out, "LANDED as") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if n := r.runCount(); n != 1 {
		t.Fatalf("the gate ran %d times, want 1", n)
	}
}

// Without the gate installed, self_land lands as it did before mg-64f2e,
// untested, and says so on its own greppable line (pm-pogo's ruling,
// 2026-11-04 17:33Z). The flip to fail-closed is mg-2000f.
func TestSelfLandWithoutTheGateLandsUntestedAndSaysSo(t *testing.T) {
	// A gate installed on the host must not count as installed in the rig
	// (mg-ca090). Plant one on the inherited PATH so this case is exercised on
	// every box, not only on one that happens to have the gate installed.
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "enth-land-gate"), []byte("#!/bin/sh\necho HOST GATE RAN; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", host+string(os.PathListSeparator)+os.Getenv("PATH"))
	if p, err := exec.LookPath("enth-land-gate"); err != nil || p != filepath.Join(host, "enth-land-gate") {
		t.Fatalf("control: the planted host gate is not on the inherited PATH (%q, %v)", p, err)
	}
	r := newSelfLandRig(t)
	os.Remove(filepath.Join(r.bin, "enth-land-gate"))
	c, cdir := r.s.Work(t, "mg-c", "", "C", map[string]string{"use.txt": "call f\n"})
	code, out := r.selfLand(t, cdir, c, "")
	if code != 0 || !strings.Contains(out, "\nGATE NOT INSTALLED — landing untested (mg-64f2e)\n") && !strings.HasPrefix(out, "GATE NOT INSTALLED — landing untested (mg-64f2e)\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "LANDED as") || r.runCount() != 0 || strings.Contains(out, "HOST GATE RAN") {
		t.Fatalf("want an ungated land (gate runs %d)\n%s", r.runCount(), out)
	}
}

// pathWithoutLandGate drops only the directories that hold a gate, so the
// rig still finds bash, go and enth on the host PATH.
func TestPathWithoutLandGate(t *testing.T) {
	with, without := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(with, "enth-land-gate"), []byte("#!/bin/sh\n"), 0o755)
	sep := string(os.PathListSeparator)
	if got := pathWithoutLandGate(without + sep + with + sep + without); got != without+sep+without {
		t.Fatalf("got %q", got)
	}
}

// Installed and red is never the fallback: the broken pair is refused even
// though an ungated land would have gone through (the control: the test
// above lands without the gate).
func TestSelfLandInstalledGateHasNoFallback(t *testing.T) {
	r := newSelfLandRig(t)
	a, _ := r.s.Work(t, "mg-a", "", "A: drop f", map[string]string{"lib.txt": "def g\n"})
	r.s.Land(t, a)
	b, bdir := r.s.Work(t, "mg-b", "", "B: call f", map[string]string{"use.txt": "call f\n"})
	_ = b
	code, out := r.selfLand(t, bdir, b, "")
	if code != 5 || strings.Contains(out, "GATE NOT INSTALLED") || strings.Contains(out, "LANDED") {
		t.Fatalf("exit %d, want 5 and no land\n%s", code, out)
	}
}

// TEST-ONLY: the gate is installed, but the token cannot attest (enth.app
// before mg-e9617). The broken pair is still refused. B's test passes, A
// lands during it, and the re-check right before the land finds a different
// tree, so B is tested again, red, and never landed.
func TestSelfLandTestOnlyRefusesTheBrokenPair(t *testing.T) {
	r := newSelfLandRig(t)
	s := r.s
	m := selfLandAttestToken.FindStringSubmatch(s.Cmd(t, "enthd", "--root", s.Root, "token", "add", "alice"))
	if m == nil {
		t.Fatal("no plain token")
	}
	s.Token = m[1]
	t.Setenv("ENTH_TOKEN", s.Token)
	a, _ := s.Work(t, "mg-a", "", "A: drop f", map[string]string{"lib.txt": "def g\n"})
	b, bdir := s.Work(t, "mg-b", "", "B: call f", map[string]string{"use.txt": "call f\n"})
	code, out := r.selfLand(t, bdir, b, a)
	if code != 5 || !strings.Contains(out, "TEST-ONLY: the trunk moved since the test") || strings.Contains(out, "LANDED") {
		t.Fatalf("exit %d, want 5 after a re-test\n%s", code, out)
	}
	if got := s.Cmd(t, "enth", append(s.Remote(), "query", "ls", "-r")...); strings.Contains(got, "use.txt") {
		t.Fatal("B landed")
	}

	// Control: a compatible Change on the new tip lands TEST-ONLY.
	c, cdir := s.Work(t, "mg-c", "", "C", map[string]string{"c.txt": "c\n"})
	code, out = r.selfLand(t, cdir, c, "")
	if code != 0 || !strings.Contains(out, "TEST-ONLY (mg-64f2e): tree") || !strings.Contains(out, "LANDED as") {
		t.Fatalf("control: exit %d\n%s", code, out)
	}
}
