package stallwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/workitem"
)

// An available item whose work is already sitting on a branch is not ready to
// dispatch, and the do-not-dispatch signal for it has to REPEAT (mg-4bf1) —
// whether the branch is merely pushed or already in the refinery merge queue,
// which are two states taking OPPOSITE instructions (mg-64bb).
//
// THE DEFECT THIS CLOSES IS NOT A MISSING SIGNAL. It is a CONTRADICTED one, and
// the distinction is the whole reason this file exists rather than a new
// detector. When a polecat is stopped after pushing, pogod already detects the
// strand exactly: reportStrandedWorkOnRelease emits work_item_stranded_push and
// mails the coordinator `[stranded-push] <polecat> left pushed work behind on
// <branch> — do NOT dispatch`, naming the polecat, the item, the branch, the
// ref, pushed=true, the target and the commit. That mail is correct, arrives
// unasked, and its body states the failure in its own words: "the item it
// belongs to is back in the pool describing itself as untouched."
//
// Meanwhile the item is back in available/, so this package's two dispatch
// checks see an ordinary unclaimed item, and priority-wake says "1
// high-priority work item(s) are ready and unclaimed — claim or dispatch now:
// mg-a932" about the same item, in the same minute, into the same inbox.
//
// WHICH ONE WINS IS DECIDED BY CADENCE, NOT BY CORRECTNESS. The recommendation
// is re-derived from available/ on every tick and repeats on a backoff
// schedule; the prohibition is sent ONCE, at release. A reader who reads their
// mail late, or unevenly, sees the dispatch advice several times and the
// do-not-dispatch advice never. That asymmetry is the mechanism by which the
// correct signal loses, and it is what this check reverses: the prohibition is
// now the one re-derived every tick, and the recommendation is the one that
// stops.
//
// IT IS NOT A RACE AND NOT A WINDOW. Two polecats were stopped on the night of
// 2026-09-07, both following the pre-deploy quiesce procedure exactly, both with
// their work confirmed durable before the stop, and BOTH items were advertised
// this way (mg-a932, mg-d788). `pogo agent stop` releases the claim — correctly,
// mg-fb13 — so the item ALWAYS re-enters the pool, and priority-wake ALWAYS
// picks it up. Nothing about it is timing-dependent. The earlier framing of the
// exposure as "one gate run", then "queue depth × gate duration", was in each
// case an estimate of how long the good signal is MISSING; it is not missing,
// and what matters is how long the two coexist.
//
// WHY THE SPAWN GATE IS NOT THE ANSWER. It is a real backstop and it holds:
// agent.strandedWorkRefusal reads git directly (strandedwork.Scan), so a spawn
// at one of these items is refused whatever this package says. That makes the
// contradiction a reporting defect rather than a data-loss one — but a
// coordinator holds a slot open, queues the dispatch, and waits out a gate run
// measured in tens of minutes on this box before learning the answer, and a
// channel that recommends refused actions is one the reader learns to skim
// (mg-dd77 made the same argument about the per-repo cap).
//
// WHY THE QUEUE IS THE SECOND HALF OF THE SAME QUESTION (mg-64bb). "Does work
// for this item already exist outside the item?" has two yeses, and mg-4bf1
// shipped only one of them. A branch in the refinery queue is pushed and
// unmerged, so it reaches this check exactly like an abandoned one and used to
// render exactly like one — including the paste-ready `pogo refinery submit`,
// aimed at a branch whose merge was already running. Measured on mg-a19a: four
// such notices across ~36 minutes while mr-dacudtqtjv1hjkm21420 was `processing`
// or `queued` throughout. The refinery has no dedup, so following that line
// queues a second merge of the same work; and `pogo check-stranded` — which this
// notice names as the place to look — was meanwhile calling the same branch
// `in_flight` and saying "wait". Two components of pogod contradicting each
// other about one item is the finding mg-4bf1 exists for, and it was reachable
// through mg-4bf1's own repair. So the queue is consulted here too, from the
// same instrument, and it changes the REMEDY rather than the exclusion: the item
// was already withheld from both dispatch checks and still is.
//
// WHY THE ITEMS ARE RE-REPORTED RATHER THAN SILENCED. Same rule as mg-1a8a's
// worked-but-unclaimed check and mg-836c's preserved-worktree check: a stranded
// item needs a DECISION — submit the branch, or rule it spent — and nothing else
// re-derives that need. Silence is what this package keeps relearning not to
// ship. So the FINDING survives and only the REMEDY changes.

