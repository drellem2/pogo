package strandwatch

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/strandedwork"
	"github.com/drellem2/pogo/internal/vcs"
)

// Stranded work on an enth-backed repository (docs/design/enth-vcs.md §5,
// slice 6b, mg-28cbc): S2's candidate set on enth.
//
// AN ENTH REPO HAS NO POLECAT BRANCHES TO LIST. A worker's output is the
// Changes it made, tied to its item by a `Pogo-Item` trailer, and the one
// query that finds them is
//
//	enth changes --unlanded
//
// read through the repo's enthd. Every unlanded Change carrying
// `Pogo-Item=<id>` is a candidate, and the item's STATUS decides what it is:
//
//   - an open item (available, pending, claimed) whose worker is not running
//     is stranded — the git path's row, found by trailer instead of by branch;
//   - a DONE item with unlanded work is stranded too, and it is the case this
//     slice exists for: the item closed (or was redispatched and its sibling
//     closed it) and the work never landed. The git sweep leaves closed items
//     to the worktree scan; on enth there is no branch to outlive the item, so
//     the trailer listing is the only place this work is visible at all;
//   - archived and shelved items are excluded and counted, as the git sweep's
//     open-item bound excludes them.
//
// A landed Change is not in the listing: its land record names it (E3), and
// that is exact where git's patch-id cherry is a heuristic. So nothing here
// needs a second, content-level opinion.
//
// A Change with NO Pogo-Item trailer cannot be matched to an item. It is
// counted on the repo's coverage line rather than dropped; a checkout whose
// base is one is reported as stranded work through the worktree list (slice 6's
// trailer-independent signal).

// EnthSource is one enth-backed repository, as the sweep reads it.
type EnthSource struct {
	// Remote and Repo are the enthd and repository name the project record
	// names, for the remedy line.
	Remote string
	Repo   string
	// Trunk is the ref work lands on (vcs.DefaultEnthTrunk when the record
	// names none).
	Trunk string
	// Unlanded lists every unlanded Change in the repository, oldest first,
	// with its trailers: `enth changes --unlanded` with no trailer filter.
	Unlanded func() ([]vcs.Change, error)
}

// EnthWork is the enth half of a row: the unlanded Changes one item's
// trailer names.
type EnthWork struct {
	Remote string `json:"remote"`
	Repo   string `json:"enth_repo"`
	// Changes are the unlanded Change ids carrying this item's trailer, oldest
	// first.
	Changes []string `json:"changes"`
	// Workers are the distinct Pogo-Worker trailers on them.
	Workers []string `json:"workers,omitempty"`
	// Stacks is how many stacks the Changes form. More than one is the
	// ambiguous case `pogo presubmit --item` refuses.
	Stacks int `json:"stacks"`
	// Newest is the newest Change's committer time.
	Newest time.Time `json:"newest,omitempty"`
}

// ItemQueueKey is the identity of an enth merge request in the queue: it has
// no branch, and is submitted by its work item.
func ItemQueueKey(repo, item string) string { return QueueKey(repo, "\x00item\x00"+item) }

// enthTarget is the target an enth row's submit names: opts.Target, else the
// repo's trunk as a branch name.
func enthTarget(target string, src EnthSource) string {
	if target != "" {
		return target
	}
	return strings.TrimPrefix(orDefault(src.Trunk, vcs.DefaultEnthTrunk), "refs/heads/")
}

// isOpenStatus reports whether status is one the open-item join covers.
func isOpenStatus(status string) bool {
	for _, s := range OpenStatuses {
		if strings.EqualFold(s, status) {
			return true
		}
	}
	return false
}

// enthScan is the per-repo result of sweeping one enth repository.
type enthScan struct {
	rows     []Row
	excluded []Excluded
	// changes and untrailed are the coverage counts: unlanded Changes listed,
	// and how many of them carry no Pogo-Item trailer.
	changes   int
	untrailed int
	// closedItems is how many items outside the open set had unlanded work
	// here, so the frame can say the join was widened.
	closedItems int
}

