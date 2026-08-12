package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// The witness store's population guard (mg-ef7d).
//
// WHAT THIS FILE IS FOR. internal/agent's witness store held polecats only until
// mg-f9e8 widened the writer to crew. Six readers that mean "the polecats"
// literally were correct up to that point by INHERITANCE from a single early
// return in noteWitnessStart, and inheritance is not checked by anything. mg-f9e8
// fixed the six readers that existed; it could not fix the seventh, and its own
// guard test enumerates the six by hand — which is precisely the thing that does
// not scale, because a reader added tomorrow is absent from a hand-written list
// and absence there reads as "fine".
//
// WHICH PROPERTY THIS BUYS, stated plainly because the ticket asked for it. Go
// has no encapsulation below the package: anything in package agent can call
// loadWitnessAllTypes, and the stronger option the ticket floated — a distinct
// type only a filtered accessor can construct — is bypassable the same way, by
// constructing it. So this is not a compiler guard and does not claim to be. It
// buys two things the previous arrangement did not have:
//
//   - the type question is asked by the IDENTIFIER at the call site. There is no
//     longer a spelling of the loader that reads as "just give me the records":
//     a caller writes loadWitnessAllTypes or loadPolecatWitness, and both name a
//     population;
//   - the enumeration is INVERTED. This test enumerates the exceptions — the few
//     functions that legitimately span every type — not the readers. A new
//     caller of loadWitnessAllTypes is therefore uncovered-and-FAILING rather than
//     uncovered-and-silent, which is the only polarity that survives a reader
//     nobody has written yet.
//
// WHAT IT CANNOT SEE. A reader that reaches past the loader entirely — its own
// os.ReadFile of WitnessPath(), its own json.Unmarshal — is invisible to a check
// keyed on function names. The witnessOnDisk assertion below closes the obvious
// spelling of that (nothing but the loader and the writer may name the on-disk
// shape); a determined reimplementation of the parse from scratch is still
// possible and is not defended against here.

// witnessAllTypesReaders is the CLOSED set of functions in this package that may
// call loadWitnessAllTypes, each with the reason it is not a defect.
//
// To add an entry you must state why a reader that answers about crew — and
// about whatever third agent type this fleet grows next — is correct for your
// caller. If you cannot, you wanted loadPolecatWitness.
var witnessAllTypesReaders = map[string]string{
	"loadPolecatWitness": "the filter itself — it is the one function that must see every type in order " +
		"to drop the ones that are not polecats. Nothing else in this package should look like this",
	"RecordAgentWitness": "the WRITER's read-modify-write cycle: a polecat-only load would drop every " +
		"crew record on the next save, and would fail to replace-by-name for a crew respawn",
	"noteWitnessExit": "the writer's other half: a polecat-only load would leave a crew record behind " +
		"forever, answering from a pid pogod no longer owns",
	"AgentWitness": "THE reader for which all-types is the fix rather than an exception (mg-f9e8) — the " +
		"mail-check classifier asks about ONE named agent of whatever kind, and an auto_start=false " +
		"crew agent invisible here is reaped while alive",
}

// TestWitnessAllTypesReadersAreDeclared is the guard itself: every caller of the
// unfiltered loader must be a declared exception.
func TestWitnessAllTypesReadersAreDeclared(t *testing.T) {
	pkg := parseAgentPackageForLint(t)

	callers := pkg.callersOf("loadWitnessAllTypes")
	for _, fn := range callers {
		if _, ok := witnessAllTypesReaders[fn]; ok {
			continue
		}
		t.Errorf(`%s calls loadWitnessAllTypes and is not a declared all-types reader.

The witness store holds records for CREW as well as polecats (mg-f9e8), and for whatever
agent type is added after that. A reader that means "the polecats" must call
loadPolecatWitness — the unfiltered loader hands you a population you did not ask for,
and the cost of that is not a wrong number:

  - the redeploy drain waits for the alive count to reach zero, and crew never exit;
  - the orphan alert mails the coordinator an authoritative "kill <pid>" per row;
  - gitgc's live set, the per-repo dispatch cap and stall-watch's in-flight set all
    silently gain rows that never clear.

If %s genuinely must see every type, add it to witnessAllTypesReaders in
%s with the reason — that is the whole procedure, and writing the
reason is the point of it.`, fn, fn, "witnessreaders_lint_test.go")
	}

	// --- vacuity controls -------------------------------------------------
	//
	// A lint that finds nothing passes, so every way this check can quietly stop
	// checking is asserted against. A rename of either loader, a parse that reads
	// no files, or an allowlist entry naming a function that no longer exists all
	// turn this test green while enforcing nothing.

	for _, required := range []string{"loadWitnessAllTypes", "loadPolecatWitness", "isPolecat"} {
		if !pkg.declares(required) {
			t.Fatalf("control: this package no longer declares %s — the lint above is now grepping for a "+
				"name nothing has, which passes while enforcing nothing. Rename the constant here too, "+
				"or delete this test deliberately rather than by accident.", required)
		}
	}

	for fn := range witnessAllTypesReaders {
		if !contains(callers, fn) {
			t.Errorf("control: witnessAllTypesReaders names %s, but no function by that name calls "+
				"loadWitnessAllTypes. Either the exception is stale (delete it) or the AST walk has "+
				"stopped seeing call sites (fix it) — and a stale allowlist is how this guard rots into "+
				"a list of names nobody checks.", fn)
		}
	}

	// The reader side must also be visible to the walk. If this drops to zero the
	// enforcement above is still "working" and protecting nothing.
	polecatReaders := pkg.callersOf("loadPolecatWitness")
	if len(polecatReaders) < 5 {
		t.Errorf("control: only %d function(s) call loadPolecatWitness (%v), want at least 5 — the five "+
			"polecat-only readers are WitnessedAlivePolecats, WitnessedPolecatVerdicts, "+
			"WitnessedPolecatRepos, WitnessedPolecatWorkItems and UnadoptablePolecats. Fewer means "+
			"either a reader has gone back to loading the whole store, or this walk has stopped "+
			"resolving calls at all.", len(polecatReaders), polecatReaders)
	}
}

