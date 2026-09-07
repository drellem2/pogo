// Package carrierdrift implements the gh-issue carrier RE-READ (mg-5d9d): for
// every LIVE gh-issue carrier in the work-item store, it asks GitHub what that
// issue looks like NOW — and reports the carriers whose record has drifted away
// from the present.
//
// # The defect, in one sentence
//
// A carrier records that an issue was noticed ONCE, and nothing re-reads the
// issue afterwards — so the carrier's existence is evidence about the PAST that
// reads as evidence about the PRESENT.
//
// # Three instances, found in one day by three different accidents
//
// All three were surfaced on 2026-09-07, none of them by an instrument:
//
//	not ACKNOWLEDGED   #159 / #160   carried, days old, no acknowledgement on the
//	                                 thread. From the reporter's side, carried-but-
//	                                 silent is INDISTINGUISHABLE from uncarried.
//	not TRIAGED        #156          carried and still at `stage: triage` 17 days
//	                                 later, while dominating a newer issue.
//	not still OPEN     #127          carried against an issue that was closed the
//	                                 SAME DAY ("This landed. Closing."). The carrier
//	                                 then sat as dispatchable work for a MONTH.
//	                                 Dispatching it would have sent a triage worker
//	                                 at a solved problem and posted an
//	                                 acknowledgement comment on a closed issue.
//
// The three symptoms differ. The missing operation is identical: a re-read of the
// issue's current state for every carried issue. That is why this is one detector
// with three finding kinds rather than three detectors — splitting it fragments
// one query into three builds with three gates, and the third waits behind the
// first two. That is not hypothetical: mg-8c4a was gated on 2026-07-09 and
// archived the same day, and the defect resurfaced as gh#164 two months later
// because a user re-reported it.
//
// There is no reason to think three is the total, which is the other argument
// against scoping to whichever symptom was noticed first.
//
// # Why `pogo check-intake` could not have caught any of them, and is not wrong
//
// `check-intake` measures whether an issue has a CARRIER. That is our
// bookkeeping, and it is accurate: on the morning of 2026-09-07 it read
// "44 carried, 0 uncarried" while #159 and #160 had gone days with no contact
// with their reporter, #156 had been untriaged for 17 days, and #127's carrier
// had been live against a closed issue for a month.
//
// The gap is the AXIS, not the accuracy. `check-intake` joins OPEN issues against
// carriers, so a carrier is the terminal state of its question — once one exists
// the issue leaves its population forever. This package joins from the other
// side: carriers against the issue's CURRENT state. It is the third member of a
// triple whose other two members already shipped:
//
//	internal/ghintake     an open issue with NO carrier          (the first step)
//	internal/carrierdrift a LIVE carrier whose issue has moved   (every step after)
//	internal/ghteardown   a DONE carrier whose issue stayed open (the last step)
//
// # Report-only, and here that is load-bearing
//
// This package never comments on an issue, never closes one, never edits a work
// item and never dispatches anything. It holds no seam through which it could.
// The cheapest imaginable fix for the acknowledgement instance — post the ack
// automatically when a carrier is filed — was deliberately NOT built here,
// because whether this fleet posts automated comments on other people's issues
// is a decision for a human and not a detail of a detector.
//
// # The detector must not itself record a fact once and re-read it never
//
// A remedy is an artifact of the same kind as the defect, so it is subject to
// that defect. Two places where this one could have rebuilt it, and what stops
// them:
//
//   - NOTHING HERE IS CACHED. Every sample re-reads every live carrier from the
//     store and every issue from GitHub. Report and Finding are values computed
//     from that sample and thrown away. The watcher keeps a fingerprint and a
//     first-seen map purely to decide whether to mail; both are recomputed
//     against live state on the next sample and neither can keep a finding alive
//     that has cleared, or suppress one that has not.
//   - THE ACKNOWLEDGEMENT MARKERS ARE A COPY. AckMarkers is the text the shipped
//     triage prompt tells a worker to post. If that template is reworded and this
//     list is not, the detector silently stops recognising acknowledgements — a
//     fact captured once, read forever after as current. That is this package's
//     own defect aimed at itself, so it is guarded by a test that reads the
//     shipped prompt corpus and fails when the two diverge, rather than by a
//     comment asking the next editor to remember.
package carrierdrift

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/ghteardown"
)

