package stallwatch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// RepoCapacity is one repository's dispatch capacity as the per-repo worker cap
// sees it right now. It is the stall-watch-shaped view of
// agent.RepoOccupancy — the same numbers the spawn point enforces on, carried
// through an interface so this package keeps no edge to internal/agent.
//
// Only the fields a NOTICE needs are here. In particular Polecats is carried
// rather than dropped: naming the workers occupying a saturated repo is what
// turns "you cannot dispatch" into a question the coordinator can act on (is
// one of these wedged?), which is the whole point of mg-dd77.
type RepoCapacity struct {
	// Repo is the normalized repository path these numbers are about.
	Repo string
	// Count is the number of live workers attributed to Repo.
	Count int
	// Cap is the effective ceiling right now. Zero means the cap is disarmed.
	Cap int
	// Polecats names the occupying workers, sorted. May be empty even when
	// Count > 0 if the source could not name them.
	Polecats []string
	// AtCap is what the spawn point would DO — true when a dispatch into Repo
	// would be refused right now. It is taken from the enforcing side rather
	// than recomputed from Count and Cap, so this watcher can never disagree
	// with the gate it is reporting on.
	AtCap bool
	// Uncertain, when non-empty, is why Count may be an undercount (typically:
	// the persisted polecat witness could not be read, or live workers could not
	// be attributed to any repo). The cap FAILS OPEN on those, so an uncertain
	// repo is still reported as dispatchable — the note travels with the advice
	// rather than replacing it.
	Uncertain string
}

// Capacity probes a repository's dispatch capacity.
//
// It is an interface, and the agent registry is reached through it rather than
// imported, both to keep internal/stallwatch free of an edge to internal/agent
// and so the notice text can be tested without a live fleet. cmd/pogod supplies
// the production implementation as a closure over its *agent.Registry.
type Capacity interface {
	// CapacityFor reports repo's occupancy against the per-repo worker cap.
	//
	// known=false means the occupancy could NOT be established, and it is NOT
	// "there is room": the third branch of mg-dd77 exists precisely so a notice
	// that cannot tell says so, instead of defaulting to "dispatch them".
	CapacityFor(repo string) (c RepoCapacity, known bool)
}

// CapacityFunc adapts a function to Capacity.
type CapacityFunc func(repo string) (RepoCapacity, bool)

// CapacityFor implements Capacity.
func (f CapacityFunc) CapacityFor(repo string) (RepoCapacity, bool) { return f(repo) }

// repoBucket is one repository's share of a notice's items, together with what
// the cap says about that repository.
type repoBucket struct {
	repo string
	ids  []string
	cap  RepoCapacity
}

// capacitySplit partitions one notice's items by what the per-repo cap would do
// with each of them right now. It is the data behind mg-dd77's three
// situations: free slots, at cap, and cannot-determine.
//
// Every item lands in exactly one of the three, and NONE are dropped — the
// finding ("these items are aging") was always true and stays in the message.
// What changes is the remedy named alongside it.
//
// # What "free" here does and does not promise
//
// The per-repo cap is one of several refusals the spawn point can issue. Two
// others are already handled upstream of this split, by silence:
// non-dispatchable assignees and `stage: gated` items never reach a notice at
// all (watchedForDispatch). But the HOST LOAD gate (agent.loadGateRefusal) and
// the stranded-push gate (agent.strandedGate) are not consulted here, so a
// "free slots" verdict means "the per-repo cap would let this through", not
// "this dispatch will succeed". That is a narrower claim than the message makes
// today, and it is stated here rather than hedged into the text: the same
// grouping would extend to another refusal by adding a probe, and a reader
// chasing a surviving false imperative should look at those two first.
type capacitySplit struct {
	// free lists the ids the cap would let through now, in the caller's order.
	free []string
	// capped groups the refused ids by repository, sorted by repository path.
	capped []repoBucket
	// cappedIDs is every id in capped, for event stamping.
	cappedIDs []string
	// unknown groups ids whose repository occupancy could not be established.
	unknown []repoBucket
	// unknownIDs is every id in unknown, for event stamping.
	unknownIDs []string
	// uncertain carries one note per repository whose count may be an
	// undercount. Those repos are still in free — see RepoCapacity.Uncertain.
	uncertain []string
	// probed is false when no Capacity was wired, which is the signal to render
	// the pre-mg-dd77 wording rather than to claim anything about capacity.
	probed bool
}

