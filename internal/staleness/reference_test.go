package staleness

// Controls for the reference's own staleness (mg-afd0).
//
// The defect these reconstruct: the prompt witness compared the installed
// corpus against ~/.pogo/deploy-src's `origin/main`, a ref the nightly fetches
// at DEPLOY TIME and never after, and reported "ok: all 9 shipped prompt(s)
// match the reference" while five prompt-touching commits sat on the real
// origin/main. The comparison was true and answered the wrong question.
//
// Every fixture here is a pair of LOCAL repos wired remote-to-mirror, so the
// whole shape — frozen tracking ref, live remote ahead of it, objects present
// or absent — is constructible without a network and without waiting a night.
// That was the property the original ticket asked of both other halves and the
// one this half did not have.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mirrorOf clones src the way the deploy runner does: a working clone with an
// `origin` remote, whose refs/remotes/origin/main is a SNAPSHOT taken at clone
// time and moves only when something fetches.
func mirrorOf(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "mirror")
	git(t, ".", "clone", "-q", src, dst)
	return dst
}

// advance adds a commit to repo touching rel, and returns nothing a test should
// read back — the point is always the ref, which the test resolves itself.
func advance(t *testing.T, repo, rel, body, subject string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, filepath.FromSlash(rel)), []byte(body))
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", subject)
}

func head(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := gitOut(context.Background(), repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, repo, err)
	}
	return strings.TrimSpace(string(out))
}

// TestReferenceFrozenAtDeployIsNotClean is the ticket's exact shape.
//
// A mirror cloned before a prompt fix shipped; an installed corpus that matches
// that mirror perfectly. Every file-by-file comparison is clean, and the honest
// verdict is still that the fleet is behind — which is the sentence nothing
// emitted before this change.
func TestReferenceFrozenAtDeployIsNotClean(t *testing.T) {
	v1 := lines(120, "doctor v1")
	upstream := fixtureRepo(t, map[string][]byte{"crew/doctor.md": v1})
	mirror := mirrorOf(t, upstream)
	deployed := head(t, mirror, "origin/main")

	installed := t.TempDir()
	writeFile(t, filepath.Join(installed, "crew", "doctor.md"), stamped(v1))

	// The prompt fix ships AFTER the deploy. The mirror is not told.
	advance(t, upstream, PromptsSubtree+"/crew/doctor.md", string(lines(121, "doctor v2")),
		"the DOCTOR's refinery-history advice names its window")

	rep := CheckPrompts(context.Background(), PromptOptions{
		Repo: mirror, Ref: "origin/main", InstalledRoot: installed,
	})
	if rep.Err != "" {
		t.Fatalf("CheckPrompts: %s", rep.Err)
	}
	if len(rep.Deltas) != 0 {
		t.Fatalf("the corpus does match the frozen reference; deltas = %+v", rep.Deltas)
	}
	if rep.Reference.Commit != deployed {
		t.Fatalf("reference resolved to %s, want the deployed revision %s", rep.Reference.Commit, deployed)
	}
	if !rep.Remote.Armed {
		t.Fatalf("remote not armed against a mirror with an origin: %+v", rep.Remote)
	}
	if !rep.Remote.Behind {
		t.Fatalf("a reference a commit behind its remote read as level: %+v", rep.Remote)
	}
	if rep.Remote.Counted {
		t.Errorf("counted a gap whose objects the mirror has never fetched: %+v", rep.Remote)
	}
	if rep.Clean() {
		t.Fatal("reported clean over a reference that cannot see the window that matters — " +
			"this is the whole defect: a true file-by-file comparison against the wrong revision")
	}

	// --fetch answers the question the default run can only pose.
	fetched := CheckPrompts(context.Background(), PromptOptions{
		Repo: mirror, Ref: "origin/main", InstalledRoot: installed, Fetch: true,
	})
	if fetched.Err != "" {
		t.Fatalf("CheckPrompts --fetch: %s", fetched.Err)
	}
	if fetched.Reference.Commit != head(t, upstream, "main") {
		t.Errorf("after --fetch the reference is %s, want the remote head %s",
			fetched.Reference.Commit, head(t, upstream, "main"))
	}
	if fetched.Remote.Was != deployed {
		t.Errorf("Was = %q, want the pre-fetch (deployed) revision %s — the fetch is the only thing "+
			"that erases the local record of it, so it has to be captured before", fetched.Remote.Was, deployed)
	}
	if !fetched.Remote.Counted || fetched.Remote.Commits != 1 || fetched.Remote.CorpusCommits != 1 {
		t.Fatalf("gap after --fetch = %+v, want 1 commit / 1 touching the corpus", fetched.Remote)
	}
	if len(fetched.Remote.Corpus) != 1 || !strings.Contains(fetched.Remote.Corpus[0].Subject, "refinery-history") {
		t.Errorf("corpus commits = %+v — a count says how far behind, only a subject says behind on WHAT",
			fetched.Remote.Corpus)
	}
	if len(fetched.Deltas) != 1 || fetched.Deltas[0].Path != "crew/doctor.md" {
		t.Fatalf("after --fetch the corpus is judged against what SHIPPED; deltas = %+v", fetched.Deltas)
	}
	if fetched.Clean() {
		t.Fatal("stayed quiet over an installed corpus that differs from the remote head")
	}
}