// IssueState, FailureClass and ClassifyLookupError are REUSED from
// internal/ghteardown rather than restated here.
//
// That taxonomy is the settled outcome of mg-dd22 — the distinction between "the
// instrument failed and this carrier was never checked" and "the instrument
// worked and the answer about this carrier is unusable", which cost one blip
// masking six real findings before it existed. A second copy of it in a sibling
// detector would be a second thing to keep correct, and the copy that drifts is
// always the one that has not been bitten yet.
type (
	// IssueState is the tri-state result of asking GitHub about an issue.
	// StateUnknown is a reportable outcome and is NEVER folded into either
	// StateOpen or StateClosed.
	IssueState = ghteardown.IssueState
	// FailureClass names why a re-read produced no usable answer.
	FailureClass = ghteardown.FailureClass
)

const (
	// StateOpen: GitHub positively reported the issue as open.
	StateOpen = ghteardown.StateOpen
	// StateClosed: GitHub positively reported the issue as closed.
	StateClosed = ghteardown.StateClosed
	// StateUnknown: the re-read did not produce a trustworthy answer.
	StateUnknown = ghteardown.StateUnknown
)

// ClassifyLookupError determines the class of a re-read failure: structurally
// when the error carries one, by reading gh's message otherwise.
func ClassifyLookupError(err error) FailureClass { return ghteardown.ClassifyLookupError(err) }

// AckMarkers are the openings that count as an ACKNOWLEDGEMENT comment on an
// issue thread, matched case-insensitively against the start of a comment body.
//
// # Why the text and not the author
//
// The obvious predicate — "a comment by somebody other than the person who filed
// it" — was measured against this fleet's live population on 2026-09-07 and does
// not discriminate at all. Of the 40 issues that live carriers pointed at, 40
// were filed by the repo owner, and 39 had zero comments from any other login.
// The acknowledgements themselves are posted by agents running under the owner's
// credential, so they carry the owner's login and `authorAssociation: OWNER` —
// the same two fields as the reporter's own comments. An author-identity
// predicate would have reported 38 of 40 carriers as unacknowledged, on a fleet
// where every one of them had in fact been acknowledged. Cry-wolf by
// construction, which is a detector that gets muted before the run that matters.
//
// The text IS decidable, because the acknowledgement is not improvised: the
// shipped triage prompt (internal/agent/prompts/templates/polecat-triage.md,
// step 3) hands the worker the exact one-line body to post, and calls it "the
// only write to the issue during triage".
//
// # Which way this fails
//
// A hand-written acknowledgement that does not open with one of these reads as
// SILENCE — a false finding, in the loud and self-correcting direction: a reader
// opens the thread, sees the reply, and clears it with a `gh-ack:` declaration
// that also records why. The quiet direction — an issue counted as acknowledged
// when nobody replied — requires someone to post this exact sentence without
// meaning it, and it is the direction that would leave a reporter waiting.
//
// It is a var rather than a const so a deployment whose acknowledgement wording
// differs can extend it, and so the drift test can read it.
var AckMarkers = []string{
	// The em-dash form is what the shipped prompt tells the worker to post.
	"thanks — looking into this",
	// The hyphen form is what a keyboard without an em-dash produces, and it is
	// the same sentence. Matching both costs nothing; matching one would make
	// the detector's answer depend on how the acknowledger typed a dash.
	"thanks - looking into this",
}

