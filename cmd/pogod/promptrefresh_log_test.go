package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestPromptRefreshLogLines_ConflictOnlyIsLoud is the regression this ticket
// exists for (mg-f86c): a boot whose ONLY outcome is a declined sync used to
// satisfy neither arm of `else if len(Updated)>0 || len(Installed)>0` and so
// logged NOTHING AT ALL — total silence for total failure. That silence is the
// mechanism by which a stale mayor.md went unnoticed until it misrouted a
// ticket. A conflict-only refresh must now log loudly, by name, with a remedy.
func TestPromptRefreshLogLines_ConflictOnlyIsLoud(t *testing.T) {
	res := &agent.InstallResult{
		Skipped: []string{"crew/pm-a.md", "crew/pm-b.md"}, // 7 skipped in the wild; count doesn't matter
		Conflicts: []agent.PromptConflict{
			{Path: "mayor.md", DistPath: "mayor.md.dist"},
		},
	}
	lines := promptRefreshLogLines(res)
	if len(lines) == 0 {
		t.Fatal("conflict-only refresh logged NOTHING — this is exactly the silent-decline bug mg-f86c fixes")
	}
	joined := strings.Join(lines, "\n")
	// The count line must exist and report the conflict.
	if !strings.Contains(joined, "conflicts=1") {
		t.Errorf("summary line must report conflicts=1; got:\n%s", joined)
	}
	// The declined file must be named loudly with its .dist and a remedy.
	assertConflictLoud(t, joined, "mayor.md", "mayor.md.dist")
}

// TestPromptRefreshLogLines_ConflictAmongUpdates covers the OTHER path: a boot
// that genuinely updated some prompts while declining one. The old code logged
// a reassuring success that structurally could not mention the decline. The
// conflict must appear in the count AND get its own loud line.
func TestPromptRefreshLogLines_ConflictAmongUpdates(t *testing.T) {
	res := &agent.InstallResult{
		Installed: []string{"crew/pm-new.md"},
		Updated:   []string{"architect.md"},
		Skipped:   []string{"crew/pm-a.md"},
		Conflicts: []agent.PromptConflict{
			{Path: "mayor.md", DistPath: "mayor.md.dist"},
		},
	}
	lines := promptRefreshLogLines(res)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "installed=1 updated=1 skipped=1 conflicts=1") {
		t.Errorf("summary must carry all four counts including conflicts; got:\n%s", joined)
	}
	assertConflictLoud(t, joined, "mayor.md", "mayor.md.dist")
}

// TestPromptRefreshLogLines_HappyPathUnchanged: an ordinary refresh with no
// conflicts still logs its one success line and nothing alarming.
func TestPromptRefreshLogLines_HappyPathUnchanged(t *testing.T) {
	res := &agent.InstallResult{
		Updated: []string{"architect.md"},
		Skipped: []string{"crew/pm-a.md", "crew/pm-b.md"},
	}
	lines := promptRefreshLogLines(res)
	if len(lines) != 1 {
		t.Fatalf("clean refresh should log exactly one line; got %d:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "conflicts=0") {
		t.Errorf("count line should still report conflicts=0; got: %s", lines[0])
	}
	if strings.Contains(lines[0], "DECLINED") {
		t.Errorf("clean refresh must not shout DECLINED; got: %s", lines[0])
	}
}

// TestPromptRefreshLogLines_NoOpIsSilent: the common boot where hash stamps
// make everything a skip (nothing installed, updated, or declined) stays quiet
// so the log isn't spammed on every restart.
func TestPromptRefreshLogLines_NoOpIsSilent(t *testing.T) {
	res := &agent.InstallResult{
		Skipped: []string{"mayor.md", "architect.md", "crew/pm-a.md"},
	}
	if lines := promptRefreshLogLines(res); lines != nil {
		t.Errorf("all-skipped refresh should be silent; got:\n%s", strings.Join(lines, "\n"))
	}
}

// assertConflictLoud checks that the rendered lines name the declined file, its
// .dist sidecar, and an actionable reconcile remedy.
func assertConflictLoud(t *testing.T, joined, path, distPath string) {
	t.Helper()
	var loud string
	for _, l := range strings.Split(joined, "\n") {
		if strings.Contains(l, "DECLINED") {
			loud = l
			break
		}
	}
	if loud == "" {
		t.Fatalf("no loud DECLINED line found; got:\n%s", joined)
	}
	for _, want := range []string{path, distPath, "Reconcile"} {
		if !strings.Contains(loud, want) {
			t.Errorf("loud line missing %q; got: %s", want, loud)
		}
	}
}
