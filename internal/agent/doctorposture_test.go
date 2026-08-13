package agent

import (
	"strings"
	"testing"
)

// The mg-477a coverage: the SHIPPED crew/doctor.md tells doctor it is expected
// to ACT within its competence and report, rather than to investigate and hand
// off. Daniel, 2026-08-13: "what use is a doctor that can't act".
//
// This file pins the shape of that posture rather than its prose, because the
// mechanism that produced the old behaviour was structural and doctor named it
// itself when asked:
//
//	Nothing in the prompt ever FORBADE restarting an agent. The procedure had no
//	step at which acting would happen. "How to Diagnose" ran listen -> gather ->
//	explain -> *suggest fixes*, and every verb on offer was suggest / offer /
//	mail. A reader following it correctly never reached a decision to act, so no
//	prohibition was needed. The second contributor was one word: "Act directly
//	(rare — only when the work is genuinely yours)".
//
// So a test that only greps for a permission sentence would pass against the
// exact file that produced the defect. What has to hold is that the main
// procedure contains an unqualified action step, and that the hedges which
// turned acting back into an exception stay gone.
//
// The three restraints the ticket named as must-not-lose are pinned alongside,
// because a rewrite that frees doctor by dropping them trades one defect for a
// worse one.

func embeddedDoctorPrompt(t *testing.T) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile("prompts/crew/doctor.md")
	if err != nil {
		t.Fatalf("read embedded prompts/crew/doctor.md: %v", err)
	}
	return string(data)
}

// TestDoctorPromptHasAnActionStep is the structural half: acting must live in
// the main line of the procedure, ahead of reporting, and carry no frequency or
// exception hedge.
func TestDoctorPromptHasAnActionStep(t *testing.T) {
	body := embeddedDoctorPrompt(t)

	const procedure = "## How to Work"
	iProc := strings.Index(body, procedure)
	if iProc < 0 {
		t.Fatalf("no %q section — the procedure is where the action step has to live; "+
			"the old %q offered listen/gather/explain/suggest and no branch at which acting happened",
			procedure, "## How to Diagnose")
	}

	// The step that acts, and the step that reports, in that order. Ordering is
	// the point: report-then-act reads as "write it up, then maybe do it", which
	// is the behaviour being corrected.
	iAct := strings.Index(body[iProc:], "**Fix what you can fix.**")
	if iAct < 0 {
		t.Fatal("the procedure has no action step — a reader can follow it end to end and " +
			"never reach a decision to act, which is exactly how the old prompt produced " +
			"investigate-and-hand-off without forbidding anything")
	}
	iReport := strings.Index(body[iProc:], "**Report, whether or not you acted.**")
	if iReport < 0 {
		t.Fatal("the procedure has no reporting step — acting INSTEAD of reporting is not the " +
			"ask; acting AND reporting is (mg-477a)")
	}
	if iAct > iReport {
		t.Error("the action step comes after the reporting step; reporting is the second half " +
			"of the instruction, not the thing acting is optional relative to")
	}

	// The action step itself must not be hedged back into an exception.
	step := body[iProc+iAct:]
	if end := strings.Index(step, "\n4."); end > 0 {
		step = step[:end]
	}
	for _, hedge := range []string{"rare", "unusual", "exceptional", "only when", "if you must"} {
		if strings.Contains(strings.ToLower(step), hedge) {
			t.Errorf("action step carries the hedge %q — a frequency or exception claim makes every "+
				"action feel like it needs justification independently of whether it is correct, "+
				"which is what the word 'rare' did in \"Act directly (rare — only when the work is "+
				"genuinely yours)\"", hedge)
		}
	}
}

// TestDoctorPromptDropsTheHandOffFraming is the negative half: the specific
// strings that produced investigate-and-hand-off must not come back.
func TestDoctorPromptDropsTheHandOffFraming(t *testing.T) {
	body := embeddedDoctorPrompt(t)

	// Each of these may still appear in the file's account of what it replaced —
	// that history is deliberate — so they are banned only as live instructions,
	// i.e. outside the two sections that quote them.
	live := body
	for _, quoting := range []string{
		"### Why this section had to be rewritten, and not just amended",
		"- **Act, then report.**",
	} {
		if i := strings.Index(live, quoting); i >= 0 {
			end := strings.Index(live[i+len(quoting):], "\n### ")
			if end < 0 {
				end = strings.Index(live[i+len(quoting):], "\n- **")
			}
			if end < 0 {
				end = len(live) - i - len(quoting)
			}
			live = live[:i] + live[i+len(quoting)+end:]
		}
	}

	banned := []struct{ s, why string }{
		{"## How to Diagnose", "the procedure is named for its output, not for diagnosis being the whole job"},
		{"Act directly (rare", "'rare' is the frequency claim that made acting feel like an exception"},
		{"Give concrete commands the user can run", "handing the user a command doctor can run itself is the hand-off shape"},
		{"Diagnosis is your remit", "it says the remit ENDS at diagnosis"},
		{"You don't usually execute work", "doctor executes state repairs routinely; this made them feel like exceptions"},
	}
	for _, b := range banned {
		if strings.Contains(live, b.s) {
			t.Errorf("shipped doctor prompt still instructs %q — %s (mg-477a)", b.s, b.why)
		}
	}
	if strings.Contains(live, "**Stay diagnostic.**") {
		t.Error("the 'Stay diagnostic. You investigate and advise.' working principle is back as a " +
			"live instruction; it worked exactly as written and doctor reported wedged agents " +
			"instead of clearing them (mg-477a)")
	}
}

