package gitgc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeSessionTemp returns a SessionTempDirs func shaped like Claude Code's
// (internal/claude.SessionTempDir): <root>/<slug-of-workdir>, where the slug
// turns every byte outside [A-Za-z0-9] into '-'. gitgc cannot import the real
// one (internal/claude depends on this package), so the encoding is restated
// here; internal/claude pins the real one against this machine.
func fakeSessionTemp(root string) func(string) []string {
	return func(workdir string) []string {
		var b strings.Builder
		for _, r := range workdir {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				b.WriteByte('-')
			}
		}
		return []string{filepath.Join(root, b.String())}
	}
}

// mkSessionTemp creates the temp dir the fake provider assigns to workdir,
// with a scratch file and a symlink out of it — the shape gh #203 reported
// (tasks/*.output links into the durable transcript store).
func mkSessionTemp(t *testing.T, fn func(string) []string, workdir, linkTarget string) string {
	t.Helper()
	dir := fn(workdir)[0]
	if err := os.MkdirAll(filepath.Join(dir, "sess", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sess", "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, filepath.Join(dir, "sess", "tasks", "a.output")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestOrphanSessionTempTouchesOnlyEligiblePolecatSlugs is the PM's required
// test (gh #203): a crew slug and a non-pogo slug sit next to an eligible
// polecat slug in the same temp root, and only the polecat's dir is removed.
// It also pins the other keeps: live, unconcluded, owner dir still present, and
// a slug that may be a subdirectory session of a live polecat.
func TestOrphanSessionTempTouchesOnlyEligiblePolecatSlugs(t *testing.T) {
	r := newTestRepo(t)
	base := t.TempDir()
	polecats := filepath.Join(base, ".pogo", "polecats")
	agents := filepath.Join(base, ".pogo", "agents")
	if err := os.MkdirAll(polecats, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpRoot := filepath.Join(base, "tmp", "claude-501")
	fn := fakeSessionTemp(tmpRoot)

	// The durable store the symlinks point into must survive: RemoveAll must
	// not follow them.
	durable := filepath.Join(base, "durable.jsonl")
	if err := os.WriteFile(durable, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	eligible := mkSessionTemp(t, fn, filepath.Join(polecats, "aaaa"), durable)
	// Crew agent named exactly like a concluded ticket's polecat: its slug is
	// under agents/, so it is never a candidate whatever mg says about "bbbb".
	crew := mkSessionTemp(t, fn, filepath.Join(agents, "bbbb"), durable)
	crewNamed := mkSessionTemp(t, fn, filepath.Join(agents, "pm-pogo"), durable)
	// Non-pogo slugs: a source repo and a path merely CONTAINING "polecats".
	nonPogo := mkSessionTemp(t, fn, filepath.Join(base, "dev", "pogo"), durable)
	lookalike := mkSessionTemp(t, fn, filepath.Join(base, "work", "polecats", "aaaa"), durable)
	live := mkSessionTemp(t, fn, filepath.Join(polecats, "cccc"), durable)
	flight := mkSessionTemp(t, fn, filepath.Join(polecats, "dddd"), durable)
	// Owner's dir still exists (a kept, dirty tree): the worktree phases own it.
	present := mkSessionTemp(t, fn, filepath.Join(polecats, "eeee"), durable)
	if err := os.MkdirAll(filepath.Join(polecats, "eeee", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A session started in a subdirectory of live polecat cccc spells the
	// same as a polecat named "cccc-sub" — whose ticket IS concluded.
	subdir := mkSessionTemp(t, fn, filepath.Join(polecats, "cccc", "sub"), durable)

	tickets := TicketIndex{
		"mg-aaaa":     TicketDone,
		"mg-bbbb":     TicketDone,
		"mg-cccc":     TicketDone,
		"mg-dddd":     TicketClaimed,
		"mg-eeee":     TicketArchived,
		"mg-cccc-sub": TicketDone,
	}
	var logged []string
	res, err := Sweep(Options{
		Repo:            r.dir,
		Tickets:         tickets,
		PolecatsDir:     polecats,
		LivePolecats:    map[string]bool{"cccc": true},
		SessionTempDirs: fn,
		Logf:            func(f string, a ...any) { logged = append(logged, f) },
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if exists(eligible) {
		t.Errorf("eligible polecat session temp dir %s was not removed", eligible)
	}
	for _, kept := range []string{crew, crewNamed, nonPogo, lookalike, live, flight, present, subdir} {
		if !exists(kept) {
			t.Errorf("session temp dir %s was removed; it must be kept", kept)
		}
	}
	if !exists(durable) {
		t.Fatal("RemoveAll followed a symlink into the durable store")
	}
	if len(res.SessionTempRemoved) != 1 || res.SessionTempRemoved[0].Path != eligible || res.SessionTempRemoved[0].Owner != "aaaa" {
		t.Errorf("SessionTempRemoved = %+v, want exactly %s owned by aaaa", res.SessionTempRemoved, eligible)
	}
	// Crew and non-pogo slugs are not candidates at all, so they appear in
	// neither list — only the four polecat-shaped keeps do.
	keptReasons := map[string]string{}
	for _, k := range res.SessionTempKept {
		keptReasons[k.Path] = k.Reason
	}
	for path, want := range map[string]string{
		live:    "live polecat cccc",
		flight:  "owner's ticket",
		present: "owner's directory still exists",
		subdir:  "live polecat cccc",
	} {
		if !strings.Contains(keptReasons[path], want) {
			t.Errorf("kept reason for %s = %q, want it to contain %q", path, keptReasons[path], want)
		}
	}
	if len(res.SessionTempKept) != 4 {
		t.Errorf("SessionTempKept = %+v, want exactly the 4 polecat-shaped keeps", res.SessionTempKept)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}
	var sawLine bool
	for _, l := range logged {
		if strings.HasPrefix(l, "removed session temp dir") {
			sawLine = true
		}
	}
	if !sawLine {
		t.Errorf("no per-action log line for the removal; logged %q", logged)
	}
	if !strings.Contains(res.Summary(), "session temp dirs: removed 1, kept 4") {
		t.Errorf("summary does not count session temp dirs:\n%s", res.Summary())
	}
}

// TestSessionTempReclaimedWithWorktree: when phase 1 reclaims a registered
// worktree, the owner's session temp dir goes with it under the same verdict;
// a worktree kept for being live keeps its temp dir too. A dry run removes
// neither but reports both.
func TestSessionTempReclaimedWithWorktree(t *testing.T) {
	for _, dry := range []bool{true, false} {
		dry := dry
		t.Run(map[bool]string{true: "dry", false: "apply"}[dry], func(t *testing.T) {
			testSessionTempReclaimedWithWorktree(t, dry)
		})
	}
}

func testSessionTempReclaimedWithWorktree(t *testing.T, dry bool) {
	r := newTestRepo(t)
	r.branch("polecat-aaaa")
	r.branch("polecat-bbbb")
	gone := r.worktree("polecat-aaaa")
	live := r.worktree("polecat-bbbb")
	fn := fakeSessionTemp(filepath.Join(t.TempDir(), "claude-501"))
	target := filepath.Join(t.TempDir(), "t")
	goneTemp := mkSessionTemp(t, fn, gone, target)
	liveTemp := mkSessionTemp(t, fn, live, target)

	res, err := Sweep(Options{
		Repo:            r.dir,
		Tickets:         TicketIndex{"mg-aaaa": TicketArchived, "mg-bbbb": TicketArchived},
		PolecatsDir:     r.polecatsDir(),
		LivePolecats:    map[string]bool{"bbbb": true},
		SessionTempDirs: fn,
		DryRun:          dry,
	})
	if err != nil {
		t.Fatalf("dry=%v Sweep: %v", dry, err)
	}
	if exists(goneTemp) == !dry {
		t.Errorf("dry=%v: reclaimed worktree's temp dir exists=%v", dry, exists(goneTemp))
	}
	if !exists(liveTemp) {
		t.Errorf("dry=%v: live polecat's temp dir was removed", dry)
	}
	if len(res.SessionTempRemoved) != 1 || res.SessionTempRemoved[0].Path != goneTemp {
		t.Errorf("dry=%v: SessionTempRemoved = %+v, want exactly %s", dry, res.SessionTempRemoved, goneTemp)
	}
	assertNotAlsoKept(t, dry, res, goneTemp)
	// Exactly one keep: the live polecat's, for being live.
	if len(res.SessionTempKept) != 1 || res.SessionTempKept[0].Path != liveTemp ||
		!strings.Contains(res.SessionTempKept[0].Reason, "live polecat bbbb") {
		t.Errorf("dry=%v: SessionTempKept = %+v, want only %s kept as live", dry, res.SessionTempKept, liveTemp)
	}
	if len(res.Errors) != 0 {
		t.Errorf("dry=%v: unexpected errors: %v", dry, res.Errors)
	}
}

// TestSessionTempReclaimedWithOrphanDir: phase 1b's orphan-dir removal takes
// the owner's session temp dir too, and a dry run reports it once — not again
// from the orphan-temp phase, which skips owners whose dir still exists.
func TestSessionTempReclaimedWithOrphanDir(t *testing.T) {
	for _, dry := range []bool{true, false} {
		r := newTestRepo(t)
		polecats := t.TempDir()
		orphan := filepath.Join(polecats, "aaaa")
		if err := os.MkdirAll(orphan, 0o755); err != nil {
			t.Fatal(err)
		}
		fn := fakeSessionTemp(filepath.Join(t.TempDir(), "claude-501"))
		temp := mkSessionTemp(t, fn, orphan, filepath.Join(t.TempDir(), "t"))

		res, err := Sweep(Options{
			Repo: r.dir, Tickets: TicketIndex{"mg-aaaa": TicketDone},
			PolecatsDir: polecats, SessionTempDirs: fn, DryRun: dry,
		})
		if err != nil {
			t.Fatalf("dry=%v Sweep: %v", dry, err)
		}
		if exists(temp) == !dry {
			t.Errorf("dry=%v: orphan dir's temp dir exists=%v", dry, exists(temp))
		}
		if len(res.SessionTempRemoved) != 1 {
			t.Errorf("dry=%v: SessionTempRemoved = %+v, want exactly one", dry, res.SessionTempRemoved)
		}
		assertNotAlsoKept(t, dry, res, temp)
		// Anchored to the session-temp line: a bare " 1, kept 0" also
		// matches the worktrees line, which counts the orphan dir itself.
		verb := map[bool]string{true: "would remove", false: "removed"}[dry]
		if !strings.Contains(res.Summary(), "  session temp dirs: "+verb+" 1, kept 0\n") {
			t.Errorf("dry=%v: summary should count the reclaimed temp dir once:\n%s", dry, res.Summary())
		}
	}
}

// assertNotAlsoKept fails when a temp dir reclaimed beside its tree is ALSO
// listed as kept — the dry-run double report review round 1 found, where the
// orphan-temp phase saw the not-yet-deleted owner dir and kept the same path.
func assertNotAlsoKept(t *testing.T, dry bool, res Result, path string) {
	t.Helper()
	for _, k := range res.SessionTempKept {
		if k.Path == path {
			t.Errorf("dry=%v: %s is reported both removed and kept (%s)", dry, path, k.Reason)
		}
	}
}

// TestSessionTempNilProviderIsInert: with no SessionTempDirs the sweep does
// exactly what it did before gh #203.
func TestSessionTempNilProviderIsInert(t *testing.T) {
	r := newTestRepo(t)
	polecats := t.TempDir()
	fn := fakeSessionTemp(filepath.Join(t.TempDir(), "claude-501"))
	temp := mkSessionTemp(t, fn, filepath.Join(polecats, "aaaa"), filepath.Join(t.TempDir(), "t"))
	res, err := Sweep(Options{Repo: r.dir, Tickets: TicketIndex{"mg-aaaa": TicketDone}, PolecatsDir: polecats})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(temp) || len(res.SessionTempRemoved)+len(res.SessionTempKept) != 0 {
		t.Errorf("nil SessionTempDirs touched session temp: exists=%v res=%+v", exists(temp), res)
	}
}

// TestSessionTempDryRunMatchesApplyForSubdirOfReclaimedTree: a session started
// in a subdirectory of a polecat's tree spells like a polecat named
// "<name>-<sub>", and the orphan-temp phase keeps such a slug while
// PolecatsDir/<name> exists. When phase 1 or 1b reclaims <name> in the same
// pass, an apply has deleted that dir before phase 1c looks and a dry run has
// not — so the dry run must count the dir as gone, or it keeps a temp dir the
// apply removes (review of PR #215, mg-fd3e5). The two runs are compared whole.
func TestSessionTempDryRunMatchesApplyForSubdirOfReclaimedTree(t *testing.T) {
	for _, source := range []string{"worktree", "orphan"} {
		t.Run(source, func(t *testing.T) {
			// Each mode in its own subtest: newTestRepo's polecats dir sits
			// beside the repo in the test's temp dir, so two fixtures built
			// under one test would share it.
			var dry, apply sessionTempOutcome
			t.Run("dry", func(t *testing.T) { dry = subdirOfReclaimedTreeSweep(t, source, true) })
			t.Run("apply", func(t *testing.T) { apply = subdirOfReclaimedTreeSweep(t, source, false) })
			if dry.removed != apply.removed || dry.kept != apply.kept {
				t.Errorf("dry run and --apply disagree:\n  dry:   removed %v kept %v\n  apply: removed %v kept %v",
					dry.removed, dry.kept, apply.removed, apply.kept)
			}
			if want := "[aaaa aaaa-cmd]"; apply.removed != want {
				t.Errorf("apply removed owners %s, want %s", apply.removed, want)
			}
			// The summary's session-temp line, as each mode prints it.
			if !strings.Contains(dry.summary, "session temp dirs: would remove 2, kept 0\n") ||
				!strings.Contains(apply.summary, "session temp dirs: removed 2, kept 0\n") {
				t.Errorf("summaries disagree:\n%s\n%s", dry.summary, apply.summary)
			}
		})
	}
}

type sessionTempOutcome struct{ removed, kept, summary string }

// subdirOfReclaimedTreeSweep builds a fresh fixture — polecat aaaa's tree
// (registered worktree or orphan dir, ticket done) with a temp dir for the
// tree and one for its "cmd" subdirectory — sweeps it, and returns the owners
// removed and kept, in a form two runs can be compared by.
func subdirOfReclaimedTreeSweep(t *testing.T, source string, dry bool) sessionTempOutcome {
	t.Helper()
	r := newTestRepo(t)
	polecats := r.polecatsDir()
	tree := filepath.Join(polecats, "aaaa")
	switch source {
	case "worktree":
		r.branch("polecat-aaaa")
		r.worktree("polecat-aaaa")
	case "orphan":
		if err := os.MkdirAll(tree, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fn := fakeSessionTemp(filepath.Join(t.TempDir(), "claude-501"))
	target := filepath.Join(t.TempDir(), "t")
	mkSessionTemp(t, fn, tree, target)
	mkSessionTemp(t, fn, filepath.Join(tree, "cmd"), target)

	res, err := Sweep(Options{
		Repo: r.dir, Tickets: TicketIndex{"mg-aaaa": TicketDone},
		PolecatsDir: polecats, SessionTempDirs: fn, DryRun: dry,
	})
	if err != nil {
		t.Fatalf("dry=%v Sweep: %v", dry, err)
	}
	if len(res.Errors) != 0 {
		t.Errorf("dry=%v: unexpected errors: %v", dry, res.Errors)
	}
	owners := func(as []SessionTempAction) string {
		var o []string
		for _, a := range as {
			o = append(o, a.Owner)
		}
		sort.Strings(o)
		return fmt.Sprint(o)
	}
	return sessionTempOutcome{owners(res.SessionTempRemoved), owners(res.SessionTempKept), res.Summary()}
}