// categoryStrandedPush keys this check's cooldown and stamps its event. The name
// matches the event type pogod already emits at release
// (work_item_stranded_push), so the one-shot detection and this repeating report
// are countable together in events.log rather than looking like two unrelated
// facts about one item.
const categoryStrandedPush = "stranded_push"

// StrandedBranch is one branch holding work an available item is still asking
// for.
//
// It is a subset of strandedwork.Finding, restated here for the reason Workers
// and Preserved are interfaces: internal/stallwatch keeps no edge to the
// packages that do the work, so the notice text stays testable without a git
// repository. cmd/pogod does the translation.
type StrandedBranch struct {
	// Branch is the short branch name (e.g. "polecat-ta932").
	Branch string
	// Ref is what the commits were read from — the remote-tracking ref when one
	// exists, otherwise the local head.
	Ref string
	// Pushed is true when Ref was a remote-tracking ref. It is the field that
	// separates two different emergencies and it is never flattened into the
	// prose: pushed work is durable and recoverable at leisure, local-only work
	// exists in one worktree on one host and git-gc reaps it. `pogo refinery
	// submit` also REFUSES a branch that is not on origin (mg-586d), so a remedy
	// that assumed pushed would be unrunnable for exactly the urgent half.
	Pushed bool
	// Unmerged is how many commits the target does not have.
	Unmerged int
	// Target is the ref the branch was compared against.
	Target string
	// Repo is the repository the branch lives in, so the remedy is paste-ready.
	Repo string
	// PreRegistration is the sha of the oldest unmerged pre-registration commit,
	// or "" when the branch carries none. It changes what the reader must not do
	// — a re-dispatch that bases on the target writes its predictions after
	// seeing the results — so it travels rather than being folded into the count.
	PreRegistration string
	// Queued is the refinery merge request ALREADY RUNNING for this branch, or
	// nil when the branch is in no queue (mg-64bb). See QueuedMerge for why the
	// distinction cannot be left to the reader.
	Queued *QueuedMerge
}

// QueuedMerge is the merge request already in the refinery queue for a stranded
// branch: the remedy for it, running.
//
// WHY A STRANDED BRANCH HAS TO CARRY THIS (mg-64bb). "Pushed and not merged" is
// TWO states, not one, and they take opposite instructions. A branch nobody has
// submitted needs `pogo refinery submit`. A branch already in the queue needs
// nothing at all — and a submit against it is a DUPLICATE merge request, because
// the refinery has no dedup. Before this field the two rendered identically
// here, so the notice below printed a paste-ready submit for a branch whose
// merge was running: measured on mg-a19a, four notices across ~36 minutes while
// mr-dacudtqtjv1hjkm21420 was `processing` or `queued` the whole time.
//
// IT IS THE SAME EXCLUSION `pogo check-stranded` ALREADY APPLIES, and that is
// the point rather than an aside. internal/strandwatch turns exactly this
// combination — a queued branch under an item that is NOT claimed — into a
// KindInFlight row whose remedy is "wait", for reasons its own comment states at
// length. This package sends the reader to that instrument by name, so answering
// the same question differently was two components of pogod contradicting each
// other about one item, which is the finding mg-4bf1 exists for, committed by
// its own repair.
//
// A PLAIN STRUCT AND NOT refinery.MergeRequest, for StrandedBranch's reason:
// internal/stallwatch keeps no edge to the packages that do the work, so the
// notice text stays testable with no refinery and no git repository.
type QueuedMerge struct {
	// MR is the merge-request id, e.g. "mr-dacudtqtjv1hjkm21420". It travels
	// rather than a bare bool for internal/strandwatch's QueuedRequest reason: a
	// reader told "this is in flight" and not told WHICH request has to go find
	// it, and the whole failure being repaired is a reader left to arbitrate
	// between two confident signals.
	MR string
	// Status is the refinery's own word — "queued" while it waits, "processing"
	// while a gate runs on it. Printed VERBATIM: both mean "do not resubmit", but
	// they are different answers to "how long until I can stop worrying about
	// this item".
	Status string
}

