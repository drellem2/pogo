// These tests drive the real enth against a sandboxed enthd. They use pogo's
// internal enthdtest harness, so they are BSL-side and stay in pogo if the
// tool itself moves to enth's ci/ (mg-459d9). The tool imports nothing from
// pogo.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs/enthvcs/enthdtest"
)

const job = "ci/selfland/build"

// The gate: a tree that calls f must define f. That is the 2026-11-04 break
// in miniature. One Change removed a function in one file and the other
// called it in another, and enth merged both cleanly. Each run is counted
// in $GATE_RUNS, outside the tree.
const gate = `echo run >> "$GATE_RUNS"; if grep -q '^call f$' use.txt 2>/dev/null; then grep -q '^def f$' lib.txt; fi`

var tokenRE = regexp.MustCompile(`"token":"(enth_[0-9a-f]+)"`)

type fixture struct {
	s    *enthdtest.Server
	runs string
}

// start serves alice/proj with lib.txt defining f on the trunk, and swaps the
// test's token for one that may also attest job.
func start(t *testing.T) *fixture {
	t.Helper()
	s := enthdtest.Start(t)
	help, _ := exec.Command(s.Enth(), "land", "--help").CombinedOutput()
	if !bytes.Contains(help, []byte("--require")) {
		why := s.BinDir + "/enth land has no --require (enth #9 item L, mg-b729a)"
		if os.Getenv("POGO_ENTH_REQUIRE") == "1" {
			t.Fatal("POGO_ENTH_REQUIRE=1: " + why)
		}
		t.Skip(why)
	}
	m := tokenRE.FindStringSubmatch(s.Cmd(t, "enthd", "--root", s.Root, "token", "add", "alice", "--attest", job))
	if m == nil {
		t.Fatal("enthd token add --attest printed no token")
	}
	s.Token = m[1]
	t.Setenv("ENTH_TOKEN", s.Token)
	f := &fixture{s: s, runs: filepath.Join(t.TempDir(), "runs")}
	t.Setenv("GATE_RUNS", f.runs)
	id, _ := s.Work(t, "mg-seed", "", "seed: lib defines f", map[string]string{"lib.txt": "def f\n"})
	s.Land(t, id)
	return f
}

func (f *fixture) runCount() int {
	b, _ := os.ReadFile(f.runs)
	return strings.Count(string(b), "run")
}

// gate runs the tool in-process and returns its exit code and parsed line.
func (f *fixture) gate(t *testing.T, extra func(*options), changes ...string) (int, outcome, string) {
	t.Helper()
	var out, errb bytes.Buffer
	o := options{job: job, gate: gate, remote: f.s.URL, repo: f.s.Repo, enth: f.s.Enth(),
		timeout: time.Minute, maxMoves: 3, changes: changes, stdout: &out, stderr: &errb}
	if extra != nil {
		extra(&o)
	}
	code := run(context.Background(), o)
	var oc outcome
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, `"type":"gated"`) {
			json.Unmarshal([]byte(l), &oc)
		}
	}
	return code, oc, errb.String()
}

// land is the caller's step 4: enth land --require job. It returns enth's
// error code ("" when it landed).
func (f *fixture) land(t *testing.T, ids ...string) string {
	t.Helper()
	args := append(append(f.s.Remote(), "land", "--require", job), ids...)
	c := f.s.Command("enth", args...)
	var se bytes.Buffer
	c.Stderr = &se
	if err := c.Run(); err != nil {
		m := regexp.MustCompile(`"code":"([a-z_]+)"`).FindStringSubmatch(se.String())
		if m == nil {
			t.Fatalf("enth land failed with no code: %v %s", err, se.String())
		}
		return m[1]
	}
	return ""
}

func (f *fixture) attestations(t *testing.T, tree string) string {
	t.Helper()
	return f.s.Cmd(t, "enth", append(f.s.Remote(), "query", "attestations", "--tree", tree)...)
}

