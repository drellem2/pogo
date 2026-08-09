package config

import "testing"

// TestIsStageGated pins the predicate both enforcement points share (mg-69b1):
// internal/agent's spawn handler refuses on it, internal/stallwatch stops
// nudging on it. One function, two callers, no second copy — the same shape
// mg-4798 imposed on the assignee gate, because a rule with two implementations
// has already begun to drift.
func TestIsStageGated(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		want  bool
	}{
		{"the gate itself", "gated", true},
		{"hand-edited capitalisation", "Gated", true},
		{"whitespace, as a body line may carry", "  gated ", true},

		// Every other stage in the vocabulary. Each is dispatchable ON PURPOSE
		// and the reason differs per stage — see the doc comment. A change that
		// gates one of these breaks the gh-issue track at that stage.
		{"triage has not happened yet", "triage", false},
		{"build is the worker's own stage", "build", false},
		{"review must be dispatchable while it reads review", "review", false},
		{"merge", "merge", false},

		// The ordinary case: nearly every work item in the store.
		{"no carrier block at all", "", false},
		{"a stage nobody defined", "gating", false},
		{"a word that merely contains it", "delegated", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStageGated(tt.stage); got != tt.want {
				t.Errorf("IsStageGated(%q) = %v, want %v", tt.stage, got, tt.want)
			}
		})
	}
}

// The gate value and the vocabulary the prompt teaches must be the same string.
// This is the pin that fails if someone renames the constant without touching
// mayor.md, or vice versa — the failure mode being a gate that is armed on a
// stage nothing ever writes, which is indistinguishable from no gate at all.
func TestGatedStageIsTheStageThePlaybookWrites(t *testing.T) {
	if GatedStage != "gated" {
		t.Fatalf("GatedStage = %q; the gh-issue playbook writes `stage: gated` and "+
			"a gate armed on any other word is silently off", GatedStage)
	}
	if !IsStageGated(GatedStage) {
		t.Fatal("IsStageGated does not gate its own constant")
	}
}
