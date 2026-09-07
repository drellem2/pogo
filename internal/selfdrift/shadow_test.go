package selfdrift

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestShadowVerdict is the classifier table. The two BENIGN rows matter as much
// as the hazards: a check that can only say "hazard" would report the mg-015f
// remedy — /usr/local/bin/mg as a symlink to ~/go/bin/mg — as a finding, and an
// instrument that fires on its own fix gets turned off.
func TestShadowVerdict(t *testing.T) {
	const winner = "/gobin/pogod"
	cases := []struct {
		name       string
		rev        string
		sameFile   bool
		wantBenign bool
		wantIn     string
	}{
		{"symlink to the winner is the fix, not a finding", "", true, true, "same file"},
		{"a separate file at the same revision is harmless", revMain, false, true, "same revision"},
		{"a different build is the hazard", revOld, false, false, "SHADOWED STALE COPY"},
		{"no vcs stamp stays a hazard — unknown is not clean", RevUnstamped, false, false, "provenance UNKNOWN"},
		{"an empty revision is not silently treated as a match", "", false, false, "provenance UNKNOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note, benign := shadowVerdict(winner, revMain, tc.rev, tc.sameFile)
			if benign != tc.wantBenign {
				t.Errorf("benign = %v, want %v (note: %s)", benign, tc.wantBenign, note)
			}
			if !strings.Contains(note, tc.wantIn) {
				t.Errorf("note %q does not mention %q", note, tc.wantIn)
			}
			if !strings.Contains(note, winner) {
				t.Errorf("note %q never names the copy that wins — a shadow report that does not say what it lost to cannot be acted on", note)
			}
		})
	}
}

