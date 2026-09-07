package carrierdrift

import (
	"reflect"
	"strings"
	"testing"
)

// identityTokens are the substrings that mark a struct field as carrying a
// GitHub ACTOR IDENTITY — who did something, as opposed to what was done or
// when.
var identityTokens = []string{
	"author", "login", "user", "actor", "assignee",
	"reviewer", "reaction", "association",
}

// looksLikeActorIdentity reports whether a field name names an actor rather
// than an act.
func looksLikeActorIdentity(field string) bool {
	lower := strings.ToLower(field)
	for _, tok := range identityTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// actorIdentityFields returns the fields of typ whose names name an actor.
// Split out of the assertion so the control below can exercise the WALK and not
// only the name filter — a walk that visited nothing would satisfy the
// assertion identically and forever.
func actorIdentityFields(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		if name := typ.Field(i).Name; looksLikeActorIdentity(name) {
			out = append(out, name)
		}
	}
	return out
}

// TestSnapshotCarriesNoActorIdentity guards the constraint recorded in this
// package's doc comment (mg-a981): on this fleet, NO GitHub-side actor identity
// separates the fleet from Daniel. Every write goes through the repo owner's
// credential, so a predicate resting on which account acted returns a clean,
// well-formed answer computed over a distinction that does not exist.
//
// The package doc states that. A comment asking the next editor to remember is
// the instrument this package's own founding failure already proved
// insufficient, so the constraint gets a detector too, and this is the cheapest
// one that exists: Snapshot is the ONLY channel through which GitHub's answer
// about an issue reaches Detect. Detect is pure and does no I/O of its own, so
// an author-identity predicate cannot be written here without first adding an
// identity field to Snapshot. Refusing that addition silently is the whole
// guard — whoever adds one has to read this message to get past it, which is
// the point at which the constraint is worth knowing.
//
// It is a filter and not a frozen field list, so unrelated additions to
// Snapshot are unaffected. That is also its limit: a field named `AckedBy` or
// `RepliedByHuman` names an actor and would slip through. The test narrows the
// window; it does not close it.
func TestSnapshotCarriesNoActorIdentity(t *testing.T) {
	for _, name := range actorIdentityFields(reflect.TypeOf(Snapshot{})) {
		t.Errorf("Snapshot.%s names a GitHub ACTOR IDENTITY.\n\n"+
			"Every write this fleet makes to GitHub goes through the repo owner's\n"+
			"credential, and so does every write Daniel makes by hand. Measured\n"+
			"2026-09-07: 421 of 425 comments on issues and PRs across the two\n"+
			"watched repos are `drellem2`, and all 383 on pogo carry ONE tuple —\n"+
			"user.type=User, author_association=OWNER,\n"+
			"performed_via_github_app=null. Assignees,\n"+
			"reviewers, reviews and reactions are not constant but EMPTY.\n\n"+
			"So a predicate over this field cannot separate a fleet action from a\n"+
			"human one, and will not fail loudly when it can't: it returns a clean\n"+
			"answer computed over a distinction that does not exist. Key on the\n"+
			"comment BODY (AckMarkers), on an `mg` field, or on the dispatch record\n"+
			"in pogod — the last of which is not on GitHub at all.\n\n"+
			"If the field is for REPORTING rather than for a predicate, say so at\n"+
			"its declaration and add its name to the exemption here deliberately.\n"+
			"See this package's doc comment, and internal/ghintake's Issue.Author\n"+
			"for the one axis identity still answers.", name)
	}
}

// TestActorIdentityFilterFires is the positive control for the test above.
//
// TestSnapshotCarriesNoActorIdentity passes today by finding nothing, and a
// filter that matched nothing at all would pass it identically and forever —
// a negative result from a broken instrument, which is the failure this whole
// package exists to name. So the filter is exercised against names it MUST
// catch and names it must not.
func TestActorIdentityFilterFires(t *testing.T) {
	for _, name := range []string{
		"CommentAuthors", "AckedByLogin", "Assignees", "Reviewers",
		"Reactions", "AuthorAssociation", "UserType", "LastActor",
	} {
		if !looksLikeActorIdentity(name) {
			t.Errorf("filter missed %q, which names an actor — the guard would pass by blindness", name)
		}
	}
	for _, name := range []string{
		"State", "Created", "ClosedAt", "Acknowledged", "AcknowledgedAt", "Comments",
	} {
		if looksLikeActorIdentity(name) {
			t.Errorf("filter flagged %q, which names an act and not an actor — the guard would block unrelated work", name)
		}
	}

	// The WALK, not just the filter: a Snapshot-shaped struct that HAS grown an
	// identity field must be reported. Without this, a walk that visited no
	// fields at all would satisfy TestSnapshotCarriesNoActorIdentity forever.
	type snapshotWithIdentity struct {
		State          IssueState
		Comments       int
		CommentAuthors []string
	}
	got := actorIdentityFields(reflect.TypeOf(snapshotWithIdentity{}))
	if len(got) != 1 || got[0] != "CommentAuthors" {
		t.Errorf("walk over a struct carrying CommentAuthors reported %v, want [CommentAuthors] — "+
			"the guard on Snapshot passes by not looking", got)
	}
}
