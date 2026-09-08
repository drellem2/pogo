package prtracking

import (
	"strings"
	"testing"
)

// ghListFixture is `gh pr list --repo drellem2/macguffin --state all --json
// number,title,headRefName,body,comments --limit 1` reduced to the fields the
// parser reads. The shape — comment bodies inline under `comments[].body` —
// was verified against the live command on 2026-09-08 before this was written.
const ghListFixture = `[
  {"number": 28,
   "title": "feat(image): publish mg as a digest-pinnable container artifact",
   "headRefName": "feat/publish-mg-image",
   "body": "recorded there in ` + "`docs/design/mg-artifact-delivery.md`" + `; this PR is the producer half.",
   "comments": [
     {"body": "## Review round 1: PASS\nReviewer: mg-cc4b · build ticket: mg-2880 · blocking: 0"},
     {"body": "Closing this — the repository owner has decided not to take image-publishing changes from outside agents."}
   ]}
]`

func TestParseGHListReadsCommentBodies(t *testing.T) {
	prs, err := parseGHList("drellem2/macguffin", []byte(ghListFixture))
	if err != nil {
		t.Fatalf("parseGHList: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want 1", len(prs))
	}
	pr := prs[0]
	if pr.Repo != "drellem2/macguffin" || pr.Number != 28 || pr.HeadRefName != "feat/publish-mg-image" {
		t.Errorf("PR = %+v, want the macguffin#28 identity", pr)
	}
	if len(pr.Comments) != 2 {
		t.Fatalf("got %d comments, want 2 — the review comment is where the external ids live", len(pr.Comments))
	}
	// End-to-end through the classifier: the parse is only useful if the ids
	// survive it.
	if got := Classify(pr, resolvesLocally); got.State != StateTrackedElsewhere {
		t.Errorf("state = %v, want %v", got.State, StateTrackedElsewhere)
	}
}

func TestParseGHListRejectsGarbage(t *testing.T) {
	if _, err := parseGHList("x/y", []byte("not json")); err == nil {
		t.Fatal("parseGHList accepted non-JSON; a repo that answered garbage must not read as a repo with no PRs")
	}
}

func TestReportRendersDenominatorsAndBothActionableClasses(t *testing.T) {
	rep := Report{
		Repos:      []string{"drellem2/macguffin", "drellem2/pogo"},
		Unreadable: map[string]string{},
		Rows: Detect([]PR{macguffin28, pogo93, {
			Repo: "drellem2/pogo", Number: 500,
			HeadRefName: "polecat-t1f04", Title: "[mg-1f04] the third answer",
		}}, resolvesLocally),
	}

	if !rep.Actionable() {
		t.Fatal("a sweep holding an external-fleet PR and a strand is not actionable")
	}
	c := rep.Counts()
	if c["tracked elsewhere"] != 1 || c["no tracker found"] != 1 || c["tracked here"] != 1 {
		t.Fatalf("counts = %v, want one of each", c)
	}

	out := rep.Render()
	for _, want := range []string{
		"2 repo(s), 3 open PR(s)",
		"1 tracked here, 1 tracked elsewhere, 1 no tracker found",
		"tracked in a store we cannot read — confirm this PR should exist",
		"Not a downgrade",
		"drellem2/pogo#93 no tracker found",
		"Landed-ness is a separate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

// A repo gh could not read must not print as a clean zero.
func TestUnreadableRepoIsActionableAndNamed(t *testing.T) {
	rep := Report{
		Repos:      []string{"payitgov/agents"},
		Unreadable: map[string]string{"payitgov/agents": "gh pr list: SAML enforcement"},
	}
	if !rep.Actionable() {
		t.Fatal("a sweep that measured nothing reported nothing to do")
	}
	out := rep.Render()
	if !strings.Contains(out, "REPOS NOT MEASURED") || !strings.Contains(out, "SAML enforcement") {
		t.Errorf("report does not name the repo it could not read:\n%s", out)
	}
	if !strings.Contains(out, "1 repo(s), 0 open PR(s)") {
		t.Errorf("report does not state the denominator that makes its zero readable:\n%s", out)
	}
}
