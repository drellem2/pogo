// Package ghintake implements the gh-issue INTAKE detector (mg-039b): for every
// OPEN issue on a watched repo, it asks whether a work item exists carrying that
// issue's `gh: <owner>/<repo>#<n>` marker — and reports the issues for which
// none does.
//
// # The failure this exists to catch
//
// drellem2/pogo#99 was filed 2026-07-29 at 18:53:58Z. The issue poller mailed
// the mayor 46 seconds later, and again 20 minutes after that when Daniel
// commented. Both mails were delivered. Neither produced a work item, and the
// issue went ~10 hours with no carrier — invisible to `mg list`, to
// `mg list --tag=gh-issue`, to the stall watch, and to every board the fleet
// reads. It surfaced only because a PM ran an open-issue sweep by hand, early,
// on a hunch.
//
// The paired issue filed in the same window, #100, WAS processed into a carrier
// (mg-2fcc). So a pair Daniel explicitly filed to be considered together was
// split, and the untracked half went dark.
//
// # Why this is a detector and not a reminder
//
// Neither the poller nor mail delivery failed. What failed is the coordinator's
// mail discipline, and the shipped mayor prompt already names the exact failure
// mode: `mg mail read` marks a message read immediately, so a read-but-unhandled
// message is invisible to every later unread check — a permanent silent drop.
// The prompt prescribes act-then-mark plus an end-of-turn check.
//
// Prescribing it was not sufficient. There was no DETECTOR, only an instruction.
// That is the gap this package closes: the reconciliation is trivially
// computable — open issues minus carried refs — and until now nobody computed
// it.
//
// # Detection, not action
//
// This package never files a work item, never comments on an issue, and never
// dispatches anything. Filing the carrier is a judgement about what the issue
// IS (triage, duplicate, wontfix, a question) and that judgement belongs to the
// coordinator. Mirrors internal/ghteardown and internal/driftwatch: report-only,
// injectable seams, no mutation.
//
// # Sibling of ghteardown, opposite end of the workflow
//
// internal/ghteardown audits the LAST step: a carrier that reached done while
// its issue stayed open. This package audits the FIRST: an open issue that never
// got a carrier at all. Same workflow surface, opposite direction — and the
// intake end is the more dangerous of the two, because a teardown miss at least
// leaves a work item behind to be found. An intake miss leaves nothing.
//
// # Absence of evidence is not evidence of a carrier — in BOTH directions
//
// ghteardown's law was that a failed lookup must never read as "closed". The
// mirror-image trap lives here and it is subtler, because this detector joins
// two populations and either one can go blind in a way that LOOKS clean:
//
//   - A failed `gh issue list` yields no issues for that repo. Folded into the
//     scan, that reads as "no open issues, nothing uncarried, all clear" — the
//     detector reporting success precisely when it can see least. So a repo whose
//     issue list could not be fetched is a first-class reportable finding
//     (RepoError), never a clean repo.
//
//   - A store read that returns zero items yields no carriers. Folded in, that
//     reads as "EVERY open issue is uncarried" — a maximal-noise report that
//     looks like a catastrophe and would get the detector muted before the run
//     that matters. So a scan that examined zero work items is reported as a
//     BLIND scan (KindBlindStore) rather than as a wall of misses.
//
// The two failures are opposite in shape (one silently clean, one loudly wrong)
// and identical in consequence: the detector stops being trusted. Both are
// named outcomes here.
//
// # The carrier population: the `gh:` marker, not the tag and not the title
//
// The predicate is the `gh:` body marker, because that is the state carrier the
// mayor's filing recipe defines and the thing every downstream listing keys on.
// Deliberately NOT:
//
//   - the `gh-issue` TAG — a carrier filed without the tag is still a carrier,
//     and treating an untagged one as absent would produce a finding that stays
//     reported no matter what the coordinator does. Cry-wolf by construction.
//   - a TITLE match — pm-pogo's hand sweep explicitly noted this distinction.
//     Titles drift, get edited, and get abbreviated; the marker is a declaration.
//
// Every mg status is scanned, including archived and shelved. For intake the
// question is only "does a carrier exist at all" — an archived carrier still
// means the issue was seen and processed, and whether it should have been
// archived with the issue open is ghteardown's question, not this one.
//
// # What counts as the marker
//
// A `gh:` line is STRUCTURAL: at the start of a line, outside blockquotes and
// outside fenced code blocks. Prose that happens to mention a ref does not make
// an item a carrier. This is not pedantry — this very ticket's body quotes the
// marker syntax and cites #99 several times, and a loose parse would have let
// mg-039b suppress the finding it exists to produce.
package ghintake

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Issue is one OPEN GitHub issue on a watched repo, as listed by `gh issue
// list`. Only open issues are ever inventoried: a closed issue with no carrier
// is not a live omission.
type Issue struct {
	// Repo is the owner/name half of the ref, e.g. "drellem2/pogo".
	Repo string
	// Number is the issue number.
	Number int
	// Title is the issue title, carried so a report names the issue in terms a
	// human recognises rather than a bare number.
	Title string
	// Author is the GitHub login that filed it. Load-bearing for triage
	// priority: an uncarried issue from an outside reporter is a stranger left
	// waiting, which is a materially worse failure than one of Daniel's own.
	Author string
	// CreatedAt is when the issue was filed, used for the grace window.
	CreatedAt time.Time
	// URL is the html_url, so a finding is one click from the thread.
	URL string
}

