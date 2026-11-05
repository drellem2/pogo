package gitgc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build-output pruning in kept, dead-owner trees (drellem2/pogo#282, change C).

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func prunedPaths(res Result) map[string]bool {
	out := map[string]bool{}
	for _, a := range res.BuildOutputPruned {
		for _, p := range a.Paths {
			out[filepath.Join(a.Root, p)] = true
		}
	}
	return out
}

// keptGitTree builds a dead owner's git worktree that is KEPT for one
// uncommitted, non-ignored file, and fills it with ignored build output plus
// the things that must never go.
func keptGitTree(t *testing.T) (r *testRepo, wt string, tickets TicketIndex) {
	t.Helper()
	r = newTestRepo(t)
	r.commit(".gitignore", "bin/\n*.log\n*.o\n.claude/\nignored-repo/\n")
	r.branch("polecat-ab12")
	wt = r.worktree("polecat-ab12")
	writeFile(t, filepath.Join(wt, "notes.txt"), "someone's work\n")           // untracked, NOT ignored: the keep
	writeFile(t, filepath.Join(wt, "bin", "pogo"), strings.Repeat("x", 4096))  // ignored dir
	writeFile(t, filepath.Join(wt, "build.log"), "log\n")                      // ignored file
	writeFile(t, filepath.Join(wt, "out", "gen.o"), "obj")                     // ignored file in a mixed dir
	writeFile(t, filepath.Join(wt, "out", "draft.txt"), "work in a mixed dir") // untracked, not ignored
	writeFile(t, filepath.Join(wt, ".claude", "settings.json"), "{}")          // pogod's, ignored
	if err := os.MkdirAll(filepath.Join(wt, "ignored-repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "ignored-repo", "work.go"), "package x") // ignored, but a repository
	return r, wt, TicketIndex{"mg-ab12": TicketDone}
}

func TestPruneBuildOutputReportsByDefault(t *testing.T) {
	r, wt, tickets := keptGitTree(t)
	var logs []string
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, Logf: func(f string, a ...any) {
		logs = append(logs, f)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.WorktreesKept) != 1 {
		t.Fatalf("control: the tree must be KEPT for its uncommitted file; kept=%v removed=%v", res.WorktreesKept, res.WorktreesRemoved)
	}
	if res.BuildOutputApplied {
		t.Fatal("BuildOutputApplied without PruneBuildOutput")
	}
	got := prunedPaths(res)
	for _, want := range []string{"bin", "build.log", "out/gen.o"} {
		if !got[filepath.Join(wt, want)] {
			t.Errorf("report lacks %s; got %v", want, got)
		}
		if !exists(filepath.Join(wt, want)) {
			t.Errorf("%s was DELETED by a report-only sweep", want)
		}
	}
	if !strings.Contains(res.Summary(), "would prune") || !strings.Contains(res.Summary(), "--prune-build-output") {
		t.Errorf("summary does not say it is a report and how to flip it:\n%s", res.Summary())
	}
	if !strings.Contains(strings.Join(logs, "\n"), "would prune build output") {
		t.Errorf("no report log line: %v", logs)
	}
}

func TestPruneBuildOutputDeletesOnlyIgnoredWhenSwitchedOn(t *testing.T) {
	r, wt, tickets := keptGitTree(t)
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, PruneBuildOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BuildOutputApplied {
		t.Fatal("BuildOutputApplied = false with the switch on")
	}
	for _, gone := range []string{"bin", "build.log", "out/gen.o"} {
		if exists(filepath.Join(wt, gone)) {
			t.Errorf("%s survived an applied prune", gone)
		}
	}
	for _, kept := range []string{"notes.txt", "out/draft.txt", ".claude/settings.json", "ignored-repo/work.go", ".gitignore"} {
		if !exists(filepath.Join(wt, kept)) {
			t.Errorf("WORK DESTROYED: %s must survive a prune", kept)
		}
	}
	// The tree is still dirty, still kept, still registered.
	if len(res.WorktreesKept) != 1 || !exists(wt) {
		t.Errorf("the tree itself must stay kept: %v", res.WorktreesKept)
	}
}

func TestPruneBuildOutputDryRunNeverDeletes(t *testing.T) {
	r, wt, tickets := keptGitTree(t)
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, PruneBuildOutput: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildOutputApplied || !exists(filepath.Join(wt, "bin", "pogo")) {
		t.Fatal("a dry run deleted build output")
	}
	if len(res.BuildOutputPruned) == 0 {
		t.Fatal("a dry run must still report")
	}
}

func TestPruneBuildOutputNeverTouchesALiveOwner(t *testing.T) {
	r, wt, tickets := keptGitTree(t)
	res, err := Sweep(Options{Repo: r.dir, Tickets: tickets, PruneBuildOutput: true,
		LivePolecats: map[string]bool{"ab12": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.BuildOutputPruned) != 0 || !exists(filepath.Join(wt, "bin", "pogo")) {
		t.Fatalf("a LIVE owner's build output was judged: %v", res.BuildOutputPruned)
	}
}

func TestPruneBuildOutputInAnEnthCheckout(t *testing.T) {
	fakeEnthOnPath(t)
	polecats := t.TempDir()
	tree := filepath.Join(polecats, "cd34")
	enthTree(t, tree, `{"path":"new.rs","op":"put","blob":"z","mode":"file"}`) // dirty: kept
	writeFile(t, filepath.Join(tree, "Cargo.toml"), "[workspace]\n")
	writeFile(t, filepath.Join(tree, "target", "debug", "enth"), strings.Repeat("x", 2048))
	writeFile(t, filepath.Join(tree, "web", "node_modules", "m", "index.js"), "x")
	writeFile(t, filepath.Join(tree, "lean", ".lake", "build", "a.olean"), "x")
	writeFile(t, filepath.Join(tree, "docs", "target", "page.md"), "no Cargo.toml beside me") // not build output
	writeFile(t, filepath.Join(tree, "new.rs"), "fn main() {}")
	res, err := Sweep(Options{Repo: tree, VCS: "enth", PolecatsDir: polecats, PruneBuildOutput: true,
		Tickets: TicketIndex{"mg-cd34": TicketDone}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.WorktreesKept) != 1 {
		t.Fatalf("control: dirty enth checkout must be kept: %v / %v", res.WorktreesKept, res.Errors)
	}
	for _, gone := range []string{"target", "web/node_modules", "lean/.lake/build"} {
		if exists(filepath.Join(tree, gone)) {
			t.Errorf("%s survived", gone)
		}
	}
	for _, kept := range []string{"new.rs", "docs/target/page.md", "a.txt", ".enth/checkout", "lean/.lake"} {
		if !exists(filepath.Join(tree, kept)) {
			t.Errorf("WORK DESTROYED: %s", kept)
		}
	}
}

func TestPruneBuildOutputLeavesWhatEnthTracks(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "w")
	enthTree(t, tree)
	writeFile(t, filepath.Join(tree, "Cargo.toml"), "")
	state := `{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"http://127.0.0.1:1","repo":"o/r"},"paths":[]}
{"path":"target/keep.txt","mode":"file","blob":"x","size":1,"mtime":1,"ino":1}
`
	writeFile(t, filepath.Join(tree, ".enth", "checkout"), state)
	writeFile(t, filepath.Join(tree, "target", "keep.txt"), "tracked")
	a, err := enthBuildOutput(tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Paths) != 0 || len(a.Left) != 1 {
		t.Fatalf("a target/ enth tracks a path under must be left: %+v", a)
	}
}

func TestPruneBuildOutputInADeadOwnersScratchpad(t *testing.T) {
	r, wt, tickets := keptGitTree(t)
	scratch := filepath.Join(t.TempDir(), "claude-501", "-slug-ab12")
	writeFile(t, filepath.Join(scratch, "s1", "scratchpad", "clone", "Cargo.toml"), "")
	writeFile(t, filepath.Join(scratch, "s1", "scratchpad", "clone", "target", "release", "bin"), strings.Repeat("x", 8192))
	writeFile(t, filepath.Join(scratch, "s1", "scratchpad", "notes.md"), "scratch notes")
	// A git repo in the scratchpad that TRACKS its node_modules: left.
	repo := filepath.Join(scratch, "s1", "scratchpad", "js")
	writeFile(t, filepath.Join(repo, "node_modules", "vendored.js"), "x")
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "v"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_, err := Sweep(Options{Repo: r.dir, Tickets: tickets, PruneBuildOutput: true,
		SessionTempDirs: func(workdir string) []string {
			if workdir == wt {
				return []string{scratch}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(scratch, "s1", "scratchpad", "clone", "target")) {
		t.Error("scratchpad target/ survived")
	}
	for _, kept := range []string{"s1/scratchpad/notes.md", "s1/scratchpad/clone/Cargo.toml", "s1/scratchpad/js/node_modules/vendored.js"} {
		if !exists(filepath.Join(scratch, kept)) {
			t.Errorf("WORK DESTROYED: %s", kept)
		}
	}
}

func TestPruneBuildOutputSeenJudgesEachTreeOnce(t *testing.T) {
	r, _, tickets := keptGitTree(t)
	seen := map[string]bool{}
	first, _ := Sweep(Options{Repo: r.dir, Tickets: tickets, BuildOutputSeen: seen})
	second, _ := Sweep(Options{Repo: r.dir, Tickets: tickets, BuildOutputSeen: seen})
	if len(first.BuildOutputPruned) != 1 || len(second.BuildOutputPruned) != 0 {
		t.Fatalf("first=%d second=%d, want 1 then 0", len(first.BuildOutputPruned), len(second.BuildOutputPruned))
	}
}

// A re-make checkout beside a polecat's own tree is that polecat's: the
// liveness gate reads the prefix, and the ticket resolver reads the work item
// (polecat template, drellem2/pogo#282).
func TestRemakeSiblingResolvesToItsPolecat(t *testing.T) {
	if !ownerLive(Options{LivePolecats: map[string]bool{"p1bfc3": true}}, "p1bfc3-remake1") {
		t.Error("a live polecat's re-make must read as live")
	}
	for _, name := range []string{"p1bfc3-remake1", "p1bfc3-remake3", "pe9492-remake2"} {
		want := "mg-" + strings.TrimPrefix(strings.Split(name, "-")[0], "p")
		id, _ := TicketIndex{want: TicketClaimed}.OwnerState(name)
		if id != want {
			t.Errorf("OwnerState(%q) = %q, want %q", name, id, want)
		}
	}
}

// A CARGO_TARGET_DIR in a scratchpad has no Cargo.toml beside it; cargo's
// CACHEDIR.TAG marks it. A target/ with neither is not build output, and a
// node_modules an enth checkout in the scratchpad tracks is left.
func TestScratchBuildOutputShapes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "s", "scratchpad", "target", "CACHEDIR.TAG"), "Signature: 8a477f597d28d172789f06886806bc55")
	writeFile(t, filepath.Join(root, "s", "scratchpad", "target", "debug", "x"), "x")
	writeFile(t, filepath.Join(root, "s", "scratchpad", "notes", "target", "plan.md"), "not build output")
	co := filepath.Join(root, "s", "scratchpad", "co")
	enthTree(t, co)
	writeFile(t, filepath.Join(co, ".enth", "checkout"), `{"format":"enth-checkout","version":1,"base":"b0","origin":{"remote":"http://127.0.0.1:1","repo":"o/r"},"paths":[]}
{"path":"web/node_modules/vendored.js","mode":"file","blob":"x","size":1,"mtime":1,"ino":1}
`)
	writeFile(t, filepath.Join(co, "web", "node_modules", "vendored.js"), "x")
	a, err := scratchBuildOutput(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.Paths, ","); got != filepath.Join("s", "scratchpad", "target") {
		t.Errorf("paths = %q, want only the CACHEDIR.TAG target", got)
	}
	if len(a.Left) != 1 || !strings.Contains(a.Left[0], "tracks web/node_modules/vendored.js") {
		t.Errorf("left = %v, want the enth-tracked node_modules", a.Left)
	}
}
