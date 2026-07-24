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
	// SourceNone: no token could be established. gh may still authenticate from
	// its own config (`gh auth login` writes hosts.yml), so this is reported,
	// not fatal — but it is the state in which the teardown detector goes blind,
	// so it must be visible in the log rather than inferred from a wall of
	// indeterminate findings.
	SourceNone Source = "none"
)

// Result describes what Ensure did. It never carries the token value.
type Result struct {
	// Source is where the token came from, or SourceNone.
	Source Source
	// Err explains a failed harvest. Never contains the token, and never
	// contains the probe shell's output.
	Err error
}

// OK reports whether a token is now present in the process environment.
func (r Result) OK() bool { return r.Source == SourceAmbient || r.Source == SourceShell }

// String renders the result for a log line. Existence-only by construction:
// there is no branch of this method that can reach the value.
func (r Result) String() string {
	switch r.Source {
	case SourceAmbient:
		return "GH_TOKEN: already present in the environment"
	case SourceShell:
		return "GH_TOKEN: absent from the environment, harvested from the user shell"
	default:
		if r.Err != nil {
			return fmt.Sprintf("GH_TOKEN: unset and could not be harvested (%v) — "+
				"gh calls will fail unless gh is authenticated by other means", r.Err)
		}
		return "GH_TOKEN: unset and could not be harvested"
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
	})
}

// ensure is the injectable core, so every branch — including the failure
// branches — is reachable without a shell, a network, or a real secret.
func ensure(getenv func(string) string, setenv func(string, string) error, harvest func() (string, error)) Result {
	// GITHUB_TOKEN counts: gh reads it when GH_TOKEN is unset, so a process that
	// has it is already authenticated and must not be second-guessed.
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if strings.TrimSpace(getenv(k)) != "" {
			return Result{Source: SourceAmbient}
		}
	}

	tok, err := harvest()
	if err != nil {
		return Result{Source: SourceNone, Err: err}
	}
	if err := validate(tok); err != nil {
		return Result{Source: SourceNone, Err: err}
	}
	if err := setenv("GH_TOKEN", tok); err != nil {
		return Result{Source: SourceNone, Err: fmt.Errorf("setenv: %w", err)}
	}
	return Result{Source: SourceShell}
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

// exitOnly strips an ExitError's captured stderr — Cmd.Output attaches it when
// Stderr is nil — so shell-init output cannot travel into a log line.
func exitOnly(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("exit status %d", ee.ExitCode())
	}
	return err
}
