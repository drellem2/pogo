package gitgc

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// Build-output pruning for KEPT trees whose owner is not live (drellem2/pogo#282,
// change C).
//
// # Why this phase exists
//
// A dead polecat's tree is kept whenever it holds any uncommitted change, or its
// ticket has not concluded (mg-ee02). That keep is right, but it keeps the WHOLE
// tree: one uncommitted file pins a 2.3G cargo target/ beside it, and the
// harness scratchpad beside the tree too (p4cd3e's held ~20G of release builds).
// Measured 2026-11-04 by mg-9b893: 9.6G of dead polecat target/ alone.
//
// # What it may remove, and what it never removes
//
// ONLY build output a build can regenerate:
//
//   - in a git tree, a path git itself reports as IGNORED
//     (`git ls-files --others --ignored --exclude-standard --directory`);
//   - in an enth checkout, a `target/` beside a Cargo.toml, a `.lake/build`, or
//     a `node_modules`, none holding a path enth's index tracks;
//   - in a harness scratchpad, the same three names, none holding a path a git
//     repository inside the scratchpad tracks.
//
// NEVER a non-ignored untracked file: those trees are kept because they hold
// someone's work. Never anything holding a .git entry (a nested repository is
// work, whatever ignores it), never pogod's own .claude/, never through a
// symlink. A tree whose state cannot be read is left alone.
//
// # Report first
//
// Deletion runs only when Options.PruneBuildOutput is set AND the sweep is not a
// dry run. Otherwise every sweep, pogod's periodic one included, REPORTS what it
// would prune and how big it is, so the report can be read before anything is
// deleted. pogod sets PruneBuildOutput from `[gitgc] prune_build_output = true`;
// `pogo gc --apply --prune-build-output` is the manual form.

// BuildOutputAction records the prune decision for one kept tree or scratchpad.
type BuildOutputAction struct {
	// Root is the tree or scratchpad pruned inside.
	Root string
	// Owner is the dead polecat the tree belongs to.
	Owner string
	// Kind says which rule chose the paths: "git-ignored", "enth build output"
	// or "scratchpad build output".
	Kind string
	// Paths are the pruned (or would-be-pruned) paths, relative to Root.
	Paths []string
	// Bytes is their total apparent size, measured before removal.
	Bytes int64
	// Left lists candidates left alone, each with why.
	Left []string
}