// DefaultStuckStages are the carrier stages that count as STUCK once
// StageWindow has elapsed, and the default is deliberately narrow.
//
// It contains exactly the stages a carrier is FILED at: `triage`, and the empty
// stage of a carrier whose body names none. That is not a guess about which
// stages matter — it is the only set for which the age this detector can measure
// is the age it needs.
//
// mg records no stage-change timestamp. What it records is `created`, so for a
// carrier still sitting at the stage it was filed at, the carrier's age IS that
// stage's age, exactly. For any LATER stage, carrier age is only a lower bound
// on how long the workflow has been unfinished, and reporting a lower bound as
// if it were a measurement is the shape of mistake this whole package exists to
// stop. So the later stages are a stated coverage boundary rather than a silent
// omission — see Report.Render, which prints the covered set.
//
// `gated` is the specific stage excluded most often and it deserves naming: a
// gated carrier is waiting on a human GO/NO-GO, the human owns the next move,
// and on 2026-09-07 that was 27 of the 41 live carriers on this store. Firing on
// all of them would bury the two findings that matter under a standing alarm
// nobody can clear from inside the fleet.
var DefaultStuckStages = []string{"", "triage"}

// Default windows. Each is the answer to a different question and none of them
// is a tuned threshold: they are set far enough below the measured failures that
// a real one cannot hide under them, and far enough above a normal cycle that
// the happy path never trips them.
const (
	// DefaultAckWindow is how long a reporter may wait, from the moment they
	// FILED, before the silence is a finding.
	//
	// Measured from the ISSUE's creation, not the carrier's, because it is the
	// reporter's clock that matters — they cannot see our carrier and do not know
	// one exists. #159 was filed 2026-08-22 and first acknowledged 2026-09-07:
	// sixteen days, throughout which `check-intake` read clean.
	//
	// A day, not an hour. Filing a carrier and dispatching triage is a
	// coordinator cycle plus a worker slot, and alarming inside that would make
	// this fire on the happy path.
	DefaultAckWindow = 24 * time.Hour

	// DefaultStageWindow is how long a carrier may sit at a filing stage before
	// the stage is stuck. Three days: long enough that a full triage cycle —
	// dispatch, investigate, SME consult, packet, gate — never trips it, and
	// nearly six times shorter than the 17 days #156 spent untriaged.
	DefaultStageWindow = 72 * time.Hour

	// DefaultClosedGrace is how long a carrier may stay LIVE after its issue is
	// closed.
	//
	// Short, because in the workflow as designed this window should never open:
	// the issue is closed during teardown, AFTER the carrier reaches done, so a
	// live carrier against a closed issue is anomalous from the first minute.
	// The grace exists for the chained case — a triage carrier done, a review
	// carrier still live — and for the seconds between a human closing an issue
	// and the fleet noticing.
	DefaultClosedGrace = 6 * time.Hour
)

// Declaration keys. Each buys silence from the alert channel for ONE finding
// kind, written by a human who knows why, and none of them buys invisibility:
// a declared carrier is still listed in its own section of the report, because
// "suppressed forever and forgotten" is the same silent absence this package
// exists to catch.
//
// One key per kind rather than a single blanket `gh-drift:`, so a declaration
// that an issue is deliberately closed cannot also silence a triage that has
// been stuck for three weeks. A declaration should say what it declares.
const (
	// KeyDeclaredClosed (`gh-closed:`) declares that this carrier is live against
	// a closed issue ON PURPOSE. It is not hypothetical: on 2026-09-07 mg-4aa5
	// was live against drellem2/pogo#111 — closed — because a public correction
	// posted on that thread still needed amending. Work owed on a closed issue is
	// a real state, and a detector with no way to say so gets muted.
	KeyDeclaredClosed = "gh-closed"
	// KeyDeclaredAck (`gh-ack:`) declares that the reporter HAS heard from us by
	// some route this detector cannot see — a reply worded differently, an email,
	// a linked PR thread.
	KeyDeclaredAck = "gh-ack"
	// KeyDeclaredParked (`gh-parked:`) declares that this carrier is deliberately
	// held at its current stage.
	KeyDeclaredParked = "gh-parked"
)

