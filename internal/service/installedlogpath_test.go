package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeDaemonPlist(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, launchdLabel+".plist"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func daemonPlistWith(pairs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>` + launchdLabel + `</string>
`)
	for i := 0; i < len(pairs); i += 2 {
		b.WriteString("    <key>" + pairs[i] + "</key>\n    <string>" + pairs[i+1] + "</string>\n")
	}
	b.WriteString("  </dict>\n</plist>\n")
	return b.String()
}

// TestInstalledLogPathReadsThePlistAndNotTheTemplate. The path a diagnostician
// greps is the one the LOADED job names — mayor.md tells them to derive it that
// way for exactly this reason (mg-7537). A plist written by an older build, or
// edited by hand, can name a different file, and a checker that judged the
// template's path instead would be reporting on a file nobody writes.
func TestInstalledLogPathReadsThePlistAndNotTheTemplate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgents exist only on macOS")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, "somewhere", "else.log")
	writeDaemonPlist(t, home, daemonPlistWith("StandardOutPath", want, "StandardErrorPath", want))

	got, ok := InstalledLogPath()
	if !ok {
		t.Fatal("an installed plist naming a log reads as no reading")
	}
	if got != want {
		t.Errorf("InstalledLogPath() = %q, want %q", got, want)
	}
	if got == PogodLogPath() {
		t.Error("returned the template's path — the plist on disk was not consulted")
	}
}

// TestInstalledLogPathPrefersStderr. The consumer compares against a process's
// fd 2. The template points both keys at one file so they agree today; when
// they do not, fd 2 is the one that answers the question being asked.
func TestInstalledLogPathPrefersStderr(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgents exist only on macOS")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeDaemonPlist(t, home, daemonPlistWith("StandardOutPath", "/out.log", "StandardErrorPath", "/err.log"))

	if got, _ := InstalledLogPath(); got != "/err.log" {
		t.Errorf("InstalledLogPath() = %q, want /err.log", got)
	}
}

// TestInstalledLogPathFallsBackToStdout. A hand-written or older plist may
// declare only StandardOutPath; that is still the file someone will grep.
func TestInstalledLogPathFallsBackToStdout(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgents exist only on macOS")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeDaemonPlist(t, home, daemonPlistWith("StandardOutPath", "/out.log"))

	if got, ok := InstalledLogPath(); !ok || got != "/out.log" {
		t.Errorf("InstalledLogPath() = %q, %v; want /out.log, true", got, ok)
	}
}

// TestInstalledLogPathIsNotAReadingWhenThereIsNothingToRead. Every one of these
// is "could not take the reading", not "the path is empty" — and a caller must
// not substitute a default for a false ok, because that is how a checker starts
// judging a file nobody writes to.
func TestInstalledLogPathIsNotAReadingWhenThereIsNothingToRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgents exist only on macOS")
	}
	for _, tc := range []struct {
		name  string
		plist string // "" means no plist at all
	}{
		{"no installed job", ""},
		{"job declares no redirect", daemonPlistWith("ProgramArguments", "/usr/bin/true")},
		{"unparseable plist", "not xml at all"},
		{"empty redirect", daemonPlistWith("StandardErrorPath", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if tc.plist != "" {
				writeDaemonPlist(t, home, tc.plist)
			}
			if got, ok := InstalledLogPath(); ok {
				t.Errorf("InstalledLogPath() = %q, true — want no reading", got)
			}
		})
	}
}

// TestPogodLogPathForReportSaysWhichClaimItIsMaking. "What the job names" and
// "what this build would install" are different claims and have already been
// confused on this box; a report that prints one as the other is the defect the
// caller exists to catch, one level up.
func TestPogodLogPathForReportSaysWhichClaimItIsMaking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := PogodLogPathForReport()
	if !strings.Contains(got, "no installed job") {
		t.Errorf("with no installed job the report reads %q — it presents a default as the job's path", got)
	}

	if runtime.GOOS != "darwin" {
		return
	}
	want := filepath.Join(home, "real.log")
	writeDaemonPlist(t, home, daemonPlistWith("StandardErrorPath", want))
	if got := PogodLogPathForReport(); got != want {
		t.Errorf("with an installed job the report reads %q, want the bare path %q", got, want)
	}
}
