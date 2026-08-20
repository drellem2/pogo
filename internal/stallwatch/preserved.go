package stallwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/workitem"
)

// An available item whose work is sitting in a retained worktree — UNCOMMITTED
// (mg-836c), or COMMITTED and reachable from nothing but that tree (mg-fcba) —
// is not ready to dispatch.
//
// THE DEFECT THIS CLOSES. When a polecat exits holding uncommitted work, pogod
// preserves its worktree rather than reaping it, emits worktree_preserved, and
// mails the coordinator a precise per-tree notice — worktree, repo, work item,
// file list, and the prohibition in capitals. All of that works. None of it
// reaches this package: the item goes back to available/, so the standard stall
// notice calls it neglected and the priority wake tells the coordinator to
// "claim or dispatch now". A worker dispatched on that advice gets a FRESH
// worktree, cannot see the files, re-derives the work, and the original is
// destroyed by the next gc reap.
//
// The notice was right and it was not enough, for a reason worth stating: it
// fires ONCE, says so in its own text, and goes to ONE addressee. On 2026-08-19
// that addressee was itself one of the agents down in the 118-hour outage the
// notice was reporting on, and its mailbox held 905 unread. A one-shot message
// is exactly as reliable as its recipient. This check is the same knowledge
// re-derived from the trees on every tick, which is what makes it survive a
// recipient being down.
//
// WHY THE ITEMS ARE RE-REPORTED RATHER THAN SILENCED. Same rule as mg-1a8a's
// worked-but-unclaimed check, and it applies harder here: an item with a
// preserved tree needs a DECISION (commit the work and land it, rescue what is
// worth keeping, or rule it spent), and nothing else in the fleet is assigned to
// ask for one. Dropping these items from the dispatch notices without saying
// anything would trade a wrong instruction for a silent hole, and silence is
// what this package keeps relearning not to ship. So the FINDING survives and
// only the REMEDY changes.
//
// THE COMMITTED HALF (mg-fcba). mg-836c defined this population over `git
// status`, which is clean the moment a worker commits — so an item whose
// polecat was stopped AFTER committing and BEFORE pushing was suppressed
// nowhere and advertised everywhere. That is the state the nightly pre-deploy
// stop creates on purpose. The spawn-time gate is not the backstop here that it
// is for the uncommitted half: the stranded-work gate does refuse an unpushed
// polecat BRANCH (mg-bfe0), but a worktree on a DETACHED HEAD has no ref for
// any ref scan to find, and this package reads no refs at all. So the same
// probe now carries both halves and every check that consults it inherits
// both.

// categoryPreservedWorktree keys this check's cooldown and stamps its event. It
// is distinct from the dispatch categories on purpose: it is the opposite
// instruction to the same reader, and sharing a cooldown would let a busy
// dispatch queue silence it.
const categoryPreservedWorktree = "preserved_worktree"

// PreservedTree is one retained worktree holding work for an available item.
//
// It is a subset of gitgc.PreservedTree, restated here rather than imported,
// for the reason Workers is an interface: internal/stallwatch keeps no edge to
// the packages that do the work, so the notice text stays testable without a
// polecats directory on disk. cmd/pogod does the translation.
type PreservedTree struct {
	// Path is the worktree directory — the single most load-bearing field in
	// the notice, because the one thing the reader must not do is delete it.
	Path string
	// Branch is what that tree has checked out, or "" when it could not be
	// read.
	Branch string
	// Modified and Untracked split the dirty count. The split is the point:
	// a modified tracked file still has its committed version in the object
	// store, while an untracked path is on no branch, in no stash and on no
	// remote — the tree is its only copy on the machine.
	Modified, Untracked int
	// Outcome is what was established about the tree, in gitgc's vocabulary:
	// "preserved" (uncommitted work positively read), "unpushed" (clean, and
	// holding commits that exist nowhere else), or anything else — including
	// the ZERO VALUE — meaning nobody established what is in it.
	//
	// A string rather than the `Unread bool` this replaced, because the zero
	// value of that bool asserted "this tree was read" and the summary then
	// printed "0 modified, 0 untracked" about a tree nobody had looked at. That
	// is survivable while there is one shape of finding; with two it is not,
	// since an unset flag would now render a dirty tree as "clean — its work is
	// already committed", which is a false sentence rather than a thin one. The
	// zero value here falls into the unknown arm, so a caller that forgets to
	// set it understates instead of lying.
	Outcome string
	// Commits is how many commits this tree's HEAD reaches that exist nowhere
	// else — no ref under refs/remotes/origin/ holds them and none has a
	// patch-equivalent on the integration branch (mg-fcba). Meaningful only
	// when CommitsFinding is non-empty.
	Commits int
	// CommitsFinding is "" when the tree holds no such commits, "local-only"
	// when it does, and "unknown" when the question could not be answered.
	// Unknown is reported as ITSELF and is still a finding: a question we
	// failed to ask is not an answer of none.
	CommitsFinding string
	// Detached is true when the tree has no branch checked out. It is the worst
	// case and the reason the committed half needed covering at all: those
	// commits are held by this worktree's HEAD and by NO REF, so nothing that
	// scans refs — the stranded-work gate included — can see them.
	Detached bool
}

