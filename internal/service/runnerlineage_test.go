package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// drellem2/pogo#126: the runner half of [lineage]. Every test here runs against
// temp dirs and throwaway git repos; none reads the host's config, its
// ~/.pogo/bin, or its launchd.

func TestIsDrellem2PogoRemote(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/drellem2/pogo":        true,
		"https://github.com/drellem2/pogo.git":    true,
		"https://github.com/drellem2/pogo/":       true,
		"git@github.com:drellem2/pogo.git":        true,
		"ssh://git@github.com/Drellem2/Pogo.git":  true,
		"/Users/someone/dev/drellem2/pogo":        true,
		"git@github.com:payitgov/.pogo.git":       false,
		"https://github.com/notdrellem2/pogo.git": false,
		"https://github.com/drellem2/pogo-fork":   false,
		"":                                        false,
	} {
		if got := isDrellem2PogoRemote(url); got != want {
			t.Errorf("isDrellem2PogoRemote(%q) = %v, want %v", url, got, want)
		}
	}
}

// TestRunnerLineageForeignness: undeclared is never foreign (no git call), a
// declared drellem2/pogo origin is not foreign, another origin is, and an
// unreadable origin on a declared lineage counts as foreign.
func TestRunnerLineageForeignness(t *testing.T) {
	calls := 0
	prev := runnerGit
	t.Cleanup(func() { runnerGit = prev })
	origin, originErr := "", error(nil)
	runnerGit = func(repo string, args ...string) ([]byte, error) {
		calls++
		return []byte(origin + "\n"), originErr
	}

	if l := runnerLineageFrom(config.LineageConfig{}); l.Declared || l.Foreign() || calls != 0 {
		t.Errorf("undeclared: declared=%v foreign=%v git calls=%d; want false/false/0", l.Declared, l.Foreign(), calls)
	}
	if l := runnerLineageFrom(config.LineageConfig{}); l.Path != config.DefaultLineageRunnerPath || l.Ref != "origin/main" {
		t.Errorf("undeclared defaults: path=%q ref=%q", l.Path, l.Ref)
	}

	origin = "git@github.com:drellem2/pogo.git"
	if l := runnerLineageFrom(config.LineageConfig{RunnerRepo: "/r", RunnerDeclared: true}); l.Foreign() {
		t.Error("a declared drellem2/pogo origin read as foreign")
	}
	origin = "git@github.com:payitgov/.pogo.git"
	if l := runnerLineageFrom(config.LineageConfig{RunnerRepo: "/r", RunnerDeclared: true}); !l.Foreign() {
		t.Error("a declared payitgov/.pogo origin did not read as foreign")
	}
	origin, originErr = "", errors.New("fatal: not a git repository")
	l := runnerLineageFrom(config.LineageConfig{RunnerRepo: "/r", RunnerDeclared: true})
	if !l.Foreign() || !strings.Contains(l.OriginNote(), "not a git repository") {
		t.Errorf("unreadable origin: foreign=%v note=%q; want foreign, with the reason named", l.Foreign(), l.OriginNote())
	}
}

// --- plist audit -----------------------------------------------------------

func jobPlist(program string, extraArgs []string, hour int, logPath string) string {
	args := "<string>" + program + "</string>"
	for _, a := range extraArgs {
		args += "<string>" + a + "</string>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>com.pogo.deploy</string>
  <key>ProgramArguments</key><array>` + args + `</array>
  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>` + strconv.Itoa(hour) + `</integer><key>Minute</key><integer>0</integer></dict>
  <key>StandardOutPath</key><string>` + logPath + `</string>
</dict>
</plist>
`
}

// TestGenericPlistAuditStatesAChangedProgramWithNoImperative: the recovery,
// deploy, reclaim and gh-watch jobs go through auditLaunchAgent with no
// classifier. A changed ProgramArguments must be named — what the job runs and
// what the installer would put there — and must NOT be followed by "run
// `pogo service install-*`", which on an org-templated host is the clobber.
func TestGenericPlistAuditStatesAChangedProgramWithNoImperative(t *testing.T) {
	dir := t.TempDir()
	rendered := jobPlist("/home/u/.pogo/bin/pogo-deploy.sh", nil, 3, "/logs/a.log")
	installed := jobPlist("/home/u/.pogo/bin/org-deploy.sh", []string{"--org"}, 3, "/logs/a.log")
	path := writeFile(t, dir, "com.pogo.deploy.plist", installed)

	res := auditLaunchAgent("com.pogo.deploy", path, "pogo service install-deploy", rendered, nil)

	if res.Status != LaunchAgentStale {
		t.Fatalf("status = %q, want stale", res.Status)
	}
	if res.ScheduleDrift {
		t.Error("schedule drift reported for plists with the same schedule")
	}
	if !containsString(res.DiffKeys, "ProgramArguments") || len(res.DiffKeys) != 1 {
		t.Errorf("DiffKeys = %v, want exactly [ProgramArguments]", res.DiffKeys)
	}
	for _, want := range []string{"installed job runs", "org-deploy.sh", "/home/u/.pogo/bin/pogo-deploy.sh", "`pogo service install-deploy` would replace it"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, res.Detail)
		}
	}
	if strings.Contains(strings.ToLower(res.Detail), "run `") {
		t.Errorf("detail gives an imperative for a changed ProgramArguments:\n%s", res.Detail)
	}
}

