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
//
// # A constraint on predicate design here: identity separates OUTSIDE, not FLEET
//
// This detector keys on the `gh:` marker, and Issue.Author is carried for triage
// priority rather than used as a predicate. That is the right side of a line
// worth stating explicitly, because the next detector on this surface will be
// tempted across it (mg-a981).
//
// Every write this fleet makes to GitHub goes through the repo owner's
// credential, and so does every write Daniel makes by hand. Measured 2026-09-07
// across drellem2/pogo and drellem2/macguffin, all states: 421 of 425 comments on
// issues and PRs are `drellem2`, and all 383 on pogo carry one identical tuple
// (`user.type=User`, `author_association=OWNER`, `performed_via_github_app=null`).
// Assignees, requested reviewers, reviews and reactions are not constant but
// EMPTY — 0 reviewers and 0 reviews across 49 PRs, 0 reactions across 198
// issues+PRs. So a predicate resting on WHICH ACCOUNT ACTED cannot separate a
// fleet action from Daniel's here, and does not fail loudly when it can't: it
// returns a clean answer computed over a distinction that does not exist.
//
// Author identity is the ONE identity field on this surface that still carries
// information, because it is answering a different question — see Issue.Author.
// A future intake-side check for "has a human responded" or "did anyone other
// than the filer touch it" is NOT covered by that exception and would measure
// nothing. internal/carrierdrift states the general form and the full
// measurement, including how to re-derive it.
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
	//
	// This is the one identity field on this surface that still carries
	// information, and it is worth being precise about WHY, because the reason
	// does not generalise (mg-a981). It separates OUTSIDE from THIS BOX: 57 of
	// pogo's 127 issues and 18 of macguffin's 22 were filed by logins that are
	// not the owner's (measured 2026-09-07). It does NOT separate the FLEET from
	// DANIEL — both act under the owner's credential and are identical on every
	// identity field GitHub exposes. Anyone reaching for this field as prior art
	// for a fleet-vs-human predicate is reading the wrong axis off a field that
	// looks like it works.
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

// CredentialState is the POSITIVE CREDENTIAL PREDICATE, evaluated ONCE for the
// whole scan by whoever built the Inventory (mg-fb29).
//
// # Why a report needs it at all
//
// Every watched repo is read with the same one credential, so "no credential" is
// ONE global cause. Without it in hand this detector could only see the
// consequence — N failed `gh issue list` calls — and it amplified that one cause
// into N per-repo faults, then listed "expired or missing gh auth" FIRST among
// four equally-weighted guesses at what caused them.
//
// Both halves of that were wrong, in opposite directions, and both were measured
// on the same host:
//
//   - On 2026-08-14, four runs mailed "2 unreadable repo(s)" leading with an auth
//     guess. The credential was valid with full scopes throughout, and the actual
//     cause was a network/DNS outage corroborated by four independent instruments
//     in the same minutes. The message sent its reader at the wrong remedy.
//   - The mirror case is the one this ticket is titled for: a host with genuinely
//     no credential gets N repo findings, one per repo, none of which names the
//     single thing that must be fixed.
//
// # It is a predicate, not an inference from the failures
//
// Nothing here reads gh's stderr to decide whether a failure was an auth
// failure. That classifier stops working SILENTLY the first time gh rewords its
// message — the check keeps passing and nobody learns anything. The state is
// established up front by internal/ghtoken (whose `gh auth token` source is what
// makes "no credential" decidable at all) and carried in.
//
// # What it does not claim — and what mg-4d59 did about it
//
// CredentialPresent means a credential was established when the scan armed. It
// does NOT mean the credential is valid right now: it can have expired or been
// revoked since, and a once-at-startup predicate cannot see that. The rendering
// has always said so rather than promising more than was measured.
//
// Saying so was not enough. The third measured case on this fleet:
//
//   - From 2026-09-01 to 2026-09-08, pogod held a GH_TOKEN inherited at exec
//     from a shell 174 hours earlier. The token was rotated in `~/.zshenv`
//     afterwards; every shell and crew agent picked up the new one and the
//     daemon could not. `gh issue list` returned HTTP 401 on BOTH watched repos
//     on every sample for 173 hours while the identical command from a crew
//     agent's shell succeeded, and the report rendered all of it under
//     CredentialPresent — whose text rules a missing credential OUT and ranks
//     "EXPIRED or REVOKED" fourth. The detector failed loudly, correctly, and
//     with its reader pointed away from the only true cause, for a week.
//
// The hedge was in the body. The subject line — the part that travels — said "a
// gh credential WAS configured (source=ambient), so this is not a missing one".
//
// So the predicate gained a fourth value, CredentialRejected, and a way to reach
// it: Reverify re-asks the question on the failure path, where the answer is
// both needed and cheap. The startup snapshot stays exactly as it was for the
// happy path, which costs nothing and was never wrong.
type CredentialState string