// TestDoctorPromptKeepsItsRestraints pins the three things the ticket named as
// must-not-lose. Freeing doctor by deleting these would trade one defect for a
// worse one: a restart that destroys the evidence, a repair nobody hears about,
// or an agent that respawns itself.
func TestDoctorPromptKeepsItsRestraints(t *testing.T) {
	body := embeddedDoctorPrompt(t)

	// 1. failing_turns stays restart-suppressed. An agent consuming nudges and
	//    failing each in ~10ms has a credential or limit problem; a restart
	//    inherits the credential and destroys the transcript that makes it
	//    diagnosable (mg-18d0, mg-8cdb — 23h30m on 2026-07-22).
	for _, s := range []string{"failing_turns", "restart_suppressed", "pogo agent diagnose"} {
		if !strings.Contains(body, s) {
			t.Errorf("restart authority ships without %q — doctor would restart the one class of "+
				"stall where a restart cannot help and destroys the evidence", s)
		}
	}
	if !strings.Contains(body, "do not restart") && !strings.Contains(body, "Do not restart") {
		t.Error("nothing in the prompt tells doctor when NOT to restart")
	}

	// 1b. Quiet is not failing. Added in the same change that granted the
	//     authority, because authority multiplies the cost of a wrong
	//     diagnosis — and it was earned immediately: doctor's first intended
	//     act under the new authority was restarting `com.pogo.notify`, which
	//     it then measured and found to be a correctly-idle consumer against a
	//     source nothing writes to. A restart would have repaired nothing and
	//     looked like a successful repair.
	if !strings.Contains(body, "FAILING, not merely QUIET") {
		t.Error("bound 2 no longer requires establishing that the thing is failing rather than " +
			"merely quiet — a component with no input is not broken, and restarting it produces " +
			"a green reading that means nothing (mg-477a)")
	}
	if !strings.Contains(body, "Resolve every candidate process to its owning job") {
		t.Error("the pid-resolution rule is gone — `poll-mail.sh` is shared by com.pogo.notify and " +
			"com.pogo.deadman, so the pattern naming the target also names the working delivery " +
			"channel; a pattern-kill repairs the idle job by destroying the live one")
	}

	// 2. Acting does not replace reporting.
	if !strings.Contains(body, "### Report what you did") {
		t.Error("no 'Report what you did' section — every repair is mailed to `human`; " +
			"a fix nobody was told about leaves the fleet believing a condition that is no longer true")
	}
	if !strings.Contains(body, "mg mail send human --from=doctor") {
		t.Error("the reporting section does not show the mail to `human`; the coordinator's inbox " +
			"is for coordination, and `human` is what the apple-side notifier polls")
	}

	// 3. doctor does not hand-edit installed prompts, including its own. A local
	//    edit has no expiry and silently blocks the shipped update (mg-b6bd,
	//    mg-d97f, mg-afd0). doctor declined exactly this edit and asked for the
	//    restraint to survive the rewrite.
	if !strings.Contains(body, "Do not hand-edit an installed prompt") {
		t.Error("the no-local-prompt-edit restraint is gone; it is not timidity, and a local edit " +
			"makes the shipped file's wrongness unobservable from the box that found it")
	}

	// 4. auto_start stays false (mitigates mg-8677). TestEmbeddedDoctorOnDemand
	//    owns the frontmatter assertion; this checks the prose does not invite
	//    flipping it while widening what doctor may do.
	if !strings.Contains(body, "**Do not restart yourself.**") {
		t.Error("nothing stops doctor restarting itself — it cannot observe its own wedge, and " +
			"with auto_start = false nothing brings it back")
	}
}

// TestDoctorPromptCarriesItsProvenance keeps the audit trail attached to the
// authority it justifies. The 2026-05-19 directive was recovered from doctor's
// stranded memory store during the mg-d97f census, proposed by doctor, held by
// architect for first-hand confirmation BECAUSE it widened the authority of the
// agent proposing it, and confirmed by Daniel on 2026-08-13.
func TestDoctorPromptCarriesItsProvenance(t *testing.T) {
	body := embeddedDoctorPrompt(t)
	for _, s := range []string{"2026-05-19", "2026-08-13", "what use is a doctor that can't act", "mg-477a"} {
		if !strings.Contains(body, s) {
			t.Errorf("provenance is missing %q — an authority whose origin is not recorded reads "+
				"later as an assumption, which is the thing architect refused to let it be", s)
		}
	}
}