// summary renders one tree as a clause. Never a bare total — see the field docs
// for why the split carries the decision.
func (t PreservedTree) summary() string {
	var s string
	switch t.Outcome {
	case "preserved":
		s = fmt.Sprintf("%s (%d modified, %d untracked", t.Path, t.Modified, t.Untracked)
		if t.Untracked > 0 {
			s += " — untracked paths exist ONLY here"
		}
	case "unpushed":
		s = t.Path + " (clean — no uncommitted files"
	default:
		return t.Path + " (could NOT be read, so whether it holds work is unknown)"
	}
	switch t.CommitsFinding {
	case "local-only":
		// Only a count that was READ is printed. Commits==0 under a local-only
		// verdict means the list could not be re-read, and "0 commit(s) exist
		// NOWHERE ELSE" is a sentence whose number says the opposite of its
		// verb — the number being the half that survives a skim.
		if t.Commits == 0 {
			s += "; commit(s) exist NOWHERE ELSE, how many is UNKNOWN"
		} else {
			s += fmt.Sprintf("; %d commit(s) exist NOWHERE ELSE", t.Commits)
		}
		if t.Detached {
			s += " and NO REF holds them — the tree is on a detached HEAD"
		}
	case "unknown":
		s += "; whether it holds commits that exist nowhere else could NOT be established"
	}
	// A detached tree said so above; ", on HEAD" would read as a branch name.
	if t.Branch != "" && t.Branch != "HEAD" {
		s += ", on " + t.Branch
	}
	return s + ")"
}

// PreservedWork is one snapshot of which available work items have a retained
// worktree holding uncommitted work.
type PreservedWork struct {
	// Items maps work-item id -> the trees retained for it. Absent means no
	// such tree was found, which — see Uncertain — is not proof that none
	// exists.
	Items map[string][]PreservedTree
	// Uncertain, when non-empty, says why Items may be INCOMPLETE: typically
	// the polecats directory could not be read. An incomplete snapshot means
	// some held item still reads as dispatchable — the pre-fix behaviour, loud
	// rather than silent — so the note travels with the dispatch advice rather
	// than replacing it.
	Uncertain string
}

// Preserved probes which available work items have a retained worktree.
//
// An interface for the same reasons Workers and Capacity are: no edge from this
// package to internal/gitgc, and testable notice text. cmd/pogod supplies the
// production implementation.
type Preserved interface {
	// Retained returns the snapshot for the given work-item ids. The caller
	// supplies the ids so the probe can be cheap — resolving a tree to an item
	// is a directory-name question, and only the named items' trees ever get a
	// `git status`.
	//
	// known=false means the question could NOT be answered at all, and it is
	// NOT "nothing is retained". On known=false every check behaves exactly as
	// it did before this fix: the items are reported as dispatchable. That is
	// the loud direction, chosen for the reason Workers.InFlight chooses it — a
	// false "dispatch this" is self-correcting (the spawn-time gate refuses it,
	// mg-836c), while a false silence looks like a healthy queue.
	Retained(ids []string) (PreservedWork, bool)
}

// PreservedFunc adapts a function to Preserved.
type PreservedFunc func(ids []string) (PreservedWork, bool)

// Retained implements Preserved.
func (f PreservedFunc) Retained(ids []string) (PreservedWork, bool) { return f(ids) }

// probePreserved asks the wired Preserved probe once per tick, for the items
// the dispatch checks would otherwise advertise.
//
// Once per TICK, not once per check, so an item cannot read as held by one
// check and free to another within the same sample. With no probe wired this
// returns the zero value, under which every check keeps its pre-mg-836c
// behaviour exactly.
func (w *Watcher) probePreserved(items []workitem.WorkItem) PreservedWork {
	if w.preserved == nil || len(items) == 0 {
		return PreservedWork{}
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if it.ID != "" {
			ids = append(ids, it.ID)
		}
	}
	held, known := w.preserved.Retained(ids)
	if !known {
		return PreservedWork{}
	}
	return held
}

// trees returns the retained trees for an item, if the snapshot names any.
func (p PreservedWork) trees(id string) ([]PreservedTree, bool) {
	if len(p.Items) == 0 {
		return nil, false
	}
	t, ok := p.Items[id]
	if !ok || len(t) == 0 {
		return nil, false
	}
	return t, true
}

// uncertaintyNote is the sentence a DISPATCH notice appends when the preserved
// snapshot may be incomplete.
//
// Attached to the dispatch notices and not to this file's own notice, for
// WorkInFlight.uncertaintyNote's reason: an incomplete snapshot can only cause a
// held item to be MISSED, never to be invented.
func (p PreservedWork) uncertaintyNote() string {
	if p.Uncertain == "" {
		return ""
	}
	return " (Preserved-worktree attribution may be incomplete — " + p.Uncertain +
		" — so one of these could have uncommitted work in a retained tree; `pogo gc --list-preserved` before dispatching.)"
}

