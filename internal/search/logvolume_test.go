package search

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/drellem2/pogo/pkg/plugin"
)

// quiesceForLogs waits for every in-flight index pass to finish touching disk
// before a test reads the capture, so no record can land after the read.
func quiesceForLogs(t *testing.T, bs *BasicSearch) {
	t.Helper()
	if !bs.Quiesce(30 * time.Second) {
		t.Fatalf("indexing still in flight 30s after the pass; log capture would be incomplete")
	}
}

// TestNoOpReindexIsSilentAtInfo is the acceptance criterion for gh#111. Over
// 11 measured days, three lines — "Reindexing", "Indexed N files for" and
// "No content changes detected" — were 45,755 of 46,298 hclog lines (98.8%)
// in pogod.log, emitted once per project per tick whether or not the pass had
// anything to do. A pass that changes nothing must now say nothing at the
// default level, while still saying all of it at Debug for anyone who asks.
func TestNoOpReindexIsSilentAtInfo(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	if changed := waitIndexed(t, events, root); !changed {
		t.Fatalf("initial index must report changed content")
	}
	quiesceForLogs(t, bs)

	// The pass that actually built the index is still audible at Info. This
	// half is the point of splitting rather than demoting: a daemon that has
	// stopped indexing must not be indistinguishable from an idle one.
	requireLogged(t, capture.records(t), "info", msgIndexed(1, root), 1)

	capture.reset()
	bs.ReIndex(root)
	if changed := waitIndexed(t, events, root); changed {
		t.Fatalf("re-index with no file changes must report unchanged; fixture is not a no-op pass")
	}
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	for _, rec := range recs {
		if recLevel(rec) == "info" {
			t.Errorf("a no-op re-index logged at info: %q\nfull output:\n%s",
				recMessage(rec), formatRecords(recs))
		}
	}

	// Every fact the demoted lines carried is still there one level down.
	requireLogged(t, recs, "debug", "Reindexing", 1)
	requireLogged(t, recs, "debug", msgIndexed(1, root), 1)
	requireLogged(t, recs, "debug", msgNoContentChanges(root), 1)
}

// TestReindexWithRealChangesStaysAtInfo is the other half of the split: the
// "Indexed N files" line is demoted by whether the pass did work, never
// unconditionally. Run against a flat demotion of all three lines, this fails.
func TestReindexWithRealChangesStaysAtInfo(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)
	capture.reset()

	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta tokentwo\n"), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	bs.ReIndex(root)
	if changed := waitIndexed(t, events, root); !changed {
		t.Fatalf("re-index after a content change must report changed content")
	}
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	requireLogged(t, recs, "info", msgIndexed(2, root), 1)
	// The pass rebuilt the index, so the skip line must not appear at all.
	requireLogged(t, recs, "", msgNoContentChanges(root), 0)
}

// TestDefaultLevelHidesTheNoOpNarration checks the same claim through the
// level filter an operator actually runs with, rather than through a Trace
// logger the daemon never uses. A no-op pass must produce no output at all at
// the default level.
func TestDefaultLevelHidesTheNoOpNarration(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})
	capture := captureLogs(t, bs, hclog.Info)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)
	capture.reset()

	bs.ReIndex(root)
	if changed := waitIndexed(t, events, root); changed {
		t.Fatalf("re-index with no file changes must report unchanged")
	}
	quiesceForLogs(t, bs)

	if recs := capture.records(t); len(recs) != 0 {
		t.Errorf("a no-op re-index wrote %d line(s) at the default level:\n%s",
			len(recs), formatRecords(recs))
	}
}

// TestReindexingLineCarriesRootAsAField pins the hclog varargs fix. The call
// was `Info("Reindexing ", path)` — msg plus a single trailing value, which
// hclog cannot pair with a key, so it emitted
// {"@message":"Reindexing ","EXTRA_VALUE_AT_END":"<path>"}. The path, the only
// useful thing on the line, was neither in the message nor in a queryable
// field.
func TestReindexingLineCarriesRootAsAField(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)
	capture.reset()

	bs.ReIndex(root)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	matches := findLogged(recs, "debug", "Reindexing")
	if len(matches) != 1 {
		t.Fatalf("want exactly one Reindexing record, got %d:\n%s", len(matches), formatRecords(recs))
	}
	rec := matches[0]
	if got := recMessage(rec); got != "Reindexing" {
		t.Errorf("message = %q, want %q (the trailing space belonged to the old concatenation)", got, "Reindexing")
	}
	if _, bad := rec["EXTRA_VALUE_AT_END"]; bad {
		t.Errorf("hclog still reports an unpaired trailing argument: %s", formatRecords([]map[string]any{rec}))
	}
	if got, _ := rec["root"].(string); got != root {
		t.Errorf("root field = %q, want %q", got, root)
	}
}
