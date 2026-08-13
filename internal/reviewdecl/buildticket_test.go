package reviewdecl

import (
	"strings"
	"testing"
)

// buildTicket is the shape mayor.md transition 3 files and transition 4 advances:
// a build ticket chained to its triage ticket, sitting at `stage: review` because
// its PR is open. It carries no `reviews:` line and it is not supposed to.
func buildTicket(id, triage string) Item {
	return Item{
		ID: id, Title: "build: something (drellem2/pogo#145)", Status: "claimed",
		Stage: "review", Created: after, Depends: []string{triage},
	}
}

// TestBothHalvesOfAPairCarryStageReviewAndOnlyOneIsAudited is mg-2829 itself,
// reproduced from the live pair that filed it.
//
// mg-c18d is the BUILD ticket for drellem2/pogo#145; mg-bd39 is the REVIEW ticket
// and declares `reviews: mg-c18d` correctly. On 2026-08-13 the shipped detector
// reported mg-c18d as an undeclared review ticket — because both carry
// `stage: review` at once, which is by design (transition 4 advances the builder
// the moment its PR opens, and the reviewer runs in exactly that window).
//
// The failure rate is what makes this a defect rather than noise: ONE PER ISSUE,
// on every correctly-run track. This test fails if the stage line is ever allowed
// to classify the population again.
func TestBothHalvesOfAPairCarryStageReviewAndOnlyOneIsAudited(t *testing.T) {
	build := buildTicket("mg-c18d", "mg-1087")
	rev := Item{
		ID: "mg-bd39", Title: "review: doctor.md event-log fix (drellem2/pogo#145)",
		Status: "claimed", Stage: "review", Reviews: "mg-c18d", Created: after,
	}

	rep := Detect([]Item{build, rev}, ConventionLandedAt)

	if len(rep.Missing) != 0 {
		t.Fatalf("Missing = %+v, want empty — the build half of a correctly-run pair is not an "+
			"undeclared review ticket, and reporting it costs one false finding PER ISSUE", rep.Missing)
	}
	if rep.Actionable() {
		t.Errorf("a correctly-run gh-issue pair is actionable: %+v", rep)
	}
	if len(rep.BuildTickets) != 1 || rep.BuildTickets[0].Item.ID != "mg-c18d" {
		t.Fatalf("BuildTickets = %+v, want mg-c18d set aside and LISTED — an exclusion nobody can "+
			"see is indistinguishable from a scan that missed it", rep.BuildTickets)
	}
	if len(rep.Declared) != 1 || rep.Declared[0].Item.ID != "mg-bd39" {
		t.Fatalf("Declared = %+v, want mg-bd39 — the review half is still audited", rep.Declared)
	}
	if rep.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1 — the build half is NOT a review ticket, so counting it in "+
			"the denominator would claim an audit that did not happen", rep.Scanned)
	}
	if rep.Population != 2 {
		t.Errorf("Population = %d, want 2 — both were READ", rep.Population)
	}
}

// TestTheRealFindingsSurviveTheClassifier. The other two items in mg-2829's live
// report — mg-49a1 and mg-7182 — genuinely carry no `reviews:` line and are the
// population this detector exists for. A classifier that fixed the false positive
// by narrowing until nothing is reported would be worse than the defect, so the
// true positives are pinned in the same shape they appeared.
func TestTheRealFindingsSurviveTheClassifier(t *testing.T) {
	items := []Item{
		buildTicket("mg-c18d", "mg-1087"),
		{ID: "mg-49a1", Title: "review: deploy drain durability predicate (drellem2/pogo#134)",
			Status: "done", Stage: "review", Created: after},
		{ID: "mg-7182", Title: "review: deploy drain cross-poll holder ledger (drellem2/pogo#135)",
			Status: "done", Stage: "review", Created: after},
	}
	rep := Detect(items, ConventionLandedAt)

	var got []string
	for _, f := range rep.Missing {
		got = append(got, f.Item.ID)
	}
	if strings.Join(got, ",") != "mg-49a1,mg-7182" {
		t.Fatalf("Missing = %v, want exactly the two real hits", got)
	}
	if rep.Unprotected() != 2 {
		t.Errorf("Unprotected() = %d, want 2", rep.Unprotected())
	}
}

