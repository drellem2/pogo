package enthvcs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/hookarm"
	"github.com/drellem2/pogo/internal/vcs"
)

// Workspaces on enth: one `enth checkout` per polecat (docs/design/enth-vcs.md
// §4, slice 5, mg-cc5cd).
//
// A workspace is DIR, an enth checkout made through the repo's enthd. There
// is no branch and no registry: nothing to collide on, nothing to prune.
// Everything the dir-only methods need (the origin, the base, the selection,
// pogod's record) is read from DIR, so Dirty, Describe, Root, Remove, Exclude
// and Refresh work without knowing which project DIR belongs to.

// DaemonExcludeGroup heads the patterns pogod writes into every enth
// workspace's .enth/info/exclude right after the checkout (§9).
const DaemonExcludeGroup = "# pogo daemon files (added automatically by pogo)"

// DaemonExcludePatterns are the files pogod itself writes into every agent's
// tree, as gitignore patterns: the same two files gitvcs.IsPogodArtefact
// carves out of `git status` (the hook settings at the root only, the
// mail-hook stamp in a .claude/ directory at any depth). On enth the exclude
// list keeps them out of `enth status` in the first place, so Dirty needs no
// carve-out (§9). A TRACKED file is never ignored (enth, as git), so a repo
// that tracks one of these still sees pogod's write to it as an edit.
func DaemonExcludePatterns() []string {
	return []string{
		"/" + filepath.ToSlash(hookarm.SettingsRelPath),
		"**/.claude/" + hookarm.StampName,
	}
}

func (b Backend) trunk() string {
	if b.Repo.Trunk != "" {
		return b.Repo.Trunk
	}
	return vcs.DefaultEnthTrunk
}

// ResolveBase is the ref a workspace for target is checked out at. enth
// resolves a ref on enthd at checkout time, so there is nothing to fetch and
// nothing to fall back through (W4's four-step chain is one call): target ""
// is the repo's trunk, a bare branch name is refs/heads/<name>, a full ref is
// itself.
func (b Backend) ResolveBase(_ context.Context, _ vcs.Repo, target string) string {
	switch {
	case target == "":
		return b.trunk()
	case strings.HasPrefix(target, "refs/"):
		return target
	default:
		return "refs/heads/" + target
	}
}

// BranchExists is false: an enth workspace has no branch of its own (W1), so
// no leftover can collide with a new one (W5).
func (Backend) BranchExists(context.Context, vcs.Repo, string) bool { return false }

// BranchSpent is false for the same reason, which makes "delete it to make
// room" unreachable.
func (Backend) BranchSpent(context.Context, vcs.Repo, string, string) bool { return false }

// DeleteBranch has nothing to act on.
func (b Backend) DeleteBranch(context.Context, vcs.Repo, string) error {
	return b.unsupported("delete branch (an enth workspace has no branch)")
}

// Contains is the adoption check (W7), which is slice 6's: adopting stranded
// enth work means checking out the worker's top unlanded Change, found by
// trailer.
func (b Backend) Contains(context.Context, vcs.Repo, string, string) error {
	return b.unsupported("contains (stranded adoption on enth is slice 6)")
}

// Create checks out the whole repo — or, when the project record narrows it,
// Repo.WorkspacePaths — at s.Base (default: the trunk) into s.Dir, through
// the repo's enthd (W2, W3, W4):
//
//	enth --remote URL --repo NAME checkout REF DIR [PATH...]
//
// then writes pogod's daemon-file excludes into DIR/.enth/info/exclude (§9)
// and pogod's workspace record (RecordRelPath). A failure is a
// *vcs.CommandError; enth's refusal code, when it printed one, is its Err as a
// *vcs.RefusedError. As on git, Create does not remove what a failed create
// left behind: the caller knows whether DIR was its own.
func (b Backend) Create(ctx context.Context, s vcs.WorkspaceSpec) (vcs.Workspace, error) {
	ref := s.Base
	if ref == "" {
		ref = b.trunk()
	}
	args := append(remoteArgs(b.Repo.Remote, b.Repo.Repo), "checkout", ref, s.Dir)
	args = append(args, b.Repo.WorkspacePaths...)
	if _, err := run(ctx, b.Bin, args...); err != nil {
		return vcs.Workspace{}, err
	}
	st, err := ReadState(s.Dir)
	if err != nil {
		return vcs.Workspace{}, fmt.Errorf("enth checkout of %s into %s succeeded but its state cannot be read: %w", ref, s.Dir, err)
	}
	for _, p := range DaemonExcludePatterns() {
		if err := excludePattern(s.Dir, DaemonExcludeGroup, p); err != nil {
			return vcs.Workspace{}, fmt.Errorf("enth workspace %s: write %s: %w", s.Dir, ExcludeRelPath, err)
		}
	}
	rec := Record{
		Repo: s.Repo.Path, Remote: b.Repo.Remote, EnthRepo: b.Repo.Repo,
		Ref: ref, Base: st.Base, Paths: append([]string(nil), b.Repo.WorkspacePaths...),
		CreatedAt: time.Now().UTC(),
	}
	if err := WriteRecord(s.Dir, rec); err != nil {
		return vcs.Workspace{}, fmt.Errorf("enth workspace %s: write %s: %w", s.Dir, RecordRelPath, err)
	}
	return vcs.Workspace{Dir: s.Dir, Repo: s.Repo, Base: st.Base}, nil
}

