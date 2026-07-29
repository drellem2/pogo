package service

import (
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

// TestRenderDeployPlistSchedulesOffHours pins 03:00. A redeploy bounces every
// agent on the box; the hour is not a preference, it is the reason the job is
// tolerable at all. It also pins that the schedule is a StartCalendarInterval
// rather than a StartInterval — an interval job fires N seconds after load
// regardless of wall-clock time, which is how a "nightly" ends up running at
// 14:30 on the day somebody reinstalls it.
func TestRenderDeployPlistSchedulesOffHours(t *testing.T) {
	rendered, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	if data.Hour != 3 || data.Minute != 0 {
		t.Errorf("schedule = %02d:%02d; want 03:00 (the off-hours window disruptive fleet ops were moved into)", data.Hour, data.Minute)
	}
	if !strings.Contains(rendered, "<key>StartCalendarInterval</key>") {
		t.Error("plist has no StartCalendarInterval — a wall-clock schedule is what makes this off-hours")
	}
	if strings.Contains(rendered, "<key>StartInterval</key>") {
		t.Error("plist uses StartInterval: that fires relative to load time, not the clock, so the 'nightly' would drift into the working day")
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

// TestDeployZshEnvFindsTheTokenFile covers the second reason this job can run
// nightly and deploy nothing (mg-36e3). The runner treats a missing GH_TOKEN as
// a hard abort, and reads it from ONE file. ~/.zshenv is the principled default
// — the only zsh init file a non-interactive shell sources — but on a real box
// the export routinely lives in ~/.zshrc, and then the nightly alerts every
// night about a token that was on disk the whole time.
//
// Preference order matters as much as detection: when .zshenv does define the
// token it must win, because that is the file that works in every context.
func TestDeployZshEnvFindsTheTokenFile(t *testing.T) {
	tokenLine := "export GH_TOKEN=sometoken\n"

	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "token in .zshrc only — the observed case",
			files: map[string]string{".zshenv": ". \"$HOME/.cargo/env\"\n", ".zshrc": "# rc\n" + tokenLine},
			want:  ".zshrc",
		},
		{
			name:  ".zshenv wins when it also defines the token",
			files: map[string]string{".zshenv": tokenLine, ".zshrc": tokenLine},
			want:  ".zshenv",
		},
		{
			name:  "indented export is still an export",
			files: map[string]string{".zshenv": "# nothing\n", ".zshrc": "  export GH_TOKEN=x\n"},
			want:  ".zshrc",
		},
		{
			name:  "a commented-out export does not count",
			files: map[string]string{".zshenv": "# export GH_TOKEN=old\n", ".zshrc": tokenLine},
			want:  ".zshrc",
		},
		{
			name:  "falls back to .zshenv when nothing defines it",
			files: map[string]string{".zshenv": "# nothing\n", ".zshrc": "# nothing\n"},
			want:  ".zshenv",
		},
		{
			name:  "token in .zprofile",
			files: map[string]string{".zprofile": tokenLine},
			want:  ".zprofile",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			got := deployZshEnv()
			if got != filepath.Join(home, tc.want) {
				t.Errorf("deployZshEnv() = %q; want %s", got, tc.want)
			}
		})
	}
}

// TestRenderDeployPlistBindsZshEnvPathNotSecret pins that the plist carries the
// PATH to the token file and never the token. The whole reason GH_TOKEN is read
// at run time is that ~/Library/LaunchAgents is world-readable; binding the file
// location is what makes that indirection work on a box whose token is not in
// the default file, and it must not become a shortcut to binding the value.
func TestRenderDeployPlistBindsZshEnvPathNotSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export GH_TOKEN=supersecretvalue\n"), 0644); err != nil {
		t.Fatalf("write .zshrc: %v", err)
	}

	rendered, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	if data.ZshEnv != filepath.Join(home, ".zshrc") {
		t.Errorf("ZshEnv = %q; want the file that actually defines the token", data.ZshEnv)
	}
	if !strings.Contains(rendered, "<key>POGO_DEPLOY_ZSHENV</key>") {
		t.Error("plist does not bind POGO_DEPLOY_ZSHENV: the runner would fall back to ~/.zshenv and abort nightly on a token it could have found")
	}
	if strings.Contains(rendered, "supersecretvalue") {
		t.Error("rendered plist contains the TOKEN VALUE — a world-readable LaunchAgent must hold only the path")
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
