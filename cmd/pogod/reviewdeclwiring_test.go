package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/reviewdecl"
)

// TestReviewDeclSeamsFitTheRealImplementations is the compile-time half of the
// wiring proof: the REAL source and the REAL mail sender fit the runner's
// signatures. Without it, a rename on either side breaks the daemon while every
// unit test in internal/reviewdecl still passes against its fixtures.
func TestReviewDeclSeamsFitTheRealImplementations(t *testing.T) {
	src := reviewdecl.Source{}
	w := reviewdecl.New(reviewdecl.Options{
		Enabled:  true,
		Source:   src.Items,
		Mail:     client.SendMGMail,
		Statuses: src.Statuses(),
	})
	if w == nil {
		t.Fatal("unreachable: the construction above is the assertion")
	}
}

// TestReviewDeclIsWiredToTheHeartbeat is the half that matters most for THIS
// detector, and it is not boilerplate.
//
// mg-253e's finding is that a guard nobody runs degrades silently to no guard at
// all. A detector for that failure which is itself constructed and never called
// would be the same defect one level up — and it is not a hypothetical: the
// sibling this family ported from macguffin (verdictwatch) was a correct,
// audited detector with zero schedules, zero cron entries and zero callers, and
// nothing noticed for as long as it existed.
func TestReviewDeclIsWiredToTheHeartbeat(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))
	if !strings.Contains(src, "reviewdecl.New(") {
		t.Error("pogod never constructs the review-declaration detector")
	}
	if !strings.Contains(src, "reviewDeclWatcher.Check(now)") {
		t.Error("pogod constructs the review-declaration detector but never calls Check — a detector " +
			"with no reader is exactly the silent absence mg-253e exists to report, reproduced one " +
			"level up (see internal/verdictwatch's package doc for what that cost last time)")
	}
	if !strings.Contains(src, "cfg.ReviewDecl.Enabled") {
		t.Error("the review-declaration detector is not gated on its config switch, so it cannot be turned off")
	}
	if !strings.Contains(src, "Source:        src.Items") {
		t.Error("the review-declaration detector is not wired to the real store source")
	}
}

// TestReviewDeclHasNoEscalationSeamInPogod. mg-253e: "Severity is genuinely low
// — do not inflate it. It should not preempt anything."
//
// Its two gh-issue siblings each pass an EscalateTo that copies `human` once a
// finding persists. This one deliberately does not, and the absence is easy to
// "fix" by pattern-matching the neighbouring blocks — so it is pinned here
// rather than left to a comment.
func TestReviewDeclHasNoEscalationSeamInPogod(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))
	start := strings.Index(src, "reviewdecl.New(")
	if start < 0 {
		t.Fatal("pogod never constructs the review-declaration detector")
	}
	end := strings.Index(src[start:], "})")
	if end < 0 {
		t.Fatal("could not find the end of the reviewdecl.New call")
	}
	block := src[start : start+end]
	// Precondition: prove the slice above is the construction call and not an
	// empty or mis-bounded fragment, which would make every assertion below pass
	// vacuously — the shape of failure this whole file exists to refuse.
	if !strings.Contains(block, "NotifyTo") || !strings.Contains(block, "Source:") {
		t.Fatalf("the sliced reviewdecl.New block does not look like the construction call, so the "+
			"assertions below would pass without testing anything:\n%s", block)
	}
	for _, banned := range []string{"Escalate", "escalationBox", `"human"`} {
		if strings.Contains(block, banned) {
			t.Errorf("the review-declaration detector was given %q. A missed declaration costs ONE "+
				"recoverable round, and copying `human` on a defence-in-depth gap teaches a reader "+
				"to filter every sibling detector alongside it (mg-253e)", banned)
		}
	}
}
