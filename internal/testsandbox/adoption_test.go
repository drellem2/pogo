package testsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// the ratchet
// ---------------------------------------------------------------------------
//
// mg-78a5 packaged the isolation and mg-0941 + mg-b4a5 converted every suite
// that had already been caught reading live state. That is five suites out of a
// population of fifty-five, and NOTHING failed for the other fifty — this
// package is a TestMain wrapper, so adoption is opt-in, and choosing to adopt is
// itself a remembering. A suite written next month reads the developer's
// ~/.pogo unless its author recalls a helper they may never have seen.
//
// The defect class has four measured instances — mg-6092, mg-e8e7, mg-5336 and
// mg-3412 — by four authors, none of whom set out to read live state. The
// commit subject of mg-78a5 is "a test cannot reach live state BY DEFAULT,
// INSTEAD OF BY REMEMBERING"; within an adopting suite that is exactly true,
// and the sentence is only true of the repository once something fails when a
// new suite does not adopt.
//
// This file is that something. It walks the tree, and every suite it finds
// either routes through the isolation or is named in the ledger beside it. The
// ledger is a RATCHET, not an allowlist: it may only shrink. Adding a line is a
// visible edit to a file whose header says what the one accepted reason is, and
// removing the last suite from it deletes the escape hatch entirely.
//
// Both directions are enforced, because only one of them is the ratchet:
//
//	unadopted and not in the ledger  ->  fail. The new suite.
//	in the ledger and now adopted    ->  fail. Converting a suite must delete
//	                                     its line, or the list rusts into an
//	                                     allowlist nobody can shorten honestly.
//	in the ledger and now gone       ->  fail. Same reason, for deletions and
//	                                     renames.
//
// TestAdoptionCheckFailsOnASuiteThatSkipsTheHelper below is the positive
// control: it builds a tree containing a Go package and a shell suite that skip
// the isolation and reads back what the check says about them. A check only
// ever observed passing on an already-converted tree has not been tested — that
// is this ticket's own defect, one level up.

// ledgerName is the file, beside this one, naming every suite that does not yet
// route through the isolation.
const ledgerName = "adoption-ledger.txt"

// suiteKind distinguishes the two populations. They are different mechanisms —
// a Go TestMain calling Main, a shell suite sourcing scripts/pogo-sandbox and
// calling pogo_sandbox_isolate — and the same defect, so they are one check.
type suiteKind string

const (
	kindGo    suiteKind = "go package"
	kindShell suiteKind = "shell suite"
)

// suite is one member of the population: a Go package directory containing
// _test.go files, or a *_test.sh script. Paths are repo-relative and
// slash-separated, which is what the ledger holds.
type suite struct {
	Path    string
	Kind    suiteKind
	Adopted bool
}

// goAdopted matches a Go suite routing through this package. The unqualified
// form is for this package's OWN tests, which call Main directly; qualifying it
// would be the only difference between the helper proving itself isolated and
// exempting itself.
var (
	goAdoptedQualified = regexp.MustCompile(`\btestsandbox\.Main\(`)
	goAdoptedLocal     = regexp.MustCompile(`\bMain\(`)
	goPackageLine      = regexp.MustCompile(`(?m)^package\s+testsandbox\b`)

	// shellAdopted matches a shell suite routing through the shell counterpart.
	// Naming the function rather than the file is deliberate: sourcing
	// scripts/pogo-sandbox and never calling the isolation is the shape that
	// reads as adopted from a grep and is not.
	shellAdopted = regexp.MustCompile(`\bpogo_sandbox_isolate\b`)
)

// skipDir names directories that hold no suite of ours: version control, vendored
// or generated trees, and the fixture repos test.sh stages under _testdata.
func skipDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", "testdata", "_testdata":
		return true
	}
	return false
}

