// Package verdictwatch reports every landed work item that NONE OF THE CHANNELS
// IT CHECKS carried a verdict to the filer over.
//
// # The predicate, in one line
//
//	AN ITEM REACHING done (OR archived) WHOSE FILER WAS TOLD BY NONE OF THE
//	CHANNELS THIS SCAN CHECKS IS A DROPPED VERDICT.
//
// Every half is read from macguffin's own store and nothing else:
//
//   - the landing      `work.done` / `work.archive` in events.jsonl
//   - the filer        `creator:` in the item's own frontmatter
//   - the worker       `polecat-<name>` in the item's result sidecar
//   - the channels     EVERY way a verdict can arrive, enumerated — see Channels
//
// # THE MEASURED THING IS THE NEAR END, AND EVERY SENTENCE SAYS SO (mg-4e02)
//
// This detector measured "the worker mailed the filer" and reported "no verdict
// reached the filer". Those were the same sentence until 2026-08-12 and they are
// not anymore: the near end is a fact about ONE channel, the far end quantifies
// over ALL channels, and the channel set grew that day.
//
// mg-f120 added a Creator-notification sent by POGOD — right transport, right
// mailbox, right item, a sender this predicate did not name — live at
// 2026-08-13T02:01:35Z. Every item that backstop covers scored DROPPED from then
// on. Two mechanisms built weeks apart against the same gap, neither aware of the
// other, so THE FIX FOR VERDICT DELIVERY WAS BEING MEASURED BY AN INSTRUMENT
// STRUCTURALLY UNABLE TO REGISTER IT WORKING — and the DROPPED count would have
// climbed as the fix took effect and read as deterioration.
//
// Three things follow, and they are the shape of this package rather than
// decoration on it:
//
//   - The channel set is a LIST the report PRINTS. A report whose claim
//     quantifies over channels must enumerate the channels it checked, or it
//     mis-reports silently the next time one is added. Add the channel here when
//     one is added to the fleet.
//   - DELIVERED names WHICH channel carried it. "A polecat did its job" and "a
//     backstop caught it" are different facts about fleet health, and collapsing
//     them destroys the thing this was built to watch.
//   - No sentence claims a verdict did not reach anyone. DROPPED means the
//     channels below carried nothing, which is true in both regimes and costs
//     nothing to say.
//
// # A RELAY IS NOT A DELIVERY
//
// The widening this must NOT take is "any mail mentioning the item id". A
// coordinator forwarding "your item is done" is a MENTION of a verdict, not a
// verdict — the question is WHO DISCHARGED THE OBLIGATION, not whether the filer
// happened to hear something. A predicate that cannot tell a verdict from talk
// about one has the defect it was built to find. So each channel matches a
// SENDER plus a shape only that sender's own notification has.
//
// # THE VERDICT IS USUALLY ALREADY ON DISK, AND A DROPPED ROW PRINTS IT
//
// The worker is resolved out of the item's result sidecar, so this package opens
// that file already — and on most rows the same file holds the verdict itself.
// Reading one field out of a verdict to identify a worker and then reporting
// DROPPED without the rest of it is not blindness to the outcome; it is looking
// PAST it. Every DROPPED row whose sidecar records a verdict prints that verdict,
// which turns "nobody was told" into "nobody was told, and here is what they
// should have been told" — recoverable in one pass instead of one item at a time.
//
// The verdict is NOT on the work item object: `mg show <id> --json` has no
// `result` key at all, so `jq -r .result` prints `null` and exits 0. A missing
// key and a blank verdict are indistinguishable through that recipe, which is how
// it survived being handed to three agents. Nothing here emits it — and nothing
// here emits a path it has not itself read, because A RETRIEVAL INSTRUCTION THIS
// TOOL EMITS IS A CLAIM THAT THE ARTIFACT IS THERE, and when it is wrong the
// resulting negative travels as somebody's evidence.
//
// # Where this came from, and why it moved
//
// This is a port of `verdictwatch.py`, the deliverable of macguffin's mg-bf3f.
// mg-f911 audited that instrument, CONFIRMED it, and repaired its rotted
// proof-of-life — and left one defect standing: NOTHING RAN IT. Zero pogo
// schedules, zero cron entries, zero references outside its own directory. It
// sat in `research/onethird_program/code/verdict_delivery_bf3f/`, and code in a
// research working directory has no runner by construction.
//
// Registering a schedule that reached into that directory would have made the
// fleet's verdict-integrity detector depend on the layout of an unrelated
// project's scratch tree. Porting it here instead puts it on the same footing as
// its eight siblings — check-acks, check-commit-body, check-intake,
// check-mailloops, check-prompts, check-staleness, check-strandedmail,
// check-teardown — every one of which is a read-only detector that reports a
// condition and takes no action. That is the family's actual membership
// criterion (pm-pogo, mg-f5dd): not which subsystem it reads, not which repo the
// state lives in. check-teardown already reads mg state and asks GitHub about
// it; check-strandedmail already reads the macguffin mail tree.
//
// # It REPORTS ONLY, and that is a boundary, not an implementation stage
//
// Nothing here files a ticket, sends the missing verdict, or edits an item. If a
// future version should FILE the missing verdict, that is a DIFFERENT command
// and it must not live in this family. A detector whose report is trusted is one
// that has never surprised its reader by doing something.
//
// # THE THREE CLASSES, WHICH ARE A SECOND AXIS AND NEVER ADDED TO THE FIRST
//
//	verdict on disk, a channel delivered   ->  fine
//	verdict on disk, no channel delivered  ->  a ROUTING failure, recoverable NOW
//	no verdict on disk, none delivered     ->  the real LOSS
//
// One collapsed DROPPED number hides which of the last two a row is, and only the
// last is unrecoverable. This partition is over the same population as the
// per-cause one (coordinator-skip / no-mailbox / delivered-by-pogod) and the two
// must never be added together.
//
// # What this detector cannot see, stated here and not in a footnote
//
//	D-1  A verdict delivered by a channel NOT in Channels — a commit subject, a
//	     docs/ file, a spoken handover, a mechanism added after this line was
//	     written — is invisible to it and is reported as DROPPED. That is the
//	     intended polarity: the original complaint is precisely that commit
//	     subjects are not delivery. What the report may therefore NOT say is that
//	     the verdict reached nobody; it says which channels it checked.
//	D-2  An item whose worker cannot be resolved is NOT reported as delivered and
//	     NOT reported as dropped. It goes in its own UNDECIDABLE bucket, sized
//	     and listed, because a detector that silently absorbs what it cannot
//	     reach is the defect this whole lineage keeps rediscovering.
//	D-3  A verdict mailed to somebody OTHER than the filer (to mayor, say) is not
//	     delivery to the filer and is reported as DROPPED. MailsElsewhere on each
//	     row records it, because it discriminates a dead process from a live one
//	     that did not mail the filer.
package verdictwatch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/mailbox"
)

