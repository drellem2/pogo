// Package filernotify tells the agent that FILED a work item that the item has
// completed.
//
// # THE DEFECT (mg-f120)
//
// Trace a PM-commissioned work item from the moment its polecat finishes:
//
//	polecat finishes -> refinery merges       -> mails the COORDINATOR
//	                 -> pogod closes the item -> (nothing)
//	                 -> coordinator archives  -> (nothing)
//
// Every arrow lands somewhere other than the agent that commissioned the work.
// The filer learned only when the polecat volunteered a mail — which most of
// them do, which is why the hole stayed invisible. On 2026-08-12 pm-onethird
// commissioned mg-145f; it was dispatched, completed, merged and archived, and
// pm-onethird found out by POLLING. Its own description of the shape is the
// reason this package exists: "an instrument whose failure mode is silence,
// where the absence erases its own evidence. A dispatch loop that relies on the
// worker volunteering the report has no detector for a worker that does not."
//
// The rejected alternative was the coordinator mailing the creator when it
// archives. It is free and needs no code, and it is what the coordinator does by
// hand until this ships — but it is a coordinator REMEMBERING, which is the
// class of control this fleet has repeatedly measured to be insufficient. So the
// notification is issued by the daemon, at the close, from the item's own
// recorded Creator.
//
// # THE REMEDY IS THE SAME KIND OF ARTIFACT AS THE DEFECT
//
// A notifier whose own failure is silent would reproduce, one layer up, exactly
// the defect it closes. Three things follow from that and none of them is
// optional:
//
//   - Every outcome is recorded, including the ones that send nothing. A skip
//     ("the filer is the worker", "the coordinator already got the refinery's
//     merge mail") emits the same kind of record as a send, so "the notifier
//     decided not to" and "the notifier never ran" are distinguishable.
//   - A send that FAILS is never recorded as handled, so the next observation of
//     the same completion retries it.
//   - A creator that cannot be READ is escalated to the coordinator rather than
//     dropped. An unreadable item is not evidence that nobody is waiting.
//
// # WHO GETS THE MAIL
//
// The creator field holds an agent name. Over the live store on 2026-08-12, 315
// of ~350 completed items named a crew agent (pm-onethird, mayor, pm-pogo,
// pm-riemann, doctor, architect) and about 20 named a POLECAT — an agent that
// is, by the time its item completes, almost always gone. A polecat's mailbox
// outlives the polecat, so mailing it is not an error that anything reports; it
// is a dead drop, which is the defect again. So the recipient is chosen by
// asking the registry whether the name still denotes an agent at all:
//
//	known to the registry (crew, running or parked; or a live polecat)
//	    -> mail the creator
//	not known (a polecat that no longer exists, a human, a typo)
//	    -> mail the COORDINATOR, naming the creator and saying why it was redirected
//
// Registry membership is the right test rather than "is a mailbox registered":
// mg keeps a mailbox forever once made, so mailbox existence answers "was this
// name ever used" and not "is anyone reading".
package filernotify

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

// Route says how the completion was observed. It changes one decision (see
// Notifier.Notify) and it is reported in the mail, because "your item merged"
// and "your item was closed by its worker without a merge" are different facts
// about what exists at the end.
type Route string

const (
	// RouteMerge is the close pogod performs itself when a branch merges: the
	// refinery reports the merge, pogod writes the result sidecar and calls
	// `mg done` on the worker's behalf.
	RouteMerge Route = "merge"
	// RouteSelfClose is the close the WORKER performs — `mg done` in its own
	// process, against the macguffin store, with pogod nowhere in the call path.
	// It is the ordinary end of a triage, audit or investigation item, whose
	// deliverable is not a branch. pogod OBSERVES it rather than performing it,
	// which is why the notification can lag the close by a poll interval.
	RouteSelfClose Route = "self-close"
)

// Completion is one finished work item, as the observer that saw it finish
// knows it.
type Completion struct {
	// ItemID is the work item that closed. Required; everything else is
	// best-effort context.
	ItemID string
	Route  Route
	// Worker is the agent that did the work, when one is known. It is compared
	// against the creator: a filer that is also the worker already knows.
	Worker string
	// Branch and MergedSHA describe what landed, on the merge route.
	Branch    string
	MergedSHA string
	// Result is a FALLBACK copy of the result sidecar, used only when the store
	// yields none. The authoritative copy is always the file mg wrote, read at
	// notify time — see Notify for why the reading, not the caller's copy, is
	// what the dedup key is built from.
	Result string
}

