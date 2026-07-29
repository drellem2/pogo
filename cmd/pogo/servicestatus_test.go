package main

// End-to-end control for the consumer-facing drift report (mg-75ec).
//
// THE POSITIVE CONTROL IS THE POINT. A status command only ever observed green
// has not been tested: it is indistinguishable from one that prints "clean"
// unconditionally, which is the exact failure this ticket exists to prevent
// somebody else's daemon from having. So every case below stands up a real
// three-way — a real git repo, real vcs-stamped binaries on a real fake GOBIN,
// and a stub pogod self-reporting a revision — and drives the SHIPPED
// `pogo service status` binary end to end, asserting it reports the drift it
// was given.
//
// It also exercises the surface a consumer actually has: no
// scripts/pogo-self-deploy anywhere on the box, and — in the last case — no
// checkout at all.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// driftJSON is the subset of `pogo --json service status` this file asserts on.
type driftJSON struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Drift     *struct {
		Repo     string `json:"repo"`
		RepoNote string `json:"repo_note"`
		Ref      string `json:"ref"`
		Main     string `json:"main"`
		Axes     []struct {
			Name     string `json:"name"`
			Revision string `json:"revision"`
			Path     string `json:"path"`
			Note     string `json:"note"`
		} `json:"axes"`
		Status       string `json:"status"`
		NeedsBuild   bool   `json:"needs_build"`
		NeedsRestart bool   `json:"needs_restart"`
		Action       string `json:"action"`
	} `json:"drift"`
}

// fakeInstall is a synthetic pogo installation: a source checkout, a GOBIN
// holding vcs-stamped pogod/pogo binaries, and the two revisions involved.
type fakeInstall struct {
	repo     string // a git checkout whose go.mod claims the pogo module
	gobin    string // where the "installed" binaries live
	builtRev string // the commit the installed binaries were built from
	headRev  string // the checkout's main HEAD
}

// newFakeInstall builds binaries at commit 1, then advances main to commit 2,
// so the installed binaries are genuinely, verifiably behind main.
func newFakeInstall(t *testing.T) fakeInstall {
	t.Helper()
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s available to build a stamped installation with", tool)
		}
	}
	fi := fakeInstall{repo: t.TempDir(), gobin: t.TempDir()}

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fi.repo, name), []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		full := append([]string{"-C", fi.repo,
			"-c", "user.email=drift@test", "-c", "user.name=drift test",
			"-c", "commit.gpgsign=false"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// go.mod must claim the pogo module: ResolveRepo reads it, not the
	// directory name, so a path merely CALLED pogo cannot anchor the compare.
	write("go.mod", "module github.com/drellem2/pogo\n\ngo 1.25\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "v1")
	fi.builtRev = git("rev-parse", "HEAD")

	// Build once at v1 and install it under both deployed names: the daemon
	// and the CLI move together, so the check reads both.
	staged := filepath.Join(t.TempDir(), "built")
	build := exec.Command("go", "build", "-buildvcs=true", "-o", staged, ".")
	build.Dir = fi.repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pogod", "pogo"} {
		if err := os.WriteFile(filepath.Join(fi.gobin, name), data, 0755); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}

	// Advance main PAST the commit the binaries were built from. This is the
	// drift: a merge landed and nothing rebuilt or restarted anything.
	write("main.go", "package main\n\nfunc main() { _ = 1 }\n")
	git("add", ".")
	git("commit", "-q", "-m", "v2")
	fi.headRev = git("rev-parse", "HEAD")

	if fi.builtRev == fi.headRev {
		t.Fatalf("fixture did not advance main past the built revision")
	}
	return fi
}

// runStatus runs the shipped CLI's `service status` against a stub pogod whose
// /version self-reports runningRev (empty means: serve nothing, i.e. the
// daemon is down). extraEnv overrides the fixture defaults.
func runStatus(t *testing.T, fi fakeInstall, runningRev string, extraEnv []string, args ...string) (driftJSON, string, int) {
	t.Helper()

	var port int
	if runningRev != "" {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/version" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"revision":%q,"go_version":"go1.25.0"}`, runningRev)
		}))
		t.Cleanup(ts.Close)
		port = ts.Listener.Addr().(*net.TCPAddr).Port
	} else {
		// Bind and immediately release a port so the CLI probes something that
		// is definitively not listening — a down daemon, not a random guess.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port = l.Addr().(*net.TCPAddr).Port
		l.Close()
	}

	full := append([]string{"--json", "service", "status"}, args...)
	cmd := exec.Command(pogoBin, full...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("POGO_PORT=%d", port),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"POGO_HOME=",
		"POGO_GOBIN="+fi.gobin,
		"POGO_REPO=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	// Run from a directory that is not any checkout, so a resolved repo can
	// only have come from --repo or $POGO_REPO.
	cmd.Dir = t.TempDir()

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v\nstderr: %s", pogoBin, err, errBuf.String())
		}
		code = ee.ExitCode()
	}
	var parsed driftJSON
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("decoding %q: %v\nstderr: %s", out.String(), err, errBuf.String())
	}
	return parsed, out.String(), code
}