// Channel is one way a verdict can reach the filer.
//
// A channel is a SENDER plus the shape of that sender's own notification, never
// "a message mentioning the item" — see the relay section in the package doc.
type Channel string

const (
	// ChannelWorker is the worker polecat mailing the filer itself: a message in
	// the filer's mailbox whose From: is that worker. This is the obligation as
	// originally written, and a delivery on this channel means a polecat did its
	// job.
	ChannelWorker Channel = "worker-mail"
	// ChannelPogod is pogod's Creator-notification (mg-f120): the daemon telling
	// the agent that COMMISSIONED an item that it completed. Same transport, same
	// mailbox, same item — a DIFFERENT SENDER, which is the whole reason this
	// channel had to be named explicitly. A delivery here means a backstop caught
	// what a polecat did not do.
	ChannelPogod Channel = "pogod-notify"
)

// Channels is every channel a scan checks, in the order it checks them.
//
// THIS LIST IS THE REPORT'S CLAIM. It is printed, not assumed complete, because a
// detector whose finding quantifies over channels mis-reports every time one is
// added and nobody edits it. When the fleet grows a way of telling a filer, add
// it here and give it a Looked line; leaving it out does not make the report
// narrower, it makes the report wrong.
func Channels() []Channel { return []Channel{ChannelWorker, ChannelPogod} }

// Looked says, in words, exactly what was examined for this channel — so a reader
// can tell whether their case is covered without reading this package.
func (c Channel) Looked() string {
	switch c {
	case ChannelWorker:
		return "a message in the filer's mailbox (unread, read OR archived) whose From: is the item's worker"
	case ChannelPogod:
		return "pogod's own completion notice for THIS item in the filer's mailbox (From: pogod, subject `COMPLETED: <id> — …`), added by mg-f120"
	}
	return string(c)
}

// Class is the RECOVERABILITY of a row. It is a SECOND AXIS over the same
// population as Status, and the two are never added together.
type Class string

const (
	// ClassDelivered — a channel carried it. Nothing to recover.
	ClassDelivered Class = "DELIVERED"
	// ClassRouting — the verdict is recorded on disk and no channel carried it.
	// A ROUTING failure: the outcome exists in full and can be handed over as it
	// stands, right now, without asking anybody to redo anything.
	ClassRouting Class = "ROUTING"
	// ClassLost — nothing was delivered and nothing was recorded either. The only
	// one of the three that is not recoverable from the store.
	ClassLost Class = "LOST"
	// ClassUndecidable — the worker could not be resolved, so no channel could be
	// checked. A statement about this detector's reach, not about the fleet.
	ClassUndecidable Class = "UNDECIDABLE"
)

// Reach says whether the filer could have been reached AT ALL, which is a
// different finding from a filer who was reachable and was not told. Those two
// scored identically before mg-4e02 and they demand opposite responses: one is a
// gap in the fleet's notification path, the other is an addressee that does not
// exist.
type Reach string

const (
	// ReachMailbox — the filer has a mailbox and it was read.
	ReachMailbox Reach = "mailbox"
	// ReachNoMailbox — the filer has no mailbox at all. NO channel could have
	// reached them, so a DROPPED row here is uncoverable rather than untold.
	ReachNoMailbox Reach = "no-mailbox"
	// ReachRedirected — pogod's notice for this item went to the coordinator
	// instead, because the filer is not a live agent (an exited polecat keeps its
	// mailbox forever, so a send there reports Delivered and is read by nobody).
	// The obligation WAS discharged as far as it could be; the filer still was not
	// told.
	ReachRedirected Reach = "redirected"
)

// Status is one item's verdict-delivery state. Three values, not two: see D-2.
type Status string

const (
	// Delivered — the filer's mailbox holds a message from the worker.
	Delivered Status = "DELIVERED"
	// Dropped — the item landed and the filer's mailbox holds nothing from the
	// worker. The finding this detector exists to produce.
	Dropped Status = "DROPPED"
	// Undecidable — the worker could not be resolved from mg's own store, so
	// neither verdict can be reached. Counted and listed, never folded into
	// either of the other two.
	Undecidable Status = "UNDECIDABLE"
)

// Verdict is the outcome recorded in the item's result sidecar — the thing the
// filer should have been told.
//
// It is carried on the row because this package has the sidecar open anyway (the
// worker's identity comes out of it) and a DROPPED row that prints the verdict is
// recoverable in one pass. Sidecar is the EXPLICIT path that was read, never a
// glob: the lifecycle directories are scanned alphabetically, so a stray copy in
// `available/` or `claimed/` sorts ahead of the real one in `done/`.
type Verdict struct {
	// Word is the worker's own verdict word — pass, partial, blocked, whatever it
	// wrote. Empty when the sidecar records a verdict whose shape names no word;
	// the verdict is still PRESENT in that case, which is why presence is "this
	// pointer is non-nil" and not "Word != \"\"".
	Word string `json:"word,omitempty"`
	// Summary is the worker's one-line account, when it recorded one.
	Summary string `json:"summary,omitempty"`
	// CompletedBy is who performed the close (`refinery` on the merge route). It
	// is what makes a merge-route row legible: nothing on that path ever asks the
	// worker to mail anyone, so a DROPPED row against it is a routing gap and not
	// a worker that vanished.
	CompletedBy string `json:"completed_by,omitempty"`
	// Sidecar is the file this was read out of, absolute and explicit.
	Sidecar string `json:"sidecar"`
}

