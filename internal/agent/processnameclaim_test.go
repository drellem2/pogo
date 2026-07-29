package agent

// A zero-tolerance ratchet on the "your process name is `pogo-crew-<name>`"
// claim in shipped prompts and in the Go-embedded minimal polecat template
// (mg-96ad, closing mg-710c's re-homed prompt half).
//
// WHY A TEST AND NOT A SWEEP. mg-710c measured the population at three sites,
// pm-pogo at five, an unbounded grep at thirteen, and mg-ccd1's sweep of the
// docs half then found two more nobody had counted. Four measurements, four
// different numbers, every one of them a fact about the search rather than
// about the corpus. A sweep fixes a snapshot; the claim kept coming back
// because every new prompt is written by copying an old one, and the old one
// said it. So the deliverable is the predicate, not the count.
//
// WHY THE CLAIM MATTERS. ProcessName() returns a DISPLAY label. Nothing sets
// it on any process: agents are spawned as their harness command (claude,
// codex, a test's fake-agent), and a harness that exec's replaces even that
// argv. `pgrep -f pogo-crew-mayor` against a live, healthy mayor matches
// nothing (measured, mg-710c). The failure is not that the lookup is
// unavailable — it is that the lookup SUCCEEDS AND RETURNS EMPTY, and empty
// reads as "the agent is gone" (mg-de08). A prompt that teaches this teaches
// an agent to conclude a healthy fleet is dead.
//
// WHAT COUNTS AS A VIOLATION. Only the assertion "process name" applied to a
// pogo-crew-/pogo-cat- label. Prose that names the label as a display label,
// or that explicitly denies it is a process name, must pass — that prose is
// the fix, and a predicate that punished it would push authors back toward
// saying nothing at all. So the check is: on a line mentioning `pogo-crew-` or
// `pogo-cat-`, the phrase "process name" is a violation UNLESS the same line
// also carries a denial ("not a process name") or names it a display label.
//
// ZERO IS THE INVENTORY. Unlike bodyRatchet there is no grandfathered set:
// every site was corrected in the same change that added this test, so the
// allowed count is zero everywhere and there is no number to raise.

import (
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// labelRE matches the display-label tokens the claim attaches to.
var labelRE = regexp.MustCompile(`pogo-(crew|cat)-`)

// The remedy, carried by every failure message so an author who trips this
// does not have to go looking for what to say instead.
const displayLabelIdiom = "call it a DISPLAY LABEL and say what it is not:\n" +
	"    Your display label is `pogo-crew-<name>` — what `pogo agent list` shows\n" +
	"    and what `/agents` returns as `process_name`. It is NOT a process name:\n" +
	"    nothing sets it on any process, so `pgrep -f pogo-crew-<name>` matches\n" +
	"    nothing even while you are healthy (mg-710c). Ask pogod for an agent's pid.\n" +
	"  There is no grandfathered inventory here — the allowed count is zero."

// exonerating phrases. Any one of them on the same line means the line is
// talking ABOUT the claim rather than making it.
var exonerations = []string{
	"not a process name",
	"not process names",
	"display label",
	"process_name", // the JSON field, which really is named that
	"POGO_PROCESS_NAME",
}

type claimViolation struct {
	Line int
	Text string
}

// scanProcessNameClaims reports lines that assert a pogo-crew-/pogo-cat- label
// IS a process name.
func scanProcessNameClaims(src string) []claimViolation {
	var out []claimViolation
	for i, line := range strings.Split(src, "\n") {
		if !labelRE.MatchString(line) {
			continue
		}
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "process name") {
			continue
		}
		exonerated := false
		for _, e := range exonerations {
			if strings.Contains(lower, strings.ToLower(e)) {
				exonerated = true
				break
			}
		}
		if !exonerated {
			out = append(out, claimViolation{Line: i + 1, Text: strings.TrimSpace(line)})
		}
	}
	return out
}

