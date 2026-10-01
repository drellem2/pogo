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
//
// # Two populations (mg-aa74)
//
// The sweep started as "boxes a mail-check SHOULD have read": it enumerated
// mail-check schedules and looked at the box each one abandoned. Since mg-aa74
// (mg-5496 phase 2) polecats have no mail-check schedule — wakewatch sends a
// pointer nudge when their mail arrives — so enumerating schedules alone would
// silently stop looking at every polecat box, which is the exact silence this
// package exists to break. So polecats are enumerated by LIVE POLECAT instead:
// every box mailbox.PolecatBoxes names for a running polecat with no mail-check
// of its own, holding unread mail older than a grace period, is a finding. The
// grace is what keeps a pointer still in flight (measured 10–67s after the
// send over the phase-1 shadow window) from reading as stranded; past it, the
// most likely story is a pointer that failed, and this sweep is the only thing
// left that would notice.
package strandedmail

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/mailbox"
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

// Finding kinds. They need different recoveries, so the report names which one
// it is rather than leaving the reader to infer it from which fields are set.
const (
	// KindAbandoned is a box a mail-check WOULD have read under the
	// pre-mg-aa96 derivation and no longer does. Nobody will ever read it, so
	// reading it (with --force) is the recovery.
	KindAbandoned = "abandoned_box"
	// KindUnconsumed is a live polecat's own box holding mail older than the
	// grace (mg-aa74). The polecat is still the right reader — reading it on its
	// behalf would mark it read and hide it from the one agent it was for — so
	// the recovery is to point the polecat at it.
	KindUnconsumed = "unconsumed_polecat_mail"
)

// Finding is one mailbox holding unread mail that no live mail-check reads.
type Finding struct {
	// Kind is KindAbandoned or KindUnconsumed.
	Kind string `json:"kind"`
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
	// trail for why this box is suspected at all. Empty for KindUnconsumed: a
	// polecat finding is suspected BECAUSE no schedule reads it.
	ScheduleID string `json:"schedule_id"`
	// WorkItemID is the polecat's work item, for KindUnconsumed.
	WorkItemID string `json:"work_item_id,omitempty"`
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
	Checked int `json:"checked"`
	// Polecats is how many live polecats with no mail-check of their own had
	// their boxes judged (mg-aa74). It is the polecat half of Checked: a sweep
	// with Checked == 0 and Polecats == 0 looked at nothing.
	Polecats int `json:"polecats"`
	// Grace is the age below which unread polecat mail is not judged, as a
	// duration string, so a reader can tell "clean" from "too young to judge".
	Grace    string    `json:"grace,omitempty"`
	Boxes    int       `json:"boxes"`
	Findings []Finding `json:"findings,omitempty"`
}

// Actionable reports whether the sweep found mail nobody will ever read.
func (r Report) Actionable() bool { return len(r.Findings) > 0 }

// Polecat is one LIVE polecat, reduced to the two identities its mail can be
// addressed to. Its boxes are mailbox.PolecatBoxes(Agent, WorkItemID) — the same
// set spawn provisions, so a box is judged here exactly when it was registered.
type Polecat struct {
	Agent      string
	WorkItemID string
}

// DefaultGrace is how old unread mail in a live polecat's box must be before the
// sweep calls it unconsumed. It matches wakewatch's re-nudge threshold
// (RenudgeAfter, 15m): younger than that, wakewatch is still working on it, and
// a report would only duplicate a pointer already in flight.
const DefaultGrace = 15 * time.Minute

// Sweep is one stranded-mail check's inputs. Detect is Sweep with no polecats,
// kept for callers (and tests) that only judge schedules.
type Sweep struct {
	// Checks are the live mail-check schedules.
	Checks []MailCheck
	// Polecats are the RUNNING polecats. Any that also has a mail-check in
	// Checks is skipped here — its schedule reads its boxes, and the shadow
	// check above already covers what that schedule abandons.
	Polecats []Polecat
	// Boxes is the mailbox enumeration (`mg mail list --json`).
	Boxes []Mailbox
	// List reads one box's messages; nil, or an error, degrades a finding to a
	// count rather than suppressing it.
	List func(mailbox string) ([]Message, error)
	// Now and Grace age polecat mail. A zero Now means time.Now(); a zero Grace
	// means DefaultGrace.
	Now   time.Time
	Grace time.Duration
}