// Outcome is what Notify did, returned so a caller (and a test) can assert on
// the decision rather than on the mail. Exactly one of Sent/Skipped/Err is the
// interesting field; Skipped carries the reason in words.
type Outcome struct {
	// To is the mailbox that was written, empty when nothing was sent.
	To string
	// Redirected is true when To is the coordinator standing in for a creator
	// that no longer exists (or could not be read).
	Redirected bool
	// Creator is the item's recorded filer, empty when it had none or could not
	// be read.
	Creator string
	// Skipped names the reason nothing was sent, in words, and is empty when
	// something was. It is never empty at the same time as an error.
	Skipped string
	Err     error
}

// Sent reports whether a mail was written.
func (o Outcome) Sent() bool { return o.To != "" && o.Err == nil }

// Notifier mails the filer of a completed work item. The zero value does
// nothing useful; use New.
type Notifier struct {
	// mail sends durable mail (client.SendMGMail in production).
	mail func(to, from, subject, body string) error
	// filing returns an item's creator and title (client.MGWorkItemFiling).
	filing func(id string) (creator, title string, err error)
	// result returns an item's result sidecar verbatim, "" when it has none
	// (workitem.ReadResult). Optional: the merge route supplies the sidecar it
	// just wrote, so this is only consulted when the caller has none.
	result func(id string) (string, error)
	// known reports whether the registry still denotes an agent by this name.
	known func(name string) bool
	// record notes an outcome somewhere durable (an events.Emit in production).
	// Optional; a nil recorder means the outcome reaches the log only.
	record func(c Completion, o Outcome)

	coordinator string

	mu   sync.Mutex
	sent map[string]bool
}

// New builds a Notifier. mail, filing and known are required; a nil one makes
// Notify a no-op that says so, rather than a panic.
func New(coordinator string,
	mail func(to, from, subject, body string) error,
	filing func(id string) (string, string, error),
	result func(id string) (string, error),
	known func(name string) bool,
) *Notifier {
	return &Notifier{
		mail:        mail,
		filing:      filing,
		result:      result,
		known:       known,
		coordinator: coordinator,
		sent:        map[string]bool{},
	}
}

// SetRecorder installs the durable-record sink. Separate from New so that
// wiring it is a visible line at the callsite rather than a fifth positional
// argument nobody reads.
func (n *Notifier) SetRecorder(fn func(c Completion, o Outcome)) {
	if n == nil {
		return
	}
	n.record = fn
}

// mailFrom is the sender name on every notification. It is pogod's own name and
// not the refinery's: the refinery's mail is about a MERGE, this is about a
// work item CLOSING, and the two are neither the same event nor the same set of
// items.
const mailFrom = "pogod"