const (
	// CredentialUnknown: nobody evaluated the predicate for this scan. The
	// honest zero value, and it renders the old four-cause list — a caller that
	// did not check gets no claim made on its behalf.
	CredentialUnknown CredentialState = ""
	// CredentialPresent: a GitHub credential was established for this scan.
	CredentialPresent CredentialState = "present"
	// CredentialMissing: no GitHub credential could be established, by any
	// source ghtoken knows — including the `gh auth login` store.
	CredentialMissing CredentialState = "missing"
	// CredentialRejected: a credential WAS established, and GitHub REFUSED it
	// (HTTP 401) when this scan asked. Existence and validity are different
	// predicates, and before mg-4d59 only the first was expressible — so this
	// state's traffic was all rendered as CredentialPresent, under a heading
	// saying a credential "WAS configured, so this is not a missing one".
	//
	// It is reached only by re-verification on the FAILURE path (see Reverify),
	// never at arm time, so it always describes the same minutes as the repo
	// errors it explains rather than a startup snapshot.
	CredentialRejected CredentialState = "rejected"
)

// CredentialFor maps a caller's credential predicate onto the pair of fields an
// Inventory carries. ok is ghtoken.Result.OK(); source is the name of the
// winning source, and is ignored when ok is false.
//
// It exists so cmd/pogod and cmd/pogo state the predicate identically in one
// line each. This package deliberately does not import ghtoken — Detect and its
// sources stay testable without a shell, a gh, or a real secret, the same reason
// it does not import config.
func CredentialFor(ok bool, source string) (CredentialState, string) {
	if !ok {
		return CredentialMissing, ""
	}
	return CredentialPresent, source
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
	// Credential is the once-per-scan credential predicate — see CredentialState
	// for why a report cannot classify an unreadable repo without it.
	Credential CredentialState
	// CredentialSource names where that credential came from ("ambient",
	// "shell", "gh-auth-token"). Reported so a reader can check the claim rather
	// than take it, and empty unless Credential is CredentialPresent.
	CredentialSource string
	// CredentialDetail is what the scan-time re-verification MEASURED, when one
	// ran — see Reverify. Empty when no repo failed, so nothing was re-asked.
	//
	// It is carried even when the re-check did NOT change the classification,
	// because "the credential could not be re-checked, the API did not answer"
	// is the strongest corroboration available for the network cause the
	// CredentialPresent branch already ranks first.
	CredentialDetail string
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
	// Credential and CredentialSource echo the Inventory's credential predicate.
	// They classify RepoErrors rather than adding findings of their own — see
	// NoCredential.
	Credential       CredentialState
	CredentialSource string
	// CredentialDetail echoes the Inventory's scan-time re-verification result.
	CredentialDetail string
}

// NoCredential reports the condition this detector used to be unable to name: no
// GitHub credential is configured, so every unreadable repo below has ONE cause
// and one remedy.
//
// It is deliberately NOT a term in Actionable(). A missing credential with no
// watched repo blinds nothing, and inventing a finding for it would be a
// standing alarm on any host that watches no repos. What it changes is how the
// repo errors that DO exist are reported: as one fault with N consequences,
// instead of as N faults.
func (r Report) NoCredential() bool { return r.Credential == CredentialMissing }

// RejectedCredential reports that a credential exists and GitHub REFUSED it —
// the state mg-4d59 added, measured at scan time rather than at arm time.
//
// Like NoCredential it is NOT a term in Actionable(), and for the same reason: a
// rejected credential with no watched repo blinds nothing. What it changes is
// how the repo errors that DO exist are reported — as one fault with N
// consequences and a remedy that is neither `gh auth login` nor a network
// investigation.
func (r Report) RejectedCredential() bool { return r.Credential == CredentialRejected }

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
		RepoErrors:       inv.RepoErrors,
		ItemErrors:       inv.ItemErrors,
		ItemsScanned:     inv.ItemsScanned,
		Statuses:         inv.Statuses,
		Repos:            inv.Repos,
		Credential:       inv.Credential,
		CredentialSource: inv.CredentialSource,
		CredentialDetail: inv.CredentialDetail,
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
		r.renderRepoErrors(&b)
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