// scanEnthRepo turns one enth repository's unlanded listing into rows.
//
// open are the open items, by id. lookup resolves an item the open set does
// not hold (a done item, typically); nil means closed items cannot be judged,
// and each one becomes an unjudged row rather than a silent drop.
func scanEnthRepo(repo string, src EnthSource, changes []vcs.Change, open map[string]Item,
	lookup func(string) (Item, error), live map[string]bool, queued map[string]QueuedRequest, target string) enthScan {
	var out enthScan
	out.changes = len(changes)
	byItem := map[string][]vcs.Change{}
	var ids []string
	for _, c := range changes {
		id := c.Trailer(vcs.TrailerItem)
		if id == "" {
			out.untrailed++
			continue
		}
		if _, ok := byItem[id]; !ok {
			ids = append(ids, id)
		}
		byItem[id] = append(byItem[id], c)
	}
	sort.Strings(ids)
	tgt := enthTarget(target, src)

	for _, id := range ids {
		cs := byItem[id]
		work := NewEnthWork(src, cs)
		branchLabel := EnthLabel(work)

		it, ok := open[id]
		if !ok {
			var err error
			switch {
			case lookup == nil:
				err = fmt.Errorf("no work-item lookup was configured, so an item outside the open set could not be read")
			default:
				it, err = lookup(id)
			}
			if err != nil {
				out.rows = append(out.rows, Row{
					Item: Item{ID: id, Repo: repo}, Branch: branchLabel, Kind: KindUnjudged,
					Target: tgt, Enth: work,
					Error: fmt.Sprintf("the %s trailer names %s and its status could not be read: %v",
						vcs.TrailerItem, id, err),
				})
				continue
			}
			if it.ID == "" {
				it.ID = id
			}
		}
		if it.Repo == "" {
			it.Repo = repo
		}
		if !isOpenStatus(it.Status) {
			if !strings.EqualFold(it.Status, "done") {
				out.excluded = append(out.excluded, Excluded{ItemID: id, Branch: branchLabel,
					Reason: fmt.Sprintf("item is %s; unlanded enth work on archived and shelved items is outside this sweep", orDefault(it.Status, "of no status"))})
				continue
			}
			out.closedItems++
		}

		// A RUNNING WORKER'S CHANGES ARE WORK IN PROGRESS, exactly as a running
		// polecat's branch is. Matched by its Pogo-Worker trailer, and by the item
		// (a live polecat on this item under another name).
		reason := ""
		for _, w := range append(append([]string(nil), work.Workers...), "") {
			if reason = excludedBecause(strandedwork.BranchPrefix+w, it, live, repo); reason != "" {
				break
			}
		}
		if reason != "" && isOpenStatus(it.Status) {
			out.excluded = append(out.excluded, Excluded{ItemID: id, Branch: branchLabel, Reason: reason})
			continue
		}

		row := Row{Item: it, Branch: branchLabel, Kind: KindStranded, Target: tgt,
			Unmerged: len(cs), Enth: work, TipTime: work.Newest}
		if q, inQueue := queued[ItemQueueKey(repo, id)]; inQueue {
			if strings.EqualFold(it.Status, "claimed") {
				out.excluded = append(out.excluded, Excluded{ItemID: id, Branch: branchLabel,
					Reason: "already in the refinery queue (" + q.MR + "), and the item is claimed"})
				continue
			}
			row.Kind = KindInFlight
			row.Queued = &q
		}
		out.rows = append(out.rows, row)
	}
	return out
}

// NewEnthWork is the enth half of a row for one item's unlanded Changes cs
// (oldest first, every one carrying that item's trailer). It is exported so
// pogod's release reporter and restart sweep (mg-35ae4) describe enth work
// exactly as this sweep does, rather than through a second implementation.
func NewEnthWork(src EnthSource, cs []vcs.Change) *EnthWork {
	work := &EnthWork{Remote: src.Remote, Repo: src.Repo, Stacks: len(vcs.Stacks(cs))}
	seenWorker := map[string]bool{}
	for _, c := range cs {
		work.Changes = append(work.Changes, c.ID)
		if w := c.Trailer(vcs.TrailerWorker); w != "" && !seenWorker[w] {
			seenWorker[w] = true
			work.Workers = append(work.Workers, w)
		}
		if c.Created.After(work.Newest) {
			work.Newest = c.Created
		}
	}
	return work
}

// EnthLabel is what an enth row prints where a git row prints its branch.
// w must hold at least one Change.
func EnthLabel(w *EnthWork) string {
	return fmt.Sprintf("enth:%s(%d change(s))", short(w.Changes[len(w.Changes)-1]), len(w.Changes))
}

// enthRemedy is the remedy line of a row carrying enth work.
func (r Row) enthRemedy() string {
	w := r.Enth
	list := fmt.Sprintf("enth --remote %s --repo %s changes --unlanded --trailer %s=%s",
		w.Remote, w.Repo, vcs.TrailerItem, r.Item.ID)
	switch r.Kind {
	case KindUnjudged:
		return fmt.Sprintf("mg show %s   # its status could not be read, so these %d unlanded Change(s) were NOT judged; then `%s`",
			r.Item.ID, len(w.Changes), list)
	case KindInFlight:
		return fmt.Sprintf("pogo refinery show %s   # do NOT dispatch at %s — its enth work is IN THE QUEUE (%s) "+
			"on %s; do NOT submit it again either", r.queuedMR(), r.Item.ID, r.queuedStatus(), r.Target)
	case KindRepoUnreadable:
		return fmt.Sprintf("enth --remote %s --repo %s changes --unlanded   # reproduce the failure; "+
			"no trailer listing, so no work was looked for on this item", w.Remote, w.Repo)
	}
	if w.Stacks > 1 {
		return fmt.Sprintf("%s   # %d STACKS from worker(s) %s — `pogo presubmit` refuses an ambiguous "+
			"listing; choose one and land it with --worker; do NOT dispatch at %s",
			list, w.Stacks, strings.Join(w.Workers, ", "), r.Item.ID)
	}
	// An enth repo has no merge queue (enth-vcs §6, mg-5b904): the work is
	// landed by hand, under a request id of its own, and recorded with pogo
	// land, which closes the item. Nothing tests it first, so the line says so.
	out := "/tmp/" + r.Item.ID + "-land.out"
	land := fmt.Sprintf("enth --remote %s --repo %s land %s --request-id %s/recover-1 > %s && pogo land --item=%s --repo=%s --landed-file=%s",
		w.Remote, w.Repo, strings.Join(w.Changes, " "), r.Item.ID, out, r.Item.ID, r.Item.Repo, out)
	if r.Target != "" && r.Target != "main" {
		land += " --target=" + r.Target
	}
	if strings.EqualFold(r.Item.Status, "done") {
		return fmt.Sprintf("%s   # the item is DONE and this work NEVER LANDED — read it first (`%s`), and build it (nothing tests a land); "+
			"land it, or retire it with a Pogo-Supersedes Change", land, list)
	}
	return fmt.Sprintf("%s   # UNTESTED: build it first, or adopt it (spawn-polecat --stranded-adopt) so a worker tests and lands it; do NOT dispatch at %s", land, r.Item.ID)
}
