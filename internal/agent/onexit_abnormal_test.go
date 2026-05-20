package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/gitgc"
)

// makeWorktreeRepo creates a throwaway git repo with one polecat worktree
// and returns (sourceRepo, worktreeDir). It mirrors how a polecat is set
// up: an isolated worktree on a polecat-* branch.
func makeWorktreeRepo(t *testing.T, name string) (string, string) {
	t.Helper()
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	repo := filepath.Join(root, "src")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "seed")
	git("commit", "-q", "-m", "seed")

	wt := filepath.Join(root, "wt-"+name)
	git("worktree", "add", "-q", wt, "-b", "polecat-"+name)
	return repo, wt
}

// TestOnExitWorktreeCleanupOnAbnormalExit is the acceptance test for
// mg-30d5 D3: the onExit callback — where polecat worktree cleanup runs —
// must fire on abnormal exits, not only clean ones. waitAndHandle invokes
// onExit after cmd.Wait returns regardless of how the process died, so
// both a raw SIGKILL (crash) and a force-stop reach the cleanup.
func TestOnExitWorktreeCleanupOnAbnormalExit(t *testing.T) {
	cases := []struct {
		name string
		// kill performs the abnormal termination for an already-spawned agent.
		kill func(t *testing.T, reg *Registry, a *Agent)
	}{
		{
			name: "crash_SIGKILL",
			kill: func(t *testing.T, reg *Registry, a *Agent) {
				if err := a.cmd.Process.Kill(); err != nil {
					t.Fatalf("kill: %v", err)
				}
			},
		},
		{
			name: "force_stop",
			kill: func(t *testing.T, reg *Registry, a *Agent) {
				if err := reg.Stop(a.Name, 2*time.Second); err != nil {
					t.Fatalf("Stop: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sourceRepo, worktreeDir := makeWorktreeRepo(t, tc.name)

			tmpDir := t.TempDir()
			reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			defer reg.StopAll(2 * time.Second)

			// Mirror pogod's onExit hook: run the worktree cleanup for any
			// exited agent that owns a worktree.
			cleaned := make(chan error, 1)
			reg.SetOnExit(func(a *Agent, _ error) {
				if a.WorktreeDir == "" {
					cleaned <- nil
					return
				}
				cleaned <- gitgc.RemoveWorktree(a.SourceRepo, a.WorktreeDir)
			})

			// Spawn a long-lived polecat process so we control when it dies.
			a, err := reg.Spawn(SpawnRequest{
				Name:        tc.name,
				Type:        TypePolecat,
				Command:     []string{"cat"},
				WorktreeDir: worktreeDir,
				SourceRepo:  sourceRepo,
			})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}

			tc.kill(t, reg, a)

			select {
			case err := <-cleaned:
				if err != nil {
					t.Fatalf("worktree cleanup failed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("onExit callback did not fire on abnormal exit")
			}

			if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
				t.Errorf("worktree dir should be removed on abnormal exit, stat err = %v", err)
			}
		})
	}
}