// StrandedWork is one snapshot of which available work items already have their
// work on a branch.
type StrandedWork struct {
	// Items maps work-item id -> the branches carrying its work. Absent means no
	// such branch was found, which — see Uncertain — is not proof that none
	// exists.
	Items map[string][]StrandedBranch
	// Uncertain, when non-empty, says why Items may be INCOMPLETE: typically a
	// repository could not be listed. An incomplete snapshot means some stranded
	// item still reads as dispatchable — the pre-fix behaviour, loud rather than
	// silent — so the note travels with the dispatch advice rather than replacing
	// it.
	Uncertain string
	// QueueConsulted records whether the refinery queue was ASKED. False means
	// every StrandedBranch.Queued is nil because nothing looked, not because
	// nothing is in flight — mg-8baa's collapse, and the one field that keeps
	// "the queue said no" and "the queue was never asked" from rendering alike.
	// It is stamped on the event and stated in the notice, because with the queue
	// unasked this check's own remedy may be a duplicate submit.
	QueueConsulted bool
}

// StrandedItem is one item the probe is being asked about: its id and the
// repository it names.
//
// THE REPO TRAVELS WITH THE ID, unlike Preserved.Retained's bare ids, and the
// difference is not stylistic. A retained worktree is on this host and names its
// own repository, so a probe can find it from the id alone; a branch lives in a
// repository nothing but the ITEM names, and a probe that had to guess would
// either scan every repo the fleet works or answer for the wrong one. An item
// whose `repo:` is empty or unresolvable therefore cannot be answered for at all
// — see Stranded.Branches for which direction that failure takes.
type StrandedItem struct {
	// ID is the work-item id.
	ID string
	// Repo is the item's `repo:` frontmatter value. Empty for an item that names
	// none, which is a gap and not a clean verdict.
	Repo string
}

// Stranded probes which available work items already have work on a branch.
//
// An interface for the same reasons Workers, Preserved and Capacity are: no edge
// from this package to internal/strandedwork, and testable notice text.
// cmd/pogod supplies the production implementation.
type Stranded interface {
	// Branches returns the snapshot for the given items. The caller supplies the
	// population so the probe can be cheap — resolving a branch to an item is a
	// NAME question, and only a branch that names one of these items is ever
	// inspected.
	//
	// known=false means the question could NOT be answered at all, and it is NOT
	// "nothing is stranded". On known=false every check behaves exactly as it did
	// before this fix: the items are reported as dispatchable. That is the loud
	// direction, chosen for the reason Workers.InFlight and Preserved.Retained
	// choose it — a false "dispatch this" is self-correcting (the spawn-time
	// stranded gate refuses it), while a false silence looks like a healthy queue.
	Branches(items []StrandedItem) (StrandedWork, bool)
}

// StrandedFunc adapts a function to Stranded.
type StrandedFunc func(items []StrandedItem) (StrandedWork, bool)

// Branches implements Stranded.
func (f StrandedFunc) Branches(items []StrandedItem) (StrandedWork, bool) { return f(items) }

// probeStranded asks the wired Stranded probe once per tick, for the items the
// dispatch checks would otherwise advertise.
//
// Once per TICK, not once per check, so an item cannot read as stranded to one
// check and free to another within the same sample. With no probe wired this
// returns the zero value, under which every check keeps its pre-mg-4bf1
// behaviour exactly.
func (w *Watcher) probeStranded(items []workitem.WorkItem) StrandedWork {
	if w.stranded == nil || len(items) == 0 {
		return StrandedWork{}
	}
	ask := make([]StrandedItem, 0, len(items))
	for _, it := range items {
		if it.ID != "" {
			ask = append(ask, StrandedItem{ID: it.ID, Repo: it.Repo})
		}
	}
	work, known := w.stranded.Branches(ask)
	if !known {
		return StrandedWork{}
	}
	return work
}

