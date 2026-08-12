package reviewdecl

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/workitem"
)

// scannedStatuses are the status directories workitem.ListAllFrom walks. Named
// here so a report can state its own coverage rather than implying it saw the
// whole store — archive/ and shelved/ are outside it (D-1).
var scannedStatuses = []string{"available", "claimed", "done", "pending"}

// Source reads work items off disk and converts them into the shape Detect
// wants. It uses ListAllFrom rather than ListFrom because a claim renames the
// file to `<id>.md.<pid>`, and ListFrom's ".md" suffix filter drops every item
// in claimed/ — which is exactly where a review ticket sits while its polecat is
// running, i.e. the only moment the exemption could ever matter.
type Source struct {
	// Root is the macguffin work directory (…/work). Empty means "resolve a
	// default" — see resolveRoot, which under a test binary does NOT resolve to
	// the live store.
	Root string
}

// resolveRoot returns the macguffin WORK directory to read.
//
// It resolves the store the same way mg, the dispatch gates and
// verdictwatch.DefaultRoot do — an explicit MG_ROOT wins, then $HOME/.macguffin
// — and appends `work/`. Mirrored rather than imported so this small detector
// does not depend on verdictwatch; the resolution order is the part that must
// not diverge, and it is one `if`.
//
// MG_ROOT is not optional politeness: an operator who points mg at another store
// and gets a report about the default one has been told something false about a
// store nobody is using.
//
// Under a test binary with no MG_ROOT it returns "" rather than the developer's
// live store, which the caller surfaces as an error. This package only reads, so
// the blast radius of getting it wrong is a report rather than a corrupted store
// — but a test that quietly scanned the developer's real work items would pass
// or fail according to what the fleet happened to be doing that hour, which is
// exactly the kind of result this family exists to distrust. The package's
// TestMain pins MG_ROOT under a sandbox, so the branch is a floor under that
// envelope and not a substitute for it.
func (s Source) resolveRoot() string {
	if s.Root != "" {
		return s.Root
	}
	if root := os.Getenv("MG_ROOT"); root != "" {
		return filepath.Join(root, "work")
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin", "work")
}

// Statuses reports which status directories this source covers.
func (s Source) Statuses() []string { return append([]string(nil), scannedStatuses...) }

// Items reads the store.
//
// A store that cannot be read is an ERROR, never an empty slice. Zero items and
// an unreadable store both render as "nothing to report", and conflating them
// would let this detector go quietly blind — the precise failure mode it was
// built to catch, reproduced inside itself.
func (s Source) Items() ([]Item, error) {
	root := s.resolveRoot()
	if root == "" {
		return nil, fmt.Errorf("cannot resolve the macguffin work directory (MG_ROOT unset and no home directory)")
	}
	items, err := workitem.ListAllFrom(root, scannedStatuses...)
	if err != nil {
		return nil, fmt.Errorf("reading work items from %s: %w", root, err)
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		out = append(out, Item{
			ID:     it.ID,
			Title:  it.Title,
			Status: it.Status,
			// Stage and Reviews arrive already parsed by the SAME code the
			// done-reaper's probe uses (workitem.scanCarrierBlock, reached from
			// parseWorkItem here and from ParseCarrier there). That identity is
			// the point — see the package doc.
			Stage:             it.Stage,
			Reviews:           it.Reviews,
			CarrierUnreadable: it.CarrierUnreadable,
			Created:           it.Created,
		})
	}
	return out, nil
}