// TestGenericPlistAuditProgramAndScheduleDriftStillNoImperative: schedule drift
// leads, as before, but a changed program on the same plist still suppresses
// the imperative.
func TestGenericPlistAuditProgramAndScheduleDriftStillNoImperative(t *testing.T) {
	dir := t.TempDir()
	rendered := jobPlist("/bin/pogo-deploy.sh", nil, 3, "/logs/a.log")
	path := writeFile(t, dir, "p.plist", jobPlist("/bin/org-deploy.sh", nil, 4, "/logs/a.log"))

	res := auditLaunchAgent("com.pogo.deploy", path, "pogo service install-deploy", rendered, nil)
	if !res.ScheduleDrift || !strings.Contains(res.Detail, "FIRES AT DIFFERENT TIMES") {
		t.Errorf("schedule drift not reported:\n%s", res.Detail)
	}
	if !strings.Contains(res.Detail, "installed job runs /bin/org-deploy.sh") {
		t.Errorf("changed program not stated:\n%s", res.Detail)
	}
	if strings.Contains(strings.ToLower(res.Detail), "run `") {
		t.Errorf("imperative given despite a changed program:\n%s", res.Detail)
	}
}

// TestGenericPlistAuditNamesOtherKeysAndKeepsItsRemedy: the positive control. A
// difference that is NOT the program — a moved log path — is this build's to
// rewrite, so the keys are named and the imperative stays.
func TestGenericPlistAuditNamesOtherKeysAndKeepsItsRemedy(t *testing.T) {
	dir := t.TempDir()
	rendered := jobPlist("/bin/pogo-deploy.sh", nil, 3, "/logs/new.log")
	path := writeFile(t, dir, "p.plist", jobPlist("/bin/pogo-deploy.sh", nil, 3, "/logs/old.log"))

	res := auditLaunchAgent("com.pogo.deploy", path, "pogo service install-deploy", rendered, nil)
	if len(res.DiffKeys) != 1 || res.DiffKeys[0] != "StandardOutPath" {
		t.Errorf("DiffKeys = %v, want [StandardOutPath]", res.DiffKeys)
	}
	if !strings.Contains(res.Detail, "differing keys: StandardOutPath") || !strings.Contains(res.Detail, "run `pogo service install-deploy`") {
		t.Errorf("detail does not name the key and the remedy:\n%s", res.Detail)
	}
	if strings.Contains(res.Detail, "installed job runs") {
		t.Errorf("an unchanged program was reported as changed:\n%s", res.Detail)
	}
}

// --- payload audit ---------------------------------------------------------

// TestPayloadAuditUndeclaredStatesTheDifferenceWithoutAnImperative: with no
// [lineage] runner, the comparison is still against this build's copy, the
// difference is still reported as stale, and no "Run `...`" follows it.
func TestPayloadAuditUndeclaredStatesTheDifferenceWithoutAnImperative(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.sh", runnerSource)
	inst := writeFile(t, dir, "inst.sh", runnerInstalled)

	a := auditPayloadScript("com.pogo.deploy", "pogo-deploy.sh", inst, src, nil,
		"pogo service install-deploy", "scripts/launchd/pogo-deploy.sh", true)
	if a.Status != PayloadStale {
		t.Fatalf("status = %q, want stale", a.Status)
	}
	if strings.Contains(a.Detail, "Run `") || strings.Contains(a.Detail, "run `") {
		t.Errorf("undeclared stale detail still gives an imperative:\n%s", a.Detail)
	}
	for _, want := range []string{"[lineage] runner_repo", "`pogo service install-deploy` would replace it"} {
		if !strings.Contains(a.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, a.Detail)
		}
	}
}

// lineageRepo makes a git repo whose origin is originURL and whose HEAD carries
// body at relPath, and returns its path.
func lineageRepo(t *testing.T, originURL, relPath, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(relPath)), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, filepath.Dir(relPath)), filepath.Base(relPath), body)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", originURL},
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "runner"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func declaredLineage(t *testing.T, repo string) runnerLineage {
	t.Helper()
	return runnerLineageFrom(config.LineageConfig{
		RunnerRepo: repo, RunnerRef: "HEAD", RunnerPath: "bin/pogo-deploy.sh", RunnerDeclared: true,
	})
}

