package config

import "testing"

// TestIsDispatchGated pins the shared predicate that both stall-watch and the
// spawn handler now read the gate vocabulary through (mg-4798). It is the single
// implementation of the rule, so its edges are the rule's edges.
func TestIsDispatchGated(t *testing.T) {
	tests := []struct {
		name     string
		assignee string
		gates    []string
		want     bool
	}{
		// The two defaults, and the reason the function exists.
		{"human gates", "human", nil, true},
		{"parked gates", "parked", nil, true},

		// Unassigned is NOT gated. This is the case the fleet runs on: if it
		// gated, dispatch would refuse every ordinary item.
		{"unassigned is dispatchable", "", nil, false},

		// An owner is not a gate. mg-4bd4's defect was treating "named
		// assignee" as "do not touch"; ownership and executability are
		// different questions.
		{"an owning agent is dispatchable", "pm-pogo", nil, false},
		{"the coordinator is dispatchable", "mayor", nil, false},

		// Case and whitespace, so a hand-edited frontmatter value still gates.
		{"uppercase gates", "Human", nil, true},
		{"padded gates", "  human  ", nil, true},
		{"mixed case parked gates", "PaRkEd", nil, true},

		// Empty gates must fall back to the defaults, not to "nothing is
		// gated" — an unwired caller has to get the enforcing behaviour.
		{"empty gates falls back to defaults", "human", []string{}, true},

		// A configured vocabulary replaces the default rather than extending
		// it, matching how config.go applies non_dispatchable_assignees.
		{"custom gate gates", "legal-review", []string{"human", "legal-review"}, true},
		{"value dropped from custom vocabulary no longer gates", "parked", []string{"human", "legal-review"}, false},
		{"custom gate is case-insensitive on both sides", "LEGAL-REVIEW", []string{" Legal-Review "}, true},

		// A gate value must match whole, not as a substring — "human" must not
		// gate an agent called "human-review-bot".
		{"substring does not gate", "human-review-bot", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDispatchGated(tt.assignee, tt.gates); got != tt.want {
				t.Errorf("IsDispatchGated(%q, %v) = %v, want %v",
					tt.assignee, tt.gates, got, tt.want)
			}
		})
	}
}

// TestIsDispatchGatedCoversEveryDefault guards against a default being added to
// the vocabulary without the predicate honouring it — the list and the function
// that reads it live in the same file precisely so they cannot drift, and this
// asserts that.
func TestIsDispatchGatedCoversEveryDefault(t *testing.T) {
	if len(DefaultNonDispatchableAssignees) == 0 {
		t.Fatal("DefaultNonDispatchableAssignees is empty; the gate would never fire")
	}
	for _, g := range DefaultNonDispatchableAssignees {
		if !IsDispatchGated(g, nil) {
			t.Errorf("default gate %q does not gate", g)
		}
	}
}