// branches returns the stranded branches for an item, if the snapshot names any.
func (s StrandedWork) branches(id string) ([]StrandedBranch, bool) {
	if len(s.Items) == 0 {
		return nil, false
	}
	b, ok := s.Items[id]
	if !ok || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// uncertaintyNote is the sentence a DISPATCH notice appends when the stranded
// snapshot may be incomplete.
//
// Attached to the dispatch notices and not to this file's own notice, for
// WorkInFlight.uncertaintyNote's reason: an incomplete snapshot can only cause a
// stranded item to be MISSED, never to be invented.
func (s StrandedWork) uncertaintyNote() string {
	if s.Uncertain == "" {
		return ""
	}
	return " (Stranded-branch attribution may be incomplete — " + s.Uncertain +
		" — so one of these could already have its work pushed; `pogo check-stranded` before dispatching.)"
}

// checkStrandedPush reports available items whose work already exists on a
// branch, telling the coordinator NOT to dispatch them and naming the remedy.
//
// items is the caller's already-listed available/ snapshot, flight the tick's
// in-flight probe, held the tick's preserved probe and strand the tick's
// stranded probe, so this costs one pass over a slice. The population is exactly
// the items the two dispatch checks dropped for this reason: same
// watchedForDispatch gate, and items a live worker or a retained worktree
// already accounts for are left to their own checks, so the populations stay
// disjoint.
//
// No age threshold gates it, for checkWorkedButUnclaimed's reason: an item whose
// work is already written is an anomaly the instant it exists, and the cost of
// acting on the recommendation instead — a whole re-derivation — does not shrink
// with age. The per-item backoff still bounds how often it repeats, and that
// backoff is the point: it is the SAME shape of schedule priority-wake uses, so
// the prohibition and the recommendation can no longer differ in cadence.
func (w *Watcher) checkStrandedPush(now time.Time, items []workitem.WorkItem, flight WorkInFlight, held PreservedWork, strand StrandedWork) {
	if len(strand.Items) == 0 {
		return
	}

	var stranded []workitem.WorkItem
	found := make(map[string][]StrandedBranch, len(items))
	for _, it := range items {
		if !w.watchedForDispatch(it) {
			continue
		}
		// A live worker on the item is checkWorkedButUnclaimed's row and carries
		// the same instruction; its branch has unmerged commits because that is
		// what work in progress IS, so reporting it here would fire on every
		// healthy polecat that has pushed once.
		if _, worked := flight.worker(it.ID); worked {
			continue
		}
		// A retained worktree is checkPreservedWorktrees' row. The populations do
		// overlap in principle — a polecat can leave both a preserved tree and a
		// pushed branch — and when they do, the preserved row is the one to keep:
		// its work exists in ONE place that a gc reap destroys, while this row's
		// work is on origin. Two notices saying the same thing about one item is
		// how a channel gets skimmed.
		if _, stuck := held.trees(it.ID); stuck {
			continue
		}
		b, ok := strand.branches(it.ID)
		if !ok {
			continue
		}
		stranded = append(stranded, it)
		found[it.ID] = b
	}
	sort.Slice(stranded, func(i, j int) bool { return stranded[i].ID < stranded[j].ID })

	due, sel := w.selectDue(categoryStrandedPush, stranded, now, w.cfg.NudgeCooldown)
	if len(due) == 0 {
		return
	}

	ids := itemIDs(due)
	anyQueued, allQueued := queuedCounts(due, found)
	msg := fmt.Sprintf(
		"stall-watch: %d work item(s) sit in available/ while the work they ask for ALREADY EXISTS "+
			"%s — %s. This is NOT a dispatch request, it is the opposite, and it is the same "+
			"fact pogod already mailed as `[stranded-push] ... do NOT dispatch` when the polecat was "+
			"released. That mail is sent ONCE; this repeats for as long as the state lasts, so the "+
			"prohibition and the dispatch recommendation no longer differ in how often they arrive. "+
			"A worker dispatched at one of these re-derives work that already exists — mg-9a19 lost "+
			"1026 lines that way. %s `pogo check-stranded` shows the "+
			"same population with its per-branch remedy, and `pogo agent spawn-polecat` refuses these "+
			"items until then.",
		len(due), whereTheWorkIs(allQueued), strings.Join(strandedSentences(due, found), "; "),
		strandedRemedy(due, found, allQueued))
	msg += strand.queueNote(anyQueued)
	msg += sel.repeatNotice()

	details := map[string]any{
		"category":      categoryStrandedPush,
		"watched_agent": w.cfg.Agent,
		"item_count":    len(due),
		"item_ids":      ids,
		"branches":      strandedDetails(due, found),
		// Whether the refinery queue was ASKED, stamped beside the rows it
		// qualifies: with it false every row's `queued` is absent because nothing
		// looked, and a counter over these events would read that as "no item was
		// ever held for a merge in flight" (mg-8baa).
		"queue_consulted":    strand.QueueConsulted,
		"oldest_age_seconds": now.Sub(oldestModTime(due)).Seconds(),
	}
	sel.stampDetails(details)
	w.fire(categoryStrandedPush, Notice{
		Subject: subject(nItems(len(due))+" unclaimed with work "+headlineWhere(allQueued), now.Sub(oldestModTime(due)), ids),
		Message: msg,
	}, details)
}

// whereTheWorkIs and headlineWhere say where the work already is, in the words
// that decide what the reader does next.
//
// They only speak in the queue's terms when EVERY branch in the notice is in it.
// A headline is the half that travels — it is what gets skimmed, quoted and
// forwarded — so a mixed set says the thing that is true of both halves rather
// than the more specific thing that is true of one.
func whereTheWorkIs(allQueued bool) string {
	if allQueued {
		return "on a branch IN THE REFINERY MERGE QUEUE"
	}
	return "on a branch"
}

func headlineWhere(allQueued bool) string {
	if allQueued {
		return "ALREADY IN THE MERGE QUEUE"
	}
	return "ALREADY ON A BRANCH"
}

// strandedRemedy names the move, which is NOT the same move for the two states
// this check now separates (mg-64bb).
//
//   - every branch queued: there is nothing to do but wait. The remedy is
//     already running, and the one command a reader would otherwise reach for
//     makes it worse — the refinery does not deduplicate, so a second request is
//     a second merge of the same work.
//   - anything else: the pre-mg-64bb sentence, with submitHint's own guard on
//     whether a paste-ready submit can be printed at all.
func strandedRemedy(items []workitem.WorkItem, found map[string][]StrandedBranch, allQueued bool) string {
	if allQueued {
		return "There is nothing to submit and nothing to dispatch: the merge is ALREADY RUNNING and " +
			"the item closes itself when it lands. Do NOT re-submit it — the refinery has no dedup, " +
			"so a second merge request merges the same work twice."
	}
	return "Get the branch merged instead (" + submitHint(items, found) + ");"
}

// queueNote is the sentence this notice appends when the refinery queue was NOT
// consulted.
//
// It is attached HERE and not only to the dispatch notices, unlike
// uncertaintyNote, because the direction of the damage is reversed. An
// unconsulted queue cannot cause an item to be missed — the branch is found
// either way — it causes this check's own REMEDY to be wrong, recommending a
// submit for a merge that may already be running. So the reader who is being
// handed that remedy is the one who has to be told.
//
// It says nothing when the queue was consulted, and nothing when it was not but
// some branch is already known to be queued — which cannot happen, and is
// guarded rather than asserted because a future wiring that half-answers must
// not print a contradiction.
func (s StrandedWork) queueNote(anyQueued bool) string {
	if s.QueueConsulted || anyQueued {
		return ""
	}
	return " The refinery queue was NOT consulted for this notice, so a branch already awaiting " +
		"merge is indistinguishable here from one nobody has submitted; `pogo check-stranded` asks " +
		"the queue and says which."
}

// strandedSentences renders one clause per item naming its branches.
//
// The PROVENANCE is on every clause and is never reduced to "pushed" (mg-bfe0).
// "PUSHED" and "LOCAL-ONLY" license different next moves and carry opposite
// urgency, and the remedy below is unrunnable for one of them.
func strandedSentences(items []workitem.WorkItem, found map[string][]StrandedBranch) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		parts := make([]string, 0, len(found[it.ID]))
		for _, b := range found[it.ID] {
			parts = append(parts, b.summary())
		}
		out = append(out, it.ID+": "+strings.Join(parts, ", "))
	}
	return out
}

