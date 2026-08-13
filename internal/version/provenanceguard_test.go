package version

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Build-time guard for mg-8d0f, adapted from macguffin's mg-b7fe guard. It is
// an ENUMERATION, not a prohibition, and the difference is the whole content of
// this file.
//
// THE TRAP, WHICH IS THE SAME IN BOTH REPOS. Go >= 1.18 embeds
// vcs.revision / vcs.time / vcs.modified into every binary with no ldflags at
// all, readable via runtime/debug.ReadBuildInfo, debug/buildinfo.ReadFile, or
// `go version -m`. When the build directory is a LINKED GIT WORKTREE (.git is a
// file) nested inside another git repository, the toolchain stamps the
// ENCLOSING repository's HEAD. It does not warn and it does not fail — it emits
// a confident, well-formed, wrong answer, and a wrong revision is the same
// token as a right one to every reader that does not check.
//
// MEASURED HERE, ON pogod ITSELF, from this worktree (mg-8d0f closing the
// inference mg-b7fe left open — it reasoned from the mechanism plus a macguffin
// reproduction rather than building a pogo binary):
//
//	worktree HEAD 359ff1a1, tree CLEAN
//	go build ./cmd/pogod  ->  vcs.revision=d6d179f2  vcs.modified=true
//	git cat-file -e d6d179f2 -> "Not a valid object name"
//
// d6d179f2 is the HEAD of ~/.pogo, the enclosing repo, whose tree had 13 dirty
// files; the pogo worktree had none. The built `pogo version` then reported
// `pogo 0.10.0 (d6d179f-dirty, branch=unknown, source=buildinfo)` — every field
// wrong except the one that says where it came from.
//
// WHY pogo CANNOT COPY macguffin's RULE. mg consumes none of this, so mg-b7fe
// could ban it outright. pogo consumes it on purpose, in four places, and the
// verbatim macguffin guard run against this tree fails on all four:
//
//	cmd/pogod/main.go:604            calls debug.ReadBuildInfo
//	internal/driftwatch/revision.go  calls debug.ReadBuildInfo
//	internal/selfdrift/hostdeps.go   imports debug/buildinfo
//	internal/version/resolve.go:116  calls debug.ReadBuildInfo
//
// Each is deliberate and documented at its site. resolve.go reads the stamp so
// a binary built by a path nobody remembered to patch still says something true,
// and reports it as source=buildinfo precisely because it can be confidently
// wrong (mg-3141). selfdrift reads a stamp OUT OF A BINARY ON DISK in order to
// classify it — its FOREIGN STAMP verdict is what turns d6d179f2 into a
// refusal, so a prohibition would ban the detector along with the defect.
//
// SO THE INVARIANT IS NOT "never read it". It is: the set of readers is a
// closed, named list, and a new one is a build failure. That is still a
// build-time prohibition — it prohibits the thing actually at risk, which is a
// fifth reader appearing that reports the stamp as though it were the truth —
// and it does not require pretending the four are wrong.
//
// THIS DOES NOT REPLACE THE FOREIGN STAMP RUNTIME GATE, and the two are not
// two implementations of one check. A build-time guard reads SOURCE; the defect
// lives in an ARTIFACT. Measured for mg-8d0f: the same Go sources — every .go
// file byte-identical, verified by diff — built once in this nested worktree
// and once in a standalone repo produced
//
//	nested worktree:  pogo 0.10.0 (d6d179f-dirty, branch=unknown, source=buildinfo)
//	standalone repo:  pogo 0.10.0 (c378c17,       branch=unknown, source=buildinfo)
//
// One stamp is foreign and one is correct, from identical source. No source
// scan can tell those apart, because nothing in the source differs — the build
// LOCATION is the variable. Note too that both say source=buildinfo: that field
// flags where the revision came from, not whether it is true. Turning d6d179f
// into a refusal takes asking git whether the SHA is in this repo, which is
// what selfdrift's FOREIGN STAMP verdict does (selfdrift.go, mg-49bc) and what
// this guard cannot do. The gate also covers every binary already installed on
// a box, which no guard on today's source reaches at all.
//
// So: this guard bounds who may read the stamp; the gate classifies stamps that
// already exist. Removing either leaves a hole the other never covered.
//
// If you are here because this test failed, do not look for a cleverer way to
// call ReadBuildInfo. Ask git: `git -C <dir> rev-parse HEAD` answers for the
// worktree you are standing in, which is the question you meant. If you truly
// need the toolchain stamp, you are adding a fifth reader: say so by adding it
// below, and carry its provenance the way internal/version does.

