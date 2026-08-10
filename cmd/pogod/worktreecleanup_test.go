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
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	// Repo and worktree are siblings INSIDE the directory this test was handed.
	// They used to be siblings inside filepath.Dir(t.TempDir()) — the testing
	// package's own MkdirTemp root, which it creates, owns and rm -rf's. A test
	// is given `001`, not the directory `001` sits in, and nothing documents
	// that entries may be created alongside it (mg-5561).
	dir := filepath.Join(base, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s%s", args, err, out, worktreeAdminState(dir))
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
	wt := filepath.Join(base, "wt-cat1")
	run("worktree", "add", "-q", wt, "polecat-cat1")
	return dir, wt
}

// catAgent is the exitedAgent a polecat's exit hands cleanupAgentWorktree —
// the same five fields main.go copies out of the registry record.
func catAgent(name, workItem, repo, wt string) exitedAgent {
	return exitedAgent{
		Name:        name,
		EventAgent:  "cat-" + name,
		WorkItemID:  workItem,
		SourceRepo:  repo,
		WorktreeDir: wt,
	}
}

// worktreeAdminState renders git's linked-worktree bookkeeping for repo, so a
// failed setup command carries the evidence about it instead of only its exit
// status.
//
// mg-5561: CI has seen `wtRepo` die inside `git worktree add` with
//
//	fatal: could not open '.git/worktrees/wt-cat1/locked' for writing:
//	       No such file or directory
//
// That is a narrow and checkable claim. builtin/worktree.c creates
// .git/worktrees/<name> with mkdir and, a few statements later, writes a
// `locked` sentinel into it under the comment "lock the incomplete repo so
// prune won't delete it". ENOENT on that write means the directory git had
// just created successfully was gone by the time it wrote the guard — the one
// window the guard exists to close. Something removed it.
//
// WHAT WAS RULED OUT, so the next reader does not repeat it: none of the git
// commands wtRepo itself runs prunes a stale `.git/worktrees` entry (init,
// add, commit, branch, status, worktree add and gc --auto were each measured
// against a planted entry, and all left it alone); the package runs no test in
// parallel; a shim recording every git invocation of this package showed the
// only prunes are internal/gitgc's own, on their own repos, seconds earlier;
// and the same shim over the whole `go test ./...` (4,417 invocations) showed
// no prune or `worktree remove` ever aimed at another test's repo. No Go code
// in this repo removes `.git/worktrees/*`. Sending git a signal in that window
// does make it delete the directory — but git then dies of the signal, which
// Go reports as "signal: …", not the exit status 128 actually observed.
//
// So the remover is still unidentified, and the reason it is unidentified is
// that the artifact carried no evidence: an exit status, a git message, and
// nothing about the directory the message is about. This is diagnosis, not
// defence. It removes nothing, retries nothing and guards nothing; it only
// makes the next occurrence say who.
func worktreeAdminState(repo string) string {
	var b strings.Builder
	b.WriteString("\n--- linked-worktree bookkeeping for " + repo + " (mg-5561) ---\n")

	gitPath := filepath.Join(repo, ".git")
	switch fi, err := os.Lstat(gitPath); {
	case err != nil:
		b.WriteString(".git: " + err.Error() + "\n")
	case fi.IsDir():
		b.WriteString(".git: directory\n")
	default:
		b.WriteString(".git: " + fi.Mode().String() + " (not a directory)\n")
	}

	adminDir := filepath.Join(gitPath, "worktrees")
	entries, err := os.ReadDir(adminDir)
	switch {
	case os.IsNotExist(err):
		// The state the CI failure implies: git's own bookkeeping directory is
		// not there. Named explicitly because "absent" and "present but empty"
		// are different facts about who removed what.
		b.WriteString(".git/worktrees: ABSENT\n")
	case err != nil:
		b.WriteString(".git/worktrees: " + err.Error() + "\n")
	case len(entries) == 0:
		b.WriteString(".git/worktrees: present, EMPTY\n")
	default:
		for _, e := range entries {
			b.WriteString(".git/worktrees/" + e.Name() + ": ")
			inner, ierr := os.ReadDir(filepath.Join(adminDir, e.Name()))
			if ierr != nil {
				b.WriteString(ierr.Error() + "\n")
				continue
			}
			names := make([]string, 0, len(inner))
			for _, f := range inner {
				names = append(names, f.Name())
			}
			b.WriteString("[" + strings.Join(names, " ") + "]\n")
		}
	}

	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		b.WriteString("git worktree list: " + err.Error() + "\n")
	}
	b.WriteString("git worktree list --porcelain:\n" + string(out))

	parent := filepath.Dir(repo)
	if siblings, serr := os.ReadDir(parent); serr == nil {
		names := make([]string, 0, len(siblings))
		for _, s := range siblings {
			names = append(names, s.Name())
		}
		b.WriteString("contents of " + parent + ": [" + strings.Join(names, " ") + "]\n")
	}
	return b.String()
}

