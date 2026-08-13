package agent

// The "numbers you did not measure" rule, pinned as a property of the shipped
// corpus (mg-8074).
//
// # What the rule is
//
// Drafted by pm-pogo as SME and approved verbatim by the prompt owner on
// 2026-08-05. It extends the correction discipline the corpus already carries:
// retracting a claim is not enough, because the FIGURES the claim carried
// travel separately and outlive it.
//
// # Why it earned a line — four numbers in one evening, everyone behaving well
//
// Every party labelled its hypotheses provisional, named tests, and retracted
// promptly. The figures still travelled, because provisional-ness attaches to
// claims and not to the data they cite.
//
//  1. doctor's "5" — the output of a grep with an invented pattern. doctor
//     retracted the CONCLUSION cleanly to both recipients twenty minutes later
//     and left the 5 standing. The real list holds 17 paths literally.
//  2. pm-pogo repeated the 5 to a peer PM and endorsed the generalisation drawn
//     from it. It reached a work item and a durable memory note, which had to be
//     deleted.
//  3. That peer PM filed two tickets on it — AFTER the retraction — by
//     reconciling the orphaned 5 against the real 17 with a plausible expansion
//     mechanism that does not exist.
//  4. "968 nudges / 0 acks" — relayed to the user and into a design brief
//     without re-derivation. The 0 was a literal carried from a different
//     window, not a measurement.
//
// The corrected figures are stronger than the wrong ones: 884 ack-capable
// deliveries produced zero acks across seven hours (10:23:00Z-17:26:00Z), then
// fifteen completions in the two minutes after. "968/0" was both wrong and
// weaker. Those numbers are recorded here rather than in the prompt because
// they are the incident, not the rule.
//
// # Why this is a corpus test and not just an edit
//
// The same reason as the mail-reconcile pin next door: the failure it addresses
// is invisible in the file. A prompt that has quietly lost a paragraph looks
// exactly like one that never had it, and nine copies of one line diverge one
// tidy-up at a time. So the assertions read the shipped text.
//
// Three things are asserted, and the third is the one worth explaining.
// The prompt owner was explicit that the mechanism sentence stays: "the first
// two sentences tell an agent what to do; the third tells it why the failure is
// invisible. A rule without its mechanism gets followed literally and then
// abandoned when it feels pedantic." So a reword that keeps the instructions
// and drops the explanation fails here on purpose — TestUnmeasuredNumbersRule
// KeepsItsThreeParts reads the shipped text, not the constant, so retuning the
// constant does not retune what the constant is allowed to say.
//
// # What is deliberately NOT asserted
//
// Placement. The rule sits beside each prompt's own correction guidance — the
// three-tier correction protocol in the PM template, the repair-report block in
// doctor's, the inter-agent communication section in the coordinator's, the
// Working Principles list in the worker templates — because it is an extension
// of "retract cleanly" and not a new obligation. That is an editorial judgement
// per file, and an assertion on it would break on the next honest re-section
// while catching nothing the presence check misses.

import (
	"bytes"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// unmeasuredNumbersRule is the approved line, verbatim. Every prompt in the
// corpus carries this exact text.
//
// It is pinned by bytes rather than by pattern because the nine copies are the
// failure mode: a paraphrase in one file is indistinguishable from the original
// when you are reading that file, and only a comparison across the corpus can
// see it. Reword it if the owner asks — in the constant, once, so all nine move
// together — and expect KeepsItsThreeParts to hold the reword to what the
// original bought.
const unmeasuredNumbersRule = "**Numbers you did not measure.** When you repeat a figure from another " +
	"agent, say whose it is and whether you re-derived it — an orphaned number " +
	"cannot be chased. When you retract or correct a claim, withdraw the figures " +
	"it carried BY NAME (\"the 5 was never measured — WATCHED holds 17\"), not just " +
	"the conclusion. A correction travels along the path of the claim; a bare " +
	"number travels further and quieter, because it reads as an observation, and " +
	"nobody re-derives an observation."

// corpusPrompts returns every markdown prompt the binary ships, keyed by path.
//
// The population is the WHOLE corpus rather than an enumeration of the nine
// files that existed on the day: the ticket's finding is that the failure
// occurred in both the worker and the crew populations, which between them are
// everything here. A prompt added later is held to the rule on the day it is
// added, which is the only version of this that does not rot.
func corpusPrompts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		b, err := fs.ReadFile(DefaultPromptsFS(), p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no markdown prompts found in the embedded corpus.\n" +
			"Either the embed moved or this walk stopped matching — either way " +
			"every assertion in this file is now vacuous, which is why the " +
			"emptiness is an error rather than a skip.")
	}
	return out
}

func TestEveryPromptCarriesTheUnmeasuredNumbersRule(t *testing.T) {
	var held []string
	for p, body := range corpusPrompts(t) {
		if !strings.Contains(body, unmeasuredNumbersRule) {
			t.Errorf("%s: does not carry the unmeasured-numbers rule verbatim.\n%s",
				p, indent("Approved verbatim by the prompt owner 2026-08-05 for the worker "+
					"templates AND the crew prompts, because the failure it addresses "+
					"happened in both populations on the day it was written. If this file "+
					"is new, add the line beside whatever correction guidance it already "+
					"carries. If the line was reworded, move the constant in this test — "+
					"once — so all copies move with it.\nwant:\n"+indent(unmeasuredNumbersRule)))
			continue
		}
		held = append(held, p)
	}
	t.Logf("held: %s", strings.Join(held, ", "))
}