// TestIsPolecatHasExactlyOneCaller keeps the population filter in ONE place.
//
// isPolecat used to be called by each polecat reader in turn. That is how the
// DEFINITION stayed right while the OBLIGATION to invoke it stayed unenforced —
// the seventh reader simply would not have called it. Now loadPolecatWitness is
// the only caller, so "did you filter?" is answered by which loader you named
// rather than by whether you remembered a line inside your loop.
//
// A second caller is not automatically wrong, but it is always a reader that
// loaded the wrong population and is patching over it inside its own body, which
// is the exact shape mg-ef7d exists to stop. Make it justify itself here.
func TestIsPolecatHasExactlyOneCaller(t *testing.T) {
	pkg := parseAgentPackageForLint(t)
	callers := pkg.callersOf("isPolecat")
	want := []string{"loadPolecatWitness"}
	if !equalStrings(callers, want) {
		t.Errorf("isPolecat is called by %v, want exactly %v.\n\n"+
			"A reader filtering inline is a reader that took the unfiltered load and then remembered — "+
			"and remembering is what mg-f9e8's six readers were relying on. Call loadPolecatWitness "+
			"instead. If a new call site really is right, say so here and widen `want`.", callers, want)
	}
}

// TestWitnessOnDiskShapeIsNotReadOutsideTheLoader closes the cheap bypass: a
// reader that skips the accessors and unmarshals the file itself would satisfy
// every check above while seeing every record in the store.
func TestWitnessOnDiskShapeIsNotReadOutsideTheLoader(t *testing.T) {
	pkg := parseAgentPackageForLint(t)
	allowed := map[string]bool{
		"loadWitnessAllTypes": true, // the parse
		"saveWitness":         true, // the write
	}
	users := pkg.usersOfIdent("witnessOnDisk")
	var bad []string
	for _, fn := range users {
		if !allowed[fn] {
			bad = append(bad, fn)
		}
	}
	if len(bad) > 0 {
		t.Errorf("%v name the on-disk witness shape directly, bypassing loadPolecatWitness.\n\n"+
			"Reading the file yourself gets you every record in it — crew included — with none of the "+
			"population question asked. Go through the loaders (mg-ef7d).", bad)
	}
	if len(users) == 0 {
		t.Error("control: nothing in this package names witnessOnDisk, so this check is grepping for a " +
			"type that no longer exists and would pass against any bypass at all.")
	}
}

// --- AST plumbing -----------------------------------------------------------

// lintPkg is the parsed non-test source of this package. Test files are excluded
// deliberately: a test that reads the whole store to inspect it is doing exactly
// what it should, and including them would turn every fixture into a finding.
type lintPkg struct {
	// calls maps a called identifier to the qualified names of the functions
	// whose bodies call it.
	calls map[string]map[string]bool
	// idents maps a referenced identifier to the functions naming it, for checks
	// that are about mentioning a type rather than calling a function.
	idents map[string]map[string]bool
	// declared is every function and method this package declares.
	declared map[string]bool
	files    int
}

func parseAgentPackageForLint(t *testing.T) *lintPkg {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	p := &lintPkg{
		calls:    map[string]map[string]bool{},
		idents:   map[string]map[string]bool{},
		declared: map[string]bool{},
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			p.files++
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				name := funcLintName(fn)
				p.declared[name] = true
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.CallExpr:
						if id := calleeIdent(node.Fun); id != "" {
							p.record(p.calls, id, name)
						}
					case *ast.Ident:
						p.record(p.idents, node.Name, name)
					}
					return true
				})
			}
		}
	}
	if p.files == 0 {
		t.Fatal("control: parsed 0 non-test files in this package — every check in this file would " +
			"pass against anything")
	}
	return p
}

func (p *lintPkg) record(m map[string]map[string]bool, key, fn string) {
	if m[key] == nil {
		m[key] = map[string]bool{}
	}
	m[key][fn] = true
}

// declares reports whether the package declares a function by this bare name.
func (p *lintPkg) declares(name string) bool {
	for decl := range p.declared {
		if decl == name || strings.HasSuffix(decl, "."+name) {
			return true
		}
	}
	return false
}

func (p *lintPkg) callersOf(name string) []string { return sortedKeys(p.calls[name]) }

func (p *lintPkg) usersOfIdent(name string) []string { return sortedKeys(p.idents[name]) }

// funcLintName renders a function or method as it would be written in prose:
// "AgentWitness", or "Registry.UnadoptablePolecats" for a method.
func funcLintName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if id, ok := recv.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// calleeIdent returns the bare name being called: `f()` yields "f" and
// `x.m()` yields "m", so a method call reaches the same key as a plain call.
// Package-qualified calls (`json.Unmarshal`) also reduce to the selector, which
// is harmless here — nothing this file greps for shares a name with one.
func calleeIdent(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
