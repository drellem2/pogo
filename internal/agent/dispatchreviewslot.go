package agent

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/workitem"
)

// # A gh-issue flow is TWO workers, not one (mg-bf42)
//
// On the gh-issue track the build worker stays alive through review, on
// purpose: the `reviews:` carrier line exists so the done-reaper does not stop
// a builder while its reviewer is running (mg-aaf6, drellem2/pogo#131), because
// a reviewer mailing findings to a stopped builder happened twice. So from the
// moment a gh-issue build is dispatched, the flow WILL need a second slot in
// the same repository — for the review worker the coordinator dispatches when
// the PR opens.
//
// The per-repo cap counted only the workers that were live. With the default
// cap of 3 (2 while the refinery has work in the repo) that let three gh-issue
// builds in: on 2026-09-08 three builders held all three slots, two of them
// had PRs open and needed reviewers, and no reviewer could ever start — while
// no builder could finish, because finishing is what review unblocks. Every
// escape was wrong: stopping a builder is the #131 failure, raising the cap
// trades a deadlock for a starved refinery, and letting builders self-close is
// what the exemption was built to prevent.
//
// So this is the refinery reserve's idea (mg-3977) applied to a second
// consumer that predictably needs a slot: every live gh-issue builder whose
// reviewer is not yet running HOLDS ONE SLOT BACK for that reviewer, and a new
// gh-issue build is admitted only if BOTH of its slots fit. The arithmetic the
// coordinator had to know from memory — one sustainable flow per cap-3 repo —
// is now what the gate computes.

// GHIssueWorkflow is the `workflow:` carrier value of the gh-issue track.
const GHIssueWorkflow = workitem.GHIssueWorkflow

// FlowCarrier is the part of a work item's state carrier the review-slot
// reserve reads.
type FlowCarrier struct {
	Workflow string
	Stage    string
	Reviews  string
}

// needsReviewer reports whether this item is a gh-issue BUILD whose worker will
// need a reviewer beside it: the gh-issue workflow, no `reviews:` line (that
// marks the review ticket), and a stage the build → review loop is still in.
// At `merge` the reviewer has already passed; at `triage` or `gated` there is
// no build worker yet.
func (c FlowCarrier) needsReviewer() bool {
	if c.Workflow != GHIssueWorkflow || c.Reviews != "" {
		return false
	}
	return c.Stage == "build" || c.Stage == "review"
}

// FlowReader reads a work item's state carrier by id. found=false covers "no
// such item" AND "could not read it", and both are treated as "declares no
// flow" — the reserve fails OPEN like every other gate here.
type FlowReader interface {
	ReadFlow(workItemID string) (c FlowCarrier, found bool)
}

// FlowReaderFunc adapts a function to FlowReader.
type FlowReaderFunc func(string) (FlowCarrier, bool)

// ReadFlow implements FlowReader.
func (f FlowReaderFunc) ReadFlow(id string) (FlowCarrier, bool) { return f(id) }

// MGFlowReader is the production FlowReader: the carrier as parsed from the
// macguffin store by internal/workitem, the same parse the stage gate and the
// done-reaper use.
type MGFlowReader struct {
	// Root overrides the macguffin store location; empty resolves through
	// macguffinStoreRoot, which under a test binary is a throwaway store.
	Root string
}

// ReadFlow implements FlowReader.
func (m MGFlowReader) ReadFlow(id string) (FlowCarrier, bool) {
	root := macguffinStoreRoot(m.Root)
	if root == "" || strings.TrimSpace(id) == "" {
		return FlowCarrier{}, false
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), id)
	if err != nil {
		log.Printf("dispatch review-slot: could not read work item %s: %v — "+
			"treating it as declaring no gh-issue flow", id, err)
		return FlowCarrier{}, false
	}
	if !found || item.CarrierUnreadable {
		return FlowCarrier{}, false
	}
	return FlowCarrier{Workflow: item.Workflow, Stage: item.Stage, Reviews: item.Reviews}, true
}

