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
	"github.com/drellem2/pogo/internal/providers"
)

// liveChecker discovers both surfaces from real binaries. It skips only the mg
// half, and only when mg is not installed.
func liveChecker(t *testing.T) (*promptcli.Checker, []string) {
	t.Helper()
	surfaces, missing, err := PromptCLISurfaces(context.Background(), pogoBin)
	if err != nil {
		t.Fatalf("discovering CLI surfaces: %v", err)
	}
	return &promptcli.Checker{
		Surfaces:  surfaces,
		Omissions: PromptCLIOmissions(),
		Values:    PromptCLIValues(),
	}, missing
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
	rep, err := promptcli.CheckFS(checker, agent.DefaultPromptsFS())
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if rep.Files == 0 {
		// An empty corpus passes every assertion below while checking nothing.
		t.Fatal("no prompt files were read; the check would pass vacuously")
	}
	for _, f := range rep.Findings {
		t.Errorf("%s\n    %s", f, strings.TrimSpace(f.Evidence))
	}
	if len(missing) > 0 {
		t.Logf("NOT CHECKED: %s not on PATH, so no `%s …` invocation was judged",
			strings.Join(missing, ", "), strings.Join(missing, "/"))
	}
	t.Logf("checked %d prompt files against %d command paths",
		rep.Files, len(checker.Surfaces["pogo"].Paths()))
	// mg-9324's real output. Logged rather than asserted: the honest number is
	// whatever it is, and pinning it would make an unrelated prompt edit fail
	// this test. TestValueCoverageIsReportedAndMostlyEmpty is where the shape of
	// the census is actually asserted.
	t.Logf("flag values: %d checkable %v; %d NOT CHECKED %v",
		len(rep.Coverage.Checked), rep.Coverage.Checked,
		len(rep.Coverage.Unchecked), rep.Coverage.Unchecked)
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
		{
			// mg-9324's own example. Every token is a real flag; `closed` is
			// refused and `done` is the spelling. This is the case mg-21b1's
			// check passed by design.
			name:    "mg-9324 refused --status value",
			root:    "mg",
			body:    "```bash\nmg list --tag=<tag> --status=closed --json\n```\n",
			rule:    promptcli.RuleUnknownFlagValue,
			subject: "mg list --status",
		},
		{
			// pm/pm-template.md:422 as it shipped until mg-9324, and the worse
			// of the two the check found on its first run: the refusal sat
			// behind `2>/dev/null`, so the dedup guard silently never fired.
			name:    "mg-9324 --status=open behind a swallowed stderr",
			root:    "mg",
			body:    "```bash\nmg list --tag=release-cut --status=open 2>/dev/null | grep -q \"$slug\"\n```\n",
			rule:    promptcli.RuleUnknownFlagValue,
			subject: "mg list --status",
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
		{"mg-9324 corrected: the accepted spelling, and no --status at all",
			"```bash\nmg list --status=done --json\nmg list --repo=/tmp/x\n```\n"},
		{"mg-9324: a placeholder value asserts nothing",
			"```bash\nmg list --status=<status> --tag=$TAG\n```\n"},
		{
			// The reason PromptCLIValues resolves --provider through
			// providers.Resolve instead of its help text: `cursor` is accepted
			// and the help text omits it. Believing the help text would report
			// this correct line as a defect.
			"mg-9324: a provider the flag's own help text forgot to list",
			"```bash\npogo agent spawn-polecat cat-1 --template=polecat --provider=cursor\n```\n",
		},
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
// mg-9324: the coverage census, and the measurement behind it
// ---------------------------------------------------------------------------

// TestValueCoverageIsReportedAndMostlyEmpty asserts the ticket's actual finding
// rather than the feature: the legal values of a flag are NOT declared anywhere
// machine-readable, so the value arm covers a small minority of the values the
// corpus writes, and the report has to say which ones.
//
// It pins the SHAPE, not the numbers — the counts move with any prompt edit, and
// a test that fails when someone adds a `--tag=` to a prompt would be noise. The
// numbers go to the log, where they are the thing to read.
func TestValueCoverageIsReportedAndMostlyEmpty(t *testing.T) {
	checker, missing := liveChecker(t)
	if len(missing) > 0 {
		t.Skipf("%s not on PATH", strings.Join(missing, ", "))
	}
	rep, err := promptcli.CheckFS(checker, agent.DefaultPromptsFS())
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	cov := rep.Coverage
	t.Logf("mg-9324 census: %d of %d value-bearing flags in the corpus can be judged",
		len(cov.Checked), len(cov.Checked)+len(cov.Unchecked))
	t.Logf("  checkable: %v", cov.Checked)
	t.Logf("  NOT CHECKED (%d): %v", len(cov.Unchecked), cov.Unchecked)

	// The census must not be empty on either side. An empty Unchecked would mean
	// the check believes it judged every value it saw, which is false and is the
	// exact failure this ticket family keeps hitting. An empty Checked would mean
	// the value arm is inert and the positive control below is vacuous.
	if len(cov.Unchecked) == 0 {
		t.Error("no unchecked values reported: either the census broke or the check " +
			"is claiming a coverage it does not have")
	}
	if len(cov.Checked) == 0 {
		t.Error("no checkable values reported: the value arm is inert")
	}
	// The uncovered set is the majority, and saying so out loud is the finding.
	// If this ever flips, the class became closable and the doc comments in
	// promptcli.ParseFlagSpecs and PromptCLIValues are stale.
	if len(cov.Checked) > len(cov.Unchecked) {
		t.Errorf("coverage flipped to a majority (%d checked vs %d not): the "+
			"'values are not machine-readable' finding no longer holds, and the "+
			"comments asserting it need rewriting", len(cov.Checked), len(cov.Unchecked))
	}
	// Every entry in the registry must still resolve a legal set. A FromHelp rule
	// whose flag stopped declaring an enumeration goes quietly inert otherwise.
	for _, r := range PromptCLIValues() {
		if r.Accepts != nil {
			continue
		}
		node, ok := checker.Surfaces[r.Path[0]].Lookup(r.Path)
		if !ok {
			t.Errorf("%q is not in the discovered surface", strings.Join(r.Path, " "))
			continue
		}
		if len(node.FlagSpecs[r.Flag].Values) == 0 {
			t.Errorf("%s --%s: the rule reads its legal set from the help text, and the "+
				"help text no longer declares one — the rule is inert",
				strings.Join(r.Path, " "), r.Flag)
		}
	}
}

// TestProviderHelpTextIsNotAuthoritative pins the measurement that decided this
// ticket's design, against the real tool.
//
// `pogo agent spawn-polecat --provider` DECLARES `(claude, codex, pi)`. Its help
// text is hand-written prose, providers.Resolve is the code that refuses, and the
// two disagree: Resolve accepts `cursor`. A value-checker that trusted declared
// enumerations would be wrong on 1 of the 4 that exist in either tool, in the
// direction that reports correct prompts as defects — which is why PromptCLIValues
// routes this flag through Resolve and requires every entry to name its source.
//
// NOT FIXED HERE, and the reason is worth reading, because it is the same shape
// one level up. TestSpawnPolecat_ProviderHelpListsPi (main_test.go, gh #29)
// exists precisely to stop this help text going stale — its comment says it
// "must enumerate every registered provider" — and it enforces that with
// `strings.Contains(out, "(claude, codex, pi)")`. A literal. So when `cursor` was
// registered the guard kept passing, and today it would REJECT the corrected
// string `(claude, codex, pi, cursor)`, which does not contain that substring.
// The guard against the stale list is now what pins it. Fixing that is gh #29's
// business, not mg-9324's; it is noted here because it is the best available
// evidence that a hand-written enumeration cannot be trusted even when someone
// has already tried to protect it.
//
// If the help text is brought up to date, this test should be DELETED along with
// the divergence, not silenced: the design reason survives it (a hand-written
// list can drift again), but the worked example would no longer be live and
// claiming it was would be the same sin.
func TestProviderHelpTextIsNotAuthoritative(t *testing.T) {
	checker, _ := liveChecker(t)
	node, ok := checker.Surfaces["pogo"].Lookup([]string{"pogo", "agent", "spawn-polecat"})
	if !ok {
		t.Fatal("pogo agent spawn-polecat missing from the discovered surface")
	}
	declared := map[string]bool{}
	for _, v := range node.FlagSpecs["provider"].Values {
		declared[v] = true
	}
	if len(declared) == 0 {
		t.Fatalf("--provider declares no enumeration; usage = %q", node.FlagSpecs["provider"].Usage)
	}
	var undeclared []string
	for _, p := range providers.All() {
		if _, ok := providers.Resolve(p.ID); !ok {
			t.Errorf("providers.All lists %q but Resolve refuses it", p.ID)
		}
		if !declared[p.ID] {
			undeclared = append(undeclared, p.ID)
		}
	}
	if len(undeclared) == 0 {
		t.Skip("the help text now lists every provider Resolve accepts; the " +
			"divergence closed, and the worked example in PromptCLIValues " +
			"and promptcli.ParseFlagSpecs should be retired with it")
	}
	t.Logf("--provider declares %v but Resolve also accepts %v — a declared "+
		"enumeration is a claim, not a legal-value set",
		node.FlagSpecs["provider"].Values, undeclared)

	// And the consequence, stated as behaviour: a correct invocation using one of
	// the undeclared-but-accepted ids must not be flagged.
	for _, id := range undeclared {
		body := "```bash\npogo agent spawn-polecat cat-1 --provider=" + id + "\n```\n"
		if got := checker.CheckFile("provider.md", body); len(got) != 0 {
			t.Errorf("--provider=%s is accepted by Resolve but was flagged: %v", id, got)
		}
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
	// mg-9324: the hermetic fixture pastes `mg list --status`'s usage string in
	// verbatim. This is the one place that checks the paste still matches the
	// tool — if macguffin renames a status, the fixture's copy goes stale and the
	// value arm starts judging against a set the tool no longer has.
	if !skipMG {
		node, ok := checker.Surfaces["mg"].Lookup([]string{"mg", "list"})
		if !ok {
			t.Error("mg list missing from the discovered surface")
		} else {
			spec := node.FlagSpecs["status"]
			if !spec.TakesValue {
				t.Error("mg list --status takes a value; the whole value arm assumes so")
			}
			legal := map[string]bool{}
			for _, v := range spec.Values {
				legal[v] = true
			}
			if len(legal) == 0 {
				t.Errorf("mg list --status declares no enumeration; usage = %q", spec.Usage)
			}
			// `done` is the accepted spelling and the negative control.
			if !legal["done"] {
				t.Errorf("`done` is not among the declared statuses %v; the negative "+
					"control in internal/promptcli assumes it is", spec.Values)
			}
			// `closed` and `open` are the two refused spellings the corpus used.
			for _, bad := range []string{"closed", "open"} {
				if legal[bad] {
					t.Errorf("%q is now an accepted status: mg-9324's positive control "+
						"has stopped being a defect and the fixtures should be retired", bad)
				}
			}
		}
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
