package service

// Tests for the custom-launcher guard, the plist backup and the daemon drift
// classification (drellem2/pogo#105).
//
// The reported host runs pogod through a wrapper, pogod-launch.sh, that injects
// agent credentials before exec'ing pogod. `pogo service install` always wrote
// pogod's PATH location into ProgramArguments, so re-installing silently
// replaced the wrapper and agents lost their credentials. Every fixture below is
// rendered through the shipped template with the wrapper as its program, which
// is the plist that host has.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// launcherSandbox points HOME, XDG_CONFIG_HOME and PATH at a temp tree that
// holds an executable fake pogod and an executable fake wrapper, and clears
// POGOD_LAUNCHER. It returns the paths of both programs.
func launcherSandbox(t *testing.T) (home, pogod, wrapper string) {
	t.Helper()
	home = t.TempDir()
	// Resolve symlinks: findPogod returns an absolute path built from PATH,
	// and on macOS t.TempDir lives under /var -> /private/var.
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	pogod = filepath.Join(bin, "pogod")
	wrapper = filepath.Join(home, ".pogo", "bin", "pogod-launch.sh")
	for _, p := range []string{pogod, wrapper} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv(LauncherEnv, "")
	return home, pogod, wrapper
}

// installedDaemonPlist writes a com.pogo.daemon plist that execs program to the
// sandbox's LaunchAgents and returns its bytes.
func installedDaemonPlist(t *testing.T, program string) []byte {
	t.Helper()
	body, _, err := renderLaunchdPlistFor(program)
	if err != nil {
		t.Fatal(err)
	}
	p := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return []byte(body)
}

func writeServiceConfig(t *testing.T, home, launcher string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "pogo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "[service]\nlauncher = \"" + launcher + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLauncherOrder(t *testing.T) {
	home, pogod, wrapper := launcherSandbox(t)

	l, err := resolveLauncher()
	if err != nil || l.Path != pogod || l.Source != launcherSourcePath {
		t.Fatalf("no override: got %+v, %v; want pogod on PATH %s", l, err, pogod)
	}

	writeServiceConfig(t, home, wrapper)
	l, err = resolveLauncher()
	if err != nil || l.Path != wrapper || l.Source != launcherSourceConfig {
		t.Fatalf("[service] launcher: got %+v, %v; want %s from config", l, err, wrapper)
	}

	other := filepath.Join(home, "other-launch.sh")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(LauncherEnv, other)
	l, err = resolveLauncher()
	if err != nil || l.Path != other || l.Source != launcherSourceEnv {
		t.Fatalf("%s set: got %+v, %v; want it to outrank config", LauncherEnv, l, err)
	}
}

func TestResolveLauncherRejectsMissingAndNonExecutable(t *testing.T) {
	home, _, _ := launcherSandbox(t)

	t.Setenv(LauncherEnv, filepath.Join(home, "nope.sh"))
	if _, err := resolveLauncher(); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing launcher: err = %v; want a 'does not exist' refusal", err)
	}

	plain := filepath.Join(home, "plain.sh")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(LauncherEnv, plain)
	if _, err := resolveLauncher(); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("non-executable launcher: err = %v; want a 'not executable' refusal", err)
	}
}

// TestLauncherOverrideIsRenderedIntoProgramArguments: the override reaches the
// plist launchd actually reads, and the systemd unit's ExecStart.
func TestLauncherOverrideIsRenderedIntoProgramArguments(t *testing.T) {
	home, pogod, wrapper := launcherSandbox(t)

	// Positive control: with no override both renderings exec pogod, so the
	// assertions below are reading the field the override changes.
	unit, err := renderSystemdUnit()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, "\nExecStart="+pogod+"\n") {
		t.Fatalf("no override: unit does not exec pogod %q:\n%s", pogod, unit)
	}

	writeServiceConfig(t, home, wrapper)

	rendered, _, err := renderLaunchdPlist()
	if err != nil {
		t.Fatal(err)
	}
	if got := plistProgram([]byte(rendered)); got != wrapper {
		t.Errorf("ProgramArguments[0] = %q; want the configured launcher %q", got, wrapper)
	}

	unit, err = renderSystemdUnit()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, "\nExecStart="+wrapper+"\n") {
		t.Errorf("systemd unit does not exec the configured launcher %q:\n%s", wrapper, unit)
	}
}

