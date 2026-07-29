package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/freshen"
)

// captureAlerts replaces the mail sink for the duration of a test and returns
// a pointer to the captured alerts. Mandatory in every test that can reach the
// loud path: the real sink shells out to `mg mail send` and would deliver a
// fabricated alarm to the live coordinator.
func captureAlerts(t *testing.T) *[]freshen.Result {
	t.Helper()
	var got []freshen.Result
	prev := staleWorkspaceAlert
	staleWorkspaceAlert = func(name string, res freshen.Result) { got = append(got, res) }
	t.Cleanup(func() { staleWorkspaceAlert = prev })
	return &got
}

// fixtureGitArgs is every fixture git invocation's leading argument list.
//
// maintenance.auto=false IS THE POINT OF THIS VARIABLE, and it is not a tuning
// knob. `git commit` (and fetch, and push) ends by forking `git maintenance run
// --auto --detach`, which daemonizes and outlives its parent. Measured on this
// fixture before the change: advance(pub, 129) left 130 detached background git
// processes behind, one per commit, against a repo the test was still driving
// in the foreground. Every one of them is pure waste here — a fixture repo has
// nothing to maintain — and `gc.auto=0` does NOT suppress them, it only makes
// each one decline after it has already forked. maintenance.auto=false is the
// only setting that stops the fork; both were measured (mg-ea0c).
//
// This is a load reduction, not a retry. Nothing here swallows or re-attempts a
// git failure; see gitFailureForensics for what a failure now reports.
var fixtureGitArgs = []string{"-c", "maintenance.auto=false"}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(append([]string{}, fixtureGitArgs...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s%s", strings.Join(args, " "), dir, err, out,
			gitFailureForensics(dir, string(out)))
	}
	return strings.TrimSpace(string(out))
}

var oidRE = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// gitFailureForensics answers, in the failure output itself, the question a
// fixture git failure otherwise takes a day to answer.
//
// WHY THIS EXISTS. mg-ea0c was one CI line — `fatal: bad tree object <oid>` plus
// `remote unpack failed: eof before pack header was fully read` — and settling
// what it meant needed the fixture rebuilt by hand and four mutations run
// against it to discover that git prints that exact pair, with no other error
// line, for one cause only: the object was ABSENT at push time. A truncated
// object adds `object file ... is empty`; a corrupt one says `loose object ...
// is corrupt`; an unopenable one says `unable to open loose object ...`. Only
// ENOENT is silent, so the silence was the whole diagnosis and it had to be
// reconstructed from outside.
//
// So this block prints the discriminator at the moment of failure instead. It
// deliberately does NOT retry and does NOT soften the failure — the ticket's
// standing instruction is that a retry would convert the flake into a slow pass
// and destroy the evidence that something is killing git subprocesses, which is
// worth knowing regardless of this test. This is the opposite trade: the failure
// stays a failure and starts naming its own mechanism.
//
// Best-effort by construction. Every probe here can fail without adding noise to
// a report that is already about a failure.
func gitFailureForensics(dir, out string) string {
	var b strings.Builder
	b.WriteString("\n--- fixture git forensics (mg-ea0c) ---\n")

	// Resolve the object store rather than assuming dir/.git. Two fixture call
	// sites would make that assumption a lie: the bare origin keeps its objects at
	// the top level, and `clone` runs with dir set to the parent, which is not a
	// repository at all. Reporting ABSENT because we looked in the wrong place is
	// worse than reporting nothing — it is the exact wrong answer, confidently.
	gitDir := rawGit(dir, "rev-parse", "--absolute-git-dir")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		b.WriteString(fmt.Sprintf("no object store to inspect: %s is not a git "+
			"directory, so no conclusion is drawn about any object named above\n", gitDir))
		b.WriteString("--- end forensics ---")
		return b.String()
	}

	for _, oid := range oidRE.FindAllString(out, 3) {
		path := filepath.Join(gitDir, "objects", oid[:2], oid[2:])
		switch fi, err := os.Stat(path); {
		case err == nil:
			b.WriteString(fmt.Sprintf("object %s: PRESENT, %d bytes, mode %s\n",
				oid, fi.Size(), fi.Mode()))
		case os.IsNotExist(err):
			// The mg-ea0c signature. Distinguish "the object" from "its whole
			// fanout directory": both read as ENOENT to git and print nothing.
			dirGone := ""
			if _, derr := os.Stat(filepath.Dir(path)); os.IsNotExist(derr) {
				dirGone = " (its objects/" + oid[:2] + " directory is gone too)"
			}
			b.WriteString(fmt.Sprintf("object %s: ABSENT%s — a reachable loose object "+
				"vanished between the commit that wrote it and this command; git itself "+
				"never prunes reachable objects, so this is an external deleter\n", oid, dirGone))
		default:
			b.WriteString(fmt.Sprintf("object %s: unstattable: %v\n", oid, err))
		}
	}

	loose, packs := objectCounts(gitDir)
	b.WriteString(fmt.Sprintf("git dir: %s\nloose objects: %d, packs: %d\n", gitDir, loose, packs))
	b.WriteString("fsck: " + firstLines(rawGit(dir, "fsck", "--no-progress", "--connectivity-only"), 4) + "\n")
	b.WriteString("--- end forensics ---")
	return b.String()
}