// Mail is the verdict message that satisfied the predicate, named so a reader
// can go and read it rather than take this report's word for it.
type Mail struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	State   string `json:"state"`
	// N is how many messages from the worker the filer holds. The row names the
	// earliest; this says whether there were others.
	N int `json:"n"`
}

// Row is one landed work item's verdict-delivery state.
type Row struct {
	ID    string `json:"id"`
	Filer string `json:"filer"`
	// Worker is the name EXACTLY as the branch spells it. The alternate
	// spellings in WorkerNames are for MATCHING; only this one is for READING.
	// The python original printed sorted(names)[0] here, which prefers
	// `mg-y0120` over `y0120` alphabetically and so printed a name that agent
	// never used (its DEFECT-1).
	Worker string `json:"worker,omitempty"`
	// Resolver says HOW the worker was identified: "sidecar" (mg recorded it),
	// or "shape" (inferred from the polecat naming convention and accepted only
	// because that name actually wrote to this filer). A reader who cannot tell
	// the two apart cannot weigh the row.
	Resolver    string   `json:"resolver,omitempty"`
	WorkerNames []string `json:"worker_names,omitempty"`
	// Landed is the RFC3339 stamp of the work.done / work.archive event,
	// verbatim as mg wrote it.
	Landed string `json:"landed"`
	// Kind is "done" or "archived".
	Kind      string `json:"kind"`
	DoneActor string `json:"done_actor,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    Status `json:"status"`
	// Class is the recoverability axis: DELIVERED / ROUTING / LOST / UNDECIDABLE.
	Class Class `json:"class"`
	// Reach says whether the filer could have been reached at all.
	Reach Reach `json:"reach"`
	// RedirectedTo names who pogod told instead, on a ReachRedirected row.
	RedirectedTo string `json:"redirected_to,omitempty"`
	// Verdict is what the item's result sidecar records, when it records one. Set
	// on every row that has one, delivered or not: on a DROPPED row it is the
	// recovery, and on a DELIVERED one it is what was recovered.
	Verdict *Verdict `json:"verdict,omitempty"`
	// VerdictMail is set only on a DELIVERED row.
	VerdictMail *Mail `json:"verdict_mail,omitempty"`
	// DeliveredBy names the channel that carried it, and is set only on a
	// DELIVERED row. "delivered by the worker" and "delivered by pogod" are
	// different facts about system health; a report that collapses them destroys
	// what this detector was built to watch.
	DeliveredBy Channel `json:"delivered_by,omitempty"`
	// MailsElsewhere counts messages this worker sent to somebody who was not
	// the filer. It discriminates a worker that died from one that mailed the
	// wrong addressee — opposite repairs, and the report must not make them look
	// the same.
	MailsElsewhere int `json:"mails_elsewhere"`
	// MailboxExists is false when the filer has no mailbox at all, which is a
	// different finding from an empty one: nobody has ever written to them, so
	// every one of their items is dropped BY CONSTRUCTION.
	MailboxExists bool `json:"mailbox_exists"`
}

// ChannelCheck is one channel, what was looked at for it, and how many rows it
// carried. The report prints these BY NAME: a finding that quantifies over
// channels is only as honest as its enumeration of them.
type ChannelCheck struct {
	Channel Channel `json:"channel"`
	Looked  string  `json:"looked"`
	// Delivered counts the rows this channel carried in this scan.
	Delivered int `json:"delivered"`
}

// Report is the outcome of one scan.
type Report struct {
	Root  string `json:"root"`
	Filer string `json:"filer,omitempty"`
	Since string `json:"since,omitempty"`
	// Coordinator is the agent pogod redirects a dead filer's notice to, and whose
	// mailbox was read to find those redirects.
	Coordinator string `json:"coordinator,omitempty"`

	// Channels enumerates what this run checked, in the order it checked it.
	Channels []ChannelCheck `json:"channels_checked"`

	Scanned     int `json:"scanned"`
	Delivered   int `json:"delivered"`
	Dropped     int `json:"dropped"`
	Undecidable int `json:"undecidable"`

	// Routing and Lost split Dropped along the recoverability axis: Routing rows
	// have the verdict on disk and need only handing over, Lost rows have nothing
	// recorded anywhere. Only the second is an actual loss, and a single collapsed
	// DROPPED number cannot say which a row is.
	Routing int `json:"routing"`
	Lost    int `json:"lost"`
	// Unreachable counts DROPPED rows whose filer could not have been reached by
	// ANY channel — no mailbox, or a notice pogod redirected because the filer is
	// not a live agent. Separated from the untold, because "nobody told them" and
	// "there was nobody to tell" are different findings.
	Unreachable int `json:"unreachable"`

	// Rows holds every scanned item, oldest landing first. The original's stated
	// purpose is that a backlog can be RECOVERED and not merely alarmed about,
	// and recovery is done in landing order.
	Rows []Row `json:"rows,omitempty"`

	// MissingBoxes names filers with no mailbox at all, sorted.
	MissingBoxes []string `json:"missing_boxes,omitempty"`

	// CollapsedCopies counts extra on-disk copies of an already-seen work item
	// — the live store holds sixteen, archived under two months each. Reported
	// rather than silently absorbed: a number that quietly shrinks the
	// population is the same class of thing this detector exists to catch.
	CollapsedCopies int `json:"collapsed_copies,omitempty"`

	// Blind names, in words, every reason this run measured nothing. A non-empty
	// Blind means the counts below it are not a result — see InstrumentFailure.
	Blind []string `json:"blind,omitempty"`
}

// InstrumentFailure reports whether this run could not look, as distinct from
// having looked and found nothing.
//
// THIS IS THE DEFECT THE WHOLE LINEAGE KEEPS REDISCOVERING. A check that cannot
// distinguish "clean" from "could not look" certifies a state it never observed.
// It is not hypothetical here: on 2026-08-09 both check-intake and
// check-teardown returned INSTRUMENT FAILURE during a four-hour network outage
// rather than exiting 0, which is the behaviour this copies.
//
// The shape it guards against for THIS detector is specific and quiet. Every
// landing comes from events.jsonl. Lose that file — renamed, rotated, a store
// pointed one directory too high — and the scan finds zero landed items, reports
// "0 dropped", and exits 0. A green census would read as fleet health while
// every verdict in the fleet went missing.
func (r Report) InstrumentFailure() bool { return len(r.Blind) > 0 }

// Actionable reports whether the run found at least one dropped verdict.
//
// UNDECIDABLE rows are deliberately NOT actionable: they are a statement about
// this detector's reach, not about the fleet's behaviour, and a detector that
// cried wolf over its own blind spot would be muted long before the run that
// mattered.
func (r Report) Actionable() bool { return r.Dropped > 0 }

// DroppedRows returns the findings, oldest landing first.
func (r Report) DroppedRows() []Row { return r.rowsWith(Dropped) }

// DeliveredRows returns the rows that satisfied the predicate.
func (r Report) DeliveredRows() []Row { return r.rowsWith(Delivered) }

// UndecidableRows returns the rows this detector could not reach.
func (r Report) UndecidableRows() []Row { return r.rowsWith(Undecidable) }

// RowsInClass returns the rows in one recoverability class, oldest landing first.
func (r Report) RowsInClass(c Class) []Row {
	var out []Row
	for _, row := range r.Rows {
		if row.Class == c {
			out = append(out, row)
		}
	}
	return out
}

func (r Report) rowsWith(s Status) []Row {
	var out []Row
	for _, row := range r.Rows {
		if row.Status == s {
			out = append(out, row)
		}
	}
	return out
}

// Options scopes one scan.
type Options struct {
	// Root is the macguffin store. Empty means DefaultRoot().
	Root string
	// Filer restricts the scan to items filed by one agent. Empty scans EVERY
	// filer — this detector is fleet-wide by construction, which is most of the
	// argument for it living here rather than in the project that discovered it.
	Filer string
	// Since is an RFC3339 prefix; items that landed before it are excluded.
	// Compared as a string, so "2026-08-05" and a full stamp both work.
	Since string
	// Coordinator is the agent pogod redirects a notice to when the filer is not a
	// live agent. Empty means DefaultCoordinator. Its mailbox is read for those
	// redirects and for nothing else — a notice sitting there is NOT counted as a
	// delivery to the filer, it is what makes a row REDIRECTED rather than untold.
	Coordinator string
}

// DefaultCoordinator is the coordinator name assumed when Options names none. It
// matches config.DefaultCoordinator, and is a literal here rather than a read of
// the live config because this package's results must not depend on the machine
// it runs on — the CLI passes the configured name in.
const DefaultCoordinator = "mayor"

// Scan runs the predicate over the store.
//
// A store it cannot read produces a Report whose Blind list says so, never an
// empty report — see InstrumentFailure. The error return is reserved for an IO
// failure severe enough that the walk itself could not proceed, and a caller
// must treat it exactly as it treats InstrumentFailure: this run measured
// nothing.
func Scan(opts Options) (Report, error) {
	root := opts.Root
	if root == "" {
		root = DefaultRoot()
	}
	coordinator := opts.Coordinator
	if coordinator == "" {
		coordinator = DefaultCoordinator
	}
	rep := Report{Root: root, Filer: opts.Filer, Since: opts.Since, Coordinator: coordinator}
	for _, ch := range Channels() {
		rep.Channels = append(rep.Channels, ChannelCheck{Channel: ch, Looked: ch.Looked()})
	}
	if root == "" {
		rep.Blind = append(rep.Blind, "no macguffin store root could be resolved (MG_ROOT unset and no home directory)")
		return rep, nil
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		rep.Blind = append(rep.Blind, fmt.Sprintf("the macguffin store %s is not a readable directory", root))
		return rep, nil
	}

	items, err := loadItems(filepath.Join(root, "work"))
	if err != nil {
		return rep, fmt.Errorf("read the work tree under %s: %w", root, err)
	}
	items, rep.CollapsedCopies = dedupeByID(items)
	if len(items) == 0 {
		rep.Blind = append(rep.Blind, fmt.Sprintf("the work tree %s holds no work items at all", filepath.Join(root, "work")))
	}

	landings, err := loadLandings(filepath.Join(root, "events.jsonl"))
	if err != nil {
		// The landing half of the predicate. Without it every item looks
		// un-landed and the scan reports a clean fleet — the exact quiet failure
		// this detector exists to make impossible.
		rep.Blind = append(rep.Blind, fmt.Sprintf("the landing half is unreadable: %v (every item would read as never landed, and the scan would report a clean fleet)", err))
		return rep, nil
	}

	mailRoot := filepath.Join(root, "mail")
	if fi, err := os.Stat(mailRoot); err != nil || !fi.IsDir() {
		// The delivery half. Without it every landed item looks undelivered and
		// the scan reports the whole fleet as dropped — loud rather than quiet,
		// but still not a measurement.
		rep.Blind = append(rep.Blind, fmt.Sprintf("the delivery half is unreadable: %s is not a readable directory (every landed item would read as dropped)", mailRoot))
		return rep, nil
	}

	sentBy := loadMailSent(filepath.Join(root, "events.jsonl"))

	// One mailbox read per filer, not per item: a fleet-wide scan of this store
	// covers hundreds of items across a few dozen filers.
	type index struct {
		byFrom map[string][]message
		exists bool
	}
	boxes := map[string]*index{}
	loadBox := func(agent string) *index {
		if idx, ok := boxes[agent]; ok {
			return idx
		}
		msgs, exists := loadMailbox(mailRoot, agent)
		idx := &index{byFrom: map[string][]message{}, exists: exists}
		for _, m := range msgs {
			key := mailbox.Canonical(m.From)
			idx.byFrom[key] = append(idx.byFrom[key], m)
		}
		boxes[agent] = idx
		return idx
	}

	// The coordinator's box, read for ONE purpose: pogod's redirected notices. A
	// filer that is not a live agent still has a mailbox — mg never removes one —
	// so pogod sends the notice to the coordinator and says in the subject who it
	// was for. Without this, such a row is indistinguishable from a filer nobody
	// tried to tell, and the two demand opposite responses.
	//
	// A coordinator with no mailbox, or one this run cannot read, yields no
	// redirects and every such row reads as untold. That is the safe direction —
	// it over-reports rather than quietly discharging a row — but it is a real
	// residual and it is why the coordinator NAME comes from config at the callsite
	// rather than being assumed here.
	redirected := map[string]string{} // item id -> the filer it was for
	for _, m := range loadBox(coordinator).byFrom[mailbox.Canonical(pogodSender)] {
		if id, forFiler := pogodNoticeSubject(m.Subject); id != "" && forFiler != "" {
			redirected[id] = forFiler
		}
	}

	missing := map[string]bool{}
	for _, item := range items {
		land, ok := landings[item.ID]
		if !ok {
			continue // never landed: outside the predicate entirely
		}
		if opts.Since != "" && land.TS < opts.Since {
			continue
		}
		if item.Creator == "" {
			// No filer means no addressee, so the predicate has nothing to say.
			continue
		}
		if opts.Filer != "" && item.Creator != opts.Filer {
			continue
		}
		idx := loadBox(item.Creator)
		if !idx.exists {
			missing[item.Creator] = true
		}

		names, resolver := resolveWorker(item, idx.byFrom)
		row := Row{
			ID:            item.ID,
			Filer:         item.Creator,
			Worker:        item.DeclaredWorker,
			Resolver:      resolver,
			WorkerNames:   names,
			Landed:        land.TS,
			Kind:          land.Kind,
			DoneActor:     land.Actor,
			Title:         item.Title,
			MailboxExists: idx.exists,
			Verdict:       item.Verdict,
		}
		switch {
		case !idx.exists:
			row.Reach = ReachNoMailbox
		case redirected[item.ID] != "" && mailbox.Canonical(redirected[item.ID]) == mailbox.Canonical(item.Creator):
			row.Reach = ReachRedirected
			row.RedirectedTo = coordinator
		default:
			row.Reach = ReachMailbox
		}
		if row.Worker == "" && len(names) > 0 {
			row.Worker = names[0]
		}

		// THE CHANNELS, IN Channels() ORDER. The worker's own mail is checked first
		// and wins a tie, so a row that both channels covered still reports that a
		// polecat did its job — pogod's notice is the backstop, and reporting the
		// backstop over the primary would hide a working fleet.
		if len(names) > 0 {
			var hits []message
			for _, n := range names {
				hits = append(hits, idx.byFrom[mailbox.Canonical(n)]...)
				for _, e := range sentBy[mailbox.Canonical(n)] {
					if mailbox.Canonical(e.To) != mailbox.Canonical(item.Creator) {
						row.MailsElsewhere++
					}
				}
			}
			sort.SliceStable(hits, func(i, j int) bool { return hits[i].Date < hits[j].Date })
			if len(hits) > 0 {
				row.deliver(ChannelWorker, hits)
			}
		}
		if row.Status != Delivered {
			// pogod's Creator-notification (mg-f120). Checked even on a row whose
			// worker could not be resolved: the notice names the item, so it does
			// not need the worker's identity to be a delivery — and an UNDECIDABLE
			// row that pogod DID cover is a delivery, not a blind spot.
			if notices := pogodNoticesFor(idx.byFrom[mailbox.Canonical(pogodSender)], item.ID, item.Creator); len(notices) > 0 {
				row.deliver(ChannelPogod, notices)
			}
		}
		if row.Status == "" {
			if len(names) == 0 {
				// D-2: neither verdict can be reached through the worker channel,
				// and pogod did not cover it either.
				row.Status = Undecidable
			} else {
				row.Status = Dropped
			}
		}
		row.Class = classify(row)
		rep.Rows = append(rep.Rows, row)
	}

	// Oldest landing first. Ties broken by id so a re-scan of an unchanged store
	// renders byte-identically: a report that reshuffles looks like it changed,
	// and a reader watching for change learns to stop reading it.
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		if rep.Rows[i].Landed != rep.Rows[j].Landed {
			return rep.Rows[i].Landed < rep.Rows[j].Landed
		}
		return rep.Rows[i].ID < rep.Rows[j].ID
	})

	rep.Scanned = len(rep.Rows)
	for _, row := range rep.Rows {
		switch row.Status {
		case Delivered:
			rep.Delivered++
			for i := range rep.Channels {
				if rep.Channels[i].Channel == row.DeliveredBy {
					rep.Channels[i].Delivered++
				}
			}
		case Dropped:
			rep.Dropped++
			if row.Reach != ReachMailbox {
				rep.Unreachable++
			}
		case Undecidable:
			rep.Undecidable++
		}
		switch row.Class {
		case ClassRouting:
			rep.Routing++
		case ClassLost:
			rep.Lost++
		}
	}
	for a := range missing {
		rep.MissingBoxes = append(rep.MissingBoxes, a)
	}
	sort.Strings(rep.MissingBoxes)

	// An UNSCOPED run that judged nothing has not measured a clean fleet; it has
	// failed to find the population. The operator asked about every filer since
	// the beginning of the store, and this store always holds landed items.
	//
	// A SCOPED run that comes back empty is a different thing and stays exit 0:
	// `--filer somebody-who-never-filed` is an answer to the question asked, and
	// the python original is pinned to that behaviour by its own construction
	// suite. The two are separated here rather than collapsed, because the
	// operator supplying a filter is the one fact that tells them apart.
	if rep.Scanned == 0 && opts.Filer == "" && opts.Since == "" {
		rep.Blind = append(rep.Blind, "an unscoped scan judged 0 landed items; the population is missing, not clean")
	}
	return rep, nil
}

// deliver records that ch carried the verdict, naming the earliest message.
func (r *Row) deliver(ch Channel, hits []message) {
	r.Status = Delivered
	r.DeliveredBy = ch
	r.VerdictMail = &Mail{
		ID: hits[0].ID, Date: hits[0].Date, Subject: hits[0].Subject,
		State: hits[0].State, N: len(hits),
	}
}

// classify places a row on the recoverability axis. See ClassRouting: the split
// that matters is whether the outcome still EXISTS somewhere, because only one of
// the two dropped classes is an actual loss.
func classify(r Row) Class {
	switch {
	case r.Status == Delivered:
		return ClassDelivered
	case r.Status == Undecidable:
		return ClassUndecidable
	case r.Verdict != nil:
		return ClassRouting
	default:
		return ClassLost
	}
}

// ---------------------------------------------------------------- the pogod channel

// pogodSender is the From: on every Creator-notification. It is pogod's own name
// (internal/filernotify's mailFrom), and it is the fact the original predicate
// could not express: right transport, right mailbox, right item, a sender that
// is not the worker.
const pogodSender = "pogod"

// pogodNotice matches the SUBJECT filernotify puts on a Creator-notification:
//
//	COMPLETED: mg-af0c — the item's title
//	MERGED BUT NOT CLOSED: mg-af0c — the item's title
//	COMPLETED: mg-af0c (filed by pf32a, who is gone) — the item's title
//
// The shape is required, not just the sender. pogod mails plenty of things that
// merely MENTION an item id — alarms, digests, escalations — and counting those
// would be the "any mail mentioning the item" widening this must not take. The
// `UNDELIVERABLE ` prefix filernotify puts on a relay to the coordinator fails
// the anchor deliberately: that message reports a FAILED notification.
var pogodNotice = regexp.MustCompile(`^(?:COMPLETED|MERGED BUT NOT CLOSED): (mg-[0-9a-z]+)(?: \(filed by ([^),]+), who is gone\))?`)

// pogodNoticeSubject parses a notification subject, returning the item it is
// about and — when the notice was REDIRECTED — the filer it was meant for.
func pogodNoticeSubject(subject string) (id, forFiler string) {
	m := pogodNotice.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return "", ""
	}
	return m[1], strings.TrimSpace(m[2])
}

// pogodNoticesFor selects, from the messages pogod sent to this filer, the ones
// that are this item's completion notice — sorted oldest first.
//
// A notice carrying "(filed by X, who is gone)" is only a delivery to X itself:
// in anybody else's mailbox it is a redirect ABOUT X, and counting it would score
// a filer who was never told as told.
func pogodNoticesFor(fromPogod []message, id, filer string) []message {
	var out []message
	for _, m := range fromPogod {
		gotID, forFiler := pogodNoticeSubject(m.Subject)
		if gotID != id {
			continue
		}
		if forFiler != "" && mailbox.Canonical(forFiler) != mailbox.Canonical(filer) {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// ---------------------------------------------------------------- items

var itemFile = regexp.MustCompile(`^(mg-[0-9a-z]+)\.md(?:\.\d+)?$`)

var (
	reCreator = regexp.MustCompile(`(?m)^creator:\s*(\S+)\s*$`)
	reTitle   = regexp.MustCompile(`(?m)^#\s+(.*)$`)
	reBranch  = regexp.MustCompile(`^polecat-(.+)$`)
)