// TestChooseInstallLauncher covers the guard as a pure decision.
func TestChooseInstallLauncher(t *testing.T) {
	_, pogod, wrapper := launcherSandbox(t)
	resolved := launcher{Path: pogod, Source: launcherSourcePath}
	plistPath := launchdPlistPath()

	wrapped, _, _ := renderLaunchdPlistFor(wrapper)
	plain, _, _ := renderLaunchdPlistFor(pogod)

	if _, err := chooseInstallLauncher([]byte(wrapped), resolved, pogod, plistPath, InstallOptions{}); err == nil {
		t.Fatal("a wrapper plist was accepted without a flag: this is #105")
	} else {
		for _, want := range []string{wrapper, plistPath, "--adopt-launcher", "--force-launcher", "[service] launcher", "Nothing was changed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal does not name %q:\n%s", want, err)
			}
		}
	}

	if l, err := chooseInstallLauncher([]byte(wrapped), resolved, pogod, plistPath, InstallOptions{AdoptLauncher: true}); err != nil || l.Path != wrapper {
		t.Errorf("--adopt-launcher: got %+v, %v; want the installed wrapper carried forward", l, err)
	}
	if l, err := chooseInstallLauncher([]byte(wrapped), resolved, pogod, plistPath, InstallOptions{ForceLauncher: true}); err != nil || l.Path != pogod {
		t.Errorf("--force-launcher: got %+v, %v; want pogod", l, err)
	}
	if _, err := chooseInstallLauncher([]byte(wrapped), resolved, pogod, plistPath, InstallOptions{ForceLauncher: true, AdoptLauncher: true}); err == nil {
		t.Error("both flags accepted; they contradict each other")
	}

	// The configured launcher matching the installed one is not a refusal.
	if l, err := chooseInstallLauncher([]byte(wrapped), launcher{Path: wrapper, Source: launcherSourceConfig}, pogod, plistPath, InstallOptions{}); err != nil || l.Path != wrapper {
		t.Errorf("installed == configured launcher: got %+v, %v; want accepted", l, err)
	}

	// Positive control: the same fixture with pogod's path, and no plist at all.
	if l, err := chooseInstallLauncher([]byte(plain), resolved, pogod, plistPath, InstallOptions{}); err != nil || l.Path != pogod {
		t.Errorf("pogod plist: got %+v, %v; want accepted", l, err)
	}
	if l, err := chooseInstallLauncher(nil, resolved, pogod, plistPath, InstallOptions{}); err != nil || l.Path != pogod {
		t.Errorf("no plist: got %+v, %v; want accepted", l, err)
	}
}

// stubInstall replaces the three seams installLaunchd reaches the world
// through. The orchestrated sequence is never run: the stub records the steps
// it was handed and returns an error, so a regression in the guard reaches a
// recorder, not launchd or the live pogod. The read-only launchctl queries
// outside the sequence (the fast-path `list`, the failure mail's `print`) get a
// canned "not loaded" answer, so the tests do not read the host's launchd
// either.
type stubInstall struct {
	ran       bool
	steps     installSteps
	mails     []string
	launchctl [][]string
}