// Ref renders the canonical `owner/repo#n` form used in the `gh:` marker.
func (i Issue) Ref() string { return fmt.Sprintf("%s#%d", i.Repo, i.Number) }

// Age reports how long the issue has been open as of now.
func (i Issue) Age(now time.Time) time.Duration {
	if i.CreatedAt.IsZero() {
		return 0
	}
	return now.Sub(i.CreatedAt)
}

// CarrierRef is one `gh:` marker found in the work-item store: the fact that
// some item claims some issue. Several items legitimately carry the same ref —
// the playbook chains triage, build and review items — so this is a many-to-one
// relation and the report names all of them.
type CarrierRef struct {
	// ItemID is the mg work-item id, e.g. "mg-d764".
	ItemID string
	// Status is the mg status the carrier was found at. Reported, never part of
	// the predicate: for intake, a carrier at any status means the issue is not
	// invisible.
	Status string
	// Ref is the normalised `owner/repo#n` the marker names.
	Ref string
}

// RepoError records a watched repo whose open-issue list could NOT be fetched.
// It is a finding, not a log line: a repo we could not read is a repo whose
// uncarried issues we cannot see, and silently omitting it would turn a blind
// spot into a clean bill of health.
type RepoError struct {
	Repo   string
	Detail string
}

// ItemError records one work item whose body could not be read, and so could not
// be checked for a `gh:` marker.
//
// This is a real and PERMANENT condition on the live store rather than a
// hypothetical: `mg show mg-3119` refuses with `ambiguous_id` because two
// archived items in different monthly partitions share that short id, and mg will
// not guess between them. Two such twins exist today.
//
// The error's DIRECTION is what makes it safe to report quietly. An item we
// cannot read is an item whose marker we cannot see, so the only mistake it can
// cause is a FALSE uncarried finding — never silence about a real one. That is
// the loud, self-correcting direction: a reader who looks finds the carrier, and
// the unreadable list is printed right there to explain why the check missed it.
//
// So these are always listed and never actionable on their own. The boundary is
// clean rather than a tuned threshold: ItemsScanned counts SUCCESSFUL body reads,
// so if every item fails the scan lands at zero and BlindStore fires, which is
// actionable. Partial blindness is a stated coverage gap; total blindness is a
// finding.
type ItemError struct {
	ID     string
	Detail string
}

// FindingKind classifies what the detector concluded about one open issue.
type FindingKind string

const (
	// KindUncarried is the finding this package exists to produce: an open issue
	// old enough to have been triaged, with no work item carrying its `gh:` ref.
	KindUncarried FindingKind = "uncarried"
	// KindFresh is an open issue with no carrier that is still inside the grace
	// window — filed minutes ago, plausibly mid-triage right now. Reported so
	// the scan is legible, never mailed. See Report.Fresh.
	KindFresh FindingKind = "fresh"
)

// Finding is one open issue's verdict.
type Finding struct {
	Issue Issue
	Kind  FindingKind
	// Age is how long the issue had been open when the scan ran, rendered in the
	// report because "uncarried for 6 minutes" and "uncarried for 6 days" are
	// the same finding with very different urgency.
	Age time.Duration
}

