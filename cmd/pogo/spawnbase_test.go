package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestSpawnBaseLine pins what the dispatcher reads after a spawn: the base the
// worktree got and where its target came from (drellem2/pogo#176).
func TestSpawnBaseLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		base *agent.SpawnBase
		want []string
	}{
		{"from item", &agent.SpawnBase{Target: "integ", TargetFrom: agent.TargetFromWorkItem, BaseRef: "origin/integ"},
			[]string{"origin/integ", "target integ", "mg-1", "branch: field"}},
		{"from request", &agent.SpawnBase{Target: "integ", TargetFrom: agent.TargetFromRequest, BaseRef: "origin/integ"},
			[]string{"origin/integ", "target integ"}},
		{"default", &agent.SpawnBase{BaseRef: "origin/main"}, []string{"origin/main", "repo default"}},
		{"no origin", &agent.SpawnBase{Target: "integ", TargetFrom: agent.TargetFromRequest}, []string{"local HEAD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spawnBaseLine(tc.base, "mg-1")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("spawnBaseLine = %q, want it to mention %q", got, w)
				}
			}
		})
	}
	if got := spawnBaseLine(nil, "mg-1"); got != "" {
		t.Errorf("nil base (an old pogod) = %q, want no line", got)
	}
	if got := spawnBaseLine(&agent.SpawnBase{}, "mg-1"); got != "" {
		t.Errorf("no worktree, no target = %q, want no line", got)
	}
}
