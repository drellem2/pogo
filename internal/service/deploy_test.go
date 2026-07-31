package service

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderDeployPlistNeverRunsAtLoad pins the key that separates "install the
// nightly" from "bounce the fleet right now". com.pogo.recovery sets
// RunAtLoad=true because draining a leftover queue at login is harmless; the
// same value here would mean every install, every reinstall, and every login
// triggered a full rebuild + fleet-wide restart as a side effect of touching
// the plist. It must be false, and it must stay false.
func TestRenderDeployPlistNeverRunsAtLoad(t *testing.T) {
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	if !strings.Contains(rendered, "<key>RunAtLoad</key>\n    <false/>") {
		t.Error("RunAtLoad is not false: installing or reloading com.pogo.deploy would bounce the fleet as a side effect")
	}
	if strings.Contains(rendered, "<key>KeepAlive</key>\n    <true/>") {
		t.Error("KeepAlive is true: the runner exits on every no-drift night, so launchd would relaunch it in a loop")
	}
}

// TestRenderDeployPlistHasNoSecrets is the one assertion that cannot be
// recovered after the fact. ~/Library/LaunchAgents is world-readable (0644, in
// a directory every local process can traverse), so a token written here is a
// token disclosed to everything on the box — and it stays disclosed until
// somebody notices and rotates it. The runner sources GH_TOKEN from ~/.zshenv
// at run time precisely so this file never has to hold one.
func TestRenderDeployPlistHasNoSecrets(t *testing.T) {
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	for _, forbidden := range []string{"GH_TOKEN", "OPENAI_API_KEY", "AWS_SECRET", "GITHUB_CLIENT_SECRET"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("rendered plist mentions %s — secrets must never be written to a world-readable LaunchAgent", forbidden)
		}
	}
}

// TestRenderDeployPlistSchedulesOffHours pins 03:00 and the two retry fires
// behind it. A redeploy bounces every agent on the box; the hours are not a
// preference, they are the reason the job is tolerable at all. It also pins
// that the schedule is a StartCalendarInterval rather than a StartInterval — an
// interval job fires N seconds after load regardless of wall-clock time, which
// is how a "nightly" ends up running at 14:30 on the day somebody reinstalls it.
//
// Every hour has to sit inside pogo-deploy.sh's own window guard (2-6). A fire
// scheduled outside it is dropped by the runner on arrival, so a plist and a
// guard that disagree produce a job that appears scheduled and never deploys —
// the failure the guard's own tests exist to keep distinguishable.
func TestRenderDeployPlistSchedulesOffHours(t *testing.T) {
	rendered, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	want := []int{3, 4, 5}
	if len(data.Hours) != len(want) {
		t.Fatalf("schedule has %d fires (%v); want %v — 03:00 plus the mg-8f7e retries", len(data.Hours), data.Hours, want)
	}
	for i, h := range want {
		if data.Hours[i] != h {
			t.Errorf("fire %d = %02d:00; want %02d:00", i, data.Hours[i], h)
		}
	}
	if data.Minute != 0 {
		t.Errorf("minute = %d; want 0", data.Minute)
	}
	// The window guard is half-open [2,6): every fire must be >= 2 and < 6.
	for _, h := range data.Hours {
		if h < 2 || h >= 6 {
			t.Errorf("fire at %02d:00 falls outside pogo-deploy.sh's window guard [2,6) — launchd would fire it and the runner would drop it, so the job would look scheduled and deploy nothing", h)
		}
	}
	// Ascending order is load-bearing: next_fire_hour in the runner walks the
	// list and returns the first entry after the current hour, so an unsorted
	// list makes the RED alert promise a retry that already happened.
	for i := 1; i < len(data.Hours); i++ {
		if data.Hours[i] <= data.Hours[i-1] {
			t.Errorf("fire hours %v are not strictly ascending — the runner's next_fire_hour walk assumes they are", data.Hours)
		}
	}
	if !strings.Contains(rendered, "<key>StartCalendarInterval</key>") {
		t.Error("plist has no StartCalendarInterval — a wall-clock schedule is what makes this off-hours")
	}
	if strings.Contains(rendered, "<key>StartInterval</key>") {
		t.Error("plist uses StartInterval: that fires relative to load time, not the clock, so the 'nightly' would drift into the working day")
	}
	// The retries only exist if launchd is actually given more than one fire.
	// A <dict> here renders one schedule and silently drops the rest.
	if strings.Count(rendered, "<key>Hour</key>") != len(want) {
		t.Errorf("plist renders %d Hour keys; want %d — StartCalendarInterval must be an <array> of dicts for launchd to schedule every fire",
			strings.Count(rendered, "<key>Hour</key>"), len(want))
	}
}