type item struct {
	ID      string
	Path    string
	Creator string
	Title   string
	// DeclaredWorker is the worker name exactly as the result sidecar's branch
	// spells it, or "" when the sidecar names none.
	DeclaredWorker string
	// SidecarNames are the spellings to MATCH a From: against.
	SidecarNames []string
	// Verdict is what the same sidecar records as the outcome, or nil when it
	// records none. Read in the same pass as the worker, because it is the same
	// file: this package cannot claim not to have seen it.
	Verdict *Verdict
}

// loadItems reads every work item under workRoot, at every status, recursively.
//
// The walk is recursive rather than a fixed list of status directories because
// archived items live under `work/archive/<month>/`, and an archived item is
// squarely inside the predicate — "done OR archived". internal/workitem's
// ListAllFrom reads a flat set of status directories and does not parse
// `creator:`, so it cannot answer this question; widening it would move the
// results every existing caller gets.
//
// The `.md.<pid>` form is matched too. A CLAIMED item is not `mg-bf3f.md` on
// disk: macguffin stamps the owning pid into the filename. That is harmless for
// this predicate (a landed item is back to a plain `.md`) and is handled anyway,
// because the original's DEFECT-5 was precisely a glob that could not see a
// single in-flight item.
func loadItems(workRoot string) ([]item, error) {
	var out []item
	err := filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A single unreadable subdirectory must not abort the walk: the
			// dropped verdict this scan exists to find could be in the next one.
			// A workRoot that does not exist at all is reported by the caller as
			// an empty population, which is Blind.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		m := itemFile.FindStringSubmatch(d.Name())
		if m == nil {
			return nil
		}
		text, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		it := item{ID: m[1], Path: path}
		if c := reCreator.FindSubmatch(text); c != nil {
			it.Creator = string(c[1])
		}
		if t := reTitle.FindSubmatch(text); t != nil {
			it.Title = strings.TrimSpace(string(t[1]))
		}
		it.DeclaredWorker, it.SidecarNames, it.Verdict = readSidecar(path)
		out = append(out, it)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

// dedupeByID collapses copies of the same work item to one row and returns how
// many were collapsed.
//
// THIS IS NOT DEFENSIVE PROGRAMMING; the live store holds sixteen of them —
// items archived under two different months, so `work/archive/2026-03/mg-3119.md`
// and `work/archive/2026-05/mg-3119.md` are both on disk. Counted twice, one
// dropped verdict is reported as two, and the second listing sends its reader
// chasing an item they have already looked at. A backlog report that inflates is
// a backlog report nobody finishes.
//
// The preference is DECLARED rather than incidental. A copy carrying a result
// sidecar wins, because the worker identity is the scarce input and preferring
// the copy that HAS one can only move a row UNDECIDABLE -> DELIVERED/DROPPED,
// never manufacture a drop against a worker nobody named. Ties break on the
// path, so a re-scan of an unchanged store picks the same copy — the original
// took whichever the glob yielded last, which is a coin flip over which copy's
// `creator:` the report is about.
func dedupeByID(items []item) ([]item, int) {
	best := map[string]item{}
	collapsed := 0
	for _, it := range items {
		prev, seen := best[it.ID]
		if !seen {
			best[it.ID] = it
			continue
		}
		collapsed++
		if preferItem(it, prev) {
			best[it.ID] = it
		}
	}
	out := make([]item, 0, len(best))
	for _, it := range best {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, collapsed
}

func preferItem(candidate, incumbent item) bool {
	if len(candidate.SidecarNames) != len(incumbent.SidecarNames) {
		return len(candidate.SidecarNames) > len(incumbent.SidecarNames)
	}
	// Then the copy whose sidecar RECORDS A VERDICT, for the same reason: it can
	// only move a row from LOST to ROUTING — from "nothing to hand over" to "here
	// is what to hand over" — and can never manufacture a finding.
	if (candidate.Verdict != nil) != (incumbent.Verdict != nil) {
		return candidate.Verdict != nil
	}
	return candidate.Path > incumbent.Path
}

// readSidecar reads the item's result sidecar ONCE, for both things it holds: the
// worker's identity (`branch: polecat-<name>`, the only worker identity macguffin
// RECORDS) and the verdict itself.
//
// One function rather than two, deliberately. Reading this file for the worker and
// then reporting a row as DROPPED without its verdict was the defect: not
// blindness to the outcome, but looking past it in a file already open. Keeping
// both reads here means a future change cannot drop the second without seeing the
// first.
//
// The path is the item's OWN sidecar, derived from the path the walk found it at,
// so it is explicit and status-correct. It is never resolved by glob across the
// lifecycle directories: those are scanned alphabetically, so a stray copy in
// `available/` or `claimed/` sorts ahead of the real one in `done/`.
func readSidecar(itemPath string) (declared string, names []string, verdict *Verdict) {
	base := itemPath
	if i := strings.Index(base, ".md"); i >= 0 {
		base = base[:i]
	}
	path := base + ".result.json"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, nil
	}
	var side struct {
		Branch      string          `json:"branch"`
		CompletedBy string          `json:"completed_by"`
		Verdict     json.RawMessage `json:"verdict"`
	}
	if json.Unmarshal(data, &side) != nil {
		return "", nil, nil
	}
	verdict = parseVerdict(side.Verdict, side.CompletedBy, path)
	if m := reBranch.FindStringSubmatch(strings.TrimSpace(side.Branch)); m != nil {
		declared, names = m[1], []string{m[1]}
	}
	return declared, names, verdict
}

// parseVerdict reads the `verdict` field, which comes in BOTH SHAPES IN THE LIVE
// STORE and that is not a hypothetical. Measured 2026-08-13 over the 140 result
// sidecars under `work/done`, and the three counts answer three different
// questions — pm-pogo's caution, after conflating two of them while measuring:
//
//	140 of 141 done items HAVE a sidecar          (a file exists)
//	121 of those 140 record a `verdict` key       (an outcome was written down)
//	  of those 121: 113 as an OBJECT (`{"verdict":"pass","summary":…}`)
//	                  8 as a BARE STRING (`"verdict":"pass"`)
//
// The last line is the one this function is about, and the reason the report's
// coverage is a property of the second number rather than the first.
//
// Provenance, because a number nobody can source becomes a property of the system
// by repetition: the 140-of-141 line is architect's measurement of 2026-08-13,
// relayed by pm-pogo and doctor. The 121 / 113 / 8 split is this branch's own, over
// the same store on the same night. Neither is a design constant — nothing here
// branches on a ratio — so a later reader should re-measure rather than inherit.
//
// This is why the recipe `jq -r '.verdict.verdict'` is not the fix for
// `jq -r .result` — it returns `null` at exit 0 on those 8, which is the SAME
// failure one level down: a wrong question that reads as a blank answer. A nil
// return here means the sidecar records no verdict AT ALL, which is the only case
// that is genuinely a loss.
func parseVerdict(raw json.RawMessage, completedBy, path string) *Verdict {
	// An absent, null or empty field records NO verdict, whatever else the sidecar
	// says about the close. `completed_by: refinery` with no verdict is a merge
	// that happened, not an outcome anybody wrote down.
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == `""` || trimmed == "{}" {
		return nil
	}
	v := &Verdict{CompletedBy: completedBy, Sidecar: path}
	var word string
	if json.Unmarshal(raw, &word) == nil {
		v.Word = strings.TrimSpace(word)
		return v
	}
	var obj struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		v.Word = strings.TrimSpace(obj.Verdict)
		v.Summary = strings.TrimSpace(obj.Summary)
		return v
	}
	// A shape neither reading understands is still a RECORDED verdict: the row is
	// recoverable from the file, which is what the class turns on. Saying "no
	// verdict on disk" here would be the report claiming a loss it did not measure.
	return v
}

