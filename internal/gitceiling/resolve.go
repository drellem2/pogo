package gitceiling

// Answering "which git work tree is this directory in?" honestly while the
// ceiling is in force (mg-490c).
//
// THE FALSE NEGATIVE. The guard this package installs bounds git's upward walk
// at POGO_HOME, so a lookup started anywhere strictly below it that finds no
// nearer .git fails with:
//
//	fatal: not a git repository (or any of the parent directories): .git
//
// That is the correct thing to do to an OPERATION — it is the whole point, and
// nothing here narrows it. It is the wrong thing to hand a QUESTION. Measured on
// this host while mg-015c was being built:
//
//	git -C ~/.pogo                          rev-parse --show-toplevel -> ~/.pogo
//	git -C ~/.pogo/agents                   rev-parse --show-toplevel -> fatal: not a git repository
//	git -C ~/.pogo/agents/pm/pm-pogo/memory rev-parse --show-toplevel -> fatal: not a git repository
//
// Twelve of the seventeen agent-state directories mg-015c enumerates are in that
// position: inside drellem2/pogo-config's work tree, tracked, with a remote —
// and each one answers "not a git repository".
//
// WHY THAT IS WORTH A HELPER. The wrong answer is shaped exactly like a clean
// result. mg-015c's first live run printed "<dir> is under no git work tree, so
// nothing there is pushed anywhere" twelve times, about directories inside a
// repo with a remote. Not an error, not an UNKNOWN — a sentence asserting
// safety, produced by a guard working as designed. The class is every instrument
// that asks whether a path is tracked, whether a tree is dirty, which remote a
// file would be pushed to, or whether that remote is public. Each of them either
// re-derives the workaround or does not notice it needs one, and the second
// outcome is silent.
//
// WHAT THIS DOES NOT DO. It does not unset, narrow or otherwise defeat the
// ceiling — that would trade a false negative for the silent escape the guard
// was built to stop. It never mutates the environment, and it issues exactly one
// read-only command, `rev-parse --show-toplevel`.
//
// The mechanism is the ceiling's own semantics, used forwards. A ceiling entry
// bounds a walk started BELOW it and does not exclude the working directory
// itself, so the walk git refused to finish can be resumed by asking again from
// the ceiling entry — which is a legitimate lookup, on a directory the guard
// deliberately leaves resolvable. ResolveWorkTree does that hop, keeps hopping
// while further entries bound the walk, and — this is the part that makes it a
// fix rather than a smarter workaround — RECORDS the hop in WorkTree.Ceilings.
// A caller that only wants to report gets the truth; a caller about to run
// something destructive can see that the root it is holding is one the guard
// hides from below, and is not entitled to pretend it walked there.
//
// LIMIT WORTH KNOWING. Resuming from a ceiling entry resumes an ordinary walk:
// if nothing above that entry is itself a ceiling, it can reach a repository
// ABOVE POGO_HOME (a git-init'd $HOME, say). That is the true answer to the
// question asked, and Ceilings is non-empty when it happens, which is the signal
// to treat it as a report and not as a target.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorkTree is the answer to "which git work tree is dir in?", together with what
// the ceiling did to the question.
type WorkTree struct {
	// Dir is the directory that was asked about, cleaned.
	Dir string
	// Toplevel is the root of the work tree containing Dir, empty when no
	// repository versions it. Empty is a DECIDED answer: a lookup that could
	// not be made returns a non-nil error instead, and callers must check that
	// before reading this field.
	Toplevel string
	// ResolvedFrom is the directory whose lookup produced Toplevel. It differs
	// from Dir exactly when the ceiling refused the direct lookup.
	ResolvedFrom string
	// Ceilings names the ceiling entries the lookup had to step onto to get an
	// answer, deepest first. Empty means the direct lookup answered and no
	// ceiling was in play — the ordinary case for any path outside POGO_HOME.
	Ceilings []string
}

// Versioned reports whether some repository versions Dir. Only meaningful when
// ResolveWorkTree returned a nil error.
func (w WorkTree) Versioned() bool { return w.Toplevel != "" }

// CeilingCrossed reports whether this answer required stepping onto a ceiling
// entry — i.e. whether a direct `git -C dir` would have said "not a git
// repository" about a directory that is, in fact, versioned.
//
// This is the distinction the raw git error cannot carry: a ceiling-refused
// lookup and a genuinely unversioned directory produce the same text.
func (w WorkTree) CeilingCrossed() bool { return len(w.Ceilings) > 0 }