// Inventory is the pair of populations Detect joins, plus the coverage facts a
// report needs to state honestly what it looked at.
//
// It is a value rather than two function calls so Detect stays pure and every
// blind-scan branch — the ones that matter most and are hardest to reach in
// production — is reachable from a table in a test.
type Inventory struct {
	// Issues are the OPEN issues successfully listed across watched repos.
	Issues []Issue
	// RepoErrors are the watched repos whose issue list could not be fetched.
	// Never empty-and-ignored: see the package doc.
	RepoErrors []RepoError
	// Carriers are every `gh:` marker found in the store.
	Carriers []CarrierRef
	// ItemErrors are work items whose bodies could not be read. A stated coverage
	// gap, not a finding — see ItemError.
	ItemErrors []ItemError
	// ItemsScanned is how many work items were SUCCESSFULLY examined to produce
	// Carriers. Zero means the scan was blind — see Report.BlindStore.
	ItemsScanned int
	// Statuses lists the mg statuses the carrier scan covered.
	Statuses []string
	// Repos lists the watched repos the issue scan covered, whether or not each
	// one succeeded.
	Repos []string
}

// Report is the outcome of one full reconciliation.
type Report struct {
	// Uncarried are open issues with no carrier, past the grace window. The
	// reason this package exists.
	Uncarried []Finding
	// Fresh are uncarried issues still inside the grace window. Listed, never
	// mailed: an issue filed 90 seconds ago is not a dropped mail, it is a mail
	// in flight, and alerting on it would make the detector fire on every single
	// new issue — noise that trains a reader to filter the sender.
	Fresh []Finding
	// RepoErrors are watched repos this scan could not see into.
	RepoErrors []RepoError
	// ItemErrors are work items whose bodies could not be read. Listed, never
	// actionable on their own — see ItemError for why the direction of that error
	// makes it safe.
	ItemErrors []ItemError
	// BlindStore is set when the carrier scan examined zero work items. In that
	// state Uncarried is deliberately left EMPTY: with no carrier population
	// there is nothing to reconcile against, and reporting every open issue as a
	// miss would be the detector shouting loudest exactly when it knows least.
	BlindStore bool
	// Carried counts open issues that DO have a carrier — the positive half, so
	// "0 uncarried" can be read as "and 9 were checked and carried" rather than
	// as "the scan found nothing".
	Carried int
	// Scanned is the number of open issues evaluated.
	Scanned int
	// CarrierRefs is the number of distinct refs the store carries.
	CarrierRefs int
	// ItemsScanned, Statuses and Repos echo the Inventory's coverage facts.
	ItemsScanned int
	Statuses     []string
	Repos        []string
}

// Actionable reports whether the scan found something a coordinator must act on.
//
// A repo error counts, and so does a blind store: a detector that cannot see is
// itself the finding. That is the whole lesson of the ten hours #99 spent
// uncarried — every instrument said fine because no instrument was looking.
func (r Report) Actionable() bool {
	return len(r.Uncarried) > 0 || len(r.RepoErrors) > 0 || r.BlindStore
}

// carriersFor indexes carrier refs by normalised ref.
func carriersFor(carriers []CarrierRef) map[string][]CarrierRef {
	idx := map[string][]CarrierRef{}
	for _, c := range carriers {
		key := NormalizeRef(c.Ref)
		if key == "" {
			continue
		}
		idx[key] = append(idx[key], c)
	}
	return idx
}

