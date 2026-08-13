// Package reviewdecl reports REVIEW TICKETS THAT DECLARE NO BUILD ITEM: work
// items whose carrier block reads `stage: review`, which are the REVIEW half of
// a gh-issue pair rather than the build half, and which carry no usable
// `reviews:` line — so pogod's done-reaper exemption can never fire for them.
//
// That middle clause is not a refinement, it is half the predicate: BOTH halves
// of the pair carry `stage: review` at the same time by design, so the stage line
// collects the population and does not classify it. buildticket.go is the whole
// of how the two are told apart and what that costs; this file's Detect calls it
// and does not re-derive it.
//
// # The absence this exists to catch (mg-253e, residual of mg-aaf6)
//
// mg-aaf6 (drellem2/pogo#131 part 3) made an open review structurally survive a
// self-closing builder: the REVIEW ticket declares `reviews: <build-item-id>` at
// creation, and cmd/pogod/donereap.go refuses to reap a builder whose item some
// live polecat's ticket names. The design is deliberately free of state anyone
// must clear later — the declaration's lifetime is bounded by the reviewer
// polecat's, not by anyone's memory.
//
// One instruction-following dependency survives it, and mg-aaf6's own author
// named it rather than claiming the guard was unconditional: THE LINE IS WRITTEN
// BY A COORDINATOR FOLLOWING AN INSTRUCTION, AND IF IT IS NOT WRITTEN NOTHING
// REPORTS THAT. The guard does not fire, the builder is reaped exactly as it was
// before mg-aaf6, and no artifact anywhere says a declaration was expected and
// missing. It degrades, silently, to the pre-PR behaviour.
//
// That is the same shape as every defect in this lineage: the miss is an
// ABSENCE, and an absence emits nothing. "Be careful to write the line" is not
// the repair — it is the thing that already failed twice in gh#131's own
// history. A sweep that reports the omission is.
//
// # Severity is low ON PURPOSE, and the report says so
//
// A missed write costs ONE ROUND, not an indefinite slot. The builder is reaped,
// the reviewer notices its counterparty is gone (polecat-review's own check,
// gh#131 parts 1+2) and mails the coordinator. That is a recoverable round.
//
// So this is defence in depth, not a hole, and the runner is priced accordingly:
// a coarse interval, a fleet mailbox, and NO escalation to `human`. Escalating a
// recoverable-round gap would be the inflation mg-253e explicitly warned against,
// and it would spend the one thing a detector cannot get back — a reader who has
// not yet learned to filter the sender.
//
// # It REPORTS ONLY, and here that is load-bearing rather than conventional
//
// Nothing here writes a `reviews:` line. The family posture (check-teardown,
// check-intake, check-verdicts) is report-only anyway, but this detector has a
// specific reason of its own: A DETECTOR THAT REPAIRS THE THING IT MEASURES
// CANNOT BE TRUSTED TO MEASURE IT. A coordinator that skipped the line may have
// had a reason, and a sweep that filled it in would thereafter report zero
// omissions whether or not any were made. There is no seam in this package
// through which a work item could be edited.
//
// # It reads the ENFORCER'S parse, not a grep
//
// The predicate is evaluated with internal/workitem.ParseCarrier — the exact
// parser client.MGWorkItemReviews hands the done-reaper. That is the whole
// reason this is not three lines of grep, and it is not a stylistic preference:
//
//   - The carrier block is only readable where it LEADS the body. A `reviews:`
//     line one row below a lead-in sentence renders perfectly under `mg show`
//     and is invisible to the exemption (mg-27d4). A grep would call that
//     declared; the guard calls it absent. A detector that disagrees with the
//     enforcer reports on a convention nobody enforces.
//   - Bodies DISCUSS the convention. This very package doc contains the string
//     `reviews:` many times over. Only an unquoted, unindented declaration in
//     the leading block is a declaration, which is precisely what the strict
//     parser already refuses to widen.
//
// # Three ways a line that IS present still leaves the builder unprotected
//
// donereap.openReviews matches by EXACT STRING EQUALITY against a live polecat's
// work-item id (cmd/pogod/donereap.go:384-390). So a present line is not the
// same as a working one:
//
//	KindSelfReference  `reviews:` naming the ticket's own id. Refused there by
//	                   name — it would exempt a polecat from its own completion
//	                   forever — so it protects nothing.
//	KindMalformed      a value that is not a bare id: `mg-aaf6 (the builder)`,
//	                   a URL, prose. It can never equal a work-item id, so the
//	                   match never happens. mg-aaf6 shipped a prompt fix for this
//	                   exact slip (commit 20a5d74); this is the mechanical half.
//	KindMissing        no line at all. The residual mg-253e was filed for.
//
// All three are the same outcome — an unprotected builder — reported apart
// because they are three different repairs.
//
// # The convention has a START DATE, and pre-convention tickets are not findings
//
// `reviews:` reached origin/main on 2026-08-12 (commit c045a9a). Every review
// ticket filed before that legitimately has no line, and a detector that flagged
// them would open with 31 false findings and never be read again. Items are
// therefore scoped on their `created:` stamp against ConventionLandedAt, and THE
// BOUNDARY IS PRINTED IN THE REPORT — a detector that silently excludes part of
// its population is the failure mode this family exists to catch, wearing the
// fix's clothes.
//
// mg-1c60 is the one pre-convention ticket that DOES carry the line (written by
// hand at 04:04Z). It is not special-cased anywhere: it simply passes the
// predicate and is listed as declared, which is what it is.
//
// # What this detector cannot see, stated here and not in a footnote
//
//	D-1  ARCHIVED AND SHELVED items are not scanned. workitem.ListAllFrom walks
//	     available/, claimed/, done/ and pending/ only. Measured on the live
//	     store when this landed: 31 of the 34 `stage: review` carriers are in
//	     archive/, and every one of them predates ConventionLandedAt — so the
//	     gap costs nothing today, and the report states the statuses it covered
//	     rather than implying it saw the store.
//	D-2  A carrier block the parser cannot reach is reported as OPAQUE, never as
//	     missing and never as declared. Its stage cannot be read, so whether it
//	     is even a review ticket is unknown. It is not counted as actionable:
//	     agent.MGDispatchGate refuses such an item outright
//	     (internal/agent/dispatchgate.go:161), so no review polecat is spawned
//	     and the exemption is never needed. It is still listed, because a
//	     population the detector could not classify must be visible.
//	D-3  A well-formed `reviews:` value naming an item that does not exist is
//	     reported as DECLARED. Resolving the id would need a second pass over a
//	     store this scan does not fully cover (see D-1), and a build item in
//	     archive/ would come back "missing" — a false finding in the one bucket
//	     that must not have any.
//	D-4  A REVIEW ticket wearing a build ticket's markers — a `depends:` edge, a
//	     `predecessor:` tag, or a title whose first word is `build` — is set
//	     aside as the build half and its missing declaration is NOT reported.
//	     That is the cost of classifying non-circularly (mg-2829), it is priced
//	     against a false-positive rate of one per issue, and every set-aside item
//	     is listed with the marker that set it aside so the reader can check it.
package reviewdecl