// The Go entry points to the toolchain's own VCS stamp. debug/buildinfo reads it
// out of a binary on disk; runtime/debug reads it out of the running process.
// Both report the enclosing repo for a worktree build.
const (
	pkgBuildinfo  = "debug/buildinfo"
	pkgRuntimeDbg = "runtime/debug"
	stampFunc     = "ReadBuildInfo"
)

// The shell doors to the same misattribution. `go version -m <binary>` prints
// exactly the settings ReadBuildInfo returns, so a script that scrapes
// vcs.revision out of it is wrong in the same way for the same reason, and the
// Go scan above cannot see it.
var shellStampTokens = []string{"go version -m", "vcs.revision"}

// goStampReaders is the closed list of Go sites permitted to read the stamp,
// keyed by repo-relative path and enclosing function. Function granularity, not
// file: cmd/pogod/main.go is licensed for versionHandler and for nothing else,
// so a second reader added to an already-listed file is still caught.
//
// A file listed here for its IMPORT (debug/buildinfo) uses the empty function
// name, because the import is the thing being licensed.
var goStampReaders = map[string]string{
	filepath.Join("cmd", "pogod", "main.go") + "#versionHandler":
	// GET /version reports the raw stamp beside the ldflags stamp, in
	// separate fields, so a reader can tell the near end from the far end.
	"pogod's GET /version reports the raw stamp as its own field",

	filepath.Join("internal", "version", "resolve.go") + "#buildInfoStamp":
	// Second-class by construction: resolve() marks it source=buildinfo and
	// the ldflags stamp always wins.
	"the fallback stamp, reported as source=buildinfo (mg-3141)",

	filepath.Join("internal", "driftwatch", "revision.go") + "#BuildRevision":
	// In-process on purpose: a loopback /version probe can be answered by a
	// process that is not this daemon; our own binary cannot lie about itself.
	"the staleness detector's in-process self-report (mg-5bd2)",

	filepath.Join("internal", "selfdrift", "hostdeps.go") + "#":
	// Reads a stamp out of a binary ON DISK, to be classified. This is the
	// FOREIGN STAMP detector's input.
	"reads an on-disk binary's stamp so selfdrift can classify it (mg-49bc)",
}

// shellStampScrapers is the same closed list for shell scripts, keyed by
// repo-relative path with the number of code lines expected to match. A count
// rather than a bare filename so a NEW scrape inside an already-listed script
// is caught; reformatting that changes the count is meant to force a look.
var shellStampScrapers = map[string]int{
	// 727 is the real scrape (installed_rev, reading a binary on disk); 868
	// is an ACTION string telling a human which command to run.
	filepath.Join("scripts", "pogo-self-deploy"): 3,
}

func TestToolchainStampReadersAreEnumerated(t *testing.T) {
	root := guardRepoRoot(t)
	found, err := scanGoStampReaders(root)
	if err != nil {
		t.Fatalf("scanning Go sources: %v", err)
	}
	unlisted, missing := diffKeys(found, goStampReaders)

	if len(unlisted) > 0 {
		t.Errorf("Go source reads the toolchain's VCS stamp at a site that is not on the list "+
			"(mg-8d0f):\n  %s\n\nBuilt from a linked worktree that stamp names the ENCLOSING repo's "+
			"HEAD, silently — measured on this tree as d6d179f2, which is not an object in pogo at "+
			"all. Derive the revision from git instead: git -C <dir> rev-parse HEAD. If you really "+
			"need the toolchain stamp, add the site to goStampReaders and report its provenance the "+
			"way internal/version does.", strings.Join(unlisted, "\n  "))
	}
	if len(missing) > 0 {
		t.Errorf("goStampReaders lists sites that no longer read the stamp:\n  %s\n\nA stale entry is "+
			"a standing licence: the next reader added there passes unexamined. Delete them.",
			strings.Join(missing, "\n  "))
	}
}