// resolveWorker returns the names to match a From: against, and how they were
// obtained.
//
// DECLARED ASYMMETRY, carried over verbatim from the original and load-bearing.
// The `shape` fallback is consulted ONLY when the sidecar is silent, and it is
// accepted ONLY when the name it proposes actually appears as a From: in that
// filer's mailbox. So it can move a row UNDECIDABLE -> DELIVERED and can NEVER
// move one to DROPPED. It can only shrink the reported drop count, never inflate
// it. A resolver that could manufacture drops would make this report
// unfalsifiable.
func resolveWorker(it item, byFrom map[string][]message) (names []string, resolver string) {
	if len(it.SidecarNames) > 0 {
		return it.SidecarNames, "sidecar"
	}
	var hits []string
	for _, cand := range shapeNames(it.ID) {
		if _, ok := byFrom[mailbox.Canonical(cand)]; ok {
			hits = append(hits, cand)
		}
	}
	if len(hits) == 0 {
		return nil, ""
	}
	sort.Strings(hits)
	return hits, "shape"
}

var idSuffix = regexp.MustCompile(`^mg-([0-9a-f]{4})$`)

// shapeNames enumerates the agent names the polecat naming convention would give
// a worker on this item.
//
// The sidecar is the only worker identity mg RECORDS, but it is not the only one
// mg CARRIES: a polecat working `mg-9a19` is named `9a19`, or `z9a19`, or
// `mg-9a19` — the id's four hex characters with at most a one-character
// generation prefix. Resolving from the sidecar alone put two items that had
// been hand-verified as DELIVERED into UNDECIDABLE, which is the same silence
// the original ticket was about (its DEFECT-2).
//
// Deliberately narrow: at most ONE alphanumeric character of prefix, or the
// literal `cat-` prefix. `mg-` spellings need no entry because every comparison
// runs through mailbox.Canonical, which is what mg itself does. A wider rule
// starts matching unrelated agents.
func shapeNames(id string) []string {
	m := idSuffix.FindStringSubmatch(id)
	if m == nil {
		return nil
	}
	s := m[1]
	out := []string{s, "cat-" + s}
	for _, c := range "abcdefghijklmnopqrstuvwxyz0123456789" {
		out = append(out, string(c)+s, "cat-"+string(c)+s)
	}
	return out
}