// TestRemoteAheadWithNoCorpusCommitsIsNotAFinding is the anti-over-claim
// control, and it is here because this ticket's own body had to be corrected
// for the same fault: it cited a rule ("a fix merging after the nightly is
// inert for ~24h") wider than what was measured.
//
// This witness judges the PROMPT CORPUS. A reference behind on commits that
// touch nothing under the corpus subtree leaves the corpus verdict exactly
// right, and reporting it as a finding would be the artifact complaining about
// over-confident reporting doing the same thing.
func TestRemoteAheadWithNoCorpusCommitsIsNotAFinding(t *testing.T) {
	body := lines(30, "mayor")
	upstream := fixtureRepo(t, map[string][]byte{"mayor.md": body})
	mirror := mirrorOf(t, upstream)
	frozen := head(t, mirror, "origin/main")

	installed := t.TempDir()
	writeFile(t, filepath.Join(installed, "mayor.md"), stamped(body))

	advance(t, upstream, "internal/scheduler/sched.go", "package scheduler\n", "nothing to do with prompts")

	// Give the mirror the objects but leave its tracking ref where the deploy
	// left it — the state of a repo that fetched for some other reason. This is
	// what makes the gap COUNTABLE, which is the branch under test.
	git(t, mirror, "fetch", "-q", "origin")
	git(t, mirror, "update-ref", "refs/remotes/origin/main", frozen)

	rep := CheckPrompts(context.Background(), PromptOptions{
		Repo: mirror, Ref: "origin/main", InstalledRoot: installed,
	})
	if rep.Err != "" {
		t.Fatalf("CheckPrompts: %s", rep.Err)
	}
	if !rep.Remote.Behind || !rep.Remote.Counted {
		t.Fatalf("remote state = %+v, want behind and countable", rep.Remote)
	}
	if rep.Remote.Commits != 1 || rep.Remote.CorpusCommits != 0 {
		t.Fatalf("gap = %d commits / %d corpus, want 1 / 0", rep.Remote.Commits, rep.Remote.CorpusCommits)
	}
	if !rep.Clean() {
		t.Fatal("reported a finding over commits that touch nothing this witness judges — " +
			"the corpus verdict is about the corpus, and claiming more is the defect this ticket is about")
	}
}

