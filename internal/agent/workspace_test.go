package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
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

func advance(t *testing.T, publisher string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		f := fmt.Sprintf("f%d.txt", i)
		writeFile(t, filepath.Join(publisher, f), "x\n")
		gitIn(t, publisher, "add", f)
		gitIn(t, publisher, "commit", "-m", "c"+f)
	}
	gitIn(t, publisher, "push", "origin", "main")
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
