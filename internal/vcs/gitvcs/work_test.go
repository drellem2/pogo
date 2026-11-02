package gitvcs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// workFixture: a clone of an origin holding main and two polecat branches, one
// pushed (polecat-aaa: an unmerged commit plus a cherry-picked-onto-main one)
// and one local only (polecat-bbb).
func workFixture(t *testing.T) (clone string, pickedSHA, uniqueSHA string) {
	t.Helper()
	_, clone = fixture(t, false)
	git(t, clone, "checkout", "-q", "-b", "polecat-aaa", "origin/main")
	write(t, clone, "a.txt", "one\n")
	git(t, clone, "add", "a.txt")
	git(t, clone, "commit", "-q", "-m", "pick me (mg-aaa1)")
	pickedSHA = git(t, clone, "rev-parse", "HEAD")
	write(t, clone, "b c.txt", "two\n")
	git(t, clone, "add", "b c.txt")
	git(t, clone, "commit", "-q", "-m", "unique work (mg-aaa1)", "-m", "Body line.\n\nPogo-Item: mg-aaa1")
	uniqueSHA = git(t, clone, "rev-parse", "HEAD")
	git(t, clone, "push", "-q", "origin", "polecat-aaa")
	git(t, clone, "checkout", "-q", "main")
	git(t, clone, "reset", "-q", "--hard", "origin/main")
	git(t, clone, "cherry-pick", "-x", pickedSHA)
	git(t, clone, "push", "-q", "origin", "main")
	git(t, clone, "fetch", "-q", "origin")
	git(t, clone, "branch", "-q", "polecat-bbb", "origin/main")
	return clone, pickedSHA, uniqueSHA
}