// axis returns a named axis from the decoded report.
func (d driftJSON) axis(t *testing.T, name string) (rev, path, note string) {
	t.Helper()
	if d.Drift == nil {
		t.Fatalf("no drift report in output")
	}
	for _, a := range d.Drift.Axes {
		if a.Name == name {
			return a.Revision, a.Path, a.Note
		}
	}
	t.Fatalf("no axis %q in %+v", name, d.Drift.Axes)
	return "", "", ""
}

// TestServiceStatus_ReportsDrift is THE positive control: a real installation
// that is genuinely behind main, driven through the shipped command, must come
// back as drift with both halves owed.
func TestServiceStatus_ReportsDrift(t *testing.T) {
	fi := newFakeInstall(t)
	got, raw, code := runStatus(t, fi, fi.builtRev, nil, "--repo", fi.repo)

	if got.Drift == nil {
		t.Fatalf("service status reported no drift section at all:\n%s", raw)
	}
	if got.Drift.Status != "drift" {
		t.Errorf("status = %q, want drift\naction: %s", got.Drift.Status, got.Drift.Action)
	}
	if !got.Drift.NeedsBuild || !got.Drift.NeedsRestart {
		t.Errorf("needs_build=%v needs_restart=%v, want both true\naction: %s",
			got.Drift.NeedsBuild, got.Drift.NeedsRestart, got.Drift.Action)
	}
	if !strings.Contains(got.Drift.Action, "BUILD + RESTART owed") {
		t.Errorf("action does not own up to both halves: %q", got.Drift.Action)
	}
	if got.Drift.Main != fi.headRev {
		t.Errorf("main = %q, want %q", got.Drift.Main, fi.headRev)
	}

	// The axes must carry the real, independently-derivable revisions — this
	// is what makes the verdict re-checkable instead of re-investigable.
	if rev, _, _ := got.axis(t, "running pogod"); rev != fi.builtRev {
		t.Errorf("running pogod = %q, want the revision the stub daemon reported %q", rev, fi.builtRev)
	}
	for _, name := range []string{"installed pogod", "installed pogo"} {
		rev, path, note := got.axis(t, name)
		if rev != fi.builtRev {
			t.Errorf("%s = %q, want the vcs stamp %q read out of the binary", name, rev, fi.builtRev)
		}
		if path != filepath.Join(fi.gobin, strings.TrimPrefix(name, "installed ")) {
			t.Errorf("%s path = %q, want it under the fake GOBIN", name, path)
		}
		if note != "" {
			t.Errorf("%s should be a plain in-repo revision, got note %q", name, note)
		}
	}

	// Report-only: the exit status must stay 0 so existing `pogo service
	// status` callers are not broken by this becoming a gate.
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (the command reports, it does not gate)", code)
	}
}

