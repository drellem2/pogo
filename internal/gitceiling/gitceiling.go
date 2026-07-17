// Package gitceiling stops a git repository lookup from walking out of POGO_HOME.
//
// Git resolves "which repo am I in?" by walking up from the working directory
// until it finds a .git. Nothing about that walk is scoped to the caller's
// intent: if the directory git was pointed at has no .git of its own, the walk
// keeps climbing and silently succeeds on whatever repo it hits first. Under
// ~/.pogo that first hit is the fleet's own live config repo, so a polecat
// teardown or a refinery gc aimed at a worktree that lost its .git operates on
// ~/.pogo instead — with no error, because from git's side nothing went wrong.
//
// Every git repo pogod manages lives nested inside POGO_HOME, so every one of
// them is in that class:
//
//	polecats/*            worktrees, torn down and recreated constantly
//	refinery/worktrees/*  the refinery's structure — cannot be relocated
//	agents/*, pogo-pa     live agents' working dirs — cannot be relocated
//
// Relocating a nested repo out from under ~/.pogo also fixes it, but only for
// the repos that CAN move, and only while $HOME stays free of a .git — a
// property nobody here owns, that can change with no commit and no signal.
// Most of this population cannot move, so that mechanism cannot be the fix.
//
// GIT_CEILING_DIRECTORIES bounds the walk itself. Set to POGO_HOME, it stops
// the upward search BEFORE ~/.pogo is examined, so a lookup that would have
// silently escaped instead fails loudly with "not a git repository". It is
// ambient: git reads it from the environment on every invocation, which is what
// makes it reach call sites nobody audited and call sites that do not exist
// yet — including git run by an agent's harness, far outside this codebase.
//
// Two properties of git's own semantics make one ceiling entry sufficient and
// safe (both are pinned by tests in gitceiling_test.go):
//
//   - The ceiling never excludes the working directory itself, so a legitimate
//     git operation ON ~/.pogo still resolves. The guard blocks escaping into
//     ~/.pogo from below; it does not embargo the directory.
//   - A ceiling that is not an ancestor of the working directory is inert, so
//     repos outside ~/.pogo (~/dev/pogo, the source repos the refinery merges)
//     are unaffected.
//
// The ceiling is derived from config.PogoHome() — the same function every pogo
// state path derives from — and never from a list of the parents that happen to
// contain repos today. Such a list is a measurement, and this one is provably
// unstable: the population grows every time pogod dispatches a polecat. Deriving
// it means a nested repo under a parent nobody has thought of yet is covered on
// the day it is created.
//
// Because Go's os/exec hands the current process's environment to children that
// do not set cmd.Env, and because the sites here that DO set it build on
// os.Environ(), calling Ensure once at startup covers both pogod's own git
// invocations and every process it spawns — the same shape as pathenv.Ensure.
package gitceiling

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/config"
)

// EnvVar is the git environment variable that bounds the repository-lookup
// walk. Git parses it as a list separated by filepath.ListSeparator (':' on
// unix, ';' on Windows) — the same convention Go uses for PATH.
const EnvVar = "GIT_CEILING_DIRECTORIES"

// Compose returns the value EnvVar should carry so that ceiling bounds the
// walk, preserving whatever current already holds.
//
// Preserving is not politeness: a caller (or an operator's shell) may have set
// a ceiling for its own reasons, and clobbering it would silently widen a walk
// somebody else deliberately bounded — this package's exact complaint about
// git's default behavior. Entries are additive; more ceilings only ever stop a
// walk earlier.
//
// Compose is idempotent, so Ensure can run on a process that already inherited
// this ceiling from its parent (pogod -> agent -> git) without growing the
// value at every hop.
//
// A relative ceiling is refused rather than cleaned up. Git ignores non-absolute
// entries outright, so accepting one would produce a variable that looks set,
// reads as protection, and bounds nothing — the silent failure this package
// exists to remove. Better to fail where the bad value entered.
func Compose(current, ceiling string) (string, error) {
	if !filepath.IsAbs(ceiling) {
		return "", fmt.Errorf("ceiling %q is not absolute; git ignores relative %s entries", ceiling, EnvVar)
	}
	ceiling = filepath.Clean(ceiling)

	sep := string(filepath.ListSeparator)
	for _, entry := range strings.Split(current, sep) {
		// An empty entry is meaningful to git — it marks the remaining entries
		// as symlink-free — so entries are compared, not filtered, and a
		// current value is never rewritten, only appended to.
		if entry != "" && filepath.Clean(entry) == ceiling {
			return current, nil
		}
	}
	if current == "" {
		return ceiling, nil
	}
	return current + sep + ceiling, nil
}

// Ensure bounds every git repository lookup made by this process, and by every
// process it spawns, at POGO_HOME.
//
// Call it once at startup, before anything shells out to git or spawns an agent.
// It is idempotent and safe to call in a process that already inherited the
// ceiling.
func Ensure() error {
	home, err := filepath.Abs(config.PogoHome())
	if err != nil {
		return fmt.Errorf("resolve POGO_HOME: %w", err)
	}
	value, err := Compose(os.Getenv(EnvVar), home)
	if err != nil {
		return err
	}
	return os.Setenv(EnvVar, value)
}
