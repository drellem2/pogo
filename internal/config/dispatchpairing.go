package config

import (
	"path/filepath"
	"strings"
)

// DispatchPairingConfig declares repositories whose work items OWE A PAIRED
// ITEM before they may be dispatched — a second work item, filed in advance,
// that references the first and carries a declared marker tag. A covered item
// with no such pair is refused at dispatch.
//
// # Which half of this is platform and which half is configuration
//
// mg-0e24 asked for that split to be stated rather than left to be discovered,
// so: THIS FILE, and the gate in internal/agent/dispatchpairing.go, are the
// PLATFORM half. They know only "items in these repos owe a paired item marked
// with these tags". They contain no research program, no notion of an audit, and
// no directory path. They ship to every consumer and are INERT until a
// deployment names a repo — Repos empty means the gate never fires, which is the
// zero value and therefore the default everywhere.
//
// The onethird standing-audit rule — *a research ticket in
// /Users/daniel/research/onethird_program owes a pre-filed independent audit* —
// is the CONFIGURATION half. It is one program's policy, explicitly "NOT a
// shipped/platform behavior", and it lives in that deployment's config.toml as
// values, not here as code. Nothing in pogo's source names it. Adding a second
// program later is a config edit, not a code change.
//
// # Why the rule routes on the REPO and not on who filed the item
//
// The onethird pre-filed-audit rule already existed, written down and agreed. It
// was missed twice in two days anyway, and the second miss is the informative
// one: the rule was not skipped, it was NEVER REACHED. It lived in one agent's
// filing checklist, so any ticket that agent did not file bypassed it silently,
// by construction. A checklist owned by one filer cannot catch a filing that
// filer did not do.
//
// The obvious repair — a marker the filer sets, in the shape of the existing
// `qa:` field — was considered and rejected for this gate. Not because filers
// are unreliable (that argument was made, and then retired: it rested on reading
// authorship out of a `creator` field that turns out to record the unix user, so
// nobody actually knows who filed the two missed tickets). It is rejected
// because a marker still requires SOMEONE TO REMEMBER SOMETHING at filing time,
// and the defect being fixed is precisely a mechanism that depends on a filer
// remembering. Routing on `repo` depends on nobody remembering anything: mg
// writes that field from the filing context, and the gate reads it.
//
// Note for anyone extending this: routing on WHO FILED an item is not merely
// undesirable here, it is unimplementable. Every agent on this host runs as one
// unix identity and the `creator` field records that identity, so authorship is
// not recoverable from the artifacts. A design that needs to tell "filed by a PM"
// from "filed by a human" is asking the store for information it does not have.
//
// # Why the obligation is default-on within a covered repo
//
// RequireTags is empty by default, and empty means EVERY item in a covered repo
// owes a pair. The asymmetry is measured, not assumed: an over-filed pair costs
// one `mg shelve`, while a missed one has now happened twice and both times
// reached the program's own state document. Requiring a positive act to CREATE
// the obligation is the thing that failed twice; so the obligation exists by
// default and the opt-out is the positive act.
//
// And the opt-out is a TAG, deliberately — something a reader can see on the
// item. An obligation waived by an absence is indistinguishable from an
// obligation nobody noticed, which is the failure mode this whole gate exists to
// remove.
type DispatchPairingConfig struct {
	// Repos lists repository paths whose items owe a paired item. Empty
	// disables the gate entirely, which is the shipped default. A path matches
	// an item whose `repo:` is that path or any directory beneath it.
	Repos []string
	// RequireTags narrows the obligation within a covered repo: when non-empty,
	// only items carrying at least one of these tags owe a pair. Empty means
	// every item in the repo owes one — see the type comment for why that is the
	// default rather than the exception.
	RequireTags []string
	// PairTags marks a paired item. It does double duty, and the second duty is
	// load-bearing: an item carrying one of these tags IS a pair, so it does not
	// itself owe one. Without that, an audit ticket filed into a covered repo
	// would owe its own audit forever.
	//
	// That double duty is why the tag must be a TIGHT marker. If a program uses
	// its pair tag loosely — say `audit` on any item that touches the audit
	// process — then every loosely-tagged item becomes exempt, and the gate goes
	// quiet exactly where the program is busiest. Pick a tag that means "this
	// item IS the paired deliverable" and nothing else.
	//
	// Empty disables the gate with a warning: an obligation that no item can
	// ever satisfy would refuse every dispatch in the repo with no way out but a
	// config edit.
	PairTags []string
	// WaiverTags are the visible opt-out: an item carrying one of these declares
	// that its pairing obligation was waived deliberately, and dispatches. Empty
	// means the repo has no opt-out at all — legal, and fail-closed, but a
	// deployment that wants a reader to be able to see a waiver must name one.
	WaiverTags []string
}

