package version

// resolve.go answers "what does this binary contain?" from inside the binary
// (mg-3141).
//
// THE DEFECT IT CLOSES. version.go's four fields were stamped by goreleaser and
// by nothing else, while both build paths this fleet runs — build.sh and
// scripts/pogo-self-deploy — were plain `go build` with no ldflags. So every
// locally built binary reported:
//
//	$ pogo version --json
//	{"branch":"","build":"dev","commit":"","version":"0.9.0"}
//
// The fields were present and EMPTY, which reads as "no revision info" rather
// than as an unstamped build. On 2026-08-13 four separate "is the fix live?"
// questions were answered by proxy — file mtimes, deploy times, and an
// inference about which revision a 03:00 build came from — because no binary
// could be asked.
//
// THREE THINGS THIS FILE DOES.
//
//  1. NEVER REPORT THE EMPTY STRING. An empty commit is indistinguishable from
//     a stamping bug in the reader. "unknown" is honest and greppable.
//
//  2. FALL BACK TO Go's OWN VCS STAMP, AND SAY SO. `go build` records
//     vcs.revision/vcs.modified in the binary automatically, and pogod's
//     GET /version has always read them. Reading them here means a binary built
//     by a path nobody remembered to patch still says something true — which is
//     the failure mode the ticket named: fix one script, leave the other
//     producing unstamped binaries.
//
//  3. NAME THE SOURCE, BECAUSE THE FALLBACK CAN BE CONFIDENTLY WRONG. Measured
//     while building this change: `go build ./cmd/pogo` inside a polecat
//     worktree stamped vcs.revision=d533d174 — the HEAD of ~/.pogo, which is
//     itself a git repo and which Go walked up into. That SHA does not exist in
//     the pogo repo at all (`git cat-file -e` fails on it), and vcs.modified was
//     true because ~/.pogo was dirty, while the pogo worktree was clean. So the
//     automatic stamp is not merely "missing the branch": in the directory
//     layout this fleet uses, it can name a foreign repository with total
//     confidence. `source` is what lets a reader tell that apart from an ldflags
//     stamp, whose repo was chosen by the build script. Without it this file
//     would have replaced one unanswerable question with a plausible wrong
//     answer, which is worse.
//
// The ldflags stamp is therefore authoritative and the build-info stamp is
// second-class, reported with its provenance attached.

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Unknown is the value every field carries when nothing could supply it. It is
// deliberately not "" — see (1) above — and deliberately not a valid SHA
// prefix, so `git merge-base --is-ancestor <fix> unknown` fails loudly instead
// of resolving to something.
const Unknown = "unknown"

// Where the revision came from. A reader that treats these as interchangeable
// has re-created the defect one layer up.
const (
	// SourceLdflags: stamped by the build script, which knew which repo it was
	// building. Trustworthy; the only source that can supply a branch.
	SourceLdflags = "ldflags"
	// SourceBuildInfo: Go's automatic vcs.revision. True about SOME repo — see
	// (3) above for why that is not the same as true about this one.
	SourceBuildInfo = "buildinfo"
	// SourceNone: nothing stamped anything. Every field is Unknown.
	SourceNone = "none"
)

// Info is the resolved, never-empty answer. It is what `pogo version --json`
// and `pogod -version` print, and it is the same shape from both so a caller
// holding either binary asks the same question the same way.
type Info struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	Commit  string `json:"commit"`
	Branch  string `json:"branch"`
	Dirty   bool   `json:"dirty"`
	Source  string `json:"source"`
}

// Stamped reports whether this binary can say what it contains at all.
func (i Info) Stamped() bool { return i.Source != SourceNone && i.Commit != Unknown }

// Describe is the one-line human form. prog is the binary's own name, because
// this package serves pogo, pogod, lsp and pose.
//
//	pogo 0.10.0 (fa02447, branch=main, source=ldflags)
//	pogo 0.10.0 (fa02447-dirty, branch=main, source=ldflags)
//	pogo 0.10.0 (unknown, branch=unknown, source=none)
//
// source is on the human line and not only in the JSON on purpose: the reading
// that gets quoted into a mail is this one, and a buildinfo-sourced revision
// quoted without its provenance is exactly the unstatable-proxy shape this
// ticket was filed about.
func (i Info) Describe(prog string) string {
	build := i.Build
	if i.Dirty {
		build += "-dirty"
	}
	return fmt.Sprintf("%s %s (%s, branch=%s, source=%s)", prog, i.Version, build, i.Branch, i.Source)
}

// vcsStamp is the subset of debug.BuildInfo this package reads. Separated out
// so resolve() is a pure function the unit tests can drive through every case
// without needing four differently-built binaries.
type vcsStamp struct {
	revision string
	modified bool
}

func buildInfoStamp() vcsStamp {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return vcsStamp{}
	}
	var s vcsStamp
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			s.revision = setting.Value
		case "vcs.modified":
			s.modified = setting.Value == "true"
		}
	}
	return s
}

// Get resolves this binary's identity.
func Get() Info {
	return resolve(Version, Build, Commit, Branch, Dirty, buildInfoStamp())
}

func resolve(version, build, commit, branch, dirty string, bi vcsStamp) Info {
	info := Info{Version: strings.TrimSpace(version)}
	if info.Version == "" {
		info.Version = Unknown
	}

	commit = strings.TrimSpace(commit)
	build = strings.TrimSpace(build)
	branch = strings.TrimSpace(branch)

	switch {
	case commit != "":
		info.Source = SourceLdflags
		info.Commit = commit
		info.Dirty = dirty == "true"
		info.Branch = orUnknown(branch)
		info.Build = orShort(build, commit)

	case strings.TrimSpace(bi.revision) != "":
		info.Source = SourceBuildInfo
		info.Commit = strings.TrimSpace(bi.revision)
		info.Dirty = bi.modified
		// Build info carries no branch, and inventing one from the ldflags
		// field would attach a branch to a revision that did not come with it.
		info.Branch = orUnknown(branch)
		info.Build = orShort(build, info.Commit)

	default:
		info.Source = SourceNone
		info.Commit = Unknown
		info.Branch = orUnknown(branch)
		info.Build = orUnknown(build)
	}
	return info
}

func orUnknown(s string) string {
	if s == "" {
		return Unknown
	}
	return s
}

// orShort prefers an explicitly stamped short build id, and otherwise derives
// one from the commit so the two can never disagree.
func orShort(build, commit string) string {
	if build != "" {
		return build
	}
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