// TestSuccessorEdgeIsReadFromEitherPlace. `depends:` and a `predecessor:<id>` tag
// state the same edge, and mg derives neither from the other — mg-c18d on the
// live store carries BOTH, while mg-8424 and mg-f32a carry only the depends. A
// classifier that read one place would miss whichever half a filer wrote.
func TestSuccessorEdgeIsReadFromEitherPlace(t *testing.T) {
	for _, tc := range []struct {
		name string
		item Item
		want string
	}{
		{"depends only", Item{ID: "mg-0001", Title: "b", Stage: "review", Created: after,
			Depends: []string{"mg-1087"}}, "depends: mg-1087"},
		{"predecessor tag only", Item{ID: "mg-0001", Title: "b", Stage: "review", Created: after,
			Tags: []string{"gh-issue", "pogo", "predecessor:mg-1087"}}, "tag predecessor:mg-1087"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Detect([]Item{tc.item}, ConventionLandedAt)
			if len(rep.BuildTickets) != 1 {
				t.Fatalf("BuildTickets = %+v, want the item set aside (missing=%+v)", rep.BuildTickets, rep.Missing)
			}
			if rep.BuildTickets[0].Detail != tc.want {
				t.Errorf("Detail = %q, want %q — the report must name the exact fact the exclusion "+
					"rests on, or a reader cannot check it", rep.BuildTickets[0].Detail, tc.want)
			}
		})
	}
}

// TestEmptyDependsListIsNotAnEdge. `depends: []` splits to nothing and a review
// ticket filed the way the playbook says carries exactly that. If an empty list
// read as an edge, EVERY review ticket would be set aside and the detector would
// report a structural zero.
func TestEmptyDependsListIsNotAnEdge(t *testing.T) {
	it := review("mg-0001", after, "")
	it.Depends = []string{"", "  "}
	it.Tags = []string{"gh-issue", "pogo"}
	rep := Detect([]Item{it}, ConventionLandedAt)
	if len(rep.BuildTickets) != 0 {
		t.Fatalf("BuildTickets = %+v, want empty — an empty depends list is not a predecessor", rep.BuildTickets)
	}
	if len(rep.Missing) != 1 {
		t.Fatalf("Missing = %+v, want the undeclared review ticket still reported", rep.Missing)
	}
}

// TestTitleMarkerCatchesABuildTicketWithNoEdge. mg-55f9 and mg-dd92 on the live
// store are build tickets at `stage: review` with `depends: []` — filed before
// the triage chaining was written into the playbook. The edge cannot see them,
// which is the whole reason the weaker title marker is admitted at all.
func TestTitleMarkerCatchesABuildTicketWithNoEdge(t *testing.T) {
	for _, title := range []string{
		"build: mail correlation id — Message-Id + --in-reply-to",
		"BUILD gh#94: git GC keys worktree liveness off the BRANCH",
		"build (gh#131 part 3): a reviews: carrier line at creation",
	} {
		t.Run(title, func(t *testing.T) {
			rep := Detect([]Item{{ID: "mg-0001", Title: title, Stage: "review", Created: after}}, ConventionLandedAt)
			if len(rep.BuildTickets) != 1 {
				t.Fatalf("BuildTickets = %+v, want %q set aside (missing=%+v)", rep.BuildTickets, title, rep.Missing)
			}
			if rep.BuildTickets[0].Detail != "title begins `build`" {
				t.Errorf("Detail = %q, want the title marker named", rep.BuildTickets[0].Detail)
			}
		})
	}
}

// TestTitleMarkerIsAWordAndNotAPrefix. The title marker is prose, which is why it
// is the second witness rather than the first — and a cheap marker that widens
// silently is not cheap. `strings.HasPrefix(title, "build")` would swallow a
// review ticket whose title happens to open with a longer word, and the cost of
// that is a review ticket whose missing line is never reported.
func TestTitleMarkerIsAWordAndNotAPrefix(t *testing.T) {
	for _, title := range []string{
		"buildable artifacts are not reproducible (drellem2/pogo#200)",
		"builders must not self-close at PR-open",
		"review: build.sh timeout regression",
		"rebuild the index after a socket read (drellem2/pogo#141)",
	} {
		t.Run(title, func(t *testing.T) {
			rep := Detect([]Item{{ID: "mg-0001", Title: title, Stage: "review", Created: after}}, ConventionLandedAt)
			if len(rep.BuildTickets) != 0 {
				t.Fatalf("%q was set aside as a build ticket on a prefix match: %+v", title, rep.BuildTickets)
			}
			if len(rep.Missing) != 1 {
				t.Fatalf("Missing = %+v, want the ticket still audited", rep.Missing)
			}
		})
	}
}

