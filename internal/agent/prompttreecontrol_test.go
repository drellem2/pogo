package agent

// Two rules pinned as properties of the shipped corpus (mg-1763): "ask which
// TREE you are in", and "a negative result needs a positive control".
//
// They ship together because they were decided together, and they are pinned
// here for the same reason the unmeasured-numbers rule next door is: a prompt
// that has quietly lost a paragraph looks exactly like one that never had it,
// and nine copies of one line diverge one tidy-up at a time. So the assertions
// read the shipped text.
//
// # Rule 1 — which TREE, not which command
//
// A broad stage (`git add -A`, `git add .`, `git commit -a`) is a hazard in a
// tree something ELSE writes to, and harmless in one only you write to. Two
// careful agents hit it within an hour on 2026-08-20 in `~/.pogo`: architect's
// 21802d2 swept 22 lines of `agents/mayor.md` into a commit whose subject was
// entirely about its own `deploy-verify.md` (re-derived here from that repo's
// history: `git show --stat 21802d2` shows the two paths), and a worker did the
// same thing on a dispatch fence. Neither was carelessness; the vector is the
// sweep, not the author.
//
// The phrasing is the load-bearing part, and it is why this test asserts the
// GENERALISATION and not the command. A line reading "never `git add -A`" would
// be actively wrong: the corpus repo's own standing policy is `git add -A &&
// git commit`, and that is CORRECT there because nothing but the agent writes
// to it. A blanket command prohibition contradicts a live and correct
// instruction elsewhere and — in the architect's words — "gets discarded on
// contact": the reader meets the counterexample, concludes the rule is wrong,
// and drops it whole. So the generalisation is not a stylistic preference over
// the prohibition. It is the version that survives meeting its own
// counterexample, and TestTreeAndControlRulesKeepTheirParts is what stops a
// later editor from "tightening" it back into the version that does not.
//
// The `~/.pogo` scope is measured rather than quoted: at the time this was
// written, 8 of that repo's 20 tracked files were dirty from writers other than
// any agent standing in it — the deployed prompts and `projects.json`. An
// earlier draft of the ticket said "9 tracked files"; that figure was the
// architect's and is not repeated here, because the line does not need it.
//
// # Rule 2 — a negative needs a positive control
//
// Four false zeros in ~90 minutes across three agents on 2026-08-20. They are
// NOT one class, and the split is what the rule is built on:
//
//	agent      construction                     fails at        silenceable?
//	mayor      `<sha>:<path>` unquoted, piped   COMMAND (git)   YES — SILENT
//	architect  `<sha>:<path>` unquoted          COMMAND (git)   YES — SILENT
//	architect  `^` parent-suffix unquoted       SHELL (zsh)     no  — LOUD
//	pm-pogo    `--include=*.go` unquoted        SHELL (zsh)     no  — LOUD
//
// Shell-level glob failures announce themselves before anything runs, and
// `2>/dev/null` on the command cannot hide them — they were never the hazard.
// The hazard is a command that RUNS and fails with its stderr suppressed or its
// status swallowed by a pipe, which is the first two rows, and those two are
// the SAME construction rather than two independent witnesses. That is a
// narrower base than "four instances", and it is stated that way on purpose:
// the ticket must not claim four witnesses for a hazard that has two. One of
// the two nearly shipped as "architect's finding independently verified", on
// two commands that never opened a file.
//
// A fifth instance generalises it in the other direction. `git symbolic-ref`
// returns empty for a detached HEAD AND for a directory that is not a worktree
// at all, which labelled 19 orphan directories DETACHED (pm-pogo's count,
// self-caught before filing, not re-derived here). That one fails toward the
// ALARMING reading, so a rule aimed only at reassuring zeros would miss it.
// Hence the general clause: when an instrument would return the same answer
// under two different world-states, it is not evidence about either until a
// control distinguishes them.
//
// # What is deliberately NOT asserted, and it matters
//
// The quoting advice — `"${sha}:${path}"`, quote `^` and `~`, `<<'EOF'`,
// single-quoted `--body` — is carried in the line, so the byte pin covers it
// like everything else; but no entry in treeAndControlParts asks for it. An
// editor who cuts it moves the constant once and the KeepsTheirParts assertions
// still hold. That asymmetry is deliberate and inverts how the two halves look
// on the page:
//
//	the quoting advice  fixes the LOUD instances, which announce themselves anyway
//	the control rule    catches the SILENT instances, which do not
//
// The quoting advice is concrete, mechanical and obviously actionable; the
// control rule is a habit and reads as advice. An editor compressing this would
// naturally keep the concrete half and cut the vague one — and would thereby
// delete the only remedy that addresses the hazard. If anything here is cut,
// cut the quoting advice: it rots on the next metacharacter, and the caret
// instance is already a case the colon-quoting fix would not have caught.
//
// Placement is not asserted either, for the same reason as next door: both
// lines sit beside the corpus's existing evidence-hygiene guidance, which is an
// editorial judgement per file.
//
// # What this does NOT close
//
// Neither line is a control. Nothing REFUSES a broad stage in `~/.pogo` and
// nothing refuses an uncontrolled negative; both are things a reader MAY parse,
// and the standing evidence (mg-dafb) is that written rules repeatedly failed
// to fire for the agents who wrote them. Closing the first would take a
// pre-commit hook in that repo, which would also refuse legitimate multi-path
// commits — a real decision nobody has taken. Recorded, not proposed.

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// whichTreeRule and positiveControlRule are the approved lines, verbatim. Every
// prompt in the corpus carries both.
//
// They are pinned by bytes rather than by pattern for the reason the
// unmeasured-numbers pin gives: a paraphrase in one file is indistinguishable
// from the original when you are reading that file, and only a comparison
// across the corpus can see it. Reword in the constant, once, so all copies
// move together — and expect the KeepsItsParts tests to hold the reword to what
// the original bought.
const whichTreeRule = "**Ask which TREE you are in, not which command you are running.** A " +
	"broad stage — `git add -A`, `git add .`, `git commit -a` — is a hazard " +
	"only in a tree something ELSE writes to. `~/.pogo` is such a tree: the " +
	"nightly deploy rewrites the prompts there and pogod rewrites " +
	"`projects.json`, so a sweep commits someone else's work under your " +
	"subject line — stage by path there. It is deliberately NOT phrased as " +
	"\"never `git add -A`\": the corpus repo's standing policy IS `git add -A " +
	"&& git commit`, and that is correct there because nothing but the agent " +
	"writes to it. A blanket command prohibition meets its own counterexample " +
	"and gets discarded on contact, taking the real hazard with it."

