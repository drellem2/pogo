package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SessionTempAction records the GC decision for one polecat's harness session
// temp dir (gh #203).
type SessionTempAction struct {
	Path string
	// Owner is the polecat whose working directory the temp dir was made for.
	Owner  string
	Reason string
}

// String renders one session-temp action as a GC log line.
func (a SessionTempAction) String() string {
	return fmt.Sprintf("%s (owner %s): %s", a.Path, a.Owner, a.Reason)
}

// sessionTempProbeName is a stand-in polecat name used to learn where a
// provider puts a polecats-dir workdir's temp dir, and how it spells the name
// inside that path. It is alphanumeric so every slug encoding keeps it intact.
const sessionTempProbeName = "POGOGCSESSIONTEMPPROBE"

// reclaimSessionTemp removes the harness session temp dirs made for workdir,
// whose owner the caller has ALREADY decided is reclaimable — it is only ever
// called beside a worktree or orphan-dir removal, under that removal's verdict,
// so it applies no gate of its own. reason is that verdict.
func reclaimSessionTemp(opts Options, owner, workdir, reason string, res *Result) {
	if opts.SessionTempDirs == nil || workdir == "" {
		return
	}
	for _, dir := range opts.SessionTempDirs(workdir) {
		removeSessionTemp(opts, SessionTempAction{Path: dir, Owner: owner, Reason: reason}, res)
	}
}

// removeSessionTemp deletes one session temp dir, or reports that it would.
// A path that does not exist is not an action: most polecats' temp dirs are
// gone already (the OS aged them out, or a previous sweep took them). A path
// that is not a real directory — a symlink above all — is left alone, because
// RemoveAll on it would be a removal of something the harness did not make.
func removeSessionTemp(opts Options, action SessionTempAction, res *Result) {
	fi, err := os.Lstat(action.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			res.Errors = append(res.Errors, fmt.Sprintf("stat session temp dir %s: %v", action.Path, err))
		}
		return
	}
	if !fi.IsDir() {
		return
	}
	if opts.DryRun {
		opts.logf("would remove session temp dir %s", action.String())
	} else {
		// Deleted, not relocated: the contents are scratch plus symlinks into
		// the harness's durable store, and a holding dir would only move the
		// unbounded growth somewhere else (gh #203).
		if err := os.RemoveAll(action.Path); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("remove session temp dir %s: %v", action.Path, err))
			return
		}
		opts.logf("removed session temp dir %s", action.String())
	}
	res.SessionTempRemoved = append(res.SessionTempRemoved, action)
}

// sweepOrphanSessionTemp reclaims the session temp dirs of polecats whose
// working directory under opts.PolecatsDir is already gone — reaped by an
// earlier sweep, before this phase existed, or by hand — so no worktree phase
// will ever reach them again (gh #203).
//
// Candidates are found by CONSTRUCTION, not by pattern: the probe below learns
// where a provider puts the temp dir for PolecatsDir/<name> and how <name> is
// spelled in it, and an entry is a candidate only if feeding its recovered name
// back through the provider reproduces its exact path. Crew slugs, non-pogo
// slugs and anything else under the temp root cannot pass that, and an encoding
// drift makes the phase miss rather than over-delete.
//
// Eligibility is sweepOrphanDirs' gate, keyed on the same owner name: never a
// live polecat, never an owner whose ticket has not concluded. Two further
// keeps are specific to a slug:
//
//   - the owner's directory still exists: the worktree phases own it, and
//     reclaim its temp dir alongside it under their own verdict;
//   - a hyphenated name has a prefix that is live or still has a directory: a
//     slug flattens '/', so "-polecats-ab12-cmd" is also how a session in
//     PolecatsDir/ab12/cmd is spelled, and that one belongs to ab12.
func sweepOrphanSessionTemp(opts Options, tickets TicketIndex, res *Result) {
	if opts.SessionTempDirs == nil || opts.PolecatsDir == "" {
		return
	}
	// Dirs phases 1 and 1b already reclaimed beside their tree this pass. In
	// an apply they are gone and never listed; in a dry run they still exist,
	// and their owner's directory does too, so without this they would be
	// reported a second time as kept — and a dry run would stop previewing
	// what --apply does (review round 1, mg-d0c10).
	taken := map[string]bool{}
	for _, a := range res.SessionTempRemoved {
		taken[a.Path] = true
	}
	// Trees phases 1 and 1b reclaimed this pass. The keeps below ask whether
	// a polecat's directory still exists; an apply has deleted these before
	// this phase runs and a dry run has not, so without this a dry run keeps a
	// "<name>-<sub>" temp dir (a session in a subdirectory of <name>'s tree)
	// that --apply removes (mg-fd3e5).
	reclaimed := reclaimedTrees(res)
	for _, probe := range opts.SessionTempDirs(filepath.Join(opts.PolecatsDir, sessionTempProbeName)) {
		root, base := filepath.Dir(probe), filepath.Base(probe)
		if strings.Count(base, sessionTempProbeName) != 1 {
			// The provider does not spell the name into the final element;
			// nothing here can be recovered safely.
			continue
		}
		pre, suf, _ := strings.Cut(base, sessionTempProbeName)
		entries, err := os.ReadDir(root)
		if err != nil {
			if !os.IsNotExist(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("read session temp root %s: %v", root, err))
			}
			continue
		}
		for _, e := range entries {
			entry := e.Name()
			if !strings.HasPrefix(entry, pre) || !strings.HasSuffix(entry, suf) || len(entry) <= len(pre)+len(suf) {
				continue
			}
			name := entry[len(pre) : len(entry)-len(suf)]
			path := filepath.Join(root, entry)
			if taken[path] || !constructsPath(opts, name, path) {
				continue
			}
			if reason, keep := keepSessionTemp(opts, tickets, reclaimed, name); keep {
				res.SessionTempKept = append(res.SessionTempKept, SessionTempAction{Path: path, Owner: name, Reason: reason})
				continue
			}
			_, state := tickets.OwnerState(name)
			removeSessionTemp(opts, SessionTempAction{
				Path: path, Owner: name, Reason: "orphan session temp dir, owner's ticket " + state.String(),
			}, res)
		}
	}
}

