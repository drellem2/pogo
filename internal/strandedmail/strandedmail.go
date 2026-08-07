// Package strandedmail finds mail sitting in a mailbox that no live mail-check
// polls.
//
// It exists because repointing a wrong mail-check is not a complete fix. When a
// polecat's mail-check was derived from its work item (mg-aa96), correspondents
// addressed its AGENT NAME while the schedule read the work-item mailbox — so
// repointing the schedule to the agent name only changes where the agent looks
// NEXT. Everything already delivered to the abandoned box stays there, and the
// repoint converts a misdelivery into an ORPHAN. Doctor's sweep of the live
// fleet on 2026-08-05 found one: box `b468` held 1 unread while agent `wb468`
// polled `wb468`, and it was an urgent correction to a builder mid-flight.
//
// A silent cutover has the same shape as the bug it fixes — mail exists, nobody
// reads it, nothing says so. This package is the "something says so".
//
// It REPORTS and never moves mail. Re-delivering would have to forge a sender
// (`mg mail send` writes a new message with a new From) or reach into another
// tool's maildir; both turn a recoverable orphan into a message whose
// provenance is a lie. Naming the message, its sender, its subject and the exact
// `mg mail read` that opens it is recovery enough, and it is honest.
package strandedmail

import (
	"fmt"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/scheduler"
)

// Mailbox is one record of `mg mail list --json` with no AGENT: every mailbox
// under the mail root with its unread count. This enumeration is what makes the
// check possible at all — it is the only view in which a mailbox nobody polls is
// visible from outside the agent that should have been reading it.
type Mailbox struct {
	Name   string `json:"mailbox"`
	Unread int    `json:"unread"`
	Exists bool   `json:"exists"`
}

// Message is one record of `mg mail list <agent> --json`.
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Read    bool   `json:"read"`
}

// MailCheck is one live mail-check schedule reduced to the identities that can
// disagree: who it is FOR, what it is KEYED on, and where it actually SENDS
// that agent.
type MailCheck struct {
	// Agent is the agent whose reachability channel this schedule is — the
	// identity correspondents address, since the protocol replies to
	// --from=$POGO_AGENT_NAME.
	Agent string
	// ScheduleID is the schedule's id, conventionally mail-check-<work-item-id>.
	// Its suffix is the ABANDONED mailbox candidate: it is the string the
	// pre-mg-aa96 template put in the message body.
	ScheduleID string
	// Polled is EVERY mailbox the schedule's message sends the agent to, as
	// parsed by scheduler.MailCheckMailboxes. Empty means the message names no
	// mailbox, in which case the agent reads its own name.
	//
	// It is a list because since mg-4f8c a mail-check names both the agent name
	// and the work-item box: mg mailboxes have no registration, so mail is in
	// whichever box the sender typed. A sweep that assumed one polled box would
	// report the second one as stranded on every correctly-configured polecat —
	// a false alarm on the healthy majority, which is the reliable way to get a
	// report ignored.
	Polled []string
}

// polledMailboxes is every box this check actually opens, canonically. A
// message naming none means the agent reads its own name.
func (c MailCheck) polledMailboxes() []string {
	var out []string
	for _, p := range c.Polled {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, scheduler.CanonicalMailbox(p))
	}
	if len(out) == 0 {
		return []string{scheduler.CanonicalMailbox(c.Agent)}
	}
	return out
}

// polls reports whether this check opens the named (already canonical) box.
func (c MailCheck) polls(box string) bool {
	for _, p := range c.polledMailboxes() {
		if p == box {
			return true
		}
	}
	return false
}

