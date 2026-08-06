package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A recurrence check for unlabeled time layouts in cmd/pogo (mg-0235).
//
// THE CLASS. A Go time.Time renders in whatever Location it was deserialized
// into. A layout with no zone designator — "2006-01-02 15:04", "15:04:05" —
// therefore prints digits whose meaning depends on where the value came from,
// with nothing on the line saying which. The reader cannot detect it. They can
// only notice, later, that two surfaces disagree.
//
// It has been found by a human twice, and both failure modes cost real time:
//
//  1. TWO SURFACES DISAGREE. `pogo refinery history` and `history --since` print
//     the same MR's done time an hour apart on a non-UTC host, because the
//     retained window unmarshals a stored +01:00 offset and the --since window
//     parses an events log written in UTC. Same command, same MR, documented
//     flag, one hour apart (drellem2/pogo#109).
//  2. THE RECORD LOOKS CORRUPT. A reader on a UTC clock read a gate "started
//     18:51:05" at 18:28 and briefly concluded the gate had started IN THE
//     FUTURE. This is the nastier one: an impossible timestamp invites the
//     conclusion that the record is wrong, not that a zone label is missing.
//
// THE RULE ENFORCED HERE is stronger than "normalize or label": every time
// layout rendered in cmd/pogo must carry a zone designator. Normalizing with
// .UTC() alone makes a value deterministic across surfaces — it kills mode (1)
// — but the printed line is still bare digits, so mode (2) survives untouched.
// Mode (2) is the one that produced a wrong conclusion about the data rather
// than a wrong reading of it. A Z-suffixed UTC timestamp cannot be misread by a
// reader in any zone; a local one is unambiguous only to a reader who already
// knows the host's offset, which an agent, a log reader, or a future reader at
// a different offset does not.
//
// A second rule guards the shape the audit-successors fix already had to
// repair: a layout whose zone designator is a LITERAL "Z" is a claim about the
// value, and the value has to be converted to make the claim true. Without
// .UTC() it prints the host's local clock and labels it Z, which is worse than
// printing it bare.
//
// IN-TREE PRECEDENT, which this check must not flag:
//   - auditsuccessors.go — .UTC().Format("2006-01-02 15:04Z")
//   - main.go (schedule list) — .Local().Format(time.RFC3339)

// zoneFindingKind names why a Format call was flagged.
type zoneFindingKind string

const (
	// kindUnlabeled: the layout carries no zone designator of any sort.
	kindUnlabeled zoneFindingKind = "unlabeled"
	// kindLiteralZWithoutUTC: the layout ends in a literal Z — a claim the
	// value is UTC — but the value is never converted to UTC.
	kindLiteralZWithoutUTC zoneFindingKind = "literal-Z-without-UTC"
	// kindUnresolvedLayout: the layout is not a literal or a time.* constant,
	// so this check cannot judge it. Flagged rather than skipped: a layout
	// hoisted into a variable would otherwise be a silent hole in the check.
	kindUnresolvedLayout zoneFindingKind = "unresolved-layout"
)

// zoneFinding is one Format call this check objects to.
type zoneFinding struct {
	File   string // base name, e.g. "refineryprogress.go"
	Line   int
	Recv   string // the receiver expression, e.g. "mr.DoneTime"
	Layout string
	Kind   zoneFindingKind
}

// key identifies a finding across line-number churn: the file and the receiver
// expression, never the line. Line numbers rot on the first edit above them.
func (f zoneFinding) key() string { return f.File + ":" + f.Recv }

func (f zoneFinding) String() string {
	return fmt.Sprintf("%s:%d  %s.Format(%q)  [%s]", f.File, f.Line, f.Recv, f.Layout, f.Kind)
}

// timeLayoutConstants maps the exported layout constants of the time package to
// their literal values, so `x.Format(time.RFC3339)` can be judged by the same
// rule as an inline string.
var timeLayoutConstants = map[string]string{
	"Layout":      time.Layout,
	"ANSIC":       time.ANSIC,
	"UnixDate":    time.UnixDate,
	"RubyDate":    time.RubyDate,
	"RFC822":      time.RFC822,
	"RFC822Z":     time.RFC822Z,
	"RFC850":      time.RFC850,
	"RFC1123":     time.RFC1123,
	"RFC1123Z":    time.RFC1123Z,
	"RFC3339":     time.RFC3339,
	"RFC3339Nano": time.RFC3339Nano,
	"Kitchen":     time.Kitchen,
	"Stamp":       time.Stamp,
	"StampMilli":  time.StampMilli,
	"StampMicro":  time.StampMicro,
	"StampNano":   time.StampNano,
	"DateTime":    time.DateTime,
	"DateOnly":    time.DateOnly,
	"TimeOnly":    time.TimeOnly,
}