// Notify mails the filer of c's work item, once per distinct completion.
//
// It is safe to call from more than one observer — the merge close and the
// done-reaper's observation both reach it — and it is safe to call repeatedly.
//
// # THE DEDUP KEY IS THE ITEM PLUS ITS RESULT SIDECAR, AS READ FROM THE STORE
//
// Not the bare item id: an item that is reopened after a failed merge and closed
// again is a SECOND completion, and a ledger keyed on the id alone would swallow
// it — this ticket's own defect wearing the fix's clothes.
//
// Not the caller's copy of the sidecar either, and not the route. The two
// observers see the same close from different angles: the merge path performs
// it and holds the JSON it wrote, while the done-reaper only notices afterwards
// that the item is terminal and knows neither branch nor SHA. Keying on anything
// either observer holds privately would let the same completion be reported
// twice. Reading the sidecar from the store gives both of them the same string
// for the same close and a different one for the next.
//
// The ledger is in memory. It has to bound repeat notifications within a
// daemon's life, and it does; it deliberately does not try to bound them across
// restarts, because the events it guards are not replayed after one — a merge
// fires its callback once, and the done-reaper's observation ends when the
// polecat it watches is reaped. A duplicate mail after a restart would be noise;
// a missing one is the defect.
func (n *Notifier) Notify(c Completion) Outcome {
	if n == nil || n.mail == nil || n.filing == nil {
		return Outcome{Skipped: "notifier not wired"}
	}
	if strings.TrimSpace(c.ItemID) == "" {
		return Outcome{Skipped: "no work item id"}
	}
	// Resolved once, before any decision: it is both the dedup fingerprint and
	// the most useful line in the mail, and the two must be the same reading.
	res := n.resolveResult(c)
	if n.alreadySent(c, res) {
		return Outcome{Skipped: "already notified for this completion"}
	}

	creator, title, err := n.filing(c.ItemID)
	if err != nil {
		// CANNOT TELL WHO FILED IT is not the same as NOBODY FILED IT, and the
		// difference is the whole reason this package exists. Escalate to the
		// coordinator, which is the actor that can go and look.
		return n.send(c, res, n.coordinator, true, "",
			fmt.Sprintf("COMPLETED: %s — could not read who filed it", c.ItemID),
			fmt.Sprintf("Work item %s has completed, and pogod could not read its creator to tell them:\n\n  %v\n\n"+
				"Nothing else in the completion path notifies a filer (mg-f120), so if this item was commissioned, "+
				"its commissioner has not been told. Check `mg show %s` for the Creator and pass the result on.\n\n%s",
				c.ItemID, err, c.ItemID, detail(c, res)))
	}
	if creator == "" {
		// An item with no recorded creator has nobody waiting on it that we can
		// name. Recorded, not silent: a store that stopped recording creators
		// would otherwise turn this whole package off without a word.
		return n.finish(c, res, Outcome{Skipped: "work item records no creator"})
	}
	if c.Worker != "" && strings.EqualFold(creator, c.Worker) {
		return n.finish(c, res, Outcome{Creator: creator, Skipped: "the filer is the worker — it already knows"})
	}
	if c.Route == RouteMerge && strings.EqualFold(creator, n.coordinator) {
		// The refinery already mails the coordinator on every merge, with the
		// branch, the target and the merged SHA. A second mail saying the same
		// thing about the same event teaches the coordinator to skim both.
		//
		// Scoped to the merge route on purpose: a coordinator-filed item that
		// closes WITHOUT a merge gets no refinery mail at all, and is exactly
		// as silent as any other filer's would be.
		return n.finish(c, res, Outcome{Creator: creator, Skipped: "coordinator filed it and the refinery already mailed it this merge"})
	}

	to, redirected := creator, false
	if n.known != nil && !n.known(creator) {
		// The polecat-that-no-longer-exists rule. Its mailbox still exists —
		// mg never removes one — so a send there would report Delivered and be
		// read by nobody. Redirect, and say in the mail who it was for.
		to, redirected = n.coordinator, true
	}

	subject := fmt.Sprintf("COMPLETED: %s — %s", c.ItemID, oneLine(title))
	if redirected {
		subject = fmt.Sprintf("COMPLETED: %s (filed by %s, who is gone) — %s", c.ItemID, creator, oneLine(title))
	}
	return n.send(c, res, to, redirected, creator, subject, body(c, res, creator, title, redirected))
}

// resolveResult reads the item's result sidecar from the store, falling back to
// the caller's copy when the store yields nothing. The error is folded into the
// returned string as a legible statement rather than returned separately: a
// sidecar that could not be READ and one that does not EXIST are different
// facts, and both of them belong in the mail.
func (n *Notifier) resolveResult(c Completion) string {
	if n.result != nil {
		got, err := n.result(c.ItemID)
		if err != nil {
			return ""
		}
		if strings.TrimSpace(got) != "" {
			return strings.TrimSpace(got)
		}
	}
	return strings.TrimSpace(c.Result)
}

// body is the notification a filer reads.
func body(c Completion, res, creator, title string, redirected bool) string {
	var b strings.Builder
	if redirected {
		fmt.Fprintf(&b, "This is %s's mail, redirected to you: %s is not a live agent, so it has no reader.\n"+
			"You are the coordinator; pass it on if the work has a waiting consumer.\n\n", creator, creator)
	} else {
		fmt.Fprintf(&b, "The work item you filed has completed.\n\n")
	}
	fmt.Fprintf(&b, "Item:   %s\n", c.ItemID)
	if title != "" {
		fmt.Fprintf(&b, "Title:  %s\n", oneLine(title))
	}
	fmt.Fprintf(&b, "Filer:  %s\n", creator)
	switch c.Route {
	case RouteMerge:
		fmt.Fprintf(&b, "Closed: its branch merged")
		if c.Branch != "" {
			fmt.Fprintf(&b, " (%s)", c.Branch)
		}
		if c.MergedSHA != "" {
			fmt.Fprintf(&b, " as %s", c.MergedSHA)
		}
		b.WriteString("\n")
	case RouteSelfClose:
		fmt.Fprintf(&b, "Closed: by its worker calling `mg done` — no merge; the deliverable is the record, not a branch\n")
	}
	b.WriteString("\n")
	b.WriteString(verdict(res))
	b.WriteString("\n\nYou are getting this because you are the item's Creator. Nothing else in the completion\n" +
		"path tells a filer: the refinery mails the coordinator, pogod closes the item, and the\n" +
		"coordinator archives it (mg-f120). Read the full record with `mg show " + c.ItemID + "`.\n")
	return b.String()
}

