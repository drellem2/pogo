package search

import (
	"testing"

	"github.com/hashicorp/go-hclog"

	"github.com/drellem2/pogo/pkg/plugin"
)

// gitHashWarnFor rebuilds the complete text the two call sites emit for root.
// git's own error string is the message's one variable field, so — following
// af0f444's pattern for the reopen refusal — the expected text is constructed
// from the parts the code owns plus the error the fixture is built to provoke,
// and compared for equality. Nothing here matches a substring or a prefix, so a
// differently worded git-hash failure added later is a different line and
// cannot be folded into these counts.
func gitHashWarnFor(root, gitErr string) string {
	return "Could not read git tree hash for " + root + ": " + gitErr
}

// gitMissingHeadErr is what `git rev-parse HEAD^{tree}` returns for a directory
// that is not a repo — the failure every fixture here provokes, and the text
// live pogod.log lines carry. Naming it lets the counting helper compare for
// equality instead of matching a prefix.
const gitMissingHeadErr = "exit status 128"

// countGitHashWarnings counts warn-level records reporting a git-tree-hash read
// failure for exactly root.
//
// Equality on the complete message, not a prefix. An earlier version matched
// with strings.HasPrefix, which was bounded and safe in practice — but it was
// the one prefix match in a change whose whole subject is that suppressing log
// lines by prefix silently widens over time. If git ever words this failure
// differently, equality fails loudly here and in
// TestGitTreeHashWarningKeepsItsFullMessage together, which is the outcome the
// house rule wants.
func countGitHashWarnings(recs []map[string]any, root string) []map[string]any {
	return findLogged(recs, "warn", gitHashWarnFor(root, gitMissingHeadErr))
}

// TestGitTreeHashWarningIsOncePerProjectNotPerSite is the acceptance criterion
// for the (c) half of gh#111. The warning keeps its level — the first
// occurrence is real signal that the index cannot use its git fast path for
// that project — but stops repeating for the lifetime of the process.
//
// The dedupe must be keyed by PROJECT. There are two call sites, one on the
// save path (serializeProjectIndex) and one on the load path (Load), and this
// test drives a single project through both plus a repeat of the first. A
// per-site dedupe passes a one-site test and fails this one.
func TestGitTreeHashWarningIsOncePerProjectNotPerSite(t *testing.T) {
	// The fixture root is a plain directory with no .git, so `git rev-parse
	// HEAD^{tree}` fails there exactly as it does for the reporter.
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req) // save-path site
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	bs.ReIndex(root) // save-path site again
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	if _, err := bs.Load(root); err != nil { // load-path site
		t.Fatalf("Load: %v", err)
	}
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	got := countGitHashWarnings(recs, root)
	if len(got) != 1 {
		t.Errorf("want exactly 1 git-tree-hash warning across 3 failing reads, got %d:\n%s",
			len(got), formatRecords(recs))
	}
	// It is still a warning: the point of the fix is the repetition, not the
	// level. pm-pogo overruled demoting this line to Debug.
	if len(got) == 1 && recLevel(got[0]) != "warn" {
		t.Errorf("git-tree-hash warning logged at %q, want warn", recLevel(got[0]))
	}
}

// TestGitTreeHashWarningIsPerProject guards the other direction: "once" is
// once per project, not once per process. Suppressing the second project's
// first failure would hide a real fact about a repo nobody had been told
// about.
func TestGitTreeHashWarningIsPerProject(t *testing.T) {
	bsA, rootA, eventsA := newTestProject(t, map[string]string{"a.txt": "alpha\n"})
	capture := captureLogs(t, bsA, hclog.Trace)

	// A second project registered against the SAME BasicSearch, which is what
	// pogod does — one search service, every project on the host.
	_, rootB, _ := newTestProject(t, map[string]string{"b.txt": "beta\n"})

	for _, root := range []string{rootA, rootB} {
		req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
		bsA.Index(&req)
		waitIndexed(t, eventsA, root)
	}
	quiesceForLogs(t, bsA)

	recs := capture.records(t)
	for _, root := range []string{rootA, rootB} {
		if got := countGitHashWarnings(recs, root); len(got) != 1 {
			t.Errorf("want 1 git-tree-hash warning for %s, got %d:\n%s",
				root, len(got), formatRecords(recs))
		}
	}
}

// TestEvictClearsGitTreeHashDedupe is the leak guard. The dedupe map lives for
// the whole process, and pogod runs for weeks; without removal it would grow
// an entry for every root ever seen, including roots the daemon has since
// dropped. Evicting a project must take its entry with it — which also means a
// project that comes back reports its first failure again.
func TestEvictClearsGitTreeHashDedupe(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{"a.txt": "alpha\n"})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	bs.gitHashWarnedMu.Lock()
	_, tracked := bs.gitHashWarned[root]
	bs.gitHashWarnedMu.Unlock()
	if !tracked {
		t.Fatalf("indexing a non-git project recorded no dedupe entry; the rest of this test proves nothing")
	}

	bs.Evict(root)

	bs.gitHashWarnedMu.Lock()
	n := len(bs.gitHashWarned)
	bs.gitHashWarnedMu.Unlock()
	if n != 0 {
		t.Errorf("dedupe map retained %d entry/entries after eviction; it outlives g.projects and leaks", n)
	}

	// And the re-registered project warns again on its first failure.
	capture.reset()
	bs.Index(&req)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	if got := countGitHashWarnings(recs, root); len(got) != 1 {
		t.Errorf("want 1 git-tree-hash warning after re-registration, got %d:\n%s",
			len(got), formatRecords(recs))
	}
}

// TestGitTreeHashWarningKeepsItsFullMessage pins the text, because the dedupe
// routes both call sites through one method and a rewrite there would silently
// change what operators grep for.
func TestGitTreeHashWarningKeepsItsFullMessage(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{"a.txt": "alpha\n"})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	got := findLogged(recs, "warn", gitHashWarnFor(root, gitMissingHeadErr))
	if len(got) != 1 {
		t.Errorf("want the unchanged %q message, got %d match(es):\n%s",
			gitHashWarnFor(root, gitMissingHeadErr), len(got), formatRecords(recs))
	}
}
