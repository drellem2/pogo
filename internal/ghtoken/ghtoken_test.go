package ghtoken

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeToken is a made-up string with the shape of a token and none of the
// authority. Nothing in this file reads, prints, or asserts on a real secret —
// the whole package is testable without one, which is the point.
const fakeToken = "ghp_notarealtoken_0000000000000000000000"

func envFuncs(initial map[string]string) (func(string) string, func(string, string) error, map[string]string) {
	env := map[string]string{}
	for k, v := range initial {
		env[k] = v
	}
	get := func(k string) string { return env[k] }
	set := func(k, v string) error { env[k] = v; return nil }
	return get, set, env
}

func TestEnsure_AmbientTokenIsNotOverwritten(t *testing.T) {
	get, set, env := envFuncs(map[string]string{"GH_TOKEN": "already-here"})
	res := ensure(get, set, func() (string, error) {
		t.Fatal("harvest must not run when the environment already has a token")
		return "", nil
	})
	if res.Source != SourceAmbient || !res.OK() {
		t.Fatalf("want ambient, got %+v", res)
	}
	if env["GH_TOKEN"] != "already-here" {
		t.Fatalf("ambient token was overwritten")
	}
}

// gh falls back to GITHUB_TOKEN when GH_TOKEN is unset, so a process holding
// only GITHUB_TOKEN is already authenticated and must not be probed.
func TestEnsure_GithubTokenCountsAsAmbient(t *testing.T) {
	get, set, _ := envFuncs(map[string]string{"GITHUB_TOKEN": "already-here"})
	res := ensure(get, set, func() (string, error) {
		t.Fatal("harvest must not run when GITHUB_TOKEN is present")
		return "", nil
	})
	if res.Source != SourceAmbient {
		t.Fatalf("want ambient, got %+v", res)
	}
}

// The launchd case: nothing in the environment, so the shell is asked.
func TestEnsure_HarvestsWhenEnvironmentIsEmpty(t *testing.T) {
	get, set, env := envFuncs(nil)
	res := ensure(get, set, func() (string, error) { return fakeToken, nil })
	if res.Source != SourceShell || !res.OK() {
		t.Fatalf("want shell, got %+v", res)
	}
	if env["GH_TOKEN"] != fakeToken {
		t.Fatalf("GH_TOKEN was not set from the harvest")
	}
}

// A whitespace-only ambient value is the same as no value: an export that
// evaluated to nothing must not be mistaken for authentication.
func TestEnsure_BlankAmbientTokenIsTreatedAsAbsent(t *testing.T) {
	get, set, env := envFuncs(map[string]string{"GH_TOKEN": "   "})
	res := ensure(get, set, func() (string, error) { return fakeToken, nil })
	if res.Source != SourceShell {
		t.Fatalf("want shell, got %+v", res)
	}
	if env["GH_TOKEN"] != fakeToken {
		t.Fatalf("blank GH_TOKEN was not replaced")
	}
}

func TestEnsure_HarvestFailureIsReportedNotFatal(t *testing.T) {
	get, set, env := envFuncs(nil)
	res := ensure(get, set, func() (string, error) { return "", errors.New("no shell here") })
	if res.Source != SourceNone || res.OK() {
		t.Fatalf("want none, got %+v", res)
	}
	if res.Err == nil {
		t.Fatal("a failed harvest must carry its reason")
	}
	if _, ok := env["GH_TOKEN"]; ok {
		t.Fatal("a failed harvest must not set GH_TOKEN")
	}
}

func TestEnsure_RejectsImplausibleProbeOutput(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"too short":   "abc",
		"has spaces":  "some shell banner text",
		"has newline": "line one\nline two",
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			get, set, env := envFuncs(nil)
			res := ensure(get, set, func() (string, error) { return out, nil })
			if res.Source != SourceNone {
				t.Fatalf("want none for %q, got %+v", name, res)
			}
			if _, ok := env["GH_TOKEN"]; ok {
				t.Fatalf("implausible probe output was written to GH_TOKEN")
			}
		})
	}
}

// The secret-discipline guard. Every observable this package produces — the
// Result string and the error text — is asserted not to contain the value, so a
// future edit that folds the probe output into a message fails here rather than
// in a log file.
func TestObservablesNeverCarryTheValue(t *testing.T) {
	get, set, _ := envFuncs(nil)
	res := ensure(get, set, func() (string, error) { return fakeToken, nil })
	if strings.Contains(res.String(), fakeToken) {
		t.Fatal("Result.String leaked the token value")
	}

	// The rejection path is the one most tempted to quote what it rejected.
	get, set, _ = envFuncs(nil)
	bad := ensure(get, set, func() (string, error) { return fakeToken + " trailing junk", nil })
	if bad.Err != nil && strings.Contains(bad.Err.Error(), fakeToken) {
		t.Fatalf("rejection error leaked the candidate value: %v", bad.Err)
	}
	if strings.Contains(bad.String(), fakeToken) {
		t.Fatalf("rejection Result.String leaked the candidate value")
	}
}