// checkProcessNameClaims walks a prompt tree and returns one problem string per
// surviving claim.
func checkProcessNameClaims(root fs.FS) ([]string, error) {
	var problems []string
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		for _, v := range scanProcessNameClaims(string(data)) {
			problems = append(problems, fmt.Sprintf(
				"%s:%d asserts a display label is a process name — %q\n  %s",
				path, v.Line, v.Text, displayLabelIdiom))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(problems)
	return problems, nil
}

// TestProcessNameClaim_ShippedPromptsHoldTheLine is the standing guard over the
// prompt tree every agent actually reads.
func TestProcessNameClaim_ShippedPromptsHoldTheLine(t *testing.T) {
	problems, err := checkProcessNameClaims(os.DirFS("prompts"))
	if err != nil {
		t.Fatalf("walking prompts: %v", err)
	}
	for _, p := range problems {
		t.Errorf("%s", p)
	}
}

// TestProcessNameClaim_MinimalPolecatTemplate covers the one shipped prompt
// that is not a file: prompt.go's Go string literal, rendered by `pogo init
// --minimal`. It was the site the original three-site enumeration missed, and
// a walk of prompts/ cannot see it.
func TestProcessNameClaim_MinimalPolecatTemplate(t *testing.T) {
	for _, v := range scanProcessNameClaims(minimalPolecatTemplate) {
		t.Errorf("the minimal polecat template (internal/agent/prompt.go) asserts a display "+
			"label is a process name at its line %d — %q\n  %s", v.Line, v.Text, displayLabelIdiom)
	}
}

// TestProcessNameClaim_PredicateCanFire is the refutation control. A ratchet
// that has only ever been observed passing is not evidence — mg-710c was filed
// against an assertion whose instrument returned empty unconditionally, and the
// reliable way to ship that bug again is to write a check nobody made go red.
//
// It also pins the exoneration boundary in both directions: the corrected
// sentence must pass, or the check would punish the very prose that fixes this.
func TestProcessNameClaim_PredicateCanFire(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantHit bool
	}{
		{
			name:    "the claim as it shipped",
			src:     "Your agent name is `doctor`. Your process name is `pogo-crew-doctor`.",
			wantHit: true,
		},
		{
			name:    "the polecat phrasing",
			src:     "Your process name follows the pattern `pogo-cat-<name>`.",
			wantHit: true,
		},
		{
			name:    "the correction must NOT fire",
			src:     "Your display label is `pogo-crew-doctor`. It is not a process name.",
			wantHit: false,
		},
		{
			name:    "naming the JSON field must NOT fire",
			src:     "`/agents` returns `pogo-cat-a3f` as the `process_name` field.",
			wantHit: false,
		},
		{
			name:    "an unrelated mention of the label must NOT fire",
			src:     "Crew agents are labelled `pogo-crew-<name>`; polecats `pogo-cat-<id>`.",
			wantHit: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(scanProcessNameClaims(tc.src)) > 0
			if got != tc.wantHit {
				t.Errorf("scanProcessNameClaims(%q) fired=%v, want %v", tc.src, got, tc.wantHit)
			}
		})
	}
}

// TestProcessNameClaim_FiresOnTheRealCorpus is the second half of the control:
// the predicate must fire on the shipped tree when the claim is reintroduced
// there, not merely on a hand-built string. Same reasoning as
// copyPromptTree's: a guard shown to work only on a toy fixture has not been
// shown to guard the thing it names.
func TestProcessNameClaim_FiresOnTheRealCorpus(t *testing.T) {
	dir := copyPromptTree(t)

	target := dir + "/crew/doctor.md"
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	poisoned := string(data) + "\nYour process name is `pogo-crew-doctor`.\n"
	if err := os.WriteFile(target, []byte(poisoned), 0644); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}

	problems, err := checkProcessNameClaims(os.DirFS(dir))
	if err != nil {
		t.Fatalf("walking poisoned tree: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("poisoned corpus produced %d problems, want exactly 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "crew/doctor.md") {
		t.Errorf("problem does not name the poisoned file: %s", problems[0])
	}
}