func TestToolchainStampScrapersInShellAreEnumerated(t *testing.T) {
	root := guardRepoRoot(t)
	found, err := scanShellStampScrapers(root)
	if err != nil {
		t.Fatalf("scanning shell scripts: %v", err)
	}

	for rel, lines := range found {
		want, listed := shellStampScrapers[rel]
		if !listed {
			t.Errorf("shell script scrapes the toolchain's VCS stamp and is not on the list "+
				"(mg-8d0f):\n  %s\n\n`go version -m` prints exactly what ReadBuildInfo returns, so "+
				"this is the same misattribution by the other door. Ask git instead: "+
				"git -C <dir> rev-parse HEAD.", strings.Join(lines, "\n  "))
			continue
		}
		if len(lines) != want {
			t.Errorf("%s: expected %d stamp-scraping code lines, found %d:\n  %s\n\nIf a scrape was "+
				"added, it needs the same scrutiny as the listed one. If one was removed, lower the "+
				"count in shellStampScrapers.", rel, want, len(lines), strings.Join(lines, "\n  "))
		}
	}
	for rel := range shellStampScrapers {
		if _, ok := found[rel]; !ok {
			t.Errorf("shellStampScrapers lists %s, which no longer scrapes the stamp — or which the "+
				"walk stopped reaching. Both are worth knowing; the second is how a guard goes quiet.", rel)
		}
	}
}

// TestTheProvenanceGuardCanFail is the guard's own positive control, kept in
// the suite rather than performed once by hand.
//
// mg-b7fe established the discipline by injecting an aliased call, a blank
// import and a shell scrape, watching each fail with file:line, then removing
// them. Those injections were real and they are why this guard is trusted — but
// a manual injection proves the guard worked on the afternoon someone ran it.
// What goes wrong later is quieter: a renamed helper, a walk that stops
// descending, an extension filter that no longer matches. A guard that has
// stopped matching anything passes forever and reads exactly like compliance,
// which is the same silent-success shape the stamp trap itself has. So the
// scanners take a root parameter and are pointed at a fixture here.
//
// Every case below was ALSO injected into the live tree during mg-8d0f and
// observed failing with file:line before being removed.
func TestTheProvenanceGuardCanFail(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// An ALIAS. `dbg.ReadBuildInfo` contains the string "ReadBuildInfo" but not
	// "debug.ReadBuildInfo", so a name grep passes it — verified against the
	// live tree, where `grep -rn 'debug\.ReadBuildInfo'` found nothing.
	write("aliased/reader.go", "package aliased\n\nimport dbg \"runtime/debug\"\n\n"+
		"func Read() bool { _, ok := dbg.ReadBuildInfo(); return ok }\n")
	// The import IS the violation for debug/buildinfo, blank or not.
	write("imported/blank.go", "package imported\n\nimport _ \"debug/buildinfo\"\n")
	// A dot-import makes call sites unattributable, so it is a site itself.
	write("dotted/dot.go", "package dotted\n\nimport . \"runtime/debug\"\n\n"+
		"func Read() bool { _, ok := ReadBuildInfo(); return ok }\n")
	// DOCUMENTING the trap must not count as falling into it. Parsing, not
	// grepping, is what buys this on the Go side.
	write("documented/warning.go", "package documented\n\n"+
		"// Never call debug.ReadBuildInfo; debug/buildinfo is banned too.\n"+
		"const Warning = \"debug.ReadBuildInfo and debug/buildinfo are enumerated, not free\"\n")

	found, err := scanGoStampReaders(root)
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}
	for _, want := range []string{
		filepath.Join("aliased", "reader.go") + "#Read",
		filepath.Join("imported", "blank.go") + "#",
		filepath.Join("dotted", "dot.go") + "#",
	} {
		what, ok := found[want]
		if !ok {
			t.Errorf("the Go scan missed %s — it can no longer catch the thing it exists for", want)
			continue
		}
		// file:line, because a finding you have to go hunting for is the
		// unattributable-report defect one layer up.
		if !strings.Contains(what, ":") {
			t.Errorf("%s reported without file:line: %q", want, what)
		}
	}
	if what, ok := found[filepath.Join("documented", "warning.go")+"#"]; ok {
		t.Errorf("a file that only DOCUMENTS the trap was reported as falling into it: %q\n"+
			"That teaches people to delete the warning, which is worse than the warning's absence.", what)
	}

	// The shell half. The scraper is EXTENSIONLESS on purpose: pogo's only real
	// one (scripts/pogo-self-deploy) is, and macguffin's .sh-keyed walk run
	// against this tree reported PASS while that scrape sat at line 727.
	write("scripts/deployish", "#!/bin/bash\nrev=$(go version -m \"$1\" | grep vcs.revision)\n")
	write("scripts/warned.sh", "#!/bin/sh\n# never scrape `go version -m` for vcs.revision\necho ok\n")

	shell, err := scanShellStampScrapers(root)
	if err != nil {
		t.Fatalf("scanning the shell fixture: %v", err)
	}
	if lines := shell[filepath.Join("scripts", "deployish")]; len(lines) == 0 {
		t.Error("the shell scan missed an extensionless scraper — the exact blind spot that made " +
			"the ported guard report PASS on this repo")
	}
	if lines, ok := shell[filepath.Join("scripts", "warned.sh")]; ok {
		t.Errorf("a shell COMMENT warning about the trap was counted as a scrape: %v\n"+
			"Comment stripping is what stops the guard training people to delete the warning.", lines)
	}
}