// SetFlowReader installs the carrier reader the review-slot reserve uses.
// Passing nil restores MGFlowReader{}.
func (r *Registry) SetFlowReader(f FlowReader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flowReader = f
}

func (r *Registry) getFlowReader() FlowReader {
	r.mu.RLock()
	f := r.flowReader
	r.mu.RUnlock()
	if f == nil {
		return MGFlowReader{}
	}
	return f
}

// ReviewSlotHold is one live gh-issue builder holding a slot for its reviewer.
type ReviewSlotHold struct {
	// Polecat is the live build worker; Build is its work item.
	Polecat string `json:"polecat"`
	Build   string `json:"build"`
}

// String renders the hold for a refusal message.
func (h ReviewSlotHold) String() string { return h.Polecat + " (" + h.Build + ")" }

// reviewSlotHolds finds, among the named polecats (the ones counted in one
// repo), the gh-issue builders whose reviewer is not running yet. items maps
// polecat name → work item id; a polecat with no item holds nothing.
//
// A builder is "covered" — holds nothing — once a live worker in the same repo
// is on an item whose `reviews:` names the builder's item, because that
// reviewer is already in Count.
func reviewSlotHolds(polecats []string, items map[string]string, fr FlowReader) []ReviewSlotHold {
	flows := map[string]FlowCarrier{}
	reviewed := map[string]bool{}
	for _, name := range polecats {
		id := strings.TrimSpace(items[name])
		if id == "" {
			continue
		}
		c, ok := fr.ReadFlow(id)
		if !ok {
			continue
		}
		flows[name] = c
		if c.Reviews != "" {
			reviewed[c.Reviews] = true
		}
	}
	var holds []ReviewSlotHold
	for _, name := range polecats {
		c, ok := flows[name]
		if !ok || !c.needsReviewer() {
			continue
		}
		id := strings.TrimSpace(items[name])
		if reviewed[id] {
			continue
		}
		holds = append(holds, ReviewSlotHold{Polecat: name, Build: id})
	}
	sort.Slice(holds, func(i, j int) bool { return holds[i].Polecat < holds[j].Polecat })
	return holds
}

// polecatWorkItems maps every live polecat — registry and witness — to its work
// item id. Registry last, so the live record wins where they disagree, exactly
// as WorkItemsInFlight orders them.
func (r *Registry) polecatWorkItems() map[string]string {
	out := map[string]string{}
	if witnessed, err := WitnessedPolecatWorkItems(); err == nil {
		for name, id := range witnessed {
			out[name] = id
		}
	}
	for _, p := range r.Polecats() {
		if id := strings.TrimSpace(p.WorkItemID); id != "" {
			out[p.Name] = id
		}
	}
	return out
}

// reviewSlotVerdict is the item-aware half of the per-repo cap: what the
// dispatch of workItemID into occ.Repo needs, given the review slots already
// held there.
type reviewSlotVerdict struct {
	// Need is how many slots this dispatch will occupy: 2 for a gh-issue build
	// (itself and, later, its reviewer), 1 for anything else.
	Need int
	// Held are the holds that still apply to this dispatch — the reviewer for a
	// held builder consumes that builder's hold, so it is not in here.
	Held []ReviewSlotHold
	// Consumes is the hold this dispatch IS the reviewer for, if any.
	Consumes *ReviewSlotHold
	// Refuse is whether Count + len(Held) + Need exceeds the cap.
	Refuse bool
}