// TestPayloadAuditDeclaredLineageComparesAgainstTheDeclaredUpstream: with a
// declared lineage the installed runner is judged against
// <repo>@<ref>:<runner_path>. Matching it is OK even though it differs from
// this build; differing from it is stale and says who would and would not
// refresh it; an unreadable ref is NOT CHECKED, never a fallback to this build.
func TestPayloadAuditDeclaredLineageComparesAgainstTheDeclaredUpstream(t *testing.T) {
	const orgRunner = "#!/bin/sh\n# the org's runner\necho org\n"
	repo := lineageRepo(t, "git@github.com:payitgov/.pogo.git", "bin/pogo-deploy.sh", orgRunner)
	l := declaredLineage(t, repo)
	if !l.Foreign() {
		t.Fatalf("payitgov lineage not foreign: %+v", l)
	}
	dir := t.TempDir()

	inst := writeFile(t, dir, "match.sh", orgRunner)
	a := auditPayloadLineage("com.pogo.deploy", "pogo-deploy.sh", inst, l, "pogo service install-deploy", true)
	if a.Status != PayloadOK {
		t.Errorf("installed == declared upstream: status = %q, want ok — %s", a.Status, a.Detail)
	}
	if a.Source != l.Spec() || !strings.Contains(a.Detail, repo+"@HEAD:bin/pogo-deploy.sh") {
		t.Errorf("Source = %q / detail %q; want the declared <repo>@<ref>:<path> named", a.Source, a.Detail)
	}

	inst = writeFile(t, dir, "drift.sh", runnerInstalled)
	a = auditPayloadLineage("com.pogo.deploy", "pogo-deploy.sh", inst, l, "pogo service install-deploy", true)
	if a.Status != PayloadStale {
		t.Fatalf("installed != declared upstream: status = %q, want stale", a.Status)
	}
	if strings.Contains(strings.ToLower(a.Detail), "run `") {
		t.Errorf("declared stale detail gives an imperative:\n%s", a.Detail)
	}
	for _, want := range []string{"declared upstream", "NOT " + repo, "--force"} {
		if !strings.Contains(a.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, a.Detail)
		}
	}

	bad := l
	bad.Ref = "no-such-ref"
	a = auditPayloadLineage("com.pogo.deploy", "pogo-deploy.sh", inst, bad, "pogo service install-deploy", true)
	if a.Status != PayloadUnknown || !strings.Contains(a.Detail, "NOT CHECKED") {
		t.Errorf("unreadable declared ref: status = %q, want unknown/NOT CHECKED — %s", a.Status, a.Detail)
	}
}

// TestAuditPayloadScriptsRoutesTheRunnerThroughADeclaredLineage drives the
// registry path — AuditPayloadScripts, as doctor and check-activation call it —
// so the Lineage row flag and the config read are pinned, not only the pure
// function.
func TestAuditPayloadScriptsRoutesTheRunnerThroughADeclaredLineage(t *testing.T) {
	if !LaunchAgentsSupported() {
		t.Skip("payload audit returns nil off darwin")
	}
	const orgRunner = "#!/bin/sh\necho org\n"
	repo := lineageRepo(t, "git@github.com:payitgov/.pogo.git", "bin/pogo-deploy.sh", orgRunner)
	home := runnerSandbox(t, "[lineage]\nrunner_repo = \""+repo+"\"\nrunner_ref = \"HEAD\"\nrunner_path = \"bin/pogo-deploy.sh\"\n")
	writeFile(t, filepath.Join(home, ".pogo", "bin"), "pogo-deploy.sh", orgRunner)

	var row *PayloadScriptAudit
	for _, a := range AuditPayloadScripts() {
		if a.Name == "pogo-deploy.sh" {
			a := a
			row = &a
		}
	}
	if row == nil {
		t.Fatal("no pogo-deploy.sh row")
	}
	if row.Status != PayloadOK || !strings.HasPrefix(row.Source, repo+"@HEAD:") {
		t.Errorf("runner row: status=%q source=%q; want ok against the declared upstream — %s", row.Status, row.Source, row.Detail)
	}
}

// runnerSandbox points HOME, XDG_CONFIG_HOME and POGO_HOME at a temp dir, writes
// configTOML as its config.toml, and returns the home.
func runnerSandbox(t *testing.T, configTOML string) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(home, ".config")
	for _, d := range []string{filepath.Join(xdg, "pogo"), filepath.Join(home, ".pogo", "bin")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	t.Setenv("POGO_DEPLOY_SRC", filepath.Join(home, "no-deploy-src"))
	if configTOML != "" {
		writeFile(t, filepath.Join(xdg, "pogo"), "config.toml", configTOML)
	}
	return home
}

// --- install-deploy --------------------------------------------------------

