package agent

import (
	"path/filepath"
	"testing"
)

// TestGetByWorkItemOrName locks the gh #48 resolution contract: an agent
// registered under its bare id resolves both from that bare id (Name) and from
// the full work-item id (WorkItemID) an authored MR carries, while a plain Get
// only matches the registry key.
func TestGetByWorkItemOrName(t *testing.T) {
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	bare := &Agent{Name: "1234", WorkItemID: "mg-1234", Type: TypePolecat}
	noItem := &Agent{Name: "mayor", Type: TypeCrew}
	reg.agents["1234"] = bare
	reg.agents["mayor"] = noItem

	tests := []struct {
		name string
		id   string
		want *Agent
	}{
		{"bare name (registry key)", "1234", bare},
		{"full work-item id", "mg-1234", bare},
		{"name with no work-item id", "mayor", noItem},
		{"empty id", "", nil},
		{"unknown id", "mg-gone", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.GetByWorkItemOrName(tt.id); got != tt.want {
				t.Errorf("GetByWorkItemOrName(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}

	// The bug: a plain Get keyed on the full work-item id misses.
	if got := reg.Get("mg-1234"); got != nil {
		t.Errorf("Get(mg-1234) should miss (registry key is bare), got %v", got)
	}
}
