package selfdrift

import (
	"strings"
	"testing"
)

// stub builds Deps from a fixed set of observations, so the classifier — the
// only part of this package with judgement in it — is exercised without a
// daemon, a checkout, or an installed binary.
type stub struct {
	running   string
	installed map[string]string // binary name -> revision
	repo      string
	repoNote  string
	main      string
	inRepo    map[string]bool // revision -> does the checkout contain it
}

func (s stub) deps() Deps {
	return Deps{
		RunningRev:   func() (string, string) { return s.running, "http://localhost:10000/version" },
		InstalledBin: func(name string) string { return "/gobin/" + name },
		BinaryRev: func(path string) string {
			name := path[strings.LastIndex(path, "/")+1:]
			if rev, ok := s.installed[name]; ok {
				return rev
			}
			return RevMissing
		},
		ResolveRepo: func() (string, string) { return s.repo, s.repoNote },
		MainRev:     func(string, string) string { return s.main },
		RevInRepo: func(_, rev string) bool {
			if s.inRepo == nil {
				// Default: every non-sentinel revision belongs to the repo, so
				// a test that is not about provenance does not accidentally
				// trip the foreign gate.
				return !isSentinel(rev)
			}
			return s.inRepo[rev]
		},
	}
}

// all builds an installed map where every deployed binary sits at rev.
func all(rev string) map[string]string {
	m := map[string]string{}
	for _, name := range DeployedCmds {
		m[name] = rev
	}
	return m
}

const (
	revMain = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	revOld  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	revMid  = "cccccccccccccccccccccccccccccccccccccccc"
)

