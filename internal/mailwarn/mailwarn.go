// Package mailwarn turns a shell command that sends macguffin mail into a
// warning about recipients nobody is home to read.
//
// # The failure it exists for
//
// `mg mail send` to a CONFIGURED agent that is not running succeeds. It exits
// 0, prints "Delivered:", and prints a message id — byte for byte the shape of
// a delivery to a live agent. Nothing in that output distinguishes the two
// cases, so the mail sits in a Maildir nobody will open, and the sender learns
// about it only by later noticing the absence of a reply, which is exactly the
// signal a merely BUSY agent also produces.
//
// On 2026-08-19 architect wrote five messages to `doctor` over an evening —
// a correction, a retraction, a work-item flag, a follow-up and a resend —
// none of which will ever be read. doctor is `auto_start = false`, was not
// running, and `pogo agent list` said so in as many words the whole time. The
// check existed; nothing PROMPTED it at the moment of sending (mg-d924).
//
// # Why the warning lives in pogo and not in mg
//
// mg owns the mailbox and pogod owns agent liveness, and they are separate
// products with no dependency between them. mg cannot answer "is this
// recipient running" without taking one, so this package takes the other
// route: pogod already installs harness hooks into an agent's working
// directory, and a PostToolUse hook can read the command the agent just ran
// and say the thing mg is not in a position to say. mg is untouched; the
// coupling is pogod -> harness, which already exists.
//
// # What it will and will not say
//
// It warns for exactly two states, both of which mean nobody is home and
// neither of which is visible in a send's output:
//
//   - ABSENT — configured, unparked, no registry entry. mg-7d20's case.
//   - PARKED — dormant by declaration. Park is the supported way to be down
//     and is never a fault, but a parked agent reads no mail until it is woken,
//     which is the property that matters at send time.
//
// It deliberately does NOT warn for a member that is present with a dead or
// exited process. That state is already a ROW in `pogo agent list`, so it is
// not the silent case this package is about — and the registry keeps a stale
// pid for the couple of seconds a restart_on_crash agent takes to come back,
// so warning on it would cry wolf at the one moment the agent is on its way up.
//
// Everything here is pure: the roster comes from the caller, so the parsing
// and the wording are testable without a daemon.
package mailwarn

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// Recipients returns the recipient names of every `mg mail send <name>` found
// in a shell command, in order, without duplicates.
//
// The input is one Bash tool call's command text, which is not a single
// invocation: it can chain with `&&`, span lines, carry a quoted heredoc body,
// and spell mg as an absolute path. So this scans a TOKEN STREAM for the
// consecutive triple (mg, mail, send) rather than trying to identify "the"
// command, and takes the first positional argument after it.
//
// It errs toward saying nothing. An unparseable command, a send whose
// recipient is behind a variable, a `send` with only flags after it — all
// yield no recipient, because a warning that names the wrong agent is worse
// than the silence this package exists to break: it teaches the reader to skip
// the line.
func Recipients(command string) []string {
	tokens := tokenize(stripHeredocs(command))
	var out []string
	seen := map[string]bool{}
	for i := 0; i+2 < len(tokens); i++ {
		if path.Base(tokens[i].text) != "mg" || tokens[i].quoted {
			continue
		}
		if tokens[i+1].text != "mail" || tokens[i+2].text != "send" {
			continue
		}
		name := positionalAfter(tokens[i+3:])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// valueFlags are the `mg mail send` flags that consume a following token when
// spelled without `=`. Cobra accepts both spellings, so `--from mayor doctor`
// and `--from=mayor doctor` name the same recipient and must parse the same.
// An unlisted `--flag` is treated as a boolean, which is the reading that
// yields a recipient rather than swallowing one.
var valueFlags = map[string]bool{
	"--from": true, "--subject": true, "--body": true, "--body-file": true,
	"-s": true, "-m": true, "-f": true,
}

// positionalAfter returns the first positional argument in tokens, stopping at
// the first shell operator — a recipient never appears past a `|` or a `&&`,
// and scanning on would pick up the next command's first word.
func positionalAfter(tokens []token) string {
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if !t.quoted && isOperator(t.text) {
			return ""
		}
		if strings.HasPrefix(t.text, "-") && !t.quoted {
			if valueFlags[t.text] {
				i++
			}
			continue
		}
		// A recipient carrying shell metacharacters was never a recipient:
		// `$AGENT` reaches mg as whatever the shell made of it, and this
		// package sees the pre-expansion text. Say nothing rather than warn
		// about a literal that no mailbox is named after.
		if strings.ContainsAny(t.text, "$`*?{}()") {
			return ""
		}
		return t.text
	}
	return ""
}

func isOperator(s string) bool {
	switch s {
	case "|", "||", "&&", ";", "&", ">", ">>", "<", "\n":
		return true
	}
	return false
}

// Warn returns the warning text for recipients that are configured agents
// nobody is home to read, or "" when there is nothing to say.
//
// Silence here is a positive answer — "I looked, and every recipient you named
// is either running or is not an agent at all". That is why a roster that
// could not be READ goes through Unavailable instead: the two must never
// render the same, which is the same rule `pogo agent list`'s absent footer
// holds itself to.
func Warn(recipients []string, rep *agent.RosterReport) string {
	if len(recipients) == 0 || rep == nil {
		return ""
	}
	byName := map[string]agent.RosterMember{}
	for _, m := range rep.Members {
		byName[m.Name] = m
	}

	var lines []string
	for _, r := range recipients {
		m, ok := byName[r]
		if !ok {
			// Not a configured agent: a work-item mailbox, `human`, a polecat,
			// anything mg knows about that pogod's roster does not. Out of
			// scope by construction — this warning is only ever about an agent
			// whose liveness pogod owns.
			continue
		}
		switch m.State {
		case agent.RosterAbsent:
			lines = append(lines, fmt.Sprintf("  %s — CONFIGURED but NOT RUNNING (%s)", m.Name, absentNote(m)))
		case agent.RosterParked:
			lines = append(lines, fmt.Sprintf("  %s — PARKED: dormant by declaration, and a parked agent reads "+
				"no mail until `pogo agent wake %s`", m.Name, m.Name))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)

	const head = "⚠ mail delivered to a mailbox nobody is reading:"
	return head + "\n" + strings.Join(lines, "\n") + "\n" +
		"The send SUCCEEDED — the message is in the Maildir and will be there when the\n" +
		"agent next starts. What did not happen is anyone reading it. If it asks for\n" +
		"action, or if you are deferring work to that agent, either start it\n" +
		"(`pogo agent start <name>`) or do the thing yourself; a reply will not come.\n" +
		"(`pogo agent roster` — full view)"
}

// absentNote renders the one thing that decides whether an absence is a fault:
// what the agent's own frontmatter asked for. It matches the wording of
// `pogo agent list`'s absent footer on purpose — a reader who sees this line at
// send time and then goes and runs the listing should meet the same sentence,
// not a second phrasing they have to reconcile with the first.
func absentNote(m agent.RosterMember) string {
	switch m.Class {
	case agent.RosterSupervised:
		return "auto_start = true — should have started at boot"
	case agent.RosterOnDemand:
		return "auto_start = false — on-demand; nothing will bring it back"
	case agent.RosterUnclassifiable:
		return "prompt unreadable — cannot say what was wanted"
	default:
		return string(m.Class)
	}
}

// Unavailable is what to say when a send was seen and the roster could not be
// read at all.
//
// It is NOT silence, and that is the whole point. Silence from this package
// means "checked, everyone you wrote to is home"; a roster pogod could not
// compute must not be able to borrow that meaning, or the instrument fails in
// exactly the direction the failure it reports on already fails — invisibly,
// and looking like the healthy case.
func Unavailable(recipients []string, err error) string {
	if len(recipients) == 0 {
		return ""
	}
	return fmt.Sprintf("⚠ could not check whether %s is running (roster unavailable: %v).\n"+
		"The mail was delivered; whether anyone is there to read it is unknown.",
		strings.Join(recipients, ", "), err)
}
