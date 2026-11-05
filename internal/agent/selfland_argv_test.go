package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// The rendered self_land run under the shell agents actually use (zsh -c, and
// bash too), against stub enth / enth-land-gate / pogo that record their argv
// (mg-c07a5). Two defects are pinned here:
//
//  1. zsh does not word-split an unquoted parameter (SH_WORD_SPLIT is off), so
//     `$stack` holding two Change ids reached enth as ONE argument, and `$lreq`
//     ("--require ci/selfland/build") reached enth land as ONE argument.
//  2. The gate was always ./build.sh, whatever the repo declares as its merge
//     gate in .pogo/refinery.toml — a weaker command than enth's own
//     `ENTH_REQUIRE_WEB=1 ./build.sh`.
//
// `pogo refinery gate` is the REAL pogo built from this tree: the stub pogo
// hands that subcommand to it, so what is tested is the template plus the
// resolver the refinery's config parser backs.

var (
	realPogoOnce sync.Once
	realPogoPath string
	realPogoErr  error
)

func buildRealPogo(t *testing.T) string {
	t.Helper()
	realPogoOnce.Do(func() {
		// Inside the package sandbox, which TestMain removes: a temp dir of its
		// own would outlive the run.
		dir := filepath.Join(sandbox.Root, "selfland-pogo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			realPogoErr = err
			return
		}
		realPogoPath = filepath.Join(dir, "pogo")
		if out, err := exec.Command("go", "build", "-o", realPogoPath, "../../cmd/pogo").CombinedOutput(); err != nil {
			realPogoErr = fmt.Errorf("building pogo: %v\n%s", err, out)
		}
	})
	if realPogoErr != nil {
		t.Fatal(realPogoErr)
	}
	return realPogoPath
}

// argvStub records one invocation as "== <name>" then one "[<arg>]" line per
// argument, so an argument holding a newline or a space cannot pass for two.
const argvStubHead = `#!/bin/sh
{ echo "== $(basename "$0")"; for a; do printf '[%s]\n' "$a"; done; } >> "$ARGV_LOG"
`

var selfLandStubs = map[string]string{
	"enth": argvStubHead + `case "$1 $2" in
  "land --help") echo "  --require JOB   land only a tree with a pass of JOB" ;;
  "land --dry-run") echo '{"type":"landed","dry_run":true,"tree":"t0"}' ;;
  "land "*) echo '{"type":"landed","new":"n1"}' ;;
  *) exit 9 ;;
esac
`,
	// Runs its --gate exactly as enth-land-gate does (sh -c in the tree), and
	// keeps what it printed, so a test can see which command actually ran.
	"enth-land-gate": argvStubHead + `for a; do case "$a" in --gate=*) g="${a#--gate=}" ;; esac; done
/bin/sh -c "$g" >> "$GATE_RAN" 2>&1 || exit 1
echo '{"type":"gated","job":"ci/selfland/build","tree":"t0","result":"pass"}'
`,
	"pogo": argvStubHead + `case "$1" in
  presubmit) printf '%s\n' "$STACK" ;;
  refinery) exec "$REAL_POGO" "$@" ;;
  land) ;;
  *) exit 9 ;;
esac
`,
}

type argvRig struct {
	bin, log, fixture string
	id                string
}

