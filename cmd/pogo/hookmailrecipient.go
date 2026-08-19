package main

// The harness-side half of the dead-channel warning.
//
// `mg mail send <agent>` cannot tell you the recipient is not running: mg owns
// the mailbox, pogod owns liveness, and they are separate products. So pogod
// installs this as the harness's PostToolUse hook on Bash, where the command
// the agent just ran is available and pogod's roster is one HTTP call away.
// The warning comes back as PostToolUse additionalContext — the non-blocking
// channel, because mail to an on-demand agent that will be started later is
// legitimate and must not be refused (mg-d924).
//
// CONTRACT THIS HOLDS ITSELF TO. It runs after EVERY Bash tool call every agent
// makes, so:
//
//   - It exits 0 always. A hook that fails must not be able to disturb the tool
//     call it is reporting on.
//   - It prints nothing at all unless the command actually contained a send. A
//     hook that speaks on every bash call is a hook whose output is skimmed.
//   - It asks pogod only when a send was seen. The common path is one string
//     scan, one stamp write, and an exit.
//
// The stamp is the one thing it does unconditionally. Every other line here is
// about the warning; that write is about the HOOK, and it is what lets pogod
// answer "which running agents are armed?" without anyone running --self-check
// per agent (mg-503d). A control whose presence can only be established by
// asking each process one at a time is a control nobody knows the coverage of.
//
// OBSERVED END TO END, 2026-08-19, Claude Code 2.1.236 (mg-503d). Until then
// this hook had been shown to PRODUCE the right envelope, and the harness had
// been shown to DECLARE that it accepts that shape — but no warning had ever
// been watched arriving in a running agent's context, which is the one link
// that fails silently: a subtly wrong envelope means the hook runs, emits, and
// nothing appears, and that is indistinguishable from having had nothing to
// say. The control: a live session with this hook registered ran
//
//	mg mail send doctor --from=p503d --subject='mg-503d positive control' ...
//
// `doctor` is auto_start = false and was not running. The send reported
// `Delivered: p503d → doctor/new/1787178994277562000.19866.3000`, and the
// warning arrived — recorded in the session transcript as an attachment with
// "hookName":"PostToolUse:Bash","hookEvent":"PostToolUse" carrying the text
// below verbatim, and quoted back by the model in the same turn. Two records,
// one from the harness and one from the model, neither inferred.
//
// The same probe measured two things this file had been assuming. The hook
// process's cwd IS the session's project directory, and the payload carries it
// explicitly as `cwd` — the field recordMailRecipientFire prefers. And the hook
// fires on a bare `echo`, i.e. on tool calls with no mail in them at all, which
// is what makes the unconditional stamp a signal about the agent rather than
// about its mail habits.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/hookarm"
	"github.com/drellem2/pogo/internal/mailwarn"
)

// hookPayload is the slice of the harness's PostToolUse JSON this hook reads.
// Unknown fields are ignored by encoding/json, so a harness that adds to the
// payload does not break the hook.
type hookPayload struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	// Cwd is the harness's own statement of the project directory this session
	// runs in. It is preferred over os.Getwd() for the stamp because it is the
	// directory pogod will look in, and it is asserted by the harness rather
	// than inferred from wherever the hook process happens to have been
	// started. Both were MEASURED equal against Claude Code 2.1.236; the field
	// is used anyway, because an equality that holds today is not a contract.
	Cwd string `json:"cwd"`
}

// mailRecipientRosterFn is the roster read, indirected so tests can substitute
// a fixture for a live pogod. Production binds client.AgentRoster.
var mailRecipientRosterFn = client.AgentRoster

// runMailRecipientHook reads a PostToolUse payload and returns the text to feed
// back into the agent's context, or "" for silence.
//
// Silence is a positive answer here: it means a send was checked and every
// recipient is either running or is not an agent pogod knows about. The one
// thing it must never mean is "the check did not run" — hence the Unavailable
// branch, which speaks up about its own failure.
func runMailRecipientHook(in io.Reader) string {
	data, err := io.ReadAll(in)
	if err != nil {
		return ""
	}
	return mailRecipientWarning(data)
}

// mailRecipientWarning is runMailRecipientHook over already-read bytes, split
// out so the command body can stamp from the same payload it warns from rather
// than reading stdin twice.
func mailRecipientWarning(data []byte) string {
	var p hookPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return ""
	}
	if p.ToolName != "" && p.ToolName != "Bash" {
		return ""
	}
	cmdText := p.ToolInput.Command
	// Cheap gate before anything expensive: the overwhelming majority of Bash
	// calls have nothing to do with mail, and this hook is on all of them.
	if !strings.Contains(cmdText, "mail") || !strings.Contains(cmdText, "send") {
		return ""
	}
	recipients := mailwarn.Recipients(cmdText)
	if len(recipients) == 0 {
		return ""
	}
	rep, err := mailRecipientRosterFn()
	if err != nil {
		return mailwarn.Unavailable(recipients, err)
	}
	return mailwarn.Warn(recipients, rep)
}

