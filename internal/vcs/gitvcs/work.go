package gitvcs

import (
	"context"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
)

// BranchPrefix is the prefix every polecat branch carries. internal/gitgc and
// internal/strandedwork define their BranchPrefix as this constant.
const BranchPrefix = "polecat-"

// RunFunc runs `git -C dir args...` with stdin fed from a string (ignored when
// empty) and returns stdout. Output is the default. internal/gitgc passes its
// budgeted runner, so the work it asks of Work stays under its commandBudget.
type RunFunc func(dir, stdin string, args ...string) (string, error)

// Work is the git backend of vcs.Work (enth-vcs slice 3, mg-d72cb). It was
// built from the git calls of internal/strandedwork and internal/gitgc without
// changing them: each method runs exactly the command its caller ran before,
// and parses the output the same way.
//
// Errors are the runner's own, UNWRAPPED, so a caller's wrapping produces the
// text it always has. The ctx is accepted for the interface and not honoured:
// none of these commands was cancellable before, and this slice changes no
// behaviour.
type Work struct {
	// Run runs git. Nil means Output.
	Run RunFunc
}

var _ vcs.Work = Work{}

func (w Work) run(dir, stdin string, args ...string) (string, error) {
	if w.Run != nil {
		return w.Run(dir, stdin, args...)
	}
	return Output(dir, stdin, args...)
}

// Find lists polecat branches as `for-each-ref` sees them, local heads and
// origin's remote-tracking refs alike, one Submission per ref in refname order.
// q.Worker narrows the list to polecat-<Worker>. q.Item is ErrUnsupported: git
// has no index of work by item.
func (w Work) Find(ctx context.Context, repo vcs.Repo, q vcs.WorkQuery) ([]vcs.Submission, error) {
	if q.Item != "" {
		return nil, vcs.ErrUnsupported
	}
	pattern := BranchPrefix + "*"
	if q.Worker != "" {
		pattern = BranchPrefix + q.Worker
	}
	out, err := w.run(repo.Path, "", "for-each-ref", "--format=%(refname)",
		"refs/heads/"+pattern, "refs/remotes/origin/"+pattern)
	if err != nil {
		return nil, err
	}
	var subs []vcs.Submission
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		branch := line
		if rest, ok := strings.CutPrefix(line, "refs/heads/"); ok {
			branch = rest
		} else if rest, ok := strings.CutPrefix(line, "refs/remotes/origin/"); ok {
			branch = rest
		}
		subs = append(subs, vcs.Submission{Branch: branch, Ref: line})
	}
	return subs, nil
}

// State runs `git cherry -v <target> <ref>` (Ref, or Branch when Ref is
// unset). A "+" line is a commit with no patch-equivalent on target and goes in
// Ahead; a "-" line has one and is counted in Equivalent.
func (w Work) State(ctx context.Context, repo vcs.Repo, s vcs.Submission, target string) (vcs.WorkState, error) {
	head := s.Ref
	if head == "" {
		head = s.Branch
	}
	out, err := w.run(repo.Path, "", "cherry", "-v", target, head)
	if err != nil {
		return vcs.WorkState{}, err
	}
	var st vcs.WorkState
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		mark, rest := line[0], strings.TrimSpace(line[1:])
		sha, subject, _ := strings.Cut(rest, " ")
		switch mark {
		case '+':
			st.Ahead = append(st.Ahead, vcs.Commit{Rev: sha, Subject: strings.TrimSpace(subject), Summary: rest})
		case '-':
			st.Equivalent++
		}
	}
	st.Landed = len(st.Ahead) == 0
	return st, nil
}

// MessageBatch is how many commits one `git log` invocation is asked for. A
// branch with hundreds of unmerged commits would otherwise build an argv long
// enough to hit the platform limit, and an E2BIG would lose the bodies for the
// whole branch rather than for one batch.
const MessageBatch = 100

// Messages reads the bodies of s.Revs, MessageBatch at a time, with
// `git log --no-walk=unsorted --format=%H%n%b%x00`. A revision git does not
// report is absent from the result. A failing batch stops the read, and the
// messages of the batches before it are returned with the error.
func (w Work) Messages(ctx context.Context, repo vcs.Repo, s vcs.Submission) ([]vcs.Message, error) {
	var msgs []vcs.Message
	for start := 0; start < len(s.Revs); start += MessageBatch {
		end := start + MessageBatch
		if end > len(s.Revs) {
			end = len(s.Revs)
		}
		// %x00 and not a literal NUL in the format string: exec argv cannot
		// carry a NUL byte at all, so building the separator on this side fails
		// before git runs. git emits the byte from the escape.
		args := []string{"log", "--no-walk=unsorted", "--format=%H%n%b%x00"}
		args = append(args, s.Revs[start:end]...)
		out, err := w.run(repo.Path, "", args...)
		if err != nil {
			return msgs, err
		}
		for _, rec := range strings.Split(out, "\x00") {
			rec = strings.TrimLeft(rec, "\n")
			if rec == "" {
				continue
			}
			sha, body, _ := strings.Cut(rec, "\n")
			msgs = append(msgs, vcs.Message{Rev: strings.TrimSpace(sha), Body: strings.TrimRight(body, "\n")})
		}
	}
	return msgs, nil
}

// Diff lists the files s.Revs touched with
// `git show --pretty=format: --name-only -z`.
//
// -z rather than a newline split: git QUOTES a path containing a space or a
// non-ASCII byte when it has to use a line-oriented format, and a quoted path
// handed back to Cat resolves to nothing. The NUL-delimited form is never
// quoted.
func (w Work) Diff(ctx context.Context, repo vcs.Repo, s vcs.Submission) (vcs.Diff, error) {
	args := append([]string{"show", "--pretty=format:", "--name-only", "-z"}, s.Revs...)
	out, err := w.run(repo.Path, "", args...)
	if err != nil {
		return vcs.Diff{}, err
	}
	var d vcs.Diff
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			d.Paths = append(d.Paths, p)
		}
	}
	return d, nil
}

// Cat returns path at rev with `git show <rev>:<path>`.
func (w Work) Cat(ctx context.Context, repo vcs.Repo, rev, path string) ([]byte, error) {
	out, err := w.run(repo.Path, "", "show", rev+":"+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
