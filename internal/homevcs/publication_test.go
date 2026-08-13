package homevcs

// Tests for the agent-state publication audit (mg-015c).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingLookup answers with v/err and records every remote it was handed, in
// call order.
func recordingLookup(v Visibility, err error, asked *[]string) VisibilityFunc {
	return func(_ context.Context, remote string) (Visibility, error) {
		if asked != nil {
			*asked = append(*asked, remote)
		}
		return v, err
	}
}

func repoFor(t *testing.T, rep PublicationReport, wantTop string) RepoPublication {
	t.Helper()
	for _, r := range rep.Repos {
		if strings.HasSuffix(r.Toplevel, wantTop) || strings.HasSuffix(wantTop, r.Toplevel) {
			return r
		}
	}
	t.Fatalf("no repo matching %q in %+v", wantTop, rep.Repos)
	return RepoPublication{}
}

// TestAuditPublicationSeesEverySubjectRepo is the mayor's correction made
// executable. The rule is "a repo this host pushes agent state to must not be
// public", and it acquired a second live member — drellem2/pogo-agent-memory,
// created 2026-08-12 — while this check was being written. A detector that found
// one repo would have been visibly wrong against a repo that already existed.
func TestAuditPublicationSeesEverySubjectRepo(t *testing.T) {
	pogoHome := t.TempDir()
	gitInit(t, pogoHome)
	gitRun(t, pogoHome, "remote", "add", "origin", "https://github.com/drellem2/pogo-config.git")

	memory := t.TempDir()
	gitInit(t, memory)
	gitRun(t, memory, "remote", "add", "origin", "https://github.com/drellem2/pogo-agent-memory.git")

	var asked []string
	rep := AuditPublication(context.Background(), []Subject{
		{Label: "$POGO_HOME", Dir: pogoHome},
		{Label: "agent memory", Dir: memory},
	}, recordingLookup(VisibilityPrivate, nil, &asked))

	if len(rep.Repos) != 2 {
		t.Fatalf("Repos = %+v, want both subject repos", rep.Repos)
	}
	if len(asked) != 2 {
		t.Fatalf("resolver was asked %v; want one call per distinct repo", asked)
	}
	// The names must come from each host's own remote, never a literal.
	joined := strings.Join(asked, " ")
	for _, want := range []string{"drellem2/pogo-config", "drellem2/pogo-agent-memory"} {
		if !strings.Contains(joined, want) {
			t.Errorf("resolver was asked %v; missing %q", asked, want)
		}
	}
	if got := repoFor(t, rep, memory).Name; got != "drellem2/pogo-agent-memory" {
		t.Errorf("Name = %q for the memory repo, want the owner/name `gh repo edit` accepts", got)
	}
}

// TestAuditPublicationDeduplicatesByWorkTree. Every per-agent memory dir under
// $POGO_HOME resolves to the same work tree, so without de-duplication a single
// repo would be asked about a dozen times — a dozen network calls and a dozen
// chances for two answers about one repo to disagree.
func TestAuditPublicationDeduplicatesByWorkTree(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	gitRun(t, home, "remote", "add", "origin", "https://github.com/drellem2/pogo-config.git")
	nested := filepath.Join(home, "agents", "crew", "pa", "memory")
	write(t, filepath.Join(nested, "MEMORY.md"), "index\n")

	var asked []string
	rep := AuditPublication(context.Background(), []Subject{
		{Label: "$POGO_HOME", Dir: home},
		{Label: "agent memory pa", Dir: nested},
	}, recordingLookup(VisibilityPrivate, nil, &asked))

	if len(rep.Repos) != 1 {
		t.Fatalf("Repos = %+v, want one — both subjects live in the same work tree", rep.Repos)
	}
	if len(asked) != 1 {
		t.Errorf("resolver was asked %v; want exactly one call for one repo", asked)
	}
	if len(rep.Repos[0].Holds) != 2 {
		t.Errorf("Holds = %v, want both subjects named so a reader knows what is at stake", rep.Repos[0].Holds)
	}
}

// TestAuditPublicationReportsPublic is the RED path. Both live subjects are
// private, so this detector will spend its life green; if nothing exercises the
// path that fires, it ships unseen.
func TestAuditPublicationReportsPublic(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	gitRun(t, home, "remote", "add", "origin", "git@github.com:drellem2/pogo-config.git")

	rep := AuditPublication(context.Background(),
		[]Subject{{Label: "$POGO_HOME", Dir: home}},
		recordingLookup(VisibilityPublic, nil, nil))

	if len(rep.Repos) != 1 || rep.Repos[0].Visibility != VisibilityPublic {
		t.Fatalf("Repos = %+v, want one PUBLIC repo", rep.Repos)
	}
	if got := rep.Repos[0].Exposure(); got != 2 {
		t.Errorf("Exposure() = %d for a PUBLIC repo, want 2 — it must sort above every clean repo", got)
	}
}