// rawGit runs a git command for diagnostics only, returning its output whether
// or not it succeeded. It must never call t.Fatalf: it is used from a path that
// is already reporting a failure.
func rawGit(dir string, args ...string) string {
	cmd := exec.Command("git", append(append([]string{}, fixtureGitArgs...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return err.Error()
	}
	return strings.TrimSpace(string(out))
}

func objectCounts(gitDir string) (loose, packs int) {
	objects := filepath.Join(gitDir, "objects")
	fanouts, _ := os.ReadDir(objects)
	for _, f := range fanouts {
		if !f.IsDir() || len(f.Name()) != 2 {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(objects, f.Name()))
		loose += len(entries)
	}
	entries, _ := os.ReadDir(filepath.Join(objects, "pack"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pack") {
			packs++
		}
	}
	return loose, packs
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, " | ")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// newWorkspace builds a POGO_HOME containing agents/<name>/repo as a clone of
// a bare origin, and returns the publisher clone used to advance origin. This
// is the on-disk shape of the checkout the ticket is about.
func newWorkspace(t *testing.T, name string) (publisher string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("POGO_HOME", filepath.Join(root, "pogohome"))

	origin := filepath.Join(root, "origin.git")
	gitIn(t, root, "init", "--bare", "--initial-branch=main", origin)
	// The one detached maintenance daemon fixtureGitArgs cannot reach. A push's
	// remote half runs `git receive-pack` under ORIGIN's config, not under the
	// pushing command line, so `-c maintenance.auto=false` on the client does not
	// suppress it — measured. Writing it into origin's own config does.
	gitIn(t, origin, "config", "maintenance.auto", "false")

	publisher = filepath.Join(root, "publisher")
	gitIn(t, root, "clone", origin, publisher)
	writeFile(t, filepath.Join(publisher, "README.md"), "v1\n")
	gitIn(t, publisher, "add", "README.md")
	gitIn(t, publisher, "commit", "-m", "initial")
	gitIn(t, publisher, "push", "-u", "origin", "main")

	repo := WorkspaceRepoDir(name)
	if err := os.MkdirAll(filepath.Dir(repo), 0755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "clone", origin, repo)
	return publisher
}

// advance publishes n new commits on origin/main.
//
// ONE git process builds all n commits, not 2n of them. The tests need a
// checkout that is *a number of commits* behind — nothing about the shape of
// those commits is asserted — and the loop that used to produce them ran
// `git add` plus `git commit` per commit: 259 git processes for advance(pub,
// 129), each one also forking a detached `git maintenance` daemon.
//
// That cost is why this fixture was the one that lost. mg-ea0c is a 1-in-87 CI
// failure where a reachable loose object written by commit 75 of 129 was ABSENT
// from the sender's object store by the time the push read it — established from
// the error text, which git emits with no accompanying error line for that cause
// and no other (see gitFailureForensics). Nothing in the test can delete a
// reachable object, and git never prunes one, so the deleter is external and the
// test is a victim rather than the defect. What the test IS responsible for is
// being the largest git-subprocess population in the package and holding the
// window between writing that object and reading it open for ~2 seconds.
// fast-import closes the window to a single process and drops 256 spawns.
//
// This narrows exposure; it does not prove a cure, and it deliberately adds no
// retry. If the failure recurs, gitFailureForensics now names its mechanism in
// the CI log rather than leaving it to be reconstructed.
func advance(t *testing.T, publisher string, n int) {
	t.Helper()

	var stream strings.Builder
	for i := 0; i < n; i++ {
		f := fmt.Sprintf("f%d.txt", i)
		msg := "c" + f
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\n", i+1)
		fmt.Fprintf(&stream, "committer t <t@example.com> 0 +0000\n")
		fmt.Fprintf(&stream, "data %d\n%s\n", len(msg), msg)
		if i == 0 {
			fmt.Fprintf(&stream, "from refs/heads/main^0\n")
		} else {
			fmt.Fprintf(&stream, "from :%d\n", i)
		}
		fmt.Fprintf(&stream, "M 100644 inline %s\ndata 2\nx\n", f)
	}
	gitInStdin(t, publisher, stream.String(), "fast-import", "--quiet")

	// fast-import moves the ref without touching the index or worktree, which
	// would leave the publisher reporting 129 deletions to anything that looked
	// at it later. Nothing does today; a fixture that lies about its own state is
	// how the next investigation gets sent the wrong way.
	gitIn(t, publisher, "reset", "--hard", "refs/heads/main")

	gitIn(t, publisher, "push", "origin", "main")
}

// gitInStdin is gitIn with a body on stdin, for the one fixture command that
// takes its work as a stream rather than as arguments.
func gitInStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(append([]string{}, fixtureGitArgs...), args...)...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s%s", strings.Join(args, " "), dir, err, out,
			gitFailureForensics(dir, string(out)))
	}
	return strings.TrimSpace(string(out))
}

