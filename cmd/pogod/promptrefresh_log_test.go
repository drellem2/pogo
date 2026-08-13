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
	lines := promptRefreshLogLines(res, "0123456789abcdef0123456789abcdef01234567")
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
	lines := promptRefreshLogLines(res, "0123456789abcdef0123456789abcdef01234567")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "installed=1 updated=1 skipped=1 conflicts=1") {
		t.Errorf("summary must carry all four counts including conflicts; got:\n%s", joined)
	}
	assertConflictLoud(t, joined, "mayor.md", "mayor.md.dist")
}

// TestPromptRefreshLogLines_HappyPathUnchanged: an ordinary refresh with no
// conflicts still logs its success line and nothing alarming.
//
// This used to assert "exactly one line", which was a stand-in for "does not
// shout DECLINED" and became wrong when mg-b6bd added the names line. The
// no-DECLINED assertion is now made directly, over the whole report, which is
// what the case was ever about.
func TestPromptRefreshLogLines_HappyPathUnchanged(t *testing.T) {
	res := &agent.InstallResult{
		Updated: []string{"architect.md"},
		Skipped: []string{"crew/pm-a.md", "crew/pm-b.md"},
	}
	lines := promptRefreshLogLines(res, "0123456789abcdef0123456789abcdef01234567")
	if len(lines) == 0 {
		t.Fatal("clean refresh with an update must log something")
	}
	if !strings.Contains(lines[0], "conflicts=0") {
		t.Errorf("count line should still report conflicts=0; got: %s", lines[0])
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "DECLINED") {
		t.Errorf("clean refresh must not shout DECLINED; got:\n%s", joined)
	}
}

// TestPromptRefreshLogLines_NamesTheFilesAndTheRevision is mg-b6bd's regression.
//
// The line this box actually logged on 2026-08-13 was
//
//	pogod: refreshed prompts (installed=0 updated=9 skipped=0 conflicts=0)
//
// and it is the reason a reader concluded prompts were installed "at some
// unrecorded point, by nobody in particular". The install had in fact just run,
// nightly, on schedule. What the reader could not get from that line — and
// could not get from anywhere else — was WHICH nine and FROM WHAT. Both must be
// on the report, or the install stays unobservable no matter how reliably it
// runs.
func TestPromptRefreshLogLines_NamesTheFilesAndTheRevision(t *testing.T) {
	res := &agent.InstallResult{
		Installed: []string{"crew/pm-new.md"},
		Updated:   []string{"crew/doctor.md", "mayor.md"},
		Skipped:   []string{"crew/pm-a.md"},
	}
	joined := strings.Join(promptRefreshLogLines(res, "d27ecc1abcdef0123456789abcdef0123456789a"), "\n")

	// The revision. A report of what changed that cannot say what it changed
	// TO is the counts line with extra words.
	if !strings.Contains(joined, "d27ecc1abcde") {
		t.Errorf("report must name the revision the prompts came from; got:\n%s", joined)
	}
	// Every name, in both categories. Not a sample, not a head -3.
	for _, want := range []string{"crew/pm-new.md", "crew/doctor.md", "mayor.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report must name %q — the counts-only line is the defect; got:\n%s", want, joined)
		}
	}
	// A skipped file is not news and must not be listed: padding the report
	// with the files that did NOT change is how the ones that did get skimmed.
	if strings.Contains(joined, "crew/pm-a.md") {
		t.Errorf("skipped files must not be listed by name; got:\n%s", joined)
	}
	// The reader's next act is a restart, and the report should say so —
	// installing a prompt under a running agent changes nothing until it
	// re-reads (act 4).
	if !strings.Contains(joined, "restart") {
		t.Errorf("the UPDATED line must point at the restart that makes it take effect; got:\n%s", joined)
	}
}

// TestPromptRefreshLogLines_NoOpIsSilent: the common boot where hash stamps
// make everything a skip (nothing installed, updated, or declined) stays quiet
// so the log isn't spammed on every restart.
func TestPromptRefreshLogLines_NoOpIsSilent(t *testing.T) {
	res := &agent.InstallResult{
		Skipped: []string{"mayor.md", "architect.md", "crew/pm-a.md"},
	}
	if lines := promptRefreshLogLines(res, "0123456789abcdef0123456789abcdef01234567"); lines != nil {
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