// TestClassify is the positive-control table. Every row but the first asserts
// the check reports DRIFT or UNKNOWN — a status command only ever observed
// green has not been tested, which is the requirement mg-75ec was filed with.
func TestClassify(t *testing.T) {
	cases := []struct {
		name         string
		s            stub
		wantStatus   Status
		wantBuild    bool
		wantRestart  bool
		wantInAction []string
	}{
		{
			name:         "clean: running == installed == main",
			s:            stub{running: revMain, installed: all(revMain), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusClean,
			wantInAction: []string{"clean", "nothing owed"},
		},
		{
			name:         "restart owed: binaries current, daemon still on the old code",
			s:            stub{running: revOld, installed: all(revMain), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantRestart:  true,
			wantInAction: []string{"RESTART owed", "no rebuild"},
		},
		{
			name:         "build owed: daemon is main, the CLI on disk is not",
			s:            stub{running: revMain, installed: map[string]string{"pogod": revMain, "pogo": revOld}, repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantBuild:    true,
			wantInAction: []string{"BUILD owed", "pogo"},
		},
		{
			name:         "build+restart owed: running == installed, both behind main",
			s:            stub{running: revOld, installed: all(revOld), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantBuild:    true,
			wantRestart:  true,
			wantInAction: []string{"BUILD + RESTART owed", "both behind"},
		},
		{
			name:         "build+restart owed: all three axes differ",
			s:            stub{running: revOld, installed: all(revMid), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantBuild:    true,
			wantRestart:  true,
			wantInAction: []string{"BUILD + RESTART owed", "all differ"},
		},
		{
			name:         "daemon down is not the same finding as stale",
			s:            stub{running: RevUnreachable, installed: all(revMain), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantRestart:  true,
			wantInAction: []string{"NOT RUNNING", "not up"},
		},
		{
			name:         "daemon down AND the binary it would start is stale",
			s:            stub{running: RevUnreachable, installed: all(revOld), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantBuild:    true,
			wantRestart:  true,
			wantInAction: []string{"NOT RUNNING", "a plain restart would start stale code"},
		},
		{
			name:         "no pogod binary on disk owes a build",
			s:            stub{running: revMain, installed: map[string]string{"pogo": revMain}, repo: "/src/pogo", main: revMain},
			wantStatus:   StatusDrift,
			wantBuild:    true,
			wantInAction: []string{"BUILD owed", "pogod"},
		},
		{
			name:         "unstamped binary is UNKNOWN, never clean and never behind",
			s:            stub{running: revMain, installed: map[string]string{"pogod": RevUnstamped, "pogo": revMain}, repo: "/src/pogo", main: revMain},
			wantStatus:   StatusUnknown,
			wantInAction: []string{"UNKNOWN PROVENANCE", "installed pogod", "rebuild will NOT clear this"},
		},
		{
			name:         "unstamped DAEMON is UNKNOWN too",
			s:            stub{running: RevUnstamped, installed: all(revMain), repo: "/src/pogo", main: revMain},
			wantStatus:   StatusUnknown,
			wantInAction: []string{"UNKNOWN PROVENANCE", "running pogod"},
		},
		{
			name: "foreign stamp is UNKNOWN, and names what was claimed",
			s: stub{
				running: revOld, installed: all(revMain), repo: "/src/pogo", main: revMain,
				inRepo: map[string]bool{revMain: true}, // revOld belongs to some other repo
			},
			wantStatus:   StatusUnknown,
			wantInAction: []string{"FOREIGN STAMP", "running pogod claims " + Short(revOld), "/src/pogo"},
		},
		{
			name:         "no checkout, daemon running replaced code: DRIFT with two axes",
			s:            stub{running: revOld, installed: all(revMain), repoNote: "no pogo source checkout (pass --repo, or set POGO_REPO)"},
			wantStatus:   StatusDrift,
			wantRestart:  true,
			wantInAction: []string{"RESTART owed", "already been replaced on disk", "main HEAD not compared"},
		},
		{
			name:         "no checkout, running == installed: UNKNOWN, not clean",
			s:            stub{running: revMain, installed: all(revMain), repoNote: "no pogo source checkout (pass --repo, or set POGO_REPO)"},
			wantStatus:   StatusUnknown,
			wantInAction: []string{"no third axis", "--repo"},
		},
		{
			name:         "no checkout and no daemon: UNKNOWN, says both",
			s:            stub{running: RevUnreachable, installed: all(revMain), repoNote: "no pogo source checkout (pass --repo, or set POGO_REPO)"},
			wantStatus:   StatusUnknown,
			wantInAction: []string{"NOT RUNNING", "main axis is unavailable"},
		},
		{
			name:         "no checkout, daemon up, no binary to restart into",
			s:            stub{running: revMain, installed: map[string]string{"pogo": revMain}, repoNote: "no pogo source checkout"},
			wantStatus:   StatusUnknown,
			wantInAction: []string{"no pogod binary is installed", "restart would not bring it back"},
		},
		{
			name:         "a checkout with no such ref falls back to the two-axis report",
			s:            stub{running: revOld, installed: all(revMain), repo: "/src/pogo", repoNote: "from --repo", main: ""},
			wantStatus:   StatusDrift,
			wantRestart:  true,
			wantInAction: []string{"RESTART owed", `has no ref "main"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Check(tc.s.deps(), "")
			if r.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q\naction: %s", r.Status, tc.wantStatus, r.Action)
			}
			if r.NeedsBuild != tc.wantBuild {
				t.Errorf("needs_build = %v, want %v (action: %s)", r.NeedsBuild, tc.wantBuild, r.Action)
			}
			if r.NeedsRestart != tc.wantRestart {
				t.Errorf("needs_restart = %v, want %v (action: %s)", r.NeedsRestart, tc.wantRestart, r.Action)
			}
			for _, want := range tc.wantInAction {
				if !strings.Contains(r.Action, want) {
					t.Errorf("action does not mention %q:\n%s", want, r.Action)
				}
			}
			// The rendered report must never disagree with the verdict.
			if txt := r.Text(); !strings.Contains(txt, string(r.Status)) || !strings.Contains(txt, r.Action) {
				t.Errorf("Text() omits status or action:\n%s", txt)
			}
		})
	}
}

// TestCheckReportsEveryAxis pins that the report always names all three axes
// and where each was observed. A verdict whose evidence cannot be re-derived
// from its own output sends the reader off to re-investigate from scratch,
// which is what mg-49bc had to do.
func TestCheckReportsEveryAxis(t *testing.T) {
	s := stub{running: revOld, installed: all(revMain), repo: "/src/pogo", main: revMain}
	r := Check(s.deps(), "main")

	want := []string{"running pogod", "installed pogod", "installed pogo"}
	if len(r.Axes) != len(want) {
		t.Fatalf("got %d axes, want %d: %+v", len(r.Axes), len(want), r.Axes)
	}
	for i, name := range want {
		if r.Axes[i].Name != name {
			t.Errorf("axis %d = %q, want %q", i, r.Axes[i].Name, name)
		}
		if r.Axes[i].Path == "" {
			t.Errorf("axis %q reports no path — the observation cannot be re-derived", name)
		}
	}
	txt := r.Text()
	for _, frag := range []string{"/gobin/pogod", "/gobin/pogo", "localhost:10000/version", "main HEAD", revMain} {
		if !strings.Contains(txt, frag) {
			t.Errorf("Text() omits %q:\n%s", frag, txt)
		}
	}
}

// TestNoCheckoutStillNamesTheMissingAxis: the degraded report must say WHY the
// third axis is absent. A blank line where main HEAD should be reads as a bug
// in the tool rather than as the expected state of a consumer with no clone.
func TestNoCheckoutStillNamesTheMissingAxis(t *testing.T) {
	s := stub{running: revMain, installed: all(revMain), repoNote: "no pogo source checkout (pass --repo, or set POGO_REPO)"}
	r := Check(s.deps(), "")
	txt := r.Text()
	if !strings.Contains(txt, "unavailable") || !strings.Contains(txt, "no pogo source checkout") {
		t.Errorf("degraded report does not explain the missing axis:\n%s", txt)
	}
	if !strings.Contains(txt, "repo: <none>") {
		t.Errorf("degraded report does not say there is no repo:\n%s", txt)
	}
}

// TestRefIsHonored: the compared ref is not hard-coded to "main", and it is
// named in the output — a report that silently compared against a different ref
// than the reader assumes is worse than no report.
func TestRefIsHonored(t *testing.T) {
	s := stub{running: revMain, installed: all(revMain), repo: "/src/pogo", main: revMain}
	r := Check(s.deps(), "release")
	if r.Ref != "release" {
		t.Errorf("ref = %q, want %q", r.Ref, "release")
	}
	if !strings.Contains(r.Text(), "release HEAD") {
		t.Errorf("Text() does not name the ref:\n%s", r.Text())
	}
	if r.Status != StatusClean {
		t.Errorf("status = %q, want clean", r.Status)
	}
}

func TestShort(t *testing.T) {
	if got := Short(revMain); got != revMain[:12] {
		t.Errorf("Short(rev) = %q, want 12 chars", got)
	}
	// Sentinels must survive intact: a truncated "<unreacha" helps nobody.
	for _, s := range []string{RevUnstamped, RevMissing, RevUnreachable} {
		if got := Short(s); got != s {
			t.Errorf("Short(%q) = %q, want it left alone", s, got)
		}
	}
}
