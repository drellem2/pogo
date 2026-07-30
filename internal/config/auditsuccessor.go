package config

import "time"

// AuditSuccessorConfig declares repositories in which a MERGED audit — a work
// item whose deliverable is findings — must be followed, inside a bounded
// window, by a SUCCESSOR: another work item that references it, or a clean
// verdict recorded on the audit itself. An audit that merges and is followed by
// neither is a detectable failure.
//
// # This is a DETECTOR, not a gate, and the shape is deliberate
//
// Nothing here refuses anything. There is no equivalent of PairingUnmet, no
// caller at dispatch, and no path by which this config can stop a merge. That
// is not an omission to be repaired later — a gate on this event would have to
// decide whether an audit's findings WARRANT a successor, which is a judgement
// and not mechanically decidable. What IS mechanically decidable is that
// nothing happened at all, and that is the only thing this measures. The
// failure mode being covered is silence; a detector is the right shape for it.
//
// # The gap it covers, stated by the thing it sits behind
//
// DispatchPairingConfig (dispatchpairing.go) enforces that a paired audit
// EXISTS at dispatch. Its own doc comment states the residue it cannot reach:
//
//	It checks that a pair EXISTS, never that it is any good. An audit ticket
//	filed to satisfy the gate and then shelved unread satisfies it.
//
// This is that residue. The pairing gate fires at DISPATCH and asks about
// existence; this fires after MERGE and asks whether anything came of it. They
// are independent on purpose — neither can mask the other, and a deployment can
// run either alone.
//
// # The two limits, preserved here so a reader sees them without rediscovering
//
//  1. A RECORDED CLEAN VERDICT IS AN ARTIFACT SOMEONE CAN PRODUCE CHEAPLY.
//     CleanVerdictTags let an audit declare "I looked, there is nothing to
//     repair" and be counted as answered. Nothing verifies that the audit
//     actually looked. Tagging an unread audit clean silences this detector
//     exactly as filing an unread audit satisfies the pairing gate. This moves
//     the proxy one step closer to the property — merged-and-acted-on — without
//     reaching it, and the step is worth taking only because the alternative
//     proxy (a successor ticket) cannot express a genuinely clean audit at all.
//
//  2. IT DETECTS AFTER THE FACT RATHER THAN PREVENTING. By the time it fires,
//     the audit has merged and the window has elapsed. It buys nothing at the
//     moment of the merge; it buys that the omission is findable afterwards by
//     someone who was not watching. Do not grow it into a refusal — see above
//     for why the refusal it would have to make is not decidable.
//
// # What it does NOT cover
//
// The line it implements is: the product owner files the repair, the dispatcher
// does not, and this observes the silence. That converts a filing race into a
// fallback so neither party depends on being fast. The stated cost of that line
// is NOT covered here: the non-filer is often the second reader and may hold
// findings the owner's version lacks. This detector sees whether A successor
// exists, never whether it carries everything the audit found. The obligation
// to read the audit and mail what you find stays human and stays explicit.
//
// Zero value names no repos, which makes the detector inert — the shipped
// default everywhere, for the same reason DispatchPairingConfig ships inert:
// this is one deployment's policy, not platform behaviour.
type AuditSuccessorConfig struct {
	// Repos lists repository paths whose merged audits are checked. Empty
	// disables the detector entirely. Matching is PairingCovers — the same
	// separator-aware prefix test the pairing gate uses, so a deployment cannot
	// have the two disagree about which repo an item is in.
	Repos []string
	// AuditTags marks an item whose deliverable is FINDINGS. Only items carrying
	// one of these are checked.
	//
	// Empty disables the detector, and that is the fail-safe direction rather
	// than an oversight: with no marker every merged item in the repo would read
	// as an audit owing a successor, and the detector would report most of the
	// repo as failing. "Do not widen to non-audit items" is a scope rule, not a
	// preference — the signal only means anything for a ticket that produces
	// findings.
	//
	// Use the TIGHT marker, the same one DispatchPairingConfig.PairTags names.
	// A loose tag applied to anything audit-adjacent makes this fire on items
	// that never owed a successor, and a detector that fires on healthy input
	// teaches its readers to skip the line.
	AuditTags []string
	// CleanVerdictTags are carried BY THE AUDIT ITSELF and record that it found
	// nothing to repair. An audit carrying one is answered without a successor.
	// See limit 1 in the type comment: this tag is cheap to produce and nothing
	// checks it. Empty means the only way to answer is a successor ticket.
	CleanVerdictTags []string
	// Window is the grace period after the merge before an unanswered audit is
	// reported. Zero selects DefaultAuditSuccessorWindow.
	Window time.Duration
}

