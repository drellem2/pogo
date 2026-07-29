package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detectorPackages are the report-only watchers, plus the harness layer that
// owns the fleet usage-limit episode. Every one of them is allowed to observe
// and to mail; none of them may act.
var detectorPackages = []string{
	"claude",     // owns the usage-limit episode the policy asks about
	"deafwatch",  // "It mails and it emits. It never registers a schedule, nudges, or restarts."
	"synthwatch", //
	"ackwatch",   //
	"stallwatch", // decides WHAT to say; pogod decides how to deliver it
	"driftwatch", //
	"synthfail",  //
}

// wakePathSymbols are the names only an ACTOR would need. A detector that
// mentions one in CODE has stopped observing and started reaching. (Prose is
// exempt — see the parser note in the test.)
var wakePathSymbols = map[string]bool{
	"NudgeWake":               true,
	"SetLimitEpisodeQuery":    true,
	"LimitEpisodeQuery":       true,
	"NudgeWithModeCorrelated": true,
	"NudgeWithMode":           true,
}

// TestNoDetectorCallsIntoWakePath is the acceptance criterion for mg-8184 that
// is not about behaviour: "nothing in the detector packages gains a call into
// the nudge path."
//
// The wake-cycle policy is safe to build against report-only detectors only
// because of its DIRECTION. The nudge decision, already about to fire, asks
// "are we inside a limit episode?" and suppresses itself — a pull. The inverse
// (a detector observing state and reaching into the nudge path) is a push, and
// it is what the report-only stance exists to prevent; a suppression built that
// way would carry the same detector→actor seam as the trigger this ticket
// deliberately left unbuilt.
//
// A comment cannot hold that line, because the inversion looks like a small
// convenience at the call site. This test can: it fails the moment a detector
// package learns a nudge-path name.
//
// It parses rather than greps, so a doc comment may still NAME the seam — the
// accessor in internal/claude/usagelimit.go explains which query pogod installs
// it into, and explaining the direction is not travelling in it. Only
// identifiers in the syntax tree count.
//
// stallwatch is the interesting entry. It already nudges — but through a
// Nudger function value that pogod supplies, so the watcher names no delivery
// mechanism and pogod (the composition root, not a detector) decides that a
// stall fire is a wake. That indirection is the shape being pinned.
func TestNoDetectorCallsIntoWakePath(t *testing.T) {
	internalDir := ".." // internal/agent -> internal

	for _, pkg := range detectorPackages {
		dir := filepath.Join(internalDir, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v — if the package moved, move this guard with it", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			fset := token.NewFileSet()
			// ParserMode 0: no comments retained, which is exactly the
			// distinction this guard needs.
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || !wakePathSymbols[id.Name] {
					return true
				}
				t.Errorf("%s:%d references the nudge path (%q).\n"+
					"The wake-cycle policy is PULL: the nudge decision asks a detector, "+
					"never the other way round. If this package now needs to cause a nudge, "+
					"that is a change to the report-only stance and needs its own ticket "+
					"(see internal/agent/wakepolicy.go).",
					path, fset.Position(id.Pos()).Line, id.Name)
				return true
			})
		}
	}
}
