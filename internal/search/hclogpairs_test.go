package search

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"

	"github.com/drellem2/pogo/pkg/plugin"
)

// The hclog key/value guard for this package (mg-6698).
//
// WHAT THIS FILE IS FOR. hclog's Info/Warn/Error take a message followed by
// key/value PAIRS. A call that passes a bare trailing value has an odd argument
// count, so hclog invents a field name for it:
//
//	g.logger.Error("Error reading file ", absPath)
//	-> {"@level":"error","@message":"Error reading file ","EXTRA_VALUE_AT_END":"..."}
//
// The one thing a reader wants from the line is then in neither the message nor
// a field they can query. gh#111 fixed exactly this on the "Reindexing" line and
// wrote the reasoning into the comment at the call site; that fix worked and the
// live log proves it. What it could not do is stop the other 23 call sites in
// this package from doing the same thing — a hand-fixed line stays fixed, and a
// hand-fixed PACKAGE does not, because nothing in the toolchain sees the defect.
// `go vet`'s printf analysis does not know hclog, and the argument count is legal
// Go either way.
//
// Two checks, deliberately different in kind:
//
//   - TestIndexPassEmitsNoUnpairedTrailingValue is live fire. It runs real index
//     passes through the real logger and reads what hclog actually wrote. It can
//     only see lines the fixture provokes, which is a minority of them.
//   - TestHclogCallsInThisPackageArePaired is static. It reaches every call site
//     including the error paths no fixture reaches, at the cost of reasoning
//     about source rather than output.
//
// Neither subsumes the other: the lint cannot prove hclog behaves as described,
// and the live test cannot reach `Error("Error creating searcher", ...)`.

// --- live fire ---------------------------------------------------------------

// printfVerb matches a formatting directive left in an hclog message. hclog does
// no formatting at all, so `Info("Processing project %s", path)` emitted the
// literal "%s" AND put the path in EXTRA_VALUE_AT_END — one call site, two
// defects, 35 occurrences on the day mg-6698 was filed.
var printfVerb = regexp.MustCompile(`%[-+# 0-9.]*[vTtbcdoOqxXUeEfFgGsp]`)

// TestIndexPassEmitsNoUnpairedTrailingValue drives the paths a running daemon
// takes every tick — first index, re-index of an unchanged tree, incremental
// re-index after a write — and asserts that nothing hclog wrote carries a
// synthetic field or an unformatted verb.
func TestIndexPassEmitsNoUnpairedTrailingValue(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})
	capture := captureLogs(t, bs, hclog.Trace)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	if err := bs.ProcessProject(&req); err != nil {
		t.Fatalf("process project: %v", err)
	}
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	// Second Index over a ready project takes the "Already indexed" branch.
	bs.Index(&req)
	quiesceForLogs(t, bs)

	// A write makes the next pass incremental, which is the branch that reuses
	// cached hashes.
	bs.ReIndex(root)
	waitIndexed(t, events, root)
	quiesceForLogs(t, bs)

	recs := capture.records(t)
	if len(recs) == 0 {
		t.Fatal("control: the capture is empty, so every assertion below would pass " +
			"against a logger that had been removed entirely")
	}
	for _, rec := range recs {
		if v, bad := rec["EXTRA_VALUE_AT_END"]; bad {
			t.Errorf("hclog invented a field for an unpaired trailing argument: %q got EXTRA_VALUE_AT_END=%v\n"+
				"The value belongs in a named key/value pair — see internal/diagnostics/diagnostics.go "+
				"for the shape.", recMessage(rec), v)
		}
		if verb := printfVerb.FindString(recMessage(rec)); verb != "" {
			t.Errorf("message %q contains %q, but hclog never formats its message — the verb reaches "+
				"the log literally", recMessage(rec), verb)
		}
	}

	// The two lines this fixture provokes that mg-6698 rewrote, checked by field
	// rather than by absence, so a call that lost its payload entirely fails too.
	if got := findLogged(recs, "info", "Processing project"); len(got) != 1 {
		t.Errorf("want exactly one %q record, got %d:\n%s", "Processing project", len(got), formatRecords(recs))
	} else if path, _ := got[0]["path"].(string); path != root {
		t.Errorf("Processing project: path field = %q, want %q", path, root)
	}

	if got := findLogged(recs, "info", "Already indexed"); len(got) != 1 {
		t.Errorf("want exactly one %q record, got %d:\n%s", "Already indexed", len(got), formatRecords(recs))
	} else if r, _ := got[0]["root"].(string); r != root {
		t.Errorf("Already indexed: root field = %q, want %q", r, root)
	}
}

