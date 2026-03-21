package version

// Version is set by goreleaser ldflags or bump-version.sh
var Version = "0.1.1"

// Build is the short commit hash, set by goreleaser ldflags
var Build = "dev"

// Commit is the full commit hash, set by goreleaser ldflags
var Commit = ""

// Branch is the git branch, set by goreleaser ldflags
var Branch = ""
