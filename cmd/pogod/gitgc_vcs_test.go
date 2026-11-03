package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/vcs"
)

// TestRunGitGCSweepDoesNotSweepAnEnthProjectAsGit drives runGitGCSweep over an
// enth project whose directory has no .git — /Users/daniel/dev/mg-enth's shape
// (mg-0ff9c). Swept as git it failed "not a git repository" every tick and
// raised A9 (gitgc_sweep_failed:<repo>) permanently. The control runs the same
// directory with the lookup answering git, and must reproduce that failure
// line, so the fix's absence of it is evidence and not an unreached path.
func TestRunGitGCSweepDoesNotSweepAnEnthProjectAsGit(t *testing.T) {
	sandboxPogoHome(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".enth"), 0o755); err != nil {
		t.Fatal(err)
	}

	origTickets, origVCS := loadTicketIndexFn, gitGCRepoVCS
	t.Cleanup(func() { loadTicketIndexFn, gitGCRepoVCS = origTickets, origVCS })
	loadTicketIndexFn = func() (gitgc.TicketIndex, error) { return gitgc.TicketIndex{}, nil }

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	cfg := config.GitGCConfig{Enabled: true, Repos: []string{repo}}
	failed := "git GC sweep of " + repo + " failed"

	// Control: the record says git (or there is none).
	logged := captureLog(t)
	gitGCRepoVCS = func(string) vcs.Kind { return vcs.Git }
	runGitGCSweep(reg, cfg, "")
	if out := logged(); !strings.Contains(out, failed) || !strings.Contains(out, "not a git repository") {
		t.Fatalf("control: sweeping the .git-less dir as git did not fail as the incident did:\n%s", out)
	}

	// The record says enth.
	logged = captureLog(t)
	gitGCRepoVCS = func(r string) vcs.Kind {
		if r == repo {
			return vcs.Enth
		}
		return vcs.Git
	}
	runGitGCSweep(reg, cfg, "")
	out := logged()
	if strings.Contains(out, failed) {
		t.Errorf("an enth project was swept as git and raised the degraded condition:\n%s", out)
	}
	line := findLogLine(out, "coverage:")
	if line == "" || !strings.Contains(line, repo) || !strings.Contains(line, "vcs=enth") {
		t.Errorf("the skip is silent — want a coverage line naming %s and vcs=enth:\n%s", repo, out)
	}
}