const positiveControlRule = "**A NEGATIVE result needs a POSITIVE CONTROL.** When a check comes back " +
	"negative — zero matches, an empty string, nothing found — run the same " +
	"instrument against a case you KNOW is positive, and report both. If the " +
	"control does not fire, the instrument is broken and the negative says " +
	"nothing. The construction that bites is a command that RUNS and fails " +
	"with its stderr suppressed or its status swallowed by a pipe: `git show " +
	"\"$sha:$path\" 2>/dev/null | grep -c X` prints `0` for a mangled revspec " +
	"exactly as it does for a real absence, and neither exit status separates " +
	"the two — shell-level glob failures abort loudly and were never the " +
	"hazard. Generally: when an instrument would return the same answer under " +
	"two different world-states, it is not evidence about either until a " +
	"control distinguishes them — `git symbolic-ref` is empty for a detached " +
	"HEAD AND for a directory that is not a worktree at all. Subordinate to " +
	"that, and the first thing to cut if anything here is cut: quote revspecs " +
	"and shas as `\"${sha}:${path}\"`, quote anything carrying `^` or `~`, use " +
	"`<<'EOF'` for heredocs, and single-quote `--body` arguments containing " +
	"backticks."

// corpusRules is the population the presence tests walk: each rule with the
// heading an editor would grep for and the guidance a failure should print.
var corpusRules = []struct {
	name    string
	heading string
	text    string
	adding  string
}{
	{
		name:    "which-TREE",
		heading: "**Ask which TREE you are in",
		text:    whichTreeRule,
		adding: "If this prompt is new, add the line beside whatever evidence-hygiene " +
			"guidance it already carries. Do NOT rewrite it as a prohibition on " +
			"`git add -A`: that phrasing is wrong in the corpus repo, whose standing " +
			"policy is exactly that command, and a rule that contradicts a live and " +
			"correct instruction elsewhere gets discarded on contact.",
	},
	{
		name:    "positive-control",
		heading: "**A NEGATIVE result needs a POSITIVE CONTROL.**",
		text:    positiveControlRule,
		adding: "If this prompt is new, add the line beside whatever evidence-hygiene " +
			"guidance it already carries. It is the half of mg-1763 that carries the " +
			"justification — it catches the two SILENT instances; the quoting advice " +
			"inside it only catches the loud ones.",
	},
}

func TestEveryPromptCarriesTheTreeAndControlRules(t *testing.T) {
	prompts := corpusPrompts(t)
	for _, rule := range corpusRules {
		var held []string
		for p, body := range prompts {
			if !strings.Contains(body, rule.text) {
				t.Errorf("%s: does not carry the %s rule verbatim.\n%s",
					p, rule.name, indent(rule.adding+"\nIf the line was reworded, move the "+
						"constant in this test — once — so all copies move with it.\nwant:\n"+
						indent(rule.text)))
				continue
			}
			held = append(held, p)
		}
		t.Logf("%s held: %s", rule.name, strings.Join(held, ", "))
	}
}