// Carrier is one LIVE gh-issue carrier work item, as parsed from the mg store.
// The carrier lines live in the work-item BODY, not the frontmatter.
type Carrier struct {
	// ID is the mg work-item id, e.g. "mg-e605".
	ID string
	// Title is the work-item title, so a report names the carrier in terms a
	// human recognises rather than a bare id.
	Title string
	// Status is the mg status. Only live statuses reach here — see MGSource.
	Status string
	// Stage is the `stage:` line, e.g. "triage".
	Stage string
	// Repo is the owner/name half of the `gh:` ref.
	Repo string
	// Number is the issue number half of the `gh:` ref. Zero when the ref could
	// not be parsed, which is reported rather than dropped.
	Number int
	// Created is when the work item was filed. For a carrier still at its filing
	// stage this is also that stage's own clock — see DefaultStuckStages.
	Created time.Time
	// DeclaredClosed, DeclaredAck and DeclaredParked carry the reasons from the
	// three declaration lines. Empty for the vast majority of carriers.
	DeclaredClosed string
	DeclaredAck    string
	DeclaredParked string
}

// Ref renders the canonical `owner/repo#n` form.
func (c Carrier) Ref() string { return fmt.Sprintf("%s#%d", c.Repo, c.Number) }

// Age reports how long the carrier has existed as of now.
func (c Carrier) Age(now time.Time) time.Duration {
	if c.Created.IsZero() {
		return 0
	}
	return now.Sub(c.Created)
}

// Snapshot is THE RE-READ: what the issue looks like right now. Every field on
// it is a present-tense fact fetched this sample, which is the whole point of
// the package — nothing here is remembered from when the carrier was filed.
type Snapshot struct {
	// State is the issue's current state.
	State IssueState
	// Created is when the issue was filed. The reporter's clock starts here.
	Created time.Time
	// ClosedAt is when the issue was closed; zero unless State is StateClosed.
	ClosedAt time.Time
	// Acknowledged reports whether any comment on the thread opens with one of
	// AckMarkers.
	Acknowledged bool
	// AcknowledgedAt is when the FIRST such comment was posted.
	AcknowledgedAt time.Time
	// Comments is the total number of comments on the thread, reported so a
	// reader can tell "nobody has said anything" from "several people have, and
	// none of it was an acknowledgement".
	Comments int
}

// FindingKind classifies what the re-read concluded about one carrier.
type FindingKind string

const (
	// KindIssueClosed: the carrier is live and dispatchable, and its issue is
	// CLOSED. Dispatching it sends a worker at a solved problem and posts an
	// acknowledgement on a closed thread.
	KindIssueClosed FindingKind = "issue_closed"
	// KindUnacknowledged: the issue is open, past AckWindow since the reporter
	// filed, and no comment on the thread acknowledges it. From the reporter's
	// side this is indistinguishable from having no carrier at all.
	KindUnacknowledged FindingKind = "unacknowledged"
	// KindStuckStage: the carrier has sat at a filing stage past StageWindow.
	KindStuckStage FindingKind = "stuck_stage"
	// KindIndeterminate: the instrument WORKED and the answer about this carrier
	// is unusable — no such issue, no such repo, a malformed `gh:` line. A
	// repeatable determination about the carrier.
	KindIndeterminate FindingKind = "indeterminate"
	// KindBlocked: this carrier was never re-read at all, because the INSTRUMENT
	// failed. Kept apart from KindIndeterminate for the reason mg-dd22 records:
	// a failure to measure must not be reported in the shape of a measurement.
	KindBlocked FindingKind = "blocked"
	// KindDeclared: a carrier that would be a finding and carries the matching
	// declaration. Reported, never mailed.
	KindDeclared FindingKind = "declared"
)

