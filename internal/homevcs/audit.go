// Package homevcs answers one question about $POGO_HOME: is some git repository
// versioning the files that pogo itself writes there? (mg-3610)
//
// WHY THIS EXISTS. On this fleet's host, `~/.pogo` is the working tree of a
// second repository (`drellem2/pogo-config`) that nothing in the pogo repo
// mentions. That repo tracks the crew prompts — which genuinely live nowhere
// else — AND the files `InstallPrompts` writes: `agents/mayor.md`,
// `agents/crew/doctor.md`, `agents/templates/*.md`, plus the daemon-written
// `projects.json`. Tracking install output guarantees a permanently dirty tree,
// because every install rewrites those files; measured 2026-08-05 at 11 files
// changed, 431 insertions, 162 deletions, none of them hand edits.
//
// WHY A DETECTOR RATHER THAN A FIX. The remedy is a machine-local ops action in
// a live directory (see docs/pogo-home-version-control.md) — it is not pogo's to
// perform. What pogo can do is make the condition VISIBLE. It was not: several
// agents spent 2026-08-05 reasoning about installed-prompt staleness without
// knowing the second repo existed, and "is the installed prompt current?" had
// two defensible answers depending on whether you compared against pogo source
// or against pogo-config HEAD. Three generations of
// `agents/templates/polecat-build-pr.md` existed simultaneously, a fact
// deducible from neither repo alone.
//
// STRICTLY READ-ONLY, and deliberately so. This audit runs against a directory
// holding the live prompts, the mail spool, schedules and events.log for a
// running fleet. It issues `rev-parse`, `ls-files`, `status` and `remote
// get-url` and nothing else — no fetch, no pull, no checkout, no index rewrite
// (`status` runs under `--no-optional-locks`). A detector that mutates the tree
// it is judging has made itself a participant.
package homevcs

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
)

// Writer names what produces a path, which is the whole basis of the verdict: a
// file pogo writes cannot be versioned as a hand-authored artifact, because the
// next write dirties the tree again.
const (
	// WriterInstall is written by agent.InstallPrompts from the binary's
	// embedded corpus. Its source of truth is internal/agent/prompts in the
	// pogo repo, and `pogo check-staleness` already compares against it.
	WriterInstall = "install"
	// WriterDaemon is written by pogod at runtime. Machine-local state:
	// what this host has discovered, not what any other host should read.
	WriterDaemon = "daemon"
)

// ManagedPath is one path pogo writes under $POGO_HOME, named relative to the
// home directory with forward slashes.
type ManagedPath struct {
	Rel    string
	Writer string
}

// Finding is one managed path that a git repository tracks.
type Finding struct {
	// Path is relative to the git work tree root, not to $POGO_HOME — it is
	// what a reader would type at `git rm --cached`.
	Path string `json:"path"`
	// Rel is the same file relative to $POGO_HOME.
	Rel    string `json:"rel"`
	Writer string `json:"writer"`
	// Dirty means the working tree copy differs from the index right now:
	// pogo has written it since the last commit. This is the permanent state
	// of a tree that tracks install output, not a transient.
	Dirty bool `json:"dirty"`
}

// Report is the audit's whole answer, including the reason it has none.
type Report struct {
	Home string `json:"home"`
	// Versioned is whether Home sits inside a git work tree at all.
	Versioned bool `json:"versioned"`
	// Toplevel is that work tree's root; Remote its origin URL, best effort.
	Toplevel string `json:"toplevel,omitempty"`
	Remote   string `json:"remote,omitempty"`
	// Examined is how many pogo-written paths were looked for. It renders on
	// every report so a clean answer can be told apart from an empty one.
	Examined int `json:"examined"`
	// Tracked are the managed paths that repo versions, worst first
	// (dirty before clean, then by path).
	Tracked []Finding `json:"tracked,omitempty"`
	// Unknown, when non-empty, says why this audit decided nothing. It is
	// never rendered as a clean result — "no repo found" and "could not
	// look" are different facts and a reader must be able to tell them apart.
	Unknown string `json:"unknown,omitempty"`
}

// DirtyCount is how many tracked managed paths currently carry uncommitted
// pogo output.
func (r Report) DirtyCount() int {
	n := 0
	for _, f := range r.Tracked {
		if f.Dirty {
			n++
		}
	}
	return n
}

// ManagedPaths enumerates every path under $POGO_HOME that pogo writes for
// itself.
//
// The install set is derived from the binary's embedded corpus rather than a
// hand-kept list, so a prompt added to internal/agent/prompts is covered the
// day it ships — a list maintained by hand would silently stop covering the
// newest file, which is exactly the file most likely to be dirty.
func ManagedPaths() []ManagedPath {
	var out []ManagedPath
	// DefaultPromptsFS is already rooted AT the corpus, so p reads
	// "mayor.md" / "templates/polecat.md" — the same layout InstallPrompts
	// writes under $POGO_HOME/agents.
	_ = fs.WalkDir(agent.DefaultPromptsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		out = append(out, ManagedPath{
			Rel:    path.Join("agents", p),
			Writer: WriterInstall,
		})
		return nil
	})
	// projects.json is the daemon's discovered-project registry
	// (internal/project). Same class as install output for this purpose: pogo
	// rewrites it, so versioning it records one machine's scan as shared truth
	// and re-dirties on the next discovery.
	out = append(out, ManagedPath{Rel: "projects.json", Writer: WriterDaemon})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// Audit reports whether a git repository tracks the paths pogo writes under
