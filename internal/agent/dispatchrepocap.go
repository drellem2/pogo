package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/config"
)

// RefineryActivity answers whether the refinery has work in a repository right
// now — a merge request in flight OR queued behind one.
//
// It is an interface, and the refinery is reached through it rather than
// imported, so this gate can be tested without a merge queue and so
// internal/agent keeps no edge to internal/refinery. cmd/pogod supplies the
// production implementation as a closure over its live *refinery.Refinery.
type RefineryActivity interface {
	// HasWorkIn reports whether the refinery holds a merge request for repo,
	// and whether that could be established at all. known=false means "no
	// information" and is NOT "no work" — see RepoOccupancyFor for what the
	// gate does with it.
	HasWorkIn(repo string) (has bool, known bool)
}

// RefineryActivityFunc adapts a function to RefineryActivity.
type RefineryActivityFunc func(repo string) (bool, bool)

// HasWorkIn implements RefineryActivity.
func (f RefineryActivityFunc) HasWorkIn(repo string) (bool, bool) { return f(repo) }

// QueuedMerge is one merge request the refinery holds, reduced to what the cap
// needs to recognise the worker that submitted it.
type QueuedMerge struct {
	// Author is what the submitter passed as --author: by protocol the work
	// item id ("mg-976f"), sometimes the agent name ("976f").
	Author string
	// Branch is the submitted branch, "polecat-<name>" for a polecat.
	Branch string
}

// MergeQueueReader lists the merge requests the refinery holds for a
// repository — queued OR in its gate — and whether it could be asked at all.
//
// Separate from RefineryActivity rather than a second method on it, so every
// existing RefineryActivityFunc keeps compiling and a probe that can only say
// "busy or not" is still a complete RefineryActivity.
type MergeQueueReader interface {
	QueuedIn(repo string) (mrs []QueuedMerge, known bool)
}

// MergeQueueReaderFunc adapts a function to MergeQueueReader.
type MergeQueueReaderFunc func(repo string) ([]QueuedMerge, bool)

// QueuedIn implements MergeQueueReader.
func (f MergeQueueReaderFunc) QueuedIn(repo string) ([]QueuedMerge, bool) { return f(repo) }

