package strandedwork

import (
	"strings"
	"testing"
)

// TestDecideIsTheOneTable pins every cell of the shared decision table
// (mg-8cda), including the two precedence rules the surfaces used to disagree
// on: suggests-landed outranks a rescue, and a rescue outranks an absent score.
func TestDecideIsTheOneTable(t *testing.T) {
	rescue := &Commit{SHA: "9f1e2d3c4b5a", Subject: "RESCUE(mg-1d05): recovered"}
	absent := Presence{Added: 100, Present: 10, Measured: true}
	partly := Presence{Added: 345, Present: 309, Measured: true} // #174's 0.896
	edge := Presence{Added: 100, Present: 50, Measured: true}    // exactly ContentAbsentRatio
	landed := Presence{Added: 100, Present: 95, Measured: true}  // exactly ContentLandedRatio
	small := Presence{Added: 5, Present: 0, Measured: false}
	for _, c := range []struct {
		name   string
		p      Presence
		rescue *Commit
		want   Cell
	}{
		{"absent", absent, nil, CellResubmit},
		{"partly present", partly, nil, CellCheckByHand},
		{"at the absent threshold", edge, nil, CellCheckByHand},
		{"at the landed threshold", landed, nil, CellSuggestsLanded},
		{"unmeasured", small, nil, CellCheckByHand},
		{"unavailable", Presence{}, nil, CellCheckByHand},
		{"rescue, absent", absent, rescue, CellRescueUnbuilt},
		{"rescue, partly present", partly, rescue, CellRescueUnbuilt},
		{"rescue, suggests landed", landed, rescue, CellSuggestsLanded},
	} {
		if got := Decide(c.p, c.rescue); got != c.want {
			t.Errorf("%s: Decide = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRemedyPhraseWithholdsTheSubmitWhereTheTableDoes: only the resubmit and
// check-by-hand cells may name a submit, and only the resubmit cell leads with it.
func TestRemedyPhraseWithholdsTheSubmitWhereTheTableDoes(t *testing.T) {
	f := Finding{Repo: "/repo", Branch: "polecat-a174", Ref: "refs/remotes/origin/polecat-a174",
		Target: "refs/remotes/origin/main", Pushed: true}
	for _, c := range []Cell{CellResubmit, CellCheckByHand, CellSuggestsLanded, CellRescueUnbuilt} {
		got := RemedyPhrase(c, f, "mg-a174")
		if strings.Contains(got, "refinery submit") != c.PrintsSubmit() {
			t.Errorf("%s: submit named = %t, want %t: %s", c, !c.PrintsSubmit(), c.PrintsSubmit(), got)
		}
		if strings.HasPrefix(got, "get the branch merged") != (c == CellResubmit) {
			t.Errorf("%s: leads with the submit: %s", c, got)
		}
	}
	// A finding assembled by hand sets Unmerged, not Rescue; the table must still see it.
	f.Unmerged = []Commit{{SHA: "c2f1854cea4f", Subject: "feat: x (mg-a174)"}, {SHA: "9f1e", Subject: "RESCUE(mg-a174): y"}}
	if got := f.Cell(Presence{Added: 100, Present: 0, Measured: true}); got != CellRescueUnbuilt {
		t.Errorf("hand-assembled rescue finding: Cell = %q, want %q", got, CellRescueUnbuilt)
	}
}
