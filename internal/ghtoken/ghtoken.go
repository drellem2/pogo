// Package ghtoken repairs GH_TOKEN for the `gh` subprocesses pogod spawns.
//
// It is the read-side sibling of internal/pathenv, and it exists for the same
// reason: launchd execs pogod directly with a minimal environment. pathenv
// covers the case where a child cannot be FOUND; this covers the case where a
// child is found, runs, and cannot AUTHENTICATE.
//
// # The failure
//
// On this box the secret exports live in `~/.zshenv`, which is sourced by every
// zsh invocation. launchd does not invoke a shell — it execs the binary — so
// pogod and every subprocess it spawns inherit an environment with no GH_TOKEN.
// `gh issue view` then exits non-zero with "please run gh auth login /
// populate the GH_TOKEN environment variable", and internal/ghteardown, which
// is scrupulously careful never to read a failed lookup as "closed", correctly
// reports every carrier as INDETERMINATE. Every carrier. Every run. Forever.
//
// That is the worst shape a detector can take: not wrong, just blind, and
// twice-daily loud about it. It is invisible from an interactive shell and from
// a crew agent — both get the full environment — so it only bites the
// pogod-resident watcher, which is exactly the process nobody is watching.
//
// # The repair
//
// Ensure asks a user shell for the value, because the shell is the thing that
// knows where the secret lives. Sourcing `~/.zshenv` is zsh's own contract for
// every invocation, so `zsh -c 'printf %s "$GH_TOKEN"'` returns the token
// whether it is a literal export, a command substitution, or a keychain lookup
// — none of which a file parser would survive. Other shells get `-lc` so their
// profile is read.
//
// The alternative — writing the token into the launchd plist's
// EnvironmentVariables — is rejected on purpose. That is a plaintext secret in
// a world-readable file, traded for a problem that has a solution which keeps
// the secret exactly where it already is.
//
// # The second source, and why it makes SourceNone mean something (mg-fb29)
//
// After the shell, `gh auth token` is asked. That is the one source in the
// proposed chain that writes NO new copy of the credential anywhere: it reads a
// token gh already holds, from a store gh already wrote, and nothing else about
// where secrets live changes. The rest of the configurable chain — an env-var
// name, a token file, a token-command — each parks a fresh copy somewhere new,
// which is a secret-handling decision and stays reserved (mg-7d62).
//
// Its value is not really the extra host it rescues. It is that it turns
// SourceNone from a heuristic into a DECIDABLE PREDICATE. Before it, "no token
// harvested" did not mean "gh cannot authenticate", because `gh auth login`
// writes hosts.yml and this package could not see that file — so a caller that
// treated SourceNone as "unauthenticated" would false-alarm on every host that
// had ever run `gh auth login`. `gh auth token` reads exactly that file, so
// after it, SourceNone means gh has no credential for the default host, and a
// caller may act on it. cmd/pogod does: the gh-issue intake detector refuses to
// arm without one, rather than letting one global cause be amplified into a
// per-repo fault for every watched repo (mg-fb29).
//
// A non-zero exit from `gh auth token` is ORDINARY, not a fault. A host that
// never ran `gh auth login` is a normal host; the probe reports and falls
// through, and nothing here raises an alarm about it. What is alarmable is the
// state at the END of the chain, and that is the caller's judgement to make.
//
// Nothing in this package parses gh's stderr to classify anything. The English
// text of "you are not logged in" is not an interface: gh rewords its messages
// and a matcher against them fails SILENTLY — the check keeps passing and
// nobody learns anything. Exit status and the presence or absence of a token on
// stdout are the whole contract.
//
// Residual, stated: `gh auth token` answers for gh's DEFAULT host. A fleet
// authenticated only against a GitHub Enterprise host (GH_ENTERPRISE_TOKEN, or
// a hosts.yml entry that is not the default) would read as SourceNone here.
// Every repo this fleet watches is on github.com.
//
// # Secret discipline
//
// The value is held in memory, handed to os.Setenv, and never returned to a
// caller, logged, or included in an error string. Result reports only WHERE the
// token came from, never what it is. The shell probe's stderr is deliberately
// discarded rather than folded into the error text: shell init files can print
// things, and an error message is a thing that gets logged and mailed.
//
// # Staleness
//
// Like pathenv, this runs once at startup, so a token rotated afterwards is not
// picked up until pogod restarts. Stated rather than hidden. The failure mode is
// the benign one — lookups go back to indeterminate, which the detector already
// reports loudly and never mistakes for "closed".
package ghtoken

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// probeTimeout bounds the shell probe. A user's shell init can be slow (the
// nvm/rvm lazy-loading on this box is why `zsh -i` is banned elsewhere in the
// fleet), but it cannot be allowed to hold up daemon startup indefinitely.
const probeTimeout = 15 * time.Second