// The 10:05Z pair, replayed. A removes f, and B calls it. They touch
// different files, so enth merges them cleanly. Each is gated green on its
// own base, and A lands. B's land, bound to the tree it tested, is REFUSED.
// Gating B again tests the merged tree, which is red, and it is never landed.
func TestReplayIncompatiblePairIsRefused(t *testing.T) {
	f := start(t)
	s := f.s
	a, _ := s.Work(t, "mg-a", "", "A: drop f", map[string]string{"lib.txt": "def g\n"})
	b, _ := s.Work(t, "mg-b", "", "B: call f", map[string]string{"use.txt": "call f\n"})

	for _, c := range []string{a, b} {
		if code, oc, log := f.gate(t, nil, c); code != exitPass || oc.Result != "pass" || !oc.Ran {
			t.Fatalf("%s on its own base: code %d %+v\n%s", c[:8], code, oc, log)
		}
	}
	if code := f.land(t, a); code != "" {
		t.Fatalf("A's land: %s", code)
	}
	tipA := s.Tip(t)

	if code := f.land(t, b); code != "unattested_tree" {
		t.Fatalf("B's land after A: got %q, want unattested_tree", code)
	}
	if s.Tip(t) != tipA {
		t.Fatal("B's refused land moved the trunk")
	}
	// The caller's loop: back to step 1. The merged tree is gated, and it is red.
	code, oc, log := f.gate(t, nil, b)
	if code != exitFail || oc.Result != "fail" || !oc.Ran {
		t.Fatalf("B on the new trunk: code %d %+v\n%s", code, oc, log)
	}
	if !strings.Contains(f.attestations(t, oc.Tree), `"result":"fail"`) {
		t.Fatalf("the red merged tree carries no fail attestation:\n%s", f.attestations(t, oc.Tree))
	}
	if code := f.land(t, b); code != "unattested_tree" {
		t.Fatalf("B's land on a red tree: got %q, want unattested_tree", code)
	}
	// A fail on the tree is a verdict. A re-gate without --retest does not re-run it.
	n := f.runCount()
	if code, oc, _ := f.gate(t, nil, b); code != exitFail || !oc.Reused || f.runCount() != n {
		t.Fatalf("re-gate of a red tree: code %d %+v, runs %d→%d", code, oc, n, f.runCount())
	}
	if s.Tip(t) != tipA {
		t.Fatal("the trunk moved: B landed")
	}
}

// The positive control. The same sequence with a COMPATIBLE pair lands both:
// B is refused once (its tested tree is no longer what would land), re-gated
// on the merged tree, green, and lands.
func TestReplayCompatiblePairLandsBoth(t *testing.T) {
	f := start(t)
	s := f.s
	a, _ := s.Work(t, "mg-a", "", "A: add g", map[string]string{"lib2.txt": "def g\n"})
	b, _ := s.Work(t, "mg-b", "", "B: call f", map[string]string{"use.txt": "call f\n"})
	for _, c := range []string{a, b} {
		if code, _, log := f.gate(t, nil, c); code != exitPass {
			t.Fatalf("%s on its own base: %d\n%s", c[:8], code, log)
		}
	}
	if code := f.land(t, a); code != "" {
		t.Fatalf("A's land: %s", code)
	}
	if code := f.land(t, b); code != "unattested_tree" {
		t.Fatalf("B's land after A: got %q, want unattested_tree", code)
	}
	code, oc, log := f.gate(t, nil, b)
	if code != exitPass || !oc.Ran || oc.Reused {
		t.Fatalf("B re-gated on the merged tree: code %d %+v\n%s", code, oc, log)
	}
	if code := f.land(t, b); code != "" {
		t.Fatalf("B's land after re-gating: %s", code)
	}
	// What landed is the tree that was tested.
	out := s.Cmd(t, "enth", append(s.Remote(), "query", "cat", "use.txt")...)
	if !strings.Contains(out, "call f") {
		t.Fatalf("B is not on the trunk: %q", out)
	}
}

