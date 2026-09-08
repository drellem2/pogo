// Package prtracking answers question 2 of the open-PR pass — "does a work
// item track this PR?" — with THREE answers instead of two, because the
// two-answer version has no cell for a PR tracked in a macguffin store this
// box cannot read, and an unrepresented state reports as the nearest available
// one.
//
// # The measurement (mg-1f04, out of mg-c496)
//
// `drellem2/macguffin` PR #28 was reported STRANDED by the open-PR pass: not
// landed, tracked by no mg item. Both halves are true OF THIS BOX. The PR's one
// review comment reads
//
//	Review round 1: PASS — Reviewer: mg-cc4b · build ticket: mg-2880 · blocking: 0 · advisory: 7
//
// and reviews `payitgov/agents docs/design/mg-artifact-delivery.md`. Neither
// `mg-2880` nor `mg-cc4b` resolves here — re-measured 2026-09-08, `mg show`
// exits non-zero for both, with `mg show mg-1f04` as the positive control at
// exit 0. They are not absent; they are in ANOTHER FLEET'S store, behind a SAML
// wall our token does not clear. The PR was tracked, reviewed and passing, and
// "untracked" was the wrong word for it.
//
// # Why this does not downgrade the finding, and why that matters more
//
// The obvious repair — call it informational and stop alarming — is WRONG, and
// it is wrong in the direction that loses the case. Daniel's ruling on the same
// PR, 2026-09-08 08:24Z, was *"re macguffin 28 close it, no PRs from payitgov
// agents"*: an external-fleet PR is exactly the thing he wants surfaced. The
// sweep's REASONING was wrong and its OUTCOME was right, and a detector can be
// both. Refuting a mechanism does not refute a finding.
//
// So StateTrackedElsewhere is ACTIONABLE, the same as StateNoTrackerFound. What
// changes is the sentence the reader gets — "tracked in a store we cannot read
// — confirm this PR should exist" instead of "nothing is carrying this work" —
// and therefore which action it invites. Neither state is silenced.
//
// # The discriminator, and its one measured limit
//
// A work-item id is read from two kinds of place, and they are not equal
// evidence:
//
//   - MECHANICAL — the head branch and the PR title. Pogo generates both from
//     the item it dispatched, so an id there is a claim by THIS fleet's naming
//     that an item exists HERE. If it does not resolve, the item is genuinely
//     gone: that is a strand, not an external tracker.
//   - PROSE — the PR body and its comments. Anybody writes these, including
//     another fleet's reviewer. An id here that does not resolve locally is
//     positive evidence of a tracker we cannot see.
//
// Mechanical evidence wins, and that ordering is what keeps the known strand a
// strand. `drellem2/pogo#93` — the PR the whole open-PR pass was built after —
// has branch `polecat-d36e3` and title `[mg-36e3] fix(deploy): …`, neither
// resolving, AND a comment reading "Triaging as mg-c76a" which DOES resolve
// (both re-measured 2026-09-08). Classify on prose first and #93 reads
// "tracked" and goes quiet; classify on mechanical first and it stays
// StateNoTrackerFound, which is the answer the pass got right the first time.
//
// The limit, stated rather than papered over: `polecat-` branch naming is not
// unique to this fleet. `payitgov/macguffin`'s default branch is `polecat-0a5f`
// (measured by the mg-cc4b review quoted above). So another fleet's polecat
// branch whose id does not resolve here is classified StateNoTrackerFound
// rather than StateTrackedElsewhere. That mis-frames the line; it does not
// silence the row, because both states are actionable and both send a human to
// look at the PR. The failure this package exists to prevent is a PR going
// quiet, and no arm of it does that.
package prtracking

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/gitgc"
)

// State is the open-PR pass's answer to "does a work item track this PR?".
type State int

const (
	// StateUnmeasured: no resolver was supplied, so no id was looked up. It is
	// a separate value for the same reason the landed-ness predicate has
	// UNJUDGED: an instrument that did not answer must not be folded into
	// either answer it could have given. Record it under "Gaps I'm watching"
	// and take no disposition on the PR this sweep.
	StateUnmeasured State = iota

	// StateTrackedHere: an id this fleet's own naming put on the PR resolves in
	// the local store. This is exactly the pass's pre-existing `tracked: yes`
	// — the arm is unchanged, deliberately, so nothing that used to alarm stops
	// alarming.
	StateTrackedHere

	// StateTrackedElsewhere: work-item ids are named on the PR, none of them
	// resolve here, and none of them came from this fleet's naming. The tracker
	// exists in a store we cannot read. ACTIONABLE — see the package comment.
	StateTrackedElsewhere

	// StateNoTrackerFound: no tracker was found. Either nothing named an id at
	// all, or this fleet's own naming named one that is not in the store —
	// which is a strand, because our naming only ever names our own items.
	StateNoTrackerFound
)