// TestFetchStateDatesTheFetchNotTheRefMove.
//
// git rewrites a ref only when it MOVES, so a remote-tracking ref's mtime dates
// the last time the branch changed. A mirror that fetched an hour ago and found
// nothing new would date itself days old by ref mtime, and this row is used to
// decide how much to trust the verdict beneath it.
func TestFetchStateDatesTheFetchNotTheRefMove(t *testing.T) {
	upstream := fixtureRepo(t, map[string][]byte{"mayor.md": lines(3, "m")})
	mirror := mirrorOf(t, upstream)
	git(t, mirror, "fetch", "-q", "origin")

	now := time.Now()
	fetchHead := filepath.Join(mirror, ".git", "FETCH_HEAD")
	refFile := filepath.Join(mirror, ".git", "refs", "remotes", "origin", "main")
	// A clone leaves its remote refs PACKED, so write a loose one by hand: this
	// test is about which of the two files decides, and it needs both to exist.
	// `update-ref` to the value already there is a no-op and creates nothing.
	writeFile(t, refFile, []byte(head(t, mirror, "origin/main")+"\n"))

	recent := now.Add(-time.Hour)
	ancient := now.Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(fetchHead, recent, recent); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(refFile, ancient, ancient); err != nil {
		t.Fatalf("aging the loose ref at %s: %v", refFile, err)
	}

	st := ReadFetchState(context.Background(), mirror, now)
	if !st.Known() {
		t.Fatalf("fetch time unknown in a repo that has fetched: %+v", st)
	}
	if st.AgeSeconds < 3500 || st.AgeSeconds > 3700 {
		t.Errorf("AgeSeconds = %d, want ~3600 — the ref's own mtime is ten days old and must not be "+
			"what decides, or a repo that fetched and found nothing new reads as abandoned", st.AgeSeconds)
	}
	if !strings.HasSuffix(st.Source, "FETCH_HEAD") {
		t.Errorf("Source = %q, want the FETCH_HEAD path — a timestamp with no named source is the same "+
			"kind of unsupported claim as the label this ticket removed", st.Source)
	}
}

// TestFetchStateUnknownIsSaidNotGuessed. A repo that has never fetched has no
// fetch time, and a zero would render as "just fetched" — the most dangerous
// possible reading of an absent record.
func TestFetchStateUnknownIsSaidNotGuessed(t *testing.T) {
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": lines(3, "m")})

	st := ReadFetchState(context.Background(), repo, time.Now())
	if st.Known() {
		t.Fatalf("claimed a fetch time for a repo that has never fetched: %+v", st)
	}
	if st.Why == "" {
		t.Error("unknown fetch time carries no reason")
	}
	if st.AgeSeconds != 0 || st.At != "" {
		t.Errorf("unknown state leaked a value: %+v", st)
	}
}

// TestFetchReferencePreservesTheDeployFetchTimestamp — the remedy checked
// against the defect it remedies.
//
// The fix for a reference frozen at deploy time is to fetch it. A plain
// `git fetch` rewrites FETCH_HEAD, which is the one file the "last fetched"
// row above is read from — so running the fix would erase the evidence of the
// fault and every later run would report the reference as freshly fetched when
// what it means is "this detector fetched it". --no-write-fetch-head is why
// that does not happen; this asserts it, and the flag's absence on an older git
// is reported rather than hidden.
func TestFetchReferencePreservesTheDeployFetchTimestamp(t *testing.T) {
	upstream := fixtureRepo(t, map[string][]byte{"mayor.md": lines(3, "m")})
	mirror := mirrorOf(t, upstream)
	git(t, mirror, "fetch", "-q", "origin")

	fetchHead := filepath.Join(mirror, ".git", "FETCH_HEAD")
	deployFetch := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(fetchHead, deployFetch, deployFetch); err != nil {
		t.Fatal(err)
	}

	advance(t, upstream, "README.md", "moved on\n", "something shipped")

	target, err := ResolveRemoteTarget(context.Background(), mirror, "origin/main")
	if err != nil {
		t.Fatalf("ResolveRemoteTarget: %v", err)
	}
	moved, err := FetchReference(context.Background(), mirror, target, 30*time.Second)
	if err != nil {
		t.Fatalf("FetchReference: %v", err)
	}

	if got := head(t, mirror, "origin/main"); got != head(t, upstream, "main") {
		t.Fatalf("the fetch did not move the tracking ref: %s", got)
	}
	fi, err := os.Stat(fetchHead)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		// An older git. The caller is told, which is the whole contract.
		t.Logf("this git does not know --no-write-fetch-head; stamp moved, and FetchReference said so")
		return
	}
	if !fi.ModTime().Truncate(time.Second).Equal(deployFetch) {
		t.Errorf("FETCH_HEAD moved from %s to %s — the fix erased the evidence the fault is read from, "+
			"and reported stampMoved=false while doing it", deployFetch, fi.ModTime())
	}
}

