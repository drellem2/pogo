package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// The addressee for stranded pushed work (mg-be37).
//
// THE DEFECT WAS DELIVERY, NOT DETECTION, and that correction is the whole
// reason this file exists rather than a second detector.
//
// reportStrandedWorkOnRelease shipped 2026-08-05 in e354eba and works. On the
// night of 2026-08-09 five polecats were released leaving pushed, unmerged work
// behind, and it fired on all five — 1:1, no false positives in the entire
// events.log, each payload naming the branch, the commit, the unmerged count and
// the exact `pogo refinery submit` line. Nobody saw any of them. Its two outputs
// were pogod's log and ~/.pogo/events.log, and `work_item_stranded_push` appears
// in exactly one non-test place in this tree: the emit site. It runs inside
// pogod after the agent process is gone, so it reached no operator terminal and
// no agent inbox.
//
// The measured cost of that was the gap between detection and discovery:
// ~1h for polecat-qa564, 2.5h for polecat-q6c90, ~3h for polecat-q56ac. In each
// window the item read `available` on the board, priority-wake advertised it as
// unclaimed, and the action it recommended — dispatch — is the one that
// re-derives work that is already pushed. mg-9a19 lost 1026 lines exactly that
// way.
//
// So the fix is an addressee. At the moment it fires, the emit site already
// knows the work item, the repo, the branch and the remedy; this file mails
// them.
//
// WHY THE COORDINATOR AND NOT `human`. Resolving this needs someone who can
// resubmit a branch, decide whether an item should be reopened, and hold the
// do-not-dispatch instruction until it is. That is the mayor's job, and the
// mayor escalates to a human when it is not. Same routing as the orphaned-polecat
// alert next door, for the same reason.
//
// WHY THE REPO'S PM IS PROBED AND NOT ASSUMED. The ticket asks for the repo's PM
// as a second recipient, and pm-onethird is in fact the human attention that
// found two of the five by hand. But `pm-<repo>` is a GUESS: mg-f04b removed a
// literal `pm-pogo` default from this tree precisely because those names belong
// to one machine's fleet, and on any other install the notice went to a mailbox
// with no reader. Since mg-d639 an unknown recipient is refused rather than
// silently invented, so the guess can now be CHECKED — mailboxExists asks mg
// whether the box is real, and a repo whose PM does not exist simply gets the
// coordinator alone. A probed name is not an assumed one.

// StrandedAlert is one stranded-work finding with everything a reader needs to
// act, assembled at the moment the fact is established rather than reconstructed
// later from a log.
type StrandedAlert struct {
	// Polecat is the agent name whose branch this is.
	Polecat string
	// WorkItemID is the item that will go back to (or is sitting in) the pool.
	WorkItemID string
	// Repo is the source repository the branch lives in.
	Repo string
	// Reason is the release reason, or the sweep's description of why this
	// polecat was never reported.
	Reason string
	// Route says which of the two detectors found it. See RouteRelease and
	// RouteRestartSweep — the remedy is the same, but the reader's confidence
	// that the polecat is DONE with the branch is not.
	Route string
	// StillAlive is true when the sweep found the polecat's process still
	// running. Only the restart sweep can produce this, and it changes the
	// advice: the work is real but the branch may still grow.
	StillAlive bool
	// ItemStatus is the work item's status as mg reports it, or "" when it could
	// not be read. It governs one paragraph of the body and nothing else — see
	// Message. Best-effort on purpose: the alert must not depend on mg being
	// reachable, and an unread status simply falls back to the wording that
	// shipped before it existed.
	ItemStatus string
	// Finding is the branch verdict. Its Summary() is the remedy sentence.
	Finding strandedwork.Finding
	// Presence is the content second opinion's measurement, and SecondOpinion is
	// its rendered sentence — empty when there was nothing to measure. Neither
	// may change what this alert SAYS TO DO: see strandedwork.Corroborate for why
	// a high presence downgrades a row to check-by-hand and never to clean.
	Presence      strandedwork.Presence
	SecondOpinion string
}