// constructsPath reports whether some provider's temp dir for
// PolecatsDir/<name> is exactly path.
func constructsPath(opts Options, name, path string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return false
	}
	for _, d := range opts.SessionTempDirs(filepath.Join(opts.PolecatsDir, name)) {
		if d == path {
			return true
		}
	}
	return false
}

// reclaimedTrees returns the directories phases 1 and 1b removed (or, in a dry
// run, would remove) this pass, under both the spelling they were reported
// with and their resolved path, since a worktree path comes from git, which
// canonicalises it, and PolecatsDir may not be.
func reclaimedTrees(res *Result) map[string]bool {
	out := map[string]bool{}
	for _, w := range res.WorktreesRemoved {
		out[filepath.Clean(w.Path)] = true
		if real, err := filepath.EvalSymlinks(w.Path); err == nil {
			out[real] = true
		}
	}
	return out
}

// polecatDirPresent reports whether PolecatsDir/<name> will still exist once
// this pass has done what it is doing: in a dry run a dir reclaimed this pass
// counts as absent, although it has not actually gone, so that the dry run
// keeps and removes exactly what --apply does. An apply asks the filesystem
// alone — the reclaimed dir is gone, and one recreated under the same name
// since phase 1 is really there and keeps its temp dirs. err is a failure to
// check.
func polecatDirPresent(opts Options, reclaimed map[string]bool, name string) (bool, error) {
	path := filepath.Join(opts.PolecatsDir, name)
	if opts.DryRun {
		if reclaimed[path] {
			return false, nil
		}
		if real, err := filepath.EvalSymlinks(path); err == nil && reclaimed[real] {
			return false, nil
		}
	}
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// keepSessionTemp applies the orphan-session-temp gate to one recovered owner
// name, returning the keep reason when the dir must stay.
func keepSessionTemp(opts Options, tickets TicketIndex, reclaimed map[string]bool, name string) (string, bool) {
	if opts.LivePolecats[name] {
		return "live polecat " + name, true
	}
	if present, err := polecatDirPresent(opts, reclaimed, name); err != nil {
		return fmt.Sprintf("owner's directory could not be checked (%v)", err), true
	} else if present {
		return "owner's directory still exists; the worktree phases own it", true
	}
	for i := 0; i < len(name); i++ {
		if name[i] != '-' {
			continue
		}
		prefix := name[:i]
		if prefix == "" {
			continue
		}
		if opts.LivePolecats[prefix] {
			return "may be a subdirectory session of live polecat " + prefix, true
		}
		if present, err := polecatDirPresent(opts, reclaimed, prefix); present || err != nil {
			return "may be a subdirectory session of polecat dir " + prefix, true
		}
	}
	if _, state := tickets.OwnerState(name); !state.Concluded() {
		return "owner's ticket " + state.String(), true
	}
	return "", false
}
