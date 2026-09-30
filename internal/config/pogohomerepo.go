package config

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// PogoHomeLiveCommitPath is the one sentence every refusal of a $POGO_HOME
// merge path gives as the way that works instead (decision on mg-feecc).
// It does not claim the push job is running: mg-a48a is where it is tracked.
const PogoHomeLiveCommitPath = "commit directly in the live checkout at $POGO_HOME (stage by path — the nightly deploy and pogod also write there); pushing the live tree to origin is a scheduled job's job (mg-a48a), not the refinery's"

// RepoIsPogoHome reports whether repo names the $POGO_HOME tree — the live
// checkout pogod runs from — and returns the resolved PogoHome it compared
// against, for the caller's message.
//
// Why this exists (mg-752a3): a refinery merge lands on the repo's ORIGIN, and
// the live checkout at $POGO_HOME never pulls. So for this one repo a merge is
// never live; it forks a second history beside the commits agents make
// directly in the live tree (~/.pogo reached 26 local / 14 origin divergence,
// and mg-ff1e's change landed twice). The decision on mg-feecc: the live
// checkout is the ONLY write path for $POGO_HOME, and the refinery path is
// refused rather than discouraged — a prompt rule saying "don't submit
// ~/.pogo" is exactly what already failed.
//
// The comparison is on RESOLVED absolute paths, so "~/.pogo", a relative path,
// a trailing slash and a symlink to the tree (macOS /tmp -> /private/tmp is
// the everyday case) all compare equal. It also asks git, when repo is a git
// checkout, for the repo's shared git dir: a linked worktree of $POGO_HOME (a
// polecat tree under $POGO_HOME/polecats cut from the live repo) shares its
// origin, so a merge submitted from it forks the live tree identically.
//
// It fails OPEN on a repo it cannot resolve at all — that repo is refused
// elsewhere for not existing — but a path that merely fails EvalSymlinks is
// still compared lexically.
func RepoIsPogoHome(repo string) (bool, string) {
	home := resolvePathForCompare(PogoHome())
	if strings.TrimSpace(repo) == "" || home == "" {
		return false, home
	}
	r := resolvePathForCompare(repo)
	if r == home {
		return true, home
	}
	// A linked worktree (or any subdirectory) of the $POGO_HOME repo: its
	// common git dir is $POGO_HOME/.git.
	out, err := exec.Command("git", "-C", r, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return false, home
	}
	common := resolvePathForCompare(strings.TrimSpace(string(out)))
	if common != "" && common == resolvePathForCompare(filepath.Join(home, ".git")) {
		return true, home
	}
	return false, home
}

// resolvePathForCompare expands a leading ~, makes p absolute, and resolves
// symlinks when it can. A path that does not (yet) exist is returned cleaned
// and absolute, so a lexical match still counts.
func resolvePathForCompare(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = expandTildePath(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return filepath.Clean(p)
}