// summary renders one branch as a clause.
func (b StrandedBranch) summary() string {
	s := fmt.Sprintf("%s (%s, %d unmerged commit(s) vs %s",
		b.Branch, b.provenance(), b.Unmerged, orTarget(b.Target))
	if b.PreRegistration != "" {
		// The one fact that changes what a reader must NOT do rather than merely
		// how urgent it is: a worker based on the target writes its predictions
		// after seeing the results, and the artifact looks identical to a valid
		// one.
		s += "; includes an unmerged PRE-REGISTRATION commit " + b.PreRegistration
	}
	if b.Queued != nil {
		// The clause that changes the instruction rather than its urgency: this
		// branch's remedy is already RUNNING, so the reader's move is to wait,
		// and a submit against it queues a second merge request for the same work.
		s += fmt.Sprintf("; ALREADY IN THE REFINERY MERGE QUEUE as %s (%s) — do NOT resubmit",
			b.Queued.MR, orUnreportedStatus(b.Queued.Status))
	}
	return s + ")"
}

// orUnreportedStatus keeps a queue entry with no status from rendering as "()",
// which reads as a formatting bug rather than as a fact the refinery did not
// supply. The MR id is the actionable half and it is present either way.
func orUnreportedStatus(status string) string {
	if status == "" {
		return "status unreported"
	}
	return status
}