import (
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/client"
)

// StageReview is the `stage:` value that makes a work item a review ticket —
// the population this detector audits. Matched case-insensitively, as the
// carrier parser's consumers do elsewhere.
const StageReview = "review"

// ConventionLandedAt is when the `reviews:` carrier line reached origin/main:
// commit c045a9a, "feat(workitem,client,pogod): a review ticket DECLARES the
// build item it reviews". A review ticket filed before this had no convention to
// follow and is not a finding.
//
// It is the COMMITTER date (2026-08-12T04:51:59Z), not the author date
// (03:24:48Z). The convention became binding when it was on main, not when it
// was written, and the ~87 minutes between the two are excluded in the direction
// that under-reports rather than the one that manufactures a finding against a
// coordinator who could not yet have known. That choice costs at most a handful
// of tickets on one morning, permanently, and it is stated in the rendered
// report next to the count it affects.
var ConventionLandedAt = time.Date(2026, 8, 12, 4, 51, 59, 0, time.UTC)

// Item is one work item as this detector needs it — the carrier fields plus the
// filing stamp the boundary is applied to. Populated from
// workitem.ListAllFrom by Source, or by hand in tests.
type Item struct {
	ID     string
	Title  string
	Status string
	// Stage and Reviews are the `stage:` and `reviews:` carrier lines AS THE
	// STRICT PARSER READ THEM. They must come from workitem.ParseCarrier and not
	// from a body scan — see the package doc.
	Stage   string
	Reviews string
	// CarrierUnreadable is workitem.Carrier.Unreadable: the block exists
	// somewhere the leading-block scan cannot reach, so no line in it can be
	// trusted, including the two above.
	CarrierUnreadable bool
	// Created is the `created:` frontmatter stamp. Zero means the store recorded
	// no filing time, which is a third answer and not an old date — see
	// KindUndatable.
	Created time.Time
	// Depends and Tags are the `depends:` and `tags:` FRONTMATTER lists, split by
	// workitem.DependsList/TagList. They are what separates the build ticket from
	// the review ticket, both of which carry `stage: review` at the same time by
	// design — see buildticket.go, which is the whole of why they are read.
	//
	// Frontmatter, not carrier: these are fields `mg` itself writes and moves, so
	// unlike the carrier block they cannot be pushed out of a parser's reach by a
	// line of prose.
	Depends []string
	Tags    []string
}

