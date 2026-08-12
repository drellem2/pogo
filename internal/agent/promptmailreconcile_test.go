package agent

// The SCOPE of the mail-reconcile rule, pinned as a property of the shipped
// corpus (mg-8918).
//
// # The defect
//
// The rule was already there. Every prompt carrying the act-then-mark mail
// discipline (mg-f73e) already ended it with "Reconcile after interruption. If
// a mail batch was interrupted, re-list and reconcile on the next cycle; don't
// trust the unread filter alone."
//
// On the 2026-08-12 03:01 bounce two agents that shipped with that sentence
// each came back with an unhandled mail they could not see. It was
// under-scoped in two specific ways:
//
//  1. "Interruption" reads as an interrupted BATCH. A restarted agent is a new
//     session that never saw the batch, so it does not know an interruption
//     occurred and the rule does not visibly apply to it. The successor
//     inherits the obligation from a predecessor that cannot tell it anything.
//
//  2. No instrument. "Re-list and reconcile" does not say against what. The
//     procedure that actually worked is `mg mail list <you> --all` compared
//     against the agent's last recorded activity; `--all` is load-bearing
//     because the unread filter cannot surface a read-but-unhandled mail by
//     construction, which is the entire failure mode.
//
// # Why this is a corpus test and not just an edit
//
// This is the same class of defect as mg-49b1 next door, with the sign
// flipped: there the wrong sentence was present, here the right sentence was
// present and too narrow. Both are invisible without something that reads the
// shipped text. An under-scoped rule leaves nothing to grep for — the file
// looks correct, and the only evidence is a mail nobody can find afterwards.
//
// So the assertions are about SHAPE, and the population is DERIVED from the
// mail-discipline text rather than enumerated, so a prompt that acquires the
// discipline later is held to the same scope on the day it acquires it.
//
// # What is deliberately NOT asserted
//
// Not the removal of anything. mg-8918 EXTENDS a correct bullet; the mg-f73e
// citation and the enumerate/dispose/end-of-turn discipline it justifies are
// asserted to still be present, because the failure mode of a scope-widening
// edit is that a later tidy-up replaces the original rule with the addition
// instead of keeping both.

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// mailDiscipline selects the population: prompts that carry the act-then-mark
// mail rule. Matched on the discipline's NAME rather than on its heading
// structure, because doctor.md renders it as a bolded run-in paragraph and the
// other two as a numbered list under a heading — and a prompt that adopts the
// discipline adopts the name with it, so this stays derived rather than becoming
// an enumeration of the three files that had it on the day it was written.
//
// The first draft selected on "read-but-unhandled" — the mechanism — and pulled
// in templates/polecat-triage.md, which uses that phrase for a different rule
// entirely (mg-039b: a mail to the coordinator is a compression of the finding,
// not a substitute for the work-item body). That is a correct exclusion rather
// than a convenient one: a {{.Worker}} is ephemeral and single-task, it runs no
// recurring mail cycle to interrupt, and it has no predecessor's on-disk
// heartbeat to reconcile against. If {{.Worker}} templates ever grow one, they
// come into this population by taking the name.
var mailDiscipline = regexp.MustCompile(`(?i)act-then-mark`)

// disciplineSection returns the text the assertions below are judged against:
// the act-then-mark passage itself, from its name to the next heading of any
// level. Everything here has to be true IN THAT PASSAGE, not merely somewhere
// in a 700-line prompt.
//
// This bound is not tidiness. The first draft matched against the whole body
// and the "what to reconcile against" assertion was GREEN on the unedited
// mayor.md and pm-template.md — both mention `sweep.log` elsewhere, in the
// stall-watch sections, about a different agent's heartbeat entirely. An
// assertion satisfied by unrelated text is a presence check wearing a
// correctness label: it would have reported this whole ticket as already fixed.
func disciplineSection(body string) string {
	loc := mailDiscipline.FindStringIndex(body)
	if loc == nil {
		return ""
	}
	rest := body[loc[0]:]
	if i := regexp.MustCompile(`\n#{1,6} `).FindStringIndex(rest); i != nil {
		rest = rest[:i[0]]
	}
	return rest
}

