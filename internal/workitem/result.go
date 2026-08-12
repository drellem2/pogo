package workitem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ReadResult returns the verbatim contents of a completed work item's result
// sidecar (`<id>.result.json`), or "" when the item has none.
//
// It exists because the sidecar is NOT reachable through `mg show --json`:
// that command returns id/title/status/creator/tags/body and nothing else, so
// a consumer that wants the verdict a worker recorded has to read the file mg
// wrote beside the item. Measured against mg v0.3.x on 2026-08-12 — `mg show
// mg-145f --json | jq keys` lists no `result` key, and `.result` is null under
// jq only because the key is absent.
//
// An item with no sidecar returns ("", nil), not an error. That is the ordinary
// state of an item closed by a bare `mg done`, and a caller reporting the
// completion must be able to say "no verdict was recorded" rather than "the
// store failed" — the two are different facts and only one of them is a defect.
func ReadResult(id string) (string, error) { return ReadResultFrom(workspaceDir(), id) }

// ReadResultFrom is ReadResult against an explicit workspace root.
//
// It looks in done/ first and then in each archive/<YYYY-MM>/ directory, newest
// month first, because a completed item does not stay in done/ for long: the
// refinery runs `mg archive --days=0` after a successful merge, so an item can
// be archived within seconds of closing. A reader that only knew done/ would
// find the sidecar or not depending on which side of that call it ran — which
// is precisely the kind of timing-dependent silence this package's consumers
// exist to remove.
func ReadResultFrom(root, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	for _, dir := range resultSearchDirs(root) {
		path := filepath.Join(dir, id+".result.json")
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	}
	return "", nil
}

// resultSearchDirs is done/ followed by the archive month directories, newest
// name first. The month directories are named `YYYY-MM`, so a reverse
// lexicographic sort is a reverse chronological one.
func resultSearchDirs(root string) []string {
	dirs := []string{filepath.Join(root, "done")}
	archive := filepath.Join(root, "archive")
	entries, err := os.ReadDir(archive)
	if err != nil {
		return dirs
	}
	var months []string
	for _, e := range entries {
		if e.IsDir() {
			months = append(months, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	for _, m := range months {
		dirs = append(dirs, filepath.Join(archive, m))
	}
	return dirs
}