// DefaultAuditSuccessorWindow is the grace period between an audit merging and
// its silence being reportable.
//
// WHAT IT IS CALIBRATED AGAINST, because a window is a judgement and an
// uncalibrated one is a number someone invented:
//
// 2026-07-30 in the onethird program, whose store held 30 items tagged
// `independent-audit`, 27 of them merged. Every one of those 27 was measured —
// merge time taken from the `<id>.result.json` mtime, successor time from the
// successor's `created:` — and the distribution was:
//
//	23 of 27 produced a successor.
//	21 of those 23 had their first successor within 20 minutes of the merge.
//	The two slowest were 2h04m (mg-6653) and 2h05m (mg-f7bc).
//	The remaining 4 produced no successor at all.
//
// 4h is a little under twice the slowest successor actually observed. That
// leaves room for a lag half again as bad as the worst real one before a
// healthy audit is reported, and still fires inside the working day the audit
// merged in — which matters, because the person who can act on it is the one
// who still remembers the audit.
//
// The day is a usable positive control precisely because it is dense: audits
// merged roughly hourly and repairs followed within minutes, so a silent one
// stands out against the day's own pattern rather than against a threshold
// picked from nothing. Recalibrate against a comparable day if the program's
// cadence changes; do not adjust this to make a report go away.
const DefaultAuditSuccessorWindow = 4 * time.Hour

// AuditWindow returns the configured window, or the calibrated default when the
// config leaves it unset.
func (c AuditSuccessorConfig) AuditWindow() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return DefaultAuditSuccessorWindow
}

// AuditSuccessorEnabled reports whether the detector has enough configuration to
// check anything. Both halves are required: repos say WHERE to look and audit
// tags say WHAT to look at, and either one alone would make the detector
// meaningless in opposite directions (no repo: nothing checked; no tag: the
// whole repo checked).
func AuditSuccessorEnabled(cfg AuditSuccessorConfig) bool {
	return len(cfg.Repos) > 0 && len(cfg.AuditTags) > 0
}

// AuditItem reports whether an item with the given repo and tags is an audit
// this detector checks, returning the configured repo entry that covers it so a
// report can name what put the item in scope.
func AuditItem(repo string, tags []string, cfg AuditSuccessorConfig) (coveringRepo string, isAudit bool) {
	if !AuditSuccessorEnabled(cfg) {
		return "", false
	}
	covering, ok := PairingCovers(repo, cfg.Repos)
	if !ok {
		return "", false
	}
	if !HasAnyTag(tags, cfg.AuditTags) {
		return "", false
	}
	return covering, true
}

// AuditCleanVerdict reports whether an audit records a clean verdict on itself.
func AuditCleanVerdict(tags []string, cfg AuditSuccessorConfig) bool {
	return HasAnyTag(tags, cfg.CleanVerdictTags)
}

// SucceedsItem reports whether a candidate work item is a SUCCESSOR to targetID:
// a different item that references it, through either channel the store
// actually uses.
//
// The two channels are `depends: [mg-1234]` and a tag containing the id
// (`mg-1234-followup`), matched on substring. Both were measured on the same
// 2026-07-30 store the window is calibrated against: every one of the 23
// successors there carried BOTH, but they are accepted independently because
// the pairing gate already accepts either and a detector that recognised fewer
// channels than the gate would report as missing a successor the gate would have
// accepted as filed.
//
// This is PairingSatisfiedBy without the pair-tag requirement, and the
// difference is the direction of travel: a PAIR is filed in advance and is
// itself an audit, so it must carry the audit marker; a SUCCESSOR is filed
// afterwards and is typically a repair ticket, which carries no audit marker and
// must not be required to. Requiring one here would report every real repair as
// absent.
//
// An item never succeeds itself.
func SucceedsItem(candidateID string, candidateTags, candidateDepends []string, targetID string) bool {
	return referencesItem(candidateID, candidateTags, candidateDepends, targetID)
}
