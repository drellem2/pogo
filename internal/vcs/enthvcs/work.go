package enthvcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// Work on enth: trailers name a worker's work (docs/design/enth-vcs.md §5,
// slice 6, mg-30da0).
//
// A worker's output is the Changes it made with
//
//	enth change DIR -m "…" --trailer Pogo-Item=mg-X --trailer Pogo-Worker=<name>
//
// and every question about it is one listing through the repo's enthd:
//
//	enth --remote URL --repo NAME changes [--unlanded] --trailer Pogo-Item=mg-X [--trailer Pogo-Worker=W]
//
// (enth #9 item G, mg-10079). Each line names the Change's id, base, author,
// committer time, trailers, and `landed`: the id a LAND RECORD says it landed
// as, or null. That field is E3 — the landed complement of the listing — and
// it is what State reads. "Absent from the unlanded list" is never read as
// "landed": it equally describes a Change that was never made.
//
// Reads are live (they go to enthd), so Fetch has nothing to refresh.

// changeLine is one line of `enth changes`.
type changeLine struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Entry  int    `json:"entry"`
	Author struct {
		Name  json.RawMessage `json:"name"`
		Email json.RawMessage `json:"email"`
	} `json:"author"`
	Created  int64           `json:"created"`
	Base     *string         `json:"base"`
	Trailers []trailerLine   `json:"trailers"`
	Landed   *string         `json:"landed"`
	Raw      json.RawMessage `json:"-"`
}

type trailerLine struct {
	Key   json.RawMessage `json:"key"`
	Value json.RawMessage `json:"value"`
}

// decodeB reads enth's byte-string encoding (a JSON string or {"hex":..});
// an absent field is "".
func decodeB(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	return decodePath(raw)
}

