package promptcli

// Both arms of the mg-21b1 control, against text that actually shipped.
//
// A check that has only ever been seen to pass on the current prompts proves
// nothing — the current prompts are the population it was written against. So
// every positive control here is the PRE-FIX text of a real defect, pinned from
// git, and every negative control is either the correction that replaced it or
// a shape that was already correct and must stay unflagged.
//
// Positive controls (each shipped, each found by someone RUNNING the command):
//
//	mg-d8ea  pm/pm-template.md:500  `mg spend --by item --tag=<tag>`
//	                                (unfixed at the time of writing; the live
//	                                 corpus test is where it shows up)
//	mg-4bb9  mayor.md:91 pre-6172fe3  "there is no append/comment subcommand"
//	mg-159a  mayor.md:196 pre-c32a5d9 "anything else (default `task`) … omit `--template`"
//
// Negative controls:
//
//	the corrected form of each of the three;
//	a correction that QUOTES the false claim in order to withdraw it;
//	a TRUE absence claim (mayor.md's "There is no `pogo server restart`");
//	a true absence claim whose flag shares a prefix with a real one
//	  (`mg show --body` is absent; `--body-hash` exists);
//	a placeholder-bearing example.

import (
	"strings"
	"testing"
)

// fixtureSurfaces is a hand-built stand-in for the two CLIs, holding exactly
// the facts these fixtures turn on. It keeps this file hermetic and fast; the
// same facts are re-asserted against the REAL discovered surfaces by
// cmd/pogo's live corpus test, so the stand-in cannot quietly drift from the
// tools it models.
func fixtureSurfaces() map[string]*Surface {
	mg := NewSurface("mg")
	mg.Add([]string{"mg"}, []string{"done", "edit", "list", "mail", "new", "shelve", "show", "spend"}, []string{"help", "root"})
	mg.Add([]string{"mg", "done"}, nil, []string{"result", "help", "root"}).TakesArgs = true
	mg.Add([]string{"mg", "edit"}, nil, []string{
		"append-body", "append-body-file", "body", "body-file", "body-hash",
		"if-unchanged", "title", "assignee", "add-tags", "priority", "help", "root",
	})
	mg.Add([]string{"mg", "list"}, nil, []string{"all", "archived", "assignee", "json", "repo", "shelved", "status", "tag", "wide", "help", "root"})
	mg.Add([]string{"mg", "show"}, nil, []string{"body-hash", "if-unchanged", "json", "help", "root"})
	mg.Add([]string{"mg", "spend"}, nil, []string{"by", "json", "rebuild", "since", "total", "window", "help", "root"})
	mg.Add([]string{"mg", "shelve"}, nil, []string{"tag", "help", "root"})
	mg.Add([]string{"mg", "new"}, nil, []string{"title", "body", "repo", "type", "help", "root"})
	mg.Add([]string{"mg", "mail"}, []string{"list", "read", "send"}, []string{"help", "root"})
	mg.Add([]string{"mg", "mail", "send"}, nil, []string{"from", "subject", "body", "body-file", "help", "root"})
	mg.Add([]string{"mg", "mail", "list"}, nil, []string{"help", "root"})
	mg.Add([]string{"mg", "mail", "read"}, nil, []string{"help", "root"})

	pogo := NewSurface("pogo")
	pogo.Add([]string{"pogo"}, []string{"agent", "server", "refinery", "schedule"}, []string{"help", "json"})
	pogo.Add([]string{"pogo", "agent"}, []string{"list", "spawn", "spawn-polecat", "stop"}, []string{"help", "json"})
	pogo.Add([]string{"pogo", "agent", "spawn-polecat"}, nil, []string{"task", "id", "repo", "branch", "template", "help", "json"})
	pogo.Add([]string{"pogo", "agent", "list"}, nil, []string{"help", "json"})
	pogo.Add([]string{"pogo", "agent", "stop"}, nil, []string{"help", "json"})
	pogo.Add([]string{"pogo", "agent", "spawn"}, nil, []string{"help", "json"})
	pogo.Add([]string{"pogo", "server"}, []string{"start", "status", "stop"}, []string{"help", "json"})
	pogo.Add([]string{"pogo", "server", "start"}, nil, []string{"help", "json"})
	pogo.Add([]string{"pogo", "server", "stop"}, nil, []string{"all", "help", "json"})
	pogo.Add([]string{"pogo", "server", "status"}, nil, []string{"help", "json"})
	pogo.Add([]string{"pogo", "refinery"}, []string{"show", "submit"}, []string{"help", "json"})
	pogo.Add([]string{"pogo", "refinery", "show"}, nil, []string{"help", "json"})
	pogo.Add([]string{"pogo", "refinery", "submit"}, nil, []string{"repo", "author", "target", "help", "json"})
	// A runnable group: `pogo schedule <agent> --cron …` as well as
	// `pogo schedule ack|list|rm`.
	pogo.Add([]string{"pogo", "schedule"}, []string{"ack", "list", "rm"},
		[]string{"cron", "id", "replay", "message", "once", "in", "help", "json"}).TakesArgs = true
	pogo.Add([]string{"pogo", "schedule", "ack"}, nil, []string{"agent", "token", "help", "json"})
	pogo.Add([]string{"pogo", "schedule", "list"}, nil, []string{"agent", "help", "json"})
	pogo.Add([]string{"pogo", "schedule", "rm"}, nil, []string{"agent", "help", "json"})

	return map[string]*Surface{"mg": mg, "pogo": pogo}
}