// TestPathCopiesOrderAndFiltering exercises the walk against a real $PATH: the
// winner is whichever directory comes first, a non-executable file of the same
// name is not a copy, and a repeated directory is not a second finding.
func TestPathCopiesOrderAndFiltering(t *testing.T) {
	first, second, third := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(dir string, mode os.FileMode) string {
		p := filepath.Join(dir, "pogod")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}
	winner := write(first, 0755)
	loser := write(second, 0755)
	write(third, 0644) // present but not executable: not a copy

	// The empty entry is the shell's "current directory". It is skipped rather
	// than resolved, so the answer does not depend on where the caller stood.
	t.Setenv("PATH", strings.Join([]string{first, "", second, third, first}, string(os.PathListSeparator)))

	got := pathCopies("pogod")
	want := []string{winner, loser}
	if len(got) != len(want) {
		t.Fatalf("pathCopies = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pathCopies[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestHostShadowsSymlinkIsBenign is the end-to-end shape of the mg-dabf fix:
// two $PATH entries for the same name, the second a symlink to the first. The
// scan must see the second entry and must NOT call it a hazard — that is the
// difference between "we looked and it is fine" and "we did not look".
func TestHostShadowsSymlinkIsBenign(t *testing.T) {
	gobin, localbin := t.TempDir(), t.TempDir()
	real := filepath.Join(gobin, "pogod")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(localbin, "pogod")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("PATH", gobin+string(os.PathListSeparator)+localbin)

	shadows := HostShadows()
	if len(shadows) != 1 {
		t.Fatalf("HostShadows() = %+v, want exactly the one symlinked copy", shadows)
	}
	s := shadows[0]
	if !s.Benign {
		t.Errorf("the symlink remedy is reported as a hazard: %+v", s)
	}
	if s.Path != link || s.Winner != real {
		t.Errorf("path/winner = %s / %s, want %s / %s", s.Path, s.Winner, link, real)
	}
	if s.Revision != "" {
		t.Errorf("revision = %q; a symlink to the winner is the same file, so there is no second revision to report", s.Revision)
	}
}

// TestHostShadowsSeparateFileIsAHazard is the pre-fix state of the same box: a
// real second file, not a link. Both files here are unstamped, which is the
// weaker of the two hazard branches and the one a scan is most tempted to
// shrug at.
func TestHostShadowsSeparateFileIsAHazard(t *testing.T) {
	gobin, localbin := t.TempDir(), t.TempDir()
	for _, dir := range []string{gobin, localbin} {
		if err := os.WriteFile(filepath.Join(dir, "pose"), []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Setenv("PATH", gobin+string(os.PathListSeparator)+localbin)

	shadows := HostShadows()
	if len(shadows) != 1 || shadows[0].Benign {
		t.Fatalf("HostShadows() = %+v, want one non-benign shadow", shadows)
	}
	if shadows[0].Name != "pose" {
		t.Errorf("name = %q, want pose — lsp and pose are exactly the names DeployedCmds does not cover", shadows[0].Name)
	}
}

// TestNoteShadowsCarriesIntoActionWithoutMovingStatus pins the split the
// package makes deliberately: `.drift.status` is a documented gate value about
// the three axes, so a shadow must not silently change what it answers — while
// Action, the line a human actually reads, must not print "nothing owed" beside
// three frozen binaries with nothing connecting them.
func TestNoteShadowsCarriesIntoActionWithoutMovingStatus(t *testing.T) {
	base := stub{running: revMain, installed: all(revMain), repo: "/src/pogo", main: revMain}

	clean := Check(base.deps(), DefaultRef)
	if clean.Status != StatusClean || clean.ShadowHazard {
		t.Fatalf("control: want a clean report with no shadow flag, got status=%s hazard=%v", clean.Status, clean.ShadowHazard)
	}

	deps := base.deps()
	deps.Shadows = func() []Shadow {
		return []Shadow{
			{Name: "pogod", Path: "/usr/local/bin/pogod", Winner: "/gobin/pogod", Revision: revOld, Note: "SHADOWED STALE COPY"},
			{Name: "mg", Path: "/usr/local/bin/mg", Winner: "/gobin/mg", Benign: true, Note: "resolves to the same file"},
		}
	}
	r := Check(deps, DefaultRef)

	if r.Status != StatusClean {
		t.Errorf("status = %s, want %s — a shadow is not one of the three axes and must not move their verdict", r.Status, StatusClean)
	}
	if !r.ShadowHazard {
		t.Error("ShadowHazard is false with a stale shadowed copy present")
	}
	for _, want := range []string{"SHADOWED COPIES (1)", "/usr/local/bin/pogod", Short(revOld), "ln -sfn"} {
		if !strings.Contains(r.Action, want) {
			t.Errorf("Action does not mention %q — the one line a reader checks:\n%s", want, r.Action)
		}
	}
	if strings.Contains(r.Action, "/usr/local/bin/mg") {
		t.Errorf("Action reports the benign symlinked copy as a finding:\n%s", r.Action)
	}

	text := r.Text()
	if !strings.Contains(text, "SHADOWED pogod") {
		t.Errorf("Text() does not surface the hazard row:\n%s", text)
	}
	if !strings.Contains(text, "shadowed mg") {
		t.Errorf("Text() drops the benign row entirely — a reader cannot tell a second copy was examined from one that was never there:\n%s", text)
	}
}

// TestInstallSetMatchesInstallScript is the coupling that produced mg-dabf.
// install.sh places four binaries; DeployedCmds covers two; nothing compared
// the lists, so lsp and pose sat frozen in /usr/local/bin for five and a half
// months with no instrument on the box looking at them. A name added to
// install.sh must arrive here, and this test is the only thing that says so.
func TestInstallSetMatchesInstallScript(t *testing.T) {
	path := filepath.Join("..", "..", "install.sh")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := regexp.MustCompile(`(?m)^BINARIES="([^"]*)"`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("install.sh has no BINARIES= line; if it was renamed, this test must follow it rather than be deleted")
	}
	want := strings.Fields(string(m[1]))
	have := map[string]bool{}
	for _, n := range InstallSet {
		have[n] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("install.sh installs %q but InstallSet does not list it — a copy of it on $PATH would be invisible to `pogo service status`", n)
		}
	}
	if len(InstallSet) != len(want) {
		t.Errorf("InstallSet = %v, install.sh BINARIES = %v", InstallSet, want)
	}
}