// --- static lint -------------------------------------------------------------

// hclogPairExemptions is the CLOSED set of call sites in this package's non-test
// source that still pass an unpaired trailing value, keyed by their exact
// message literal.
//
// It is EMPTY, and that is the intended end state. The sweep (mg-6698) landed
// with two entries in it, both in serializeProjectIndex's read loop, held back
// because mg-f32a was rewriting those same statements for the socket fix and two
// branches on one statement is the thing worth avoiding. mg-f32a paired them, so
// the entries went stale and this map emptied — which is the outcome it was
// recording the wait for.
//
// The test below fails on an entry that no longer matches anything, so a
// re-added exemption cannot outlive the call site it excuses. If you are here
// because that failure named your branch: delete the entry, that is the whole
// procedure.
var hclogPairExemptions = map[string]string{}

// hclogLevels are the methods that take (msg, key, value, ...). Log() and the
// With/Named builders are deliberately absent: Log takes a leading level and
// With takes pairs with no message, so both have the opposite parity.
var hclogLevels = map[string]bool{
	"Trace": true, "Debug": true, "Info": true, "Warn": true, "Error": true,
}

type hclogCall struct {
	pos     string // file:line
	msg     string // the message literal, or "" when it is not a plain string
	nargs   int    // total arguments including the message
	spread  bool   // the call ends in args..., so parity is not decidable here
	message string
}

// TestHclogCallsInThisPackageArePaired is the guard: every logger call must have
// an odd argument count (one message plus whole pairs) and a message free of
// printf verbs.
func TestHclogCallsInThisPackageArePaired(t *testing.T) {
	calls := parseHclogCalls(t)

	seenExempt := map[string]bool{}
	for _, c := range calls {
		if c.spread {
			continue
		}
		if c.nargs%2 == 0 {
			if _, ok := hclogPairExemptions[c.msg]; ok {
				seenExempt[c.msg] = true
				continue
			}
			t.Errorf(`%s: %s has %d arguments — a message plus %d value(s), which hclog cannot pair.

hclog appends the odd one out as EXTRA_VALUE_AT_END, so the payload lands in a synthetic
field that no consumer of this log queries: not a grep for a path, not a doctor check, not
an event-spine reader. An empty result from those is indistinguishable from "it never
happened", which is the failure gh#136 was reported for.

Write it as a named pair — ("msg", "<key>", v) — the way internal/diagnostics/diagnostics.go
does. Pick a key that names the value: path, root, error, index_path.`,
				c.pos, c.message, c.nargs, c.nargs-1)
		}
		if verb := printfVerb.FindString(c.msg); verb != "" {
			t.Errorf("%s: %s has %q in its message, but hclog does no formatting — the verb is emitted "+
				"literally and the argument it was meant to consume becomes a field (or EXTRA_VALUE_AT_END). "+
				"Drop the verb and name the value.", c.pos, c.message, verb)
		}
	}

	// --- vacuity controls ---------------------------------------------------
	//
	// A lint that inspects nothing passes. Every way this check can stop
	// checking — a receiver rename, a parse that reads no files, an exemption
	// outliving the call site it excuses — is asserted against here.

	if len(calls) < 40 {
		t.Fatalf("control: the walk found only %d logger call(s) in this package's non-test source, "+
			"want at least 40 — there were 53 when this guard was written. Fewer means the receiver "+
			"or the level names moved and this test is now inspecting almost nothing.", len(calls))
	}

	for msg, why := range hclogPairExemptions {
		if seenExempt[msg] {
			continue
		}
		t.Errorf(`control: hclogPairExemptions still excuses %q, but no unpaired call site has that message.

Recorded reason: %s

Either the call site was fixed — delete this entry, that is the whole procedure — or the walk
has stopped seeing it, in which case this guard is excusing a defect it can no longer find.
A stale exemption is how a check like this rots into a list nobody reads.`, msg, why)
	}
}