// RepoOccupancy is what the per-repo cap saw when it decided. It is served on
// /agents/hostload as well as used internally, because a coordinator planning a batch
// of dispatches needs the same numbers pogod will enforce on — the precedent is
// HostLoadResponse.WouldRefuseDispatch, and the reason is the same: an advisory
// count that could drift from the enforced one lets a coordinator plan against
// a host pogod sees differently.
type RepoOccupancy struct {
	// Repo is the normalized repository path the count is about.
	Repo string `json:"repo"`
	// Polecats are the live workers attributed to Repo that COUNT against the
	// cap, by name, sorted. A worker excused by the merge-queue credit is in
	// MergeQueued instead, never in both.
	Polecats []string `json:"polecats"`
	// Count is len(Polecats) and is the number compared against Cap.
	Count int `json:"count"`
	// MergeQueued are live workers in Repo that are NOT counted, because their
	// branch is in the refinery's merge queue and the per-repo credit
	// (MergeQueuedCredit) covers them (mg-976f). They are alive and polling,
	// not building; a failed gate can send one back to building, which is why
	// the credit is bounded. Live = Count + len(MergeQueued) + len(Finished).
	MergeQueued []string `json:"merge_queued,omitempty"`
	// Finished are live workers in Repo that are NOT counted, because their
	// work item already reads terminal (done/archived) — they have nothing
	// left to build and are waiting only to be reaped (drellem2/pogo#128). A
	// worker whose status could not be read is NOT here: it is counted, as it
	// was before #128. Never in Polecats or MergeQueued.
	Finished []string `json:"finished,omitempty"`
	// MergeQueuedOverCredit are workers whose branch is in the queue but who
	// ARE counted, because the credit was already spent. They are in Polecats.
	MergeQueuedOverCredit []string `json:"merge_queued_over_credit,omitempty"`
	// Cap is the effective ceiling right now — MaxPolecatsPerRepo less the
	// refinery's reserve when the refinery has work here. Zero means the cap is
	// disarmed and nothing is refused.
	Cap int `json:"cap"`
	// ConfiguredCap is MaxPolecatsPerRepo before the reserve is applied, so a
	// reader can tell a cap of 2 from a cap of 3 with one slot held back.
	ConfiguredCap int `json:"configured_cap"`
	// RefineryReserved is how many slots the refinery is holding right now.
	RefineryReserved int `json:"refinery_reserved,omitempty"`
	// RefineryHasWork is whether a merge request for this repo is in flight or
	// queued; RefineryKnown is whether that could be established. Both are
	// carried because "the refinery could not be asked" and "the refinery is
	// idle" produce the same reserve and must not read the same.
	RefineryHasWork bool `json:"refinery_has_work"`
	RefineryKnown   bool `json:"refinery_known"`
	// Unattributed names live polecats whose repository is unknown — witnessed
	// survivors written before the repo was recorded, and --no-worktree
	// polecats that have none. They are NOT in Count. Reported so that a cap
	// which may be undercounting says so instead of looking exact.
	Unattributed []string `json:"unattributed,omitempty"`
	// WitnessErr is set when the persisted witness could not be read, meaning
	// polecats that outlived a previous pogod are invisible to this count. The
	// gate FAILS OPEN in that case — see repoCapRefusal.
	WitnessErr string `json:"witness_err,omitempty"`
	// Unresolvable is set when the requested repo is a non-empty string that
	// names no repository this host can count workers in — a bare NAME with no
	// resolver wired or no unique match, or a path that is not a directory. It
	// carries the reason.
	//
	// It is NOT the same as an empty repo, and the difference is the whole of
	// mg-cd4a. An empty repo means "contends for nothing", and Count 0 is the
	// true answer. An unresolvable one means the count could not be TAKEN, and
	// Count 0 is a fabrication — the repository may well be saturated. Callers
	// that report capacity must render this as "could not be determined";
	// WouldRefuse stays false because the cap fails open, so the gate itself is
	// unchanged.
	Unresolvable string `json:"unresolvable,omitempty"`
	// ReviewSlotHolds are the live gh-issue builders in this repo whose
	// reviewer is not running yet. Each holds one slot back for that reviewer,
	// because the builder stays alive through review and the reviewer WILL be
	// dispatched (mg-bf42). They are not in Count — they are the slots Count
	// will grow into.
	ReviewSlotHolds []ReviewSlotHold `json:"review_slot_holds,omitempty"`
	// WouldRefuse is what the spawn path would do with an ordinary request for
	// this repo right now: Count plus the held review slots against Cap. The
	// one exception is the reviewer a hold is FOR, which is admitted into its
	// held slot whenever Count alone is under Cap.
	WouldRefuse bool `json:"would_refuse"`
	// WouldRefuseGHIssueBuild is what the spawn path would do with a gh-issue
	// BUILD, which needs two slots — itself and its future reviewer. It is the
	// number to plan a batch of gh-issue dispatches against: with the default
	// cap of 3 it admits ONE flow per repo, and none while the refinery holds
	// its reserve and another flow is live (mg-bf42).
	WouldRefuseGHIssueBuild bool `json:"would_refuse_gh_issue_build"`
}

// SetDispatchCap installs the per-repo cap policy. The zero value disarms the
// gate; cmd/pogod passes cfg.DispatchCap, whose default is armed.
func (r *Registry) SetDispatchCap(c config.DispatchCapConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchCap = c
}

// SetRefineryActivity installs the refinery probe the cap reserves against.
// Passing nil leaves the reserve unenforced — the gate still caps, it simply
// holds nothing back, because it cannot tell an idle refinery from one it
// failed to ask.
func (r *Registry) SetRefineryActivity(a RefineryActivity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refineryActivity = a
}

func (r *Registry) dispatchCapPolicy() config.DispatchCapConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dispatchCap
}

// SetMergeQueue installs the probe the merge-queue credit reads (mg-976f).
// nil excuses nobody: every live worker counts, as before the credit existed.
func (r *Registry) SetMergeQueue(q MergeQueueReader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mergeQueue = q
}

func (r *Registry) getMergeQueue() MergeQueueReader {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mergeQueue
}

func (r *Registry) getRefineryActivity() RefineryActivity {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.refineryActivity
}