// TestWorkspaceRepoDirIsUnderAgentHome pins the convention this whole
// mechanism keys on. Nothing in pogo creates this path — it is a hand-made
// convention several crew agents follow, which is exactly why nothing kept it
// fresh.
func TestWorkspaceRepoDirIsUnderAgentHome(t *testing.T) {
	t.Setenv("POGO_HOME", "/tmp/ph")
	if got, want := WorkspaceRepoDir("pm-onethird"), "/tmp/ph/agents/pm-onethird/repo"; got != want {
		t.Errorf("WorkspaceRepoDir = %q, want %q", got, want)
	}
}

// TestFreshenWorkspaceRefreshesCleanStaleCheckout is the fix: a long-lived
// workspace that is months behind and clean comes current at agent start,
// without anyone having to remember anything.
func TestFreshenWorkspaceRefreshesCleanStaleCheckout(t *testing.T) {
	logPath := useTempEventLog(t)
	alerts := captureAlerts(t)
	pub := newWorkspace(t, "pm-test")
	advance(t, pub, 9)

	res := freshenWorkspace("pm-test")

	if res.Status != freshen.StatusUpdated {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, freshen.StatusUpdated, res)
	}
	if res.Behind != 9 {
		t.Errorf("Behind = %d, want 9", res.Behind)
	}
	if len(*alerts) != 0 {
		t.Errorf("a successfully refreshed workspace must not alert: %+v", *alerts)
	}

	ev := findEvent(readEventLines(t, logPath), "agent_workspace_freshened", "pm-test")
	if ev == nil {
		t.Fatal("no agent_workspace_freshened event emitted")
	}
	d := ev["details"].(map[string]any)
	if d["status"] != string(freshen.StatusUpdated) {
		t.Errorf("event status = %v, want %v", d["status"], freshen.StatusUpdated)
	}
	if d["behind"].(float64) != 9 {
		t.Errorf("event behind = %v, want 9", d["behind"])
	}
}