// String renders one prune action as a GC log line.
func (a BuildOutputAction) String() string {
	s := fmt.Sprintf("%s (owner %s, %s): %d path(s), %s", a.Root, a.Owner, a.Kind, len(a.Paths), humanBytes(a.Bytes))
	if len(a.Paths) > 0 {
		shown := a.Paths
		if len(shown) > 5 {
			shown = shown[:5]
		}
		s += " [" + strings.Join(shown, ", ")
		if len(a.Paths) > len(shown) {
			s += fmt.Sprintf(", +%d more", len(a.Paths)-len(shown))
		}
		s += "]"
	}
	if len(a.Left) > 0 {
		s += fmt.Sprintf("; left %d: %s", len(a.Left), strings.Join(a.Left, "; "))
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// buildOutputApply reports whether this sweep deletes build output, rather than
// only reporting it.
func (o Options) buildOutputApply() bool { return o.PruneBuildOutput && !o.DryRun }

// ownerLive reports whether owner, or any hyphen-prefix of it, is a live
// polecat. The prefix arm is keepSessionTemp's: a slug flattens '/', and a
// "<name>-remake1" tree is <name>'s.
func ownerLive(opts Options, owner string) bool {
	if owner == "" || opts.LivePolecats[owner] {
		return true // an unknown owner is never "not live"
	}
	for i := 0; i < len(owner); i++ {
		if owner[i] == '-' && i > 0 && opts.LivePolecats[owner[:i]] {
			return true
		}
	}
	return false
}

// pruneKeptBuildOutput is the prune phase. It runs after every other worktree
// phase, over what they KEPT: a removed tree takes everything with it already.
func pruneKeptBuildOutput(opts Options, res *Result) {
	scratch := map[string]string{} // scratchpad path -> owner
	trees := map[string]bool{}
	for _, w := range sortedWorktrees(res.WorktreesKept) {
		if ownerLive(opts, w.Owner) || trees[w.Path] {
			continue
		}
		trees[w.Path] = true
		if opts.SessionTempDirs != nil {
			for _, d := range opts.SessionTempDirs(w.Path) {
				scratch[d] = w.Owner
			}
		}
		if opts.BuildOutputSeen != nil {
			if opts.BuildOutputSeen[w.Path] {
				continue
			}
			opts.BuildOutputSeen[w.Path] = true
		}
		var a BuildOutputAction
		var err error
		switch {
		case enthvcs.IsCheckout(w.Path):
			a, err = enthBuildOutput(w.Path)
		case isGitTree(w.Path):
			a, err = gitIgnoredOutput(w.Path)
		default:
			// An orphan dir with no VCS: nothing can say what is ignored.
			continue
		}
		a.Root, a.Owner = w.Path, w.Owner
		if err != nil {
			opts.logf("left build output in kept tree %s (owner %s) alone: %v", w.Path, w.Owner, err)
			continue
		}
		applyBuildOutput(opts, a, res)
	}
	for _, s := range res.SessionTempKept {
		if !ownerLive(opts, s.Owner) {
			scratch[s.Path] = s.Owner
		}
	}
	paths := make([]string, 0, len(scratch))
	for p := range scratch {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if opts.BuildOutputSeen != nil {
			if opts.BuildOutputSeen[p] {
				continue
			}
			opts.BuildOutputSeen[p] = true
		}
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() {
			continue // most scratchpads are gone already; a symlink is not ours
		}
		a, err := scratchBuildOutput(p)
		a.Root, a.Owner = p, scratch[p]
		if err != nil {
			opts.logf("left build output in scratchpad %s (owner %s) alone: %v", p, scratch[p], err)
			continue
		}
		applyBuildOutput(opts, a, res)
	}
}

func isGitTree(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// applyBuildOutput deletes a's paths when the switch is on, or reports them.
func applyBuildOutput(opts Options, a BuildOutputAction, res *Result) {
	if len(a.Paths) == 0 {
		if len(a.Left) > 0 {
			opts.logf("left build output alone in %s", a.String())
		}
		return
	}
	if !opts.buildOutputApply() {
		opts.logf("would prune build output %s", a.String())
		res.BuildOutputPruned = append(res.BuildOutputPruned, a)
		return
	}
	var done []string
	for _, rel := range a.Paths {
		p := filepath.Join(a.Root, rel)
		if err := os.RemoveAll(p); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("prune build output %s: %v", p, err))
			continue
		}
		done = append(done, rel)
	}
	a.Paths = done
	opts.logf("pruned build output %s", a.String())
	res.BuildOutputPruned = append(res.BuildOutputPruned, a)
}

// measure walks one candidate, summing apparent sizes, and refuses it if it
// holds a .git entry anywhere (a nested repository is work, not output).
func measure(path string) (int64, error) {
	var n int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" && p != path {
			return fmt.Errorf("holds a repository at %s", p)
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n, err
}

// gitIgnoredOutput lists what git reports as ignored in a git tree.
func gitIgnoredOutput(root string) (BuildOutputAction, error) {
	a := BuildOutputAction{Kind: "git-ignored"}
	out, err := runOutput("git", "-C", root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return a, fmt.Errorf("git ls-files --ignored: %w", err)
	}
	for _, rel := range bytes.Split(out, []byte{0}) {
		r := strings.TrimSuffix(string(rel), "/")
		if r == "" {
			continue
		}
		if r == ".claude" || strings.HasPrefix(r, ".claude/") {
			continue // pogod's own files, not build output
		}
		addCandidate(&a, root, r)
	}
	return a, nil
}

// addCandidate measures one candidate and records it as a path or as left.
func addCandidate(a *BuildOutputAction, root, rel string) {
	p := filepath.Join(root, rel)
	fi, err := os.Lstat(p)
	if err != nil {
		a.Left = append(a.Left, fmt.Sprintf("%s: %v", rel, err))
		return
	}
	var n int64
	if fi.IsDir() {
		if n, err = measure(p); err != nil {
			a.Left = append(a.Left, fmt.Sprintf("%s: %v", rel, err))
			return
		}
	} else {
		n = fi.Size()
	}
	a.Paths = append(a.Paths, rel)
	a.Bytes += n
}

// isBuildOutputDir reports whether dir (with name base) is one of the named
// build-output shapes: target/ beside a Cargo.toml, .lake/build, node_modules.
func isBuildOutputDir(dir, base string) bool {
	switch base {
	case "node_modules":
		return true
	case "build":
		return filepath.Base(filepath.Dir(dir)) == ".lake"
	case "target":
		// Beside a Cargo.toml, or cargo's own CACHEDIR.TAG inside it: a
		// CARGO_TARGET_DIR pointed into a scratchpad has no Cargo.toml beside
		// it (measured: p860f7's scratchpad/target, 1.0G).
		if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "Cargo.toml")); err == nil {
			return true
		}
		_, err := os.Stat(filepath.Join(dir, "CACHEDIR.TAG"))
		return err == nil
	}
	return false
}