// parseChanges reads `enth changes` output. A line it cannot read is an
// error, never skipped: a listing that silently dropped a Change would read
// as "no work" (the failure this identity scheme guards against).
func parseChanges(out []byte) ([]vcs.Change, error) {
	var cs []vcs.Change
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var l changeLine
		if err := json.Unmarshal(line, &l); err != nil {
			return nil, fmt.Errorf("enth changes: unreadable line %q: %w", line, err)
		}
		if l.Type != "change" || l.ID == "" {
			return nil, fmt.Errorf("enth changes: unexpected line %q", line)
		}
		c := vcs.Change{ID: l.ID, Entry: l.Entry, Created: time.Unix(l.Created, 0).UTC()}
		if l.Base != nil {
			c.Base = *l.Base
		}
		if l.Landed != nil {
			c.Landed = *l.Landed
		}
		var err error
		if c.AuthorName, err = decodeB(l.Author.Name); err != nil {
			return nil, err
		}
		if c.AuthorEmail, err = decodeB(l.Author.Email); err != nil {
			return nil, err
		}
		for _, t := range l.Trailers {
			k, err := decodeB(t.Key)
			if err != nil {
				return nil, err
			}
			v, err := decodeB(t.Value)
			if err != nil {
				return nil, err
			}
			c.Trailers = append(c.Trailers, vcs.Trailer{Key: k, Value: v})
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// remote is where Work's reads go: the record's enthd and repo.
func (b Backend) remote() []string { return remoteArgs(b.Repo.Remote, b.Repo.Repo) }

// ListChanges is the trailer listing: the Changes stored by `enth change`
// (oldest first) whose trailers include Pogo-Item=item (when item is set) and
// Pogo-Worker=worker (when worker is set), only the unlanded ones when
// unlanded is true.
func (b Backend) ListChanges(ctx context.Context, item, worker string, unlanded bool) ([]vcs.Change, error) {
	args := append(b.remote(), "changes")
	if unlanded {
		args = append(args, "--unlanded")
	}
	if item != "" {
		args = append(args, "--trailer", vcs.TrailerItem+"="+item)
	}
	if worker != "" {
		args = append(args, "--trailer", vcs.TrailerWorker+"="+worker)
	}
	out, err := run(ctx, b.Bin, args...)
	if err != nil {
		return nil, err
	}
	return parseChanges(out)
}

// submission builds the Submission for one stack.
func submission(item string, stack []vcs.Change) vcs.Submission {
	s := vcs.Submission{Item: item, Changes: stack}
	for _, c := range stack {
		s.Revs = append(s.Revs, c.ID)
	}
	if n := len(stack); n > 0 {
		top := stack[n-1]
		s.Ref = top.ID
		s.Worker = top.Trailer(vcs.TrailerWorker)
		if s.Item == "" {
			s.Item = top.Trailer(vcs.TrailerItem)
		}
	}
	return s
}

// Find lists the unlanded work matching q as stacks, one Submission each
// (§5). q.Item and q.Worker select by trailer; both empty is every unlanded
// Change in the repo. More than one Submission for an item is the ambiguous
// case: FindStack refuses it, Find only reports it.
func (b Backend) Find(ctx context.Context, _ vcs.Repo, q vcs.WorkQuery) ([]vcs.Submission, error) {
	cs, err := b.ListChanges(ctx, q.Item, q.Worker, true)
	if err != nil {
		return nil, err
	}
	var subs []vcs.Submission
	for _, st := range vcs.Stacks(cs) {
		subs = append(subs, submission(q.Item, st))
	}
	return subs, nil
}

// FindStack is the submission of item (and worker, when given): the one
// stack its unlanded Changes form. None is an error wrapping vcs.ErrNoWork;
// more than one is a *vcs.AmbiguousWorkError naming each stack's worker.
func (b Backend) FindStack(ctx context.Context, item, worker string) (vcs.Submission, error) {
	if item == "" {
		// An empty item filter lists every worker's unlanded work: never a
		// submission.
		return vcs.Submission{}, fmt.Errorf("enth %s: a submission is named by its work item, and none was given", b.Repo.Repo)
	}
	subs, err := b.Find(ctx, vcs.Repo{}, vcs.WorkQuery{Item: item, Worker: worker})
	if err != nil {
		return vcs.Submission{}, err
	}
	switch len(subs) {
	case 0:
		who := vcs.TrailerItem + "=" + item
		if worker != "" {
			who += " " + vcs.TrailerWorker + "=" + worker
		}
		return vcs.Submission{}, fmt.Errorf("enth %s: no unlanded Change carries %s: %w", b.Repo.Repo, who, vcs.ErrNoWork)
	case 1:
		return subs[0], nil
	}
	return vcs.Submission{}, &vcs.AmbiguousWorkError{Item: item, Worker: worker, Stacks: subs}
}

// State judges a submission by its land records (S4, E3): each of its Changes
// is landed exactly when a land record names it, which is exact where git's
// patch-id cherry is a heuristic. A Change the listing does not know at all
// is an error, not "landed". target is not consulted: a land record does not
// say which ref it moved in enth's listing, and v1 lands only on the trunk.
func (b Backend) State(ctx context.Context, _ vcs.Repo, s vcs.Submission, _ string) (vcs.WorkState, error) {
	ids := s.Revs
	if len(ids) == 0 {
		for _, c := range s.Changes {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return vcs.WorkState{}, fmt.Errorf("enth %s: work state of an empty submission", b.Repo.Repo)
	}
	// Item-filtered when the submission names one: a smaller read, and the
	// Changes of an item are exactly what an item's submission holds.
	all, err := b.ListChanges(ctx, s.Item, "", false)
	if err != nil {
		return vcs.WorkState{}, err
	}
	byID := map[string]vcs.Change{}
	for _, c := range all {
		byID[c.ID] = c
	}
	var st vcs.WorkState
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			return vcs.WorkState{}, fmt.Errorf("enth %s: Change %s is not in the listing of %s=%s: its land state is unknown, not landed",
				b.Repo.Repo, id, vcs.TrailerItem, s.Item)
		}
		if c.Landed != "" {
			st.Equivalent++
			continue
		}
		subject, err := b.subject(ctx, id)
		if err != nil {
			return vcs.WorkState{}, err
		}
		st.Ahead = append(st.Ahead, vcs.Commit{Rev: id, Subject: subject, Summary: id + " " + subject})
	}
	st.Landed = len(st.Ahead) == 0
	return st, nil
}

// showLine is `enth query show`'s line.
type showLine struct {
	Type    string          `json:"type"`
	Change  string          `json:"change"`
	Parents []string        `json:"parents"`
	Subject json.RawMessage `json:"subject"`
	Message json.RawMessage `json:"message"`
}

func (b Backend) show(ctx context.Context, id string) (showLine, error) {
	out, err := run(ctx, b.Bin, append(b.remote(), "query", "show", id)...)
	if err != nil {
		return showLine{}, err
	}
	var l showLine
	if err := json.Unmarshal(bytes.TrimSpace(out), &l); err != nil || l.Type != "change" {
		return showLine{}, fmt.Errorf("enth query show %s: unreadable answer %q", id, bytes.TrimSpace(out))
	}
	return l, nil
}

func (b Backend) subject(ctx context.Context, id string) (string, error) {
	l, err := b.show(ctx, id)
	if err != nil {
		return "", err
	}
	return decodeB(l.Subject)
}

// Messages returns each of s.Revs' message bodies: the message without its
// subject line and the blank line after it, as git's %b is (S7, R12). The
// trailers are part of the body, as in git.
func (b Backend) Messages(ctx context.Context, _ vcs.Repo, s vcs.Submission) ([]vcs.Message, error) {
	var msgs []vcs.Message
	for _, id := range s.Revs {
		l, err := b.show(ctx, id)
		if err != nil {
			return msgs, err
		}
		m, err := decodeB(l.Message)
		if err != nil {
			return msgs, err
		}
		body := ""
		if i := strings.IndexByte(m, '\n'); i >= 0 {
			body = strings.TrimLeft(m[i+1:], "\n")
		}
		msgs = append(msgs, vcs.Message{Rev: id, Body: body})
	}
	return msgs, nil
}

// FullMessages returns each Change's whole message, subject line included,
// as git's %B is. The closing-ref gate reads these (R12, enth-vcs slice 9):
// a GitHub closing keyword in a subject closes the issue as surely as one in
// the body, and Messages leaves the subject out. On error it also returns
// the messages it read before the failure.
func (b Backend) FullMessages(ctx context.Context, ids []string) ([]vcs.Message, error) {
	var msgs []vcs.Message
	for _, id := range ids {
		l, err := b.show(ctx, id)
		if err != nil {
			return msgs, err
		}
		m, err := decodeB(l.Message)
		if err != nil {
			return msgs, err
		}
		msgs = append(msgs, vcs.Message{Rev: id, Body: m})
	}
	return msgs, nil
}

type lsLine struct {
	Type string          `json:"type"`
	Path json.RawMessage `json:"path"`
	Mode string          `json:"mode"`
	Blob string          `json:"blob"`
}

// tree reads every file of rev's tree: path → mode+blob.
func (b Backend) tree(ctx context.Context, rev string) (map[string]string, error) {
	out, err := run(ctx, b.Bin, append(b.remote(), "query", "ls", "-r", "--rev", rev)...)
	if err != nil {
		return nil, err
	}
	t := map[string]string{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var l lsLine
		if err := json.Unmarshal(line, &l); err != nil {
			return nil, fmt.Errorf("enth query ls %s: unreadable line %q: %w", rev, line, err)
		}
		if l.Type != "file" {
			continue
		}
		p, err := decodeB(l.Path)
		if err != nil {
			return nil, err
		}
		t[p] = l.Mode + " " + l.Blob
	}
	return t, nil
}

// Diff returns the paths s.Revs touched (S8, R15): for each Change, the
// files whose mode or content differ between its first parent's tree and
// its own. A root Change touched every file it has.
func (b Backend) Diff(ctx context.Context, _ vcs.Repo, s vcs.Submission) (vcs.Diff, error) {
	seen := map[string]bool{}
	for _, id := range s.Revs {
		l, err := b.show(ctx, id)
		if err != nil {
			return vcs.Diff{}, err
		}
		after, err := b.tree(ctx, id)
		if err != nil {
			return vcs.Diff{}, err
		}
		before := map[string]string{}
		if len(l.Parents) > 0 {
			if before, err = b.tree(ctx, l.Parents[0]); err != nil {
				return vcs.Diff{}, err
			}
		}
		for p, e := range after {
			if before[p] != e {
				seen[p] = true
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				seen[p] = true
			}
		}
	}
	var d vcs.Diff
	for p := range seen {
		d.Paths = append(d.Paths, p)
	}
	sort.Strings(d.Paths)
	return d, nil
}

// Cat returns path's content at rev (S9): `enth query cat PATH --rev REV`.
// A path not in rev's tree is a *vcs.RefusedError with code not_found.
func (b Backend) Cat(ctx context.Context, _ vcs.Repo, rev, path string) ([]byte, error) {
	out, err := run(ctx, b.Bin, append(b.remote(), "query", "cat", path, "--rev", rev)...)
	if err != nil {
		return nil, err
	}
	var l struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &l); err != nil || l.Type != "file" {
		return nil, fmt.Errorf("enth query cat %s at %s: not a file (%q)", path, rev, bytes.TrimSpace(out))
	}
	c, err := decodeB(l.Content)
	if err != nil {
		return nil, err
	}
	return []byte(c), nil
}