// queued reports whether any branch on any of these items is already in the
// refinery queue, and whether EVERY one is.
//
// The two answers are separate because they change different sentences: "any"
// decides whether the notice must say anything about the queue at all, and "all"
// decides whether the headline and the remedy may speak in the queue's terms
// without being wrong about a branch in the same notice that nobody has
// submitted. A mixed set gets the neutral wording and the per-branch clauses,
// which is the only rendering true of both halves.
func queuedCounts(items []workitem.WorkItem, found map[string][]StrandedBranch) (some, all bool) {
	total, inQueue := 0, 0
	for _, it := range items {
		for _, b := range found[it.ID] {
			total++
			if b.Queued != nil {
				inQueue++
			}
		}
	}
	return inQueue > 0, total > 0 && inQueue == total
}

// provenance names where the branch's commits actually live, in the words the
// reader has to act on. See StrandedBranch.Pushed.
func (b StrandedBranch) provenance() string {
	if b.Pushed {
		return "PUSHED"
	}
	return "LOCAL-ONLY — on no remote ref, and git-gc reaps the worktree"
}

// submitHint renders the remedy, and prints a paste-ready submit ONLY for a
// branch that is on origin.
//
// `pogo refinery submit` refuses a branch that is not pushed (mg-586d), so a
// single unconditional command would be unrunnable for exactly the population
// whose work is not durable — the mg-bfe0 failure, where the remedy told the
// reader two false things at once. With a mixed or local-only set the reader is
// sent to the instrument that renders the per-branch remedy correctly rather
// than being handed a command that cannot work.
//
// A QUEUED BRANCH IS THE SECOND WAY THAT COMMAND CAN BE WRONG, and it fails in
// the opposite direction (mg-64bb): the submit RUNS, and what it produces is a
// duplicate merge request for work whose merge is already in flight. The refusal
// that catches the unpushed case does not catch this one, so the guard has to be
// here. Measured on mg-a19a: exactly one branch, pushed, four notices — the
// shape that reaches this line — while its MR sat in the queue for ~36 minutes.
func submitHint(items []workitem.WorkItem, found map[string][]StrandedBranch) string {
	var only StrandedBranch
	n, id := 0, ""
	for _, it := range items {
		for _, b := range found[it.ID] {
			n++
			only, id = b, it.ID
		}
	}
	if n != 1 || !only.Pushed || only.Queued != nil {
		return "`pogo check-stranded` names the branch and the remedy for each"
	}
	return fmt.Sprintf("`pogo refinery submit %s --repo=%s --author=%s`", only.Branch, only.Repo, id)
}

// strandedDetails stamps the item->branch attribution onto the emitted event, so
// "aging because nobody dispatched it" and "aging because its work is already
// written" are countable apart in events.log rather than only distinguishable by
// reading prose.
func strandedDetails(items []workitem.WorkItem, found map[string][]StrandedBranch) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		for _, b := range found[it.ID] {
			row := map[string]any{
				"item_id":  it.ID,
				"branch":   b.Branch,
				"ref":      b.Ref,
				"pushed":   b.Pushed,
				"unmerged": b.Unmerged,
				"target":   b.Target,
				"repo":     b.Repo,
			}
			if b.PreRegistration != "" {
				row["pre_registration"] = b.PreRegistration
			}
			if b.Queued != nil {
				// So "held because its work was pushed and abandoned" and "held
				// because its merge is running" are countable apart in events.log.
				// They are the same exclusion and different emergencies: one needs
				// somebody to act, the other needs everybody not to.
				row["queued_mr"] = b.Queued.MR
				row["queued_status"] = b.Queued.Status
			}
			out = append(out, row)
		}
	}
	return out
}

// orTarget keeps an unread target from rendering as an empty comparison, which
// would read as "vs nothing" — a sentence whose missing half looks like a
// formatting bug rather than a gap.
func orTarget(target string) string {
	if target == "" {
		return "its target"
	}
	return target
}
