package main

// The LIVE arm of the mg-21b1 control: the shipped prompt corpus, checked
// against the real accept-surfaces of the real `mg` and the `pogo` built from
// THIS revision.
//
// internal/promptcli's tests prove the rules work against a hand-built surface.
// They cannot prove the surface is real, and a check whose model of the tool
// has drifted from the tool is worse than none — it reports confidently and
// wrongly, which is precisely the failure mode this whole ticket is about.
// So this file does three things the hermetic tests cannot:
//
//  1. runs the check on the real corpus, and requires it clean;
//  2. re-runs the three pre-fix defects against the REAL surfaces, and requires
//     each one flagged — the corpus is the population the rules were written
//     against, so passing on it proves nothing on its own;
//  3. pins the handful of surface facts the hermetic fixtures assume, so the
//     stand-in cannot quietly stop describing the tool.
//
// pogoBin comes from main_test.go's TestMain: a binary built from the working
// tree, NOT the one on PATH. That is load-bearing. While this was being
// written the installed `pogo` had no `check-intake` and the source did, so a
// PATH-based check would have reported mayor.md's correct instruction as a
// defect.

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/promptcli"
)

// liveChecker discovers both surfaces from real binaries. It skips only the mg
// half, and only when mg is not installed.
func liveChecker(t *testing.T) (*promptcli.Checker, []string) {
	t.Helper()
	surfaces, missing, err := PromptCLISurfaces(context.Background(), pogoBin)
	if err != nil {
		t.Fatalf("discovering CLI surfaces: %v", err)
	}
	return &promptcli.Checker{Surfaces: surfaces, Omissions: PromptCLIOmissions()}, missing
}

// TestShippedPromptsMatchTheCLISurface is the gate. Every `mg …` / `pogo …`
// invocation in the shipped corpus must name a subcommand and flags the tool
// actually has.
//
// Its first run reported three defects, all of which shipped:
//
//	pm/pm-template.md:531  `mg spend --by item --tag=<tag>`   (mg-d8ea, filed)
//	pm/pm-template.md:535  `mg list --since 7d`               (found by this check)
//	crew/doctor.md:78      `pogo service start`               (found by this check)
//
// The ticket predicted "a fourth is a matter of time". There were two, and the
// check found them the first time it was pointed at the corpus.
func TestShippedPromptsMatchTheCLISurface(t *testing.T) {
	checker, missing := liveChecker(t)
	findings, files, err := promptcli.CheckFS(checker, agent.DefaultPromptsFS())
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if files == 0 {
		// An empty corpus passes every assertion below while checking nothing.
		t.Fatal("no prompt files were read; the check would pass vacuously")
	}
	for _, f := range findings {
		t.Errorf("%s\n    %s", f, strings.TrimSpace(f.Evidence))
	}
	if len(missing) > 0 {
		t.Logf("NOT CHECKED: %s not on PATH, so no `%s …` invocation was judged",
			strings.Join(missing, ", "), strings.Join(missing, "/"))
	}
	t.Logf("checked %d prompt files against %d command paths",
		files, len(checker.Surfaces["pogo"].Paths()))
}

// ---------------------------------------------------------------------------
// The check must be able to FAIL, on text that actually shipped
// ---------------------------------------------------------------------------

// TestPreFixDefectsAreFlaggedAgainstTheRealSurface reruns the three fixtures
// against the discovered surfaces rather than a hand-built one. If `mg` ever
// grows a `--tag` on `spend`, or the routing map gains a `task` entry, these
// stop failing — and that is correct: the defect would have stopped being one.
func TestPreFixDefectsAreFlaggedAgainstTheRealSurface(t *testing.T) {
	checker, missing := liveChecker(t)
	needsMG := map[string]bool{}
	for _, m := range missing {
		needsMG[m] = true
	}

	cases := []struct {
		name    string
		root    string
		body    string
		rule    string
		subject string
	}{
		{
			// mg-d8ea, pm/pm-template.md:500 as it shipped.
			name:    "mg-d8ea omit-the-real-flag",
			root:    "mg",
			body:    "```bash\nmg spend --by item   --tag=<your-tag> --since 7d --json\n```\n",
			rule:    promptcli.RuleUnknownFlag,
			subject: "mg spend --tag",
		},
		{
			// mg-4bb9, mayor.md:91 pre-6172fe3, verbatim in three prompts.
			name: "mg-4bb9 false absence of the append flag",
			root: "mg",
			body: "- **Update fields without claiming.** `mg edit <id> --body=\"<new body>\"` replaces " +
				"the body wholesale — there is no append/comment subcommand. To leave a note for a " +
				"future actor without rewriting the body, mail them.\n",
			rule:    promptcli.RuleFalseAbsence,
			subject: "mg edit append",
		},
		{
			// mg-159a, mayor.md:196 pre-c32a5d9. `task` is the DEFAULT type.
			name: "mg-159a omit --template for an unmapped type",
			root: "pogo",
			body: "| `type` | template |\n|---|---|\n| `design` | `--template=polecat-architect` |\n" +
				"| anything else (default `task`) | default {{.Worker}} — omit `--template` |\n",
			rule:    promptcli.RuleBadOmission,
			subject: "pogo agent spawn-polecat --template",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if needsMG[tc.root] {
				t.Skipf("%s is not on PATH", tc.root)
			}
			got := checker.CheckFile("prefix.md", tc.body)
			for _, f := range got {
				if f.Rule == tc.rule && strings.Contains(f.Subject, tc.subject) {
					return
				}
			}
			t.Fatalf("the real surface did not flag this shipped defect.\nwant %s on %q\ngot %v",
				tc.rule, tc.subject, got)
		})
	}
}