// TestFreshenWorkspaceAlertsOnDirtyStaleCheckout is the acceptance criterion:
// a workspace that cannot be refreshed must SAY SO where someone will see it,
// and must not be touched.
func TestFreshenWorkspaceAlertsOnDirtyStaleCheckout(t *testing.T) {
	logPath := useTempEventLog(t)
	alerts := captureAlerts(t)
	pub := newWorkspace(t, "pm-dirty")
	advance(t, pub, 129)

	repo := WorkspaceRepoDir("pm-dirty")
	precious := "uncommitted work\n"
	writeFile(t, filepath.Join(repo, "README.md"), precious)
	before := gitIn(t, repo, "rev-parse", "HEAD")

	res := freshenWorkspace("pm-dirty")

	if res.Status != freshen.StatusDeclinedDirty {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, freshen.StatusDeclinedDirty, res)
	}
	if res.Behind != 129 {
		t.Errorf("Behind = %d, want 129", res.Behind)
	}
	// It said so.
	if len(*alerts) != 1 {
		t.Fatalf("expected exactly 1 alert for a stale-and-declined workspace, got %d", len(*alerts))
	}
	if ev := findEvent(readEventLines(t, logPath), "agent_workspace_freshened", "pm-dirty"); ev == nil {
		t.Error("declined workspace emitted no event — the durable half of the signal is missing")
	}
	// And it did not touch anything.
	got, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != precious {
		t.Errorf("CLOBBERED uncommitted work: %q", got)
	}
	if after := gitIn(t, repo, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved on a dirty tree: %s -> %s", before, after)
	}
}

// TestFreshenWorkspaceIsSilentWhenAgentHasNoRepo: most crew agents keep no
// repo/ checkout. If absence were loud, every agent start would alert and the
// channel would be muted — which is how the original two-month staleness
// survived unnoticed in the first place.
func TestFreshenWorkspaceIsSilentWhenAgentHasNoRepo(t *testing.T) {
	logPath := useTempEventLog(t)
	alerts := captureAlerts(t)
	t.Setenv("POGO_HOME", t.TempDir())

	res := freshenWorkspace("agent-without-repo")

	if res.Status != freshen.StatusSkipped {
		t.Fatalf("Status = %q, want %q", res.Status, freshen.StatusSkipped)
	}
	if len(*alerts) != 0 {
		t.Errorf("absence of a repo must not alert: %+v", *alerts)
	}
	if _, err := os.Stat(logPath); err == nil {
		if ev := findEvent(readEventLines(t, logPath), "agent_workspace_freshened", "agent-without-repo"); ev != nil {
			t.Error("absence of a repo must not emit an event")
		}
	}
}