// Finding is one verdict about one carrier. A single carrier can produce more
// than one — an unacknowledged issue whose triage is also stuck is two separate
// facts with two separate remedies, and collapsing them would hide whichever was
// noticed second.
type Finding struct {
	Carrier  Carrier
	Snapshot Snapshot
	Kind     FindingKind
	// Age is the age this finding is measured on, and WHICH age depends on the
	// kind: time since the issue was closed, since the reporter filed, or since
	// the carrier was filed. Render says which.
	Age time.Duration
	// Detail carries the re-read error for KindIndeterminate and KindBlocked, or
	// the human's stated reason for KindDeclared.
	Detail string
	// Suppressed is the kind a KindDeclared finding would otherwise have been.
	Suppressed FindingKind
	// Class is why the re-read produced no answer, for the two no-verdict kinds.
	Class FailureClass
}

// Key identifies a finding across samples: one carrier, one kind. It is what the
// watcher ages and fingerprints, so a new finding on a carrier that already has
// one cannot reset the older one's clock.
func (f Finding) Key() string { return string(f.Kind) + "|" + f.Carrier.ID + "|" + f.Carrier.Ref() }

// Windows are the three thresholds, carried into Detect as a value so a report
// can print what it was measured against instead of implying a universal
// constant.
type Windows struct {
	// Ack is how long a reporter may wait for an acknowledgement. Zero means
	// DefaultAckWindow; NEGATIVE disables the acknowledgement check.
	Ack time.Duration
	// Stage is how long a carrier may sit at a filing stage. Zero means
	// DefaultStageWindow; NEGATIVE disables the stage check.
	Stage time.Duration
	// Closed is how long a carrier may stay live after its issue closes. Zero
	// means DefaultClosedGrace; NEGATIVE disables the grace so a closed issue
	// counts immediately.
	Closed time.Duration
	// Stages is the set of stages the stage check covers. Nil means
	// DefaultStuckStages.
	Stages []string
}

// resolve fills in the defaults, preserving the zero/negative distinction that
// lets a config omitting a key differ from one turning a check off.
func (w Windows) resolve() Windows {
	out := w
	if out.Ack == 0 {
		out.Ack = DefaultAckWindow
	}
	if out.Stage == 0 {
		out.Stage = DefaultStageWindow
	}
	if out.Closed == 0 {
		out.Closed = DefaultClosedGrace
	}
	if out.Stages == nil {
		out.Stages = append([]string(nil), DefaultStuckStages...)
	}
	return out
}

// covers reports whether the stage check applies to a carrier's stage.
func (w Windows) covers(stage string) bool {
	stage = strings.ToLower(strings.TrimSpace(stage))
	for _, s := range w.Stages {
		if strings.ToLower(strings.TrimSpace(s)) == stage {
			return true
		}
	}
	return false
}

// Report is the outcome of one full re-read pass.
type Report struct {
	// Closed are live carriers whose issue is closed.
	Closed []Finding
	// Unacknowledged are carriers whose reporter has heard nothing.
	Unacknowledged []Finding
	// StuckStage are carriers that have not moved off a filing stage.
	StuckStage []Finding
	// Indeterminate are carriers a working instrument could not resolve.
	Indeterminate []Finding
	// Blocked are carriers that were never re-read, because the instrument
	// failed. NOT a determination.
	Blocked []Finding
	// Declared are findings suppressed by an explicit human declaration.
	// Reported, never mailed.
	Declared []Finding
	// Scanned is the number of live carriers evaluated, so "checked 41, all
	// current" is distinguishable from "checked 0 because the store read failed"
	// — two very different facts that otherwise both render as no findings.
	Scanned int
	// Current counts carriers re-read and found to match the present: issue open,
	// acknowledged, stage moving. The positive half, so a clean report reads as
	// a measurement rather than as an empty one.
	Current int
	// Statuses lists the mg statuses the carrier scan covered.
	Statuses []string
	// StoreItems is how many work items were examined to find those carriers.
	StoreItems int
	// Windows echoes the thresholds this report was measured against.
	Windows Windows
}