// TestAuditPublicationSortsMostExposedFirst. A renderer that caps its list
// truncates the tail, so a public repo that sorted behind private ones could be
// elided out of the very row that exists to name it.
func TestAuditPublicationSortsMostExposedFirst(t *testing.T) {
	private, unknown, public := t.TempDir(), t.TempDir(), t.TempDir()
	for _, d := range []string{private, unknown, public} {
		gitInit(t, d)
		gitRun(t, d, "remote", "add", "origin", "https://github.com/acme/"+filepath.Base(d)+".git")
	}
	byDir := map[string]struct {
		v   Visibility
		err error
	}{
		private: {VisibilityPrivate, nil},
		unknown: {"", errors.New("rate limited")},
		public:  {VisibilityPublic, nil},
	}
	lookup := func(_ context.Context, remote string) (Visibility, error) {
		for d, ans := range byDir {
			if strings.Contains(remote, filepath.Base(d)) {
				return ans.v, ans.err
			}
		}
		return "", errors.New("unexpected remote " + remote)
	}

	rep := AuditPublication(context.Background(), []Subject{
		{Label: "private", Dir: private},
		{Label: "unknown", Dir: unknown},
		{Label: "public", Dir: public},
	}, lookup)

	if len(rep.Repos) != 3 {
		t.Fatalf("Repos = %+v, want 3", rep.Repos)
	}
	if rep.Repos[0].Visibility != VisibilityPublic {
		t.Errorf("Repos[0] = %+v, want the PUBLIC repo first", rep.Repos[0])
	}
	if rep.Repos[1].Unknown == "" {
		t.Errorf("Repos[1] = %+v, want the unestablished repo above the private one", rep.Repos[1])
	}
	if rep.Repos[2].Visibility != VisibilityPrivate {
		t.Errorf("Repos[2] = %+v, want the PRIVATE repo last", rep.Repos[2])
	}
}

// TestAuditPublicationReportsUnestablishedRatherThanPrivate. The failure class
// this ticket was filed against is the instrument that goes quiet when it stops
// being able to see. An unauthenticated or rate-limited `gh` looks identical to
// a private repo from in here, and must not be recorded as one.
func TestAuditPublicationReportsUnestablishedRatherThanPrivate(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	gitRun(t, home, "remote", "add", "origin", "https://github.com/drellem2/pogo-config.git")

	rep := AuditPublication(context.Background(),
		[]Subject{{Label: "$POGO_HOME", Dir: home}},
		recordingLookup("", errors.New("HTTP 401: Bad credentials"), nil))

	r := rep.Repos[0]
	if r.Visibility != "" {
		t.Errorf("Visibility = %q; a resolver that failed established nothing", r.Visibility)
	}
	if !strings.Contains(r.Unknown, "Bad credentials") {
		t.Errorf("Unknown = %q, want the resolver's reason carried through", r.Unknown)
	}
	if r.Exposure() == 0 {
		t.Error("Exposure() = 0 for an unestablished repo; that ranks it with the ones known to be safe")
	}
}

// TestAuditPublicationWithNoRemoteIsDecided. A repo that pushes nowhere is a
// decided answer, not an undecided one — reporting it as "could not establish"
// would make every developer's local-only home a standing warning and train
// readers past the row.
func TestAuditPublicationWithNoRemoteIsDecided(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)

	called := false
	rep := AuditPublication(context.Background(),
		[]Subject{{Label: "$POGO_HOME", Dir: home}},
		func(context.Context, string) (Visibility, error) { called = true; return VisibilityPublic, nil })

	if called {
		t.Error("the resolver was consulted for a repo with no origin; there is nothing published to ask about")
	}
	r := rep.Repos[0]
	if r.Remote != "" || r.Visibility != "" || r.Unknown != "" {
		t.Errorf("repo = %+v; want a remote-less repo to carry no publication state at all", r)
	}
	if r.Exposure() != 0 {
		t.Errorf("Exposure() = %d, want 0 for a repo that pushes nowhere", r.Exposure())
	}
}

// TestAuditPublicationSeparatesNoRepoFromCouldNotLook. "There is no repository
// here" and "I could not tell" are different facts, and folding the second into
// the first is the unearned all-clear this package exists to argue against.
func TestAuditPublicationSeparatesNoRepoFromCouldNotLook(t *testing.T) {
	plain := t.TempDir()
	missing := filepath.Join(t.TempDir(), "was-never-created")

	rep := AuditPublication(context.Background(), []Subject{
		{Label: "plain dir", Dir: plain},
		{Label: "absent dir", Dir: missing},
	}, recordingLookup(VisibilityPrivate, nil, nil))

	if len(rep.Repos) != 0 {
		t.Fatalf("Repos = %+v, want none", rep.Repos)
	}
	if len(rep.Unversioned) != 1 || !strings.Contains(rep.Unversioned[0], "plain dir") {
		t.Errorf("Unversioned = %v, want the directory under no repo", rep.Unversioned)
	}
	if len(rep.Undecided) != 1 || !strings.Contains(rep.Undecided[0], "absent dir") {
		t.Errorf("Undecided = %v, want the unreadable directory kept separate", rep.Undecided)
	}
}