// ---------------------------------------------------------------- events

type landing struct {
	TS    string
	Kind  string
	Actor string
}

// loadLandings reads the first landing of every item out of events.jsonl.
//
// An item that goes done and is later archived lands ONCE, at `done`. An item
// archived without ever going done lands at `archive` — the predicate says "done
// OR archived" and both are terminal for the filer's purposes.
//
// A missing or unreadable events.jsonl is an ERROR, not an empty map. It is the
// landing half of the predicate: returning an empty map would make every item
// read as never landed and the scan report a clean fleet.
func loadLandings(path string) (map[string]landing, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]landing{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"work.`) {
			continue
		}
		var e struct {
			Type   string `json:"type"`
			ItemID string `json:"item_id"`
			TS     string `json:"ts"`
			Actor  string `json:"actor"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		var kind string
		switch e.Type {
		case "work.done":
			kind = "done"
		case "work.archive":
			kind = "archived"
		default:
			continue
		}
		if e.ItemID == "" {
			continue
		}
		prev, ok := out[e.ItemID]
		if !ok || (prev.Kind == "archived" && kind == "done") {
			out[e.ItemID] = landing{TS: e.TS, Kind: kind, Actor: e.Actor}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type sentEvent struct {
	From string `json:"from"`
	To   string `json:"to"`
	TS   string `json:"ts"`
}

// loadMailSent indexes every mail.sent event by canonical sender, which is what
// MailsElsewhere counts against. A store with no readable events file yields an
// empty index; the landing read above has already declared that blindness, so
// this one does not re-declare it.
func loadMailSent(path string) map[string][]sentEvent {
	out := map[string][]sentEvent{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"mail.sent"`) {
			continue
		}
		var e struct {
			Type string `json:"type"`
			sentEvent
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "mail.sent" {
			continue
		}
		key := mailbox.Canonical(e.From)
		out[key] = append(out[key], e.sentEvent)
	}
	return out
}