// Actionable reports whether the pass found something a coordinator must act on.
// Blocked counts, and so does indeterminate: a detector that cannot see is
// itself the finding, and the ten hours behind ghintake's founding failure were
// ten hours in which nothing was looking.
func (r Report) Actionable() bool {
	return len(r.Closed) > 0 || len(r.Unacknowledged) > 0 || len(r.StuckStage) > 0 ||
		len(r.Indeterminate) > 0 || len(r.Blocked) > 0
}

// InstrumentFailure reports whether this pass should be read as a BROKEN
// INSTRUMENT rather than as a result: every carrier it scanned came back without
// a verdict.
//
// It takes two carriers to say this, for ghteardown's reason: with one scanned
// carrier a no-verdict pass and a no-verdict carrier are the same observation,
// and claiming instrument failure from a sample of one would be inventing the
// distinction rather than detecting it.
//
// When this is true the pass has measured NOTHING — and in particular it is not
// evidence that a previously reported finding has cleared.
func (r Report) InstrumentFailure() bool {
	return r.Scanned >= 2 && len(r.Blocked)+len(r.Indeterminate) == r.Scanned
}

// FailureClasses lists the distinct causes behind the no-verdict findings, in a
// stable order, so a report can name what went wrong in a word.
func (r Report) FailureClasses() []string {
	seen := map[FailureClass]bool{}
	var out []string
	for _, group := range [][]Finding{r.Blocked, r.Indeterminate} {
		for _, f := range group {
			if f.Class != ghteardown.FailureNone && !seen[f.Class] {
				seen[f.Class] = true
				out = append(out, string(f.Class))
			}
		}
	}
	sort.Strings(out)
	return out
}

// Findings returns every actionable finding in a stable order, for callers that
// need the flat set — the watcher's ageing and fingerprinting, and --json.
func (r Report) Findings() []Finding {
	var out []Finding
	out = append(out, r.Closed...)
	out = append(out, r.Unacknowledged...)
	out = append(out, r.StuckStage...)
	out = append(out, r.Indeterminate...)
	out = append(out, r.Blocked...)
	return out
}

// SnapshotFunc re-reads one issue's current state. Production binds GHSnapshot;
// tests substitute a table so every branch — including the failure branches,
// which are the whole point — is reachable without a network.
//
// It MUST NOT mutate anything: no closing, no commenting, no labelling. An
// implementation with side effects would break the report-only guarantee that
// lets this run unattended on other people's issue trackers.
type SnapshotFunc func(repo string, number int) (Snapshot, error)