// The two routes a stranded-work alert can arrive by.
const (
	// RouteRelease is the original gate: pogod released a polecat's claim and
	// the branch had unmerged work. The polecat is gone, so the branch is final.
	RouteRelease = "release"

	// RouteRestartSweep is the backstop: this polecat was never reachable by the
	// gate at all, because it outlived the pogod that spawned it. See
	// strandedsweep.go.
	RouteRestartSweep = "restart_sweep"
)

// strandedAlertMail is the mail sink, swappable for tests. Production sends via
// `mg mail send`; tests capture.
var strandedAlertMail = defaultStrandedAlertMail

// SetStrandedAlertMail replaces the stranded-work mail sink. Exported for tests
// and for a daemon that wants to route these somewhere else; passing nil
// restores the default.
func SetStrandedAlertMail(f func(StrandedAlert)) {
	if f == nil {
		f = defaultStrandedAlertMail
	}
	strandedAlertMail = f
}

// defaultStrandedAlertMail delivers one alert to every recipient.
//
// BEST-EFFORT AND DELIBERATELY SO. The work_item_stranded_push event is already
// on the spine by the time this runs, so a machine with no mg on PATH loses the
// improvement and not the record. What it must never do is take the daemon down
// with it, or block a release: a polecat wedged by an outage is stopped while
// the outage is still on, and that is exactly when a mail send is slowest.
func defaultStrandedAlertMail(a StrandedAlert) {
	recipients := strandedRecipients(a.Repo)
	subject, body := a.Message()
	for _, to := range recipients {
		cmd := exec.Command("mg", "mail", "send", to,
			"--from", "pogod", "--subject", subject, "--body", body)
		out, err := cmd.CombinedOutput()
		if err == nil {
			continue
		}
		log.Printf("strandedwork: cannot mail %s about %s's stranded branch %s (%v): %s",
			to, a.Polecat, a.Finding.Branch, err, strings.TrimSpace(string(out)))
		emitStrandedMailUndelivered(a, to, err)
	}
}

// emitStrandedMailUndelivered puts a FAILED delivery on the event spine.
//
// This is the part worth copying from the worktree-preservation path, which is
// the one notifier in this daemon validated end to end (22 delivered notices
// over three days). It logs on a send failure AND emits, and the emit is what
// makes the delivery layer itself observable: without it, "the alert was never
// generated" and "the alert was generated and the mail bounced" are the same
// silence — which is a small copy of the exact defect this ticket exists to
// close, one layer further down. A report with no reader is what we are fixing;
// a delivery with no record would be the same mistake wearing a different hat.
func emitStrandedMailUndelivered(a StrandedAlert, to string, cause error) {
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_push_undelivered",
		Agent:      "pogod",
		WorkItemID: a.WorkItemID,
		Repo:       a.Repo,
		Details: map[string]any{
			"recipient": to,
			"polecat":   a.Polecat,
			"branch":    a.Finding.Branch,
			"route":     a.Route,
			"error":     cause.Error(),
			"summary":   a.Finding.Summary(),
		},
	})
}

// strandedRecipients returns who is told, in delivery order: the coordinator
// always, then the repo's PM when a mailbox of that name actually exists.
//
// The coordinator is unconditional. It is the one mailbox pogo guarantees, and
// leaving the list empty when a probe fails would rebuild this ticket's defect
// at the delivery layer — a detector whose addressee resolves to nobody.
func strandedRecipients(repo string) []string {
	out := []string{CoordinatorName()}
	if pm := repoPMCandidate(repo); pm != "" && pm != out[0] && mailboxExists(pm) {
		out = append(out, pm)
	}
	return out
}

// repoPMCandidate is the name a repo's PM would have if it has one:
// `pm-<repo directory name>`. It is a CANDIDATE and never a recipient on its
// own — strandedRecipients probes it before using it.
func repoPMCandidate(repo string) string {
	base := strings.TrimSpace(filepath.Base(strings.TrimRight(repo, "/")))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return "pm-" + strings.ToLower(base)
}