// Detect joins the two populations and classifies every open issue. It is pure:
// no I/O, no ordering assumptions about the input.
//
// grace is how long an open issue is allowed to exist without a carrier before
// it becomes a finding. A non-positive grace means every uncarried issue counts
// immediately.
func Detect(inv Inventory, now time.Time, grace time.Duration) Report {
	rep := Report{
		RepoErrors:   inv.RepoErrors,
		ItemErrors:   inv.ItemErrors,
		ItemsScanned: inv.ItemsScanned,
		Statuses:     inv.Statuses,
		Repos:        inv.Repos,
	}

	idx := carriersFor(inv.Carriers)
	rep.CarrierRefs = len(idx)

	// A scan that examined no work items has no carrier population, and joining
	// against an empty set would classify every open issue as a miss. Report the
	// blindness instead. Note this is NOT the same as "the store had no
	// carriers": 2000 items with zero `gh:` markers is a fact about the store,
	// and it is reported as findings. Zero items examined is a fact about the
	// SCAN.
	if inv.ItemsScanned == 0 {
		rep.BlindStore = true
		rep.Scanned = len(inv.Issues)
		return rep
	}

	for _, iss := range inv.Issues {
		rep.Scanned++
		if len(idx[NormalizeRef(iss.Ref())]) > 0 {
			rep.Carried++
			continue
		}
		age := iss.Age(now)
		f := Finding{Issue: iss, Age: age}
		if grace > 0 && !iss.CreatedAt.IsZero() && age < grace {
			f.Kind = KindFresh
			rep.Fresh = append(rep.Fresh, f)
			continue
		}
		f.Kind = KindUncarried
		rep.Uncarried = append(rep.Uncarried, f)
	}

	// Oldest first: the issue that has been waiting longest is the one a reader
	// should act on first, and it is also the one a report ordered by number
	// would bury. Ties break on ref so repeated scans of unchanged state produce
	// byte-identical output — a report that reshuffles looks like it changed, and
	// a reader watching for change learns to stop reading it.
	byAge := func(s []Finding) {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].Age != s[j].Age {
				return s[i].Age > s[j].Age
			}
			return s[i].Issue.Ref() < s[j].Issue.Ref()
		})
	}
	byAge(rep.Uncarried)
	byAge(rep.Fresh)
	sort.SliceStable(rep.RepoErrors, func(i, j int) bool { return rep.RepoErrors[i].Repo < rep.RepoErrors[j].Repo })
	sort.SliceStable(rep.ItemErrors, func(i, j int) bool { return rep.ItemErrors[i].ID < rep.ItemErrors[j].ID })

	return rep
}

// NormalizeRef canonicalises a `gh:` ref for comparison: lowercased, trimmed,
// with any surrounding URL or decoration stripped. Returns "" when the input is
// not a usable owner/repo#number ref.
//
// Case folding matters: GitHub owner and repo names are case-insensitive, so a
// carrier filed as `gh: Drellem2/Pogo#99` covers `drellem2/pogo#99`. A
// case-sensitive compare would report an issue as uncarried while a perfectly
// good carrier sat in the store — a false alarm the coordinator could not clear.
func NormalizeRef(ref string) string {
	repo, number, err := ParseRef(ref)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s#%d", strings.ToLower(repo), number)
}