// $POGO_HOME.
func Audit(ctx context.Context) Report {
	return auditHome(ctx, config.PogoHome(), ManagedPaths())
}

func auditHome(ctx context.Context, home string, managed []ManagedPath) Report {
	rep := Report{Home: home, Examined: len(managed)}

	// A home that does not exist is not a home with no repository — git would
	// fail with "cannot change to", which reads like "not a repo" and is not.
	// Say which one happened.
	if _, err := os.Stat(home); err != nil {
		rep.Unknown = "$POGO_HOME (" + home + ") could not be read, so nothing checked whether a repository versions it: " + err.Error()
		return rep
	}

	// --show-prefix is asked for in the same breath as --show-toplevel, and it
	// is what names $POGO_HOME's position INSIDE the work tree. Deriving that
	// with filepath.Rel instead looks equivalent and is not: on macOS, git
	// reports /private/var/... where the caller holds /var/..., so every
	// pathspec came out as "../../.." and the audit reported a permanently
	// clean tree — a false all-clear on exactly the machine that has the
	// defect.
	out, err := gitOut(ctx, home, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		// Two very different causes, and only one of them is "no repo". A
		// missing `git` means we looked at nothing; saying "not versioned"
		// there would be an unearned all-clear.
		if _, lookErr := exec.LookPath("git"); lookErr != nil {
			rep.Unknown = "git is not on PATH, so nothing checked whether a repository versions $POGO_HOME"
			return rep
		}
		if isNotARepo(err) {
			return rep
		}
		rep.Unknown = "could not determine whether $POGO_HOME is inside a git work tree: " + err.Error()
		return rep
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	rep.Versioned = true
	rep.Toplevel = strings.TrimSpace(lines[0])
	prefix := ""
	if len(lines) > 1 {
		prefix = strings.TrimSpace(lines[1])
	}
	if url, err := gitOut(ctx, rep.Toplevel, "remote", "get-url", "origin"); err == nil {
		rep.Remote = strings.TrimSpace(url)
	}

	// Name each managed path relative to the work tree root, since that is the
	// only spelling git accepts as a pathspec and the only one a reader can
	// paste. $POGO_HOME is usually the root itself, but need not be.
	byPath := map[string]ManagedPath{}
	var specs []string
	for _, m := range managed {
		spec := path.Join(prefix, m.Rel)
		byPath[spec] = m
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return rep
	}

	listed, err := gitOut(ctx, rep.Toplevel, append([]string{"ls-files", "-z", "--"}, specs...)...)
	if err != nil {
		rep.Unknown = "could not list tracked files in " + rep.Toplevel + ": " + err.Error()
		return rep
	}
	var tracked []string
	for _, name := range strings.Split(listed, "\x00") {
		if name == "" {
			continue
		}
		if _, ok := byPath[name]; ok {
			tracked = append(tracked, name)
		}
	}
	if len(tracked) == 0 {
		return rep
	}

	dirty := dirtyPaths(ctx, rep.Toplevel, tracked)
	for _, p := range tracked {
		m := byPath[p]
		rep.Tracked = append(rep.Tracked, Finding{Path: p, Rel: m.Rel, Writer: m.Writer, Dirty: dirty[p]})
	}
	sort.Slice(rep.Tracked, func(i, j int) bool {
		if rep.Tracked[i].Dirty != rep.Tracked[j].Dirty {
			return rep.Tracked[i].Dirty
		}
		return rep.Tracked[i].Path < rep.Tracked[j].Path
	})
	return rep
}

// dirtyPaths reports which of paths differ from the index right now.
//
// `--no-optional-locks` is load-bearing, not hygiene: a plain `git status`
// refreshes and rewrites the index, taking index.lock in a directory another
// process may be using. This runs inside a live fleet's home; it may read, and
// it may not write.
func dirtyPaths(ctx context.Context, top string, paths []string) map[string]bool {
	dirty := map[string]bool{}
	args := append([]string{"--no-optional-locks", "status", "--porcelain", "-z", "--"}, paths...)
	out, err := gitOut(ctx, top, args...)
	if err != nil {
		// A status that failed is not evidence of a clean tree. The tracked
		// findings still stand — dirtiness is detail on top of them.
		return dirty
	}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		dirty[entry[3:]] = true
		// A rename/copy entry is followed by its ORIGINAL path in the next
		// NUL field. Consuming it here keeps that path from being read as a
		// status line of its own.
		if strings.ContainsAny(xy, "RC") {
			i++
		}
	}
	return dirty
}

// isNotARepo distinguishes "there is no git repository here" — an ordinary,
// healthy answer — from every other reason rev-parse can fail. The home's
// existence is established before this runs, so the "cannot change to" spelling
// is not one of the cases it has to absorb.
func isNotARepo(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not a git repository")
}

// gitOut runs git in dir and folds stderr into the error, so a failure says why
// rather than just "exit status 128".
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", &gitError{args: args, err: err, stderr: msg}
		}
		return "", &gitError{args: args, err: err}
	}
	return string(out), nil
}

type gitError struct {
	args   []string
	err    error
	stderr string
}

func (e *gitError) Error() string {
	s := "git " + strings.Join(e.args, " ") + ": " + e.err.Error()
	if e.stderr != "" {
		s += ": " + e.stderr
	}
	return s
}

func (e *gitError) Unwrap() error { return e.err }