// Source records where the token came from. It is the whole observable output
// of this package — deliberately, because the alternative observable output
// would be the token.
type Source string

const (
	// SourceAmbient: the process already had GH_TOKEN (or GITHUB_TOKEN, which
	// gh also honours). Nothing was done. This is what an interactive shell,
	// a crew agent, and a `pogo` run from a terminal all see.
	SourceAmbient Source = "ambient"
	// SourceShell: the environment had no token and one was harvested from a
	// user shell. This is the launchd case — the one this package exists for.
	SourceShell Source = "shell"
	// SourceGH: neither the environment nor the user shell had one, and `gh auth
	// token` returned the credential gh already holds (typically written to
	// hosts.yml by `gh auth login`). The last link in the chain, and the only one
	// that creates no new copy of the secret — see the package doc.
	SourceGH Source = "gh-auth-token"
	// SourceNone: no credential could be established, by ANY of the three
	// sources. Since `gh auth token` covers the `gh auth login` case this package
	// used to be blind to, this is now a decidable statement about gh's default
	// host rather than a heuristic: gh will not authenticate. Callers may act on
	// it, and cmd/pogod does.
	SourceNone Source = "none"
)

// Attempt records one source that was tried and did not yield. Existence-only,
// like everything else here: it names the source and a reason that has been
// through exitOnly or validate, never a value and never a probe's output.
type Attempt struct {
	Source Source
	Err    error
}

// Result describes what Ensure did. It never carries the token value.
type Result struct {
	// Source is where the token came from, or SourceNone.
	Source Source
	// Err explains a failed harvest — the FIRST source's reason, which is the
	// most specific one on a host that has a shell export and no gh login. Never
	// contains the token, and never contains a probe's output.
	Err error
	// Tried lists every source attempted and why it did not yield, in order. On
	// a SourceNone result this is the whole diagnosis, and it is what lets one
	// log line distinguish "no shell export" from "no gh login" from both.
	Tried []Attempt
}

// OK reports whether a GitHub credential is now available to this process and
// its children — which, for every source here, means GH_TOKEN is set in the
// environment.
//
// This is the POSITIVE CREDENTIAL PREDICATE the intake detector arms on. It is
// only worth that much because of SourceGH: without it, !OK() included every
// host authenticated by `gh auth login`.
func (r Result) OK() bool { return r.Source != SourceNone }

// String renders the result for a log line. Existence-only by construction:
// there is no branch of this method that can reach the value.
//
// It NAMES THE WINNING SOURCE, and that is load-bearing rather than tidy. The
// "harvested from the user shell" line is the evidence that refuted gh#113's
// premise — 163 occurrences in pogod.log proved the launchd daemon was not
// tokenless. With more than one source in the chain, a line that does not say
// WHICH one won can no longer answer the question it just answered.
func (r Result) String() string {
	switch r.Source {
	case SourceAmbient:
		return "GH_TOKEN: present (source=ambient) — already in the process environment"
	case SourceShell:
		return "GH_TOKEN: present (source=shell) — absent from the environment, harvested from the user shell"
	case SourceGH:
		return "GH_TOKEN: present (source=gh-auth-token) — absent from the environment and from the user " +
			"shell, read from the credential `gh` already holds"
	default:
		var b strings.Builder
		b.WriteString("GH_TOKEN: ABSENT (source=none) — no GitHub credential could be established")
		for _, a := range r.Tried {
			fmt.Fprintf(&b, "; %s: %v", a.Source, a.Err)
		}
		// States which sources were asked and lets their own reasons above carry
		// the rest. An unconditional "including `gh auth login` — that path is
		// checked" would be FALSE on a host with no gh on PATH, where the probe
		// could not run: a claim about coverage that is wrong exactly when
		// coverage is missing.
		b.WriteString(". `gh auth token` is in the chain, so a credential written by `gh auth login` " +
			"is covered whenever gh itself is reachable — the per-source reasons above say whether " +
			"it was")
		return b.String()
	}
}

