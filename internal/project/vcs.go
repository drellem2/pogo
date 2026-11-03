package project

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// VCS selection on the project record (docs/design/enth-vcs.md §2, slice 4,
// mg-160e8).

// ErrNotRegistered is SetVCS's answer for a path with no project record.
var ErrNotRegistered = errors.New("project not registered")

// BusyError is SetVCS's refusal: the repo has work in flight that was started
// under its current backend. Moving a repo between backends is a handover
// (§14.2), not a toggle, so the change waits until the repo is quiet.
type BusyError struct {
	Path    string
	From    vcs.Kind
	To      vcs.Kind
	Reasons []string
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("refusing to change the vcs of %s from %s to %s while it has work in flight: %s",
		e.Path, e.From, e.To, strings.Join(e.Reasons, "; "))
}

// BusyFunc reports what is in flight for the repo at repoPath (open MRs, live
// polecats), one human-readable line each. Empty means the repo is quiet.
type BusyFunc func(repoPath string) []string

// samePath compares registry paths, which carry a trailing separator, with
// the separator-less paths the refinery and agents carry.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func indexOf(path string) int {
	for i, p := range projects {
		if samePath(p.Path, path) {
			return i
		}
	}
	return -1
}

// VCSOf is the backend lookup by repo: the kind and enth settings the project
// record at repoPath names. A repo with no record, or a record with no vcs
// key, is git — which is what every repo was before selection existed.
func VCSOf(repoPath string) (vcs.Kind, *vcs.EnthRepo) {
	i := indexOf(repoPath)
	if i < 0 {
		return vcs.Git, nil
	}
	p := projects[i]
	if p.Enth != nil {
		e := *p.Enth
		e.WorkspacePaths = append([]string(nil), p.Enth.WorkspacePaths...)
		return p.Kind(), &e
	}
	return p.Kind(), nil
}

func sameEnth(a, b *vcs.EnthRepo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// onlyTokenFileDiffers reports whether the change from cur to (k, e) is the
// enth token file alone (mg-3a813). That is not a handover of backend — the
// repo stays on the same enthd and trunk — so it is not refused while work is
// in flight. A polecat already running keeps the token it was spawned with;
// pogo's own enth calls read the file afresh each time.
func onlyTokenFileDiffers(cur Project, k vcs.Kind, e *vcs.EnthRepo) bool {
	if cur.VCS != k || cur.Enth == nil || e == nil {
		return false
	}
	a, b := *cur.Enth, *e
	a.TokenFile, b.TokenFile = "", ""
	return a.Equal(b)
}

// SetVCS changes the backend recorded for the project at path and persists
// it. The selection is validated and normalized first (vcs.NormalizeSelection:
// git is stored as no key at all). Re-stating the current selection is a
// no-op success. Any real change is refused with a *BusyError when busy
// reports work in flight for the repo; busy may be nil only in tests. A change
// of the enth token file alone is not a change of backend and is never
// refused as busy (onlyTokenFileDiffers).
func SetVCS(path string, kind vcs.Kind, enth *vcs.EnthRepo, busy BusyFunc) (*Project, error) {
	k, e, err := vcs.NormalizeSelection(kind, enth)
	if err != nil {
		return nil, err
	}
	i := indexOf(path)
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotRegistered, path)
	}
	cur := projects[i]
	if cur.VCS == k && sameEnth(cur.Enth, e) {
		out := cur
		return &out, nil
	}
	if busy != nil && !onlyTokenFileDiffers(cur, k, e) {
		if reasons := busy(cur.Path); len(reasons) > 0 {
			to := k
			if to == "" {
				to = vcs.Git
			}
			return nil, &BusyError{Path: cur.Path, From: cur.Kind(), To: to, Reasons: reasons}
		}
	}
	projects[i].VCS = k
	projects[i].Enth = e
	SaveProjects()
	out := projects[i]
	return &out, nil
}