// verdict renders the worker's own result, or states plainly that there is
// none. "No verdict was recorded" is a fact worth reading — it is the shape a
// worker leaves when it skips `--verdict-file` at submit (mg-dfea) — and it must
// not be indistinguishable from a notifier that failed to look.
func verdict(res string) string {
	if res == "" {
		return "Verdict: NONE RECORDED — the item closed with no readable result sidecar, so there is\n" +
			"         no statement of how the work came out beyond the item's own history (mg-dfea)."
	}
	return "Verdict (the worker's result sidecar, verbatim):\n" + indent(res, "  ")
}

// detail is the short context block used by the escalation paths, where there
// is no creator to address and the coordinator needs the facts anyway.
func detail(c Completion, res string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Route:  %s\n", c.Route)
	if c.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", c.Branch)
	}
	if c.MergedSHA != "" {
		fmt.Fprintf(&b, "SHA:    %s\n", c.MergedSHA)
	}
	b.WriteString("\n" + verdict(res))
	return b.String()
}

// send writes the mail and records the outcome. A FAILED send is not marked as
// notified, so a later observation of the same completion tries again.
func (n *Notifier) send(c Completion, res, to string, redirected bool, creator, subject, body string) Outcome {
	o := Outcome{To: to, Redirected: redirected, Creator: creator}
	if to == "" {
		o.Skipped = "no recipient (no coordinator configured)"
		return n.finish(c, res, o)
	}
	if err := n.mail(to, mailFrom, subject, body); err != nil {
		o.Err = fmt.Errorf("mail %s about %s: %w", to, c.ItemID, err)
		log.Printf("filernotify: FAILED to tell %s that %s completed: %v — the filer has not been told and nothing else "+
			"will tell it (mg-f120)", to, c.ItemID, o.Err)
		// LAST RESORT: hand it to the coordinator rather than let the report
		// die here. The commonest way this arm is reached is a recipient mg has
		// no mailbox for — `mg mail send` exits 3 with no_such_mailbox on an
		// unregistered name (mg-d639), and a name the registry knows is not
		// automatically a name mg has been told about. A refusal that ends the
		// attempt would be this ticket's defect exactly: a report lost, once,
		// with nothing but a stderr line saying so.
		//
		// Deliberately NOT marked as handled — see finish. If a later
		// observation of the same close comes round, it tries the real filer
		// again, because a redirect is a worse outcome than a direct hit and
		// should not be allowed to stand in for one permanently.
		if to != n.coordinator && n.coordinator != "" {
			relay := fmt.Sprintf("This mail was addressed to %s and could not be delivered:\n\n  %v\n\n"+
				"You are the coordinator; the report below has reached nobody else.\n\n%s", to, err, body)
			if rerr := n.mail(n.coordinator, mailFrom, "UNDELIVERABLE "+subject, relay); rerr != nil {
				log.Printf("filernotify: the fallback to %s also failed for %s: %v — this completion has been "+
					"reported to nobody (mg-f120)", n.coordinator, c.ItemID, rerr)
			} else {
				o.Redirected = true
				log.Printf("filernotify: relayed %s's completion notice to %s after %s refused it (mg-f120)",
					c.ItemID, n.coordinator, to)
			}
		}
		if n.record != nil {
			n.record(c, o)
		}
		return o
	}
	log.Printf("filernotify: told %s that work item %s completed (route=%s, redirected=%t) (mg-f120)",
		to, c.ItemID, c.Route, redirected)
	return n.finish(c, res, o)
}

// finish marks the completion notified and records the outcome. Called for
// sends AND for deliberate skips: a skip that leaves no record is
// indistinguishable from a notifier that never ran, which is this ticket's
// defect reproduced inside its own fix.
func (n *Notifier) finish(c Completion, res string, o Outcome) Outcome {
	n.mu.Lock()
	if n.sent == nil {
		n.sent = map[string]bool{}
	}
	n.sent[dedupKey(c, res)] = true
	n.mu.Unlock()
	if o.Skipped != "" {
		log.Printf("filernotify: sent nothing about work item %s — %s (mg-f120)", c.ItemID, o.Skipped)
	}
	if n.record != nil {
		n.record(c, o)
	}
	return o
}

func (n *Notifier) alreadySent(c Completion, res string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sent[dedupKey(c, res)]
}

// dedupKey identifies one COMPLETION, not one item. See Notify for why the
// distinction matters and why the fingerprint is the sidecar as read from the
// store rather than anything either observer holds privately.
func dedupKey(c Completion, res string) string {
	return c.ItemID + "\x00" + res
}

// oneLine flattens a title for a subject line. Work item titles in this fleet
// run long and occasionally wrap; a newline in a `mg mail send --subject`
// argument is not something the reader ever benefits from.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 120
	// Truncated by RUNE, not by byte: this fleet's titles carry em dashes and
	// arrows routinely, and a byte slice through one produces a subject line
	// ending in a replacement character.
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