// scanGoStampReaders returns every stamp-reading site as "relpath#funcname",
// mapped to a human description of what was found there.
//
// It is an AST scan and not a grep for two reasons, both of which a name-only
// scan gets wrong. An ALIASED import (`dbg "runtime/debug"`, then
// `dbg.ReadBuildInfo()`) never contains the string "debug.ReadBuildInfo"; and
// the strings "ReadBuildInfo" and "debug/buildinfo" appear all over this very
// file, which documents the trap. Parsing means describing the trap cannot
// count as falling into it — the Go half of qb7fe's comment-stripping.
func scanGoStampReaders(root string) (map[string]string, error) {
	found := map[string]string{}
	err := walkGuardFiles(root, func(rel, path string) error {
		if filepath.Ext(path) != ".go" {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}

		// Local names bound to runtime/debug in this file. Usually "debug",
		// but an alias binds something else and would slip a name-only scan.
		runtimeDebugNames := map[string]bool{}
		for _, imp := range f.Imports {
			// Every finding carries file:line, imports included. An import has
			// no call site, but "imports debug/buildinfo" with no path attached
			// is a failure you have to go looking for — the same
			// unattributable-report defect this guard exists to prevent, one
			// layer up.
			at := rel + ":" + strconv.Itoa(fset.Position(imp.Pos()).Line) + ": "
			switch strings.Trim(imp.Path.Value, `"`) {
			case pkgBuildinfo:
				// No innocent use: the package exists only to read the stamp,
				// so the import itself is the site.
				found[rel+"#"] = at + "imports " + pkgBuildinfo
			case pkgRuntimeDbg:
				// runtime/debug is mostly innocent (debug.Stack, SetGCPercent),
				// so the import is not a site; the ReadBuildInfo call is.
				name := "debug"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				if name == "." {
					// A dot-import makes the call site unattributable, so it
					// is reported as a site in its own right rather than
					// silently dropped.
					found[rel+"#"] = at + "dot-imports " + pkgRuntimeDbg +
						" — import it by name so " + stampFunc + " calls stay visible"
					continue
				}
				if name != "_" {
					runtimeDebugNames[name] = true
				}
			}
		}
		if len(runtimeDebugNames) == 0 {
			return nil
		}

		// Walk each function separately so a call can be attributed to the
		// function that contains it.
		record := func(fn string, sel *ast.SelectorExpr, id *ast.Ident) {
			line := strconv.Itoa(fset.Position(sel.Pos()).Line)
			key := rel + "#" + fn
			found[key] = rel + ":" + line + ": calls " + id.Name + "." + stampFunc
		}
		ast.Inspect(f, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(decl, func(m ast.Node) bool {
				sel, ok := m.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != stampFunc {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || !runtimeDebugNames[id.Name] {
					return true
				}
				record(decl.Name.Name, sel, id)
				return true
			})
			return false
		})
		// Calls outside any function (a package-level var initialiser) are
		// attributed to "" rather than dropped.
		ast.Inspect(f, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncDecl); ok {
				return false
			}
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != stampFunc {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || !runtimeDebugNames[id.Name] {
				return true
			}
			record("", sel, id)
			return true
		})
		return nil
	})
	return found, err
}