// renderRepoErrors writes the unreadable-repo section, in one of three shapes
// chosen by the credential predicate (mg-fb29).
//
// The three-way split IS the fix. One shape said "N unreadable repos, common
// causes: expired or missing gh auth, rate limiting, offline, renamed repo" for
// every cause there is, which meant it was wrong in both directions: it hid a
// missing credential behind N repo names, and it pointed at a credential when
// the credential was fine. Two sibling instruments on this fleet already report
// this class correctly — the refinery says *infrastructure, retrying* and
// gh-teardown-watch says *this run measured nothing* — so the target is not
// hypothetical.
func (r Report) renderRepoErrors(b *strings.Builder) {
	n := len(r.RepoErrors)

	// Reverify can promote an UNRECORDED arm-time predicate straight to rejected,
	// and that path has no source name to print. "source=" with nothing after it
	// reads as a rendering bug and invites the reader to distrust the rest of a
	// message whose whole job is being believed.
	credSrc := r.CredentialSource
	if credSrc == "" {
		credSrc = "unrecorded"
	}

	switch r.Credential {
	case CredentialMissing:
		fmt.Fprintf(b, "NO GITHUB CREDENTIAL — this host has no GitHub credential, so the %d watched\n"+
			"repo(s) below could not be listed. That is ONE fault, not %d:\n\n", n, n)
	case CredentialRejected:
		fmt.Fprintf(b, "GITHUB REJECTED THIS SCAN'S CREDENTIAL — the %d watched repo(s) below could\n"+
			"not be listed because the credential this scan is using does not authenticate.\n"+
			"That is ONE fault, not %d:\n\n", n, n)
	case CredentialPresent:
		fmt.Fprintf(b, "UNREADABLE — %d watched repo(s) whose open issues could NOT be listed. "+
			"A credential WAS configured:\n\n", n)
	default:
		fmt.Fprintf(b, "UNREADABLE — %d watched repo(s) whose open issues could NOT be listed:\n\n", n)
	}

	for _, e := range r.RepoErrors {
		fmt.Fprintf(b, "  %s\n      %s\n\n", e.Repo, e.Detail)
	}

	switch r.Credential {
	case CredentialMissing:
		b.WriteString("The per-repo errors above are consequences, not causes, and fixing them one at a\n" +
			"time is not a thing anyone can do. There is one remedy:\n\n" +
			"  gh auth login          # then restart pogod, which reads the credential at startup\n\n" +
			"This was checked rather than guessed. `gh auth token` is asked for the credential\n" +
			"gh already holds, so a host authenticated by `gh auth login` — with nothing in the\n" +
			"environment and nothing in any shell profile — reads as CONFIGURED here. Only a\n" +
			"host where none of those three sources yields anything reaches this message.\n\n")
	case CredentialRejected:
		fmt.Fprintf(b, "This was MEASURED at scan time, not inferred from the failures above and not\n"+
			"read off gh's error prose: GitHub answered a direct request with HTTP 401.\n\n"+
			"  %s\n\n"+
			"The per-repo errors above are consequences of that one cause. Network is ruled\n"+
			"out — the API answered. Rate limiting is ruled out — a throttle is HTTP 403, not\n"+
			"401. A renamed or deleted repo cannot produce a 401 either, though a credential\n"+
			"this bad would hide one if it existed; that is a thing to re-check AFTER the\n"+
			"restart, not a competing explanation for what you are reading now.\n\n"+
			"The remedy is NOT `gh auth login` alone, and this is the part that cost 173 hours\n"+
			"the first time (mg-4d59). The credential that failed is the one in the SCANNING\n"+
			"PROCESS's environment (source=%s), which was copied at exec and is never re-read.\n"+
			"A shell, a crew agent and this daemon can hold three different tokens, and the\n"+
			"first two working proves nothing about the third — that contrast is exactly what\n"+
			"the escalation reported. So:\n\n"+
			"  gh auth status                  # confirm the shell's credential is good\n"+
			"  <rotate, or re-login>           # only if it is not\n"+
			"  launchctl kickstart -k gui/$(id -u)/com.pogo.daemon\n"+
			"                                  # REQUIRED, and the step that is easy to skip:\n"+
			"                                  # pogod reads the credential ONCE, at startup,\n"+
			"                                  # and cannot pick up a rotation without this\n\n"+
			"Until that restart this message will repeat with a valid token sitting in every\n"+
			"shell on the box, and NO new GitHub issue is visible to this fleet: it reaches no\n"+
			"carrier, no triage and no gate for as long as this lasts.\n\n",
			r.CredentialDetail, credSrc)
	case CredentialPresent:
		fmt.Fprintf(b, "A GitHub credential WAS established for this scan (source=%s), so \"no gh\n"+
			"credential configured\" is RULED OUT — that much was measured, not guessed. Causes\n"+
			"still open, most likely first:\n\n"+
			"  1. Network or DNS failure. An outage produces exactly this shape — every watched\n"+
			"     repo failing at once — and on 2026-08-14 it produced it four times while this\n"+
			"     message still led with an auth guess. Four other instruments on this fleet saw\n"+
			"     the same minutes as ENOTFOUND / `ssh: connect to host github.com` (mg-c058).\n"+
			"  2. Rate limiting.\n"+
			"  3. A renamed or deleted repo in the watch list, or one this credential cannot see.\n"+
			"  4. An EXPIRED or REVOKED credential. Last, and since mg-4d59 no longer merely\n"+
			"     unexcluded: this scan RE-ASKS GitHub directly whenever a repo fails, so\n"+
			"     reaching this message means the re-check did not come back HTTP 401. The line\n"+
			"     below says what it DID come back with — \"the API could not be reached\" is\n"+
			"     cause 1 corroborated, not a credential question.\n"+
			"     The residual still stands and is not talked away: the arm-time predicate\n"+
			"     cannot see a revocation since, and neither can a point measurement taken one\n"+
			"     moment ago. A 403 is deliberately left uninterpreted here — it is rate\n"+
			"     limiting as often as it is a scope problem.\n\n"+
			"That ORDER is the fix. The four causes used to be listed as equals with auth first,\n"+
			"which is how one network outage became a nine-day credential question.\n\n",
			credSrc)
		if r.CredentialDetail != "" {
			fmt.Fprintf(b, "scan-time credential re-check: %s\n\n", r.CredentialDetail)
		} else {
			// Stated rather than left blank: a missing re-check and a re-check
			// that found nothing wrong are different facts, and a reader ranking
			// causes 1-4 needs to know which one they have.
			b.WriteString("scan-time credential re-check: DID NOT RUN — nothing re-asked GitHub about\n" +
				"this credential, so cause 4 is unexcluded here rather than ruled out.\n\n")
		}
	default:
		b.WriteString("The credential predicate was NOT evaluated for this scan, so this report cannot\n" +
			"tell an auth fault from a network one. Common causes: expired or missing gh auth,\n" +
			"rate limiting, offline, or a renamed/deleted repo in the watch list.\n\n")
	}

	b.WriteString("These are NOT clean. A failed issue list and a repo with no open issues are\n" +
		"indistinguishable to a careless check, so an unreadable repo is reported rather\n" +
		"than counted as covered.\n\n")
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
		credSrc := r.CredentialSource
		if credSrc == "" {
			credSrc = "unrecorded"
		}
		repos := make([]string, 0, n)
		for _, e := range r.RepoErrors {
			repos = append(repos, e.Repo)
		}
		// The subject line is the part that travels. "2 unreadable repo(s)" is
		// what a reader skims, forwards, and files a ticket from — and the body's
		// distinction between an auth fault and a network one does not survive
		// that trip unless it is here too. This ticket exists because it wasn't:
		// the title it was filed under, and the nine days it then spent parked as
		// a credential question for a human, both came from a subject line that
		// counted repos instead of naming a cause.
		switch r.Credential {
		case CredentialMissing:
			parts = append(parts, fmt.Sprintf(
				"NO GitHub credential configured — one fault, %d repo(s) unreadable as a result: %s",
				n, strings.Join(repos, ", ")))
		case CredentialRejected:
			// Leads with the cause, in the part that travels. The subject this
			// replaces said "a gh credential WAS configured (source=%s), so this is
			// not a missing one" — true, and it is what a reader skimmed, forwarded
			// and filed a ticket from for 173 hours while GitHub was returning 401
			// to every call the sentence was about (mg-4d59). A body hedge does not
			// survive that trip; the subject has to carry it.
			parts = append(parts, fmt.Sprintf(
				"GitHub REJECTED this scan's credential (HTTP 401, source=%s) — one fault, %d repo(s) "+
					"unreadable as a result, NO new issue is visible to the fleet: %s. "+
					"Fix the credential AND restart pogod, which reads it only at startup",
				credSrc, n, strings.Join(repos, ", ")))
		case CredentialPresent:
			// States the MEASUREMENT, not the conclusion. "not an auth fault" would
			// be the same over-claim in the other direction: the predicate is a
			// startup snapshot and cannot see a credential revoked since, so a
			// subject asserting it would be this ticket's own defect rebuilt with a
			// stronger claim than the message it replaced. The body ranks the
			// remaining causes; the subject says only what was checked.
			parts = append(parts, fmt.Sprintf(
				"%d unreadable repo(s) — a gh credential WAS configured (source=%s), so this is not "+
					"a missing one: %s", n, credSrc, strings.Join(repos, ", ")))
		default:
			parts = append(parts, fmt.Sprintf("%d unreadable repo(s), cause unclassified "+
				"(no credential check ran): %s", n, strings.Join(repos, ", ")))
		}
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
