package agent

// The PM sweep's "Is CI broken RIGHT NOW?" step must not judge the default
// branch on a listing that does not reach its tip (mg-62522).
//
// # The defect
//
// The step read `gh run list --branch "$def" --limit 5`. On 2026-09-29 that
// listing for drellem2/pogo ended on 2026-09-08 (abf1749) while main's head was
// 2c13a25 — whose run the UNFILTERED listing carried. The rows were well-formed
// and all `success`, so a red main that morning would have read as 09-08's
// green. The branch filter is GitHub's, server-side; why it stalls is not
// established and it is not constant (minutes later the same command listed the
// head), so the only defence is a positive control against the tip's sha.
//
// # Why this test runs the shipped block
//
// The fix is shell inside a prompt. A prose assertion would pass on a block
// that prints the right words and never compares a sha. So the block is cut out
// of pm-template.md and executed against a fake `gh` that serves a stale
// filtered listing, and the verdict line it prints is asserted — with a fresh
// listing as the positive control that the fake and the extraction work at all.

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ciTip   = "2c13a2545681c9e1d8434541c8802d163d595ee6"
	ciOld   = "abf1749000000000000000000000000000000000"
	ciOther = "feedbee000000000000000000000000000000000"
)

// ciCheckBlock returns the shell between the "Is CI broken RIGHT NOW?" comment
// and the SECONDARY failure-history listing, de-indented from the per-repo loop.
func ciCheckBlock(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(DefaultPromptsFS(), "pm/pm-template.md")
	if err != nil {
		t.Fatalf("reading pm/pm-template.md: %v", err)
	}
	s := string(b)
	start := strings.Index(s, "# Is CI broken RIGHT NOW?")
	end := strings.Index(s, "# SECONDARY, and a DIFFERENT QUESTION")
	if start < 0 || end < start {
		t.Fatalf("pm-template.md no longer has the CI step between %q and %q; this test would assert nothing (mg-62522)",
			"# Is CI broken RIGHT NOW?", "# SECONDARY, and a DIFFERENT QUESTION")
	}
	var out []string
	for _, ln := range strings.Split(s[start:end], "\n") {
		out = append(out, strings.TrimPrefix(ln, "  "))
	}
	return strings.Join(out, "\n")
}

const fakeGH = `#!/bin/bash
case "$1 $2" in
  "repo view") echo "$FAKE_DEF" ;;
  "api "*)
    case "$2" in
      */commits/*) [ -n "$FAKE_TIP" ] || { echo "HTTP 502" >&2; exit 1; }; echo "$FAKE_TIP" ;;
      */compare/*) echo "$FAKE_AHEAD" ;;
    esac ;;
  "run list")
    for a in "$@"; do [ "$a" = --branch ] && { cat "$FAKE_DIR/branch.json"; exit 0; }; done
    cat "$FAKE_DIR/all.json" ;;
  *) echo "fake gh: unexpected $*" >&2; exit 2 ;;
esac
`

func ciRun(sha, branch, conclusion, at string) string {
	return `{"status":"completed","conclusion":"` + conclusion + `","workflowName":"CI","createdAt":"` + at +
		`","headSha":"` + sha + `","headBranch":"` + branch + `","event":"push"}`
}

func runCICheck(t *testing.T, shell, tip, branchJSON, allJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"branch.json": branchJSON, "all.json": allJSON} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(shell, "-c", "slug=drellem2/pogo\n"+ciCheckBlock(t))
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_DIR="+dir, "FAKE_DEF=main", "FAKE_TIP="+tip, "FAKE_AHEAD=3")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: running the CI step: %v\n%s", shell, err, out)
	}
	if strings.Contains(string(out), "fake gh: unexpected") {
		t.Fatalf("%s: the CI step called gh in a way the fake does not serve:\n%s", shell, out)
	}
	return string(out)
}

func TestPMCIStepHasAPositiveControlAgainstTheBranchTip(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skipf("no jq: %v", err)
	}
	stale := "[" + ciRun(ciOld, "main", "success", "2026-09-08T12:46:57Z") + "]"
	fresh := "[" + ciRun(ciTip, "main", "success", "2026-09-29T05:31:54Z") + "," +
		ciRun(ciOld, "main", "success", "2026-09-08T12:46:57Z") + "]"
	// The unfiltered listing: the tip's run is RED, interleaved with another
	// branch's run, which the client-side filter must drop.
	allWithRedTip := "[" + ciRun(ciOther, "polecat-x", "success", "2026-09-29T06:00:00Z") + "," +
		ciRun(ciTip, "main", "failure", "2026-09-29T05:31:54Z") + "," +
		ciRun(ciOld, "main", "success", "2026-09-08T12:46:57Z") + "]"
	allWithoutTip := "[" + ciRun(ciOther, "polecat-x", "success", "2026-09-29T06:00:00Z") + "," +
		ciRun(ciOld, "main", "success", "2026-09-08T12:46:57Z") + "]"

	cases := []struct {
		name, tip, branch, all string
		want, notWant          []string
	}{
		{
			// Positive control: the fake, the extraction and the sha comparison
			// all work, so the negatives below mean something.
			name: "fresh filter reaches tip", tip: ciTip, branch: fresh, all: allWithRedTip,
			want:    []string{"CI reaches tip — drellem2/pogo main@2c13a25"},
			notWant: []string{"STALE", "NOT COVERED", "UNKNOWN"},
		},
		{
			// The 2026-09-29 shape: --branch stops at 09-08, main is red.
			name: "stale filter falls back to the unfiltered listing", tip: ciTip, branch: stale, all: allWithRedTip,
			want:    []string{"CI STALE FILTER", "did not list the tip 2c13a25", `"conclusion": "failure"`, "CI reaches tip"},
			notWant: []string{"polecat-x"},
		},
		{
			name: "tip has no run anywhere", tip: ciTip, branch: stale, all: allWithoutTip,
			want:    []string{"CI TIP NOT COVERED", "newest listed run is on abf1749, 3 commit(s) behind", "UNKNOWN, not green"},
			notWant: []string{"CI reaches tip"},
		},
		{
			name: "empty listing", tip: ciTip, branch: "[]", all: "[]",
			want:    []string{"CI TIP NOT COVERED", "the listing is empty"},
			notWant: []string{"CI reaches tip"},
		},
		{
			name: "tip unreadable", tip: "", branch: stale, all: allWithRedTip,
			want:    []string{"CI TIP UNKNOWN", "UNKNOWN, not green"},
			notWant: []string{"CI reaches tip", "STALE FILTER"},
		},
	}

	// PMs run zsh; the fleet's other shells are bash. Both must agree.
	var shells []string
	for _, sh := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(sh); err == nil {
			shells = append(shells, sh)
		}
	}
	if len(shells) == 0 {
		t.Skip("neither bash nor zsh on PATH")
	}
	for _, sh := range shells {
		for _, c := range cases {
			t.Run(sh+"/"+c.name, func(t *testing.T) {
				out := runCICheck(t, sh, c.tip, c.branch, c.all)
				for _, w := range c.want {
					if !strings.Contains(out, w) {
						t.Errorf("output lacks %q.\n%s\noutput:\n%s", w, indent(
							"A --branch listing that does not reach the tip must not be judged as the "+
								"state of the branch; the step compares against the tip's sha (mg-62522)."), indent(out))
					}
				}
				for _, nw := range c.notWant {
					if strings.Contains(out, nw) {
						t.Errorf("output unexpectedly contains %q.\noutput:\n%s", nw, indent(out))
					}
				}
			})
		}
	}
}