// survey walks root and returns every suite in it, sorted, with adoption read
// from the file contents rather than from a list somebody maintains.
func survey(root string) ([]suite, error) {
	var out []suite

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel != "." && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		switch {
		case strings.HasSuffix(d.Name(), "_test.sh"):
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			out = append(out, suite{
				Path:    filepath.ToSlash(rel),
				Kind:    kindShell,
				Adopted: shellAdopted.Match(body),
			})
		case strings.HasSuffix(d.Name(), "_test.go"):
			// A Go suite is a PACKAGE, not a file: the isolation is established
			// once per test binary by TestMain, so a package with twenty test
			// files adopts once. Counting files would report this repository as
			// 23-of-N adopted when the number that matters is 3.
			dir := filepath.ToSlash(filepath.Dir(rel))
			for i := range out {
				if out[i].Path == dir && out[i].Kind == kindGo {
					return nil // already recorded by an earlier file in this package
				}
			}
			adopted, readErr := goPackageAdopts(filepath.Dir(path))
			if readErr != nil {
				return readErr
			}
			out = append(out, suite{Path: dir, Kind: kindGo, Adopted: adopted})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// goPackageAdopts reports whether any test file in dir hands its TestMain to
// this package.
func goPackageAdopts(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		if goAdoptedQualified.Match(body) {
			return true, nil
		}
		if goPackageLine.Match(body) && goAdoptedLocal.Match(body) {
			return true, nil
		}
	}
	return false, nil
}

// readLedger returns the paths named in the ledger, in file order, plus the
// line number each was found on so a complaint can point at it.
func readLedger(path string) (map[string]int, []string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	lines := map[string]int{}
	var order []string
	for i, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if prev, dup := lines[line]; dup {
			return nil, nil, fmt.Errorf("%s:%d names %s, which line %d already named",
				ledgerName, i+1, line, prev)
		}
		lines[line] = i + 1
		order = append(order, line)
	}
	return lines, order, nil
}

// violations is the whole check, as a function of a surveyed population and a
// ledger, so the positive control can run it against a tree built to fail.
func violations(suites []suite, ledger map[string]int) []string {
	var out []string

	present := map[string]suite{}
	for _, s := range suites {
		present[s.Path] = s
		if s.Adopted {
			continue
		}
		if _, listed := ledger[s.Path]; listed {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s %s does not route through the test isolation.\n"+
				"    Its tests read and write the developer's live ~/.pogo, which is "+
				"mg-6092 / mg-e8e7 / mg-5336 / mg-3412 verbatim.\n"+
				"    Fix it: %s", s.Kind, s.Path, howToAdopt(s.Kind)))
	}

	for path, line := range ledger {
		s, found := present[path]
		switch {
		case !found:
			out = append(out, fmt.Sprintf(
				"%s:%d names %s, which no longer exists.\n"+
					"    Delete the line. A ledger that keeps names for suites that are "+
					"gone cannot be read as a backlog.", ledgerName, line, path))
		case s.Adopted:
			out = append(out, fmt.Sprintf(
				"%s:%d names %s, which now DOES route through the isolation.\n"+
					"    Delete the line. The ledger only shrinks; leaving converted "+
					"suites in it turns a backlog into an allowlist.", ledgerName, line, path))
		}
	}

	sort.Strings(out)
	return out
}

func howToAdopt(k suiteKind) string {
	if k == kindShell {
		return `source "$HERE/pogo-sandbox" and call pogo_sandbox_isolate before the ` +
			`first command that can reach $HOME — see scripts/pogo-self-deploy_sigint_test.sh.`
	}
	return "add a TestMain calling testsandbox.Main(\"<pkg>\"), and one test calling " +
		"testsandbox.Verify — see internal/driver/testmain_test.go."
}