// checkPreservedWorktrees reports available items whose work is already sitting
// uncommitted in a retained worktree, telling the coordinator NOT to dispatch
// them and naming what the decision is.
//
// items is the caller's already-listed available/ snapshot, flight the tick's
// in-flight probe and held the tick's preserved probe, so this costs one pass
// over a slice. The population is exactly the items the two dispatch checks
// dropped for this reason: same watchedForDispatch gate, and items a live worker
// holds are left to checkWorkedButUnclaimed, so the populations stay disjoint.
//
// No age threshold gates it, for checkWorkedButUnclaimed's reason: an item whose
// only copy of its work is unreachable to the fleet is an anomaly the instant it
// exists, and the cost of missing it — a re-derivation plus a reap — is highest
// early. The per-item backoff still bounds how often it repeats.
func (w *Watcher) checkPreservedWorktrees(now time.Time, items []workitem.WorkItem, flight WorkInFlight, held PreservedWork) {
	if len(held.Items) == 0 {
		return
	}

	var stuck []workitem.WorkItem
	trees := make(map[string][]PreservedTree, len(items))
	for _, it := range items {
		if !w.watchedForDispatch(it) {
			continue
		}
		// A live worker on the item is already reported by
		// checkWorkedButUnclaimed with the same instruction (do not dispatch),
		// and its tree is that worker's working copy rather than a retained
		// one. Two notices saying the same thing about one item is how a
		// channel gets skimmed.
		if _, worked := flight.worker(it.ID); worked {
			continue
		}
		t, ok := held.trees(it.ID)
		if !ok {
			continue
		}
		stuck = append(stuck, it)
		trees[it.ID] = t
	}
	sort.Slice(stuck, func(i, j int) bool { return stuck[i].ID < stuck[j].ID })

	due, sel := w.selectDue(categoryPreservedWorktree, stuck, now, w.cfg.NudgeCooldown)
	if len(due) == 0 {
		return
	}

	ids := itemIDs(due)
	msg := fmt.Sprintf(
		"stall-watch: %d work item(s) sit in available/ while their work already exists in a "+
			"retained worktree and nowhere else — UNCOMMITTED, or COMMITTED and held by nothing but "+
			"that tree — %s. This is NOT a dispatch request, it is the "+
			"opposite. A polecat spawned at one of these gets a FRESH worktree, cannot see any of it, "+
			"and re-derives work that already exists; the original is then destroyed by the next gc "+
			"reap. DO NOT clear this by removing the worktree — nothing else on this machine holds "+
			"it. Read it (`pogo gc --list-preserved` lists every retained tree, splits "+
			"modified from untracked, and names the commits that exist nowhere else), then decide: "+
			"make the work reachable on a branch and land it, "+
			"rescue what is worth keeping by hand, or rule the work spent. `pogo agent spawn-polecat` "+
			"refuses these items until then (mg-836c, mg-fcba).",
		len(due), strings.Join(preservedSentences(due, trees), "; "))
	msg += sel.repeatNotice()

	details := map[string]any{
		"category":           categoryPreservedWorktree,
		"watched_agent":      w.cfg.Agent,
		"item_count":         len(due),
		"item_ids":           ids,
		"worktrees":          preservedDetails(due, trees),
		"oldest_age_seconds": now.Sub(oldestModTime(due)).Seconds(),
	}
	sel.stampDetails(details)
	w.fire(categoryPreservedWorktree, Notice{
		Subject: subject(nItems(len(due))+" unclaimed with UNCOMMITTED work preserved", now.Sub(oldestModTime(due)), ids),
		Message: msg,
	}, details)
}

// preservedSentences renders one clause per item naming its trees.
func preservedSentences(items []workitem.WorkItem, trees map[string][]PreservedTree) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		parts := make([]string, 0, len(trees[it.ID]))
		for _, t := range trees[it.ID] {
			parts = append(parts, t.summary())
		}
		out = append(out, it.ID+": "+strings.Join(parts, ", "))
	}
	return out
}

// preservedDetails stamps the item->worktree attribution onto the emitted
// event, so "aging because nobody dispatched it" and "aging because its work is
// stuck in a tree" are countable apart in events.log rather than only
// distinguishable by reading prose.
func preservedDetails(items []workitem.WorkItem, trees map[string][]PreservedTree) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		for _, t := range trees[it.ID] {
			out = append(out, map[string]any{
				"item_id":         it.ID,
				"worktree":        t.Path,
				"branch":          t.Branch,
				"modified_paths":  t.Modified,
				"untracked_paths": t.Untracked,
				"unread":          t.Outcome != "preserved" && t.Outcome != "unpushed",
				"outcome":         t.Outcome,
				// The committed half, stamped so "this item is held because its
				// files are unsaved" and "held because its commits never left
				// the box" are countable apart in events.log rather than only
				// distinguishable by reading the prose (mg-fcba).
				"commits_finding":     t.CommitsFinding,
				"unreachable_commits": t.Commits,
				"detached":            t.Detached,
			})
		}
	}
	return out
}