// mailboxExists asks mg whether a mailbox is real.
//
// IT ANSWERS FALSE ON EVERY FAILURE, and here that is the safe direction — the
// opposite of the polarity trap that runs through the rest of this ticket. A
// wrong false costs one PM a copy of a mail the coordinator already has; a wrong
// true costs a delivery refusal logged as an error on every stranded branch
// forever. The coordinator is not gated on this probe, so no failure of it can
// silence the alert.
func mailboxExists(name string) bool {
	out, err := exec.Command("mg", "mail", "list", name, "--json").Output()
	if err != nil {
		return false
	}
	var probe struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return false
	}
	return probe.Exists
}

// sendStrandedAlert fills in what the alert can only learn by asking, then
// delivers it. Both detectors go through here so the probe has one call site and
// the sink still receives a fully-populated alert for tests to inspect.
func sendStrandedAlert(a StrandedAlert) {
	if a.ItemStatus == "" {
		a.ItemStatus = workItemStatusProbe(a.WorkItemID)
	}
	strandedAlertMail(a)
}

// workItemStatusTimeout bounds the status probe. Short: it decides one paragraph
// of wording, and nothing about the alert depends on getting an answer.
const workItemStatusTimeout = 5 * time.Second

// workItemStatusProbe reads a work item's status, swappable for tests.
var workItemStatusProbe = mgWorkItemStatus

// SetWorkItemStatusProbe replaces the status probe used to word stranded-work
// alerts. Exported for tests; passing nil restores the default.
func SetWorkItemStatusProbe(f func(string) string) {
	if f == nil {
		f = mgWorkItemStatus
	}
	workItemStatusProbe = f
}

// mgWorkItemStatus asks mg for one work item's status, or returns "" when it
// cannot be read.
//
// IT ANSWERS "" ON EVERY FAILURE, and "" means "keep the wording that shipped
// before this probe existed". That is the safe direction here: a wrong "" costs
// one paragraph of over-specific advice on a closed item, while treating an
// unreadable store as evidence of a status would let a transient mg failure
// rewrite the alert's most emphatic sentence.
//
// STDOUT ONLY, via Output(). `mg show --json` writes advisories to stderr — a
// collision note for an id that also names an archived item, for instance — and
// merging the two streams makes the store's own answer unparseable for an item
// it answered perfectly (see internal/client's mgShowJSON, mg-aaf6).
func mgWorkItemStatus(id string) string {
	if id == "" {
		return ""
	}
	// Bounded, because this runs on the claim-release path. Everything else that
	// path shells out to is either bounded already (strandedwork.Fetch) or is the
	// alert itself; a status probe is the one call here that buys only wording,
	// so it is the last thing that should be able to hold a release open.
	ctx, cancel := context.WithTimeout(context.Background(), workItemStatusTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mg", "show", id, "--json").Output()
	if err != nil {
		return ""
	}
	var item struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return ""
	}
	return strings.TrimSpace(item.Status)
}

// itemIsClosed reports whether the alert's work item is in a status nothing will
// be dispatched at. "" — the status could not be read — is NOT closed, so an
// unreadable store leaves the wording exactly as it shipped.
func (a StrandedAlert) itemIsClosed() bool {
	switch strings.ToLower(a.ItemStatus) {
	case "done", "archived", "cancelled", "canceled":
		return true
	}
	return false
}