func TestProbeCommand(t *testing.T) {
	// zsh reads ~/.zshenv on every invocation, so -c is enough and -i (which can
	// stall under load) is avoided. Everything else needs a login shell.
	if got := ProbeCommand("/bin/zsh"); len(got) != 2 || got[0] != "-c" || got[1] != ProbeScript {
		t.Errorf("zsh should be probed with -c, got %v", got)
	}
	if got := ProbeCommand("/bin/bash"); len(got) != 2 || got[0] != "-lc" {
		t.Errorf("bash should be probed with -lc, got %v", got)
	}
}

// probeUnderMinimalEnv runs the real probe with a SEALED environment — the
// reproduction of what launchd hands pogod. The seal lives here rather than in
// shellHarvest because production subprocesses must inherit os.Environ() to
// carry GIT_CEILING_DIRECTORIES (internal/gitceiling); test files are exempt
// from that guard precisely so a test can decide the environment itself.
func probeUnderMinimalEnv(t *testing.T, shell string, env []string) string {
	t.Helper()
	cmd := exec.Command(shell, ProbeCommand(shell)...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// The mechanism test, and the reason this package is believable: run the real
// probe against a real zsh whose environment is a faithful reproduction of
// launchd's — no GH_TOKEN, minimal PATH — with a throwaway HOME holding a
// .zshenv that exports a FAKE token. If `zsh -c` did not source ~/.zshenv, or
// the probe script were wrong, this fails.
func TestProbe_SourcesZshenvUnderALaunchdMinimalEnv(t *testing.T) {
	shell := requireZsh(t)

	home := t.TempDir()
	zshenv := "export GH_TOKEN=" + fakeToken + "\n"
	if err := os.WriteFile(filepath.Join(home, ".zshenv"), []byte(zshenv), 0o600); err != nil {
		t.Fatal(err)
	}

	// launchd's environment, reproduced: HOME and a bare PATH, nothing else.
	// Note there is no GH_TOKEN here — the value can only arrive via .zshenv.
	got := probeUnderMinimalEnv(t, shell, []string{"HOME=" + home, "PATH=/usr/bin:/bin"})
	if got != fakeToken {
		t.Fatalf("probe did not return the value exported by .zshenv (got %d bytes)", len(got))
	}
	if err := validate(got); err != nil {
		t.Fatalf("a harvested token must validate: %v", err)
	}
}

// The negative half of the mechanism test: with no .zshenv to source, the probe
// returns nothing rather than inventing something. Without this, a probe that
// always printed a constant would pass the test above.
func TestProbe_EmptyWhenNothingExportsIt(t *testing.T) {
	shell := requireZsh(t)
	home := t.TempDir()
	got := probeUnderMinimalEnv(t, shell, []string{"HOME=" + home, "PATH=/usr/bin:/bin"})
	if got != "" {
		t.Fatalf("probe returned %d bytes for a HOME with no .zshenv", len(got))
	}
	if err := validate(got); err == nil {
		t.Fatal("an empty probe result must not validate")
	}
}

// shellHarvest itself, against the real /bin/sh, with the inherited (not
// sealed) environment production uses. It proves the assembled function — flags,
// script, trimming, error handling — runs and returns cleanly; whether a token
// comes back depends on the developer's own shell, so nothing is asserted about
// the value, and nothing prints it.
func TestShellHarvest_RunsAgainstARealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shells only")
	}
	got, err := shellHarvest("/bin/sh")
	if err != nil {
		t.Fatalf("harvest against /bin/sh failed: %v", err)
	}
	if strings.ContainsAny(got, " \t\n") {
		t.Fatalf("harvest returned untrimmed output (%d bytes)", len(got))
	}
}

func requireZsh(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("zshenv sourcing is the macOS/launchd case")
	}
	if _, err := os.Stat("/bin/zsh"); err != nil {
		t.Skip("no /bin/zsh on this host")
	}
	return "/bin/zsh"
}

func TestUserShell_PrefersAnAbsoluteExecutableSHELL(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	if got := UserShell(); got != "/bin/sh" {
		t.Fatalf("want /bin/sh, got %q", got)
	}
	// A relative or missing $SHELL — as under launchd — must fall back.
	t.Setenv("SHELL", "")
	got := UserShell()
	if !filepath.IsAbs(got) {
		t.Fatalf("fallback shell must be absolute, got %q", got)
	}
	if st, err := os.Stat(got); err != nil || st.IsDir() {
		t.Fatalf("fallback shell %q is not an executable file", got)
	}
}