// shadowMailbox is the mailbox this agent's mail-check WOULD have read under the
// pre-mg-aa96 work-item derivation: the schedule id's suffix. Empty when the id
// carries no suffix, or when the box is one the agent already reads — which
// covers the healthy case, the historically-agreeing case (agent name is the
// work item id minus "mg-"), and, since mg-4f8c, the normal case where the
// mail-check reads both boxes deliberately.
func (c MailCheck) shadowMailbox() string {
	suffix := strings.TrimPrefix(c.ScheduleID, scheduler.MailCheckIDPrefix)
	if suffix == "" || suffix == c.ScheduleID {
		return ""
	}
	shadow := scheduler.CanonicalMailbox(suffix)
	if shadow == "" || c.polls(shadow) {
		return ""
	}
	return shadow
}

// Finding is one mailbox holding unread mail that no live mail-check reads.
type Finding struct {
	// Mailbox is the abandoned box, canonically named.
	Mailbox string `json:"mailbox"`
	// Unread is what mg reports sitting in it.
	Unread int `json:"unread"`
	// Agent is who the mail was for, and Polls is every box that agent looks in
	// instead, comma-separated. Both are needed: the whole failure is that these
	// disagree, and after mg-4f8c a healthy mail-check reads more than one box —
	// so "which boxes DID it open?" is what makes the finding legible.
	Agent string `json:"agent"`
	Polls string `json:"polls"`
	// ScheduleID is the mail-check the shadow was derived from — the audit
	// trail for why this box is suspected at all.
	ScheduleID string `json:"schedule_id"`
	// Messages are the unread messages themselves, when they could be read.
	// A correction from a coordinator to a builder mid-flight is the traffic
	// most at risk here (it is sent off-cadence to an agent already working),
	// so the sender and subject are worth more than the count.
	Messages []Message `json:"messages,omitempty"`
	// ReadError, when non-empty, records why the messages could not be
	// enumerated. The finding still stands on mg's unread count — "there is
	// mail here and I could not open it" is a report, not a reason to go quiet.
	ReadError string `json:"read_error,omitempty"`
}

// Report is one sweep.
//
// Checked is load-bearing next to an empty Findings: "no mail-check has an
// abandoned mailbox" and "I judged nothing" are different statements, and a
// reader that renders them identically is reproducing the defect this package
// exists to catch.
type Report struct {
	Checked  int       `json:"checked"`
	Boxes    int       `json:"boxes"`
	Findings []Finding `json:"findings,omitempty"`
}

// Actionable reports whether the sweep found mail nobody will ever read.
func (r Report) Actionable() bool { return len(r.Findings) > 0 }

// Detect cross-references live mail-checks against the mailbox enumeration and
// returns every abandoned box that still holds unread mail.
//
// list reads the messages of one mailbox (`mg mail list <box> --json`); a nil
// list, or one that errors, degrades the finding to a count rather than
// suppressing it.
func Detect(checks []MailCheck, boxes []Mailbox, list func(mailbox string) ([]Message, error)) Report {
	unread := make(map[string]int, len(boxes))
	for _, b := range boxes {
		if !b.Exists {
			continue
		}
		unread[scheduler.CanonicalMailbox(b.Name)] = b.Unread
	}

	rep := Report{Checked: len(checks), Boxes: len(unread)}
	seen := make(map[string]bool, len(checks))
	for _, c := range checks {
		shadow := c.shadowMailbox()
		if shadow == "" || seen[shadow] {
			continue
		}
		n := unread[shadow]
		if n == 0 {
			// Either the box never existed or it is genuinely empty. Neither is
			// a stranded message, and reporting them would drown the one that
			// is — most polecats are in exactly this state most of the time.
			continue
		}
		seen[shadow] = true
		f := Finding{
			Mailbox:    shadow,
			Unread:     n,
			Agent:      c.Agent,
			Polls:      strings.Join(c.polledMailboxes(), ", "),
			ScheduleID: c.ScheduleID,
		}
		if list != nil {
			msgs, err := list(shadow)
			if err != nil {
				f.ReadError = err.Error()
			} else {
				f.Messages = msgs
			}
		}
		rep.Findings = append(rep.Findings, f)
	}
	sort.Slice(rep.Findings, func(i, j int) bool { return rep.Findings[i].Mailbox < rep.Findings[j].Mailbox })
	return rep
}