var reconcileScope = []struct {
	what string
	re   *regexp.Regexp
	why  string
}{
	{
		what: "the restart named as an interruption",
		re:   regexp.MustCompile(`(?i)(?:bounce|crash|restart|redeploy)[^.\n]{0,200}interruption|interruption[^.\n]{0,200}(?:bounce|crash|restart|redeploy)|RESTART is an interruption`),
		why: "The bullet said \"interrupted\", which a restarted session reads as an " +
			"interrupted BATCH — an event it has no memory of and therefore no reason " +
			"to think applies to it. Two agents shipped with the unextended sentence " +
			"and both lost mail on one bounce (mg-8918). The restart case has to be " +
			"named, not implied.",
	},
	{
		what: "the instrument — `mg mail list <you> --all`",
		re:   regexp.MustCompile(`mg mail list [^\n]*--all`),
		why: "\"Re-list and reconcile\" does not say against what, so it is advice rather " +
			"than a procedure. This is the one that worked on the night, and `--all` is " +
			"the load-bearing part.",
	},
	{
		what: "why --all is required, not merely helpful",
		re:   regexp.MustCompile(`(?i)unread filter cannot surface[^.\n]{0,120}by construction`),
		why: "Without the reason, `--all` reads as a thoroughness flag and gets dropped " +
			"by the next reader optimising a command. It is not optional: a " +
			"read-but-unhandled mail is invisible to the unread filter BY CONSTRUCTION, " +
			"which is precisely the mail this reconciliation exists to find.",
	},
	{
		what: "what to reconcile against",
		re:   regexp.MustCompile(`(?i)turnlog|sweep\.log`),
		why: "A restarted session has no memory of the batch, so the comparison has to be " +
			"against an on-disk artifact its predecessor left: the turnlog line (mg-a270) " +
			"or the sweep.log heartbeat. Naming neither leaves \"reconcile\" with no " +
			"second operand.",
	},
	{
		what: "the mg-f73e citation it extends",
		re:   regexp.MustCompile(`mg-f73e`),
		why: "mg-8918 widens an existing correct rule. If a later edit replaces the rule " +
			"with the widening, the corpus keeps the restart procedure and loses the " +
			"reason a read-but-unhandled mail is a permanent silent drop at all.",
	},
}

func TestMailReconcileRuleNamesTheRestartAndItsInstrument(t *testing.T) {
	var population []string
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
		body := string(b)
		if !mailDiscipline.MatchString(body) {
			return nil
		}
		population = append(population, p)

		section := disciplineSection(body)
		for _, req := range reconcileScope {
			if req.re.MatchString(section) {
				continue
			}
			t.Errorf("%s: the act-then-mark passage does not state %s (want /%s/).\n%s\nsection:\n%s",
				p, req.what, req.re, indent(req.why), indent(section))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if len(population) == 0 {
		t.Fatal("no prompt carries the act-then-mark mail discipline.\n" +
			"Either it was removed from the whole corpus — which is the mg-f73e " +
			"regression this test exists to notice — or the phrasing this test " +
			"selects on drifted and it is now asserting nothing.")
	}
	t.Logf("held: %s", strings.Join(population, ", "))
}

// The reconcile instruction is addressed to a specific mailbox, and the mailbox
// name is per-prompt: `{{.Coordinator}}` for the coordinator, `pm-<your-name>`
// for the PM template, `doctor` for doctor. A copy-paste that leaves another
// agent's name in the command sends the reader to a box that is not theirs —
// and `mg mail list` exits 0 on a mailbox that never existed, so the mistake is
// silent exactly where this procedure is supposed to end silence.
func TestReconcileInstrumentNamesTheReadersOwnMailbox(t *testing.T) {
	// The mailbox each prompt's own mail-discipline text must address. Derived
	// from the listing the prompt already tells its agent to run for the normal
	// unread check, so the two cannot drift apart.
	want := map[string]*regexp.Regexp{
		"mayor.md":          regexp.MustCompile(`mg mail list \{\{\.Coordinator\}\} --all`),
		"pm/pm-template.md": regexp.MustCompile(`mg mail list pm-<your-name> --all`),
		"crew/doctor.md":    regexp.MustCompile(`mg mail list doctor --all`),
	}
	for p, re := range want {
		b, err := fs.ReadFile(DefaultPromptsFS(), p)
		if err != nil {
			t.Errorf("reading %s: %v\nIf this prompt was renamed or removed, re-point this "+
				"test deliberately — and check the replacement carries the reconcile "+
				"instrument addressed to its own mailbox.", p, err)
			continue
		}
		if !re.MatchString(string(b)) {
			t.Errorf("%s: reconcile instrument does not address this agent's own mailbox (want /%s/).\n%s",
				p, re, indent("`mg mail list` exits 0 on a mailbox that never existed, so a "+
					"pasted-in wrong name reads as a clean reconciliation with nothing to find "+
					"— the exact silence this procedure exists to break."))
		}
	}
}
