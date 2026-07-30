package agent

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// DispatchPairingGate answers, at the moment of dispatch, whether a work item
// owes a PAIRED work item that has not been filed — a second item, filed in
// advance, that references this one and carries a declared marker tag.
//
// It sits beside DispatchGate rather than inside it because it answers a
// different question. DispatchGate asks "may this item be executed at all",
// reading one field on the item itself. This asks "has the obligation this
// item's repository puts on it been discharged", which requires reading the rest
// of the store. Same chokepoint, two independent verdicts, neither able to mask
// the other.
//
// WHY IT IS AT DISPATCH. The rule it enforces for onethird — a research ticket
// owes a pre-filed independent audit — already existed as prose and was already
// agreed by everyone involved. It was missed twice in two days, and the second
// miss showed why writing it down harder would not have worked: the rule lived
// in one agent's FILING path, so any ticket that agent did not file never
// reached it. Dispatch is the one place every work item passes through before
// any worker touches it, whoever filed it. See
// internal/config/dispatchpairing.go for the platform/configuration split and
// for why the gate routes on `repo` rather than on a filer-set marker.
//
// An interface so the handler is testable without a macguffin store, mirroring
// DispatchGate and ClaimReleaser.
type DispatchPairingGate interface {
	// PairingUnmet reports whether workItemID owes a paired item that does not
	// exist. It returns a rendered refusal — the gate knows the repo, the
	// vocabulary, and the id, and a refusal assembled anywhere else would have to
	// be handed all three.
	PairingUnmet(workItemID string) (refusal string, unmet bool)
}

// MGDispatchPairingGate is the production DispatchPairingGate: it reads the work
// item out of the macguffin store, decides whether the item's repo puts a
// pairing obligation on it, and — when it does — scans the store for the pair.
type MGDispatchPairingGate struct {
	// Root overrides the macguffin store location. Empty resolves via
	// macguffinStoreRoot, which under a test binary is a throwaway temp store
	// rather than the live one.
	Root string
	// Cfg is the pairing policy. Its zero value names no repos, which makes this
	// gate inert — deliberately, because the policy is one deployment's
	// configuration and must not ship as behaviour to every consumer.
	Cfg config.DispatchPairingConfig
}