// Detect re-reads every carrier and classifies the drift. It is pure: no I/O of
// its own beyond the injected re-read, and no ordering assumptions about input.
//
// The order of the checks is deliberate. A CLOSED issue short-circuits: once the
// issue is closed, "nobody acknowledged it" and "triage never finished" are no
// longer the news and no longer the remedy — the carrier itself is what has to
// be resolved. Acknowledgement and stage are then evaluated INDEPENDENTLY,
// because an unacknowledged issue whose triage is also stuck is two facts with
// two different fixes.
func Detect(carriers []Carrier, snap SnapshotFunc, now time.Time, w Windows) Report {
	w = w.resolve()
	rep := Report{Windows: w}

	for _, c := range carriers {
		rep.Scanned++

		s, err := snap(c.Repo, c.Number)
		if err != nil || s.State == StateUnknown || s.State == "" {
			// The re-read produced nothing usable. Which bucket it lands in is
			// decided by the CLASS, not by how it reads: "we never reached
			// GitHub" and "GitHub answered and the answer is unusable" are
			// different facts about different things (mg-dd22).
			detail := "re-read did not return a usable issue state"
			class := ghteardown.FailureSubject
			if err != nil {
				detail = err.Error()
				class = ClassifyLookupError(err)
			}
			f := Finding{Carrier: c, Snapshot: s, Detail: detail, Class: class, Age: c.Age(now)}
			if class.Instrument() {
				f.Kind = KindBlocked
				rep.Blocked = append(rep.Blocked, f)
			} else {
				f.Kind = KindIndeterminate
				rep.Indeterminate = append(rep.Indeterminate, f)
			}
			continue
		}

		if s.State == StateClosed {
			age := sinceOr(s.ClosedAt, now, c.Age(now))
			if w.Closed <= 0 || age >= w.Closed {
				f := Finding{Carrier: c, Snapshot: s, Kind: KindIssueClosed, Age: age}
				if c.DeclaredClosed != "" {
					rep.Declared = append(rep.Declared, declare(f, c.DeclaredClosed))
					continue
				}
				rep.Closed = append(rep.Closed, f)
				continue
			}
			// Inside the grace window: the teardown sequence is plausibly still
			// running. Counted as current rather than silently dropped.
			rep.Current++
			continue
		}

		hit := false

		if w.Ack > 0 && !s.Acknowledged {
			age := sinceOr(s.Created, now, 0)
			if !s.Created.IsZero() && age >= w.Ack {
				f := Finding{Carrier: c, Snapshot: s, Kind: KindUnacknowledged, Age: age}
				if c.DeclaredAck != "" {
					rep.Declared = append(rep.Declared, declare(f, c.DeclaredAck))
				} else {
					rep.Unacknowledged = append(rep.Unacknowledged, f)
					hit = true
				}
			}
		}

		if w.Stage > 0 && w.covers(c.Stage) {
			age := c.Age(now)
			if !c.Created.IsZero() && age >= w.Stage {
				f := Finding{Carrier: c, Snapshot: s, Kind: KindStuckStage, Age: age}
				if c.DeclaredParked != "" {
					rep.Declared = append(rep.Declared, declare(f, c.DeclaredParked))
				} else {
					rep.StuckStage = append(rep.StuckStage, f)
					hit = true
				}
			}
		}

		if !hit {
			rep.Current++
		}
	}

	// Stable order so repeated passes over unchanged state produce byte-identical
	// reports. Oldest first within each section — the carrier that has been
	// drifting longest is the one to act on, and it is also the one a report
	// ordered by id would bury. Ties break on the finding key so the order is
	// total.
	byAge := func(s []Finding) {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].Age != s[j].Age {
				return s[i].Age > s[j].Age
			}
			return s[i].Key() < s[j].Key()
		})
	}
	byAge(rep.Closed)
	byAge(rep.Unacknowledged)
	byAge(rep.StuckStage)
	byAge(rep.Indeterminate)
	byAge(rep.Blocked)
	byAge(rep.Declared)

	return rep
}

// declare converts a finding into its suppressed form, preserving what it would
// have been so the declared section can say what is being silenced.
func declare(f Finding, reason string) Finding {
	f.Suppressed = f.Kind
	f.Kind = KindDeclared
	f.Detail = reason
	return f
}

// sinceOr returns now-at when at is set, and the fallback otherwise. The
// fallback matters: a snapshot that carries no closedAt still describes a closed
// issue, and returning zero there would park the finding inside every grace
// window forever.
func sinceOr(at, now time.Time, fallback time.Duration) time.Duration {
	if at.IsZero() {
		return fallback
	}
	d := now.Sub(at)
	if d < 0 {
		return 0
	}
	return d
}

// IsAcknowledgement reports whether one comment body reads as an acknowledgement
// — see AckMarkers for why the predicate is the text and not the author.
func IsAcknowledgement(body string) bool {
	b := strings.ToLower(strings.TrimSpace(body))
	for _, m := range AckMarkers {
		if strings.HasPrefix(b, strings.ToLower(m)) {
			return true
		}
	}
	return false
}