// ReadToken builds the AGENT/MSG-ID token `mg mail read` accepts.
//
// It exists because the two are NOT the same string: `mg mail list <box>
// --json` emits a bare id ("1785952504865455000.62267.5000"), and handing that
// to `mg mail read` fails with `expected AGENT/MSG-ID format` (verified against
// the live binary, 2026-08-05).
func ReadToken(mailbox, id string) string {
	if strings.Contains(id, "/") {
		return id
	}
	return mailbox + "/" + id
}

// ReadCommand is the command that actually opens a stranded message.
//
// --force is not optional here and is not a shortcut: mg refuses a cross-box
// read outright —
//
//	refusing to read aa96's mail as agent "waa96": reading marks the message
//	read and hides it from aa96's unread list. Re-run with --force if this
//	cross-box read is intentional
//
// — and nobody reading this report is the abandoned mailbox, because the
// abandoned mailbox belongs to no running agent. Both halves of that refusal
// are true and both are fine here: the box has no reader to hide anything from,
// and marking it read is what makes it stop being stranded. Printing the
// command WITHOUT --force would hand every reader an error instead of their
// message, which is how a report gets written off as broken.
//
// A report whose recovery command does not run is a report that gets ignored,
// and this one exists precisely because nobody was going to find the message on
// their own. Both defects in this line — the bare id, and the missing --force —
// were found by running the sweep against the live fleet and typing what it
// printed.
func ReadCommand(mailbox, id string) string {
	return "mg mail read " + ReadToken(mailbox, id) + " --force"
}

// Render formats the sweep for a terminal.
func (r Report) Render() string {
	var b strings.Builder
	if !r.Actionable() {
		if r.Checked == 0 {
			fmt.Fprintf(&b, "No mail-check schedules to judge — nothing was checked.\n")
			fmt.Fprintf(&b, "That is not an all-clear: with no schedules read, an abandoned mailbox is\n")
			fmt.Fprintf(&b, "invisible to this sweep the same way it is invisible to the agent.\n")
			return b.String()
		}
		fmt.Fprintf(&b, "✓ No stranded mail: %d mail-check(s) checked against %d mailbox(es).\n", r.Checked, r.Boxes)
		return b.String()
	}

	fmt.Fprintf(&b, "⚠ STRANDED MAIL: %d mailbox(es) hold unread mail that no live mail-check reads.\n", len(r.Findings))
	fmt.Fprintf(&b, "  (%d mail-check(s) checked against %d mailbox(es))\n\n", r.Checked, r.Boxes)
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "  %s — %d unread, for agent %s, which polls %s instead\n", f.Mailbox, f.Unread, f.Agent, f.Polls)
		fmt.Fprintf(&b, "    from schedule %s\n", f.ScheduleID)
		for _, m := range f.Messages {
			fmt.Fprintf(&b, "    · from %s: %s\n", m.From, m.Subject)
			fmt.Fprintf(&b, "        %s\n", ReadCommand(f.Mailbox, m.ID))
		}
		if f.ReadError != "" {
			fmt.Fprintf(&b, "    · could not enumerate the messages: %s\n", f.ReadError)
			fmt.Fprintf(&b, "        mg mail list %s\n", f.Mailbox)
		}
		fmt.Fprintln(&b)
	}
	b.WriteString("Corrections are the traffic most at risk: they are sent off-cadence to an agent\n")
	b.WriteString("already working, which is what a scheduled poll handles worst. Read these before\n")
	b.WriteString("assuming the agent they were sent to is working from current information.\n")
	b.WriteString("\nIf the intended recipient is still running, reading the message is only half the\n")
	b.WriteString("recovery — the SENDER must re-send it to the agent name. This report does not\n")
	b.WriteString("re-deliver: mg mail send would write a new message under a new From, and a\n")
	b.WriteString("correction whose provenance is a lie is worse than one that arrived late.\n")
	return b.String()
}