// TestHclogPairDetectorFiresOnAKnownBadCall is the detector's own control. The
// checks above are all negative — they pass when they find nothing — so a walk
// that resolved no arguments at all would report a clean package. This asserts
// the parity test fires on a call shaped like the defect.
func TestHclogPairDetectorFiresOnAKnownBadCall(t *testing.T) {
	const bad = `package p
func f(g G) {
	g.logger.Error("Error reading file ", absPath)
	g.logger.Info("Processing project %s", path)
	g.logger.Info("Processing project", "path", path)
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bad.go", bad, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	calls := collectHclogCalls(fset, file)
	if len(calls) != 3 {
		t.Fatalf("collector found %d call(s) in the fixture, want 3 — it is not recognising "+
			"g.logger.<Level>(...) at all", len(calls))
	}
	if calls[0].nargs%2 != 0 {
		t.Errorf("the unpaired call was read as %d argument(s); the parity check cannot fire", calls[0].nargs)
	}
	if printfVerb.FindString(calls[1].msg) == "" {
		t.Errorf("the printf verb in %q was not detected", calls[1].msg)
	}
	if calls[2].nargs%2 == 0 {
		t.Errorf("the correctly paired call was flagged as unpaired (%d args) — this lint would "+
			"fail every fixed call site", calls[2].nargs)
	}
}

// --- AST plumbing ------------------------------------------------------------

// parseHclogCalls walks this package's non-test source. Test files are excluded
// deliberately: a fixture may construct a malformed call on purpose, and the one
// directly above does.
//
// The directory comes from this file's own path, not from the working directory:
// this package's TestMain does os.Chdir("../..") (search_impl_test.go), so a walk
// of "." reads the repo root, finds no package there, and parses nothing. That
// version of this test passed — it was the files==0 control below that caught it,
// which is the entire argument for writing such controls.
func parseHclogCalls(t *testing.T) []hclogCall {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test file's path; the package directory is unknown")
	}
	dir := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	var out []hclogCall
	files := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			files++
			out = append(out, collectHclogCalls(fset, file)...)
		}
	}
	if files == 0 {
		t.Fatalf("control: parsed 0 non-test files in %s — every check keyed on this walk would pass "+
			"against anything", dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	return out
}

// collectHclogCalls returns every `<something>.logger.<Level>(...)` call in file.
func collectHclogCalls(fset *token.FileSet, file *ast.File) []hclogCall {
	var out []hclogCall
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !hclogLevels[sel.Sel.Name] || !isLoggerExpr(sel.X) {
			return true
		}
		c := hclogCall{
			pos:     fset.Position(call.Pos()).String(),
			nargs:   len(call.Args),
			spread:  call.Ellipsis != token.NoPos,
			message: "logger." + sel.Sel.Name,
		}
		if len(call.Args) > 0 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					c.msg = s
					c.message = "logger." + sel.Sel.Name + "(" + lit.Value + ", …)"
				}
			}
		}
		out = append(out, c)
		return true
	})
	return out
}

// isLoggerExpr reports whether e names the logger field — `g.logger` here, or a
// bare `logger` if this package ever grows a free-standing one.
func isLoggerExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "logger"
	case *ast.SelectorExpr:
		return x.Sel.Name == "logger"
	}
	return false
}