// String renders the state as the words the pass's report uses.
func (s State) String() string {
	switch s {
	case StateTrackedHere:
		return "tracked here"
	case StateTrackedElsewhere:
		return "tracked elsewhere"
	case StateNoTrackerFound:
		return "no tracker found"
	default:
		return "unmeasured"
	}
}

// PR is the subset of an open pull request this predicate reads. The split
// between the mechanical fields and the prose fields is the whole
// discriminator, so it is a property of the TYPE and not of a call site.
type PR struct {
	Repo   string // owner/name, for the report line only
	Number int

	// Mechanical — generated by pogo from the work item it dispatched.
	HeadRefName string
	Title       string

	// Prose — written by whoever wrote it, including other fleets' agents.
	Body     string
	Comments []string
}

// Result is a classification plus the evidence behind it. The evidence is not
// decoration: a row saying "tracked in a store we cannot read" is only
// actionable if the reader can see WHICH ids said so and check them.
type Result struct {
	State State

	// Mechanical ids, from the branch and title, deduplicated and sorted.
	Mechanical []string
	// Prose ids, from the body and comments, minus any that are also
	// mechanical.
	Prose []string
	// Resolved is every id, from either source, that resolves locally.
	Resolved []string
}

// Actionable reports whether this row belongs in the sweep's findings.
//
// True for BOTH StateTrackedElsewhere and StateNoTrackerFound. The point of
// splitting them was never to quiet one of them.
func (r Result) Actionable() bool {
	return r.State == StateTrackedElsewhere || r.State == StateNoTrackerFound
}

// Report renders the one line the sweep prints for this PR.
func (r Result) Report(pr PR) string {
	where := fmt.Sprintf("%s#%d", pr.Repo, pr.Number)
	if pr.Repo == "" {
		where = fmt.Sprintf("#%d", pr.Number)
	}
	switch r.State {
	case StateTrackedHere:
		return fmt.Sprintf("%s tracked here — %s", where, strings.Join(r.Resolved, ", "))
	case StateTrackedElsewhere:
		return fmt.Sprintf(
			"%s tracked in a store we cannot read — confirm this PR should exist. "+
				"Named by review artifacts on the PR: %s; none resolve locally, and this fleet's naming names none.",
			where, strings.Join(r.Prose, ", "))
	case StateNoTrackerFound:
		if len(r.Mechanical) > 0 {
			return fmt.Sprintf(
				"%s no tracker found — this fleet's naming names %s and it is not in the store.",
				where, strings.Join(r.Mechanical, ", "))
		}
		return fmt.Sprintf("%s no tracker found — nothing on the PR names a work item.", where)
	default:
		return fmt.Sprintf("%s tracking UNMEASURED — no resolver ran; take no disposition this sweep.", where)
	}
}

// Classify answers question 2 for one PR. `resolves` reports whether an id
// exists in the local macguffin store; a nil resolves yields StateUnmeasured
// rather than a negative, because every id would otherwise read "does not
// resolve" and every PR would report a finding.
func Classify(pr PR, resolves func(id string) bool) Result {
	r := Result{
		Mechanical: mechanicalIDs(pr),
		Prose:      nil,
	}
	r.Prose = without(IDs(pr.Body, strings.Join(pr.Comments, "\n")), r.Mechanical)

	if resolves == nil {
		r.State = StateUnmeasured
		return r
	}

	var mechanicalResolved bool
	for _, id := range r.Mechanical {
		if resolves(id) {
			mechanicalResolved = true
			r.Resolved = append(r.Resolved, id)
		}
	}
	var proseResolved bool
	for _, id := range r.Prose {
		if resolves(id) {
			proseResolved = true
			r.Resolved = append(r.Resolved, id)
		}
	}

	switch {
	case mechanicalResolved:
		r.State = StateTrackedHere
	case len(r.Mechanical) > 0:
		// Our own naming named an item that is not here. See the package
		// comment: this is drellem2/pogo#93, and it is a strand.
		r.State = StateNoTrackerFound
	case len(r.Prose) > 0 && !proseResolved:
		r.State = StateTrackedElsewhere
	default:
		// Either nothing named an id at all, or the only ids naming this PR
		// came from prose AND resolve locally. The second case is NOT promoted
		// to `tracked here`: a passing mention of a live item in a comment is
		// not the pass's tracking evidence, and reading it as such would take a
		// genuinely stranded PR off the report. The resolving ids are carried
		// in Resolved so the reader can check them in one command.
		r.State = StateNoTrackerFound
	}
	return r
}

