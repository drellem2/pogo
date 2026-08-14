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

// noHarvest is a source that must not be consulted. Every call site that uses it
// is asserting an ordering or short-circuit property, not just a return value.
func noHarvest(t *testing.T, why string) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Fatalf("this source must not run: %s", why)
		return "", nil
	}
}

// failHarvest is a source with nothing to give — the ordinary state of `gh auth
// token` on a host that never ran `gh auth login`.
func failHarvest(msg string) func() (string, error) {
	return func() (string, error) { return "", errors.New(msg) }
}

func TestEnsure_AmbientTokenIsNotOverwritten(t *testing.T) {
	get, set, env := envFuncs(map[string]string{"GH_TOKEN": "already-here"})
	res := ensure(get, set,
		noHarvest(t, "the environment already has a token"),
		noHarvest(t, "the environment already has a token"))
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
	res := ensure(get, set,
		noHarvest(t, "GITHUB_TOKEN is present"),
		noHarvest(t, "GITHUB_TOKEN is present"))
	if res.Source != SourceAmbient {
		t.Fatalf("want ambient, got %+v", res)
	}
}

// The launchd case: nothing in the environment, so the shell is asked. The gh
// source must NOT be reached — the shell won, and a chain that keeps probing
// after a win spends a subprocess to learn nothing.
func TestEnsure_HarvestsWhenEnvironmentIsEmpty(t *testing.T) {
	get, set, env := envFuncs(nil)
	res := ensure(get, set,
		func() (string, error) { return fakeToken, nil },
		noHarvest(t, "the shell already yielded a token"))
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
	res := ensure(get, set,
		func() (string, error) { return fakeToken, nil },
		noHarvest(t, "the shell already yielded a token"))
	if res.Source != SourceShell {
		t.Fatalf("want shell, got %+v", res)
	}
	if env["GH_TOKEN"] != fakeToken {
		t.Fatalf("blank GH_TOKEN was not replaced")
	}
}

func TestEnsure_HarvestFailureIsReportedNotFatal(t *testing.T) {
	get, set, env := envFuncs(nil)
	res := ensure(get, set, failHarvest("no shell here"), failHarvest("not logged in"))
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
			res := ensure(get, set,
				func() (string, error) { return out, nil },
				failHarvest("not logged in"))
			if res.Source != SourceNone {
				t.Fatalf("want none for %q, got %+v", name, res)
			}
			if _, ok := env["GH_TOKEN"]; ok {
				t.Fatalf("implausible probe output was written to GH_TOKEN")
			}
		})
	}
}

// mg-fb29's first ask: `gh auth token` is the LAST link, and it runs only when
// the shell had nothing. This is the host the old chain could not serve — no
// export anywhere a shell can see, but `gh auth login` has been run.
func TestEnsure_FallsThroughToGHAuthToken(t *testing.T) {
	get, set, env := envFuncs(nil)
	res := ensure(get, set,
		failHarvest("the user shell reported no GH_TOKEN"),
		func() (string, error) { return fakeToken, nil })
	if res.Source != SourceGH || !res.OK() {
		t.Fatalf("want gh-auth-token, got %+v", res)
	}
	if env["GH_TOKEN"] != fakeToken {
		t.Fatal("GH_TOKEN was not set from the gh credential")
	}
	if len(res.Tried) != 1 || res.Tried[0].Source != SourceShell {
		t.Fatalf("the shell attempt should be recorded as tried-and-empty, got %+v", res.Tried)
	}
}

// A non-zero `gh auth token` is ORDINARY, not a fault: a host that never ran
// `gh auth login` is a normal host. It must be recorded and fallen through, and
// it must not turn a successful shell harvest into a failure.
func TestEnsure_GHFailureIsOrdinaryNotAFault(t *testing.T) {
	// Shell wins; gh is never asked, so its state cannot matter at all.
	get, set, _ := envFuncs(nil)
	if res := ensure(get, set,
		func() (string, error) { return fakeToken, nil },
		noHarvest(t, "the shell already won")); res.Source != SourceShell {
		t.Fatalf("a gh with no login must not spoil a shell harvest, got %+v", res)
	}

	// Both empty: SourceNone, and the log line names BOTH sources it asked, so a
	// reader can tell "no shell export" from "no gh login" from both.
	get, set, _ = envFuncs(nil)
	res := ensure(get, set,
		failHarvest("the user shell reported no GH_TOKEN"),
		failHarvest("`gh auth token` exited non-zero (exit status 1)"))
	if res.Source != SourceNone || res.OK() {
		t.Fatalf("want none, got %+v", res)
	}
	if len(res.Tried) != 2 || res.Tried[0].Source != SourceShell || res.Tried[1].Source != SourceGH {
		t.Fatalf("both sources must be recorded in order, got %+v", res.Tried)
	}
	line := res.String()
	for _, want := range []string{string(SourceShell), string(SourceGH), "gh auth login"} {
		if !strings.Contains(line, want) {
			t.Errorf("the SourceNone line does not mention %q — a reader cannot tell which\n"+
				"source was missing, which is the whole diagnosis: %s", want, line)
		}
	}
}

// The named-source requirement (mg-fb29, pm-pogo's second condition). The
// startup log line is what refuted gh#113's premise; with two sources in the
// chain, a line that does not say WHICH one won stops being able to answer that
// question. So every winning branch must name its source, and no two may read
// alike.
func TestResultStringNamesTheWinningSource(t *testing.T) {
	seen := map[string]Source{}
	for _, src := range []Source{SourceAmbient, SourceShell, SourceGH, SourceNone} {
		line := Result{Source: src}.String()
		if !strings.Contains(line, "source="+string(src)) {
			t.Errorf("Result{%s}.String() does not name its source: %s", src, line)
		}
		if other, dup := seen[line]; dup {
			t.Errorf("sources %s and %s render identically: %s", other, src, line)
		}
		seen[line] = src
	}
}