func TestWorkFind(t *testing.T) {
	clone, _, _ := workFixture(t)
	repo := vcs.Repo{Path: clone}
	subs, err := Work{}.Find(context.Background(), repo, vcs.WorkQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []vcs.Submission{
		{Branch: "polecat-aaa", Ref: "refs/heads/polecat-aaa"},
		{Branch: "polecat-bbb", Ref: "refs/heads/polecat-bbb"},
		{Branch: "polecat-aaa", Ref: "refs/remotes/origin/polecat-aaa"},
	}
	if !reflect.DeepEqual(subs, want) {
		t.Fatalf("Find = %+v, want %+v", subs, want)
	}

	one, err := Work{}.Find(context.Background(), repo, vcs.WorkQuery{Worker: "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, want[1:2]) {
		t.Fatalf("Find(Worker=bbb) = %+v, want %+v", one, want[1:2])
	}

	if _, err := (Work{}).Find(context.Background(), repo, vcs.WorkQuery{Item: "mg-aaa1"}); !errors.Is(err, vcs.ErrUnsupported) {
		t.Fatalf("Find(Item) = %v, want ErrUnsupported: git has no item index", err)
	}
}

func TestWorkState(t *testing.T) {
	clone, _, uniqueSHA := workFixture(t)
	repo := vcs.Repo{Path: clone}
	st, err := Work{}.State(context.Background(), repo,
		vcs.Submission{Branch: "polecat-aaa", Ref: "refs/remotes/origin/polecat-aaa"}, "refs/remotes/origin/main")
	if err != nil {
		t.Fatal(err)
	}
	want := vcs.WorkState{
		Ahead:      []vcs.Commit{{Rev: uniqueSHA, Subject: "unique work (mg-aaa1)", Summary: uniqueSHA + " unique work (mg-aaa1)"}},
		Equivalent: 1,
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("State = %+v, want %+v", st, want)
	}

	// Positive control for Landed: a branch with nothing ahead, read by Branch.
	st, err = Work{}.State(context.Background(), repo, vcs.Submission{Branch: "polecat-bbb"}, "refs/remotes/origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Landed || len(st.Ahead) != 0 {
		t.Fatalf("State(polecat-bbb) = %+v, want Landed with nothing ahead", st)
	}

	// The runner's error comes back unwrapped, so callers' wrapping is theirs.
	_, err = Work{}.State(context.Background(), repo, vcs.Submission{Ref: "refs/heads/nope"}, "main")
	if err == nil {
		t.Fatal("State on a missing ref must fail")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("State error %v does not carry the exec error", err)
	}
}

func TestWorkMessagesDiffCat(t *testing.T) {
	clone, pickedSHA, uniqueSHA := workFixture(t)
	repo := vcs.Repo{Path: clone}
	ctx := context.Background()

	msgs, err := Work{}.Messages(ctx, repo, vcs.Submission{Revs: []string{uniqueSHA, pickedSHA}})
	if err != nil {
		t.Fatal(err)
	}
	want := []vcs.Message{
		{Rev: uniqueSHA, Body: "Body line.\n\nPogo-Item: mg-aaa1"},
		{Rev: pickedSHA, Body: ""},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("Messages = %+v, want %+v", msgs, want)
	}

	d, err := Work{}.Diff(ctx, repo, vcs.Submission{Revs: []string{uniqueSHA}})
	if err != nil {
		t.Fatal(err)
	}
	// "b c.txt" is the case -z exists for: a line-oriented format quotes it.
	if !reflect.DeepEqual(d.Paths, []string{"b c.txt"}) {
		t.Fatalf("Diff = %q, want [\"b c.txt\"]", d.Paths)
	}

	blob, err := Work{}.Cat(ctx, repo, uniqueSHA, "b c.txt")
	if err != nil || string(blob) != "two\n" {
		t.Fatalf("Cat = %q, %v; want \"two\\n\"", blob, err)
	}
	if _, err := (Work{}).Cat(ctx, repo, "refs/remotes/origin/main", "b c.txt"); err == nil {
		t.Fatal("Cat of a path absent at rev must fail")
	}
}

// TestWorkMessagesKeepsEarlierBatches pins the partial-read contract that
// strandedwork.loadBodies relies on: a failing batch returns what the batches
// before it read.
func TestWorkMessagesKeepsEarlierBatches(t *testing.T) {
	clone, _, uniqueSHA := workFixture(t)
	revs := make([]string, MessageBatch)
	for i := range revs {
		revs[i] = uniqueSHA
	}
	revs = append(revs, strings.Repeat("0", 40)) // the second batch: no such commit
	msgs, err := Work{}.Messages(context.Background(), vcs.Repo{Path: clone}, vcs.Submission{Revs: revs})
	if err == nil {
		t.Fatal("a batch naming a missing commit must fail")
	}
	if len(msgs) == 0 || msgs[0].Rev != uniqueSHA {
		t.Fatalf("Messages dropped the first batch on the second's failure: %+v", msgs)
	}
}

// TestWorkRunIsInjected: a Run func replaces Output for every method, which is
// how gitgc keeps Work's commands under its commandBudget.
func TestWorkRunIsInjected(t *testing.T) {
	var calls []string
	w := Work{Run: func(dir, stdin string, args ...string) (string, error) {
		calls = append(calls, dir+" "+strings.Join(args, " "))
		return "", fmt.Errorf("injected")
	}}
	repo := vcs.Repo{Path: "/r"}
	ctx := context.Background()
	w.Find(ctx, repo, vcs.WorkQuery{})
	w.State(ctx, repo, vcs.Submission{Ref: "h"}, "t")
	w.Messages(ctx, repo, vcs.Submission{Revs: []string{"x"}})
	w.Diff(ctx, repo, vcs.Submission{Revs: []string{"x"}})
	w.Cat(ctx, repo, "x", "p")
	want := []string{
		"/r for-each-ref --format=%(refname) refs/heads/polecat-* refs/remotes/origin/polecat-*",
		"/r cherry -v t h",
		"/r log --no-walk=unsorted --format=%H%n%b%x00 x",
		"/r show --pretty=format: --name-only -z x",
		"/r show x:p",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %q\nwant %q", calls, want)
	}
}

func TestRunSplitBudget(t *testing.T) {
	_, _, err := RunSplit(50*time.Millisecond, "sleep", "5")
	if !errors.Is(err, ErrCommandBudget) || !strings.Contains(err.Error(), "sleep 5") {
		t.Fatalf("RunSplit over budget = %v, want ErrCommandBudget naming the command", err)
	}
	// Positive control: under budget, the command's own result.
	if out, err := RunOutput(5*time.Second, "echo", "ok"); err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("RunOutput = %q, %v", out, err)
	}
}