// TestEitherMarkerAloneExcludes pins the OR, which is a priced choice and not an
// accident. Requiring BOTH markers would keep reporting every build ticket filed
// without a triage edge — mg-55f9 and mg-dd92 today — and the cost asymmetry runs
// hard the other way: a false positive fires once per issue forever and costs the
// detector its reader, while a false negative costs one recoverable round.
func TestEitherMarkerAloneExcludes(t *testing.T) {
	edgeOnly := Item{ID: "mg-0001", Title: "the deploy drain must track holders ACROSS polls",
		Stage: "review", Created: after, Depends: []string{"mg-fd94"}}
	titleOnly := Item{ID: "mg-0002", Title: "build: cut v0.4.0 release prep", Stage: "review", Created: after}

	rep := Detect([]Item{edgeOnly, titleOnly}, ConventionLandedAt)
	if len(rep.BuildTickets) != 2 {
		t.Fatalf("BuildTickets = %+v, want both set aside — either marker alone excludes", rep.BuildTickets)
	}
	if rep.Scanned != 0 || rep.Actionable() {
		t.Errorf("Scanned = %d, actionable = %v, want 0/false", rep.Scanned, rep.Actionable())
	}
}

// TestAReviewTicketWithAnEdgeIsExcluded is the KNOWN COST of classifying
// non-circularly, pinned as a deliberate choice rather than left to be discovered
// as a surprise. mg-3c19 on the live store — "review: reap-after-merge fix",
// `depends: [mg-a58e]` — is a review ticket filed in the older style, before the
// playbook forbade the edge, and it is the whole known population of this case.
//
// If this test ever starts failing because a real review ticket needs auditing
// despite an edge, the repair is a stronger marker (resolve the edge and check
// whether the predecessor is a triage ticket), not a quiet widening of the OR.
// D-4 in the package doc is the same statement for a reader who never runs tests.
func TestAReviewTicketWithAnEdgeIsExcluded(t *testing.T) {
	it := Item{ID: "mg-3c19", Title: "review: reap-after-merge fix (drellem2/pogo#48)",
		Status: "archive", Stage: "review", Created: after, Depends: []string{"mg-a58e"}}
	rep := Detect([]Item{it}, ConventionLandedAt)
	if len(rep.Missing) != 0 {
		t.Fatalf("Missing = %+v, want empty: the edge excludes, and that cost is documented", rep.Missing)
	}
	if len(rep.BuildTickets) != 1 {
		t.Fatalf("BuildTickets = %+v, want the item LISTED so the cost is visible rather than silent", rep.BuildTickets)
	}
	// It must be listed with the exact fact, because "check it" is the only
	// recourse a reader has against this exclusion.
	if rep.BuildTickets[0].Detail != "depends: mg-a58e" {
		t.Errorf("Detail = %q, want the edge named verbatim", rep.BuildTickets[0].Detail)
	}
}

// TestOpaqueWinsOverTheBuildMarker pins the guard ORDER, which is the same
// mg-27d4 lesson the opaque check already carries. An item whose carrier block is
// out of the parser's reach has no readable `stage:`, so whether it is even in
// this population is unknown — and a build marker sitting in FRONTMATTER (which
// is readable) must not be allowed to resolve that unknown into an answer.
func TestOpaqueWinsOverTheBuildMarker(t *testing.T) {
	it := Item{ID: "mg-0001", Title: "build: something", Stage: "review", Created: after,
		Depends: []string{"mg-1087"}, CarrierUnreadable: true}
	rep := Detect([]Item{it}, ConventionLandedAt)
	if len(rep.BuildTickets) != 0 {
		t.Fatalf("an unreadable carrier was classified from frontmatter: %+v", rep.BuildTickets)
	}
	if len(rep.Opaque) != 1 {
		t.Fatalf("Opaque = %+v, want the item — its stage could not be read, so its membership is unknown", rep.Opaque)
	}
}

// TestBuildTicketsDoNotMoveTheFingerprint. Set-aside items never mail, so a build
// ticket appearing or advancing to `stage: review` must not re-notify an unchanged
// finding set — which is exactly the event that fires once per issue.
func TestBuildTicketsDoNotMoveTheFingerprint(t *testing.T) {
	base := []Item{review("mg-0001", after, "")}
	a := Detect(base, ConventionLandedAt)
	b := Detect(append(append([]Item{}, base...), buildTicket("mg-c18d", "mg-1087")), ConventionLandedAt)
	if a.fingerprint() != b.fingerprint() {
		t.Error("a build ticket reaching `stage: review` changed the fingerprint — the mail that " +
			"fires from that is the once-per-issue notice mg-2829 was filed about")
	}
}

// TestBuildTicketOrderIsStable. Same reason the other groups are sorted: a report
// that reshuffles between runs reads as a report that changed.
func TestBuildTicketOrderIsStable(t *testing.T) {
	rep := Detect([]Item{
		buildTicket("mg-ccc1", "mg-1087"), buildTicket("mg-aaa1", "mg-1087"), buildTicket("mg-bbb1", "mg-1087"),
	}, ConventionLandedAt)
	var got []string
	for _, f := range rep.BuildTickets {
		got = append(got, f.Item.ID)
	}
	if strings.Join(got, ",") != "mg-aaa1,mg-bbb1,mg-ccc1" {
		t.Errorf("BuildTickets order = %v, want sorted by id", got)
	}
}

