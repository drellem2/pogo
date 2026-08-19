package workitem

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSidecar(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".result.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

func TestReadResultFindsASidecarInDone(t *testing.T) {
	root := t.TempDir()
	writeSidecar(t, filepath.Join(root, "done"), "mg-1234", `{"verdict":"pass"}`)

	got, err := ReadResultFrom(root, "mg-1234")
	if err != nil {
		t.Fatalf("ReadResultFrom: %v", err)
	}
	if got != `{"verdict":"pass"}` {
		t.Errorf("got %q", got)
	}
}

// The archive race is the whole reason this reader exists in this shape: the
// coordinator archives a merged item by id during its cleanup pass, so an item
// can leave done/ at any moment after it closes. A reader that knew only done/
// would find the verdict or not depending on which side of that archive it ran.
// (This said "the refinery runs `mg archive --days=0` right after a merge"
// until mg-eadd. That call went in 2026-03-26; the race did not.)
func TestReadResultReachesAnAlreadyArchivedItem(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, filepath.Join(root, "archive", "2026-08"), "mg-145f", `{"verdict":"pass"}`)

	got, err := ReadResultFrom(root, "mg-145f")
	if err != nil {
		t.Fatalf("ReadResultFrom: %v", err)
	}
	if got != `{"verdict":"pass"}` {
		t.Errorf("an archived item's verdict was not reachable: got %q", got)
	}
}

// done/ outranks the archive, and the newest month outranks older ones: a
// re-closed item's current record must not be shadowed by an older copy.
func TestReadResultPrefersDoneThenTheNewestArchiveMonth(t *testing.T) {
	root := t.TempDir()
	writeSidecar(t, filepath.Join(root, "archive", "2026-06"), "mg-9999", `{"attempt":1}`)
	writeSidecar(t, filepath.Join(root, "archive", "2026-08"), "mg-9999", `{"attempt":2}`)

	got, _ := ReadResultFrom(root, "mg-9999")
	if got != `{"attempt":2}` {
		t.Errorf("expected the newest archive month to win, got %q", got)
	}

	writeSidecar(t, filepath.Join(root, "done"), "mg-9999", `{"attempt":3}`)
	got, _ = ReadResultFrom(root, "mg-9999")
	if got != `{"attempt":3}` {
		t.Errorf("expected done/ to outrank the archive, got %q", got)
	}
}

// No sidecar is not an error. An item closed by a bare `mg done` has none, and
// a consumer must be able to report "no verdict recorded" as a fact rather than
// as a store failure.
func TestAnItemWithNoSidecarIsNotAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ReadResultFrom(root, "mg-nope")
	if err != nil {
		t.Fatalf("absent sidecar reported as an error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestReadResultOnAnEmptyIDOrMissingRoot(t *testing.T) {
	if got, err := ReadResultFrom(t.TempDir(), ""); got != "" || err != nil {
		t.Errorf("empty id: got %q, %v", got, err)
	}
	if got, err := ReadResultFrom(filepath.Join(t.TempDir(), "nothing-here"), "mg-1234"); got != "" || err != nil {
		t.Errorf("missing root: got %q, %v", got, err)
	}
}