// Kind classifies what the detector concluded about one item.
type Kind string

const (
	// KindMissing is the finding this package exists to produce: a review ticket
	// filed after the convention landed, with no `reviews:` line. Its builder is
	// unprotected and nothing else says so.
	KindMissing Kind = "missing"
	// KindSelfReference is a `reviews:` line naming the ticket's own id.
	// donereap refuses it by name, so it protects exactly as much as no line.
	KindSelfReference Kind = "self_reference"
	// KindMalformed is a `reviews:` value that is not a bare work-item id. The
	// exemption matches by exact string equality, so a decorated value can never
	// match anything.
	KindMalformed Kind = "malformed"
	// KindUndatable is a review ticket with no `reviews:` line AND no readable
	// `created:` stamp, so the convention boundary cannot be applied to it. It
	// is NOT resolved to either side: a failure to reach a verdict is reported
	// as one, never as a clean result.
	KindUndatable Kind = "undatable"
	// KindPreConvention is a review ticket filed before ConventionLandedAt with
	// no line. Counted and listed, never a finding.
	KindPreConvention Kind = "pre_convention"
	// KindDeclared is a review ticket with a usable declaration. The healthy
	// case, counted so a zero has a denominator.
	KindDeclared Kind = "declared"
	// KindOpaque is an item whose carrier block the parser could not reach.
	// Whether it is even a review ticket is unknown — see D-2.
	KindOpaque Kind = "opaque"
	// KindBuildTicket is an item carrying `stage: review` that is the BUILD half
	// of a gh-issue pair, not the review half. It carries no `reviews:` line
	// because it is not supposed to. Counted and listed, never a finding — see
	// buildticket.go.
	KindBuildTicket Kind = "build_ticket"
)

// Finding is one item's verdict.
type Finding struct {
	Item Item
	Kind Kind
	// Detail names what was read, for the kinds where a value exists — the
	// offending `reviews:` string, or the filing stamp that put an item on the
	// far side of the boundary. Empty for KindMissing, where the whole finding
	// is that there was nothing to read.
	Detail string
}

// Report is the outcome of one scan.
type Report struct {
	// Missing, SelfReference, Malformed and Undatable are the actionable
	// findings: in every one of them the done-reaper's exemption cannot fire, or
	// cannot be shown to be unnecessary.
	Missing       []Finding
	SelfReference []Finding
	Malformed     []Finding
	Undatable     []Finding
	// PreConvention and Declared are counted and listed, never alerted on.
	PreConvention []Finding
	Declared      []Finding
	// Opaque is the population the detector could not classify — see D-2.
	Opaque []Finding
	// BuildTickets are the items that carry `stage: review` and are the BUILD
	// half of a gh-issue pair. They are OUTSIDE the review-ticket population, so
	// they are not in Scanned and never actionable, and they are listed with the
	// marker that excluded them because that exclusion is the one this detector
	// most needs a reader to be able to check (mg-2829).
	BuildTickets []Finding
	// Scanned is how many review tickets were evaluated. It is the DENOMINATOR:
	// "0 missing" over an unstated population is not a pass, and mg-253e asked
	// for this explicitly (the mg-9adc/mg-e9ee family).
	Scanned int
	// Population is how many work items were read to find those review tickets,
	// so a store that came back nearly empty is visible as such rather than
	// rendering as a clean sweep.
	Population int
	// Boundary is the convention start date this run applied. Printed, because a
	// detector that silently excludes part of its population is the defect.
	Boundary time.Time
	// Statuses names the status directories the scan covered, so the report
	// states its own coverage instead of implying it saw the whole store (D-1).
	Statuses []string
}

