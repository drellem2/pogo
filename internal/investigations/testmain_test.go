package investigations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any resolves onto the developer's live tree.
//
// This package searches a directory it is handed and has no default rooted in
// the operator's state, so the isolation is cheap here. It is adopted anyway,
// and TestCorpusResolutionHasNoLiveFallback below is the positive control that
// keeps the claim true rather than assumed — the failure mode this whole
// package exists to fight is an instrument that quietly reads a different
// population than the one it names.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("investigations")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestCorpusResolutionHasNoLiveFallback: FindCorpus resolves upward from the
// directory it is given and nowhere else. If a future edit adds a fallback to
// $HOME, ~/.pogo or a compiled-in path, a search run from an unrelated tree
// would silently answer from a corpus the caller never named — and its results
// would look identical to a correct search.
func TestCorpusResolutionHasNoLiveFallback(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	empty := filepath.Join(sandbox.Root, "no-corpus-here")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := FindCorpus(empty)
	if err == nil {
		t.Fatalf("FindCorpus(%s) resolved to %s from a tree with no corpus; "+
			"a search would answer from a population the caller never named", empty, got)
	}
}