// The three parts the line has to keep, asserted against the SHIPPED TEXT
// rather than against the constant above. A reword that satisfies the byte pin
// by moving the constant still has to satisfy this.
var unmeasuredNumbersParts = []struct {
	what string
	re   *regexp.Regexp
	why  string
}{
	{
		what: "attribution when you repeat someone else's figure",
		re:   regexp.MustCompile(`(?i)say whose it is and whether you re-derived it`),
		why: "A figure with no owner cannot be chased back to the command that " +
			"produced it. doctor's \"5\" was repeated by two agents and filed into " +
			"two tickets, and at no point in that chain did anyone hold enough to " +
			"re-run the grep that made it.",
	},
	{
		what: "withdrawal BY NAME, not just of the conclusion",
		re:   regexp.MustCompile(`withdraw the figures[^.]{0,80}BY NAME`),
		why: "This is the whole instruction. The prompt owner's own worked example: " +
			"the conclusion a nudge does not revive a wedged agent NEVER rested on " +
			"the 968 — it rests on an untreated control that woke anyway — so " +
			"retracting the claim as a unit would have thrown away a sound argument " +
			"along with an unverified number, and retracting nothing would have let " +
			"the number keep travelling. By name is the only option that gets both " +
			"right.",
	},
	{
		what: "the mechanism — why a bare number outruns its correction",
		re:   regexp.MustCompile(`(?i)a bare\s+number travels further and quieter`),
		why: "The owner was explicit that this sentence stays. The first two " +
			"sentences tell an agent what to do; this one tells it why the failure " +
			"is invisible — a correction travels along the path of the claim, while " +
			"a bare number reads as an observation and nobody re-derives an " +
			"observation. A rule without its mechanism gets followed literally and " +
			"then abandoned when it feels pedantic.",
	},
}

func TestUnmeasuredNumbersRuleKeepsItsThreeParts(t *testing.T) {
	for p, body := range corpusPrompts(t) {
		i := strings.Index(body, "**Numbers you did not measure.**")
		if i < 0 {
			// Absence is reported by the test above; reporting it twice
			// would double every failure line for one defect.
			continue
		}
		// Bound to the paragraph. An assertion satisfied by unrelated prose
		// elsewhere in a 1200-line prompt is a presence check wearing a
		// correctness label.
		para := body[i:]
		if j := strings.Index(para, "\n"); j != -1 {
			para = para[:j]
		}
		for _, part := range unmeasuredNumbersParts {
			if part.re.MatchString(para) {
				continue
			}
			t.Errorf("%s: the unmeasured-numbers rule no longer states %s (want /%s/).\n%s\nparagraph:\n%s",
				p, part.what, part.re, indent(part.why), indent(para))
		}
	}
}

// The rule has to survive RENDERING, not merely exist in the file. Worker
// templates are text/template, and the neighbouring bullets in the section it
// was added to are gated — `{{if .WorkerCores}}` on the core budget, `{{if eq
// .Provider "claude"}}` on the modal dismissal. A line that lands inside one of
// those conditionals is present in every grep and absent from the prompt an
// agent is actually handed, on exactly the dispatches where the gate is false.
func TestUnmeasuredNumbersRuleSurvivesTemplateRendering(t *testing.T) {
	shapes := map[string]TemplateVars{
		"preview": PreviewTemplateVars(),
		"no core budget, non-claude harness": {
			Id: "mg-0000", Repo: "/repo", WorktreeDir: "/wt", Provider: "codex",
		},
		"core budget, claude harness, integration branch": {
			Id: "mg-0000", Repo: "/repo", WorktreeDir: "/wt", Provider: "claude",
			Branch: "feature", WorkerCores: 3, HostCores: 10,
			RecentCommits: "abc1234 something", RecentFiles: "a.go",
		},
		"no worktree": {
			Id: "mg-0000", Repo: "/repo", WorktreeDir: "/home", NoWorktree: true,
		},
	}
	for p, body := range corpusPrompts(t) {
		if !strings.HasPrefix(p, "templates/") {
			continue // crew prompts are rendered statically, not through text/template
		}
		for name, vars := range shapes {
			tmpl, err := template.New(path.Base(p)).Parse(body)
			if err != nil {
				t.Errorf("%s: parsing as a template: %v", p, err)
				break
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, withDefaults(vars)); err != nil {
				t.Errorf("%s [%s]: executing: %v", p, name, err)
				continue
			}
			if strings.Contains(buf.String(), unmeasuredNumbersRule) {
				continue
			}
			t.Errorf("%s: the unmeasured-numbers rule is in the file but NOT in the prompt rendered for %s.\n%s",
				p, name, indent("It has been moved inside a conditional block. The line is "+
					"unconditional guidance: it applies to every dispatch, and a gate "+
					"that hides it hides it silently — the file still greps clean."))
		}
	}
}