// fixtureMappedTypes mirrors agent.workerTemplateForType. The live rule in
// cmd/pogo calls agent.TemplateForType directly; here the map is inlined so the
// package under test stays free of an internal/agent dependency.
var fixtureMappedTypes = map[string]bool{"design": true, "qa": true}

func fixtureChecker() *Checker {
	return &Checker{
		Surfaces: fixtureSurfaces(),
		Omissions: []OmissionRule{{
			Path: []string{"pogo", "agent", "spawn-polecat"},
			Flag: "template",
			AcceptedWithout: func(scope string) bool {
				return fixtureMappedTypes[strings.ToLower(strings.TrimSpace(scope))]
			},
			Why: "the type→template map is closed; an unmapped type selects no template and the spawn is refused with a 409",
		}},
	}
}

func findingsFor(t *testing.T, body string) []Finding {
	t.Helper()
	return fixtureChecker().CheckFile("fixture.md", body)
}

func hasRule(fs []Finding, rule string, subjectSubstr string) bool {
	for _, f := range fs {
		if f.Rule == rule && strings.Contains(f.Subject, subjectSubstr) {
			return true
		}
	}
	return false
}

func dump(fs []Finding) string {
	if len(fs) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("\n  " + f.String())
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// MUST FLAG — the three defects, from the text that shipped
// ---------------------------------------------------------------------------

// mgD8eaPreFix is pm/pm-template.md:498-500 as it shipped. `mg spend` has no
// --tag; the working form is `--by tag:<tag>`. It fails "at the exact moment a
// PM is computing what to tell Daniel".
const mgD8eaPreFix = "```bash\n" +
	"# Trajectory: 7-day spend rolled up by tag and by item, scoped to your product.\n" +
	"mg spend --by tag    --since 7d --json\n" +
	"mg spend --by item   --tag=<your-tag> --since 7d --json\n" +
	"```\n"

func TestMustFlag_mgD8ea_UnknownFlagOnSpend(t *testing.T) {
	got := findingsFor(t, mgD8eaPreFix)
	if !hasRule(got, RuleUnknownFlag, "mg spend --tag") {
		t.Fatalf("mg-d8ea not flagged; findings:%s", dump(got))
	}
	// The line above it is correct and must not be swept up with it.
	for _, f := range got {
		if strings.Contains(f.Subject, "--by") || strings.Contains(f.Subject, "--since") {
			t.Errorf("flagged a real flag on mg spend: %s", f)
		}
	}
}

// mg4bb9PreFix is mayor.md:91 as it shipped before 6172fe3, repeated verbatim
// in pm/pm-template.md and crew/doctor.md. `--append-body` and
// `--append-body-file` exist, and `mg edit --help` OPENS by recommending the
// second. Nobody ran it, because the prompt had already answered the question.
const mg4bb9PreFix = "- **Update fields without claiming.** `mg edit <id> --title=... --add-tags=... " +
	"--priority=... --assignee=...` for metadata. `mg edit <id> --body=\"<new body>\"` replaces " +
	"the body wholesale — there is no append/comment subcommand. To leave a note for a future " +
	"actor without rewriting the body, mail them.\n"

func TestMustFlag_mg4bb9_FalseAbsenceOfAppend(t *testing.T) {
	got := findingsFor(t, mg4bb9PreFix)
	if !hasRule(got, RuleFalseAbsence, "append") {
		t.Fatalf("mg-4bb9 not flagged; findings:%s", dump(got))
	}
	for _, f := range got {
		if f.Rule == RuleFalseAbsence && strings.Contains(f.Detail, "--append-body") {
			return
		}
	}
	t.Errorf("the finding should name the flags that exist; got:%s", dump(got))
}

// mg159aPreFix is mayor.md's step-2 type table as it shipped before c32a5d9.
// `task` is the DEFAULT type and by far the most common, so this was wrong
// about the ordinary dispatch, not an edge case: the closed Go map selects no
// template for it and pogod refuses the spawn with a 409.
const mg159aPreFix = "For everything else, the work item's **`type`** field picks the template.\n\n" +
	"| `type` | template |\n" +
	"|---|---|\n" +
	"| `design` | `--template=polecat-architect` |\n" +
	"| `qa` | `--template=polecat-qa` |\n" +
	"| anything else (default `task`) | default {{.Worker}} — omit `--template` |\n"

func TestMustFlag_mg159a_OmitTemplateForAnUnmappedType(t *testing.T) {
	got := findingsFor(t, mg159aPreFix)
	if !hasRule(got, RuleBadOmission, "--template") {
		t.Fatalf("mg-159a not flagged; findings:%s", dump(got))
	}
	// The two rows above it route correctly and must survive.
	for _, f := range got {
		if strings.Contains(f.Evidence, "polecat-architect") || strings.Contains(f.Evidence, "polecat-qa") {
			t.Errorf("flagged a correct routing row: %s", f)
		}
	}
}

// TestMustFlag_UnknownSubcommand covers the fourth shape: a group command given
// a subcommand it has not got.
func TestMustFlag_UnknownSubcommand(t *testing.T) {
	got := findingsFor(t, "Bounce it with `pogo server restart` when it wedges.\n")
	if !hasRule(got, RuleUnknownSubcommand, "pogo server restart") {
		t.Fatalf("unknown subcommand not flagged; findings:%s", dump(got))
	}
}

// ---------------------------------------------------------------------------
// MUST NOT FLAG — the corrections, the retractions, the placeholders
// ---------------------------------------------------------------------------

func TestMustNotFlag(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			// mg-d8ea's working form. One tag, with an item breakdown.
			"mg-d8ea corrected",
			"```bash\nmg spend --by tag:<your-tag> --since 7d --json\n```\n",
		},
		{
			// mg-4bb9's replacement text. It necessarily QUOTES the false claim
			// in order to withdraw it — an absence-predicate with no retraction
			// handling scores this careful correction worse than the sloppy
			// original it replaced.
			"mg-4bb9 corrected, quoting the claim it retracts",
			"  **This bullet asserted the opposite until mg-4bb9, and that is the lesson rather than " +
				"a footnote.** It said `mg edit` had \"no append/comment subcommand\" and sent you to mail " +
				"instead. `mg edit --help` opened, and still opens, with the banner **ADDING TO A BODY? " +
				"USE `--append-body-file`, NOT `--body-file`**.\n",
		},
		{
			// The same retraction written with the explicit marker, which is
			// what a prompt author should reach for rather than hoping their
			// phrasing lands in the vocabulary.
			"explicit retraction marker",
			"<!-- promptcli:retracted --> The old text claimed `mg edit` has no append subcommand.\n",
		},
		{
			// mg-159a's replacement row: it names the refusal and the flag that
			// gets past it, and instructs nothing to be omitted.
			"mg-159a corrected",
			"| anything else — bare `task`, `scoping`, `audit`, `bug`, or no `type` at all | " +
				"**unmapped: no template is selected and the spawn is refused with a 409 naming the type.** " +
				"Pass `--template=polecat` explicitly to dispatch the build polecat by hand. |\n",
		},
		{
			// An omission instruction that is TRUE: `design` is in the map, so
			// omitting --template routes rather than refusing.
			"omission that the map accepts",
			"For a `design` item you may omit `--template`; the router picks the architect.\n",
		},
		{
			// A TRUE absence claim, shipped in mayor.md. The tool agrees, so
			// there is nothing to report — and the invocation quoted inside the
			// denial must not be re-reported as an unknown subcommand.
			"true absence claim about a command",
			"The surfaces are `pogo server start`, `pogo server stop` and `pogo server status`. " +
				"There is no `pogo server restart` command; a restart is stop-then-start.\n",
		},
		{
			// A true absence claim whose flag shares a prefix with a real one.
			// `mg show --body` genuinely does not exist (mg-9fc8 is the incident
			// where its usage error got stored as a work item body) even though
			// `--body-hash` does. Stem matching would get this backwards, which
			// is why a literally-named flag is matched exactly.
			"true absence claim about a prefix-sharing flag",
			"They also record that `mg show <id> --body` does not exist; use " +
				"`mg show <id> --json | jq -r .body`.\n",
		},
		{
			// A true absence claim about a flag, shipped in mayor.md.
			"true absence claim about a flag",
			"`mg shelve <id>` removes the item from normal listings. `mg shelve` does not take a " +
				"`--note` flag, so pair it with a one-line mail.\n",
		},
		{
			// Placeholders are not arguments to validate.
			"placeholder-bearing example",
			"```bash\npogo agent spawn-polecat <short-id> \\\n  --template=polecat \\\n" +
				"  --task=\"<work item title>\" \\\n  --id=\"<work item id>\" \\\n" +
				"  --repo=\"<target repo path>\"\n```\n",
		},
		{
			// A template action inside a command NAME. Rendering resolves
			// `spawn-{{.Worker}}` to a real subcommand; stripping would not.
			"template variable inside a command name",
			"You were spawned via `pogo agent spawn-{{.Worker}} --template=polecat-architect`.\n",
		},
		{
			// A heredoc body is data, not a command. This one contains a line
			// that reads like an invocation.
			"heredoc body",
			"```bash\nmg mail send mayor --from=$POGO_AGENT_NAME --body-file - <<'EOF'\n" +
				"mg frobnicate --with-a-flag-that-does-not-exist\nEOF\n```\n",
		},
		{
			// A non-shell fence. pm-template.md ships a ```markdown roadmap
			// skeleton whose prose contains "Filing as mg if approved."
			"non-shell fenced block",
			"```markdown\n## Later (proposed)\n- *Idea: thing X. Filing as mg if approved.*\n```\n",
		},
		{
			// A PROHIBITION is not an absence claim. Every command in this
			// list exists; the prompt is telling a triage worker not to run
			// them. Reading "no `X`" as "`X` does not exist" would report the
			// three that do, and — worse — suppress a real defect written the
			// same way.
			"prohibition list",
			"- **You do not write code.** No commits, no branch pushes, no `gh pr create`, " +
				"no `pogo refinery submit`, and no `mg done` until the verdict lands.\n",
		},
		{
			// Out of scope by design: not our tools, not our surface to pin.
			"non-pogo commands",
			"```bash\ngh issue close 41 --comment \"done\"\ngit rebase --onto main\njq -r .status\n```\n",
		},
		{
			// A leaf command's positional argument is not a bad subcommand.
			"positional arguments on leaf commands",
			"```bash\nmg show mg-21b1\nmg mail read 21b1 3\npogo refinery show mr-7 --json\n```\n",
		},
		{
			// A bare `pogo`/`mg` in prose, and a hyphenated identifier that
			// merely contains the root name.
			"prose mentions and lookalike identifiers",
			"Your display label is `pogo-cat-<name>`; `pgrep -f pogo-cat-<name>` matches nothing. " +
				"pogo stays running while mg is idle.\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findingsFor(t, tc.body); len(got) != 0 {
				t.Errorf("expected no findings, got:%s", dump(got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Falsification: the negative arm must be able to fail
// ---------------------------------------------------------------------------

// TestNegativeArmIsNotVacuous guards against the way an absence-predicate dies:
// a suppression grows until it suppresses everything and the suite still
// passes. Each fixture below is a MUST-NOT-FLAG case with its suppressing
// property removed, and each one must then be flagged.
func TestNegativeArmIsNotVacuous(t *testing.T) {
	cases := []struct {
		name string
		body string
		rule string
	}{
		{
			"retraction markers removed from the mg-4bb9 correction",
			"The bullet notes that `mg edit` has no append/comment subcommand.\n",
			RuleFalseAbsence,
		},
		{
			"fence language changed from markdown to bash",
			"```bash\nmg frobnicate --json\n```\n",
			RuleUnknownSubcommand,
		},
		{
			"placeholder replaced by a literal bad flag",
			"```bash\npogo agent spawn-polecat short-id --templat=polecat\n```\n",
			RuleUnknownFlag,
		},
		{
			"heredoc terminator removed, so the body is read as commands",
			"```bash\nmg mail send mayor --from=x\nmg frobnicate --with-a-flag\n```\n",
			RuleUnknownSubcommand,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findingsFor(t, tc.body)
			if !hasRule(got, tc.rule, "") {
				t.Errorf("expected a %s finding, got:%s", tc.rule, dump(got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit coverage for the pieces the fixtures exercise indirectly
// ---------------------------------------------------------------------------

func TestRenderKeepsLineNumbers(t *testing.T) {
	in := "a\n{{if eq .Provider \"claude\"}}\nb {{.Worker}}\n{{end}}\nc\n"
	out := Render(in)
	if got, want := strings.Count(out, "\n"), strings.Count(in, "\n"); got != want {
		t.Fatalf("line count changed: got %d newlines, want %d", got, want)
	}
	if !strings.Contains(out, "b polecat") {
		t.Errorf("worker not rendered: %q", out)
	}
}

func TestRenderUnknownActionBecomesPlaceholder(t *testing.T) {
	out := Render("run `mg show {{.SomethingNew}}`")
	if !strings.Contains(out, Placeholder) {
		t.Fatalf("unknown action should become a placeholder: %q", out)
	}
}

func TestParseHelpReadsSubcommandsAndInheritedFlags(t *testing.T) {
	page := `Show details for a single merge request

Usage:
  pogo refinery show <mr-id> [flags]
  pogo refinery show [command]

Available Commands:
  cancel      Cancel a merge request
  show        Show details

Flags:
  -h, --help   help for show

Global Flags:
      --json   Output in JSON format
`
	subs, flags, takesArgs := ParseHelp(page)
	if len(subs) != 2 || subs[0] != "cancel" || subs[1] != "show" {
		t.Errorf("subcommands = %v", subs)
	}
	var hasJSON bool
	for _, f := range flags {
		if f == "json" {
			hasJSON = true
		}
	}
	if !hasJSON {
		t.Errorf("inherited --json missing from %v", flags)
	}
	if !takesArgs {
		t.Errorf("`<mr-id>` in the usage line should mark the command as taking positionals")
	}
}

func TestParseHelpPureGroupTakesNoArgs(t *testing.T) {
	_, _, takesArgs := ParseHelp("Manage agent processes\n\nUsage:\n  pogo agent [command]\n\nAvailable Commands:\n  list  List agents\n")
	if takesArgs {
		t.Errorf("`pogo agent [command]` is a pure group and takes no positionals")
	}
}
