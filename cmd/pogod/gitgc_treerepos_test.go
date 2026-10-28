package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/gitgc"
)

// TestRunGitGCSweepReachesARepoWithNoRunningPolecat is drellem2/pogo#162's
// second cause, end to end through runGitGCSweep, on a scratch repo.
//
// The state is the default one: no [gitgc] repos configured and no agent
// registered, so the sweep set used to be EMPTY and the sweep returned before
// doing anything — the concluded polecat's tree stayed on disk forever. The
// tree sits under $POGO_HOME/polecats, where pogod puts every polecat, and
// names its own repository; that is now enough to sweep it.
func TestRunGitGCSweepReachesARepoWithNoRunningPolecat(t *testing.T) {
	sandboxPogoHome(t)
	logged := captureLog(t)

	polecatsDir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		t.Fatal(err)
	}
	repo := newPolecatRepo(t)
	repo.git("branch", gitgc.BranchPrefix+"0047")
	wt := filepath.Join(polecatsDir, "0047")
	repo.git("worktree", "add", "-q", wt, gitgc.BranchPrefix+"0047")

	orig := loadTicketIndexFn
	t.Cleanup(func() { loadTicketIndexFn = orig })
	loadTicketIndexFn = func() (gitgc.TicketIndex, error) {
		return gitgc.TicketIndex{"mg-0047": gitgc.TicketArchived}, nil
	}

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if got := gitGCRepos(reg, config.GitGCConfig{}, polecatsDir); len(got) != 1 || got[0] != repo.dir {
		t.Fatalf("gitGCRepos = %v, want [%s] — the tree's own repository must be in the sweep set "+
			"with nothing configured and nothing running", got, repo.dir)
	}

	runGitGCSweep(reg, config.GitGCConfig{Enabled: true}, "")

	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("the concluded polecat's tree is still on disk (stat err = %v): a repository with no "+
			"running polecat must still be swept.\nlog was:\n%s", err, logged())
	}
}
