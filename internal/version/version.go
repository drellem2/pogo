package version

// The four stamp variables. Each is set by `-ldflags -X` at build time; see
// resolve.go for what happens when they are NOT set, which — until mg-3141 —
// was every binary this fleet actually ran.
//
// KEEP EXACTLY ONE LINE IN THIS FILE MATCHING `Version = `. scripts/
// bump-version.sh and scripts/check-version.sh both read the version with
// `grep 'Version = ' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/'`,
// which yields a multi-line answer the moment a second line matches. That is
// why the resolution logic lives in resolve.go rather than here.

// Version is set by goreleaser ldflags or bump-version.sh
var Version = "0.10.0"

// Build is the short commit hash, set by ldflags (build.sh, pogo-self-deploy,
// goreleaser). Empty means unstamped; resolve.go turns that into "unknown"
// rather than leaving a caller to guess.
var Build = ""

// Commit is the full commit hash, set by ldflags. It is the field the liveness
// question is asked of: `git merge-base --is-ancestor <fix> <commit>`.
var Commit = ""

// Branch is the git branch, set by ldflags. Nothing else can supply it — Go's
// automatic VCS stamping records a revision but never a branch — so an
// unstamped build has no second source for this field.
var Branch = ""

// Dirty is "true" when the tree had uncommitted changes at build time. A
// revision without this qualifier is a claim about the repo, not about the
// binary: the same SHA describes a clean build and a build with arbitrary
// local edits on top of it.
var Dirty = ""
