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
//     scan and an exit.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/client"
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
	emitHookContext(os.Stdout, runMailRecipientHook(os.Stdin))
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
// It reports two independent facts, because either one alone is a false
// all-clear: whether the hook is REGISTERED in this directory's harness
// settings, and whether the roster it consults can be READ.
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