// emitHookContext writes the harness's PostToolUse additionalContext envelope,
// which is how a hook puts text in front of the model without blocking the tool
// call it followed.
func emitHookContext(w io.Writer, text string) {
	if text == "" {
		return
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"additionalContext": text,
		},
	}
	enc, err := json.Marshal(out)
	if err != nil {
		return
	}
	fmt.Fprintln(w, string(enc))
}

// mailRecipientHookMain is the command body, split out so the cobra wiring in
// main.go stays a one-liner and the behaviour is testable without a process.
func mailRecipientHookMain(selfCheck bool) {
	if selfCheck {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "pogo hook mail-recipient --self-check: %v\n", err)
			os.Exit(1)
		}
		os.Exit(mailRecipientSelfCheck(os.Stdout, dir))
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	recordMailRecipientFire(data)
	emitHookContext(os.Stdout, mailRecipientWarning(data))
}

// recordMailRecipientFire stamps this agent's working directory so pogod can
// see that the hook RAN, in this process, rather than only that the settings
// file names it.
//
// Errors are dropped on purpose and are not printed. This hook runs after every
// Bash tool call every agent makes; a stamp it cannot write costs the fleet
// report one agent's certainty — it must never cost the agent a line of stderr
// on every command, and it must never be able to disturb the tool call it
// follows. The state it leaves behind, "registered but never seen firing", is
// the honest reading of a stamp that could not be written.
func recordMailRecipientFire(data []byte) {
	var p hookPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	dir := p.Cwd
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return
		}
	}
	_ = hookarm.RecordFire(dir)
}

// mailRecipientSelfCheck answers "is this warning actually armed for me?" for
// the agent whose working directory is dir.
//
// The remedy in this file has the same shape as the defect it repairs: it
// reports by SPEAKING, so every way it can fail to speak — hook never
// installed, pogod unreachable, an old pogod that predates the hook — looks
// exactly like "every recipient you wrote to is running". That is unacceptable
// in a fix for a silent failure, so the state is made askable in one command
// instead of being inferred from the absence of warnings.
//
// It reports three independent facts, because any one alone is a false
// all-clear: whether the hook is REGISTERED in this directory's harness
// settings, whether it has ever been SEEN RUNNING here, and whether the roster
// it consults can be READ.
//
// The middle one is the one a settings file cannot supply. A harness loads its
// hooks when the session starts, so a registration written afterwards is a
// promise the running process never heard, and a registration naming a binary
// that has moved is a hook that runs and fails — both indistinguishable on disk
// from a working one. Note the asymmetry this command cannot fix: it has no
// process start time to compare the stamp against, so it prints when the hook
// last ran and leaves the reader to judge. pogod HAS the start time, which is
// why `pogo agent list` is the sharper instrument (mg-503d).
func mailRecipientSelfCheck(w io.Writer, dir string) int {
	ok := true

	cmd, installed, err := claude.MailRecipientHookCommand(dir)
	switch {
	case err != nil:
		fmt.Fprintf(w, "hook registration: UNKNOWN — %v\n", err)
		ok = false
	case installed:
		fmt.Fprintf(w, "hook registration: yes — %s\n", cmd)
	default:
		fmt.Fprintf(w, "hook registration: NO — nothing is registered under PostToolUse in\n"+
			"  %s/.claude/settings.local.json. Mail from this agent to a stopped\n"+
			"  recipient will report Delivered with no warning, exactly as before mg-d924.\n"+
			"  pogod installs it at spawn, so an agent started before this shipped needs a\n"+
			"  restart to get it.\n", dir)
		ok = false
	}

	// Reported, deliberately NOT part of the exit code. This command is itself
	// run through Bash, so the hook fires AFTER it: the first --self-check of a
	// perfectly armed session sees no stamp, and failing on that would be a
	// false alarm in the one instrument whose credibility this fix depends on.
	// pogod is not in that race — it reads the stamp long after the fact and has
	// a start time to date it against — which is why the fleet view treats the
	// same fact as decisive and this does not.
	fired, err := hookarm.LastFire(dir)
	switch {
	case err != nil:
		fmt.Fprintf(w, "hook observed running: UNKNOWN — %v\n", err)
	case fired.IsZero():
		fmt.Fprintf(w, "hook observed running: not yet — nothing has written %s.\n"+
			"  Expected on a session's FIRST self-check (this command runs through Bash,\n"+
			"  so the hook fires after it). Still 'not yet' on the second is the tell that\n"+
			"  the registration was never loaded, or names a command that does not run.\n",
			hookarm.StampPath(dir))
	default:
		fmt.Fprintf(w, "hook observed running: yes — last at %s\n", fired.Format(time.RFC3339))
	}

	rep, err := mailRecipientRosterFn()
	switch {
	case err != nil:
		fmt.Fprintf(w, "roster read: FAILED — %v\n", err)
		ok = false
	case rep == nil || rep.Configured == 0:
		fmt.Fprintf(w, "roster read: EMPTY — no configured crew agents to compare against,\n"+
			"  which is not the same as everybody being present.\n")
		ok = false
	default:
		fmt.Fprintf(w, "roster read: ok — %d configured, %d not running\n", rep.Configured, len(rep.Absent))
		for _, m := range rep.Absent {
			fmt.Fprintf(w, "  %s would be warned about\n", m.Name)
		}
	}

	if ok {
		return 0
	}
	return 1
}