// Fetch has nothing to refresh: every read goes to enthd.
func (Backend) Fetch(context.Context, vcs.Repo) error { return nil }

// UntrailedBaseError reports a workspace whose base is an unlanded Change
// that carries no Pogo-Item trailer (§5, "Trailers the worker forgot", guard
// 1): work every trailer query is blind to. It is the trailer-independent
// signal, read from the workspace itself.
type UntrailedBaseError struct {
	Dir    string
	Change vcs.Change
}

func (e *UntrailedBaseError) Error() string {
	return fmt.Sprintf("%s: its base %s is an unlanded Change with no %s trailer, so no trailer query can find it. "+
		"Remedy: this Change has no %s trailer — make the work again with "+
		"`enth change %s -m \"…\" --trailer %s=<item> --trailer %s=<worker>` on a fresh checkout, or ask the mayor",
		e.Dir, e.Change.ID, vcs.TrailerItem, vcs.TrailerItem, e.Dir, vcs.TrailerItem, vcs.TrailerWorker)
}

// WorkspaceBase reads dir's base and its origin, and the Backend that reads
// that origin: the dir-only entry point for the trailer-less signal and for
// presubmit, which know a workspace but not its project record.
func WorkspaceBase(dir string, bin string) (Backend, string, error) {
	root := dir
	if _, r, err := Detect(dir); err != nil {
		return Backend{}, "", err
	} else if r != "" {
		root = r
	}
	st, err := ReadState(root)
	if err != nil {
		return Backend{}, "", err
	}
	if st.Origin.Remote == "" {
		return Backend{}, "", fmt.Errorf("%s: not a checkout of an enthd (origin %+v): its Changes are not durable on a server", root, st.Origin)
	}
	return Backend{Repo: vcs.EnthRepo{Remote: st.Origin.Remote, Repo: st.Origin.Repo}, Bin: bin}, st.Base, nil
}