// TestRemoteStateCleanRule pins the one judgement call in the change, because
// it is the line between "the detector under-reports" (a gap) and "the detector
// cries wolf on every laptop" (a report nobody reads).
func TestRemoteStateCleanRule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		st    RemoteState
		clean bool
	}{
		{"no remote to consult", RemoteState{Armed: false, Err: "no origin"}, true},
		{"armed but unreachable", RemoteState{Armed: true, Err: "network is down"}, true},
		{"level with the remote", RemoteState{Armed: true, Behind: false}, true},
		{"behind, gap unknowable", RemoteState{Armed: true, Behind: true, Counted: false}, false},
		{"behind, nothing touching the corpus", RemoteState{Armed: true, Behind: true, Counted: true, Commits: 9, CorpusCommits: 0}, true},
		{"behind, a prompt shipped", RemoteState{Armed: true, Behind: true, Counted: true, Commits: 9, CorpusCommits: 1}, false},
	} {
		if got := tc.st.Clean(); got != tc.clean {
			t.Errorf("%s: Clean() = %v, want %v", tc.name, got, tc.clean)
		}
	}
}

// TestResolveRemoteTargetShapes. The remote is resolved from the repo's own
// configuration rather than by splitting the ref string, because
// "origin/release/2026-08" does not split the obvious way and a local branch
// does not split at all while still having a remote to be behind. A guess here
// would produce a confident row about the wrong branch.
func TestResolveRemoteTargetShapes(t *testing.T) {
	upstream := fixtureRepo(t, map[string][]byte{"mayor.md": lines(3, "m")})
	mirror := mirrorOf(t, upstream)

	got, err := ResolveRemoteTarget(context.Background(), mirror, "origin/main")
	if err != nil {
		t.Fatalf("origin/main: %v", err)
	}
	if got.Name != "origin" || got.Branch != "main" || got.URL == "" {
		t.Errorf("origin/main resolved to %+v", got)
	}

	// A working checkout's local branch, via its upstream — the shape a
	// developer running this from ~/dev/pogo hits.
	got, err = ResolveRemoteTarget(context.Background(), mirror, "main")
	if err != nil {
		t.Fatalf("main: %v", err)
	}
	if got.Name != "origin" || got.Branch != "main" {
		t.Errorf("local main resolved to %+v", got)
	}

	// A repo with no remote at all: not armed, with a reason. The caller prints
	// the disarm rather than skipping it.
	if _, err := ResolveRemoteTarget(context.Background(), upstream, "main"); err == nil {
		t.Error("a branch with no upstream resolved to a remote target")
	}
}

// TestCheckPromptsSkipRemoteLeavesTheCorpusVerdictIntact. --skip-remote is for
// an offline host; it must cost the qualifier and nothing else.
func TestCheckPromptsSkipRemoteLeavesTheCorpusVerdictIntact(t *testing.T) {
	body := lines(40, "mayor")
	upstream := fixtureRepo(t, map[string][]byte{"mayor.md": body})
	mirror := mirrorOf(t, upstream)
	advance(t, upstream, PromptsSubtree+"/mayor.md", string(lines(41, "mayor")), "a prompt shipped")

	installed := t.TempDir()
	writeFile(t, filepath.Join(installed, "mayor.md"), stamped(body))

	rep := CheckPrompts(context.Background(), PromptOptions{
		Repo: mirror, Ref: "origin/main", InstalledRoot: installed, SkipRemote: true,
	})
	if rep.Err != "" {
		t.Fatalf("CheckPrompts: %s", rep.Err)
	}
	if rep.Remote.Armed {
		t.Errorf("--skip-remote still consulted the remote: %+v", rep.Remote)
	}
	if len(rep.Deltas) != 0 || !rep.Clean() {
		t.Errorf("--skip-remote changed the corpus verdict: deltas=%+v clean=%v", rep.Deltas, rep.Clean())
	}
	// The fetch age is local evidence and survives --skip-remote, which is what
	// keeps the row carrying its limit on a host that cannot reach anything.
	if rep.Reference.Fetch.Known() == (rep.Reference.Fetch.At == "") {
		t.Errorf("fetch state is self-contradictory: %+v", rep.Reference.Fetch)
	}
}