// splitByCapacity groups items by the repository they name and asks the
// capacity probe about each distinct repository once.
//
// Cost: one probe per distinct repo per NOTICE, not per tick — every caller
// runs this after its per-item cooldown has already decided a nudge is going
// out. On this fleet that is two probes on the rare tick that fires.
//
// An item naming no repository is free by construction, and is not probed: a
// --no-worktree dispatch contends for no repository's test suite, and the cap
// leaves it uncapped for that reason (agent.RepoOccupancyFor returns early on
// an empty path). Probing "" would invite a wiring where the empty bucket
// answers for some other repo.
func (w *Watcher) splitByCapacity(items []workitem.WorkItem) capacitySplit {
	var s capacitySplit
	if w.capacity == nil {
		s.free = itemIDs(items)
		return s
	}
	s.probed = true

	type probed struct {
		cap   RepoCapacity
		known bool
	}
	seen := make(map[string]probed)
	cappedAcc := make(map[string]*repoBucket)
	unknownAcc := make(map[string]*repoBucket)
	uncertain := make(map[string]string)

	for _, it := range items {
		repo := config.NormalizeRepo(it.Repo)
		if repo == "" {
			s.free = append(s.free, it.ID)
			continue
		}
		p, ok := seen[repo]
		if !ok {
			c, known := w.capacity.CapacityFor(repo)
			if strings.TrimSpace(c.Repo) == "" {
				c.Repo = repo
			}
			p = probed{cap: c, known: known}
			seen[repo] = p
		}
		switch {
		case !p.known:
			bucketAdd(unknownAcc, repo, p.cap, it.ID)
		case p.cap.AtCap:
			bucketAdd(cappedAcc, repo, p.cap, it.ID)
		default:
			s.free = append(s.free, it.ID)
			if p.cap.Uncertain != "" {
				uncertain[repo] = p.cap.Uncertain
			}
		}
	}

	s.capped, s.cappedIDs = sortedBuckets(cappedAcc)
	s.unknown, s.unknownIDs = sortedBuckets(unknownAcc)
	for _, repo := range sortedKeys(uncertain) {
		s.uncertain = append(s.uncertain, fmt.Sprintf("%s (%s)", repo, uncertain[repo]))
	}
	return s
}

func bucketAdd(acc map[string]*repoBucket, repo string, c RepoCapacity, id string) {
	b, ok := acc[repo]
	if !ok {
		b = &repoBucket{repo: repo, cap: c}
		acc[repo] = b
	}
	b.ids = append(b.ids, id)
}