// ---------------------------------------------------------------- mail

type message struct {
	ID      string
	State   string
	From    string
	Subject string
	Date    string
}

var header = regexp.MustCompile(`(?m)^([A-Za-z-]+):[ \t]*(.*)$`)

// loadMailbox reads every message in an agent's mailbox: unread, read AND
// ARCHIVED. Returns the messages and whether the mailbox exists at all — which
// is a different thing from an empty mailbox, because "nobody ever wrote to
// them" and "they got no verdicts" are different findings.
//
// The maildir is read DIRECTLY rather than through `mg mail list --json`, and
// that is not a shortcut. `--all` on that command means read plus unread; it
// does NOT include archived mail. A filer who received a verdict and then filed
// it away would read as never having received one, which would turn ordinary
// inbox hygiene into a fleet-wide false alarm.
func loadMailbox(mailRoot, agent string) ([]message, bool) {
	box := filepath.Join(mailRoot, agent)
	if fi, err := os.Stat(box); err != nil || !fi.IsDir() {
		return nil, false
	}
	var msgs []message
	for _, sub := range []string{"new", "cur", "archive"} {
		dir := filepath.Join(box, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			head, _, _ := strings.Cut(string(data), "\n\n")
			m := message{ID: e.Name(), State: sub}
			for _, kv := range header.FindAllStringSubmatch(head, -1) {
				switch strings.ToLower(kv[1]) {
				case "from":
					m.From = strings.TrimSpace(kv[2])
				case "subject":
					m.Subject = strings.TrimSpace(kv[2])
				case "date":
					m.Date = strings.TrimSpace(kv[2])
				}
			}
			msgs = append(msgs, m)
		}
	}
	return msgs, true
}

// ---------------------------------------------------------------- root

// DefaultRoot resolves the macguffin store the same way mg and the dispatch
// gates do: an explicit MG_ROOT wins, then $HOME/.macguffin.
//
// Under a test binary with no MG_ROOT it returns "" rather than the developer's
// live store. This package only reads, so the blast radius of getting it wrong
// is a test that passes or fails on the contents of one machine's ~/.macguffin —
// exactly the kind of result this package exists to distrust. Tests pass an
// explicit root.
func DefaultRoot() string {
	if root := os.Getenv("MG_ROOT"); root != "" {
		return root
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin")
}