// Detect cross-references live mail-checks against the mailbox enumeration and
// returns every abandoned box that still holds unread mail.
//
// list reads the messages of one mailbox (`mg mail list <box> --json`); a nil
// list, or one that errors, degrades the finding to a count rather than
// suppressing it.
func Detect(checks []MailCheck, boxes []Mailbox, list func(mailbox string) ([]Message, error)) Report {
	return Sweep{Checks: checks, Boxes: boxes, List: list}.Run()
}

// Run performs the sweep: abandoned boxes from the schedules, then unconsumed
// mail in live polecats' own boxes.
func (sw Sweep) Run() Report {
	checks, list := sw.Checks, sw.List
	unread := make(map[string]int, len(sw.Boxes))
	for _, b := range sw.Boxes {
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
			Kind:       KindAbandoned,
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

	rep.Findings = append(rep.Findings, sw.polecatFindings(&rep, unread, seen)...)
	sort.Slice(rep.Findings, func(i, j int) bool { return rep.Findings[i].Mailbox < rep.Findings[j].Mailbox })
	return rep
}

// polecatFindings judges live polecats' own boxes (mg-aa74). A polecat with a
// mail-check of its own is left to that schedule; every other running polecat
// is counted in rep.Polecats whether or not its boxes hold anything, because
// "judged and clean" is the claim the count backs.
func (sw Sweep) polecatFindings(rep *Report, unread map[string]int, seen map[string]bool) []Finding {
	now := sw.Now
	if now.IsZero() {
		now = time.Now()
	}
	grace := sw.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	rep.Grace = grace.String()

	scheduled := make(map[string]bool, len(sw.Checks))
	for _, c := range sw.Checks {
		scheduled[scheduler.CanonicalMailbox(c.Agent)] = true
		for _, p := range c.polledMailboxes() {
			scheduled[p] = true
		}
	}

	var out []Finding
	for _, p := range sw.Polecats {
		if strings.TrimSpace(p.Agent) == "" || scheduled[scheduler.CanonicalMailbox(p.Agent)] {
			continue
		}
		rep.Polecats++
		for _, raw := range mailbox.PolecatBoxes(p.Agent, p.WorkItemID) {
			box := scheduler.CanonicalMailbox(raw)
			if seen[box] || scheduled[box] {
				continue
			}
			n := unread[box]
			if n == 0 {
				continue
			}
			f := Finding{
				Kind:       KindUnconsumed,
				Mailbox:    box,
				Unread:     n,
				Agent:      p.Agent,
				WorkItemID: p.WorkItemID,
			}
			if sw.List == nil {
				// No way to age the mail, so no way to tell an in-flight pointer
				// from a failed one. Report on the count: a false positive here
				// costs a glance, a false negative is the failure this exists for.
				f.ReadError = "messages not enumerated, so their age is unknown"
			} else if msgs, err := sw.List(box); err != nil {
				f.ReadError = err.Error()
			} else {
				stale := staleUnread(msgs, now, grace)
				if len(stale) == 0 {
					// Every unread message is younger than the grace: wakewatch
					// is still on it. Not a finding.
					continue
				}
				f.Messages = stale
			}
			seen[box] = true
			out = append(out, f)
		}
	}
	return out
}

// staleUnread is the unread messages at least grace old. A message whose date
// will not parse is kept: an age we cannot read is not evidence of youth.
func staleUnread(msgs []Message, now time.Time, grace time.Duration) []Message {
	var out []Message
	for _, m := range msgs {
		if m.Read {
			continue
		}
		at, err := time.Parse(time.RFC3339, m.Date)
		if err != nil || now.Sub(at) >= grace {
			out = append(out, m)
		}
	}
	return out
}

// NudgeCommand is the recovery for KindUnconsumed: point the live polecat at
// its own mail, the way wakewatch would have. It is deliberately NOT
// ReadCommand — reading a live polecat's mail on its behalf marks it read and
// hides it from the one agent it was addressed to.
func NudgeCommand(agentName, box string) string {
	return fmt.Sprintf("pogo nudge %s \"mail waiting — mg mail list %s\"", agentName, box)
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
		if r.Checked == 0 && r.Polecats == 0 {
			fmt.Fprintf(&b, "No mail-check schedules and no live polecats to judge — nothing was checked.\n")
			fmt.Fprintf(&b, "That is not an all-clear: with nothing read, an abandoned mailbox is\n")
			fmt.Fprintf(&b, "invisible to this sweep the same way it is invisible to the agent.\n")
			return b.String()
		}
		fmt.Fprintf(&b, "✓ No stranded mail: %d mail-check(s) and %d live polecat(s) checked against %d mailbox(es).\n",
			r.Checked, r.Polecats, r.Boxes)
		if r.Polecats > 0 && r.Grace != "" {
			fmt.Fprintf(&b, "  (polecat mail younger than %s is not judged: wakewatch is still pointing at it)\n", r.Grace)
		}
		return b.String()
	}

	fmt.Fprintf(&b, "⚠ STRANDED MAIL: %d mailbox(es) hold unread mail that nothing is going to read.\n", len(r.Findings))
	fmt.Fprintf(&b, "  (%d mail-check(s) and %d live polecat(s) checked against %d mailbox(es))\n\n", r.Checked, r.Polecats, r.Boxes)
	abandoned, unconsumed := 0, 0
	for _, f := range r.Findings {
		if f.Kind == KindUnconsumed {
			unconsumed++
			fmt.Fprintf(&b, "  %s — %d unread, for LIVE polecat %s, with no mail-check; older than %s, so its wakewatch pointer did not get it read\n",
				f.Mailbox, f.Unread, f.Agent, r.Grace)
			if f.WorkItemID != "" {
				fmt.Fprintf(&b, "    working %s\n", f.WorkItemID)
			}
			for _, m := range f.Messages {
				fmt.Fprintf(&b, "    · from %s: %s (%s)\n", m.From, m.Subject, m.Date)
			}
			if f.ReadError != "" {
				fmt.Fprintf(&b, "    · could not enumerate the messages: %s\n", f.ReadError)
			}
			fmt.Fprintf(&b, "        %s\n", NudgeCommand(f.Agent, f.Mailbox))
			fmt.Fprintln(&b)
			continue
		}
		abandoned++
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
	if unconsumed > 0 {
		b.WriteString("A live polecat's own mail is NOT read on its behalf — that would mark it read and\n")
		b.WriteString("hide it from the agent it was for. Point the polecat at it (the nudge above), and if\n")
		b.WriteString("pointers keep failing for it, tell the coordinator: since mg-aa74 a pointer is a\n")
		b.WriteString("polecat's only wake.\n")
		if abandoned > 0 {
			b.WriteString("\n")
		}
	}
	if abandoned > 0 {
		b.WriteString("Corrections are the traffic most at risk: they are sent off-cadence to an agent\n")
		b.WriteString("already working, which is what a scheduled poll handles worst. Read these before\n")
		b.WriteString("assuming the agent they were sent to is working from current information.\n")
		b.WriteString("\nIf the intended recipient is still running, reading the message is only half the\n")
		b.WriteString("recovery — the SENDER must re-send it to the agent name. This report does not\n")
		b.WriteString("re-deliver: mg mail send would write a new message under a new From, and a\n")
		b.WriteString("correction whose provenance is a lie is worse than one that arrived late.\n")
	}
	return b.String()
}
