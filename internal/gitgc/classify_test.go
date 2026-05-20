package gitgc

import "testing"

func TestStateFromStatus(t *testing.T) {
	cases := map[string]TicketState{
		"done":      TicketDone,
		"archived":  TicketArchived,
		"available": TicketInFlight,
		"claimed":   TicketInFlight,
		"pending":   TicketInFlight,
		"shelved":   TicketUnknown,
		"":          TicketUnknown,
		"bogus":     TicketUnknown,
	}
	for status, want := range cases {
		if got := stateFromStatus(status); got != want {
			t.Errorf("stateFromStatus(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestConcluded(t *testing.T) {
	for _, s := range []TicketState{TicketUnknown, TicketInFlight} {
		if s.Concluded() {
			t.Errorf("%v.Concluded() = true, want false", s)
		}
	}
	for _, s := range []TicketState{TicketDone, TicketArchived} {
		if !s.Concluded() {
			t.Errorf("%v.Concluded() = false, want true", s)
		}
	}
}

func TestBranchSuffix(t *testing.T) {
	cases := map[string]string{
		"polecat-30d5":        "30d5",
		"polecat-mg-9cdc":     "mg-9cdc",
		"polecat-cat-mg-a1d8": "cat-mg-a1d8",
		"main":                "",
		"feature/x":           "",
		"polecat-":            "",
	}
	for in, want := range cases {
		if got := BranchSuffix(in); got != want {
			t.Errorf("BranchSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTicketIndex(t *testing.T) {
	ndjson := []byte(`{"id":"mg-1111","status":"archived"}
{"id":"mg-2222","status":"claimed"}

{"id":"mg-3333","status":"done"}
not json
{"status":"missing-id"}
{"id":"mg-4444","status":"shelved"}
`)
	idx := parseTicketIndex(ndjson)
	want := TicketIndex{
		"mg-1111": TicketArchived,
		"mg-2222": TicketInFlight,
		"mg-3333": TicketDone,
		"mg-4444": TicketUnknown,
	}
	if len(idx) != len(want) {
		t.Fatalf("parsed %d items, want %d: %v", len(idx), len(want), idx)
	}
	for id, st := range want {
		if idx[id] != st {
			t.Errorf("idx[%q] = %v, want %v", id, idx[id], st)
		}
	}
}

// TestBranchStateResolution exercises the candidate-ID heuristic against
// the real spread of polecat branch naming conventions found in the wild.
func TestBranchStateResolution(t *testing.T) {
	idx := TicketIndex{
		"mg-30d5": TicketInFlight, // bare-id branch
		"mg-9cdc": TicketArchived, // polecat-mg-<id> branch
		"mg-a1d8": TicketArchived, // polecat-cat-mg-<id> branch
		"mg-486f": TicketArchived, // polecat-pc-<id> branch
		"mg-3963": TicketDone,     // retry-suffixed branch
		"mg-30eb": TicketArchived, // prefixed + suffixed branch
		"mg-06cb": TicketArchived, // glued single-letter prefix
		"mg-283e": TicketArchived, // glued retry prefix
		"mg-55d1": TicketArchived, // pc-<id> with trailing retry letter
	}
	cases := []struct {
		branch   string
		wantID   string
		wantStat TicketState
	}{
		{"polecat-30d5", "mg-30d5", TicketInFlight},
		{"polecat-mg-9cdc", "mg-9cdc", TicketArchived},
		{"polecat-cat-mg-a1d8", "mg-a1d8", TicketArchived},
		{"polecat-pc-486f", "mg-486f", TicketArchived},
		{"polecat-3963-r", "mg-3963", TicketDone},
		{"polecat-gt-30eb-fix", "mg-30eb", TicketArchived},
		{"polecat-p06cb", "mg-06cb", TicketArchived},
		{"polecat-r283e", "mg-283e", TicketArchived},
		{"polecat-pc-55d1r", "mg-55d1", TicketArchived},
		{"polecat-cloud-mode", "", TicketUnknown}, // no resolvable ticket
		{"polecat-install", "", TicketUnknown},
		{"main", "", TicketUnknown},
	}
	for _, c := range cases {
		id, st := idx.BranchState(c.branch)
		if id != c.wantID || st != c.wantStat {
			t.Errorf("BranchState(%q) = (%q, %v), want (%q, %v)",
				c.branch, id, st, c.wantID, c.wantStat)
		}
	}
}

// TestBranchStateUnknownIsSafe confirms that a branch whose hex code does
// not exist in the index resolves to unknown rather than mis-binding.
func TestBranchStateUnknownIsSafe(t *testing.T) {
	idx := TicketIndex{"mg-aaaa": TicketArchived}
	if id, st := idx.BranchState("polecat-bbbb"); st != TicketUnknown || id != "" {
		t.Errorf("BranchState(polecat-bbbb) = (%q, %v), want (\"\", unknown)", id, st)
	}
}
