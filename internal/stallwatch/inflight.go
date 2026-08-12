package stallwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/workitem"
)

// An available item with a live worker on it is not neglected (mg-1a8a).
//
// THE DEFECT THIS CLOSES. pogod takes a work item's claim at spawn (mg-7254),
// but that claim FAILS OPEN: on a not-found or unreadable store the polecat is
// dispatched anyway and the item stays in available/. Every check in this
// package infers ownership from item status, so from that moment all of them are
// wrong in the same direction — the standard stall notice calls the item
// neglected, the priority wake tells the coordinator to "claim or dispatch now",
// and a coordinator acting on that nag spawns a SECOND polecat onto work already
// in progress. Two workers on one item means two branches touching the same
// files, which is the concurrent-edit shape that has cost this fleet repeated
// rebase conflicts. The fail-open log line at the spawn point predicted this
// outcome in prose; nothing acted on it.
//
// THE SHAPE OF THE FIX. The claim field cannot carry the distinction — it is
// already overloaded with "in progress", "finished, awaiting a human" (mg-ed7b)
// and now "in progress but unclaimable" — so the repair is a SECOND source of
// truth rather than a better claim: pogod knows which polecats are alive and
// which item each was dispatched at, independently of whether the claim stuck.
// The two dispatch checks consult it and drop those items from their population;
// this check picks them up and says the opposite thing about them.
//
// WHY THE ITEMS ARE RE-REPORTED RATHER THAN SILENCED. Suppressing them would fix
// the double-dispatch and hide the anomaly, and an unclaimed-but-worked item is
// a defect somebody must repair — the claim is what `mg done` needs at the END
// of the work, so the polecat discovers it after doing everything. Silence is
// also what this package keeps relearning not to ship (mg-4bd4, mg-1693): a
// detector that goes quiet is indistinguishable from a healthy queue. So the
// FINDING survives and only the REMEDY changes, which is the same move mg-dd77
// made for items in a repo at its worker cap.

// categoryWorkedUnclaimed keys this check's cooldown and stamps its event. It is
// distinct from the two dispatch categories on purpose: it is the opposite
// instruction to the same reader, and sharing a cooldown would let a busy
// dispatch queue silence it.
const categoryWorkedUnclaimed = "worked_unclaimed"

// InFlightWorker is the worker a work item is being worked by right now.
type InFlightWorker struct {
	// Name is the worker's agent name.
	Name string
	// PID is the worker's process id, or 0 when it is not known. It is carried so
	// the notice can name the pid `mg claim --pid` wants, rather than sending the
	// reader off to look it up.
	PID int
	// Evidence is how pogod knows — "registry" for this process's own live
	// registry, "witness" for a polecat known only from the persisted witness.
	// A registry entry is this pogod's own bookkeeping; a witness entry counts a
	// polecat as live when its identity could not be DISPROVED, which is weaker
	// and is why the notice says which one it is looking at.
	Evidence string
}

// WorkInFlight is one snapshot of which work items have workers on them.
type WorkInFlight struct {
	// Items maps work-item id -> the worker on it. Absent means "no live worker
	// is attributed to this item", which — see Uncertain — is not the same as
	// proof that none exists.
	Items map[string]InFlightWorker
	// Uncertain, when non-empty, says why Items may be INCOMPLETE: typically the
	// persisted polecat witness could not be read, so survivors of an earlier
	// pogod are missing. An incomplete snapshot means some worked item still
	// reads as neglected — the pre-fix behaviour, loud rather than silent — so
	// the note travels with the dispatch advice rather than replacing it.
	Uncertain string
}

// Workers probes which work items are being worked right now.
//
// An interface for the same two reasons Capacity is one: internal/stallwatch
// keeps no edge to internal/agent, and the notice text stays testable without a
// live fleet. cmd/pogod supplies the production implementation as a closure over
// its *agent.Registry.
type Workers interface {
	// InFlight returns the current snapshot.
	//
	// known=false means the question could NOT be answered at all, and it is NOT
	// "nothing is in flight". On known=false every check behaves exactly as it
	// did before this fix: the items are reported as neglected. That is the loud
	// direction, chosen deliberately — a false "dispatch this" is self-correcting
	// (the coordinator checks `pogo agent list`, or the spawn is refused),
	// whereas a false silence looks like a healthy queue.
	InFlight() (WorkInFlight, bool)
}

// WorkersFunc adapts a function to Workers.
type WorkersFunc func() (WorkInFlight, bool)

// InFlight implements Workers.
func (f WorkersFunc) InFlight() (WorkInFlight, bool) { return f() }

// probeInFlight asks the wired Workers probe once per tick.
//
// Once per TICK, not once per check: the three checks that read available/ share
// one snapshot, so a polecat cannot appear in-flight to one of them and absent
// to another within the same sample. With no probe wired this returns the zero
// value, under which every check keeps its pre-mg-1a8a behaviour exactly.
func (w *Watcher) probeInFlight() WorkInFlight {
	if w.workers == nil {
		return WorkInFlight{}
	}
	flight, known := w.workers.InFlight()
	if !known {
		return WorkInFlight{}
	}
	return flight
}

// worker returns the live worker on an item, if the snapshot names one.
func (f WorkInFlight) worker(id string) (InFlightWorker, bool) {
	if len(f.Items) == 0 {
		return InFlightWorker{}, false
	}
	who, ok := f.Items[id]
	return who, ok
}