// TestGuardRunnerOverwrite: the refusal fires on exactly one combination —
// installed runner differs from this build's AND the declared lineage is
// foreign — and --force lets it through with a notice.
func TestGuardRunnerOverwrite(t *testing.T) {
	dir := t.TempDir()
	dst := writeFile(t, dir, "pogo-deploy.sh", "org runner\n")
	ours := []byte("drellem2 runner\n")
	foreign := runnerLineage{Declared: true, Repo: "/org", Ref: "origin/main", Path: "bin/pogo-deploy.sh", Origin: "git@github.com:payitgov/.pogo.git"}
	home := runnerLineage{Declared: true, Repo: "/src", Ref: "origin/main", Path: "scripts/launchd/pogo-deploy.sh", Origin: "https://github.com/drellem2/pogo.git"}

	_, err := guardRunnerOverwrite(dst, ours, foreign, false)
	if err == nil {
		t.Fatal("cross-lineage overwrite was not refused")
	}
	for _, want := range []string{"refusing", "/org@origin/main:bin/pogo-deploy.sh", "payitgov/.pogo", "Nothing was changed", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}

	notice, err := guardRunnerOverwrite(dst, ours, foreign, true)
	if err != nil || !strings.Contains(notice, "--force") || !strings.Contains(notice, ".prev") {
		t.Errorf("--force: notice=%q err=%v; want a notice naming --force and .prev", notice, err)
	}

	for name, c := range map[string]struct {
		l       runnerLineage
		content []byte
		dst     string
	}{
		"undeclared":          {runnerLineage{Repo: "/src"}, ours, dst},
		"declared drellem2":   {home, ours, dst},
		"identical runner":    {foreign, []byte("org runner\n"), dst},
		"no runner installed": {foreign, ours, filepath.Join(dir, "absent.sh")},
	} {
		if notice, err := guardRunnerOverwrite(c.dst, c.content, c.l, false); err != nil || notice != "" {
			t.Errorf("%s: notice=%q err=%v; want allowed silently", name, notice, err)
		}
	}
}

// TestInstallDeployRefusesACrossLineageOverwrite drives the real InstallDeploy
// on a sandboxed host whose [lineage] names a foreign runner upstream: it must
// refuse before writing the runner, the .prev or the plist, and before any
// launchctl call. launchctl is stubbed regardless, so a regression reaches a
// recorder and not the host's com.pogo.deploy.
func TestInstallDeployRefusesACrossLineageOverwrite(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("InstallDeploy is macOS-only")
	}
	repo := lineageRepo(t, "git@github.com:payitgov/.pogo.git", "bin/pogo-deploy.sh", "org runner\n")
	home := runnerSandbox(t, "[lineage]\nrunner_repo = \""+repo+"\"\n")
	src := writeFile(t, t.TempDir(), "pogo-deploy.sh", "#!/bin/sh\n# this build's runner\n")
	t.Setenv("POGO_DEPLOY_SCRIPT", src)
	t.Setenv("POGO_NET_CONTROL_SCRIPT", src)
	installed := writeFile(t, filepath.Join(home, ".pogo", "bin"), "pogo-deploy.sh", "org runner\n")

	var calls [][]string
	prev := deployLaunchctl
	deployLaunchctl = func(args ...string) ([]byte, error) { calls = append(calls, args); return nil, nil }
	t.Cleanup(func() { deployLaunchctl = prev })

	err := InstallDeploy(DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("InstallDeploy err = %v; want a cross-lineage refusal", err)
	}
	if b, _ := os.ReadFile(installed); string(b) != "org runner\n" {
		t.Errorf("installed runner changed despite the refusal: %q", b)
	}
	if _, err := os.Stat(installed + ".prev"); !os.IsNotExist(err) {
		t.Errorf("a .prev was written despite the refusal (stat err=%v)", err)
	}
	if _, err := os.Stat(deployPlistPath()); !os.IsNotExist(err) {
		t.Errorf("the plist was written despite the refusal (stat err=%v)", err)
	}
	if len(calls) != 0 {
		t.Errorf("launchctl was called despite the refusal: %v", calls)
	}

	// --force goes through, and keeps the org's runner as .prev.
	if err := InstallDeploy(DeployOptions{Force: true}); err != nil {
		t.Fatalf("InstallDeploy --force: %v", err)
	}
	if b, _ := os.ReadFile(installed); !strings.Contains(string(b), "this build's runner") {
		t.Errorf("--force did not install this build's runner: %q", b)
	}
	if b, _ := os.ReadFile(installed + ".prev"); string(b) != "org runner\n" {
		t.Errorf("--force did not keep the org runner as .prev: %q", b)
	}
	if len(calls) == 0 {
		t.Error("--force install made no (stubbed) launchctl call")
	}
}
