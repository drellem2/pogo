package selfdrift

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallSet is every binary install.sh places on $PATH, mirroring that
// script's BINARIES list.
//
// It is DELIBERATELY WIDER than DeployedCmds, because the two lists answer
// different questions. DeployedCmds is "what does a deploy own" — the axes
// compare those against main, and adding a name there changes the build/restart
// verdict. This list is "what else of ours is on this box", which is a scan
// with no verdict attached, and `lsp` and `pose` are exactly the copies the two
// answers differ on: they are installed, they are not deploy-compared, and
// nothing was looking at them at all.
var InstallSet = []string{"pogo", "pogod", "lsp", "pose"}

// Shadow is one copy of a pogo binary that is on $PATH and is NOT the copy that
// would run.
//
// WHY THIS EXISTS. InstalledBin resolves through exec.LookPath — "what would
// ACTUALLY run if you typed the name" — and that is the right answer to the
// drift question. It is also the reason a box can carry a five-month-old build
// of every binary and be reported clean: on 2026-03-20 an install.sh run left
// frozen copies of pogod, lsp and pose in /usr/local/bin, and for five and a
// half months every instrument on the box looked past them at ~/go/bin, because
// ~/.zprofile prepends ~/go/bin ahead of /usr/local/bin (mg-dabf).
//
// The protection was never a property of the box. It was a property of how a
// shell happens to be invoked: measured on that host, `zsh -c -l` resolved
// ~/go/bin/pogod while `bash -lc` — which gets /etc/profile's path_helper
// ordering and no pogo prepend — resolved /usr/local/bin/pogod, all three
// names, to the March build. A missing binary errors loudly; a wrong binary of
// the right name starts, serves, and is simply wrong.
//
// A shadow is therefore reported, not judged as drift: nothing is running it
// today, and a report that says otherwise would be describing PATH order rather
// than the machine.
type Shadow struct {
	// Name is the binary name, e.g. "pogod".
	Name string `json:"name"`
	// Path is the shadowed copy, as it sits on $PATH.
	Path string `json:"path"`
	// Winner is the copy that wins resolution — what typing the name runs.
	Winner string `json:"winner"`
	// Revision is the shadowed file's vcs stamp, or RevUnstamped. Empty for a
	// benign shadow that is the same file as Winner, where there is nothing to
	// compare.
	Revision string `json:"revision,omitempty"`
	// Note says what kind of shadow this is, in the report's own words.
	Note string `json:"note"`
	// Benign is true when this copy cannot be a wrong-version hazard: it
	// resolves to the same file the winner does (a symlink — the fix), or it is
	// a separate file carrying the same revision.
	Benign bool `json:"benign"`
}

// HostShadows walks $PATH and reports every copy of an InstallSet binary that
// loses resolution to an earlier one.
//
// It returns nothing when a name has one copy, which is the ordinary case; the
// interesting output is the box with two.
func HostShadows() []Shadow {
	var out []Shadow
	for _, name := range InstallSet {
		copies := pathCopies(name)
		if len(copies) < 2 {
			continue
		}
		winner := copies[0]
		winnerReal := realPath(winner)
		winnerRev := BinaryRev(winner)
		for _, path := range copies[1:] {
			s := Shadow{Name: name, Path: path, Winner: winner}
			if realPath(path) != winnerReal {
				s.Revision = BinaryRev(path)
			}
			s.Note, s.Benign = shadowVerdict(winner, winnerRev, s.Revision, realPath(path) == winnerReal)
			out = append(out, s)
		}
	}
	return out
}

// shadowVerdict decides whether one losing copy is a hazard. It is pure — every
// filesystem question is answered by the caller and arrives as an argument — so
// the four outcomes can be exercised without manufacturing four binaries.
func shadowVerdict(winner, winnerRev, rev string, sameFile bool) (note string, benign bool) {
	if sameFile {
		// The mg-015f remedy, and the one applied in mg-dabf:
		// /usr/local/bin/mg is a symlink to ~/go/bin/mg, so the two entries are
		// one file and the loser cannot drift away from the winner.
		return "resolves to the same file as " + winner + " — cannot go stale", true
	}
	switch {
	case rev == RevUnstamped || rev == RevMissing || rev == "":
		// Not assumed stale and not assumed current: a file that carries no
		// stamp has told us nothing, and calling it either would be a claim we
		// did not measure. It stays a hazard, because "unknown build" is not a
		// reason to stop looking at it.
		return "a SEPARATE file from " + winner + " with NO vcs stamp — provenance UNKNOWN", false
	case rev == winnerRev:
		return "a separate file, but the same revision as " + winner, true
	default:
		return "SHADOWED STALE COPY — a different build from " + winner, false
	}
}

// pathCopies returns every executable file named name on $PATH, in resolution
// order, deduplicated by directory.
//
// An empty $PATH entry means the current directory to the shell, and is skipped
// rather than resolved: a scan whose answer depends on where the operator
// happened to be standing is not a description of the box.
func pathCopies(name string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		p := filepath.Join(dir, name)
		if isExecutableFile(p) {
			out = append(out, p)
		}
	}
	return out
}

// isExecutableFile stats through symlinks: a symlinked entry is a real
// candidate for execution, and is in fact the shape the fix takes.
func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0111 != 0
}

// realPath resolves symlinks, falling back to the path itself. The fallback is
// deliberate: EvalSymlinks failing must not make two entries look like the same
// file, which is the answer that would hide a hazard.
func realPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// noteShadows records the shadow finding on the report and appends it to
// Action.
//
// IT DOES NOT MOVE Status. `.drift.status` is a documented gate value
// (docs/operations.md) meaning "are the three axes in agreement", and widening
// it here would silently change what every existing caller of that field is
// asking. ShadowHazard is its own field for the same reason a sentinel is its
// own value in this package: two different findings must not arrive at a
// consumer as the same one.
//
// Action DOES carry it, because Action is the line a human reads, and a clean
// verdict printed beside three frozen binaries with nothing to connect them is
// the reassuring answer this whole package exists to stop producing.
func noteShadows(r *Report) {
	var haz []string
	for _, s := range r.Shadowed {
		if s.Benign {
			continue
		}
		haz = append(haz, fmt.Sprintf("%s at %s (%s), shadowed by %s", s.Name, s.Path, Short(s.Revision), s.Winner))
	}
	if len(haz) == 0 {
		return
	}
	r.ShadowHazard = true
	r.Action += fmt.Sprintf("  SHADOWED COPIES (%d): %s. Nothing is running these today — they lose $PATH resolution — but that is a property of how a shell is invoked, not of the box: a login shell that does not prepend the winner's directory (e.g. `bash -lc`, which gets /etc/profile's path_helper ordering) resolves them instead, and gets a wrong binary that starts and serves rather than an error. Replace each with a symlink: ln -sfn <winner> <path>.",
		len(haz), strings.Join(haz, "; "))
}
