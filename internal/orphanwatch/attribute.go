package orphanwatch

import (
	"path/filepath"
	"strings"
)

// OwnerFromCwd attributes a process to the polecat that owns its working
// directory, and is the whole reason this detector is possible.
//
// # Why cwd and not ppid
//
// The reported symptom of mg-4518 was `ppid=1` — a compute process reparented
// to launchd with no polecat above it. Reading that as the signature of a leak
// is the trap, and it was written into the ticket twice before it was measured
// out again: a polecat that starts background work in the ordinary way
// (`nohup ... & ` from a tool-call shell that then exits) produces EXACTLY that
// signature for every healthy worker it launches. On 2026-08-07 four live
// workers belonging to one running polecat all showed ppid=1 at 60-68% CPU, and
// a sweep keyed on that would have destroyed all four mid-computation.
//
// `ppid` is destroyed by reparenting. `cwd` is not: a working directory is a
// property of the process itself, survives the death of its parent, and on this
// fleet it carries the owning polecat's id in the path. So attribution comes
// from cwd, and the verdict comes from asking the agent registry whether that
// owner is still alive.
//
// # The two path shapes, and why the second one is derived rather than hardcoded
//
//	<root>/<id>[/...]            a worker started from the polecat's worktree
//	.../<slug(root)>-<id>/...    a worker started from the harness scratchpad
//
// The second is Claude Code's scratchpad convention: it names a session
// directory after the agent's working directory with every non-alphanumeric
// character replaced by '-', so /Users/daniel/.pogo/polecats/p00a1 becomes the
// component `-Users-daniel--pogo-polecats-p00a1`. That is where the first
// confirmed orphan of this class was found writing (`.../scratchpad/growth.txt`,
// last write 19:42, owner dead). Deriving the component from root rather than
// pasting the observed literal means a fleet whose polecats live somewhere else
// is covered by the same rule.
//
// # Fail closed
//
// ok is false for anything this cannot attribute, and a caller MUST treat that
// as "leave it alone", never as "orphan". A worker that chdir'd out of its
// polecat tree loses the marker, and not every runner starts children from a
// polecat-owned directory. Unattributable is a blind spot to be counted and
// reported, not a verdict.
// OwnerFromAnyRoot applies OwnerFromCwd against each spelling of the root and
// returns the first attribution.
//
// A root has more than one spelling because a working directory read out of the
// kernel is fully symlink-resolved and a configured path is not. On darwin
// /tmp, /var and every t.TempDir() live behind a symlink into /private, so a
// scan rooted at the configured spelling compares `/var/folders/...` against a
// cwd of `/private/var/folders/...` and attributes nothing — reporting a tree
// full of orphans as clean. That is not hypothetical: it is what the live probe
// for this package did on its first run.
//
// Both spellings are tried rather than only the resolved one because the
// harness-scratchpad shape (see OwnerFromCwd) slugifies the path the AGENT was
// given, which is the unresolved one.
func OwnerFromAnyRoot(roots []string, cwd string) (string, bool) {
	for _, root := range roots {
		if owner, ok := OwnerFromCwd(root, cwd); ok {
			return owner, true
		}
	}
	return "", false
}

func OwnerFromCwd(root, cwd string) (string, bool) {
	if root == "" || cwd == "" {
		return "", false
	}
	root = filepath.Clean(root)
	cwd = filepath.Clean(cwd)

	// Shape 1: literally under the polecats root.
	if id, ok := underRoot(root, cwd); ok {
		return id, true
	}

	// Shape 2: a slugified copy of the root appears as a path component.
	prefix := slugify(root) + "-"
	for _, part := range strings.Split(cwd, string(filepath.Separator)) {
		if !strings.HasPrefix(part, prefix) {
			continue
		}
		if id := part[len(prefix):]; validID(id) {
			return id, true
		}
	}
	return "", false
}

// underRoot returns the first path component below root, when cwd is root
// itself or lives beneath it.
//
// The equality case matters: the reproduction built for this ticket ran
// `nohup` from the polecat's own worktree, so the orphan's cwd was the root
// entry exactly — `/Users/daniel/.pogo/polecats/q4518` with nothing after it.
// A prefix rule that required a trailing separator would have attributed
// nothing and reported the constructed orphan as unattributable, which is to
// say it would have gone green on the case it was built to catch.
func underRoot(root, cwd string) (string, bool) {
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	id := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		id = rel[:i]
	}
	if !validID(id) {
		return "", false
	}
	return id, true
}

// slugify replaces every character that is not a letter or digit with '-',
// reproducing the harness's session-directory naming.
func slugify(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// validID rejects components that cannot be an agent name, so a stray path does
// not manufacture an owner that the registry will then fail to find and this
// will then call dead. An id that is not a real agent name would attribute the
// process to nobody and — with liveness answering "not registered" — convict it.
// The registry lookup is the gate, but a malformed id must never reach it.
func validID(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