// UntrailedBase is the trailer-independent signal for one workspace: it
// returns an *UntrailedBaseError when dir's base is an unlanded Change with
// no Pogo-Item trailer, and nil when the base is landed, or is unlanded with
// the trailer. A base that is not a Change made by `enth change` (the
// checkout's own starting point, a landed trunk Change) is not in the
// listing and is not stranded work.
func (b Backend) UntrailedBase(ctx context.Context, dir, base string) error {
	cs, err := b.ListChanges(ctx, "", "", true)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.ID == base && c.Trailer(vcs.TrailerItem) == "" {
			return &UntrailedBaseError{Dir: dir, Change: c}
		}
	}
	return nil
}

// IsNoWork reports whether err is the empty-listing refusal.
func IsNoWork(err error) bool { return errors.Is(err, vcs.ErrNoWork) }

// RefTip is the Change ref (a bare branch name or a full ref) names on enthd
// now: R2's "does the target exist", and the T a land is checked against.
// A ref that does not exist is a *vcs.RefusedError (enth's not_found).
func (b Backend) RefTip(ctx context.Context, ref string) (string, error) {
	l, err := b.show(ctx, b.ResolveBase(ctx, vcs.Repo{}, ref))
	if err != nil {
		return "", err
	}
	return l.Change, nil
}