// Ensure makes GH_TOKEN available to this process and to every subprocess it
// spawns afterwards. Call it once, early, alongside pathenv.Ensure — Go's
// os/exec copies the parent environment at exec time, so fixing it once fixes
// every `gh` invocation thereafter.
//
// It is a no-op when a token is already present, which is why it is safe to
// call from the CLI as well as the daemon: in an authed shell it does nothing
// and costs nothing.
func Ensure() Result {
	return ensure(os.Getenv, os.Setenv, func() (string, error) {
		return shellHarvest(UserShell())
	}, ghAuthToken)
}

// ensure is the injectable core, so every branch — including the failure
// branches — is reachable without a shell, a network, or a real secret.
//
// The sources are tried in order and the FIRST that yields a plausible token
// wins. Shell before gh, deliberately: the shell probe is what this box actually
// uses (163 startup lines' worth), it needs no subprocess of gh's own, and it
// answers for whatever the operator configured rather than for whatever gh was
// last logged into.
func ensure(getenv func(string) string, setenv func(string, string) error,
	shellHarvest, ghHarvest func() (string, error)) Result {
	// GITHUB_TOKEN counts: gh reads it when GH_TOKEN is unset, so a process that
	// has it is already authenticated and must not be second-guessed.
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if strings.TrimSpace(getenv(k)) != "" {
			return Result{Source: SourceAmbient}
		}
	}

	res := Result{Source: SourceNone}
	for _, src := range []struct {
		name    Source
		harvest func() (string, error)
	}{
		{SourceShell, shellHarvest},
		{SourceGH, ghHarvest},
	} {
		tok, err := src.harvest()
		if err == nil {
			err = validate(tok)
		}
		if err == nil {
			if serr := setenv("GH_TOKEN", tok); serr != nil {
				err = fmt.Errorf("setenv: %w", serr)
			} else {
				res.Source = src.name
				return res
			}
		}
		// Not a fault — just a source that had nothing. Recorded so the one log
		// line can say which sources were asked, and fallen through.
		res.Tried = append(res.Tried, Attempt{Source: src.name, Err: err})
		if res.Err == nil {
			res.Err = err
		}
	}
	return res
}

// validate rejects a probe result that cannot be a token. The checks are shape
// only — no prefix matching, because GitHub has shipped several token formats
// (40-hex, ghp_, github_pat_) and a validator that knows today's list is a
// validator that rejects tomorrow's. Crucially, no branch of this function puts
// the candidate into the error text.
func validate(tok string) error {
	if tok == "" {
		return fmt.Errorf("the user shell reported no GH_TOKEN")
	}
	if len(tok) < 8 || len(tok) > 4096 {
		return fmt.Errorf("the user shell reported a GH_TOKEN of implausible length (%d bytes)", len(tok))
	}
	for _, r := range tok {
		if r <= ' ' || r == 0x7f {
			// Almost always shell init noise on stdout rather than a token.
			return fmt.Errorf("the user shell's GH_TOKEN output contains whitespace or control characters — " +
				"probably shell-init output rather than a token")
		}
	}
	return nil
}