// TestAuditPublicationWithNoResolverSaysSo. A nil resolver is a wiring mistake,
// and the one answer it must not produce is silence that reads like an
// all-clear.
func TestAuditPublicationWithNoResolverSaysSo(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	gitRun(t, home, "remote", "add", "origin", "https://github.com/drellem2/pogo-config.git")

	rep := AuditPublication(context.Background(), []Subject{{Label: "$POGO_HOME", Dir: home}}, nil)
	if rep.Repos[0].Visibility != "" {
		t.Errorf("Visibility = %q, want empty when nothing asked", rep.Repos[0].Visibility)
	}
	if rep.Repos[0].Unknown == "" {
		t.Error("Unknown is empty for a remote nothing asked about; that is indistinguishable from a repo with no remote")
	}
}

// TestAuditPublicationDoesNotWriteToTheSubject. Same constraint as the sibling
// audit: this runs against directories holding a live fleet's mail, schedules
// and memory corpus. The index's mtime standing in for "did anything write" is
// the cheapest observation that would catch a git subcommand that mutates.
func TestAuditPublicationDoesNotWriteToTheSubject(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	gitRun(t, home, "remote", "add", "origin", "https://github.com/drellem2/pogo-config.git")

	index := filepath.Join(home, ".git", "index")
	before, err := statMod(index)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if rep := AuditPublication(context.Background(),
		[]Subject{{Label: "$POGO_HOME", Dir: home}},
		recordingLookup(VisibilityPrivate, nil, nil)); len(rep.Repos) == 0 {
		t.Fatal("audit found nothing, so this test would pass vacuously")
	}
	after, err := statMod(index)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if !after.Equal(before) {
		t.Errorf("the audit rewrote .git/index (%v -> %v); it may read these directories and may not write to them", before, after)
	}
}

// statMod is os.Stat's mtime, named so the assertion above reads as the
// observation it is.
func statMod(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// TestAuditPublicationFoldsNestedSubjectsIntoTheirEnclosingOne is the ceiling
// problem made a test. pogod sets GIT_CEILING_DIRECTORIES=$POGO_HOME on itself
// and everything it spawns (internal/gitceiling), so `git -C` in a per-agent
// memory dir under $POGO_HOME answers "not a git repository" even though that
// dir sits inside pogo-config's work tree. Twelve of this host's seventeen
// subjects are in that position. Taking the refusal at face value would print
// "nothing there is pushed anywhere" twelve times about directories inside a
// repo with a remote — false, and enough noise to bury the two real verdicts.
func TestAuditPublicationFoldsNestedSubjectsIntoTheirEnclosingOne(t *testing.T) {
	home := t.TempDir()
	gitInit(t, home)
	gitRun(t, home, "remote", "add", "origin", "https://github.com/drellem2/pogo-config.git")

	subs := []Subject{{Label: "$POGO_HOME", Dir: home}}
	for _, name := range []string{"mayor", "doctor", "pa"} {
		dir := filepath.Join(home, "agents", "crew", name, "memory")
		write(t, filepath.Join(dir, "MEMORY.md"), "index\n")
		subs = append(subs, Subject{Label: "agent memory " + name, Dir: dir})
	}
	// A sibling directory whose name merely starts with the home's path must
	// NOT be folded in: containment is a component comparison, not a prefix.
	sibling := home + "-elsewhere"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	subs = append(subs, Subject{Label: "unrelated", Dir: sibling})

	// The stub answers only for a resolvable repo; if a nested dir were
	// resolved on its own it would land in Unversioned instead.
	rep := AuditPublication(context.Background(), subs, recordingLookup(VisibilityPrivate, nil, nil))

	if len(rep.Repos) != 1 {
		t.Fatalf("Repos = %+v, want exactly the enclosing repo", rep.Repos)
	}
	if got := len(rep.Repos[0].Holds); got != 4 {
		t.Errorf("Holds = %v (%d), want $POGO_HOME plus the 3 nested memory dirs it answers for",
			rep.Repos[0].Holds, got)
	}
	if len(rep.Unversioned) != 1 || !strings.Contains(rep.Unversioned[0], "unrelated") {
		t.Errorf("Unversioned = %v; the sibling sharing a path PREFIX is not nested and must be resolved on its own",
			rep.Unversioned)
	}
}