// TestServiceStatus_ReportsClean is the negative control for the control: the
// same machinery, with the daemon and binaries actually at main HEAD, must not
// cry drift. Without this the drift assertion above proves only that the
// command says "drift" all the time.
func TestServiceStatus_ReportsClean(t *testing.T) {
	fi := newFakeInstall(t)

	// Rewind main onto the commit the binaries were built from, so all three
	// axes genuinely agree.
	out, err := exec.Command("git", "-C", fi.repo, "reset", "--hard", "-q", fi.builtRev).CombinedOutput()
	if err != nil {
		t.Fatalf("git reset: %v\n%s", err, out)
	}

	got, raw, code := runStatus(t, fi, fi.builtRev, nil, "--repo", fi.repo)
	if got.Drift == nil {
		t.Fatalf("no drift section:\n%s", raw)
	}
	if got.Drift.Status != "clean" {
		t.Errorf("status = %q, want clean\naction: %s", got.Drift.Status, got.Drift.Action)
	}
	if got.Drift.NeedsBuild || got.Drift.NeedsRestart {
		t.Errorf("clean install owes work: build=%v restart=%v", got.Drift.NeedsBuild, got.Drift.NeedsRestart)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// TestServiceStatus_DaemonDownIsItsOwnFinding: "nothing is running" must not be
// reported as "running something stale". They send the reader to different
// places.
func TestServiceStatus_DaemonDownIsItsOwnFinding(t *testing.T) {
	fi := newFakeInstall(t)
	got, raw, _ := runStatus(t, fi, "", nil, "--repo", fi.repo)
	if got.Drift == nil {
		t.Fatalf("no drift section:\n%s", raw)
	}
	if !strings.Contains(got.Drift.Action, "NOT RUNNING") {
		t.Errorf("action does not say the daemon is down: %q", got.Drift.Action)
	}
	rev, path, _ := got.axis(t, "running pogod")
	if rev != "<unreachable>" {
		t.Errorf("running axis = %q, want <unreachable>", rev)
	}
	if !strings.Contains(path, "/version") {
		t.Errorf("running axis does not name what was probed: %q", path)
	}
}

// TestServiceStatus_NoCheckoutStillFindsDrift is the consumer's real situation:
// no pogo source anywhere, no scripts/pogo-self-deploy, nothing to compare
// against main. Two of three axes are still measurable, and a daemon running
// code that `go install` already replaced on disk is drift that needs no repo.
func TestServiceStatus_NoCheckoutStillFindsDrift(t *testing.T) {
	fi := newFakeInstall(t)

	// The daemon self-reports main HEAD while the binaries on disk are the
	// older build — i.e. the binary under the running daemon was swapped. No
	// --repo, no POGO_REPO, cwd outside any checkout.
	got, raw, _ := runStatus(t, fi, fi.headRev, nil)
	if got.Drift == nil {
		t.Fatalf("no drift section:\n%s", raw)
	}
	if got.Drift.Status != "drift" {
		t.Errorf("status = %q, want drift with two axes\naction: %s", got.Drift.Status, got.Drift.Action)
	}
	if !got.Drift.NeedsRestart || got.Drift.NeedsBuild {
		t.Errorf("restart=%v build=%v, want restart only (main is not measurable)", got.Drift.NeedsRestart, got.Drift.NeedsBuild)
	}
	if got.Drift.Main != "" {
		t.Errorf("main = %q, want it reported as unavailable", got.Drift.Main)
	}
	// The absent axis must be EXPLAINED, not silently blank.
	for _, frag := range []string{"--repo", "POGO_REPO"} {
		if !strings.Contains(got.Drift.RepoNote, frag) {
			t.Errorf("repo_note %q does not tell the reader how to supply the third axis (%s)", got.Drift.RepoNote, frag)
		}
	}
}

// TestServiceStatus_PlainTextNamesEveryAxis: the default (non-JSON) output is
// what a human actually runs. It must print all three axes and the verdict —
// a report that only --json can see is not a shipped surface.
func TestServiceStatus_PlainTextNamesEveryAxis(t *testing.T) {
	fi := newFakeInstall(t)
	cmd := exec.Command(pogoBin, "service", "status", "--repo", fi.repo)
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir(), "POGO_HOME=",
		"POGO_GOBIN="+fi.gobin, "POGO_REPO=", "POGO_PORT=1")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("service status: %v\n%s", err, out)
	}
	text := string(out)
	for _, frag := range []string{
		"revision drift", "running pogod", "installed pogod", "installed pogo",
		"main HEAD", "status", "action", fi.repo, fi.gobin, fi.headRev,
	} {
		if !strings.Contains(text, frag) {
			t.Errorf("plain output omits %q:\n%s", frag, text)
		}
	}
}

// TestServiceStatus_NoDriftFlagKeepsTheOldContract: --no-drift restores the
// original one-line answer, for a caller that only ever wanted to know whether
// the plist is installed and does not want to pay for an HTTP probe.
func TestServiceStatus_NoDriftFlagKeepsTheOldContract(t *testing.T) {
	fi := newFakeInstall(t)
	got, raw, code := runStatus(t, fi, fi.builtRev, nil, "--no-drift", "--repo", fi.repo)
	if got.Drift != nil {
		t.Errorf("--no-drift still ran the check: %s", raw)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(raw, `"installed"`) {
		t.Errorf("--no-drift dropped the service-installed answer: %s", raw)
	}
}