func stubInstallSeams(t *testing.T) *stubInstall {
	t.Helper()
	s := &stubInstall{}
	prevRun, prevMail, prevRead := runInstallSequence, installMailer, launchctlRead
	runInstallSequence = func(steps installSteps) (orchestrationRestore, error) {
		s.ran = true
		s.steps = steps
		return orchestrationRestore{}, errors.New("stub: orchestrated install not run in tests")
	}
	installMailer = func(subject, body string) { s.mails = append(s.mails, subject+"\n"+body) }
	launchctlRead = func(args ...string) ([]byte, error) {
		s.launchctl = append(s.launchctl, args)
		return []byte("Could not find service \"" + launchdLabel + "\" in domain for port\n"), errors.New("stub: exit status 113")
	}
	t.Cleanup(func() { runInstallSequence, installMailer, launchctlRead = prevRun, prevMail, prevRead })
	return s
}

// launchctlVerbs lists the launchctl subcommands the stub was asked for.
func (s *stubInstall) launchctlVerbs() []string {
	var verbs []string
	for _, a := range s.launchctl {
		if len(a) > 0 {
			verbs = append(verbs, a[0])
		}
	}
	return verbs
}

// TestInstallRefusesAWrapperPlistBeforeTouchingAnything drives the real
// installLaunchd against a wrapper plist: it must refuse before the
// orchestrated sequence (which is where the crew quiesce happens), leave the
// plist byte-identical, and write no backup.
func TestInstallRefusesAWrapperPlistBeforeTouchingAnything(t *testing.T) {
	_, _, wrapper := launcherSandbox(t)
	stub := stubInstallSeams(t)
	before := installedDaemonPlist(t, wrapper)

	err := installLaunchd(InstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "refusing to install") {
		t.Fatalf("installLaunchd err = %v; want a refusal", err)
	}
	if stub.ran {
		t.Fatal("the orchestrated install ran (crew quiesce included) before the refusal")
	}
	after, _ := os.ReadFile(launchdPlistPath())
	if string(after) != string(before) {
		t.Error("the refused install changed the plist")
	}
	if baks, _ := filepath.Glob(launchdPlistPath() + ".bak*"); len(baks) != 0 {
		t.Errorf("a refused install wrote backups: %v", baks)
	}
	if len(stub.mails) != 1 || !strings.Contains(stub.mails[0], "untouched") {
		t.Errorf("want one failure mail saying orchestration was untouched; got %q", stub.mails)
	}
	// The refusal precedes the fast-path `list`; only the mail's `print` runs,
	// and it runs through the seam.
	if got := strings.Join(stub.launchctlVerbs(), ","); got != "print" {
		t.Errorf("launchctl reads through the seam = %q; want \"print\"", got)
	}
}

// TestInstallPositiveControlProceedsAndBacksUp is the same drive with the
// installed plist naming pogod (an older rendering, so it differs): the guard
// passes, the sequence is reached, and its writePlist step keeps the previous
// bytes in a .bak.
func TestInstallPositiveControlProceedsAndBacksUp(t *testing.T) {
	_, pogod, _ := launcherSandbox(t)
	stub := stubInstallSeams(t)
	prevNow := backupNow
	backupNow = func() time.Time { return time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { backupNow = prevNow })

	before := installedDaemonPlist(t, pogod)
	stale := strings.Replace(string(before), "    <key>ProcessType</key>\n    <string>Interactive</string>\n", "", 1)
	if stale == string(before) {
		t.Fatal("fixture: could not strip ProcessType from the rendered plist")
	}
	if err := os.WriteFile(launchdPlistPath(), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	_ = installLaunchd(InstallOptions{})
	if !stub.ran {
		t.Fatal("a plist naming pogod was refused: the guard fires on the case it must pass")
	}
	// Both read-only launchctl queries went to the stub, not the host: the
	// fast-path `list` and, because the stubbed sequence fails, the mail's
	// `print`.
	if got := strings.Join(stub.launchctlVerbs(), ","); got != "list,print" {
		t.Errorf("launchctl reads through the seam = %q; want \"list,print\"", got)
	}
	if err := stub.steps.writePlist(); err != nil {
		t.Fatalf("writePlist: %v", err)
	}
	bak := launchdPlistPath() + ".bak.2026-10-01T070000Z"
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("no backup at %s: %v", bak, err)
	}
	if string(got) != stale {
		t.Error("backup does not hold the previous plist's bytes")
	}
	now, _ := os.ReadFile(launchdPlistPath())
	if string(now) != string(before) {
		t.Error("writePlist did not write the rendered plist")
	}
}