// TestCorrectedFormsAreCleanAgainstTheRealSurface is the other half: the text
// that replaced each defect, and a correction that quotes the claim it
// withdraws. An absence-predicate that scores a careful correction worse than
// the sloppy original teaches people to stop explaining their fixes.
func TestCorrectedFormsAreCleanAgainstTheRealSurface(t *testing.T) {
	checker, missing := liveChecker(t)
	if len(missing) > 0 {
		t.Skipf("%s not on PATH", strings.Join(missing, ", "))
	}
	cases := []struct{ name, body string }{
		{"mg-d8ea corrected", "```bash\nmg spend --by tag:<your-tag> --since 7d --json\n```\n"},
		{"mg-4bb9 corrected", "  **This bullet asserted the opposite until mg-4bb9.** It said `mg edit` had " +
			"\"no append/comment subcommand\" and sent you to mail instead. `mg edit --help` opened, and " +
			"still opens, with the banner **ADDING TO A BODY? USE `--append-body-file`, NOT `--body-file`**.\n"},
		{"mg-159a corrected", "| anything else — bare `task`, `scoping`, `audit`, `bug`, or no `type` at all | " +
			"**unmapped: no template is selected and the spawn is refused with a 409 naming the type.** " +
			"Pass `--template=polecat` explicitly. |\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checker.CheckFile("fixed.md", tc.body); len(got) != 0 {
				t.Errorf("expected no findings, got %v", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Anti-drift: the hermetic fixtures' model of the tools must stay true
// ---------------------------------------------------------------------------

// TestFixtureSurfaceFactsAreReal pins the specific facts internal/promptcli's
// hand-built surface asserts. Those tests are fast and hermetic precisely
// because they do not run a binary; this is the one place that checks their
// model against the tool.
func TestFixtureSurfaceFactsAreReal(t *testing.T) {
	checker, missing := liveChecker(t)
	skipMG := len(missing) > 0

	type fact struct {
		root    string
		path    []string
		flag    string
		present bool
		why     string
	}
	facts := []fact{
		{"mg", []string{"mg", "spend"}, "tag", false, "mg-d8ea: the defect is that --tag does not exist here"},
		{"mg", []string{"mg", "spend"}, "by", true, "the working form is --by tag:<tag>"},
		{"mg", []string{"mg", "edit"}, "append-body", true, "mg-4bb9: the flag the prompts denied"},
		{"mg", []string{"mg", "edit"}, "append-body-file", true, "mg-4bb9: the flag `mg edit --help` opens by recommending"},
		{"mg", []string{"mg", "list"}, "since", false, "found by this check: pm-template.md instructed it"},
		{"mg", []string{"mg", "show"}, "body-hash", true, "the prefix-sharing flag that must not answer a denial of --body"},
		{"mg", []string{"mg", "show"}, "body", false, "mg-9fc8: --body genuinely does not exist on mg show"},
		{"pogo", []string{"pogo", "agent", "spawn-polecat"}, "template", true, "mg-159a: the flag the table said to omit"},
	}
	for _, f := range facts {
		if f.root == "mg" && skipMG {
			t.Skip("mg is not on PATH")
		}
		node, ok := checker.Surfaces[f.root].Lookup(f.path)
		if !ok {
			t.Errorf("%q is missing from the discovered surface", strings.Join(f.path, " "))
			continue
		}
		if got := node.Flags[f.flag]; got != f.present {
			t.Errorf("%q --%s: present=%v, want %v (%s)",
				strings.Join(f.path, " "), f.flag, got, f.present, f.why)
		}
	}

	// A runnable group: mayor.md's `pogo schedule mayor --cron …` is correct
	// and must not read as a misspelled subcommand.
	if node, ok := checker.Surfaces["pogo"].Lookup([]string{"pogo", "schedule"}); !ok {
		t.Error("pogo schedule missing from the discovered surface")
	} else if !node.TakesArgs {
		t.Error("pogo schedule takes an <agent> positional; the fixture surface assumes so")
	}
	// A pure group: `pogo service start` must stay reportable.
	if node, ok := checker.Surfaces["pogo"].Lookup([]string{"pogo", "service"}); !ok {
		t.Error("pogo service missing from the discovered surface")
	} else if node.TakesArgs || node.Subs["start"] {
		t.Error("pogo service is a pure group with no `start`; the doctor.md fix depends on it")
	}
}

// TestCheckPromptsCommandRunsClean exercises the shipped command end to end,
// including its exit code, since that is what a caller scripts against.
func TestCheckPromptsCommandRunsClean(t *testing.T) {
	if _, err := exec.LookPath("mg"); err != nil {
		t.Skip("mg is not on PATH")
	}
	out, errOut, code := runPogo(t, nil, "check-prompts")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "clean:") {
		t.Errorf("expected a clean report, got:\n%s", out)
	}
}