// repoRoot walks up from the working directory to the module root. The test
// binary runs in its own package directory, and hard-coding "../.." would be a
// fact about this file's depth that nothing checks.
func repoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatalf("could not read the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatalf("no go.mod above %s — cannot find the repository root to walk", dir)
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// the check itself
// ---------------------------------------------------------------------------

// TestEveryTestSuiteRoutesThroughTheIsolation is the ratchet. It fails locally,
// before the merge queue, where the author of the new suite can still act on
// it — which is why it is a test and not a CI step.
func TestEveryTestSuiteRoutesThroughTheIsolation(t *testing.T) {
	root := repoRoot(t)

	suites, err := survey(root)
	if err != nil {
		t.Fatalf("could not walk %s: %v", root, err)
	}
	if len(suites) == 0 {
		// A walk that finds nothing passes every assertion below while checking
		// nothing at all — the shape this ticket is about.
		t.Fatalf("found no test suites under %s; the walk is broken, not the tree", root)
	}

	ledgerPath := filepath.Join(root, "internal", "testsandbox", ledgerName)
	ledger, _, err := readLedger(ledgerPath)
	if err != nil {
		t.Fatalf("could not read the adoption ledger: %v", err)
	}

	// The population, named. "23 files adopted" was the number that made this
	// look nearly done; the unit of adoption is the test binary.
	var goTotal, goAdopted, shTotal, shAdopted int
	for _, s := range suites {
		switch s.Kind {
		case kindGo:
			goTotal++
			if s.Adopted {
				goAdopted++
			}
		case kindShell:
			shTotal++
			if s.Adopted {
				shAdopted++
			}
		}
	}
	t.Logf("test isolation adoption: %d/%d Go packages containing _test.go files, "+
		"%d/%d shell *_test.sh suites, %d/%d overall; %d named in %s as not yet converted",
		goAdopted, goTotal, shAdopted, shTotal, goAdopted+shAdopted, goTotal+shTotal,
		len(ledger), ledgerName)

	if bad := violations(suites, ledger); len(bad) > 0 {
		t.Errorf("\n%s\n%d suite(s) disagree with %s:\n\n%s\n\n%s",
			strings.Repeat("=", 72), len(bad), ledgerName,
			strings.Join(bad, "\n\n"), ledgerAdvice)
	}
}

const ledgerAdvice = "The ledger is a ratchet: it may only shrink. If you are here because you " +
	"wrote a\nnew suite, adopt the isolation — do not add a line. No suite in this repository\n" +
	"has been found to need the developer's live state, and \"this one needs it\" is not\n" +
	"an accepted entry; see the header of " + ledgerName + "."

// ---------------------------------------------------------------------------
// the positive control
// ---------------------------------------------------------------------------

// TestAdoptionCheckFailsOnASuiteThatSkipsTheHelper is the requirement this
// ticket exists to satisfy at one level up. The check above runs against a tree
// where the only unadopted suites are already in the ledger, so on this
// repository it is only ever observed PASSING — and a check observed only
// passing is exactly the "23 adopted, 0 enforcement" measurement that produced
// this ticket.
//
// So: build a tree containing one Go package and one shell suite that skip the
// isolation, one of each that does not, and read back what the check says.
func TestAdoptionCheckFailsOnASuiteThatSkipsTheHelper(t *testing.T) {
	root := t.TempDir()

	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("go.mod", "module example.com/fixture\n\ngo 1.25.0\n")

	write("internal/skipped/skipped_test.go", `package skipped

import "testing"

func TestSomething(t *testing.T) {}
`)
	write("internal/adopted/testmain_test.go", `package adopted

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

func TestMain(m *testing.M) {
	_, down := testsandbox.Main("adopted")
	code := m.Run()
	down()
	os.Exit(code)
}
`)
	write("scripts/skipped_test.sh", "#!/usr/bin/env bash\nset -euo pipefail\necho hi\n")
	write("scripts/adopted_test.sh", "#!/usr/bin/env bash\nsource \"$HERE/pogo-sandbox\"\npogo_sandbox_isolate\n")

	// Directories the walk must not descend into, each holding a suite that
	// would be a violation if it were counted.
	write("vendor/other/other_test.go", "package other\n")
	write("internal/project/_testdata/fixture/fixture_test.go", "package fixture\n")

	suites, err := survey(root)
	if err != nil {
		t.Fatalf("survey: %v", err)
	}

	got := map[string]bool{}
	for _, s := range suites {
		got[string(s.Kind)+" "+s.Path] = s.Adopted
	}
	want := map[string]bool{
		"go package internal/adopted":         true,
		"go package internal/skipped":         false,
		"shell suite scripts/adopted_test.sh": true,
		"shell suite scripts/skipped_test.sh": false,
	}
	if len(got) != len(want) {
		t.Errorf("survey found %d suites, want %d: %v", len(got), len(want), got)
	}
	for k, wantAdopted := range want {
		gotAdopted, found := got[k]
		if !found {
			t.Errorf("survey did not find %s", k)
			continue
		}
		if gotAdopted != wantAdopted {
			t.Errorf("survey reports %s adopted=%v, want %v", k, gotAdopted, wantAdopted)
		}
	}

	// THE FAILING DIRECTION. With an empty ledger, both skippers must be named.
	bad := violations(suites, map[string]int{})
	if len(bad) != 2 {
		t.Fatalf("check reported %d violations against a tree with two unadopted suites, "+
			"want 2:\n%s", len(bad), strings.Join(bad, "\n"))
	}
	joined := strings.Join(bad, "\n")
	for _, must := range []string{"internal/skipped", "scripts/skipped_test.sh"} {
		if !strings.Contains(joined, must) {
			t.Errorf("the violation report does not name %s:\n%s", must, joined)
		}
	}
	for _, mustNot := range []string{"internal/adopted", "scripts/adopted_test.sh", "vendor/", "_testdata"} {
		if strings.Contains(joined, mustNot) {
			t.Errorf("the violation report names %s, which is adopted or out of population:\n%s",
				mustNot, joined)
		}
	}

	// And the passing direction, so the check is not merely a function that
	// always complains: ledgered skippers are quiet.
	if bad := violations(suites, map[string]int{
		"internal/skipped":        3,
		"scripts/skipped_test.sh": 4,
	}); len(bad) != 0 {
		t.Errorf("ledgered suites still reported as violations:\n%s", strings.Join(bad, "\n"))
	}
}

// TestLedgerRustIsAViolation is the other half of the ratchet, and the half
// that is easy to leave untested because nothing breaks the day it stops
// working: a ledger that keeps names for suites that have since adopted, or
// that no longer exist, is an allowlist that only grows.
func TestLedgerRustIsAViolation(t *testing.T) {
	suites := []suite{
		{Path: "internal/converted", Kind: kindGo, Adopted: true},
		{Path: "internal/still", Kind: kindGo, Adopted: false},
	}
	ledger := map[string]int{
		"internal/converted": 7,
		"internal/still":     8,
		"internal/deleted":   9,
	}

	bad := violations(suites, ledger)
	if len(bad) != 2 {
		t.Fatalf("got %d violations, want 2 (one converted, one deleted):\n%s",
			len(bad), strings.Join(bad, "\n"))
	}
	joined := strings.Join(bad, "\n")
	if !strings.Contains(joined, "internal/converted") || !strings.Contains(joined, "no longer exists") {
		t.Errorf("the report is missing the converted or the deleted case:\n%s", joined)
	}
	if strings.Contains(joined, "internal/still") {
		t.Errorf("a still-unadopted ledgered suite was reported:\n%s", joined)
	}
}

// TestLedgerRejectsDuplicateLines keeps the ledger readable as a count. A path
// named twice would make "how many suites are left" a different number from
// "how many lines are in the file", and the count is the only thing anybody
// reads it for.
func TestLedgerRejectsDuplicateLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ledgerName)
	if err := os.WriteFile(path, []byte("# c\ninternal/a\ninternal/b\ninternal/a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLedger(path); err == nil {
		t.Fatal("readLedger accepted a duplicate path")
	}
}

// TestLedgerIsSortedAndNotEmptyOfExplanation is a small thing that keeps the
// file a document rather than a dumping ground: the header has to survive, and
// the paths have to be in an order a reviewer can diff.
func TestLedgerIsSortedAndNotEmptyOfExplanation(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "testsandbox", ledgerName)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read the ledger: %v", err)
	}
	if !strings.Contains(string(body), "mg-457b") {
		t.Error("the ledger has lost its header; without it the file reads as an " +
			"allowlist rather than a backlog with one accepted reason")
	}

	_, order, err := readLedger(path)
	if err != nil {
		t.Fatalf("could not parse the ledger: %v", err)
	}
	if !sort.StringsAreSorted(order) {
		t.Errorf("the ledger's paths are not sorted; sort them so a removal is a "+
			"one-line diff:\n%s", strings.Join(order, "\n"))
	}
}
