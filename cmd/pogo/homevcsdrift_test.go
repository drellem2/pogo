package main

// Tests for the "$POGO_HOME version control" doctor row (mg-3610).

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/homevcs"
)

// TestHomeVCSLine_NeverFails. `fail` sets doctor's exit code, and the remedy is
// a machine-local ops action in a live directory. A detector that grows into a
// gate through the exit code is still a gate.
func TestHomeVCSLine_NeverFails(t *testing.T) {
	reports := []homevcs.Report{
		{Home: "/h", Examined: 9},
		{Home: "/h", Examined: 9, Versioned: true, Toplevel: "/h"},
		{Home: "/h", Examined: 9, Versioned: true, Toplevel: "/h", Tracked: []homevcs.Finding{
			{Path: "agents/mayor.md", Rel: "agents/mayor.md", Writer: homevcs.WriterInstall, Dirty: true},
		}},
		{Home: "/h", Unknown: "git is not on PATH"},
	}
	for _, rep := range reports {
		if status, detail := homeVCSLine(rep); status == "fail" {
			t.Errorf("status = fail for %+v (%s); this detector must never set doctor's exit code", rep, detail)
		}
	}
}

// TestHomeVCSLine_UnversionedHomeSaysSo: the common case for anyone who is not
// this fleet's host. It must read as a decided all-clear, and must still say
// how many paths it looked for — a bare "ok" cannot be told from a check that
// examined nothing.
func TestHomeVCSLine_UnversionedHomeSaysSo(t *testing.T) {
	status, detail := homeVCSLine(homevcs.Report{Home: "/Users/x/.pogo", Examined: 9})
	if status != "pass" {
		t.Errorf("status = %q, want pass for a home under no repository", status)
	}
	if !strings.Contains(detail, "not inside a git work tree") || !strings.Contains(detail, "9") {
		t.Errorf("detail = %q, want the verdict and the population examined", detail)
	}
}

// TestHomeVCSLine_TrackedInstallOutputWarnsWithTheProhibition is the ticket.
// The prohibition travels IN the finding, because the obvious way to make a
// dirty tree clean is to commit it — which records one machine's install output
// as shared truth and re-dirties on the next install.
func TestHomeVCSLine_TrackedInstallOutputWarnsWithTheProhibition(t *testing.T) {
	rep := homevcs.Report{
		Home:      "/Users/x/.pogo",
		Versioned: true,
		Toplevel:  "/Users/x/.pogo",
		Remote:    "https://github.com/drellem2/pogo-config.git",
		Examined:  9,
		Tracked: []homevcs.Finding{
			{Path: "agents/mayor.md", Rel: "agents/mayor.md", Writer: homevcs.WriterInstall, Dirty: true},
			{Path: "projects.json", Rel: "projects.json", Writer: homevcs.WriterDaemon},
		},
	}
	status, detail := homeVCSLine(rep)
	if status != "warn" {
		t.Fatalf("status = %q, want warn; detail = %q", status, detail)
	}
	for _, want := range []string{
		"TRACKS",
		"agents/mayor.md",
		"MODIFIED",
		"projects.json",
		"pogo-config",
		"Do NOT resolve this by committing them",
		"docs/pogo-home-version-control.md",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q\ndetail = %q", want, detail)
		}
	}
}

// TestHomeVCSLine_CleanVersionedHomeIsNotAWarning: a repo in $POGO_HOME is not
// itself the defect. Versioning `agents/crew/*.md` — prompts with no upstream
// anywhere — is the job that repo should keep. Warning on its mere existence
// would train readers to skip the row.
func TestHomeVCSLine_CleanVersionedHomeIsNotAWarning(t *testing.T) {
	status, detail := homeVCSLine(homevcs.Report{
		Home: "/Users/x/.pogo", Versioned: true, Toplevel: "/Users/x/.pogo",
		Remote: "https://github.com/drellem2/pogo-config.git", Examined: 9,
	})
	if status != "pass" {
		t.Errorf("status = %q, want pass when the repo tracks nothing pogo writes; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "tracks none of the 9") {
		t.Errorf("detail = %q, want it to name what was checked and found absent", detail)
	}
}

// TestHomeVCSLine_UndecidableIsNotPass. "Could not look" reported as "nothing
// found" is the failure mode this package exists to argue against.
func TestHomeVCSLine_UndecidableIsNotPass(t *testing.T) {
	status, detail := homeVCSLine(homevcs.Report{Home: "/h", Unknown: "git is not on PATH, so nothing checked"})
	if status != "warn" {
		t.Errorf("status = %q, want warn when the audit decided nothing", status)
	}
	if !strings.HasPrefix(detail, "NOT CHECKED: ") {
		t.Errorf("detail = %q, want the NOT CHECKED disclaimer to lead", detail)
	}
}

// TestHomeVCSLine_ListIsCapped keeps a 16-file repo from turning one checklist
// row into a wall.
func TestHomeVCSLine_ListIsCapped(t *testing.T) {
	rep := homevcs.Report{Home: "/h", Versioned: true, Toplevel: "/h", Examined: 40}
	for i := 0; i < homeVCSMaxListed+5; i++ {
		rep.Tracked = append(rep.Tracked, homevcs.Finding{
			Path: "agents/f" + string(rune('a'+i)) + ".md", Writer: homevcs.WriterInstall,
		})
	}
	_, detail := homeVCSLine(rep)
	if !strings.Contains(detail, "(+5 more)") {
		t.Errorf("detail = %q, want the elided remainder counted", detail)
	}
}

// TestDoctorCheck_HomeVCSRowAlwaysRenders runs the real binary. A row that
// exists only in a unit test is a row that can be dropped from the checklist
// without a single failure.
func TestDoctorCheck_HomeVCSRowAlwaysRenders(t *testing.T) {
	home := t.TempDir()
	line, ok := doctorChecks(t, []string{"POGO_HOME=" + home})[homeVCSCheckName]
	if !ok {
		t.Fatalf("no %q row in doctor --check; the detector must be visible even when it finds nothing", homeVCSCheckName)
	}
	if !strings.HasPrefix(line, "pass\t") {
		t.Errorf("row = %q; a temp home under no repository is a clean answer", line)
	}
	if !strings.Contains(line, home) {
		t.Errorf("row = %q, want it to name the home it examined (%s)", line, home)
	}
}
