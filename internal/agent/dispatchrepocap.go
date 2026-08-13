package agent

import (
	"fmt"
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

// RepoOccupancy is what the per-repo cap saw when it decided. It is served on
// /agents/hostload as well as used internally, because a coordinator planning a batch
// of dispatches needs the same numbers pogod will enforce on — the precedent is
// HostLoadResponse.WouldRefuseDispatch, and the reason is the same: an advisory
// count that could drift from the enforced one lets a coordinator plan against
// a host pogod sees differently.
type RepoOccupancy struct {
	// Repo is the normalized repository path the count is about.
	Repo string `json:"repo"`
	// Polecats are the live workers attributed to Repo, by name, sorted.
	Polecats []string `json:"polecats"`
	// Count is len(Polecats) and is the number compared against Cap.
	Count int `json:"count"`
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
	// WouldRefuse is what the spawn path would do with a request for this repo
	// right now.
	WouldRefuse bool `json:"would_refuse"`
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

	occ.Polecats = make([]string, 0, len(inRepo))
	for name := range inRepo {
		occ.Polecats = append(occ.Polecats, name)
	}
	sort.Strings(occ.Polecats)
	occ.Count = len(occ.Polecats)
	occ.Unattributed = dedupeSorted(unattributed, inRepo)

	if act := r.getRefineryActivity(); act != nil {
		occ.RefineryHasWork, occ.RefineryKnown = act.HasWorkIn(norm)
	}
	reserving := occ.RefineryHasWork && occ.RefineryKnown
	occ.Cap = cfg.EffectiveCap(reserving)
	if reserving && cfg.Armed() {
		occ.RefineryReserved = cfg.MaxPolecatsPerRepo - occ.Cap
	}
	occ.WouldRefuse = cfg.Armed() && occ.Count >= occ.Cap
	return occ
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
func (r *Registry) repoCapRefusal(repo string) string {
	occ := r.RepoOccupancyFor(repo)
	if !occ.WouldRefuse {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "repo %s already has %d worker(s) in it and the cap is %d: %s. ",
		occ.Repo, occ.Count, occ.Cap, strings.Join(occ.Polecats, ", "))
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
