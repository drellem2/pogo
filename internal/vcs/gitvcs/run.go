// Package gitvcs is the git backend of internal/vcs. It was built from
// internal/refinery's git calls without changing them (slice 1, mg-a18a3):
// the runner is the refinery's former gitCmdOutput, the Lander is the git
// half of its attemptMerge pipeline, and Refs is its default-branch lookup,
// --auto-create-target and --post-merge-tag.
package gitvcs

import (
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/ghtoken"
)

// CommitterName / CommitterEmail is the identity git uses to author and commit
// the commits a rebase replay creates. The refinery's clones have no local
// user.name/user.email, and pogod runs under launchd/systemd — often with no
// global/system git config and a username git can't auto-derive into a valid
// ident ("fatal: empty ident name (for <runner@host>) not allowed"). Supplying
// an explicit identity via the environment makes the replay self-contained
// (ia-1428, gh #7).
const (
	CommitterName  = "pogo refinery"
	CommitterEmail = "refinery@pogo.local"
)

// IdentityEnv returns GIT_AUTHOR_*/GIT_COMMITTER_* environment entries that
// fall back to the refinery identity for any that aren't already set in the
// process environment. A pre-existing non-empty value (a developer's shell
// identity, or a test's seeded identity) takes precedence; an unset or empty
// value gets the default. Appended after os.Environ(), these entries win over
// any empty same-key values inherited from the environment (Go's exec uses the
// last value for duplicate keys).
func IdentityEnv() []string {
	defaults := map[string]string{
		"GIT_AUTHOR_NAME":     CommitterName,
		"GIT_AUTHOR_EMAIL":    CommitterEmail,
		"GIT_COMMITTER_NAME":  CommitterName,
		"GIT_COMMITTER_EMAIL": CommitterEmail,
	}
	var env []string
	for k, def := range defaults {
		if os.Getenv(k) == "" {
			env = append(env, k+"="+def)
		}
	}
	return env
}

// Runner runs git commands for one caller. LogPrefix is the caller's log tag
// ("refinery"), so a git failure is attributed to the subsystem that ran it.
type Runner struct {
	LogPrefix string
}

// Run runs a git command in dir and captures combined stdout/stderr, trimmed.
// Returns the output and any error. Keeping git's error text lets callers log
// it and store it in failure records, rather than losing it to pogod's
// stdout/stderr.
func (r Runner) Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// pogod runs under launchd/systemd with no TTY. Without disabling
	// interactive prompts, an HTTPS remote with no credentials makes git
	// hang forever waiting for a username on stdin. Force prompts off so
	// auth failures fail fast and callers can detect them from the output.
	//
	// Also supply a committer/author identity so rebase replays don't fail
	// with "Committer identity unknown" when no ambient git config is
	// available (ia-1428, gh #7).
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, IdentityEnv()...)
	// A network op against an https github.com remote authenticates through
	// `gh auth git-credential`, which reads GH_TOKEN from THIS child's env;
	// pogod no longer carries one of its own (mg-37183).
	if ghtoken.GitNeedsCredential(args) {
		cmd.Env = ghtoken.ChildEnv(cmd.Env)
	}
	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if err != nil {
		if note, benign := BenignOutcome(out); benign {
			log.Printf("%s: git %v: %s (expected outcome)", r.LogPrefix, args, note)
		} else {
			log.Printf("%s: git %v failed: %s: %v", r.LogPrefix, args, out, err)
		}
	}
	return out, err
}

// benignOutcomes maps a git failure's COMPLETE output text to a plain-words
// description of the expected outcome it represents. The key is matched by
// exact string equality against Run's trimmed combined output — not against
// the command, and not against a prefix.
//
// That distinction is the whole point, not a detail (mg-5d3f). The refinery
// calls `git rebase --abort` unconditionally to clear crash debris, so the
// abort fails on every clean clone: 245 of these landed in one 50,603-line log,
// all of them reading `git [rebase --abort] failed`, none of them an error.
// Benign lines at error level teach a reader to skip error lines.
//
// Suppressing by COMMAND would silence the whole class, so a genuinely new
// `rebase --abort` failure — a concurrent git process holding .git/index.lock
// is a real one the refinery can hit, since gitgc and agents touch the same
// worktrees — would be swallowed on the day it first appeared. Matching the
// full text silences only the outcome measured to be benign; any other wording
// fails the equality test and still logs as a failure, which is where a new
// variant belongs. The safe form self-invalidates when the world changes.
//
// Add an entry only for an outcome measured to be benign in every occurrence.
var benignOutcomes = map[string]string{
	"fatal: no rebase in progress": "no rebase in progress, nothing to abort",
}

// BenignOutcome reports whether git output is a known expected outcome rather
// than a failure, returning the description to log in its place.
func BenignOutcome(out string) (string, bool) {
	note, ok := benignOutcomes[out]
	return note, ok
}