// Rollback removes a workspace this process just created (W6). There is no
// registration to undo: the checkout is the directory.
func (Backend) Rollback(_ context.Context, _ vcs.Repo, dir string) error {
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

// Remove deletes the workspace at dir (C6). It refuses a directory that is
// not an enth checkout root, so this backend can never be the one that
// deletes an arbitrary tree. An absent dir is already removed. Committed
// Changes are on enthd (§4, durability), so only Dirty's answer is at risk,
// and asking it is the caller's job, as on git.
func (Backend) Remove(_ context.Context, _ vcs.Repo, dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if !IsCheckout(dir) {
		return &vcs.CommandError{Args: []string{"rm", "-rf", dir},
			Err: fmt.Errorf("refusing to remove %s: it is not an enth checkout (no %s)", dir, StateRelPath)}
	}
	if err := os.RemoveAll(dir); err != nil {
		return &vcs.CommandError{Args: []string{"rm", "-rf", dir}, Err: err}
	}
	return nil
}

// statusOp is one edit line of `enth status`.
type statusOp struct {
	Path json.RawMessage `json:"path"`
	Op   string          `json:"op"`
	Type string          `json:"type"`
}

// Dirty lists dir's uncommitted edits as `enth status DIR` reports them
// (P1, R15), one git-porcelain-shaped line each so every consumer of the git
// answer — gitgc's counts, `pogo worktree-dirty`'s output — reads it
// unchanged:
//
//	" M path"  a put of a path in the checkout's index (a modified file)
//	"?? path"  a put of a path not in it (a new file)
//	" D path"  a delete
//
// pogod's own files are not listed because the exclude list Create writes
// keeps them out of status (§9); there is no carve-out here.
//
// An outside_selection refusal (a new file under no selected path) is NOT an
// error: those paths are uncommitted work like any other, so each is listed as
// "?? path". Any other failure — not a checkout, enth missing, a state it
// cannot read — is an error, never "clean". `enth status` reads DIR alone: no
// store, no remote, no token.
func (b Backend) Dirty(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, b.Bin, "status", dir)
	if err != nil {
		var ce *vcs.CommandError
		var re *vcs.RefusedError
		if errors.As(err, &ce) && errors.As(err, &re) && re.Code == "outside_selection" {
			if e, ok := parseError(ce.Output[len(out):]); ok {
				var lines []string
				for _, raw := range e.Paths {
					p, derr := decodePath(raw)
					if derr != nil {
						return nil, derr
					}
					lines = append(lines, "?? "+quotePath(p))
				}
				if len(lines) > 0 {
					return lines, nil
				}
			}
		}
		return nil, fmt.Errorf("enth status %s: %w", dir, err)
	}
	st, err := ReadState(dir)
	if err != nil {
		return nil, fmt.Errorf("enth status %s: %w", dir, err)
	}
	var lines []string
	sawStatus := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var op statusOp
		if err := json.Unmarshal(sc.Bytes(), &op); err != nil {
			return nil, fmt.Errorf("enth status %s: unreadable line %q: %w", dir, sc.Text(), err)
		}
		if op.Type == "status" {
			sawStatus = true
			continue
		}
		p, err := decodePath(op.Path)
		if err != nil {
			return nil, err
		}
		switch op.Op {
		case "put":
			if _, tracked := st.Entries[p]; tracked {
				lines = append(lines, " M "+quotePath(p))
			} else {
				lines = append(lines, "?? "+quotePath(p))
			}
		case "delete":
			lines = append(lines, " D "+quotePath(p))
		default:
			// An op this code does not know is still an edit: listing it
			// keeps the tree dirty, which is the safe reading.
			lines = append(lines, " ? "+quotePath(p))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawStatus {
		// No status line is not a clean tree: it is an enth this code does not
		// understand.
		return nil, fmt.Errorf("enth status %s printed no status line: %q", dir, out)
	}
	return lines, nil
}

// quotePath renders p as git's porcelain does: bare when it is plain, quoted
// when it holds a space, a quote, a control byte or a non-UTF-8 byte.
func quotePath(p string) string {
	for _, r := range p {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f || r == 0xfffd || r == ' ' {
			return fmt.Sprintf("%q", p)
		}
	}
	return p
}

// Root resolves a path anywhere inside an enth checkout to the checkout's
// root. A path whose nearest repo marker is git, or that has none, is an
// error.
func (Backend) Root(_ context.Context, dir string) (string, error) {
	kind, root, err := Detect(dir)
	if err != nil {
		return "", err
	}
	if kind != vcs.Enth {
		return "", fmt.Errorf("%s is not inside an enth checkout (no %s at or above it)", dir, StateRelPath)
	}
	return root, nil
}

// Describe names the pogo repo dir was cut for and its base, from the tree
// (W9, S10, X4). The base and origin come from .enth/checkout; the pogo
// project comes from pogod's workspace record, because nothing in enth's
// state names it. A checkout with no record — one pogod did not create — is
// a *vcs.UnnamedRepoError naming its origin. Branch is always empty.
func (b Backend) Describe(ctx context.Context, dir string) (vcs.Workspace, error) {
	root, err := b.Root(ctx, dir)
	if err != nil {
		return vcs.Workspace{}, err
	}
	st, err := ReadState(root)
	if err != nil {
		return vcs.Workspace{}, err
	}
	rec, err := ReadRecord(root)
	if errors.Is(err, fs.ErrNotExist) {
		return vcs.Workspace{}, &vcs.UnnamedRepoError{Dir: dir, Reason: fmt.Sprintf(
			"%s is an enth checkout of %s, but it has no pogo workspace record (%s): pogo did not create it, "+
				"so the pogo project it belongs to cannot be named", root, originText(st.Origin), RecordRelPath)}
	}
	if err != nil {
		return vcs.Workspace{}, err
	}
	return vcs.Workspace{Dir: dir, Repo: vcs.Repo{Path: rec.Repo}, Base: st.Base}, nil
}

func originText(o Origin) string {
	switch {
	case o.Remote != "" && o.Repo != "":
		return o.Repo + " on " + o.Remote
	case o.Remote != "":
		return o.Remote
	case o.Store != "":
		return "the local store " + o.Store
	}
	return "an unnamed origin"
}

// Exclude keeps relPath (relative to the checkout root dir) out of
// `enth status`, through DIR/.enth/info/exclude, anchored at the root, under
// group, idempotently (W8) — gitvcs.Workspaces.Exclude's contract on enth.
// It refuses a dir that is not an enth checkout root, as git refuses to edit
// an enclosing repo's exclude.
func (Backend) Exclude(_ context.Context, dir, group, relPath string) error {
	if dir == "" || relPath == "" {
		return nil
	}
	return excludePattern(dir, group, "/"+filepath.ToSlash(filepath.Clean(relPath)))
}

func excludePattern(dir, group, pattern string) error {
	if !IsCheckout(dir) {
		return fmt.Errorf("%s is not an enth checkout root (no %s); refusing to write an exclude for it", dir, StateRelPath)
	}
	p := filepath.Join(dir, ExcludeRelPath)
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var sb strings.Builder
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		sb.WriteString("\n")
	}
	if !strings.Contains(string(data), group) {
		sb.WriteString(group + "\n")
	}
	sb.WriteString(pattern + "\n")
	_, err = f.WriteString(sb.String())
	return err
}