// OK() is the POSITIVE CREDENTIAL PREDICATE cmd/pogod arms the intake detector
// on, so which sources satisfy it is a pinned property rather than an
// implementation detail. Before SourceGH existed, !OK() also covered every host
// authenticated by `gh auth login` — which is precisely why arming on it would
// have false-alarmed, and why item 1 had to land before item 3.
func TestOKIsTrueForEveryRealSourceAndFalseOnlyForNone(t *testing.T) {
	for _, src := range []Source{SourceAmbient, SourceShell, SourceGH} {
		if !(Result{Source: src}).OK() {
			t.Errorf("%s must satisfy OK() — a credential was established", src)
		}
	}
	if (Result{Source: SourceNone}).OK() {
		t.Error("SourceNone must NOT satisfy OK()")
	}
}

// The probe is `gh auth token`, not `gh auth status`. Pinned because the
// difference is the whole anti-pattern this ticket rejected: `auth token` has a
// machine contract (a token on stdout, or a non-zero exit), while `auth status`
// has an English report, and classifying auth by matching that prose stops
// working silently the first time gh rewords it.
func TestGHProbeReadsTheTokenNotTheProse(t *testing.T) {
	if len(GHTokenCommand) != 2 || GHTokenCommand[0] != "auth" || GHTokenCommand[1] != "token" {
		t.Fatalf("want [auth token], got %v", GHTokenCommand)
	}
}

// The secret-discipline guard. Every observable this package produces — the
// Result string and the error text — is asserted not to contain the value, so a
// future edit that folds the probe output into a message fails here rather than
// in a log file.
func TestObservablesNeverCarryTheValue(t *testing.T) {
	get, set, _ := envFuncs(nil)
	res := ensure(get, set, func() (string, error) { return fakeToken, nil }, failHarvest("no gh"))
	if strings.Contains(res.String(), fakeToken) {
		t.Fatal("Result.String leaked the token value")
	}

	// The same, one link further down the chain: a token that arrives from `gh
	// auth token` is exactly as secret as one from the shell.
	get, set, _ = envFuncs(nil)
	viaGH := ensure(get, set, failHarvest("no shell export"), func() (string, error) { return fakeToken, nil })
	if viaGH.Source != SourceGH {
		t.Fatalf("setup: want gh source, got %+v", viaGH)
	}
	if strings.Contains(viaGH.String(), fakeToken) {
		t.Fatal("the gh-source Result.String leaked the token value")
	}

	// The rejection path is the one most tempted to quote what it rejected — and
	// with two sources there are two of them, each with its own Tried entry.
	for name, res := range map[string]Result{
		"shell": ensureFresh(t, func() (string, error) { return fakeToken + " trailing junk", nil },
			failHarvest("no gh")),
		"gh": ensureFresh(t, failHarvest("no shell export"),
			func() (string, error) { return fakeToken + " trailing junk", nil }),
	} {
		if res.Err != nil && strings.Contains(res.Err.Error(), fakeToken) {
			t.Errorf("%s rejection error leaked the candidate value: %v", name, res.Err)
		}
		if strings.Contains(res.String(), fakeToken) {
			t.Errorf("%s rejection Result.String leaked the candidate value", name)
		}
		for _, a := range res.Tried {
			if a.Err != nil && strings.Contains(a.Err.Error(), fakeToken) {
				t.Errorf("%s: Attempt(%s).Err leaked the candidate value", name, a.Source)
			}
		}
	}
}

// ensureFresh runs the chain against a throwaway environment map.
func ensureFresh(t *testing.T, shell, gh func() (string, error)) Result {
	t.Helper()
	get, set, _ := envFuncs(nil)
	return ensure(get, set, shell, gh)
}

// ghAuthToken against the real gh on this host, with the inherited environment
// production uses. Like TestShellHarvest_RunsAgainstARealShell, it proves the
// assembled function runs and returns cleanly; whether a credential comes back
// depends on whether this developer has ever run `gh auth login`, so NOTHING is
// asserted about the value and nothing prints it.
//
// The failure branch is the interesting one and it is asserted: a host with no
// gh login must produce an error that is EXIT-STATUS ONLY. gh's stderr on that
// path is a paragraph of English including a login invitation, and folding it
// into the error would put it in a log line and a mail body.
func TestGHAuthToken_RunsAgainstTheRealGH(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("no gh on this host")
	}
	got, err := ghAuthToken()
	if err != nil {
		// gh's not-logged-in stderr is several lines of prose. Ours is one short
		// line carrying an exit status, so shape is a sufficient and wording-proof
		// assertion — which matters, since the wording is the thing that changes.
		msg := err.Error()
		if strings.ContainsAny(msg, "\n\r") || len(msg) > 200 {
			t.Fatalf("the error carries more than an exit status (%d bytes, multi-line=%t) — "+
				"gh's stderr must not travel into a log line",
				len(msg), strings.ContainsAny(msg, "\n\r"))
		}
		if !strings.Contains(msg, "exit status") && !strings.Contains(msg, "not on PATH") {
			t.Fatalf("a non-zero gh must report its exit status and nothing else: %q", msg)
		}
		return
	}
	if strings.ContainsAny(got, " \t\n") {
		t.Fatalf("gh auth token returned untrimmed output (%d bytes)", len(got))
	}
	if err := validate(got); err != nil {
		t.Fatalf("a credential gh holds must validate: %v", err)
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