// Reuse. A Change whose base is the tip runs the gate once, not twice. A
// second gate on the same tree (a re-run after a lost answer, or another
// lander) finds the pass and runs nothing.
func TestBaseAtTipRunsTheGateOnce(t *testing.T) {
	f := start(t)
	c, _ := f.s.Work(t, "mg-c", "", "C", map[string]string{"use.txt": "call f\n"})
	code, first, log := f.gate(t, nil, c)
	if code != exitPass || !first.Ran || first.Reused {
		t.Fatalf("first gate: %d %+v\n%s", code, first, log)
	}
	if first.New != c {
		t.Fatalf("a Change on the tip lands as it is: new %s, want %s", first.New, c)
	}
	code, second, log := f.gate(t, nil, c)
	if code != exitPass || second.Ran || !second.Reused || second.Tree != first.Tree {
		t.Fatalf("second gate: %d %+v\n%s", code, second, log)
	}
	if n := f.runCount(); n != 1 {
		t.Fatalf("the gate ran %d times, want 1", n)
	}
	if code := f.land(t, c); code != "" {
		t.Fatalf("land: %s", code)
	}
}

// An infrastructure failure attests nothing: a timeout, a signal, or a host
// out of disk. The positive control, a plain exit 1, is attested --result
// fail, which proves the attestation reader would have seen one.
func TestInfrastructureFailureAttestsNothing(t *testing.T) {
	f := start(t)
	c, _ := f.s.Work(t, "mg-c", "", "C", map[string]string{"x.txt": "x\n"})
	cases := []struct {
		name string
		o    func(*options)
	}{
		{"timeout", func(o *options) { o.gate = "sleep 30"; o.timeout = 500 * time.Millisecond }},
		{"signal", func(o *options) { o.gate = "kill -9 $$" }},
		{"enospc", func(o *options) { o.gate = "echo 'write /tmp/x: no space left on device' >&2; exit 1" }},
		{"cannot-start", func(o *options) { o.gate = "true"; o.workdir = "/dev/null/nope" }},
	}
	var tree string
	for _, tc := range cases {
		start := time.Now()
		code, oc, log := f.gate(t, tc.o, c)
		if code != exitInfra {
			t.Fatalf("%s: code %d %+v, want %d\n%s", tc.name, code, oc, exitInfra, log)
		}
		if !strings.Contains(log, "nothing attested") {
			t.Fatalf("%s: does not say nothing was attested:\n%s", tc.name, log)
		}
		if tc.name == "timeout" && time.Since(start) > 20*time.Second {
			t.Fatalf("the timeout did not kill the gate (took %s)", time.Since(start))
		}
	}
	// Read T for the query.
	d, err := options{remote: f.s.URL, repo: f.s.Repo, enth: f.s.Enth()}.dryRun(context.Background(), []string{c})
	if err != nil {
		t.Fatal(err)
	}
	tree = d.Tree
	if got := strings.TrimSpace(f.attestations(t, tree)); got != "" {
		t.Fatalf("an infrastructure failure attested something:\n%s", got)
	}
	if code := f.land(t, c); code != "unattested_tree" {
		t.Fatalf("land after infra failures: %q, want unattested_tree", code)
	}
	// Positive control: a real red is attested, and the same reader sees it.
	code, oc, log := f.gate(t, func(o *options) { o.gate = "exit 1" }, c)
	if code != exitFail || oc.Tree != tree {
		t.Fatalf("control: %d %+v\n%s", code, oc, log)
	}
	if !strings.Contains(f.attestations(t, tree), `"result":"fail"`) {
		t.Fatal("control: the fail attestation is not visible to the reader")
	}
}

