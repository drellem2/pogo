package gitgc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/hookarm"
)

// writeArtefact writes a file at rel under wtPath, creating its directories.
func writeArtefact(t *testing.T, wtPath, rel string) {
	t.Helper()
	p := filepath.Join(wtPath, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// newBareIgnoreRepo is newTestRepo with the user's global excludes switched
// off. This host's ~/.config/git/ignore lists **/.claude/settings.local.json,
// which would make the settings half of these tests pass without the code
// under test doing anything.
func newBareIgnoreRepo(t *testing.T) *testRepo {
	t.Helper()
	r := newTestRepo(t)
	r.git("config", "core.excludesFile", os.DevNull)
	return r
}

// pogodArtefacts writes what pogod itself puts into an agent's tree: the hook
// settings at the root, a stamp beside them, and a stamp under a subdirectory
// the session cd'd into (the stamp follows the cwd, gh #240).
func pogodArtefacts(t *testing.T, wtPath string) {
	t.Helper()
	writeArtefact(t, wtPath, hookarm.SettingsRelPath)
	writeArtefact(t, wtPath, filepath.Join(".claude", hookarm.StampName))
	writeArtefact(t, wtPath, filepath.Join("sub", ".claude", hookarm.StampName))
}

// TestWorktreeDirtyIgnoresPogodArtefacts is gh #162/#155/#240: a tree holding
// nothing but the files pogod wrote into it is not uncommitted work. The
// fixture repo's .gitignore does NOT list .claude/, which is the shape that
// bites — pogo's own repo ignores it and so never showed the defect.
func TestWorktreeDirtyIgnoresPogodArtefacts(t *testing.T) {
	r := newBareIgnoreRepo(t)
	if err := os.MkdirAll(filepath.Join(r.dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	r.commit(filepath.Join("sub", "keep.txt"), "keep")
	r.branch("polecat-artefacts")
	wtPath := r.worktree("polecat-artefacts")

	pogodArtefacts(t, wtPath)

	isDirty, files, err := WorktreeDirty(wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if isDirty {
		t.Fatalf("a tree holding only pogod's own artefacts must read clean, got %v", files)
	}

	// And the guard acts on it: gc removes the tree instead of preserving it.
	if err := RemoveWorktree(r.dir, wtPath, OwnerUnproven); err != nil {
		t.Fatalf("RemoveWorktree must remove a tree holding only pogod's artefacts: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone, stat err = %v", err)
	}
}

// TestWorktreeDirtyStillSeesRealWorkBesideArtefacts is the other half: the
// exclusion is by exact identity, untracked only, so anything else still counts.
func TestWorktreeDirtyStillSeesRealWorkBesideArtefacts(t *testing.T) {
	cases := []struct {
		name string
		// setup runs in the worktree after pogod's artefacts are written.
		setup func(t *testing.T, r *testRepo, wtPath string)
		want  string // a path the dirty list must name
	}{
		{
			name: "untracked file elsewhere",
			setup: func(t *testing.T, r *testRepo, wtPath string) {
				writeArtefact(t, wtPath, "wip.go")
			},
			want: "wip.go",
		},
		{
			name: "other untracked file inside .claude",
			setup: func(t *testing.T, r *testRepo, wtPath string) {
				writeArtefact(t, wtPath, filepath.Join(".claude", "notes.md"))
			},
			want: ".claude/notes.md",
		},
		{
			name: "settings file below the root is not pogod's",
			setup: func(t *testing.T, r *testRepo, wtPath string) {
				writeArtefact(t, wtPath, filepath.Join("sub", ".claude", "settings.local.json"))
			},
			want: "sub/.claude/settings.local.json",
		},
		{
			name: "stamp name outside a .claude directory",
			setup: func(t *testing.T, r *testRepo, wtPath string) {
				writeArtefact(t, wtPath, hookarm.StampName)
			},
			want: hookarm.StampName,
		},
		{
			name: "tracked file modified",
			setup: func(t *testing.T, r *testRepo, wtPath string) {
				writeArtefact(t, wtPath, "seed.txt")
			},
			want: "seed.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newBareIgnoreRepo(t)
			if err := os.MkdirAll(filepath.Join(r.dir, "sub"), 0755); err != nil {
				t.Fatal(err)
			}
			r.commit(filepath.Join("sub", "keep.txt"), "keep")
			r.branch("polecat-mixed")
			wtPath := r.worktree("polecat-mixed")
			pogodArtefacts(t, wtPath)
			tc.setup(t, r, wtPath)

			isDirty, files, err := WorktreeDirty(wtPath)
			if err != nil {
				t.Fatal(err)
			}
			if !isDirty {
				t.Fatalf("real work beside pogod's artefacts must read dirty")
			}
			if len(files) != 1 || !strings.HasSuffix(files[0], tc.want) {
				t.Errorf("dirty list = %v, want exactly one entry naming %s "+
					"(pogod's own artefacts must not be listed)", files, tc.want)
			}

			var dwe *DirtyWorktreeError
			if err := RemoveWorktree(r.dir, wtPath, OwnerUnproven); !errors.As(err, &dwe) {
				t.Fatalf("want a *DirtyWorktreeError, got %T: %v", err, err)
			}
		})
	}
}

// TestWorktreeDirtyCountsTrackedArtefactChanges: a repo that COMMITS a file at
// an artefact's path has a version of it in HEAD, and a change to it is a
// change git can see. Excluding by name must not reach tracked entries.
func TestWorktreeDirtyCountsTrackedArtefactChanges(t *testing.T) {
	r := newBareIgnoreRepo(t)
	if err := os.MkdirAll(filepath.Join(r.dir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	r.commit(hookarm.SettingsRelPath, "{}")
	r.branch("polecat-tracked")
	wtPath := r.worktree("polecat-tracked")

	writeArtefact(t, wtPath, hookarm.SettingsRelPath) // modify the tracked copy

	isDirty, files, err := WorktreeDirty(wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isDirty || len(files) != 1 || !strings.HasSuffix(files[0], "settings.local.json") {
		t.Fatalf("a modified TRACKED settings file must read dirty, got dirty=%v files=%v",
			isDirty, files)
	}
}