// TestFreshenWorkspaceAlertsWhenFreshnessIsUnknown: an unreachable remote must
// alert too. A check that silently passes when it could not run is the exact
// failure mode this ticket exists to remove.
func TestFreshenWorkspaceAlertsWhenFreshnessIsUnknown(t *testing.T) {
	alerts := captureAlerts(t)
	newWorkspace(t, "pm-offline")
	repo := WorkspaceRepoDir("pm-offline")
	gitIn(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	res := freshenWorkspace("pm-offline")

	if res.Status != freshen.StatusFailed {
		t.Fatalf("Status = %q, want %q: %+v", res.Status, freshen.StatusFailed, res)
	}
	if len(*alerts) != 1 {
		t.Errorf("unknown freshness must alert, got %d alerts", len(*alerts))
	}
}

// TestStaleWorkspaceMailNeverHandsOutADestructiveCommand. The dirty case is
// the one where the reader is most likely to paste whatever the mail contains,
// and the one where pasting `git reset --hard` destroys the 83 staged adds
// this whole guard exists to protect. The body must route through a decision,
// not a command.
func TestStaleWorkspaceMailNeverHandsOutADestructiveCommand(t *testing.T) {
	remedy := remedyFor(freshen.Result{Status: freshen.StatusDeclinedDirty})
	for _, bad := range []string{"reset --hard", "checkout -f", "clean -fd", "stash drop"} {
		if strings.Contains(remedy, bad) {
			t.Errorf("dirty-case remedy hands out destructive command %q:\n%s", bad, remedy)
		}
	}
	if !strings.Contains(remedy, "status") {
		t.Errorf("dirty-case remedy must send the reader to look first:\n%s", remedy)
	}
}

// TestFixtureGitLeavesNoDetachedMaintenanceDaemons pins the load reduction
// mg-ea0c turned on, and pins it by observing git's OWN process trace rather
// than by asserting that a config string is present. The config string is the
// mechanism; the absent daemon is the property, and only one of those two is
// what a future git release could change underneath us.
//
// Before this setting, advance(pub, 129) left 130 of these daemons behind — one
// per commit, each forked against a repo the test was still driving in the
// foreground. `gc.auto=0` does not stop them: each daemon still forks and only
// then declines. Both were measured, as was the total: 394 git processes for
// advance(129) before, 9 after.
//
// The suppressed arm drives the REAL fixture — newWorkspace and advance, both
// halves of a push included — rather than a hand-rolled stand-in, because the
// receive-side daemon runs under the bare origin's config and a stand-in that
// only committed locally would never have caught it.
func TestFixtureGitLeavesNoDetachedMaintenanceDaemons(t *testing.T) {
	traces := t.TempDir()

	suppressed := filepath.Join(traces, "suppressed.json")
	t.Setenv("GIT_TRACE2_EVENT", suppressed)
	pub := newWorkspace(t, "pm-trace")
	advance(t, pub, 3)

	if n := countMaintenanceRecords(t, suppressed); n != 0 {
		t.Errorf("the fixture left %d `git maintenance run --auto` records in its trace, want 0 — "+
			"a detached background git daemon is being forked against a repo the test is "+
			"still driving; check fixtureGitArgs and origin's own maintenance.auto", n)
	}

	// POSITIVE CONTROL. A check that cannot go red is not a check. Run the same
	// three commands WITHOUT fixtureGitArgs and require that the daemon does
	// appear, so that this test failing means "the suppression broke" and not
	// "git stopped auto-maintaining, or renamed the trace field, and this
	// assertion has been passing vacuously ever since".
	control := filepath.Join(traces, "control.json")
	t.Setenv("GIT_TRACE2_EVENT", control)
	ctl := t.TempDir()
	writeFile(t, filepath.Join(ctl, "a.txt"), "a\n")
	for _, args := range [][]string{
		{"init", "--initial-branch=main", "."},
		{"add", "a.txt"},
		{"commit", "-m", "c"},
	} {
		cmd := exec.Command("git", args...) // deliberately without fixtureGitArgs
		cmd.Dir = ctl
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("control git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if countMaintenanceRecords(t, control) == 0 {
		t.Error("the positive control spawned no maintenance daemon either, so the " +
			"assertion above proves nothing: git's auto-maintenance behaviour or its " +
			"trace2 argv format has changed and this test needs rewriting, not deleting")
	}
}

// countMaintenanceRecords counts trace2 records naming a detached auto-maintenance
// run. git emits more than one record per spawn, so this is a presence/absence
// measure and not a process count.
func countMaintenanceRecords(t *testing.T, tracePath string) int {
	t.Helper()
	b, err := os.ReadFile(tracePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(b), `"maintenance","run","--auto"`)
}

// TestAdvancePublishesNCommitsAndLeavesACoherentPublisher pins what advance is
// FOR, now that it builds its commits with one fast-import instead of 2n
// subprocesses. The two freshen tests above assert a Behind count; if the
// fixture that produces that count silently stopped producing it — or produced
// it while leaving the publisher reporting phantom deletions — they would still
// pass, or fail for a reason that points at the wrong file.
func TestAdvancePublishesNCommitsAndLeavesACoherentPublisher(t *testing.T) {
	pub := newWorkspace(t, "pm-fixture")
	before := gitIn(t, pub, "rev-list", "--count", "HEAD")

	advance(t, pub, 7)

	if got, want := gitIn(t, pub, "rev-list", "--count", "HEAD"), "8"; got != want {
		t.Errorf("publisher HEAD is %s commits deep after advance(7), want %s (was %s)",
			got, want, before)
	}
	if head, remote := gitIn(t, pub, "rev-parse", "HEAD"), gitIn(t, pub, "rev-parse", "origin/main"); head != remote {
		t.Errorf("origin/main is %s but publisher HEAD is %s — advance did not publish", remote, head)
	}
	// The reset --hard contract. fast-import moves the ref without touching the
	// index or worktree, and a fixture that lies about its own state is how the
	// next investigation gets sent the wrong way.
	if st := gitIn(t, pub, "status", "--porcelain"); st != "" {
		t.Errorf("publisher left incoherent by advance:\n%s", st)
	}
	// And the property both freshen tests actually rest on, restated against the
	// fixture alone: the workspace clone really is N behind.
	if res := freshen.Checkout(WorkspaceRepoDir("pm-fixture")); res.Behind != 7 {
		t.Errorf("workspace measures %d behind after advance(7), want 7: %+v", res.Behind, res)
	}
}

// TestGitFailureForensicsDiscriminatesAbsentFromPresent encodes the finding that
// cost mg-ea0c its investigation, so the next reader gets it from a test instead
// of from a comment.
//
// git reports an ABSENT object and a PRESENT-but-broken one through the same
// `fatal: bad tree object <oid>` line; the only difference is which additional
// error lines accompany it, and for the absent case there are none. That silence
// is the diagnosis. A forensics block that could not tell the two apart would
// have been worth nothing, so this proves it can — in both directions, because
// only the PRESENT arm shows that the ABSENT arm is reporting on evidence rather
// than on a path it never managed to build.
func TestGitFailureForensicsDiscriminatesAbsentFromPresent(t *testing.T) {
	repo := t.TempDir()
	gitIn(t, repo, "init", "--initial-branch=main", ".")
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-m", "c")

	tree := gitIn(t, repo, "rev-parse", "HEAD^{tree}")
	// The shape of the real CI line, so the OID is recovered the same way.
	failure := "fatal: bad tree object " + tree

	present := gitFailureForensics(repo, failure)
	if !strings.Contains(present, tree+": PRESENT") {
		t.Errorf("forensics did not report a present object as PRESENT:\n%s", present)
	}
	if strings.Contains(present, "ABSENT") {
		t.Errorf("forensics called a present object ABSENT:\n%s", present)
	}

	path := filepath.Join(repo, ".git", "objects", tree[:2], tree[2:])
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	absent := gitFailureForensics(repo, failure)
	if !strings.Contains(absent, tree+": ABSENT") {
		t.Errorf("forensics did not report a deleted object as ABSENT — the mg-ea0c "+
			"signature would go unrecognised again:\n%s", absent)
	}
	if !strings.Contains(absent, "external deleter") {
		t.Errorf("forensics reported the absence without naming what it implies:\n%s", absent)
	}

	// AND IT MUST REFUSE TO CONCLUDE WHEN IT CANNOT LOOK. Two fixture call sites
	// run git with dir set somewhere that is not a working checkout — `clone` runs
	// from the parent directory, and the bare origin has no `.git` at all. Probing
	// dir/.git/objects there finds nothing and would report ABSENT: the exact wrong
	// answer, stated confidently, on the one signature this whole block exists to
	// recognise. So a resolved-object-store check gates the probe, and this is the
	// arm that proves the gate is load-bearing.
	notARepo := gitFailureForensics(t.TempDir(), failure)
	if strings.Contains(notARepo, "ABSENT") {
		t.Errorf("forensics concluded ABSENT from a directory that is not a repository:\n%s", notARepo)
	}
	if !strings.Contains(notARepo, "no conclusion is drawn") {
		t.Errorf("forensics did not say it could not look:\n%s", notARepo)
	}

	// The bare origin's objects live at the top level, not under .git.
	bare := t.TempDir()
	gitIn(t, bare, "init", "--bare", "--initial-branch=main", ".")
	bareTree := gitInStdin(t, bare, "", "hash-object", "-w", "-t", "tree", "--stdin")
	bareOut := gitFailureForensics(bare, "fatal: bad tree object "+bareTree)
	if !strings.Contains(bareOut, bareTree+": PRESENT") {
		t.Errorf("forensics missed an object that IS present in a bare repo, so a bare-repo "+
			"failure would be misreported as the mg-ea0c signature:\n%s", bareOut)
	}
}