// TestRenderDeployPlistIsWellFormedXML is the check that the template edit did
// not break the file for launchd. The plist is assembled by string template, so
// a mismatched or unclosed tag compiles, renders, installs, and is then rejected
// by launchctl at bootstrap — leaving a job that is "installed" and never fires.
// mg-8f7e turned a single <dict> schedule into an <array> of them by hand, which
// is exactly the edit that gets this wrong.
func TestRenderDeployPlistIsWellFormedXML(t *testing.T) {
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	dec := xml.NewDecoder(strings.NewReader(rendered))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("rendered plist is not well-formed XML: %v\n\n%s", err, rendered)
		}
	}
}

// TestRenderDeployPlistBindsDeploySrc pins the anti-clobber invariant. The
// runner defaults POGO_DEPLOY_SRC to $HOME/.pogo/deploy-src on its own, so an
// unbound plist looks harmless — right up until POGO_HOME points somewhere
// else, at which point the two disagree about which tree is being built. Bind
// it here and the plist and the script cannot drift apart.
//
// It must also never name the developer's working tree: ~/dev/pogo is a place a
// human edits, and a 03:00 fetch/checkout/merge there can land on a
// half-finished change.
func TestRenderDeployPlistBindsDeploySrc(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("POGO_HOME", custom)
	os.Unsetenv("POGO_DEPLOY_SRC")

	rendered, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	want := filepath.Join(custom, "deploy-src")
	if data.DeploySrc != want {
		t.Errorf("DeploySrc = %q; want %q", data.DeploySrc, want)
	}
	if !strings.Contains(rendered, "<key>POGO_DEPLOY_SRC</key>") {
		t.Error("rendered plist does not export POGO_DEPLOY_SRC; the script's own default could name a different tree")
	}
	if strings.Contains(rendered, "/dev/pogo") {
		t.Error("rendered plist names a dev working tree as the build source — the nightly must build from a dedicated checkout it alone writes to")
	}
}

// TestRenderDeployPlistPathResolvesGoAndTools is the 07-23 regression. That
// manual redeploy failed outright with "go: command not found" because the
// job's PATH lacked Homebrew, and separately gh calls fail unauthenticated
// without the tools on ~/go/bin. launchd hands a job a minimal PATH; every
// binary this deploy needs has to be named here or it is not there.
func TestRenderDeployPlistPathResolvesGoAndTools(t *testing.T) {
	_, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	for _, dir := range []string{"/opt/homebrew/bin", "go/bin"} {
		if !strings.Contains(data.Path, dir) {
			t.Errorf("PATH %q omits %s", data.Path, dir)
		}
	}
	// Same helper the daemon and recovery plists use, so all three agree by
	// construction rather than by three hand-maintained copies.
	if data.Path != launchdPath() {
		t.Errorf("PATH diverges from launchdPath(): %q vs %q", data.Path, launchdPath())
	}
}

// TestDeployIsNotRecovery pins the separation mg-cf48 declined to collapse.
// Two labels, two plists, two logs: if the deploy ever shared com.pogo.recovery's
// label it would replace the tier-3 safety net on install, and the mechanism
// you reach for when the box is already broken would start rebuilding from main.
func TestDeployIsNotRecovery(t *testing.T) {
	if deployLabel == recoveryLabel {
		t.Fatalf("deploy and recovery share the label %q", deployLabel)
	}
	if deployPlistPath() == recoveryPlistPath() {
		t.Errorf("deploy and recovery share a plist path: %s", deployPlistPath())
	}
	if deployScriptInstallPath() == recoveryScriptInstallPath() {
		t.Errorf("deploy and recovery share a script path: %s", deployScriptInstallPath())
	}
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	// The recovery job is edge-triggered on a queue directory; the deploy job
	// is not, and must not be. A WatchPaths deploy would rebuild pogod every
	// time a polecat asked for a restart.
	if strings.Contains(rendered, "<key>WatchPaths</key>") {
		t.Error("deploy plist has WatchPaths: a file event must not be able to trigger a rebuild")
	}
}

// TestFindDeployScriptSourceHonorsOverride keeps the installer testable and
// gives an operator a way out when the bundled copy cannot be located (a `go
// install`ed pogo has no scripts/ sibling).
func TestFindDeployScriptSourceHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pogo-deploy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("POGO_DEPLOY_SCRIPT", script)

	got, err := findDeployScriptSource()
	if err != nil {
		t.Fatalf("findDeployScriptSource: %v", err)
	}
	if got != script {
		t.Errorf("findDeployScriptSource() = %q; want the override %q", got, script)
	}
}