// layoutTokens are reference-time components. A string argument containing none
// of them is not a time layout, so the Format call is somebody else's Format.
var layoutTokens = []string{"2006", "15:04", "03:04", "01/02", "02/01", "Jan", "Mon", "PM", "_2"}

func looksLikeTimeLayout(s string) bool {
	for _, tok := range layoutTokens {
		if strings.Contains(s, tok) {
			return true
		}
	}
	return false
}

// zoneDesignator reports what kind of zone information the layout prints:
// "offset" for a real zone specifier (Z07:00, -0700, MST), "literal-Z" for a
// bare trailing Z that is a literal and asserts UTC, "" for none.
func zoneDesignator(layout string) string {
	if strings.Contains(layout, "Z07") || strings.Contains(layout, "-07") ||
		strings.Contains(layout, "+07") || strings.Contains(layout, "MST") {
		return "offset"
	}
	if strings.Contains(layout, "Z") {
		return "literal-Z"
	}
	return ""
}

// normalizer returns "UTC", "Local", or "" for the method the value was
// normalized with immediately before Format — i.e. the X in X.Format(...).
func normalizer(x ast.Expr) string {
	call, ok := x.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	switch sel.Sel.Name {
	case "UTC", "Local":
		return sel.Sel.Name
	}
	return ""
}

// zoneScanStats names the population the scan covered. A check that reports
// only its findings cannot be told apart from a check that looked at nothing,
// so the tree test prints this even when it passes.
type zoneScanStats struct {
	Files        int // non-test .go files parsed
	FormatCalls  int // every single-argument .Format(x) call seen
	TimeLayouts  int // of those, the ones whose layout is a resolvable time layout
	Zoned        int // of those, the ones carrying a real offset specifier
	LiteralZ     int // ... a literal Z (correct only with .UTC())
	Unlabeled    int // ... nothing at all
	Unresolvable int // layout not a literal or time.* constant, so unjudgeable
}

func (s zoneScanStats) String() string {
	return fmt.Sprintf("%d non-test .go files, %d Format() calls, %d of them time layouts: "+
		"%d carry an offset specifier, %d a literal Z, %d carry nothing, %d unjudgeable",
		s.Files, s.FormatCalls, s.TimeLayouts, s.Zoned, s.LiteralZ, s.Unlabeled, s.Unresolvable)
}

// scanUnlabeledTimeLayouts parses every non-test .go file directly in dir and
// returns the Format calls that break the rules above, alongside the population
// it searched over.
func scanUnlabeledTimeLayouts(dir string) ([]zoneFinding, zoneScanStats, error) {
	var stats zoneScanStats
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, stats, err
	}

	var findings []zoneFinding
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			base := filepath.Base(path)
			stats.Files++
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Format" || len(call.Args) != 1 {
					return true
				}
				stats.FormatCalls++

				layout, resolved := resolveLayout(call.Args[0])
				recv := exprString(fset, sel.X)
				pos := fset.Position(call.Lparen)

				if !resolved {
					// Only complain about an unresolved argument when the
					// receiver is plausibly a time value; other packages have
					// Format methods too.
					if looksLikeTimeReceiver(recv) {
						stats.Unresolvable++
						findings = append(findings, zoneFinding{
							File: base, Line: pos.Line, Recv: recv,
							Layout: exprString(fset, call.Args[0]), Kind: kindUnresolvedLayout,
						})
					}
					return true
				}
				if !looksLikeTimeLayout(layout) {
					return true
				}
				stats.TimeLayouts++

				switch zoneDesignator(layout) {
				case "offset":
					// Self-describing in any Location. Nothing to say.
					stats.Zoned++
				case "literal-Z":
					stats.LiteralZ++
					if normalizer(sel.X) != "UTC" {
						findings = append(findings, zoneFinding{
							File: base, Line: pos.Line, Recv: recv,
							Layout: layout, Kind: kindLiteralZWithoutUTC,
						})
					}
				default:
					stats.Unlabeled++
					findings = append(findings, zoneFinding{
						File: base, Line: pos.Line, Recv: recv,
						Layout: layout, Kind: kindUnlabeled,
					})
				}
				return true
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, stats, nil
}

// resolveLayout extracts the layout string from a Format argument, handling a
// string literal and a time.* constant. Anything else is unresolved.
func resolveLayout(arg ast.Expr) (string, bool) {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(a.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.SelectorExpr:
		pkg, ok := a.X.(*ast.Ident)
		if !ok || pkg.Name != "time" {
			return "", false
		}
		s, ok := timeLayoutConstants[a.Sel.Name]
		return s, ok
	}
	return "", false
}