// uncertaintyNote is the sentence a DISPATCH notice appends when the in-flight
// snapshot may be incomplete, so an imperative that could be aimed at
// already-worked items says so.
//
// It is deliberately attached to the dispatch notices and not to this file's own
// notice: an incomplete snapshot can only cause a worked item to be MISSED
// (reported as neglected), never to be invented.
func (f WorkInFlight) uncertaintyNote() string {
	if f.Uncertain == "" {
		return ""
	}
	return " (Live-worker attribution may be incomplete — " + f.Uncertain +
		" — so one of these could already have a worker on it; check `pogo agent list` before dispatching.)"
}

// checkWorkedButUnclaimed reports available items that a live worker is already
// on, telling the coordinator NOT to dispatch them and naming the repair.
//
// items is the caller's already-listed available/ snapshot and flight the tick's
// single in-flight probe, so this costs one pass over a slice. The population is
// exactly the items the two dispatch checks dropped: same watchedForDispatch
// gate, so a `human`/`parked`/`blocked:` item stays silent here too, and the
// three populations remain disjoint.
//
// No age threshold gates it. The two dispatch checks wait out an age because a
// freshly-filed item is not yet evidence of anything; a worked-but-unclaimed
// item is an anomaly the instant it exists, and its cost — a second dispatch —
// is highest in the first minutes, before the first polecat has pushed anything.
// The per-item backoff still bounds how often it repeats.
func (w *Watcher) checkWorkedButUnclaimed(now time.Time, items []workitem.WorkItem, flight WorkInFlight) {
	if len(flight.Items) == 0 {
		return
	}

	var worked []workitem.WorkItem
	whos := make(map[string]InFlightWorker, len(items))
	for _, it := range items {
		if !w.watchedForDispatch(it) {
			continue
		}
		who, ok := flight.worker(it.ID)
		if !ok {
			continue
		}
		worked = append(worked, it)
		whos[it.ID] = who
	}
	sort.Slice(worked, func(i, j int) bool { return worked[i].ID < worked[j].ID })

	due, sel := w.selectDue(categoryWorkedUnclaimed, worked, now, w.cfg.NudgeCooldown)
	if len(due) == 0 {
		return
	}

	ids := itemIDs(due)
	msg := fmt.Sprintf(
		"stall-watch: %d work item(s) sit in available/ while a LIVE WORKER is already on them — %s. "+
			"This is NOT a dispatch request, it is the opposite: their status says unclaimed, so the "+
			"standard notice and priority-wake would have told you to dispatch them, and a second polecat "+
			"on one item means two branches touching the same files. The claim did not stick — claim-at-spawn "+
			"fails open on a not-found or unreadable store (grep events.log for work_item_claim_at_spawn_failed). "+
			"Confirm each worker with `pogo agent list`; if it is there, restore the invariant with %s, and if "+
			"it is GONE the item really is free and dispatching it is correct.",
		len(due), strings.Join(workerSentences(due, whos), "; "), claimHint(due, whos))
	msg += sel.repeatNotice()

	details := map[string]any{
		"category":           categoryWorkedUnclaimed,
		"watched_agent":      w.cfg.Agent,
		"item_count":         len(due),
		"item_ids":           ids,
		"workers":            workerDetails(due, whos),
		"oldest_age_seconds": now.Sub(oldestModTime(due)).Seconds(),
	}
	sel.stampDetails(details)
	w.fire(categoryWorkedUnclaimed, Notice{
		Subject: subject(nItems(len(due))+" unclaimed but WORKED", now.Sub(oldestModTime(due)), ids),
		Message: msg,
	}, details)
}

// workerSentences renders one clause per item naming its worker and the evidence
// behind it. The evidence is the load-bearing half: "witness" means the process
// could not be disproved rather than positively seen, and a reader deciding
// whether to dispatch is entitled to that difference.
func workerSentences(items []workitem.WorkItem, whos map[string]InFlightWorker) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		who := whos[it.ID]
		name := who.Name
		if name == "" {
			name = "unnamed worker"
		}
		if who.PID > 0 {
			out = append(out, fmt.Sprintf("%s is being worked by %s (pid %d, %s)", it.ID, name, who.PID, who.Evidence))
			continue
		}
		out = append(out, fmt.Sprintf("%s is being worked by %s (%s)", it.ID, name, who.Evidence))
	}
	return out
}

// claimHint renders the repair command. It names the WORKER's pid rather than
// leaving the reader to run a bare `mg claim`, which would stamp the caller's
// own pid onto the claim — pogo reads that pid as the polecat's in two places
// (claimrestamp.go, spawnclaimadopt.go), so a claim restored with the wrong one
// trades this defect for a quieter one.
func claimHint(items []workitem.WorkItem, whos map[string]InFlightWorker) string {
	if len(items) == 1 {
		if who := whos[items[0].ID]; who.PID > 0 {
			return fmt.Sprintf("`mg claim %s --pid %d`", items[0].ID, who.PID)
		}
		return fmt.Sprintf("`mg claim %s --pid <the worker's pid>`", items[0].ID)
	}
	return "`mg claim <id> --pid <that worker's pid>`"
}

// workerDetails stamps the item->worker attribution onto the emitted event, so
// "aging because nobody dispatched it" and "aging because its claim failed open"
// are countable apart in events.log rather than only distinguishable by reading
// prose.
func workerDetails(items []workitem.WorkItem, whos map[string]InFlightWorker) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		who := whos[it.ID]
		d := map[string]any{
			"item_id":  it.ID,
			"polecat":  who.Name,
			"evidence": who.Evidence,
		}
		if who.PID > 0 {
			d["pid"] = who.PID
		}
		out = append(out, d)
	}
	return out
}