// TestWorktreeAdminStateReportsTheVanishedDirectory keeps the mg-5561
// diagnostic honest.
//
// A diagnostic nobody exercises is a comment that compiles. This one exists for
// a failure seen once, in CI, on Linux — so the only chance it will be right on
// the day it fires is a test that stages the state it describes and reads what
// it says. Both arms matter: the healthy repo must report the registration it
// has (or the helper could be a constant), and the stripped repo must report
// ABSENT in so many words (or a reader gets the same silence the original
// artifact gave).
func TestWorktreeAdminStateReportsTheVanishedDirectory(t *testing.T) {
	repo, wt := wtRepo(t)

	healthy := worktreeAdminState(repo)
	if !strings.Contains(healthy, ".git/worktrees/wt-cat1: [") {
		t.Errorf("a live registration must be itemised, got:\n%s", healthy)
	}
	if !strings.Contains(healthy, wt) {
		t.Errorf("the registration listing must name the worktree path %s, got:\n%s", wt, healthy)
	}
	if strings.Contains(healthy, "ABSENT") {
		t.Errorf("a healthy repo must not be reported as missing its bookkeeping, got:\n%s", healthy)
	}

	// The state the CI failure implies: git's bookkeeping directory gone while
	// the registration it described is still being created.
	if err := os.RemoveAll(filepath.Join(repo, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}
	stripped := worktreeAdminState(repo)
	if !strings.Contains(stripped, ".git/worktrees: ABSENT") {
		t.Errorf("the whole point is saying that git's own bookkeeping directory is not there — "+
			"without it the next occurrence is again an exit status and no evidence. Got:\n%s", stripped)
	}
	if !strings.Contains(stripped, ".git: directory") {
		t.Errorf("the report must distinguish a missing worktrees/ from a missing .git, got:\n%s", stripped)
	}
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

	outcome := cleanupAgentWorktree(catAgent("cat1", "mg-ee02", repo, wt), "mayor", mail)
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

	outcome := cleanupAgentWorktree(catAgent("cat1", "mg-ee02", repo, wt), "mayor", mail)
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
	if got := cleanupAgentWorktree(catAgent("cat1", "mg-ee02", "/nonexistent", ""), "mayor", nil); got != worktreeNone {
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

	if got := cleanupAgentWorktree(catAgent("cat1", "mg-ee02", repo, wt), "mayor", mail); got != worktreePreserved {
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

	outcome := cleanupAgentWorktree(catAgent("cat1", "mg-ee02", repo, wt), "mayor", mail)
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
			if got := cleanupAgentWorktree(catAgent("cat-a15", "mg-342d", repo, wt), "mayor", mail); got != tc.want {
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

// TestPreservedWorktreeNoticeRefusesDispatchAtItsWorkItem is mg-32e3's control.
//
// THE MECHANISM DID NOT FAIL; IT WAS ADDRESSED TO THE WRONG QUESTION. The
// preserved-worktree notice worked — 22 delivered notices over three days, two
// of them for `qbe37`, whose tree held 16 uncommitted paths including a
// 1450-line package that existed in no other location on the machine. What it
// could not say is "do not dispatch a worker at this work item", because it was
// composed from the agent name, the repo and the tree, and no work-item id
// reached it. The message that says exactly that sentence,
// `work_item_stranded_push`, is defined over PUSHED commits and never fires for
// a tree whose work was never committed. The fleet held both halves and
// combined neither, and on 2026-08-10 the coordinator received two notices for
// qbe37 and dispatched at its work item anyway.
//
// So this asserts the sentence, and asserts it in the SUBJECT: a prohibition
// that arrives in paragraph four of a message filed under worktree hygiene is a
// prohibition that does not travel.
func TestPreservedWorktreeNoticeRefusesDispatchAtItsWorkItem(t *testing.T) {
	repo, wt := wtRepo(t)
	if err := os.WriteFile(filepath.Join(wt, "strandwatch.go"), []byte("package strandwatch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotSubject, gotBody = subject, body
		return nil
	}

	if got := cleanupAgentWorktree(catAgent("qbe37", "mg-be37", repo, wt), "mayor", mail); got != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved", got)
	}

	if !strings.Contains(gotSubject, "mg-be37") {
		t.Errorf("the SUBJECT must name the work item — it is the part that gets skimmed and "+
			"forwarded, and a notice that names only the tree is what let a dispatch go out on "+
			"top of preserved work. Got %q", gotSubject)
	}
	if !strings.Contains(gotSubject, "do NOT dispatch") {
		t.Errorf("the SUBJECT must carry the prohibition, matching the stranded-push alert. Got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "DO NOT DISPATCH A WORKER AT mg-be37") {
		t.Errorf("the body must say the sentence in full, got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "work item: mg-be37") {
		t.Errorf("the body must state the work item as a field a reader can act on, got:\n%s", gotBody)
	}
	// The reason the prohibition is needed at all: nothing else can produce it.
	// A reader told only "there is a pinned tree" reaches for the guards that
	// exist, and every one of them is blind here.
	for _, want := range []string{"PUSHED commits", "check-stranded", "priority-wake"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("the body must explain that no pushed-commit guard can see this (missing %q), got:\n%s",
				want, gotBody)
		}
	}
	// The hygiene half must survive the addition — this is still the message
	// that says a tree is pinned and how to reclaim it.
	if !strings.Contains(gotBody, wt) || !strings.Contains(gotBody, "pogo gc") {
		t.Errorf("the worktree-hygiene half must not be displaced, got:\n%s", gotBody)
	}
}

// TestPreservedWorktreeNoticeWithoutAWorkItemSaysSo is the negative arm, and it
// is the one that keeps this fix from re-committing the defect it repairs.
//
// The obvious shape — print the work-item row only when there is an id — makes
// "this agent has no item" and "nobody passed the id" the same artifact, which
// is precisely how `a.WorkItemID` sat unpassed five arguments from the call
// site with nothing anywhere to notice. A crew agent legitimately has no item;
// a polecat with none has a broken record. Only a reader can tell which, so the
// notice must say which case it is rather than going quiet.
//
// It must also NOT invent a prohibition it cannot address: "do NOT dispatch at
// """ is worse than silence.
func TestPreservedWorktreeNoticeWithoutAWorkItemSaysSo(t *testing.T) {
	repo, wt := wtRepo(t)
	if err := os.WriteFile(filepath.Join(wt, "wip.go"), []byte("package wip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotSubject, gotBody = subject, body
		return nil
	}

	if got := cleanupAgentWorktree(catAgent("crew1", "", repo, wt), "mayor", mail); got != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved", got)
	}
	if strings.Contains(gotSubject, "do NOT dispatch") {
		t.Errorf("with no work item there is no item to refuse dispatch at; the subject must not "+
			"claim one. Got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "NONE RECORDED") {
		t.Errorf("a missing work item must be REPORTED, not omitted — an absent field and a field "+
			"nobody passed look identical, and that identity is this ticket. Got:\n%s", gotBody)
	}
	if strings.Contains(gotBody, "DO NOT DISPATCH A WORKER AT \n") ||
		strings.Contains(gotBody, "dispatch at .") {
		t.Errorf("the prohibition must not be rendered with an empty item, got:\n%s", gotBody)
	}
}

// TestUndeterminedWorktreeNoticeDoesNotClaimWorkExists keeps the mg-4d45
// distinction intact through the mg-32e3 change.
//
// Cannot-tell is its own answer. The prohibition still belongs here — an item
// whose tree could not be read cannot be certified safe to dispatch at — but it
// must be stated as "until this tree has been read", never as "this tree holds
// work", which is a claim nobody established.
func TestUndeterminedWorktreeNoticeDoesNotClaimWorkExists(t *testing.T) {
	repo, wt := wtRepo(t)
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotSubject, gotBody = subject, body
		return nil
	}

	if got := cleanupAgentWorktree(catAgent("cat1", "mg-4d45", repo, wt), "mayor", mail); got != worktreeUndetermined {
		t.Fatalf("outcome = %v, want worktreeUndetermined", got)
	}
	if !strings.Contains(gotSubject, "mg-4d45") || !strings.Contains(gotSubject, "do NOT dispatch") {
		t.Errorf("an unreadable tree still makes its item unsafe to dispatch at; say so in the "+
			"subject. Got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "UNTIL THIS TREE HAS BEEN READ") {
		t.Errorf("the prohibition must be the conditional one, got:\n%s", gotBody)
	}
	if strings.Contains(gotBody, "This tree holds work that was never committed") {
		t.Errorf("we did not establish that there IS work here — asserting it sends a reader "+
			"hunting files that may not exist (mg-4d45). Got:\n%s", gotBody)
	}
}

// TestPreservedWorktreeIsOnTheEventSpine covers the record half.
//
// The preservation path's mail half works and its record half did not exist:
// three days of it had to be reconstructed by grepping `PRESERVED worktree` out
// of pogod.log, which pogod writes to inherited stderr and which therefore is
// not durable at all. That is the exact mirror of `work_item_stranded_push`,
// whose event half worked and whose mail half was missing until mg-be37 — so
// both halves are asserted here, keyed on the field that makes the event
// joinable to anything: the work item.
//
// Both outcomes are asserted because a consumer asking "does this item have
// work nobody pushed?" needs both: `preserved` is a positive finding and
// `undetermined` is a tree that could not be ruled out.
func TestPreservedWorktreeIsOnTheEventSpine(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcome     string
		setup       func(t *testing.T, wt string)
		want        worktreeCleanupOutcome
		wantDirty   bool
		wantDetails string
	}{
		{
			name:    "preserved",
			outcome: "preserved",
			setup: func(t *testing.T, wt string) {
				if err := os.WriteFile(filepath.Join(wt, "strandwatch.go"), []byte("package x\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			want:        worktreePreserved,
			wantDirty:   true,
			wantDetails: "uncommitted change",
		},
		{
			name:    "undetermined",
			outcome: "undetermined",
			setup: func(t *testing.T, wt string) {
				if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /nonexistent\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			want:        worktreeUndetermined,
			wantDirty:   false,
			wantDetails: "cannot determine",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spine := filepath.Join(t.TempDir(), "events.log")
			events.SetLogPathForTesting(spine)
			t.Cleanup(func() { events.SetLogPathForTesting("") })

			repo, wt := wtRepo(t)
			tc.setup(t, wt)
			mail := func(to, from, subject, body string) error { return nil }

			if got := cleanupAgentWorktree(catAgent("qbe37", "mg-be37", repo, wt), "mayor", mail); got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}

			found, err := events.ReadFiltered(spine, events.Filter{Type: "worktree_preserved"})
			if err != nil {
				t.Fatalf("reading the spine: %v", err)
			}
			if len(found) != 1 {
				t.Fatalf("want exactly one worktree_preserved event, got %d: %+v\n"+
					"A retained worktree left NOTHING structured behind before mg-32e3 — three days "+
					"of this path had to be reconstructed from log.Printf lines pogod writes to "+
					"inherited stderr.", len(found), found)
			}
			ev := found[0]
			if ev.WorkItemID != "mg-be37" {
				t.Errorf("work_item_id = %q, want mg-be37 — without it the event cannot be joined "+
					"to the item that is now unsafe to dispatch at, which is the whole ticket",
					ev.WorkItemID)
			}
			if ev.Repo != repo {
				t.Errorf("repo = %q, want %q", ev.Repo, repo)
			}
			if ev.Agent != "cat-qbe37" {
				t.Errorf("agent = %q, want the event identity cat-qbe37 (docs/event-log.md convention)", ev.Agent)
			}
			if ev.Details["outcome"] != tc.outcome {
				t.Errorf("outcome = %v, want %q — a tree known dirty and a tree we could not read "+
					"are different facts", ev.Details["outcome"], tc.outcome)
			}
			if pushed, ok := ev.Details["pushed"].(bool); !ok || pushed {
				t.Errorf("pushed = %v, want false stated explicitly: this is the population every "+
					"pushed-commit guard misses, and a consumer must not have to infer it",
					ev.Details["pushed"])
			}
			if s, _ := ev.Details["detail"].(string); !strings.Contains(s, tc.wantDetails) {
				t.Errorf("detail = %q, want it to carry the underlying finding (%q)", s, tc.wantDetails)
			}
			_, hasDirty := ev.Details["dirty_paths"]
			if hasDirty != tc.wantDirty {
				t.Errorf("dirty_paths present = %t, want %t — a count is only meaningful when the "+
					"tree was actually read", hasDirty, tc.wantDirty)
			}
		})
	}
}

// TestPreservedWorktreeEventSurvivesAFailedNotice is the ordering proof.
//
// The event is emitted BEFORE the mail is attempted and does not depend on it,
// so the record is durable whatever happens to mg. Get this backwards and the
// improvement becomes a new dependency of the thing it was meant to back up —
// which is the shape of every notifier in this daemon that degrades to
// log.Printf when its send fails.
func TestPreservedWorktreeEventSurvivesAFailedNotice(t *testing.T) {
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	repo, wt := wtRepo(t)
	if err := os.WriteFile(filepath.Join(wt, "wip.go"), []byte("package wip\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mail := func(to, from, subject, body string) error {
		return errors.New("mg mail send failed: no such mailbox")
	}
	if got := cleanupAgentWorktree(catAgent("qbe37", "mg-be37", repo, wt), "mayor", mail); got != worktreePreserved {
		t.Fatalf("outcome = %v, want worktreePreserved", got)
	}

	preserved, err := events.ReadFiltered(spine, events.Filter{Type: "worktree_preserved"})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	if len(preserved) != 1 || preserved[0].WorkItemID != "mg-be37" {
		t.Fatalf("the preservation record must not depend on the mail succeeding, got %+v", preserved)
	}

	// And the A15 record of the LOST notice must carry the item too: when the
	// notice is gone, this event is the only surviving trace, so it has to
	// answer the same question the notice would have.
	lost, err := events.ReadFiltered(spine, events.Filter{Type: "worktree_notice_undelivered"})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	if len(lost) != 1 {
		t.Fatalf("want one worktree_notice_undelivered, got %+v", lost)
	}
	if lost[0].WorkItemID != "mg-be37" {
		t.Errorf("work_item_id = %q, want mg-be37 — a lost notice is exactly when the event is "+
			"the only trace of which item became unsafe to dispatch at", lost[0].WorkItemID)
	}
	if lost[0].Repo != repo {
		t.Errorf("repo = %q, want %q", lost[0].Repo, repo)
	}
}

// TestReapedWorktreeEmitsNothing is the noise control.
//
// The common path — a polecat that committed, pushed and merged — reaps its
// tree, and must leave neither a mail nor an event. An event on every clean exit
// makes `pogo events --type worktree_preserved` a stream instead of an alarm,
// and the point of the alarm is that a non-empty result means something.
func TestReapedWorktreeEmitsNothing(t *testing.T) {
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	repo, wt := wtRepo(t)
	if got := cleanupAgentWorktree(catAgent("cat1", "mg-clean", repo, wt), "mayor",
		func(to, from, subject, body string) error { return nil }); got != worktreeReaped {
		t.Fatalf("outcome = %v, want worktreeReaped", got)
	}
	found, err := events.ReadFiltered(spine, events.Filter{Type: "worktree_preserved"})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("a reaped clean worktree must emit nothing; got %+v", found)
	}
}