// findNamedOutput walks root for the named build-output dirs, not descending
// into a match, nor into .git, .enth, .claude or a symlink.
func findNamedOutput(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // an unreadable subtree is simply not a candidate
		}
		if !d.IsDir() || p == root {
			return nil
		}
		switch d.Name() {
		case ".git", ".enth", ".claude":
			return filepath.SkipDir
		}
		if isBuildOutputDir(p, d.Name()) {
			rel, _ := filepath.Rel(root, p)
			found = append(found, rel)
			return filepath.SkipDir
		}
		return nil
	})
	return found, err
}

// enthBuildOutput lists the named build output in an enth checkout that enth's
// index tracks nothing under.
func enthBuildOutput(root string) (BuildOutputAction, error) {
	a := BuildOutputAction{Kind: "enth build output"}
	st, err := enthvcs.ReadState(root)
	if err != nil {
		return a, fmt.Errorf("read enth state: %w", err)
	}
	found, err := findNamedOutput(root)
	if err != nil {
		return a, err
	}
	for _, rel := range found {
		if t := trackedUnder(st.Entries, filepath.ToSlash(rel)); t != "" {
			a.Left = append(a.Left, fmt.Sprintf("%s: enth tracks %s", rel, t))
			continue
		}
		addCandidate(&a, root, rel)
	}
	return a, nil
}

func trackedUnder(entries map[string]string, rel string) string {
	for p := range entries {
		if p == rel || strings.HasPrefix(p, rel+"/") {
			return p
		}
	}
	return ""
}

// scratchBuildOutput lists the named build output in a harness scratchpad. One
// inside a git repository or an enth checkout is a candidate only if that
// repository or checkout tracks nothing under it.
func scratchBuildOutput(root string) (BuildOutputAction, error) {
	a := BuildOutputAction{Kind: "scratchpad build output"}
	found, err := findNamedOutput(root)
	if err != nil {
		return a, err
	}
	for _, rel := range found {
		p := filepath.Join(root, rel)
		if why := trackedInEnclosingRepo(root, p); why != "" {
			a.Left = append(a.Left, fmt.Sprintf("%s: %s", rel, why))
			continue
		}
		addCandidate(&a, root, rel)
	}
	return a, nil
}

// trackedInEnclosingRepo finds the nearest git repository or enth checkout
// enclosing p, between p's parent and root inclusive, and says why p must be
// left if that repository tracks anything under it, or could not be asked. ""
// means p may go: nothing encloses it, or what does tracks nothing there.
func trackedInEnclosingRepo(root, p string) string {
	for d := filepath.Dir(p); strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if enthvcs.IsCheckout(d) {
			st, err := enthvcs.ReadState(d)
			if err != nil {
				return fmt.Sprintf("enth checkout %s unreadable: %v", d, err)
			}
			rel, _ := filepath.Rel(d, p)
			if t := trackedUnder(st.Entries, filepath.ToSlash(rel)); t != "" {
				return fmt.Sprintf("enth checkout %s tracks %s", d, t)
			}
			return ""
		}
		if isGitTree(d) {
			out, err := runOutput("git", "-C", d, "ls-files", "-z", "--", p)
			if err != nil {
				return fmt.Sprintf("git ls-files in %s: %v", d, err)
			}
			if len(bytes.TrimRight(out, "\x00")) > 0 {
				return "tracked by " + d
			}
			return ""
		}
		if d == root || d == filepath.Dir(d) {
			break
		}
	}
	return ""
}