// TestRenderListsEverySetAsideItemAndItsMarker. The exclusion is the one thing a
// reader most needs to be able to audit, since a classifier that over-excludes
// renders identically to a quiet week. Both the item and the marker must reach
// the page, and the report must say out loud that a set-aside item's missing line
// is not reported above.
func TestRenderListsEverySetAsideItemAndItsMarker(t *testing.T) {
	out := Detect([]Item{buildTicket("mg-c18d", "mg-1087"), review("mg-0001", after, "")},
		ConventionLandedAt).Render()

	for _, want := range []string{
		"mg-c18d",
		"depends: mg-1087",
		"BUILD half",
		"NOT in the report above",
		"set aside as the BUILD half",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderCountsSetAsideItemsInTheCoverageParagraph. Every exclusion this
// detector makes is stated next to the denominator it subtracts from — the
// pre-convention and not-classifiable counts already are, and a third exclusion
// that was not would be the family's own defect wearing the fix's clothes.
func TestRenderCountsSetAsideItemsInTheCoverageParagraph(t *testing.T) {
	out := Detect([]Item{buildTicket("mg-c18d", "mg-1087")}, ConventionLandedAt).Render()
	if !strings.Contains(out, "1 set aside as the BUILD half") {
		t.Errorf("coverage paragraph does not count the set-aside item:\n%s", out)
	}
	// Scanned is zero here and the report must say so is only clean if the store
	// holds no review tickets — and it must point at the classifier, because with
	// every review-stage item set aside, the classifier is the likelier fault.
	if !strings.Contains(out, "the classifier — not the store") {
		t.Errorf("a zero-scanned run with set-aside items does not point at the classifier:\n%s", out)
	}
}

// TestFirstWord covers the letters-only stop directly, including the shapes that
// have no leading letters at all.
func TestFirstWord(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"build: x", "build"},
		{"  BUILD gh#94: y", "build"},
		{"build (gh#131 part 3): z", "build"},
		{"buildable", "buildable"},
		{"review: build.sh", "review"},
		{"", ""},
		{"   ", ""},
		{"#131 part 3", ""},
	} {
		if got := firstWord(tc.in); got != tc.want {
			t.Errorf("firstWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSourceCarriesTheBuildMarkersOffDisk closes the seam between the classifier
// and the store. `depends:` and `tags:` are FRONTMATTER, not carrier lines, and
// the detector had never read either before mg-2829 — so a classifier that works
// on hand-built fixtures and reads nothing in production is the exact shape this
// family exists to catch. This is the real parser, on the real file layout.
func TestSourceCarriesTheBuildMarkersOffDisk(t *testing.T) {
	root := t.TempDir()
	// The pair, written the way mayor.md transition 3 files it and transition 4
	// advances it: the builder chained to its triage ticket and already at
	// `stage: review` because its PR is open.
	writeStoreItem(t, root, "claimed", "mg-c18d",
		filedAfter+"depends: [mg-1087]\ntags: [gh-issue, pogo, predecessor:mg-1087]\n",
		"# build: doctor.md must read the event log it writes (drellem2/pogo#145)\n"+
			"workflow: gh-issue\nstage: review\ngh: drellem2/pogo#145\n\nBody.\n")
	writeStoreItem(t, root, "claimed", "mg-bd39",
		filedAfter+"depends: []\ntags: [gh-issue, pogo]\n",
		"# review: doctor.md event-log fix (drellem2/pogo#145)\n"+
			"workflow: gh-issue\nstage: review\ngh: drellem2/pogo#145\nreviews: mg-c18d\n\nBody.\n")

	items, err := Source{Root: root}.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	rep := Detect(items, ConventionLandedAt)

	if len(rep.Missing) != 0 {
		t.Fatalf("Missing = %+v, want empty — this is a correctly-run pair", rep.Missing)
	}
	if len(rep.BuildTickets) != 1 || rep.BuildTickets[0].Item.ID != "mg-c18d" {
		t.Fatalf("BuildTickets = %+v, want mg-c18d — if this is empty the frontmatter never "+
			"reached the classifier and it is inert in production", rep.BuildTickets)
	}
	if rep.BuildTickets[0].Detail != "depends: mg-1087" {
		t.Errorf("Detail = %q, want the depends edge read off disk", rep.BuildTickets[0].Detail)
	}
	if rep.Scanned != 1 || len(rep.Declared) != 1 {
		t.Errorf("Scanned = %d, Declared = %+v, want 1 review ticket, declared", rep.Scanned, rep.Declared)
	}
}