// newArgvRig makes the stubs and a fixture checkout. decl, when non-empty, is
// written as the fixture's .pogo/refinery.toml quality_gate.
func newArgvRig(t *testing.T, decl string) *argvRig {
	t.Helper()
	r := &argvRig{bin: t.TempDir(), log: filepath.Join(t.TempDir(), "argv"), fixture: t.TempDir(),
		id: fmt.Sprintf("mg-sla%d", time.Now().UnixNano()%1e9)}
	for name, body := range selfLandStubs {
		if err := os.WriteFile(filepath.Join(r.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture's build.sh says which environment it ran under.
	if err := os.WriteFile(filepath.Join(r.fixture, "build.sh"), []byte("#!/bin/sh\necho \"build.sh ran with FOO=${FOO:-unset}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if decl != "" {
		os.MkdirAll(filepath.Join(r.fixture, ".pogo"), 0o755)
		if err := os.WriteFile(filepath.Join(r.fixture, ".pogo", "refinery.toml"), []byte("quality_gate = \""+decl+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, sfx := range []string{"attempt", "pending", "out", "err", "gate", "gerr"} {
			os.Remove("/tmp/" + r.id + "-land." + sfx)
		}
	})
	return r
}

// selfLandFn renders tmpl for the enth VCS and lifts its self_land function.
func selfLandFn(t *testing.T, tmpl, id, repo string) string {
	t.Helper()
	out := render(t, tmpl, embeddedPolecatTemplates(t)[tmpl], TemplateVars{
		Id: id, Repo: repo, WorktreeDir: "/w", VCS: TemplateVCSEnth, EnthRemote: "http://enthd.invalid", EnthRepo: "r"})
	i := strings.Index(out, "self_land() {")
	if i < 0 {
		t.Fatalf("%s: no self_land in the render", tmpl)
	}
	// The function's closing brace sits at the indentation of its opening line.
	ind := out[strings.LastIndex(out[:i], "\n")+1 : i]
	end := "\n" + ind + "}\n"
	j := strings.Index(out[i:], end)
	if j < 0 {
		t.Fatalf("%s: self_land has no closing brace at indent %q", tmpl, ind)
	}
	return out[i : i+j+len(end)]
}

// run runs fn's self_land under shell from the fixture and returns the exit
// code, the output (with what the gate printed appended), and each recorded invocation's argv by program.
func (r *argvRig) run(t *testing.T, shell, fn, stack string) (int, string, map[string][][]string) {
	t.Helper()
	os.Remove(r.log)
	os.Remove(r.log + ".ran")
	c := exec.Command(shell, "-c", fn+"\nself_land\n")
	c.Dir = r.fixture
	c.Env = append(os.Environ(),
		"PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ARGV_LOG="+r.log, "GATE_RAN="+r.log+".ran", "STACK="+stack, "REAL_POGO="+buildRealPogo(t))
	b, err := c.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	ran, _ := os.ReadFile(r.log + ".ran")
	return code, string(b) + "--- the gate printed:\n" + string(ran), parseArgvLog(t, r.log)
}

func parseArgvLog(t *testing.T, path string) map[string][][]string {
	t.Helper()
	raw, _ := os.ReadFile(path)
	calls := map[string][][]string{}
	var name string
	var cur []string
	flush := func() {
		if name != "" {
			calls[name] = append(calls[name], cur)
		}
	}
	// An argument may hold a newline, so "[" ... "]" may span lines.
	var arg strings.Builder
	open := false
	for _, l := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		switch {
		case open:
			arg.WriteString("\n" + l)
		case strings.HasPrefix(l, "== "):
			flush()
			name, cur = strings.TrimPrefix(l, "== "), nil
			continue
		case strings.HasPrefix(l, "["):
			arg.Reset()
			arg.WriteString(l[1:])
			open = true
		default:
			if l != "" {
				t.Fatalf("unreadable argv log line %q", l)
			}
			continue
		}
		if s := arg.String(); strings.HasSuffix(s, "]") {
			cur = append(cur, strings.TrimSuffix(s, "]"))
			open = false
		}
	}
	flush()
	return calls
}

// landCall is the one enth land that is neither --help nor --dry-run.
func landCall(t *testing.T, calls map[string][][]string, out string) []string {
	t.Helper()
	var got [][]string
	for _, a := range calls["enth"] {
		if len(a) > 1 && a[0] == "land" && a[1] != "--help" && a[1] != "--dry-run" {
			got = append(got, a)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one enth land, got %d: %q\n%s", len(got), got, out)
	}
	return got[0]
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

func selfLandShells(t *testing.T) []string {
	var sh []string
	for _, s := range []string{"zsh", "bash"} {
		if _, err := exec.LookPath(s); err == nil {
			sh = append(sh, s)
		}
	}
	if len(sh) == 0 || sh[0] != "zsh" {
		t.Skip("no zsh: the shell agents run is what this tests")
	}
	return sh
}

// Positive control for the harness: the shell under test really does NOT
// split an unquoted parameter. If zsh ever split it, a pass below would say
// nothing about zsh.
func TestSelfLandHarnessShellDoesNotWordSplit(t *testing.T) {
	selfLandShells(t)
	out, err := exec.Command("zsh", "-c", `stack=$(printf "a\nb\n"); printf "[%s]\n" $stack`).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "[a\nb]\n" {
		t.Fatalf("zsh split an unquoted parameter (%q): this harness cannot tell zsh from bash", got)
	}
}

func TestSelfLandPassesSeparateArgvUnderZsh(t *testing.T) {
	// The 1-Change stack isolates --require: before mg-c07a5 it failed alone
	// under zsh, where the 2-Change stack failed first at the gate.
	for _, ids := range [][]string{{"c1aaaa", "c2bbbb"}, {"c1aaaa"}} {
		for _, shell := range selfLandShells(t) {
			for _, tmpl := range []string{"polecat.md", "polecat-architect.md"} {
				t.Run(fmt.Sprintf("%d-change/%s/%s", len(ids), shell, tmpl), func(t *testing.T) {
					r := newArgvRig(t, "")
					fn := selfLandFn(t, tmpl, r.id, r.fixture)
					// What the real producer prints: one line, space-separated (mg-88ece).
					// A newline-joined stub stack is how this test once passed while
					// every real 2-Change land failed.
					code, out, calls := r.run(t, shell, fn, enthvcs.PrintStackLine(ids))
					// 1. the stack is one arg per Change id, to the gate and to the land.
					g := calls["enth-land-gate"]
					if len(g) != 1 || len(g[0]) < len(ids) || strings.Join(g[0][len(g[0])-len(ids):], " ") != strings.Join(ids, " ") {
						t.Fatalf("enth-land-gate argv %q: want the %d Change ids as its last %d args\n%s", g, len(ids), len(ids), out)
					}
					if code != 0 || !strings.Contains(out, "LANDED as "+r.id+"/1") {
						t.Fatalf("self_land: exit %d\n%s", code, out)
					}
					land := landCall(t, calls, out)
					i := indexOf(land, ids[0])
					if i < 0 || i+len(ids) > len(land) || strings.Join(land[i:i+len(ids)], " ") != strings.Join(ids, " ") {
						t.Fatalf("enth land argv %q: want %q as %d args", land, ids, len(ids))
					}
					// 2. --require and its job are 2 args.
					k := indexOf(land, "--require")
					if k < 0 || k+1 >= len(land) || land[k+1] != "ci/selfland/build" {
						t.Fatalf("enth land argv %q: want --require and ci/selfland/build as 2 args", land)
					}
					for _, a := range land {
						if strings.ContainsAny(a, " \n") {
							t.Fatalf("enth land got a joined argument %q in %q", a, land)
						}
					}
				})
			}
		}
	}
}

func TestSelfLandRunsTheDeclaredGate(t *testing.T) {
	for _, shell := range selfLandShells(t) {
		for _, tmpl := range []string{"polecat.md", "polecat-architect.md"} {
			t.Run(shell+"/"+tmpl, func(t *testing.T) {
				r := newArgvRig(t, "FOO=1 ./build.sh")
				code, out, calls := r.run(t, shell, selfLandFn(t, tmpl, r.id, r.fixture), "c1aaaa")
				if code != 0 {
					t.Fatalf("self_land: exit %d\n%s", code, out)
				}
				if !strings.Contains(out, "GATE COMMAND: FOO=1 ./build.sh\n") || !strings.Contains(out, "build.sh ran with FOO=1") {
					t.Fatalf("the declared gate FOO=1 ./build.sh was not shown and run:\n%s", out)
				}
				if strings.Contains(out, "fell back") {
					t.Fatalf("a declared gate reported a fallback:\n%s", out)
				}
				if g := calls["enth-land-gate"]; len(g) != 1 || indexOf(g[0], "--gate=FOO=1 ./build.sh") < 0 {
					t.Fatalf("enth-land-gate argv %q: want --gate=FOO=1 ./build.sh as one arg", g)
				}
			})
		}
	}
}

func TestSelfLandFallsBackToBuildShAndSaysSo(t *testing.T) {
	for _, shell := range selfLandShells(t) {
		t.Run(shell, func(t *testing.T) {
			r := newArgvRig(t, "")
			code, out, calls := r.run(t, shell, selfLandFn(t, "polecat.md", r.id, r.fixture), "c1aaaa")
			if code != 0 {
				t.Fatalf("self_land: exit %d\n%s", code, out)
			}
			if !strings.Contains(out, "fell back to ./build.sh") || !strings.Contains(out, "GATE COMMAND: ./build.sh\n") ||
				!strings.Contains(out, "build.sh ran with FOO=unset") {
				t.Fatalf("no declaration: want ./build.sh run and a fallback notice:\n%s", out)
			}
			if g := calls["enth-land-gate"]; len(g) != 1 || indexOf(g[0], "--gate=./build.sh") < 0 {
				t.Fatalf("enth-land-gate argv %q: want --gate=./build.sh", g)
			}
		})
	}
}

// oldStackReader is the self_land reader before mg-88ece: one id per LINE.
const oldStackReader = `stack=(); while read -r id; do [ -n "$id" ] && stack+=("$id"); done <<< "$ids"`

// stackReaderLine lifts the line of fn that turns $ids into the stack array.
func stackReaderLine(t *testing.T, fn string) string {
	t.Helper()
	var got []string
	for _, l := range strings.Split(fn, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "stack=();") {
			got = append(got, l)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one stack-reader line in self_land, got %q", got)
	}
	return got[0]
}

// countStack runs reader under shell with ids set to printed, the way
// self_land's ids=$(pogo presubmit --print-stack) sets it, and returns the
// stack's elements.
func countStack(t *testing.T, shell, reader, printed string) []string {
	t.Helper()
	script := "local_ids() { local ids id; local -a stack\n" +
		"ids=$(printf '%s\\n' \"$PRINTED\")\n" + reader + "\n" +
		`for id in "${stack[@]}"; do printf '[%s]\n' "$id"; done` + "\n}\nlocal_ids\n"
	c := exec.Command(shell, "-c", script)
	c.Env = append(os.Environ(), "PRINTED="+printed)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shell, err, out)
	}
	var els []string
	for _, l := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			els = append(els, l[1:len(l)-1])
		} else if l != "" {
			t.Fatalf("%s: unexpected output line %q\n%s", shell, l, out)
		}
	}
	return els
}

// A 2-Change stack, as --print-stack really prints it (ONE line, space-
// separated), becomes 2 stack elements under zsh and bash (mg-88ece). The
// positive control: the pre-fix reader, under the same harness, yields 1
// element "id1 id2" — the failure enth-land-gate refused as "not a Change id".
func TestSelfLandSplitsTheOneLinePrintStack(t *testing.T) {
	ids := []string{"7dc1aaaa", "7db1bbbb"}
	printed := enthvcs.PrintStackLine(ids)
	for _, shell := range selfLandShells(t) {
		t.Run(shell+"/control", func(t *testing.T) {
			if got := countStack(t, shell, oldStackReader, printed); len(got) != 1 || got[0] != printed {
				t.Fatalf("control: the per-line reader gave %q for %q, want the single joined element — the harness cannot see the bug", got, printed)
			}
		})
		for _, tmpl := range []string{"polecat.md", "polecat-architect.md"} {
			t.Run(shell+"/"+tmpl, func(t *testing.T) {
				reader := stackReaderLine(t, selfLandFn(t, tmpl, "mg-x", "/r"))
				for _, in := range []string{printed, "7dc1aaaa\n7db1bbbb", "  7dc1aaaa \t 7db1bbbb  "} {
					if got := countStack(t, shell, reader, in); strings.Join(got, ",") != strings.Join(ids, ",") {
						t.Fatalf("%s reader gave %q for %q, want %q", tmpl, got, in, ids)
					}
				}
				if got := countStack(t, shell, reader, enthvcs.PrintStackLine(ids[:1])); len(got) != 1 || got[0] != ids[0] {
					t.Fatalf("%s reader gave %q for a 1-Change stack", tmpl, got)
				}
			})
		}
	}
}