// UserShell picks the shell to probe. $SHELL when it names an executable (the
// user's own choice), else zsh on macOS where it is the login default, else
// /bin/sh. Under launchd $SHELL is typically unset, which is precisely the case
// the fallbacks cover.
func UserShell() string {
	if sh := os.Getenv("SHELL"); filepath.IsAbs(sh) {
		if st, err := os.Stat(sh); err == nil && !st.IsDir() {
			return sh
		}
	}
	if runtime.GOOS == "darwin" {
		if st, err := os.Stat("/bin/zsh"); err == nil && !st.IsDir() {
			return "/bin/zsh"
		}
	}
	return "/bin/sh"
}

// probeFlags returns the flags that make a shell read the user's environment
// init. zsh sources ~/.zshenv on EVERY invocation, so a plain -c suffices and
// avoids the far heavier (and, under load, hang-prone) interactive startup.
// Every other shell only reads its init as a login shell, so they get -lc.
func probeFlags(shell string) []string {
	if strings.HasPrefix(filepath.Base(shell), "zsh") {
		return []string{"-c"}
	}
	return []string{"-lc"}
}

// ProbeScript prints the token with no trailing newline and no diagnostics. The
// GITHUB_TOKEN fallback mirrors gh's own precedence.
//
// Exported, with ProbeCommand, so the launchd-minimal-environment test can run
// the SAME probe under a sealed environment. Sealing it in production code
// would drop GIT_CEILING_DIRECTORIES from the child (internal/gitceiling), so
// the seal lives where it belongs: in the test that needs it.
const ProbeScript = `printf %s "${GH_TOKEN:-${GITHUB_TOKEN:-}}"`

// ProbeCommand returns the full argument list for probing shell.
func ProbeCommand(shell string) []string {
	return append(probeFlags(shell), ProbeScript)
}

// shellHarvest runs the probe and returns whatever the shell printed.
//
// The child's environment is deliberately left nil — os/exec reads that as
// "inherit the parent's" — so the probe carries pogod's GIT_CEILING_DIRECTORIES
// like every other subprocess (see internal/gitceiling). A sealed environment
// here would drop the ceiling; the minimal-environment reproduction that tests
// this belongs in the test, which builds its own Cmd from ProbeArgs and
// ProbeScript.
//
// stderr is captured to a throwaway buffer rather than to the returned error:
// shell init can print, and the error string ends up in logs and mail.
func shellHarvest(shell string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, ProbeCommand(shell)...)
	cmd.Stderr = nil // discarded on purpose; see the doc comment above.
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s environment probe timed out after %s", filepath.Base(shell), probeTimeout)
	}
	if err != nil {
		// Report the exit status only. The shell's own output is not repeated.
		return "", fmt.Errorf("%s environment probe failed (exit status only, output withheld): %w",
			filepath.Base(shell), exitOnly(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// GHTokenCommand is the argv `gh auth token` probe runs. Exported so a test can
// assert the exact command rather than a paraphrase of it: `gh auth token`
// prints the credential and nothing else, while `gh auth status` prints a
// human-readable report whose wording is not an interface (see the package doc
// on why nothing here reads gh's prose).
var GHTokenCommand = []string{"auth", "token"}

// ghAuthToken asks gh for the credential it already holds.
//
// Every non-success is an ORDINARY outcome reported as an error and nothing
// more: gh absent from PATH, gh present but never logged in, gh slow. None of
// them is a fault of this package's, none of them is logged as one, and the
// caller decides whether the END of the chain is alarmable.
//
// The exit STATUS is the whole contract. gh's stderr is discarded by exitOnly
// for two independent reasons: it can be prose that a matcher would silently
// stop recognising after a gh release, and it is a string that ends up in logs
// and mail.
func ghAuthToken() (string, error) {
	bin, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("gh is not on PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, GHTokenCommand...)
	cmd.Stderr = nil // discarded on purpose; see the doc comment above.
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("`gh auth token` timed out after %s", probeTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("`gh auth token` exited non-zero (%w) — ordinary on a host that has "+
			"never run `gh auth login`", exitOnly(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// exitOnly strips an ExitError's captured stderr — Cmd.Output attaches it when
// Stderr is nil — so shell-init output cannot travel into a log line.
func exitOnly(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("exit status %d", ee.ExitCode())
	}
	return err
}