// The tree proof. A dry run's result checked out while the trunk is where
// the dry run read it is proven to be T. Once the trunk has moved, the same
// checkout is NOT T, and materialise says so instead of letting it be
// tested.
func TestMaterialiseProvesTheTree(t *testing.T) {
	f := start(t)
	s := f.s
	o := options{remote: s.URL, repo: s.Repo, enth: s.Enth()}
	ctx := context.Background()
	b, _ := s.Work(t, "mg-b", "", "B", map[string]string{"use.txt": "call f\n"})
	a, _ := s.Work(t, "mg-a", "", "A", map[string]string{"other.txt": "o\n"})
	s.Land(t, a) // B's base is no longer the tip: its dry run is a real hunk-merge.
	d, err := o.dryRun(ctx, []string{b})
	if err != nil {
		t.Fatal(err)
	}
	if d.New == b {
		t.Fatal("expected a rebased result")
	}
	moved, err := o.materialise(ctx, d, filepath.Join(t.TempDir(), "t1"))
	if err != nil || moved {
		t.Fatalf("materialising the dry run's result: moved=%v err=%v", moved, err)
	}
	z, _ := s.Work(t, "mg-z", "", "Z", map[string]string{"zed.txt": "z\n"})
	s.Land(t, z)
	moved, err = o.materialise(ctx, d, filepath.Join(t.TempDir(), "t2"))
	if err != nil || !moved {
		t.Fatalf("after the trunk moved: moved=%v err=%v, want moved", moved, err)
	}
}

// A dry run enth refuses (here, a conflict) is passed through as exit 3,
// carrying enth's code, and nothing is gated.
func TestRefusedDryRunPassesThrough(t *testing.T) {
	f := start(t)
	a, _ := f.s.Work(t, "mg-a", "", "A", map[string]string{"lib.txt": "def a\n"})
	b, _ := f.s.Work(t, "mg-b", "", "B", map[string]string{"lib.txt": "def b\n"})
	f.s.Land(t, a)
	code, _, log := f.gate(t, nil, b)
	if code != exitRefused || !strings.Contains(log, "conflict") {
		t.Fatalf("code %d, want %d with conflict\n%s", code, exitRefused, log)
	}
	if f.runCount() != 0 {
		t.Fatal("a refused dry run ran the gate")
	}
}

func TestParseFlags(t *testing.T) {
	if _, err := parseFlags([]string{"--gate=true", "c"}); err == nil {
		t.Fatal("no --job accepted")
	}
	if _, err := parseFlags([]string{"--job=j", "c"}); err == nil {
		t.Fatal("no --gate accepted")
	}
	if _, err := parseFlags([]string{"--job=j", "--gate=true"}); err == nil {
		t.Fatal("no Change accepted")
	}
	o, err := parseFlags([]string{"--job=j", "--gate=true", "--ref=refs/heads/x", "c1", "c2"})
	if err != nil || o.ref != "refs/heads/x" || len(o.changes) != 2 {
		t.Fatalf("%+v %v", o, err)
	}
}

// TEST-ONLY. A token without --attest still gets a real result. A pass exits
// 7 with attested=false, a red exits 1, and nothing is written either way.
func TestNoAttestTokenIsTestOnly(t *testing.T) {
	f := start(t)
	m := tokenRE.FindStringSubmatch(f.s.Cmd(t, "enthd", "--root", f.s.Root, "token", "add", "alice"))
	if m == nil {
		t.Fatal("no plain token")
	}
	t.Setenv("ENTH_TOKEN", m[1])
	green, _ := f.s.Work(t, "mg-g", "", "G", map[string]string{"use.txt": "call f\n"})
	code, oc, log := f.gate(t, nil, green)
	if code != exitTestOnly || oc.Result != "pass" || oc.Attested || !oc.Ran {
		t.Fatalf("green: code %d %+v\n%s", code, oc, log)
	}
	red, _ := f.s.Work(t, "mg-r", "", "R", map[string]string{"use.txt": "call f\n", "lib.txt": "def g\n"})
	code, oc2, log := f.gate(t, nil, red)
	if code != exitFail || oc2.Result != "fail" || oc2.Attested {
		t.Fatalf("red: code %d %+v\n%s", code, oc2, log)
	}
	for _, tr := range []string{oc.Tree, oc2.Tree} {
		if got := strings.TrimSpace(f.attestations(t, tr)); got != "" {
			t.Fatalf("a forbidden attest wrote something: %s", got)
		}
	}
}