// ResolveWorkTree reports which git work tree dir belongs to, resuming the walk
// across any GIT_CEILING_DIRECTORIES entry that bounds it.
//
// A nil error with Versioned() false means "nothing versions this directory" and
// can be reported as such. A non-nil error means the lookup could not be made —
// which is never the same fact, and must not be rendered as one.
func ResolveWorkTree(ctx context.Context, dir string) (WorkTree, error) {
	w := WorkTree{Dir: filepath.Clean(dir)}
	ceiling := os.Getenv(EnvVar)
	at := w.Dir
	for {
		top, err := toplevel(ctx, at)
		if err == nil {
			w.ResolvedFrom = at
			if top == "" {
				// A successful rev-parse that named nothing. Nothing observed
				// produces this, which is exactly why it leaves as an error: an
				// empty Toplevel behind a nil error is Versioned() == false,
				// which is the false-negative shape this file exists to remove,
				// arrived at from inside the fix.
				return w, &lookupError{dir: at, err: errNamedNoWorkTree}
			}
			w.Toplevel = top
			return w, nil
		}
		w.ResolvedFrom = at
		if !IsNotARepo(err) {
			// Anything else — no git on PATH, an unreadable directory, a
			// killed context — is "I could not look". Reporting it as "no
			// repository" is the failure this file exists to remove, so it
			// leaves as an error.
			return w, err
		}
		next := bounding(at, ceiling)
		if next == "" {
			// No ceiling bounds a walk from here, so git's refusal was the
			// walk finishing, not the guard interrupting it. Decided.
			return w, nil
		}
		// Strictly shorter each time (Bounding returns strict ancestors only),
		// so this terminates.
		w.Ceilings = append(w.Ceilings, next)
		at = next
	}
}

// Bounding returns the deepest GIT_CEILING_DIRECTORIES entry this process
// carries that bounds a repository lookup started in dir, or "" if none does.
//
// It is the test a caller running its own git needs in order to tell the two
// meanings of "not a git repository" apart: with a non-empty result, that error
// may be the guard refusing to look rather than a report that nothing is there.
func Bounding(dir string) string { return bounding(dir, os.Getenv(EnvVar)) }

// bounding is Bounding against an explicit ceiling value.
//
// The deepest entry is the one that stops the walk first, so it is the one to
// resume from. Entries git ignores are ignored here too — a relative entry
// bounds nothing, and the empty entry is git's symlink-free marker, not a path.
// Comparison is by resolved path and by whole components, because git matches
// ceiling entries against a getcwd() that has no symlinks in it, and because
// /a/bc does not live under /a/b.
//
// One divergence from git is deliberate, and it is deliberate because of which
// direction it errs in. Git skips resolving the entries that follow an empty
// one — that is what the empty marker means — so against a symlinked entry after
// a marker, git compares literally where this resolves. Resolving can only find
// MORE bounds than git applied, never fewer: git bounds on a literal entry only
// when that spelling is already a physical ancestor, which resolving preserves.
// A bound git did not apply costs one extra hop that re-asks a question already
// answered "no repository" and changes no result. A bound git applied and this
// missed would be the false negative back again, silently, which is why the
// error is taken on this side.
func bounding(dir, ceiling string) string {
	real := resolved(dir)
	best := ""
	for _, entry := range strings.Split(ceiling, string(filepath.ListSeparator)) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		e := resolved(entry)
		// A ceiling entry never excludes the working directory itself, so an
		// entry equal to dir bounds nothing about a lookup started there.
		if e == real || !under(real, e) {
			continue
		}
		if len(e) > len(best) {
			best = e
		}
	}
	return best
}

// IsNotARepo reports whether err is git's "there is no repository here". On its
// own it does NOT distinguish an unversioned directory from a ceiling-refused
// lookup — git spells both the same way. Bounding is the other half.
func IsNotARepo(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not a git repository")
}

// toplevel runs the one read-only command this file issues.
//
// cmd.Env is deliberately never set. os/exec reads an unset Env as "inherit the
// parent's", which is the only way GIT_CEILING_DIRECTORIES reaches this git at
// all — and this function's whole subject is what that variable does, so sealing
// the environment here would seal away the thing under discussion. The property
// is pinned for the module by TestSubprocessEnvironmentsInheritTheCeiling, which
// flagged an earlier draft of this file that took an env parameter for the
// convenience of its tests. It was right to.
func toplevel(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", &lookupError{dir: dir, err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return strings.TrimSpace(string(out)), nil
}

// errNamedNoWorkTree is the one failure git does not report itself.
var errNamedNoWorkTree = errors.New("git named no work tree root")

// lookupError folds git's stderr into the error, so a failure says why rather
// than "exit status 128" — the text is what IsNotARepo reads and what a report
// prints.
type lookupError struct {
	dir    string
	err    error
	stderr string
}

func (e *lookupError) Error() string {
	s := "git -C " + e.dir + " rev-parse --show-toplevel: " + e.err.Error()
	if e.stderr != "" {
		s += ": " + e.stderr
	}
	return s
}

func (e *lookupError) Unwrap() error { return e.err }

// resolved is filepath.EvalSymlinks with the raw path as a fallback, so a
// directory that does not exist still compares by name instead of vanishing
// from the comparison.
func resolved(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// under reports whether dir sits inside root, compared as path components so
// that /a/bc is not read as living under /a/b.
func under(dir, root string) bool {
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