// scanShellStampScrapers returns the matching code lines per script.
//
// Comment lines are stripped first, and that is not a nicety: the trap is
// documented in build.sh's version derivation and in three places inside
// pogo-self-deploy. Without stripping, a comment WARNING about the stamp trips
// the guard, which teaches people to delete the warning (qb7fe's detail).
//
// Its limit, stated rather than discovered later: only whole-line comments are
// stripped, because a trailing `#` cannot be told from a `#` inside a quoted
// string without parsing the shell. A trailing comment mentioning vcs.revision
// therefore trips this guard. That is the right way round — it fails loudly and
// is fixed by moving the note to its own line, whereas a cleverer stripper that
// guessed wrong would fail silently.
func scanShellStampScrapers(root string) (map[string][]string, error) {
	found := map[string][]string{}
	err := walkGuardFiles(root, func(rel, path string) error {
		if !isShellScript(path) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			code := strings.TrimSpace(line)
			if code == "" || strings.HasPrefix(code, "#") {
				continue
			}
			for _, bad := range shellStampTokens {
				if strings.Contains(code, bad) {
					found[rel] = append(found[rel], rel+":"+strconv.Itoa(i+1)+": "+bad)
				}
			}
		}
		return nil
	})
	return found, err
}

// isShellScript identifies a script by extension OR by shebang.
//
// The shebang half is why this is not a copy of macguffin's walk, which keys on
// ".sh" alone. pogo's only real scraper is scripts/pogo-self-deploy, which has
// NO extension: run against this tree, the extension-only scan reported PASS
// while the scrape sat at line 727. A guard that cannot see the one thing it is
// for is worse than no guard, because it is also an assurance.
func isShellScript(path string) bool {
	if filepath.Ext(path) == ".sh" {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [64]byte
	n, _ := f.Read(head[:])
	first := string(head[:n])
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	return strings.HasPrefix(first, "#!") && strings.Contains(first, "sh")
}

// walkGuardFiles visits every regular file under root, skipping dot-directories
// (.git in particular, which in a linked worktree is a file but in the source
// repo is a large directory), vendor and testdata.
func walkGuardFiles(root string, fn func(rel, path string) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" ||
				name == "testdata" || name == "_testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		return fn(rel, path)
	})
}

// diffKeys splits found against allowed: sites present but unlisted, and sites
// listed but absent.
func diffKeys(found map[string]string, allowed map[string]string) (unlisted, missing []string) {
	for k, what := range found {
		if _, ok := allowed[k]; !ok {
			unlisted = append(unlisted, what)
		}
	}
	for k := range allowed {
		if _, ok := found[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(missing)
	return unlisted, missing
}

// guardRepoRoot locates the module root from this test's own source path, the
// way internal/turnlog's writer guard does. It does not depend on a TestMain,
// which is what macguffin's testProjectRoot needs and is one of the two things
// that had to change to bring this guard across.
func guardRepoRoot(t *testing.T) string {
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
