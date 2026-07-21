// Package ghteardown implements the gh-issue TEARDOWN detector (mg-6e57): for
// every gh-issue carrier work item that has reached status=done, it asks
// whether the referenced GitHub issue is actually closed — and reports the ones
// that are not.
//
// # The failure this exists to catch
//
// mg-07ba was the gh-issue carrier for drellem2/pogo#89. It reached
// `status=done, stage: merge` on 2026-07-17. Every promise made in the thread
// was fulfilled. But the workflow's LAST step — verify the issue closed, then
// close it with a comment — never ran, and #89 sat OPEN from Jul 17 to Jul 21.
//
// Nothing noticed. That is the whole problem: from the outside, a carrier that
// finished its teardown and a carrier that skipped it are the same three
// characters, `done`. The miss is an ABSENCE, and an absence emits nothing.
// It is also invisible in exactly the direction that hurts — an issue left open
// after the work lands reads to the reporter as "ignored", which is the precise
// opposite of what happened.
//
// # Detection, not action
//
// This package NEVER closes an issue and NEVER comments. Closing an external
// issue is outward-facing and stays human-gated. Its job is to make the miss
// impossible to sit on for four days, not to post on anyone's behalf. It
// mirrors internal/driftwatch: report-only, injectable seams, no mutation.
//
// # Absence of evidence is not evidence of closure
//
// The single hardest thing about this detector is that it asks a question about
// EXTERNAL state over the network, and the reassuring answer ("not open, so
// teardown must have happened") is exactly what a broken lookup produces. A
// `gh issue view` that fails — rate limit, expired auth, network, renamed repo,
// transferred issue — yields no "OPEN" token, and a naive parse reads that
// silence as closed. A silent-failure detector that fails silently is worse
// than no detector at all, because it also manufactures confidence.
//
// So issue state here is a TRI-STATE: Open, Closed, or Unknown. Unknown is a
// first-class reportable outcome, never folded into Closed. This is the same
// law the repo learned from mg-de08 and restated in mg-07ba's own body — "a
// reap must require positive evidence of death, never absence of evidence of
// life." Closure, likewise, requires positive evidence of closure.
//
// # The predicate, and the "open on purpose" problem
//
// The predicate is `workflow: gh-issue` + `status=done` + a resolvable `gh:`
// ref whose issue is Open. Deliberately NOT gated on `stage: merge`: the mayor's
// own carrier note on mg-c155 records that its `stage` field is "misleading",
// so stage is not load-bearing evidence. `status=done` is the positive claim
// that the workflow finished, and that is the claim being audited.
//
// That predicate fires on a real and legitimate case: a carrier whose issue is
// open ON PURPOSE. mg-c155 is done, but drellem2/pogo#88 is correctly open,
// waiting on the reporter for a patch that Daniel explicitly asked for. Closing
// it would retract an outstanding request. If the detector shouts about #88 on
// every run, it will be muted long before the run that matters — a detector
// that cries wolf is a detector that has been turned off.
//
// The resolution is an EXPLICIT, machine-readable declaration in the carrier
// body, alongside the other carrier lines:
//
//	gh-open: waiting on reporter for a format-patch (Daniel's ask, 2026-07-20)
//
// A carrier carrying that line is classified DeclaredOpen rather than a miss,
// and does not mail. But it is still REPORTED, in its own section of the CLI
// output — because "suppressed forever and forgotten" is the same shape of
// silent absence this package exists to catch. The declaration buys silence
// from the alert channel, not invisibility.
//
// Note the asymmetry that makes this safe: the opt-out must be written by a
// human who knows why the issue is open. Nothing infers it. An un-annotated
// done carrier with an open issue is always a miss, which is the correct
// default — it fails toward noticing.
package ghteardown

import (
	"fmt"
	"sort"
	"strings"
)

// IssueState is the tri-state result of asking GitHub whether an issue is
// closed. StateUnknown is not a failure to be logged and dropped — it is a
// reportable outcome, because "we could not tell" and "teardown ran" must never
// collapse into the same answer. See the package doc.
type IssueState string

const (
	// StateOpen: GitHub positively reported the issue as open.
	StateOpen IssueState = "open"
	// StateClosed: GitHub positively reported the issue as closed. This is the
	// ONLY state that clears a carrier, and it requires a successful lookup.
	StateClosed IssueState = "closed"
	// StateUnknown: the lookup did not produce a trustworthy answer. Covers a
	// non-zero exit from gh, unparseable output, an unrecognised state string,
	// a deleted or transferred issue, and a repo that no longer resolves.
	StateUnknown IssueState = "unknown"
)