// PairingUnmet implements DispatchPairingGate.
//
// THE FAILURE DIRECTIONS ARE SPLIT, AND THE SPLIT IS THE POINT.
//
// Before the item is known to be covered, the gate fails OPEN, exactly as
// DispatchGate does: no id, no store, no such item, unparseable item — all
// dispatch. It has to. `--id` is optional by design, and a gate that refused
// every id-less spawn or every spawn during one bad read of the store would halt
// the fleet over a policy that concerns one repository.
//
// Once the item IS known to be covered — it was read from disk and its `repo:`
// is one the deployment named — the gate fails CLOSED: a store scan that errors
// refuses the dispatch rather than waving it through. At that point "I could not
// check" is not equivalent to "there is nothing to check", and the blast radius
// of being wrong is one repository's dispatches rather than the whole fleet. The
// obligation exists by default within a covered repo (see PairingOwed), so an
// unverifiable obligation is an undischarged one.
//
// WHAT THIS DOES NOT COVER, stated because it will otherwise be discovered:
//
//   - A spawn with no `--id` is not checked. Nothing links it to a work item, so
//     there is no repo to route on.
//   - Work that never passes through dispatch is not checked. A deliverable
//     produced by an agent working directly, or by a human, reaches the repo
//     without a spawn — this gate cannot see it, and no gate sited here can. That
//     half is covered by the owning PM's sweep, and the two halves are
//     independent on purpose.
//   - It checks that a pair EXISTS, never that it is any good. An audit ticket
//     filed to satisfy the gate and then shelved unread satisfies it.
//   - Items in shelved/, pending/ and archive/ are not scanned as candidate
//     pairs. A shelved audit is a dropped obligation, not a discharged one.
func (m MGDispatchPairingGate) PairingUnmet(workItemID string) (string, bool) {
	if workItemID == "" || len(m.Cfg.Repos) == 0 {
		return "", false
	}
	if len(m.Cfg.PairTags) == 0 {
		// A rule that names repos but no pair tag can never be satisfied by any
		// item. Refusing everything in those repos would be fail-closed in the
		// letter and a deadlock in practice, with no way out but a config edit, so
		// this reports the misconfiguration and dispatches.
		log.Printf("dispatch pairing: [dispatch_pairing] names repos %v but no pair_tags — "+
			"no item could ever satisfy the requirement, so it is NOT being enforced; "+
			"set pair_tags to the marker your paired items carry", m.Cfg.Repos)
		return "", false
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return "", false
	}
	workRoot := filepath.Join(root, "work")

	item, found, err := workitem.FindFrom(workRoot, workItemID)
	if err != nil {
		log.Printf("dispatch pairing: could not read work item %s from %s: %v — "+
			"dispatching WITHOUT the pairing check", workItemID, root, err)
		return "", false
	}
	if !found {
		return "", false
	}

	covering, owed := config.PairingOwed(item.Repo, item.TagList(), m.Cfg)
	if !owed {
		return "", false
	}

	// An item whose frontmatter carries no `id:` is still a covered item, and it
	// still owes a pair — the id the caller dispatched by is the one a pair would
	// have to reference, so use that rather than emitting a refusal with a hole
	// in it where the id should be.
	targetID := item.ID
	if targetID == "" {
		targetID = workItemID
	}

	// Covered. From here a failure to answer is a refusal, not a pass.
	candidates, err := workitem.ListAllFrom(workRoot)
	if err != nil {
		return fmt.Sprintf("work item %s is in %s, whose items owe a paired item tagged %s before "+
			"dispatch, and the macguffin store at %s could not be scanned to check: %v. "+
			"REFUSING rather than assuming the pair exists — fix the store, or waive the "+
			"requirement visibly by tagging %s with one of %s",
			workItemID, covering, quoteList(m.Cfg.PairTags), workRoot, err,
			workItemID, waiverAdvice(m.Cfg.WaiverTags)), true
	}
	for _, c := range candidates {
		if config.PairingSatisfiedBy(c.ID, c.TagList(), c.DependsList(), targetID, m.Cfg.PairTags) {
			return "", false
		}
	}

	return fmt.Sprintf("work item %s is in %s, whose items owe a PAIRED work item that must be filed "+
		"BEFORE dispatch, and none was found: no item in the store carries a %s tag and references %s "+
		"(by `depends:` or by a tag containing the id). File the pair first — "+
		"`mg new --repo=%s --tags=%s,%s-followup --depends=%s ...` — then dispatch this item. "+
		"If it genuinely owes nothing, say so ON THE ITEM by tagging it %s, so the waiver is visible "+
		"to a reader instead of being an absence",
		workItemID, covering, quoteList(m.Cfg.PairTags), targetID,
		item.Repo, firstOr(m.Cfg.PairTags, "pair"), targetID, targetID,
		waiverAdvice(m.Cfg.WaiverTags)), true
}

// waiverAdvice renders the opt-out half of a refusal. A deployment that
// configured no waiver tags has no opt-out, and the message says so rather than
// suggesting a tag that would not work.
func waiverAdvice(tags []string) string {
	if len(tags) == 0 {
		return "(no waiver_tags are configured for this repo, so there is no opt-out — " +
			"file the pair, or add waiver_tags to [dispatch_pairing])"
	}
	return "one of " + quoteList(tags)
}

func quoteList(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(items))
	for _, i := range items {
		parts = append(parts, fmt.Sprintf("%q", i))
	}
	return strings.Join(parts, "/")
}

func firstOr(items []string, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	return items[0]
}

// SetDispatchPairingGate installs the pairing gate consulted before a polecat is
// dispatched. Passing nil restores the default.
//
// The default here is MGDispatchPairingGate{} — functional but INERT, because
// its zero-value config names no repos. That is the opposite of
// SetDispatchGate's default and the difference is deliberate: the assignee gate
// enforces a platform rule, so its default must enforce, while this gate
// enforces one deployment's policy, so its default must not. A daemon that never
// reaches the wiring line applies no program's rules to anybody.
func (r *Registry) SetDispatchPairingGate(g DispatchPairingGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchPairingGate = g
}

func (r *Registry) getDispatchPairingGate() DispatchPairingGate {
	r.mu.RLock()
	g := r.dispatchPairingGate
	r.mu.RUnlock()
	if g == nil {
		return MGDispatchPairingGate{}
	}
	return g
}

// dispatchPairingRefusal returns the refusal message for an item whose pairing
// obligation is unmet, or "" when dispatch is allowed.
func (r *Registry) dispatchPairingRefusal(workItemID string) string {
	refusal, unmet := r.getDispatchPairingGate().PairingUnmet(workItemID)
	if !unmet {
		return ""
	}
	return refusal
}