// looksLikeTimeReceiver is a name heuristic used only to decide whether an
// unresolvable layout is worth reporting.
func looksLikeTimeReceiver(recv string) bool {
	lower := strings.ToLower(recv)
	for _, hint := range []string{"time", "at", "date", "expiry", "stamp", "now", "deadline"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func exprString(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable>"
	}
	return b.String()
}

// TestUnlabeledTimeLayoutScannerFiresOnItsControl is the positive control. A
// check that has never been shown to fail is not evidence that the tree is
// clean — it is equally consistent with a scanner that finds nothing anywhere.
// The fixture in testdata/zonelabel holds one deliberately unlabeled layout and
// one deliberately mislabeled one, alongside the two correct in-tree patterns,
// and this test requires the scanner to separate them.
func TestUnlabeledTimeLayoutScannerFiresOnItsControl(t *testing.T) {
	findings, _, err := scanUnlabeledTimeLayouts(filepath.Join("testdata", "zonelabel"))
	if err != nil {
		t.Fatalf("scanning the control fixture: %v", err)
	}

	got := make(map[string]zoneFindingKind, len(findings))
	for _, f := range findings {
		got[f.key()] = f.Kind
	}

	want := map[string]zoneFindingKind{
		"bad.go:e.SubmitTime":        kindUnlabeled,
		"bad.go:e.DoneTime.UTC()":    kindUnlabeled,
		"bad.go:e.MergedAt":          kindLiteralZWithoutUTC,
		"bad.go:e.ScheduledAt":       kindUnresolvedLayout,
		"good.go:e.MergedAt.UTC()":   "",
		"good.go:e.NextFire.Local()": "",
	}

	for key, wantKind := range want {
		gotKind, flagged := got[key]
		switch {
		case wantKind == "" && flagged:
			t.Errorf("scanner flagged the correct pattern %s as %s — it would flag the in-tree precedent it is meant to endorse", key, gotKind)
		case wantKind != "" && !flagged:
			t.Errorf("scanner did NOT flag %s, which is deliberately defective in the fixture (expected %s). The check cannot be trusted to find the real thing.", key, wantKind)
		case wantKind != "" && gotKind != wantKind:
			t.Errorf("scanner flagged %s as %s, want %s", key, gotKind, wantKind)
		}
	}

	// .UTC() alone must not satisfy the check: the value is deterministic but
	// the printed line is still bare digits, which is failure mode (2).
	if got["bad.go:e.DoneTime.UTC()"] != kindUnlabeled {
		t.Errorf(".UTC() without a zone designator was accepted; the reader still cannot tell what zone the digits are in")
	}
}

// gh109Waiver holds the Format calls that mg-6f5e — the drellem2/pogo#109 fix —
// owns, and which this ticket is explicitly told not to touch: that fix is at a
// human go/no-go gate and is reviewed separately.
//
// The waiver is expected to be EXHAUSTED, not merely respected: the test below
// fails if an entry stops matching anything, so when mg-6f5e lands, this map
// tells you to delete it rather than leaving a permanent hole in the check.
var gh109Waiver = map[string]int{
	"main.go:mr.DoneTime":               2, // refinery history + refinery show
	"main.go:mr.SubmitTime":             1, // refinery show
	"main.go:mr.StartTime":              1, // refinery show
	"refineryprogress.go:mr.SubmitTime": 1, // refinery queue
}

// TestCmdPogoTimeLayoutsCarryAZoneDesignator is the recurrence check itself: it
// runs the scanner over the whole of cmd/pogo and requires the population to be
// empty apart from the separately gated gh#109 lines.
func TestCmdPogoTimeLayoutsCarryAZoneDesignator(t *testing.T) {
	findings, stats, err := scanUnlabeledTimeLayouts(".")
	if err != nil {
		t.Fatalf("scanning cmd/pogo: %v", err)
	}
	// Printed on pass as well as on failure: "no findings" is only meaningful
	// beside the size of the population it was drawn from.
	t.Logf("population searched: %s", stats)
	if stats.TimeLayouts < 20 {
		t.Fatalf("only %d time layouts found in cmd/pogo — the scan is not reaching the tree it is meant to cover (%s)", stats.TimeLayouts, stats)
	}

	remaining := make(map[string]int, len(gh109Waiver))
	for k, v := range gh109Waiver {
		remaining[k] = v
	}

	var unwaived []zoneFinding
	for _, f := range findings {
		if remaining[f.key()] > 0 {
			remaining[f.key()]--
			continue
		}
		unwaived = append(unwaived, f)
	}

	for _, f := range unwaived {
		t.Errorf("unlabeled time layout: %s\n"+
			"    A reader cannot tell which zone these digits are in, and two surfaces rendering\n"+
			"    from differently-deserialized values will disagree by the host's offset.\n"+
			"    Fix with the in-tree pattern: v.UTC().Format(\"<layout>Z\").", f)
	}

	for key, left := range remaining {
		if left > 0 {
			t.Errorf("gh109Waiver entry %q expected %d more unlabeled call(s) than the tree has — "+
				"if mg-6f5e (drellem2/pogo#109) has landed, delete the entry; a waiver that matches "+
				"nothing is a permanent hole in this check.", key, left)
		}
	}
}
