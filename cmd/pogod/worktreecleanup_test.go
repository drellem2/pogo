package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// wtRepo builds a real git repo with a polecat worktree, standing in for the
// state a live polecat is in at exit.
func wtRepo(t *testing.T) (repo string, worktree string) {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.name", "t")
	run("config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "seed.txt")
	run("commit", "-qm", "seed")
	run("branch", "polecat-cat1")
	wt := filepath.Join(filepath.Dir(dir), "wt-cat1")
	run("worktree", "add", "-q", wt, "polecat-cat1")
	return dir, wt
}

// TestCleanupAgentWorktreePreservesDirty is the end-to-end control for
// mg-ee02 at the exit-hook layer — the layer `pogo agent stop` actually
// reaches. `stop` SIGTERMs the process; the registry's onExit hook then fires
// and calls exactly this function. Before the fix it force-removed, and a
// stopped mid-flight polecat lost its working tree.
func TestCleanupAgentWorktreePreservesDirty(t *testing.T) {
	repo, wt := wtRepo(t)
	racetest := filepath.Join(wt, "trust_hook_race_test.go")
	if err := os.WriteFile(racetest, []byte("package x // 201 irreplaceable lines\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotTo, gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotTo, gotSubject, gotBody = to, subject, body
		return nil
	}

	outcome := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail)
	if outcome != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved", outcome)
	}
	if _, err := os.Stat(racetest); err != nil {
		t.Fatalf("THE WORK WAS DESTROYED: %v", err)
	}

	// Preservation must be loud, or it trades data loss for silent
	// accumulation — the exact cost of choosing preserve over refuse.
	if gotTo != "mayor" {
		t.Errorf("notice should go to the coordinator, got %q", gotTo)
	}
	if !strings.Contains(gotSubject, "cat1") {
		t.Errorf("subject should name the agent, got %q", gotSubject)
	}
	// The operator needs three things to act: what was kept, where it is, and
	// how to reclaim it once they are done.
	if !strings.Contains(gotBody, "trust_hook_race_test.go") {
		t.Errorf("body must name the uncommitted file, got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, wt) {
		t.Errorf("body must give the worktree path, got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "--force") {
		t.Errorf("body must state how to reclaim it, got:\n%s", gotBody)
	}
}

// TestCleanupAgentWorktreeReapsClean is the negative control: the common case
// — stopping a polecat that has committed and merged — must still reap. A fix
// that leaks worktrees is a different defect, not a fix.
func TestCleanupAgentWorktreeReapsClean(t *testing.T) {
	repo, wt := wtRepo(t)

	mailed := false
	mail := func(to, from, subject, body string) error { mailed = true; return nil }

	outcome := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail)
	if outcome != worktreeReaped {
		t.Fatalf("outcome = %v, want worktreeReaped", outcome)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("clean worktree must still be reaped, stat err = %v", err)
	}
	if mailed {
		t.Error("reaping a clean worktree must not mail anyone; that would be noise on the common path")
	}
	// The registration must go too, or the branch stays pinned and the
	// gitgc branch phase leaks.
	out, _ := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	if strings.Contains(string(out), wt) {
		t.Errorf("registration should be gone:\n%s", out)
	}
}

// TestCleanupAgentWorktreeNoWorktree: a --no-worktree polecat has nothing to
// clean up and must not be treated as an error.
func TestCleanupAgentWorktreeNoWorktree(t *testing.T) {
	if got := cleanupAgentWorktree("cat1", "/nonexistent", "", "mayor", nil); got != worktreeNone {
		t.Errorf("outcome = %v, want worktreeNone", got)
	}
}

// TestCleanupAgentWorktreeSurvivesMailFailure: an unreachable coordinator must
// not change the preservation decision. The tree stays either way — losing the
// work because the mail failed would reintroduce the bug through the back door.
func TestCleanupAgentWorktreeSurvivesMailFailure(t *testing.T) {
	repo, wt := wtRepo(t)
	keep := filepath.Join(wt, "wip.go")
	if err := os.WriteFile(keep, []byte("package wip\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mail := func(to, from, subject, body string) error { return os.ErrDeadlineExceeded }

	if got := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail); got != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved even when mail fails", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("work must survive a failed notification: %v", err)
	}
}

// TestCleanupAgentWorktreeKeepsUndeterminable is the mg-4d45 control at the
// exit-hook layer — the layer `pogo agent stop` actually reaches.
//
// The hook fires AFTER the process has exited, so this is precisely the site
// where a naive reading of "liveness decides" would answer GONE and reap. It
// must not: the tree belonged to an agent that was running until moments ago,
// and its files are that agent's in-flight work.
func TestCleanupAgentWorktreeKeepsUndeterminable(t *testing.T) {
	repo, wt := wtRepo(t)
	precious := filepath.Join(wt, "irreplaceable.go")
	if err := os.WriteFile(precious, []byte("package x // the only copy\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// A genuine `git status` failure: a present but corrupt .git pointer.
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent/garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotSubject, gotBody = subject, body
		return nil
	}

	outcome := cleanupAgentWorktree("cat1", repo, wt, "mayor", mail)
	if outcome != worktreeUndetermined {
		t.Fatalf("outcome = %v, want worktreeUndetermined", outcome)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("THE WORK WAS DESTROYED — a tree we could not read must survive: %v", err)
	}

	// The notice must report what actually happened. Claiming "uncommitted
	// work" would send an operator hunting for files we never established are
	// there; cannot-tell has to stay distinguishable from dirty.
	if !strings.Contains(gotBody, "could NOT be checked") {
		t.Errorf("body must say the check failed, got:\n%s", gotBody)
	}
	if strings.Contains(gotSubject, "preserved uncommitted work") {
		t.Errorf("subject must not claim uncommitted work was found, got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "pogo gc") {
		t.Errorf("body must say how to reclaim the tree, got:\n%s", gotBody)
	}
}

// TestCleanupAgentWorktreeRecordsAnUndeliveredNotice is enumeration row A15's
// positive control (mg-342d).
//
// A15 is the one row in mg-c3f0's Class A whose fix is NOT a mail, and the reason
// is that mail is the thing that failed. Reacting to a failed send by sending
// another mail is a retry wearing an alarm's clothes: it fails the same way for
// the same reason. mg-c3f0's meta-finding named this exact shape — 12
// notification sites degrade to log.Printf when their send fails, so pogod.log is
// not only where UNROUTED conditions go, it is where ROUTED ones go to die.
//
// worktreecleanup.go emitted no events at all before this, so a preserved
// worktree whose notice was lost left nothing behind but a log line: the tree
// pinned its branch indefinitely and no query anywhere could find out. The fix is
// to make the failure STRUCTURED rather than louder, which is what the six
// watcher packages already do with their mail_error field.
//
// TestCleanupAgentWorktreeSurvivesMailFailure above covers the other half: the
// WORK survives a failed notification. This covers the record of it.
func TestCleanupAgentWorktreeRecordsAnUndeliveredNotice(t *testing.T) {
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	for _, tc := range []struct {
		name    string
		outcome string
		setup   func(t *testing.T, wt string)
		want    worktreeCleanupOutcome
	}{
		{
			name:    "preserved",
			outcome: "preserved",
			setup: func(t *testing.T, wt string) {
				if err := os.WriteFile(filepath.Join(wt, "wip.go"), []byte("package wip\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			want: worktreePreserved,
		},
		{
			name:    "undetermined",
			outcome: "undetermined",
			setup: func(t *testing.T, wt string) {
				// A present but corrupt .git pointer: `git status` fails, so
				// dirtiness cannot be determined and the tree is KEPT.
				if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			want: worktreeUndetermined,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, wt := wtRepo(t)
			tc.setup(t, wt)
			mail := func(to, from, subject, body string) error {
				return errors.New("mg mail send failed: no such mailbox")
			}
			if got := cleanupAgentWorktree("cat-a15", repo, wt, "mayor", mail); got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}

			found, err := events.ReadFiltered(spine, events.Filter{
				Type: "worktree_notice_undelivered",
			})
			if err != nil {
				t.Fatalf("reading the spine: %v", err)
			}
			var ev *events.Event
			for i := range found {
				if found[i].Details["outcome"] == tc.outcome {
					ev = &found[i]
				}
			}
			if ev == nil {
				t.Fatalf("a %s-worktree notice failed to send and left NOTHING on the event "+
					"spine. The tree now pins its branch and no query can find out why — "+
					"which is mg-c3f0's meta-finding verbatim: pogod.log is where routed "+
					"conditions go to die. Events found: %+v", tc.outcome, found)
			}
			if ev.Agent != "mayor" {
				t.Errorf("event attributed to %q, want the addressee that did NOT hear (mayor) — "+
					"the open question is what the coordinator was never told", ev.Agent)
			}
			if ev.Details["row"] != "A15" {
				t.Errorf("row = %v, want A15 so the enumeration and the daemon can be reconciled",
					ev.Details["row"])
			}
			if s, _ := ev.Details["mail_error"].(string); !strings.Contains(s, "no such mailbox") {
				t.Errorf("mail_error = %q, want the underlying send failure — a record that the "+
					"notice was lost without saying WHY is a record nobody can act on", s)
			}
			if ev.Details["worktree"] != wt {
				t.Errorf("worktree = %v, want %s: the whole point is naming the tree that is "+
					"being preserved unannounced", ev.Details["worktree"], wt)
			}
		})
	}
}