// PairingCovers reports whether repo is at or beneath one of the configured
// repos. Comparison is on cleaned paths with a separator-aware prefix test, so
// `/x/research/onethird_program` covers `/x/research/onethird_program/sub` but
// NOT `/x/research/onethird_program_v2` — the string-prefix version of this test
// would have quietly covered the second.
func PairingCovers(repo string, repos []string) (string, bool) {
	r := strings.TrimSpace(repo)
	if r == "" || len(repos) == 0 {
		return "", false
	}
	r = filepath.Clean(r)
	for _, want := range repos {
		w := filepath.Clean(strings.TrimSpace(want))
		if w == "" || w == "." {
			continue
		}
		if r == w || strings.HasPrefix(r, w+string(filepath.Separator)) {
			return want, true
		}
	}
	return "", false
}

// PairingOwed reports whether an item with the given repo and tags owes a paired
// item, and returns the configured repo entry that covers it so a refusal can
// name what put the item in scope.
//
// The exemptions are checked in the order a reader would ask about them: is this
// item itself a pair (PairTags), was the obligation waived on purpose
// (WaiverTags), and — only when RequireTags narrows the repo — is this item one
// of the kinds that owes anything.
func PairingOwed(repo string, tags []string, cfg DispatchPairingConfig) (coveringRepo string, owed bool) {
	if len(cfg.PairTags) == 0 {
		return "", false
	}
	covering, ok := PairingCovers(repo, cfg.Repos)
	if !ok {
		return "", false
	}
	if HasAnyTag(tags, cfg.PairTags) {
		return "", false
	}
	if HasAnyTag(tags, cfg.WaiverTags) {
		return "", false
	}
	if len(cfg.RequireTags) > 0 && !HasAnyTag(tags, cfg.RequireTags) {
		return "", false
	}
	return covering, true
}

// PairingSatisfiedBy reports whether a candidate item is a filed pair FOR
// targetID: it carries one of the pair tags and it references the target.
//
// Two reference channels are accepted because the store already uses both, and a
// gate that recognised only one would refuse dispatches whose pair is sitting
// right there. `depends: [mg-1234]` is the structured channel; a tag containing
// the id (`mg-1234-followup`) is the one filers reach for when the pair is a
// consequence rather than a prerequisite. Matching a tag on substring is
// deliberately loose — an id is distinctive enough that a false positive means
// someone wrote the id on purpose.
//
// A candidate never pairs with itself.
func PairingSatisfiedBy(candidateID string, candidateTags, candidateDepends []string, targetID string, pairTags []string) bool {
	if strings.TrimSpace(targetID) == "" || len(pairTags) == 0 {
		return false
	}
	if !HasAnyTag(candidateTags, pairTags) {
		return false
	}
	return referencesItem(candidateID, candidateTags, candidateDepends, targetID)
}

// referencesItem reports whether a candidate work item points at targetID
// through either channel the store uses: `depends: [mg-1234]`, or a tag
// containing the id (`mg-1234-followup`). An item never references itself.
//
// Extracted so the dispatch-pairing gate and the audit-successor detector
// (auditsuccessor.go) cannot drift apart on what "references" means. They ask
// opposite-direction questions — does a PAIR exist in advance, did a SUCCESSOR
// follow — but if the detector recognised fewer channels than the gate it would
// report as unanswered an audit whose successor the gate would have accepted,
// and the two would contradict each other over the same two files.
//
// Tag matching is on substring, deliberately loose: an mg id is distinctive
// enough that a false positive means somebody wrote that id on purpose.
func referencesItem(candidateID string, candidateTags, candidateDepends []string, targetID string) bool {
	target := strings.TrimSpace(targetID)
	if target == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(candidateID), target) {
		return false
	}
	for _, d := range candidateDepends {
		if strings.EqualFold(strings.TrimSpace(d), target) {
			return true
		}
	}
	lowerTarget := strings.ToLower(target)
	for _, t := range candidateTags {
		if strings.Contains(strings.ToLower(t), lowerTarget) {
			return true
		}
	}
	return false
}

// HasAnyTag reports whether tags contains any of want, comparing
// case-insensitively and whitespace-trimmed — the same normalization
// IsDispatchGated applies to assignees, so a hand-edited frontmatter value
// behaves the same in both gates.
func HasAnyTag(tags, want []string) bool {
	if len(tags) == 0 || len(want) == 0 {
		return false
	}
	for _, w := range want {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		for _, t := range tags {
			if strings.ToLower(strings.TrimSpace(t)) == w {
				return true
			}
		}
	}
	return false
}
