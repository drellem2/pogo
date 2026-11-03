package refinery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs slice 9 (mg-2e9fc): the closing-ref gate for an enth MR reads the
// Change messages and never the PR body (docs/design/enth-vcs.md §11).

type fakeChangeMessages struct {
	msgs  map[string]string
	err   error
	short bool
	asked []string
}

func (f *fakeChangeMessages) FullMessages(_ context.Context, ids []string) ([]vcs.Message, error) {
	f.asked = append(f.asked, ids...)
	var out []vcs.Message
	for i, id := range ids {
		if f.short && i == len(ids)-1 {
			break
		}
		out = append(out, vcs.Message{Rev: id, Body: f.msgs[id]})
	}
	return out, f.err
}

func TestEnthClosingRefGateReadsChangeMessages(t *testing.T) {
	// A gh that would report a PR body closing an issue: if the enth gate ever
	// asked it, the "clean" case below would fail.
	logPath := fakeGHLog(t, `printf '%s\n' '{"number":57,"body":"Resolves drellem2/pogo#111\n"}'`)
	ctx := context.Background()

	clean := &fakeChangeMessages{msgs: map[string]string{
		"c1": "feat: a (mg-9)\n\nRefs drellem2/pogo#111\n\nPogo-Item: mg-9\n",
		"c2": "fix: b (mg-9)\n\nPogo-Item: mg-9\n",
	}}
	if err := checkEnthClosingRefs(ctx, clean, []string{"c1", "c2"}); err != nil {
		t.Errorf("clean Changes rejected: %v", err)
	}
	if strings.Join(clean.asked, ",") != "c1,c2" {
		t.Errorf("read %v, want every Change of the MR", clean.asked)
	}

	inSubject := &fakeChangeMessages{msgs: map[string]string{
		"c1": "feat: a (mg-9)\n",
		"c2": "fix: closes drellem2/pogo#89 (mg-9)\n\nPogo-Item: mg-9\n",
	}}
	err := checkEnthClosingRefs(ctx, inSubject, []string{"c1", "c2"})
	if err == nil {
		t.Fatal("MISS: a closing keyword in a Change subject would have landed on the mirror")
	}
	for _, want := range []string{"Change c2", "drellem2/pogo#89", "CHANGE MESSAGE", "Closing-ref-ack:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure message omits %q:\n%v", want, err)
		}
	}
	for _, not := range []string{"Change c1", "gh pr edit", "pull request #"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("failure message names %q, which it must not:\n%v", not, err)
		}
	}

	acked := &fakeChangeMessages{msgs: map[string]string{
		"c1": "fix: x (mg-9)\n\nFixes drellem2/pogo#89\n\nClosing-ref-ack: drellem2/pogo#89 — intentional; discharges it\n",
	}}
	if err := checkEnthClosingRefs(ctx, acked, []string{"c1"}); err != nil {
		t.Errorf("acknowledged closure rejected: %v", err)
	}

	if calls := ghCalls(t, logPath); len(calls) != 0 {
		t.Errorf("the enth gate ran gh (%q); an enth repo has no PR body to read", calls)
	}

	// Git control: the git gate, with the same gh on PATH, still asks gh for
	// the PR body and catches what it says — so the silence above is the enth
	// gate's, not a gh stub that never answers.
	workDir := closingRefRepo(t)
	cleanBranch(t, workDir, "polecat-e9c1")
	logPath = fakeGHLog(t, `printf '%s\n' '{"number":57,"body":"Resolves drellem2/pogo#111\n"}'`)
	if err := checkClosingRefs(workDir, "main", "polecat-e9c1"); err == nil || !strings.Contains(err.Error(), "pull request #57 body") {
		t.Errorf("git control: checkClosingRefs = %v, want the PR body caught", err)
	}
	if !ghCalled(t, logPath, "pr view polecat-e9c1") {
		calls := ghCalls(t, logPath)
		t.Errorf("git control: gh calls = %q, want a pr view", calls)
	}
}

// Unreadable is not clean, as for git's unreadable history.
func TestEnthClosingRefGateFailsClosed(t *testing.T) {
	ctx := context.Background()
	if err := checkEnthClosingRefs(ctx, &fakeChangeMessages{}, nil); err == nil {
		t.Error("an MR with no Changes was judged clean")
	}
	failing := &fakeChangeMessages{msgs: map[string]string{"c1": "feat: ok\n"}, err: errors.New("enthd: connection refused")}
	if err := checkEnthClosingRefs(ctx, failing, []string{"c1"}); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("a failed read = %v, want the error", err)
	}
	short := &fakeChangeMessages{msgs: map[string]string{"c1": "feat: ok\n", "c2": "fix: closes #4\n"}, short: true}
	if err := checkEnthClosingRefs(ctx, short, []string{"c1", "c2"}); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Errorf("a partial read = %v, want a refusal", err)
	}
}