// Render formats a report for human reading. The CLI body and the mail body are
// the same text, so what a coordinator is mailed about is exactly what they can
// re-derive on demand with `pogo check-intake`.
func (r Report) Render() string {
	var b strings.Builder

	if r.BlindStore {
		b.WriteString("BLIND SCAN — the carrier scan examined ZERO work items.\n\n" +
			"No reconciliation was performed. This is reported rather than rendered as\n" +
			"'every open issue is uncarried', which is what joining against an empty carrier\n" +
			"set would produce: a wall of findings that looks like a catastrophe, is entirely\n" +
			"an artefact of the scan, and would get this detector muted before the run that\n" +
			"matters.\n\n" +
			"Likely causes: `mg` not on PATH, an unreadable or uninitialised store, or a\n" +
			"--root pointing somewhere empty.\n\n")
	}

	if len(r.Uncarried) > 0 {
		fmt.Fprintf(&b, "UNCARRIED — %d open issue(s) with NO work item carrying their `gh:` ref:\n\n", len(r.Uncarried))
		for _, f := range r.Uncarried {
			fmt.Fprintf(&b, "  %s  uncarried for %s\n", f.Issue.Ref(), humanAge(f.Age))
			fmt.Fprintf(&b, "      %s\n", f.Issue.Title)
			if f.Issue.Author != "" {
				fmt.Fprintf(&b, "      filed by %s\n", f.Issue.Author)
			}
			fmt.Fprintf(&b, "      %s\n\n", f.Issue.URL)
		}
		b.WriteString("Each of these is a reporter waiting with no acknowledgement, and none of them\n" +
			"appears in `mg list`, `mg list --tag=gh-issue`, or any other board the fleet\n" +
			"reads. File a carrier per the GH-Issue Workflow playbook in the mayor prompt:\n\n" +
			"  mg new --type=task --priority=high --tags=gh-issue --repo=<local repo path> \\\n" +
			"    --title=\"triage: <issue title>\" --body-file - <<'EOF'\n" +
			"  workflow: gh-issue\n" +
			"  stage: triage\n" +
			"  gh: <owner>/<repo>#<n>\n" +
			"  EOF\n\n" +
			"A deliberate no-carrier decision (spam, duplicate, out of scope) still needs a\n" +
			"carrier to record it — that is what makes the decision visible instead of\n" +
			"indistinguishable from a dropped mail.\n\n")
	}

	if len(r.RepoErrors) > 0 {
		fmt.Fprintf(&b, "UNREADABLE — %d watched repo(s) whose open issues could NOT be listed:\n\n", len(r.RepoErrors))
		for _, e := range r.RepoErrors {
			fmt.Fprintf(&b, "  %s\n      %s\n\n", e.Repo, e.Detail)
		}
		b.WriteString("These are NOT clean. A failed issue list and a repo with no open issues are\n" +
			"indistinguishable to a careless check, so an unreadable repo is reported rather\n" +
			"than counted as covered. Common causes: expired or missing gh auth, rate\n" +
			"limiting, offline, or a renamed/deleted repo in the watch list.\n\n")
	}

	if len(r.ItemErrors) > 0 {
		fmt.Fprintf(&b, "coverage gap — %d work item(s) whose body could not be read:\n\n", len(r.ItemErrors))
		for _, e := range r.ItemErrors {
			fmt.Fprintf(&b, "  %s\n      %s\n\n", e.ID, e.Detail)
		}
		b.WriteString("Listed, not alarmed, because of the DIRECTION of this error: an item whose body\n" +
			"we cannot read is an item whose `gh:` marker we cannot see, so it can only make\n" +
			"this check report an issue that IS carried — never stay silent about one that is\n" +
			"not. If a finding above looks wrong, one of these is the likely reason.\n\n" +
			"The known permanent case is `ambiguous_id`: two archived items in different\n" +
			"monthly partitions sharing a 4-hex short id, which mg refuses to guess between.\n\n")
	}

	if len(r.Fresh) > 0 {
		fmt.Fprintf(&b, "fresh — %d uncarried issue(s) still inside the grace window, not alarmed:\n\n", len(r.Fresh))
		for _, f := range r.Fresh {
			fmt.Fprintf(&b, "  %s  open %s — %s\n", f.Issue.Ref(), humanAge(f.Age), f.Issue.Title)
		}
		b.WriteString("\nListed because they are one grace window away from being findings, not because\n" +
			"anything is wrong: an issue filed minutes ago is a mail in flight, not a dropped\n" +
			"one.\n\n")
	}

	fmt.Fprintf(&b, "scanned %d open issue(s) across %d repo(s) [%s]; %d carried, %d uncarried.\n",
		r.Scanned, len(r.Repos), strings.Join(r.Repos, " "), r.Carried, len(r.Uncarried))
	fmt.Fprintf(&b, "carrier population: %d distinct `gh:` ref(s) across %d work item(s) in status [%s].\n",
		r.CarrierRefs, r.ItemsScanned, strings.Join(r.Statuses, " "))

	return b.String()
}

// MailSubject renders the one-line summary for the alert channel. Only called
// when the report is actionable.
func (r Report) MailSubject() string {
	var parts []string
	if r.BlindStore {
		parts = append(parts, "BLIND SCAN (0 work items examined)")
	}
	if n := len(r.Uncarried); n > 0 {
		refs := make([]string, 0, n)
		for _, f := range r.Uncarried {
			refs = append(refs, f.Issue.Ref())
		}
		parts = append(parts, fmt.Sprintf("%d open issue(s) with no carrier: %s", n, strings.Join(refs, ", ")))
	}
	if n := len(r.RepoErrors); n > 0 {
		repos := make([]string, 0, n)
		for _, e := range r.RepoErrors {
			repos = append(repos, e.Repo)
		}
		parts = append(parts, fmt.Sprintf("%d unreadable repo(s): %s", n, strings.Join(repos, ", ")))
	}
	return strings.Join(parts, "; ")
}

// humanAge renders a duration the way a reader triages by: hours and minutes up
// to a day, then days. "uncarried for 10h14m" is the sentence #99 should have
// produced.
func humanAge(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd%dh", days, int(d.Hours())%24)
}