// The parts each line has to keep, asserted against the SHIPPED TEXT rather
// than against the constants above. A reword that satisfies the byte pin by
// moving the constant still has to satisfy this.
var treeAndControlParts = []struct {
	rule    string
	heading string
	what    string
	re      *regexp.Regexp
	why     string
}{
	{
		rule:    "which-TREE",
		heading: "**Ask which TREE you are in",
		what:    "the generalisation — the tree, not the command",
		re:      regexp.MustCompile(`hazard only in a tree something ELSE writes to`),
		why: "This IS the rule. A reader who takes away \"avoid these three commands\" " +
			"has the wrong invariant: the same sweep is correct in a tree only they " +
			"write to, and dangerous in one they share.",
	},
	{
		rule:    "which-TREE",
		heading: "**Ask which TREE you are in",
		what:    "the scoped, actionable instance",
		re:      regexp.MustCompile(`stage by path`),
		why: "A generalisation with no instance is unactionable. `~/.pogo` is the tree " +
			"the fleet actually stands in while something else rewrites it, and " +
			"staging by path is the whole remedy.",
	},
	{
		rule:    "which-TREE",
		heading: "**Ask which TREE you are in",
		what:    "why the obvious phrasing is refused, with its counterexample",
		re:      regexp.MustCompile(`(?s)deliberately NOT phrased as.{0,400}?discarded on contact`),
		why: "Without this, the next editor tightens the rule into \"never `git add " +
			"-A`\" — which is shorter, more concrete, and wrong. The corpus repo's " +
			"standing policy is that exact command and is correct there. A rule that " +
			"contradicts a live and correct instruction elsewhere is dropped WHOLE by " +
			"the reader who meets the counterexample, so naming the counterexample " +
			"inside the rule is what makes it survive.",
	},
	{
		rule:    "positive-control",
		heading: "**A NEGATIVE result needs a POSITIVE CONTROL.**",
		what:    "the instruction — run a known-positive case and report both",
		re:      regexp.MustCompile(`(?s)a case you KNOW is positive, and report both`),
		why: "Reporting only the negative is what every one of the instances did. The " +
			"control has to be REPORTED, not merely run, or a reader of the finding " +
			"cannot tell a controlled zero from an uncontrolled one.",
	},
	{
		rule:    "positive-control",
		heading: "**A NEGATIVE result needs a POSITIVE CONTROL.**",
		what:    "what a dead control means",
		re:      regexp.MustCompile(`(?s)control does not fire, the instrument is broken`),
		why: "The consequence is the point: an uncontrolled negative is not weak " +
			"evidence, it is no evidence. One of the silent instances nearly shipped " +
			"as \"independently verified\" on two commands that never opened a file.",
	},
	{
		rule:    "positive-control",
		heading: "**A NEGATIVE result needs a POSITIVE CONTROL.**",
		what:    "the construction that actually bites — silent, not loud",
		re:      regexp.MustCompile(`(?s)stderr suppressed or its status swallowed by a pipe`),
		why: "The rule cannot be \"you will see an error\", because in the construction " +
			"that bites there is nothing to see. Shell-level glob failures abort " +
			"loudly before anything runs and `2>/dev/null` cannot hide them; a " +
			"command that RUNS and fails with its stderr redirected produces a zero " +
			"indistinguishable from a real absence, at an exit status that does not " +
			"separate them either.",
	},
	{
		rule:    "positive-control",
		heading: "**A NEGATIVE result needs a POSITIVE CONTROL.**",
		what:    "the generalisation — two world-states, one signal",
		re:      regexp.MustCompile(`(?s)same answer under two different world-states`),
		why: "\"Watch out for false negatives\" is the wrong generalisation and leaves " +
			"the fifth instance uncovered: `git symbolic-ref` is empty for a detached " +
			"HEAD AND for a directory that is not a worktree, which fails toward the " +
			"ALARMING reading rather than the reassuring one. A positive control " +
			"covers both, because it asks the same question either way.",
	},
}

func TestTreeAndControlRulesKeepTheirParts(t *testing.T) {
	for p, body := range corpusPrompts(t) {
		for _, part := range treeAndControlParts {
			i := strings.Index(body, part.heading)
			if i < 0 {
				// Absence is reported by the presence test; reporting it twice
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
			if part.re.MatchString(para) {
				continue
			}
			t.Errorf("%s: the %s rule no longer states %s (want /%s/).\n%s\nparagraph:\n%s",
				p, part.rule, part.what, part.re, indent(part.why), indent(para))
		}
	}
}

// Both rules have to survive RENDERING, not merely exist in the file. Worker
// templates are text/template, and the section they were added to has gated
// neighbours — `{{if .WorkerCores}}` on the core budget, `{{if eq .Provider
// "claude"}}` on the modal dismissal. A line that lands inside one of those
// conditionals is present in every grep and absent from the prompt an agent is
// actually handed, on exactly the dispatches where the gate is false.
func TestTreeAndControlRulesSurviveTemplateRendering(t *testing.T) {
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
		tmpl, err := template.New(path.Base(p)).Parse(body)
		if err != nil {
			t.Errorf("%s: parsing as a template: %v", p, err)
			continue
		}
		for name, vars := range shapes {
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, withDefaults(vars)); err != nil {
				t.Errorf("%s [%s]: executing: %v", p, name, err)
				continue
			}
			for _, rule := range corpusRules {
				if strings.Contains(buf.String(), rule.text) {
					continue
				}
				t.Errorf("%s: the %s rule is in the file but NOT in the prompt rendered for %s.\n%s",
					p, rule.name, name, indent("It has been moved inside a conditional block. "+
						"Both lines are unconditional guidance: they apply to every dispatch, "+
						"and a gate that hides one hides it silently — the file still greps clean."))
			}
		}
	}
}