// Actionable reports whether the scan found anything a coordinator must look at.
//
// Opaque items are deliberately NOT actionable: they are already refused by the
// dispatch gate, so they cannot reach the state this detector guards, and they
// are already reported by stallwatch. Raising them here too would mail a second
// alarm about a condition someone is already being told about — and a detector
// that duplicates another's findings gets both of them filtered.
func (r Report) Actionable() bool {
	return len(r.Missing)+len(r.SelfReference)+len(r.Malformed)+len(r.Undatable) > 0
}

// Unprotected counts the builders left without the mg-aaf6 guard: every kind in
// which a declaration is absent or unusable. It is the one number worth putting
// in a subject line.
func (r Report) Unprotected() int {
	return len(r.Missing) + len(r.SelfReference) + len(r.Malformed)
}

// Detect classifies items against the convention boundary. It is pure: no I/O,
// no ordering assumptions about the input.
//
// A zero boundary means "apply none" — every review ticket is judged against the
// convention. That is for auditing history on demand (`--since` on the CLI), not
// for the standing runner, which always passes ConventionLandedAt.
func Detect(items []Item, boundary time.Time) Report {
	rep := Report{Boundary: boundary}
	for _, it := range items {
		rep.Population++

		// Before anything else: if the block could not be read, neither could the
		// `stage:` line, so this item's membership in the population is itself
		// unknown. It must not fall through to the stage test, which would read
		// an empty Stage as "not a review ticket" — the fail-open direction
		// mg-27d4 exists to close.
		if it.CarrierUnreadable {
			rep.Opaque = append(rep.Opaque, Finding{Item: it, Kind: KindOpaque})
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(it.Stage), StageReview) {
			continue
		}

		// `stage: review` is carried by BOTH halves of a gh-issue pair at once —
		// the review ticket from creation, the build ticket from the moment its
		// PR opens — so it collects the population rather than classifying it.
		// The build half is set aside BEFORE Scanned++ because it is not a review
		// ticket, and counting it in the denominator would make the report claim
		// to have audited an item it deliberately did not. See buildticket.go.
		if marker, detail := classifyBuild(it); marker != MarkerNone {
			rep.BuildTickets = append(rep.BuildTickets, Finding{
				Item: it, Kind: KindBuildTicket, Detail: detail,
			})
			continue
		}
		rep.Scanned++

		decl := strings.TrimSpace(it.Reviews)
		switch {
		case decl == "":
			switch {
			case it.Created.IsZero():
				rep.Undatable = append(rep.Undatable, Finding{Item: it, Kind: KindUndatable})
			case it.Created.Before(boundary):
				rep.PreConvention = append(rep.PreConvention, Finding{
					Item: it, Kind: KindPreConvention, Detail: stamp(it.Created),
				})
			default:
				rep.Missing = append(rep.Missing, Finding{Item: it, Kind: KindMissing})
			}
		case decl == it.ID:
			rep.SelfReference = append(rep.SelfReference, Finding{
				Item: it, Kind: KindSelfReference, Detail: decl,
			})
		case !client.LooksLikeWorkItemID(decl):
			rep.Malformed = append(rep.Malformed, Finding{
				Item: it, Kind: KindMalformed, Detail: decl,
			})
		default:
			rep.Declared = append(rep.Declared, Finding{
				Item: it, Kind: KindDeclared, Detail: decl,
			})
		}
	}

	// Stable order so repeated scans of an unchanged store render byte-identical
	// reports. A report that reshuffles looks like it changed, and a reader
	// watching for change learns to stop reading it.
	for _, g := range [][]Finding{
		rep.Missing, rep.SelfReference, rep.Malformed, rep.Undatable,
		rep.PreConvention, rep.Declared, rep.Opaque, rep.BuildTickets,
	} {
		sort.SliceStable(g, func(i, j int) bool { return g[i].Item.ID < g[j].Item.ID })
	}
	return rep
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "(no created: stamp)"
	}
	return t.UTC().Format(time.RFC3339)
}
