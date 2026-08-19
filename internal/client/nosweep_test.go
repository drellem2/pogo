package client

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// `mg archive --days=0` is estate-wide and gate-blind, and until mg-eadd this
// package shipped a helper that wrapped it — `ArchiveMGDoneItems`, with a doc
// comment asserting the refinery called it after every merge. The call site had
// been gone since 2026-03-26 (mg-1f67, deliberate: completions stay visible to
// the coordinator long enough to act on), so the helper had zero callers and
// the comment had been false for 146 days. That comment is where mayor.md's
// retracted "the refinery archives the work item automatically" came from
// (mg-c2e1) — a dead function's comment reached a shipped prompt.
//
// Deleting the function is not by itself a guard: the next reader to want
// post-merge archiving writes the same three-argument exec call back, and it
// looks like tidying up an obvious gap. What makes it a defect rather than a
// gap is what the sweep takes. Measured 2026-08-19 with `mg archive --days=0
// --dry-run` against the live store: **4 items would have been archived and not
// one of them belonged to the coordinator** — two architect's in `~/.claude`,
// one a PM's in `~/research/onethird_program`, and one a request the human had
// filed that same morning. It cannot see a gate either: carrier state is
// pogod's parse of the item body, not a field mg carries (`mg show --json` has
// no `workflow`/`stage` key), so no callsite can filter on it, and the sweep
// has eaten live `gh-issue` gate carriers twice.
//
// mg-c2e1 removed the prescription from mayor.md, where a human-shaped reader
// is at least in the path. This guard is the same rule for code, where nothing
// is.
//
// Post-merge archiving is still a legitimate want. The form that is safe is the
// one mayor.md now prescribes: archive the ONE id that merged, gated on its
// worker having exited — `mg archive <id>`, which this guard does not match.
func TestNoProductionCodeRunsTheEstateWideArchiveSweep(t *testing.T) {
	offenders, err := scanForSweepCallers(repoRootForSweepGuard(t))
	if err != nil {
		t.Fatalf("walking the repo: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("production code invokes the estate-wide archive sweep:\n  %s\n\n"+
			"`mg archive --days=0` archives every agent's done work, not the caller's. A live "+
			"--dry-run on 2026-08-19 would have taken 4 items, 0 of them the coordinator's, and it "+
			"cannot see a gh-issue gate at all because carrier state is not a field mg carries. "+
			"If you want post-merge archiving, archive the ONE id you merged once its worker has "+
			"exited: `mg archive <id>` (mg-eadd, mg-c2e1).",
			strings.Join(offenders, "\n  "))
	}
}

// TestTheSweepGuardCanFail is the guard's positive control. A source scan that
// silently stopped matching — a renamed helper, a walk that skipped a
// directory, a regexp that no longer fits how the args are written — goes on
// passing forever and reads exactly like compliance. This arm plants the
// helpful-looking reintroduction and requires the guard to catch it.
func TestTheSweepGuardCanFail(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "internal", "client")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// The reintroduction, written the way it would actually be written: a small
	// helper restoring a capability the comment says the refinery already has.
	if err := os.WriteFile(filepath.Join(pkg, "agent.go"), []byte(
		"package client\n\n"+
			"// ArchiveMGDoneItems archives merged work at its lifecycle endpoint.\n"+
			"func ArchiveMGDoneItems() error {\n"+
			"\treturn execCommand(\"mg\", \"archive\", \"--days=0\").Run()\n"+
			"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	offenders, err := scanForSweepCallers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 1 || !strings.Contains(offenders[0], filepath.Join("internal", "client", "agent.go")) {
		t.Fatalf("THE GUARD DID NOT FIRE on a planted sweep caller. Until this arm fires, a passing "+
			"guard means nothing. offenders=%v", offenders)
	}
}

// sweepArgv matches the sweep as an EXEC ARGUMENT LIST — the quoted `archive`
// subcommand followed by the quoted `--days=0` flag, which is how every Go
// invocation of an external command in this repo is written
// (`execCommand("mg", "archive", "--days=0")`, `exec.Command(...)`, or a
// `[]string{...}` built up and passed along).
//
// Matching the argv form rather than the bare text `mg archive --days=0` is
// deliberate and is the difference between failing on the defect and failing on
// the fix. Non-test Go files in this repo NAME the sweep in order to warn about
// it — `cmd/pogo/checkprompts.go` embeds it in help text listing the destructive
// commands its corpus contains and never runs, and `internal/promptcli/surface.go`
// explains in comments why the surface checker must not execute what it
// extracts. A scan for the prose form would flag both, and the natural way to
// quiet it is to stop writing the warning down.
//
// Known limit, stated rather than papered over: this catches the sweep spelled
// out in source. It would not catch one assembled at runtime (`"--days="+n`) or
// shelled out through a string built by fmt.Sprintf. Those are not how this
// repo runs external commands, and a guard that tried to cover them would match
// the prose too.
var sweepArgv = regexp.MustCompile(`"archive"\s*,\s*"--days=0"`)

// scanForSweepCallers reports non-test Go files under root that invoke the
// estate-wide archive sweep.
//
// _test.go files are exempt on purpose. `cmd/pogod/filernotify_live_test.go`
// runs the sweep legitimately: it passes `--root <t.TempDir()>`, so it operates
// on a private store it created, and it is establishing against a REAL mg that
// a result sidecar survives being archived. The danger this guard exists for is
// a sweep pointed at the live estate, which a `--root`-scoped fixture is not.
// The cost of the exemption is that a test could reintroduce an unscoped sweep;
// that is a smaller surface than the false failures a blanket rule would cause.
func scanForSweepCallers(root string) ([]string, error) {
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "_testdata", "bin", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !sweepArgv.Match(data) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		offenders = append(offenders, rel)
		return nil
	})
	return offenders, err
}

// repoRootForSweepGuard locates the module root from this test's own source
// path. It skips rather than fails when the source tree is not readable: this
// guard reads the repo, and a checkout that does not have one has nothing to
// say about it.
func repoRootForSweepGuard(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		t.Skipf("repo root not readable from %s (%v); this guard needs the source tree", abs, err)
	}
	return abs
}