// itemID matches a macguffin work-item id. Case-insensitive because a human
// writing one in prose may shout it; the result is normalised to lower case.
//
// The trailing boundary is what keeps `mg-28801` and `mg-2880x` out — a
// 4-hex-code id is exactly four characters and a longer run is not one. The
// hex class is also what keeps ordinary `mg-`-prefixed FILENAMES out: the
// evidence for this package came from a PR whose body cites
// `docs/design/mg-artifact-delivery.md`, and `arti` is not hex.
var itemID = regexp.MustCompile(`(?i)\bmg-[0-9a-f]{4}\b`)

// IDs extracts every distinct work-item id mentioned in the given texts,
// sorted. Exported because the extractor is the part a reader will want to run
// against a text by hand before believing a row.
func IDs(texts ...string) []string {
	seen := map[string]bool{}
	for _, t := range texts {
		for _, m := range itemID.FindAllString(t, -1) {
			seen[strings.ToLower(m)] = true
		}
	}
	return sorted(seen)
}

// mechanicalIDs reads the ids this fleet's own naming put on the PR: the
// `mg-XXXX` in the title, and every id the branch name could be spelling.
//
// The branch side goes through gitgc's polecat-name resolver rather than a
// second copy of the naming rule. Pogo has spelled polecat branches at least
// five ways over its history and that enumeration already exists in one place;
// a private copy here would be a second artifact stating the same rule, which
// is the failure docs/pm-open-pr-pass.md already records against itself.
func mechanicalIDs(pr PR) []string {
	seen := map[string]bool{}
	for _, id := range IDs(pr.Title) {
		seen[id] = true
	}
	if suffix := gitgc.BranchSuffix(pr.HeadRefName); suffix != "" {
		for _, c := range gitgc.ItemIDsForName(suffix) {
			if itemID.MatchString(c) {
				seen[strings.ToLower(c)] = true
			}
		}
	}
	return sorted(seen)
}

// LocalResolver returns a resolver backed by `mg show`, refusing to return one
// that cannot be trusted to say "no".
//
// controlID must be an id known to exist — the caller's own work item is the
// obvious one. If it does not resolve, `mg` is unreachable, pointed at another
// store, or broken, and EVERY subsequent lookup would answer "does not
// resolve": the sweep would then report every PR in the fleet as untracked or
// externally tracked at once. That is the negative-needs-a-positive-control
// rule applied to the one instrument this package depends on. An empty
// controlID skips the control and the caller owns the consequence.
func LocalResolver(controlID string) (func(string) bool, error) {
	resolves := func(id string) bool {
		return exec.Command("mg", "show", id).Run() == nil
	}
	if controlID == "" {
		return resolves, nil
	}
	if !resolves(controlID) {
		return nil, fmt.Errorf(
			"positive control failed: `mg show %s` did not exit 0, so a negative from this resolver means nothing",
			controlID)
	}
	return resolves, nil
}

func without(ids, exclude []string) []string {
	drop := map[string]bool{}
	for _, e := range exclude {
		drop[e] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !drop[id] {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sorted(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AutoControlID returns some work-item id that exists in the local store, for
// use as LocalResolver's positive control when the caller did not name one.
//
// It exists so the control cannot be FORGOTTEN. A `--control` flag that
// defaults to "no control" defaults to the unsafe answer: a broken `mg` then
// makes every lookup return "does not resolve", and the sweep reports the whole
// fleet as untracked or externally tracked at once, every row looking measured.
// The id is read from the store the resolver will query, so if that listing is
// empty or unreadable there is no control to be had and the caller must be told
// rather than defaulted.
func AutoControlID() (string, error) {
	out, err := exec.Command("mg", "list", "--all", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("cannot list work items to derive a positive control: %w", err)
	}
	if id := firstItemID(out); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("`mg list --all --json` named no work item, so there is no positive control to run")
}

// firstItemID returns the first `"id"` value in `mg list --json`'s NDJSON.
func firstItemID(ndjson []byte) string {
	for _, line := range strings.Split(string(ndjson), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		if item.ID != "" {
			return item.ID
		}
	}
	return ""
}