// RepoOccupancyFor counts the live workers in one repository and reports what
// the cap would do about another.
//
// # Where the count comes from, and why it is a union
//
// Two sources, deduplicated by name:
//
//   - the in-memory registry, authoritative while this pogod has run
//     continuously, and EMPTY after a restart — permanently, because the
//     registry has no adopt path (mg-13a3);
//   - the persisted polecat witness, which survives a restart.
//
// Reading the registry alone would have uncapped this gate on every redeploy,
// which is exactly when survivors exist. That is mg-0130's lesson applied to a
// third caller, and it is cheaper to apply it here than to rediscover it.
//
// # Both failure directions, stated
//
// An unreadable witness store FAILS OPEN: the dispatch proceeds and the report
// carries WitnessErr. Refusing on missing information would halt dispatch into
// every repo for a reason the caller cannot check or clear — the same argument
// loadGateRefusal makes, and the same one that keeps this gate from counting
// unattributed polecats against a repo it guessed. A cap is a throttle; a
// throttle that jams shut on a bad read is worse than no throttle.
//
// A refinery that cannot be asked reserves NOTHING, for the same reason.
func (r *Registry) RepoOccupancyFor(repo string) RepoOccupancy {
	cfg := r.dispatchCapPolicy()
	norm := config.NormalizeRepo(repo)
	occ := RepoOccupancy{Repo: norm, ConfiguredCap: cfg.MaxPolecatsPerRepo}
	if norm == "" {
		// Nothing to contend on: a --no-worktree dispatch runs no repository's
		// test suite. Left uncapped deliberately rather than pooled under some
		// "" bucket, which would let unrelated in-place edits block each other.
		return occ
	}
	// unresolved reports an occupancy that could not be TAKEN. It still carries
	// the ceiling, because Cap == 0 is this struct's signal for "the cap is
	// disarmed" — returning the zero value here would say the fleet has no
	// per-repo limit, which is a second confident falsehood in the slot the
	// first one just vacated.
	unresolved := func(why string) RepoOccupancy {
		occ.Unresolvable = why
		occ.Cap = cfg.EffectiveCap(false)
		return occ
	}
	if !filepath.IsAbs(norm) {
		// A bare NAME, not a path. Every comparison below runs through
		// config.SameRepo, which compares cleaned strings, so counting against
		// this string would find nothing and report a saturated repository as
		// empty. Resolve it, or say it could not be resolved.
		res := r.getRepoResolver()
		if res == nil {
			return unresolved("\"" + norm + "\" is a repository NAME, not a path, " +
				"and this pogod has no name resolver wired to look it up")
		}
		path, ok := res.ResolveRepo(norm)
		if !ok {
			return unresolved("\"" + norm + "\" is a repository NAME, not a path, " +
				"and it matches no single repository known to this host")
		}
		occ.Repo, norm = path, path
	}

	inRepo := map[string]bool{}
	var unattributed []string
	for _, p := range r.Polecats() {
		if strings.TrimSpace(p.SourceRepo) == "" {
			unattributed = append(unattributed, p.Name)
			continue
		}
		if config.SameRepo(p.SourceRepo, norm) {
			inRepo[p.Name] = true
		}
	}

	witnessed, wUnattributed, err := WitnessedPolecatRepos()
	if err != nil {
		occ.WitnessErr = err.Error()
	} else {
		for name, wrepo := range witnessed {
			if config.SameRepo(wrepo, norm) {
				inRepo[name] = true
			}
		}
		unattributed = append(unattributed, wUnattributed...)
	}

	live := make([]string, 0, len(inRepo))
	for name := range inRepo {
		live = append(live, name)
	}
	sort.Strings(live)
	occ.Unattributed = dedupeSorted(unattributed, inRepo)

	var workItems map[string]string
	if len(live) > 0 {
		workItems = r.polecatWorkItems()
	}
	// Finished workers first (drellem2/pogo#128), so a worker that is both
	// finished and still in the merge queue does not spend the merge-queue
	// credit on a slot it would not have held anyway.
	excused := map[string]bool{}
	occ.Finished = finishedPolecats(live, workItems, r.getItemStatusReader())
	for _, name := range occ.Finished {
		excused[name] = true
	}
	if cfg.MergeQueuedCredit > 0 && len(live) > 0 {
		if q := r.getMergeQueue(); q != nil {
			if mrs, known := q.QueuedIn(norm); known {
				var waiting []string
				for _, name := range live {
					if !excused[name] {
						waiting = append(waiting, name)
					}
				}
				queued := mergeQueuedPolecats(waiting, workItems, mrs)
				for i, name := range queued {
					if i < cfg.MergeQueuedCredit {
						excused[name] = true
						occ.MergeQueued = append(occ.MergeQueued, name)
					} else {
						occ.MergeQueuedOverCredit = append(occ.MergeQueuedOverCredit, name)
					}
				}
			}
		}
	}
	occ.Polecats = make([]string, 0, len(live))
	for _, name := range live {
		if !excused[name] {
			occ.Polecats = append(occ.Polecats, name)
		}
	}
	occ.Count = len(occ.Polecats)

	// An absolute path that is not a directory is the same fabrication one
	// spelling further along: nobody is working in a repository that is not
	// there, but nobody can work in it either, so "0 of 3, dispatch away" is
	// advice that can only be refused.
	//
	// Guarded on Count == 0 deliberately. A repository holding live workers is
	// self-evidently real, and a transient stat failure on a SATURATED repo
	// must never demote it to "could not be determined" — that would drop the
	// at-cap guidance for exactly the repositories that need it, which is the
	// defect this function is being fixed for, re-entered through the fix.
	//
	// len(live), not Count: a repo whose only workers are excused by the
	// merge-queue credit has a Count of 0 and is every bit as real.
	if len(live) == 0 && !isDirectory(norm) {
		return unresolved(norm + " is not a directory on this host")
	}

	if act := r.getRefineryActivity(); act != nil {
		occ.RefineryHasWork, occ.RefineryKnown = act.HasWorkIn(norm)
	}
	reserving := occ.RefineryHasWork && occ.RefineryKnown
	occ.Cap = cfg.EffectiveCap(reserving)
	if reserving && cfg.Armed() {
		occ.RefineryReserved = cfg.MaxPolecatsPerRepo - occ.Cap
	}
	// Every LIVE worker, excused or not: a hold is about the reviewer a builder
	// will need, and a builder waiting on the merge queue will still need one.
	occ.ReviewSlotHolds = reviewSlotHolds(live, workItems, r.getFlowReader())
	held := len(occ.ReviewSlotHolds)
	occ.WouldRefuse = cfg.Armed() && occ.Count+held >= occ.Cap
	occ.WouldRefuseGHIssueBuild = cfg.Armed() && occ.Count+held+2 > occ.Cap
	return occ
}