func (r *Registry) reviewSlotVerdictFor(occ RepoOccupancy, workItemID string) reviewSlotVerdict {
	v := reviewSlotVerdict{Need: 1}
	var incoming FlowCarrier
	if id := strings.TrimSpace(workItemID); id != "" {
		incoming, _ = r.getFlowReader().ReadFlow(id)
	}
	// Only `stage: build` is charged two slots at dispatch. The protocol
	// dispatches a builder at `build` and moves it to `review` only once it is
	// live with a PR open — so an item ARRIVING at `stage: review` with no
	// `reviews:` line is, in practice, a review ticket whose declaration was
	// omitted. Charging it as a build would refuse the very reviewer the hold
	// is for, which is this ticket's deadlock re-entered through the fix; it is
	// instead let into any hold, since which builder it covers cannot be read.
	gh := incoming.Workflow == GHIssueWorkflow
	if (workitem.Carrier{Workflow: incoming.Workflow, Stage: incoming.Stage, Reviews: incoming.Reviews}).ChargedAsGHIssueBuild() {
		v.Need = 2
	}
	undeclaredReviewer := gh && incoming.Reviews == "" && incoming.Stage == "review"
	for i, h := range occ.ReviewSlotHolds {
		if v.Consumes == nil && ((incoming.Reviews != "" && incoming.Reviews == h.Build) || undeclaredReviewer) {
			v.Consumes = &occ.ReviewSlotHolds[i]
			continue
		}
		v.Held = append(v.Held, h)
	}
	v.Refuse = occ.Cap > 0 && occ.Count+len(v.Held)+v.Need > occ.Cap
	return v
}

// reviewSlotRefusal renders the refusal for a dispatch the plain cap admits
// but the review-slot reserve does not, or "" when it fits.
func (r *Registry) reviewSlotRefusal(occ RepoOccupancy, workItemID string) string {
	if occ.Unresolvable != "" {
		// The count could not be taken; the cap fails open, and so does this.
		return ""
	}
	v := r.reviewSlotVerdictFor(occ, workItemID)
	if !v.Refuse {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "repo %s has %d live worker(s) and the cap is %d, ", occ.Repo, occ.Count, occ.Cap)
	if len(v.Held) > 0 {
		names := make([]string, len(v.Held))
		for i, h := range v.Held {
			names[i] = h.String()
		}
		fmt.Fprintf(&b, "and %d more slot(s) are HELD for the reviewers of live gh-issue builds whose "+
			"review worker is not running yet: %s. ", len(v.Held), strings.Join(names, ", "))
	} else {
		b.WriteString("with no review slots held. ")
	}
	if v.Need == 2 {
		fmt.Fprintf(&b, "Work item %s is a gh-issue BUILD, which needs TWO slots — its builder stays "+
			"alive through review (mg-aaf6, drellem2/pogo#131), so its reviewer will need a slot beside "+
			"it — and %d + %d held + 2 exceeds %d. ", workItemID, occ.Count, len(v.Held), occ.Cap)
		if occ.ConfiguredCap > 0 && occ.ConfiguredCap < 2 {
			fmt.Fprintf(&b, "With max_polecats_per_repo = %d this repo can NEVER hold a gh-issue flow: "+
				"raise it to at least 2 or the builder will wait on a reviewer that is always refused. ",
				occ.ConfiguredCap)
		}
	} else {
		fmt.Fprintf(&b, "Admitting this worker (%d + %d held + 1 > %d) would take a slot a reviewer "+
			"will need. ", occ.Count, len(v.Held), occ.Cap)
	}
	b.WriteString("Filling the repo anyway builds the 2026-09-08 deadlock: builders hold every slot, " +
		"their reviewers can never start, and the builders cannot finish because finishing is what " +
		"review unblocks (mg-bf42). This is a LATER: it clears when a held flow's reviewer is " +
		"dispatched or its build reaches `stage: merge` or finishes. A gh-issue flow's reviewer is " +
		"always admitted into the slot held for it. ")
	if occ.RefineryReserved > 0 {
		fmt.Fprintf(&b, "(%d of the %d configured slots is also reserved for the refinery right now.) ",
			occ.RefineryReserved, occ.ConfiguredCap)
	}
	b.WriteString("Read the held slots with `pogo host load --repo=" + occ.Repo + "`.")
	return b.String()
}