// Carrier is a gh-issue workflow carrier work item, as parsed from the mg
// store. The carrier fields live in the work-item BODY (not frontmatter) — see
// internal/agent/prompts/mayor.md and ParseBody.
type Carrier struct {
	// ID is the mg work-item id, e.g. "mg-07ba".
	ID string
	// Title is the work-item title, carried so a report can name the carrier in
	// terms a human recognises rather than a bare id.
	Title string
	// Status is the mg status, e.g. "done". The detector only ever evaluates
	// carriers whose status claims completion.
	Status string
	// Stage is the `stage:` line, e.g. "merge". Carried for the report only —
	// it is deliberately NOT part of the predicate (see package doc).
	Stage string
	// Repo is the owner/name half of the `gh:` ref, e.g. "drellem2/pogo".
	Repo string
	// Number is the issue number half of the `gh:` ref, e.g. 89.
	Number int
	// DeclaredOpenReason is the value of an optional `gh-open:` line, by which a
	// human declares this issue is open deliberately. Empty for the vast
	// majority of carriers. A carrier that sets it is reported but never mailed.
	DeclaredOpenReason string
}

// Ref renders the canonical `owner/repo#n` form used in the `gh:` line, in mail
// subjects, and in reports.
func (c Carrier) Ref() string { return fmt.Sprintf("%s/%d", c.Repo, c.Number) }

// String is Ref with the '/' before the number replaced by '#'.
func (c Carrier) String() string { return fmt.Sprintf("%s#%d", c.Repo, c.Number) }

// FindingKind classifies what the detector concluded about one carrier.
type FindingKind string

const (
	// KindMiss is the finding this package exists to produce: a carrier claiming
	// completion whose issue is still open, with no human declaration that it is
	// open on purpose. This is a teardown miss.
	KindMiss FindingKind = "teardown_miss"
	// KindIndeterminate is a carrier whose issue state could not be established.
	// It is NOT clean and must never be presented as such — it is the shape a
	// broken detector produces, so it is surfaced loudly enough to be fixed.
	KindIndeterminate FindingKind = "indeterminate"
	// KindDeclaredOpen is a carrier whose issue is open and which carries a
	// `gh-open:` declaration explaining why. Reported, never mailed.
	KindDeclaredOpen FindingKind = "declared_open"
)

// Finding is one carrier's verdict.
type Finding struct {
	Carrier Carrier
	Kind    FindingKind
	State   IssueState
	// Detail carries the lookup error for KindIndeterminate, or the human's
	// stated reason for KindDeclaredOpen. Empty for KindMiss.
	Detail string
}

// Report is the outcome of one full scan.
type Report struct {
	// Misses are carriers whose teardown did not run. The reason this package exists.
	Misses []Finding
	// Indeterminate are carriers whose issue state could not be established.
	Indeterminate []Finding
	// DeclaredOpen are carriers open on purpose, per an explicit `gh-open:` line.
	DeclaredOpen []Finding
	// Scanned is the number of carriers evaluated, so a report can distinguish
	// "checked 12, all clean" from "checked 0 because the store read failed" —
	// two very different facts that both otherwise render as "no findings".
	Scanned int
}

// Actionable reports whether the scan found anything a human must look at.
// Indeterminate counts: a detector that cannot see is itself the finding.
func (r Report) Actionable() bool { return len(r.Misses) > 0 || len(r.Indeterminate) > 0 }

// LookupFunc resolves the current state of one issue. Production binds
// GHLookup; tests substitute a table so every branch — including the failure
// branches, which are the whole point — is reachable without a network.
//
// It MUST NOT mutate anything: no closing, no commenting. This package is a
// detector, and an implementation with side effects would break the report-only
// guarantee that lets it run unattended.
type LookupFunc func(repo string, number int) (IssueState, error)