// TestInstallAdoptLauncherCarriesTheWrapperForward: --adopt-launcher reaches
// the sequence with the wrapper still in ProgramArguments.
func TestInstallAdoptLauncherCarriesTheWrapperForward(t *testing.T) {
	_, _, wrapper := launcherSandbox(t)
	stub := stubInstallSeams(t)
	before := installedDaemonPlist(t, wrapper)
	// Make it differ so writePlist has something to write.
	if err := os.WriteFile(launchdPlistPath(), append(before, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	_ = installLaunchd(InstallOptions{AdoptLauncher: true})
	if !stub.ran {
		t.Fatal("--adopt-launcher still refused")
	}
	if err := stub.steps.writePlist(); err != nil {
		t.Fatal(err)
	}
	now, _ := os.ReadFile(launchdPlistPath())
	if got := plistProgram(now); got != wrapper {
		t.Errorf("ProgramArguments[0] after --adopt-launcher = %q; want %q", got, wrapper)
	}
}

func TestWritePlistWithBackupNoOpWhenUnchanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(p, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	if bak, err := writePlistWithBackup(p, []byte("same"), "same"); err != nil || bak != "" {
		t.Errorf("unchanged write: bak=%q err=%v; want no backup", bak, err)
	}
	if bak, err := writePlistWithBackup(filepath.Join(filepath.Dir(p), "new.plist"), nil, "x"); err != nil || bak != "" {
		t.Errorf("fresh write: bak=%q err=%v; want no backup", bak, err)
	}
	prev, _ := os.ReadFile(p)
	bak, err := writePlistWithBackup(p, prev, "changed")
	if err != nil || bak == "" {
		t.Fatalf("changed write: bak=%q err=%v; want a backup", bak, err)
	}
	// launchd loads *.plist from LaunchAgents; a backup with that suffix would
	// come back at login as a second job under the same label.
	if strings.HasSuffix(bak, ".plist") {
		t.Errorf("backup %q ends in .plist", bak)
	}
}

// TestAuditNamesACustomLauncherInsteadOfPrintingInstall is the doctor/nightly
// half: on a wrapper host the audit used to print `pogo service install` as the
// remedy — the destructive command, on exactly the host where it destroys.
func TestAuditNamesACustomLauncherInsteadOfPrintingInstall(t *testing.T) {
	_, pogod, wrapper := launcherSandbox(t)
	installed := installedDaemonPlist(t, wrapper)
	rendered, _, err := renderLaunchdPlistFor(pogod)
	if err != nil {
		t.Fatal(err)
	}

	res := auditLaunchAgent(launchdLabel, launchdPlistPath(), "pogo service install", rendered, nil)
	if res.Status != LaunchAgentStale {
		t.Fatalf("status = %q; want stale", res.Status)
	}
	res = classifyDaemonDrift(res, installed, []byte(rendered))

	if !strings.Contains(res.Detail, "custom launcher "+wrapper) {
		t.Errorf("Detail does not name the custom launcher:\n%s", res.Detail)
	}
	if !strings.Contains(res.Detail, "set service.launcher first") {
		t.Errorf("Detail does not say to set service.launcher first:\n%s", res.Detail)
	}
	if res.Remedy == "pogo service install" || strings.Contains(res.Detail, "run `pogo service install`") {
		t.Errorf("the bare install command is still offered as the fix: remedy=%q detail=%q", res.Remedy, res.Detail)
	}
	if !strings.Contains(res.Remedy, wrapper) {
		t.Errorf("Remedy %q does not carry the launcher path to configure", res.Remedy)
	}
}

// TestAuditNamesMissingProcessTypeAndConditionalKeepAlive reproduces the
// pre-39c77ec plist from the issue: no ProcessType, a conditional KeepAlive.
func TestAuditNamesMissingProcessTypeAndConditionalKeepAlive(t *testing.T) {
	_, pogod, _ := launcherSandbox(t)
	rendered, _, err := renderLaunchdPlistFor(pogod)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(rendered, "    <key>ProcessType</key>\n    <string>Interactive</string>\n", "", 1)
	old = strings.Replace(old, "    <key>KeepAlive</key>\n    <true/>\n", "    <key>KeepAlive</key>\n    <dict>\n        <key>SuccessfulExit</key>\n        <false/>\n    </dict>\n", 1)
	if old == rendered {
		t.Fatal("fixture: template changed shape")
	}
	path := writePlist(t, old)

	res := classifyDaemonDrift(auditLaunchAgent(launchdLabel, path, "pogo service install", rendered, nil), []byte(old), []byte(rendered))
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %q; want ProcessType and KeepAlive", res.Findings)
	}
	if !strings.Contains(res.Detail, "no ProcessType key") || !strings.Contains(res.Detail, "KeepAlive is conditional") || !strings.Contains(res.Detail, "inert") {
		t.Errorf("Detail does not name both findings and KeepAlive's inertness:\n%s", res.Detail)
	}
	// With pogod as the program the install IS the fix, and says so.
	if res.Remedy != "pogo service install" || strings.Contains(res.Detail, "custom launcher") {
		t.Errorf("a pogod plist was reported as a custom launcher: remedy=%q detail=%q", res.Remedy, res.Detail)
	}
}

// TestAuditDaemonPositiveControl: a plist identical to the rendering is ok,
// and classification is not reached.
func TestAuditDaemonPositiveControl(t *testing.T) {
	_, pogod, _ := launcherSandbox(t)
	installedDaemonPlist(t, pogod)
	for _, a := range AuditLaunchAgents() {
		if a.Label != launchdLabel {
			continue
		}
		if a.Status != LaunchAgentOK || len(a.Findings) != 0 || a.Remedy != "pogo service install" {
			t.Errorf("pogod plist matching this build: status=%q findings=%q remedy=%q", a.Status, a.Findings, a.Remedy)
		}
		return
	}
	if LaunchAgentsSupported() {
		t.Fatal("com.pogo.daemon not audited")
	}
}

// TestAuditLaunchAgentsClassifiesAWrapperPlist drives the path doctor and the
// nightly actually take — AuditLaunchAgents, not classifyDaemonDrift directly —
// against an installed wrapper plist. It pins the Classify wiring in
// managedLaunchAgents: with that field unset the audit still reports stale,
// but offers the bare `pogo service install` that destroys the wrapper.
func TestAuditLaunchAgentsClassifiesAWrapperPlist(t *testing.T) {
	if !LaunchAgentsSupported() {
		t.Skip("AuditLaunchAgents audits nothing off darwin")
	}
	_, _, wrapper := launcherSandbox(t)
	installedDaemonPlist(t, wrapper)
	for _, a := range AuditLaunchAgents() {
		if a.Label != launchdLabel {
			continue
		}
		if a.Status != LaunchAgentStale {
			t.Fatalf("wrapper plist: status = %q; want stale", a.Status)
		}
		if !strings.Contains(a.Detail, "custom launcher "+wrapper) {
			t.Errorf("Detail does not name the custom launcher — Classify not reached:\n%s", a.Detail)
		}
		if a.Remedy == "pogo service install" || !strings.Contains(a.Remedy, wrapper) {
			t.Errorf("Remedy = %q; want the set-[service]-launcher remedy carrying %s", a.Remedy, wrapper)
		}
		return
	}
	t.Fatal("com.pogo.daemon not audited")
}