// finishedPolecats returns, in live's (sorted) order, the live workers whose
// work item reads terminal. A worker with no work item, or whose status could
// not be read, is left out — and so stays counted. That is the fail-open
// direction for a cap: a worker we could not classify might be building, and
// counting it is exactly what the cap did before #128.
func finishedPolecats(live []string, workItems map[string]string, sr ItemStatusReader) []string {
	if sr == nil {
		return nil
	}
	var out []string
	for _, name := range live {
		id := strings.TrimSpace(workItems[name])
		if id == "" {
			continue
		}
		status, err := sr.ReadItemStatus(id)
		if err != nil || !IsTerminalItemStatus(status) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// mergeQueuedPolecats returns, in live's (sorted) order, the live workers that
// authored one of mrs.
//
// A worker is matched on any of its three spellings, because submitters are
// not consistent: the protocol's --author is the work item id, some submit
// under the agent name, and the branch is "polecat-<name>" whatever the author
// says. Matching only one would leave a waiting worker counted — the direction
// that fails CLOSED, so it would be merely the old behaviour, but silently so.
func mergeQueuedPolecats(live []string, workItems map[string]string, mrs []QueuedMerge) []string {
	if len(mrs) == 0 {
		return nil
	}
	authors := map[string]bool{}
	branches := map[string]bool{}
	for _, mr := range mrs {
		if a := strings.TrimSpace(mr.Author); a != "" {
			authors[a] = true
		}
		if b := strings.TrimSpace(mr.Branch); b != "" {
			branches[b] = true
		}
	}
	var out []string
	for _, name := range live {
		id := strings.TrimSpace(workItems[name])
		if authors[name] || (id != "" && authors[id]) || branches["polecat-"+name] {
			out = append(out, name)
		}
	}
	return out
}

// dedupeSorted returns the unique names in list, minus any already counted.
func dedupeSorted(list []string, counted map[string]bool) []string {
	if len(list) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, n := range list {
		if counted[n] || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// repoCapRefusal returns the refusal message when a repository already holds
// its allowance of workers, or "" when dispatch may proceed.
//
// # It is a LATER, not a NO
//
// Like the load gate and unlike the assignee and pairing gates, the identical
// request succeeds once a worker in that repo finishes. The message says so,
// because the reader is usually a coordinator with no human in the loop and
// "hold this item" and "abandon this item" are opposite actions. It also says
// that a DIFFERENT repository is not refused BY THIS CAP, since the whole point
// of a per-repo cap is that a refusal here is not a refusal everywhere — and
// "dispatch elsewhere" is a third action, better than either of the other two.
//
// # What it no longer promises
//
// It used to say a dispatch into a different repo was "unaffected", full stop,
// and that this refusal cleared "as soon as a worker here finishes". Both were
// measured wrong on 2026-08-13 (mg-eb47). The host gate refuses every spawn
// regardless of repo, so a per-repo slot freed by a merge was unusable and
// rerouting elsewhere would have been refused too — a freed slot is not
// capacity, it is the absence of one particular refusal. And worker count is
// not a proxy for the resource in either direction: the fleet went from 6
// agents holding 6.1 of 10 cores to 5 holding 7.0, because two refinery gates
// that self-parallelise outweighed them. A coordinator that took either sentence
// literally retried twice into a guaranteed refusal, which is exactly the cost
// a refusal message exists to avoid.
//
// # Two checks, in this order
//
// The plain count first: a repo whose LIVE workers already fill the cap
// refuses everything, the reviewer a slot is held for included — there is no
// slot to give it. Only under that does the review-slot reserve apply
// (mg-bf42), which is item-aware: it reads workItemID's carrier to tell a
// gh-issue build (needs two slots) and the reviewer a hold is for (consumes
// its hold) from everything else. See dispatchreviewslot.go.
func (r *Registry) repoCapRefusal(repo, workItemID string) string {
	occ := r.RepoOccupancyFor(repo)
	if occ.Cap == 0 || occ.Count < occ.Cap {
		return r.reviewSlotRefusal(occ, workItemID)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "repo %s already has %d worker(s) in it and the cap is %d: %s. ",
		occ.Repo, occ.Count, occ.Cap, strings.Join(occ.Polecats, ", "))
	if len(occ.MergeQueued) > 0 {
		fmt.Fprintf(&b, "(%d more worker(s) are live but NOT counted, because their branch is in the "+
			"merge queue and they are only waiting on it: %s — mg-976f.) ",
			len(occ.MergeQueued), strings.Join(occ.MergeQueued, ", "))
	}
	if len(occ.MergeQueuedOverCredit) > 0 {
		fmt.Fprintf(&b, "(%s also only waiting on the merge queue, but the credit for waiting workers "+
			"is spent, so they count.) ", strings.Join(occ.MergeQueuedOverCredit, ", "))
	}
	if len(occ.Finished) > 0 {
		fmt.Fprintf(&b, "(%d more worker(s) are live but NOT counted, because their work item is already "+
			"done or archived (terminal) and they are only waiting to be reaped: %s — drellem2/pogo#128.) ",
			len(occ.Finished), strings.Join(occ.Finished, ", "))
	}
	if occ.RefineryReserved > 0 {
		fmt.Fprintf(&b, "%d of the %d configured slots is RESERVED for the refinery, which has a "+
			"merge request for this repo in flight or queued — the workers verifying their branches "+
			"must not starve the process that merges them (mg-3977). ",
			occ.RefineryReserved, occ.ConfiguredCap)
	}
	b.WriteString("This is a LATER, not a refusal of the item — nothing about the work item is wrong, " +
		"and the same request succeeds once a worker here finishes. It is also NOT a fleet-wide " +
		"limit: THIS cap does not refuse a dispatch into a DIFFERENT repo, because what saturates is " +
		"one repo's test suite run concurrently, not the number of workers. ")
	b.WriteString("A freed slot here is not capacity, though, and neither is another repo — the HOST " +
		"gate is a separate refusal and it applies to every spawn regardless of repo. Do not retry on " +
		"a worker EXITING: measured 2026-08-13, the fleet went from 6 agents holding 6.1 of 10 cores to " +
		"5 agents holding 7.0 — fewer agents, MORE cores, because two refinery gates that each " +
		"parallelise across packages outweighed the ones that left (mg-eb47). " +
		"Retry on the fleet's CORE share dropping below the host gate's mark, which is a different event. ")
	if occ.WitnessErr != "" {
		fmt.Fprintf(&b, "(The persisted polecat witness could not be read — %s — so this count may be "+
			"missing survivors of an earlier pogod.) ", occ.WitnessErr)
	}
	if len(occ.Unattributed) > 0 {
		fmt.Fprintf(&b, "(%d live worker(s) could not be attributed to any repo and are NOT in this "+
			"count: %s.) ", len(occ.Unattributed), strings.Join(occ.Unattributed, ", "))
	}
	b.WriteString("Read both numbers with `pogo host load --repo=" + occ.Repo + "` — it reports this " +
		"count and the host's core share from the same gates that refuse on them.")
	return b.String()
}

// isDirectory reports whether p is a directory right now. Any error — missing,
// unreadable, a plain file — answers false, and the caller turns that into
// "could not be determined" rather than into a refusal.
func isDirectory(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