// Detect evaluates carriers against the live issue state and classifies each.
// It is pure: no I/O of its own, no ordering assumptions about the input.
//
// A carrier whose status does not claim completion is skipped entirely — the
// detector audits a claim of doneness, and a carrier still in flight has made
// no such claim.
func Detect(carriers []Carrier, lookup LookupFunc) Report {
	var rep Report
	for _, c := range carriers {
		if !strings.EqualFold(c.Status, "done") {
			continue
		}
		rep.Scanned++

		state, err := lookup(c.Repo, c.Number)
		switch {
		case state == StateClosed:
			// The only clean outcome, and it required a positive answer.
			continue
		case state == StateOpen:
			if c.DeclaredOpenReason != "" {
				rep.DeclaredOpen = append(rep.DeclaredOpen, Finding{
					Carrier: c, Kind: KindDeclaredOpen, State: state,
					Detail: c.DeclaredOpenReason,
				})
				continue
			}
			rep.Misses = append(rep.Misses, Finding{Carrier: c, Kind: KindMiss, State: state})
		default:
			// StateUnknown, or any state a future gh version invents. Both land
			// here rather than being optimistically treated as closed.
			detail := "lookup did not return a usable issue state"
			if err != nil {
				detail = err.Error()
			}
			rep.Indeterminate = append(rep.Indeterminate, Finding{
				Carrier: c, Kind: KindIndeterminate, State: StateUnknown, Detail: detail,
			})
		}
	}

	// Stable order so repeated scans of an unchanged store produce byte-identical
	// reports — a report that reshuffles looks like it changed, and a human
	// watching for change would learn to stop reading it.
	for _, s := range [][]Finding{rep.Misses, rep.Indeterminate, rep.DeclaredOpen} {
		sort.SliceStable(s, func(i, j int) bool { return s[i].Carrier.ID < s[j].Carrier.ID })
	}
	return rep
}

// Render formats a report for human reading — the CLI body and the mail body
// are the same text, so what a human is paged about is exactly what they can
// re-derive on demand.
func (r Report) Render() string {
	var b strings.Builder

	if len(r.Misses) > 0 {
		fmt.Fprintf(&b, "TEARDOWN MISS — %d carrier(s) claim done but their issue is still OPEN:\n\n", len(r.Misses))
		for _, f := range r.Misses {
			fmt.Fprintf(&b, "  %s  %s\n", f.Carrier.ID, f.Carrier)
			fmt.Fprintf(&b, "      status=%s stage=%s\n", f.Carrier.Status, stageOr(f.Carrier.Stage))
			fmt.Fprintf(&b, "      %s\n", f.Carrier.Title)
			fmt.Fprintf(&b, "      https://github.com/%s/issues/%d\n\n", f.Carrier.Repo, f.Carrier.Number)
		}
		b.WriteString("The work claims to be finished but the issue was never closed. Verify the\n" +
			"thread is genuinely answered, then close it WITH a comment — the close is\n" +
			"outward-facing and stays human-gated, which is why this detector reports\n" +
			"rather than acts.\n\n" +
			"If an issue is open deliberately, say so in the carrier body so this stops\n" +
			"counting as a miss (it stays listed, under 'declared open'):\n" +
			"  gh-open: <why it is open on purpose>\n\n")
	}

	if len(r.Indeterminate) > 0 {
		fmt.Fprintf(&b, "INDETERMINATE — %d carrier(s) whose issue state could NOT be established:\n\n", len(r.Indeterminate))
		for _, f := range r.Indeterminate {
			fmt.Fprintf(&b, "  %s  %s\n      %s\n\n", f.Carrier.ID, f.Carrier, f.Detail)
		}
		b.WriteString("These are NOT clean. A failed lookup and a closed issue are indistinguishable\n" +
			"to a careless check, so they are reported rather than assumed shut. Common\n" +
			"causes: expired gh auth, rate limiting, a transferred or deleted issue, or a\n" +
			"renamed repo in the carrier's gh: ref.\n\n")
	}

	if len(r.DeclaredOpen) > 0 {
		fmt.Fprintf(&b, "declared open on purpose — %d carrier(s), not counted as misses:\n\n", len(r.DeclaredOpen))
		for _, f := range r.DeclaredOpen {
			fmt.Fprintf(&b, "  %s  %s\n      %s\n\n", f.Carrier.ID, f.Carrier, f.Detail)
		}
		b.WriteString("Listed because a declaration that outlives its reason is the same silent\n" +
			"absence this detector exists to catch. Re-read them occasionally.\n\n")
	}

	if !r.Actionable() && len(r.DeclaredOpen) == 0 {
		fmt.Fprintf(&b, "no teardown misses: %d done gh-issue carrier(s) scanned, every issue confirmed closed.\n", r.Scanned)
		return b.String()
	}

	fmt.Fprintf(&b, "scanned %d done gh-issue carrier(s).\n", r.Scanned)
	return b.String()
}

// MailSubject renders the one-line summary for the alert channel. Only called
// when the report is actionable.
func (r Report) MailSubject() string {
	var parts []string
	if n := len(r.Misses); n > 0 {
		refs := make([]string, 0, n)
		for _, f := range r.Misses {
			refs = append(refs, f.Carrier.String())
		}
		parts = append(parts, fmt.Sprintf("%d gh-issue teardown miss(es): %s", n, strings.Join(refs, ", ")))
	}
	if n := len(r.Indeterminate); n > 0 {
		parts = append(parts, fmt.Sprintf("%d indeterminate", n))
	}
	return strings.Join(parts, "; ")
}

func stageOr(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