func sortedBuckets(acc map[string]*repoBucket) ([]repoBucket, []string) {
	if len(acc) == 0 {
		return nil, nil
	}
	out := make([]repoBucket, 0, len(acc))
	for _, repo := range sortedKeys(acc) {
		out = append(out, *acc[repo])
	}
	var ids []string
	for _, b := range out {
		ids = append(ids, b.ids...)
	}
	return out, ids
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// noticeText carries the category-specific wording a dispatch notice needs, so
// the capacity rendering is shared between the two surfaces that had the same
// blind spot (stall-watch and priority-wake, mg-dd77) instead of copied.
type noticeText struct {
	// head is the already-formatted prefix used whenever at least one item is
	// dispatchable, or capacity could not be established.
	head string
	// capHead is the prefix used when EVERY item in the notice is refused by
	// the cap. It exists because the headline is the part that travels: a
	// message whose first clause says "unclaimed" and whose third says "at
	// capacity" is read as the first clause.
	capHead string
	// verb is the imperative naming the remedy for dispatchable items —
	// unchanged from before mg-dd77, because for those items it was right.
	verb string
}

// atCapAdvice is the sentence that replaces the imperative when the cap is the
// binding constraint. subject names what the sentence is about, because in a
// mixed notice part of the message IS a dispatch request and an unscoped "This
// is not a dispatch request" would contradict it.
//
// It names the two remedies a coordinator would otherwise be pushed toward and
// rules them out, because "dispatch now" at cap has exactly two satisfying
// moves and both are damaging: preempting a working polecat strands its pushed
// branch (mg-be37), and snoozing hides ready work to silence a detector. An
// alarm should never leave those as the available responses.
//
// What it deliberately does NOT say is "no action required, they will dispatch
// when a slot frees" — the wording mg-dd77's own ticket proposed. Nothing in
// pogod auto-dispatches a work item: the coordinator dispatches, and at cap the
// correct action is the same dispatch on a LATER sweep. Telling the coordinator
// its queue drains itself would be this ticket's defect inverted — a confident
// sentence about a mechanism that does not exist — so the notice says LATER
// instead, in the same vocabulary the cap's own refusal uses.
func atCapAdvice(subject string) string {
	return subject + " is a throughput observation, not a dispatch request: `pogo agent spawn-polecat` " +
		"refuses a repo at its cap, so dispatching now yields only a refusal. It is a LATER, not a never — " +
		"nothing auto-dispatches these, and the same request succeeds once a worker there finishes, which " +
		"is when to make it. Do not preempt a worker or snooze an item to satisfy this notice; the question " +
		"worth asking now is whether one of the named workers is wedged."
}

// countIs renders "1 is" / "2 are", so a notice about a single item does not
// read as though it were written by a machine that never checked.
func countIs(n int) string {
	if n == 1 {
		return "1 is"
	}
	return fmt.Sprintf("%d are", n)
}

// message renders the whole notice: the caller's headline plus the remedy the
// cap would actually permit.
//
// The three shapes correspond to mg-dd77's three situations, and which one is
// used is decided by what the probe SAID, not by what would make the sentence
// shorter.
func (s capacitySplit) message(t noticeText) string {
	var b strings.Builder
	switch {
	case len(s.capped) == 0:
		// Nothing refused. The imperative is correct — say it exactly as it was
		// said before mg-dd77, including when no probe was wired at all.
		b.WriteString(t.head)
		if len(s.free) > 0 {
			fmt.Fprintf(&b, " — %s: %s", t.verb, strings.Join(s.free, ", "))
		} else {
			b.WriteString(" —")
		}
	case len(s.free) == 0 && len(s.unknown) == 0:
		// Everything is refused. The headline itself has to carry that.
		b.WriteString(t.capHead)
		fmt.Fprintf(&b, " — %s. %s", strings.Join(capSentences(s.capped), "; "), atCapAdvice("This"))
	default:
		b.WriteString(t.head)
		subject := "That"
		if len(s.free) > 0 {
			fmt.Fprintf(&b, " — %d can be dispatched now: %s. The other %s in a repository at its worker cap: %s. ",
				len(s.free), strings.Join(s.free, ", "),
				countIs(len(s.cappedIDs)), strings.Join(capSentences(s.capped), "; "))
			subject = "That second group"
		} else {
			fmt.Fprintf(&b, " — %s in a repository at its worker cap: %s. ",
				countIs(len(s.cappedIDs)), strings.Join(capSentences(s.capped), "; "))
		}
		b.WriteString(atCapAdvice(subject))
	}

	for _, u := range s.unknown {
		fmt.Fprintf(&b, " Occupancy for %s could not be determined, so %s may or may not be dispatchable right now — attempt the dispatch and read the refusal if there is one.",
			u.repo, strings.Join(u.ids, ", "))
	}
	if len(s.uncertain) > 0 {
		fmt.Fprintf(&b, " (Worker counts for %s may be an undercount, so a dispatch there could still be refused.)",
			strings.Join(s.uncertain, "; "))
	}
	return b.String()
}

// capSentences renders one clause per saturated repository, naming the
// occupying workers. The names are the load-bearing part — without them the
// message is still an instruction the reader cannot act on, just a politer one.
func capSentences(buckets []repoBucket) []string {
	out := make([]string, len(buckets))
	for i, b := range buckets {
		who := strings.Join(b.cap.Polecats, ", ")
		if who == "" {
			who = "workers unnamed"
		}
		out[i] = fmt.Sprintf("%s is at its cap of %d (%d worker(s): %s): %s",
			b.repo, b.cap.Cap, b.cap.Count, who, strings.Join(b.ids, ", "))
	}
	return out
}

// stampDetails records the split on an emitted event, so "aging because nobody
// dispatched it" and "aging because its repo is full" are countable apart in
// events.log rather than only distinguishable by reading prose. They are the
// same message today, and that is half of what mg-dd77 is about: at cap, aging
// items are the EXPECTED steady state and say nothing about coordinator
// diligence; below cap the identical message means work is being neglected.
func (s capacitySplit) stampDetails(details map[string]any) {
	if !s.probed {
		return
	}
	details["capacity_probed"] = true
	if len(s.free) > 0 {
		details["dispatchable_ids"] = s.free
	}
	if len(s.cappedIDs) > 0 {
		details["at_cap_ids"] = s.cappedIDs
		repos := make([]map[string]any, len(s.capped))
		for i, b := range s.capped {
			repos[i] = map[string]any{
				"repo":     b.repo,
				"count":    b.cap.Count,
				"cap":      b.cap.Cap,
				"polecats": b.cap.Polecats,
				"item_ids": b.ids,
			}
		}
		details["at_cap_repos"] = repos
	}
	if len(s.unknownIDs) > 0 {
		details["occupancy_unknown_ids"] = s.unknownIDs
	}
	if len(s.uncertain) > 0 {
		details["occupancy_uncertain"] = s.uncertain
	}
}