// Refresh brings a long-lived enth workspace up to the current tip of the ref
// pogod's record says it follows (R19, O1). `enth checkout --sync` does not
// exist (epic §8 Q3), so it checks the ref out again into a temporary sibling
// and moves the new tree's files into place:
//
//   - not an enth checkout: skipped.
//   - no pogod record: declined_no_upstream — nothing names the ref, and
//     guessing the trunk would be a referent nobody configured.
//   - Dirty non-empty: declined_dirty, never clobbered. Dirty failing: failed.
//   - the base is no longer the one pogod recorded: declined_diverged. A
//     Change was made in this workspace, and moving the tree to the ref's tip
//     would take it off that Change.
//   - the fresh checkout's base equals this one's: already_current.
//   - otherwise: updated. Files the new base dropped are deleted, every file
//     of the new base is renamed into place (keeping the size, mtime and inode
//     its index recorded), then the new state file replaces the old. Files
//     enth does not track — ignored ones, pogod's own — are not touched.
//
// enth does not report how many entries lay between the two bases, so Behind
// and Ahead stay -1 (not determined) on every outcome but already_current.
// The swap is not atomic: a crash part-way leaves files whose state disagrees
// with .enth/checkout, which Dirty then reports, so it is seen, not silent.
func (b Backend) Refresh(ctx context.Context, dir string) vcs.RefreshResult {
	res := vcs.RefreshResult{Path: dir, Behind: -1, Ahead: -1}
	if !IsCheckout(dir) {
		res.Status, res.Detail = vcs.RefreshSkipped, "not an enth checkout"
		return res
	}
	st, err := ReadState(dir)
	if err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, err.Error()
		return res
	}
	rec, err := ReadRecord(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			res.Status = vcs.RefreshDeclinedNoUpstream
			res.Detail = fmt.Sprintf("no pogo workspace record (%s) names the ref this checkout follows", RecordRelPath)
		} else {
			res.Status, res.Detail = vcs.RefreshFailed, err.Error()
		}
		return res
	}
	res.Branch = rec.Ref
	remote, repo := st.Origin.Remote, st.Origin.Repo
	if remote == "" {
		remote, repo = rec.Remote, rec.EnthRepo
	}
	res.Upstream = rec.Ref + " of " + originText(Origin{Remote: remote, Repo: repo})
	res.From = short(st.Base)
	dirty, err := b.Dirty(ctx, dir)
	if err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, err.Error()
		return res
	}
	if len(dirty) > 0 {
		res.Status = vcs.RefreshDeclinedDirty
		res.Detail = fmt.Sprintf("%d uncommitted edit(s), e.g. %s", len(dirty), dirty[0])
		return res
	}
	if st.Base != rec.Base {
		res.Status = vcs.RefreshDeclinedDiverged
		res.Detail = fmt.Sprintf("the checkout's base moved from %s (recorded) to %s: a Change was made here, "+
			"and refreshing would move the workspace off it", short(rec.Base), short(st.Base))
		return res
	}
	tmpParent, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+".refresh-")
	if err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, err.Error()
		return res
	}
	defer os.RemoveAll(tmpParent)
	fresh := filepath.Join(tmpParent, "co")
	args := append(remoteArgs(remote, repo), "checkout", rec.Ref, fresh)
	args = append(args, st.Paths...)
	if _, err := run(ctx, b.Bin, args...); err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, err.Error()
		return res
	}
	nst, err := ReadState(fresh)
	if err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, err.Error()
		return res
	}
	if nst.Base == st.Base {
		res.Status, res.Behind, res.Ahead = vcs.RefreshAlreadyCurrent, 0, 0
		return res
	}
	if err := swapTree(dir, fresh, st, nst); err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, "refresh interrupted part-way, the tree may be mixed: "+err.Error()
		return res
	}
	rec.Base = nst.Base
	if err := WriteRecord(dir, rec); err != nil {
		res.Status, res.Detail = vcs.RefreshFailed, "tree refreshed but the record was not updated: "+err.Error()
		return res
	}
	res.Status, res.To, res.Ahead = vcs.RefreshUpdated, short(nst.Base), 0
	return res
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// swapTree moves fresh's tracked files into dir: deletes what old tracked and
// new does not, renames every new file into place, and installs the new state
// file last.
func swapTree(dir, fresh string, old, nu State) error {
	var gone []string
	for p := range old.Entries {
		if _, ok := nu.Entries[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return err
		}
		pruneEmptyParents(dir, filepath.Dir(filepath.Join(dir, filepath.FromSlash(p))))
	}
	var keep []string
	for p, mode := range nu.Entries {
		if mode != "gitlink" {
			keep = append(keep, p)
		}
	}
	sort.Strings(keep)
	for _, p := range keep {
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := makeParents(dir, filepath.Dir(dst)); err != nil {
			return err
		}
		if fi, err := os.Lstat(dst); err == nil && fi.IsDir() {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
		if err := os.Rename(filepath.Join(fresh, filepath.FromSlash(p)), dst); err != nil {
			return err
		}
	}
	return os.Rename(filepath.Join(fresh, StateRelPath), filepath.Join(dir, StateRelPath))
}

// makeParents creates d (under root), replacing any non-directory standing
// where a directory must be (a file that became a directory upstream).
func makeParents(root, d string) error {
	rel, err := filepath.Rel(root, d)
	if err != nil || rel == "." {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err == nil && !fi.IsDir() {
			if err := os.Remove(cur); err != nil {
				return err
			}
		}
	}
	return os.MkdirAll(d, 0o755)
}

func pruneEmptyParents(root, d string) {
	for d != root && strings.HasPrefix(d, root) {
		if os.Remove(d) != nil {
			return
		}
		d = filepath.Dir(d)
	}
}