// Message renders the alert as (subject, body).
//
// THE BODY CARRIES THE REMEDY AND THE PROHIBITION TOGETHER. Naming only the
// problem is what this ticket's own history shows does not work: the state is
// invisible from both directions (the board says `available`, the repo holds
// finished work), so a reader who is told "there is stranded work" and not what
// to do about it resolves it with the move that is already being recommended to
// them by priority-wake. Both sentences must travel, and they must travel in the
// part that gets skimmed.
func (a StrandedAlert) Message() (subject, body string) {
	item := a.WorkItemID
	if item == "" {
		item = "(none recorded — the branch's commits named no work item)"
	}
	subject = fmt.Sprintf("[stranded-push] %s left pushed work behind on %s — do NOT dispatch at %s",
		a.Polecat, a.Finding.Branch, a.WorkItemID)
	switch {
	case a.WorkItemID == "":
		subject = fmt.Sprintf("[stranded-push] %s left pushed work behind on %s",
			a.Polecat, a.Finding.Branch)
	case a.itemIsClosed():
		// The prohibition has to be true in the half that travels furthest, and
		// "do NOT dispatch" is not true of an item nothing will be dispatched at.
		subject = fmt.Sprintf("[stranded-push] %s is %s but %s never merged — %s left pushed work behind",
			a.WorkItemID, a.ItemStatus, a.Finding.Branch, a.Polecat)
	}

	var b strings.Builder
	if a.itemIsClosed() {
		fmt.Fprintf(&b, "A polecat's branch carries work the target does not have, and the item it belongs to\n"+
			"was closed anyway.\n\n")
	} else {
		fmt.Fprintf(&b, "A polecat's branch carries work the target does not have, and the item it belongs to\n"+
			"is back in the pool describing itself as untouched.\n\n")
	}
	fmt.Fprintf(&b, "Polecat:    %s\n", a.Polecat)
	if a.ItemStatus != "" {
		fmt.Fprintf(&b, "Work item:  %s (status %s)\n", item, a.ItemStatus)
	} else {
		fmt.Fprintf(&b, "Work item:  %s\n", item)
	}
	fmt.Fprintf(&b, "Repo:       %s\n", a.Repo)
	fmt.Fprintf(&b, "Branch:     %s (ref %s, pushed=%t)\n", a.Finding.Branch, a.Finding.Ref, a.Finding.Pushed)
	fmt.Fprintf(&b, "Target:     %s\n", a.Finding.Target)
	fmt.Fprintf(&b, "Unmerged:   %d commit(s)", len(a.Finding.Unmerged))
	if len(a.Finding.Unmerged) > 0 {
		fmt.Fprintf(&b, ", oldest %s %q", shortSHA(a.Finding.Unmerged[0].SHA), a.Finding.Unmerged[0].Subject)
	}
	fmt.Fprintf(&b, "\nReason:     %s\n\n", a.Reason)

	// The second opinion goes ABOVE the remedy, because it is the one paragraph
	// that changes how the remedy should be read (mg-5ec6). The SUBJECT is left
	// alone on purpose: "do NOT dispatch" stays true in both worlds — if the
	// branch really did land, the item's work exists on the target and a worker
	// sent at it re-derives that instead — so hedging the half that travels
	// furthest would buy nothing and cost the prohibition.
	if a.SecondOpinion != "" {
		fmt.Fprintf(&b, "%s\n\n", wrapAt(a.SecondOpinion, 88))
	}

	fmt.Fprintf(&b, "WHAT TO DO — resubmit, do not dispatch:\n\n")
	fmt.Fprintf(&b, "    pogo refinery submit %s --repo=%s", a.Finding.Branch, a.Repo)
	if a.WorkItemID != "" {
		fmt.Fprintf(&b, " --author=%s", a.WorkItemID)
	}
	b.WriteString("\n\n")

	if a.WorkItemID != "" && a.itemIsClosed() {
		// The board paragraph below is FALSE for a closed item, and saying it
		// anyway is not a harmless overstatement: it tells the reader to expect a
		// dispatch that cannot happen, which invites them to treat the whole
		// notice as boilerplate. A done item is not a re-derivation risk; the
		// thing that is actually wrong is that its branch never landed (mg-1af2).
		fmt.Fprintf(&b, "THE ITEM IS ALREADY %s, SO THIS IS NOT A RE-DISPATCH RISK. Nothing will be\n"+
			"dispatched at %s and priority-wake will not advertise it. What IS wrong is that %s\n"+
			"was closed with work on its branch that never reached %s — either the branch still\n"+
			"needs submitting, or it was superseded and can be deleted. Establish which; a closed\n"+
			"item with unmerged commits is exactly the state nobody is looking for.\n\n",
			strings.ToUpper(a.ItemStatus), a.WorkItemID, a.WorkItemID, a.Finding.Target)
	} else if a.WorkItemID != "" {
		fmt.Fprintf(&b, "DO NOT DISPATCH A WORKER AT %s. The work already exists and is already pushed;\n"+
			"a worker sent at this item re-derives it from scratch and the pushed copy is what gets\n"+
			"thrown away. mg-9a19 lost 1026 lines exactly that way. The board shows the item as\n"+
			"available and priority-wake will advertise it as unclaimed and ready — that advice is\n"+
			"wrong for as long as this branch is unmerged, and nothing on the board says so.\n\n",
			a.WorkItemID)
	}

	if a.Finding.Disposition == strandedwork.DispositionPreRegistration {
		fmt.Fprintf(&b, "PRE-REGISTRATION COMMIT — READ BEFORE ANY RE-DISPATCH.\n%s\n\n", a.Finding.Summary())
	}

	if !a.Finding.Pushed {
		fmt.Fprintf(&b, "THE WORK IS NOT ON ORIGIN. %s was read from a LOCAL head, so this branch exists\n"+
			"only in a worktree on this host. git gc of that worktree destroys it. Push it before\n"+
			"anything else — 'stranded on origin' is recoverable at leisure, this is not.\n\n",
			a.Finding.Ref)
	}

	switch a.Route {
	case RouteRestartSweep:
		fmt.Fprintf(&b, "HOW THIS WAS FOUND, and why it is late. Not by the release gate — that gate could\n"+
			"never have fired for this polecat. It reports from releasePolecatClaim, which needs a\n"+
			"registry entry, and the registry is in-memory with no adopt path: a polecat that was\n"+
			"running when a pogod restarted has no entry in the successor and never will, so it is\n"+
			"un-instrumented for the whole rest of its life, graceful stop included. This is the\n"+
			"startup sweep that exists for exactly that population, so the finding may be hours or\n"+
			"days old.\n\n")
		if a.StillAlive {
			fmt.Fprintf(&b, "THE POLECAT IS STILL RUNNING (witnessed alive at sweep time). Its work is real and\n"+
				"already pushed, but the branch is not necessarily final — it may still commit more.\n"+
				"Do not submit under a working polecat unless you have established it is finished;\n"+
				"see the orphaned-polecat alert for the same process, which carries the identity\n"+
				"re-check you need before acting on its pid. What is certain either way is the\n"+
				"do-not-dispatch half: this item's work exists.\n\n")
		}
	default:
		fmt.Fprintf(&b, "HOW THIS WAS FOUND. pogod released this polecat's claim and checked its branch on\n"+
			"the way out (internal/agent/strandedgate.go). The polecat is gone, so the branch is\n"+
			"final and safe to submit as it stands.\n\n")
	}

	b.WriteString("The same fact is on the event spine as work_item_stranded_push; this mail is the\n" +
		"half that is observable from outside the daemon that found it (mg-be37).\n")
	return subject, b.String()
}

// wrapAt greedily wraps s to at most width columns, on spaces.
//
// Every other paragraph in this body is hand-wrapped in its format string; the
// second opinion is assembled in another package and arrives as one long line,
// and a mail whose paragraphs are 400 columns wide is a mail that gets skimmed
// past. A word longer than width is left over-long rather than broken: the long
// tokens here are branch names and shas, and a broken sha is worse than a ragged
// line.
func wrapAt(s string, width int) string {
	var b strings.Builder
	col := 0
	for i, word := range strings.Fields(s) {
		switch {
		case i == 0:
			col = len(word)
		case col+1+len(word) > width:
			b.WriteString("\n")
			col = len(word)
		default:
			b.WriteString(" ")
			col += 1 + len(word)
		}
		b.WriteString(word)
	}
	return b.String()
}

// shortSHA mirrors strandedwork's rendering so a sha in this mail and a sha in
// the Summary() line it quotes are the same length.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
