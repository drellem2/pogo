package alerts

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The guard: no alert mail leaves pogo except through Mail / MailOutput, named
// with a declared constant (drellem2/pogo#262 slice 1). Later slices mute and
// route at the seam, so a send that goes around it would be an alert nobody
// can mute — the guard is what makes "every alert is configurable" true of the
// code rather than of the code that existed the day this was written.
//
// It is a syntactic check, not a type-checked one. It catches the shapes a
// send has actually taken in this repo:
//
//   - any reference to SendMGMail (the transport's old name);
//   - `"mail", "send"` as adjacent string literals in a call or slice (an exec
//     of `mg mail send`), outside a transport handed to Mail;
//   - a four-argument call to a function named mail/Mail/send/sendMail/
//     SendMail/Transport — the to/from/subject/body MailFunc every watcher
//     takes — outside a transport handed to Mail.
//
// What it cannot see is a MailFunc stored under some other name and called
// directly. TestGuardFindsTheSeam's floor is the backstop against the checker
// going blind wholesale; review is the backstop against a creative rename.

// probeAllowed are files that run `mg mail send` against a THROWAWAY store
// they created, to test mg itself. Nothing they send reaches a person, so
// they are not alerts and have nothing to mute or route.
var probeAllowed = map[string]string{
	"internal/mgcontract/mgcontract.go": "mg contract probe: sends into a store `mg init` made seconds earlier",
	"internal/verdictwatch/probe.go":    "verdict-detector self-test: sends into a throwaway store",
}

// seamFuncs are the alerts functions a send site names its alert to.
var seamFuncs = map[string]bool{"Mail": true, "MailOutput": true, "Withhold": true}

var mailCallees = map[string]bool{
	"mail": true, "Mail": true, "send": true, "sendMail": true, "SendMail": true, "Transport": true,
}

type scanResult struct {
	violations []string
	seamCalls  map[string]int // const identifier -> number of call sites
	files      int
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}

// scan walks every Go file under root. self is the root-relative directory of
// the alerts package itself, which is exempt from the bypass rules (it IS the
// seam) but not from the SendMGMail rule.
func scan(root, self string, allowed map[string]string) (scanResult, error) {
	res := scanResult{seamCalls: map[string]int{}}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if info.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		res.files++
		isTest := strings.HasSuffix(name, "_test.go")
		inSelf := filepath.ToSlash(filepath.Dir(rel)) == self
		at := func(n ast.Node) string {
			return fmt.Sprintf("%s:%d", rel, fset.Position(n.Pos()).Line)
		}

		// Transports handed to the seam: function literals passed as an
		// argument to alerts.Mail. Sends inside them are the seam's own.
		var transports [][2]token.Pos
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && isSeamCall(c, inSelf) {
				for _, a := range c.Args {
					if fl, ok := a.(*ast.FuncLit); ok {
						transports = append(transports, [2]token.Pos{fl.Pos(), fl.End()})
					}
				}
			}
			return true
		})
		insideTransport := func(n ast.Node) bool {
			for _, r := range transports {
				if n.Pos() >= r[0] && n.End() <= r[1] {
					return true
				}
			}
			return false
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "SendMGMail" && !inSelf {
					res.violations = append(res.violations, at(x)+": SendMGMail — send through alerts.Mail with alerts.Transport")
				}
			case *ast.CallExpr:
				if isTest || inSelf {
					return true
				}
				if isSeamCall(x, inSelf) {
					if len(x.Args) == 0 {
						return true
					}
					sel, ok := x.Args[0].(*ast.SelectorExpr)
					if id, isID := selX(sel); !ok || !isID || id != "alerts" {
						res.violations = append(res.violations, at(x)+": the alert name must be a declared alerts.<Name> constant")
						return true
					}
					res.seamCalls[sel.Sel.Name]++
					return true
				}
				if hasMailSend(x.Args) && !insideTransport(x) {
					if _, ok := allowed[rel]; !ok {
						res.violations = append(res.violations, at(x)+": exec of `mg mail send` outside alerts.Mail")
					}
				}
				if _, probe := allowed[rel]; len(x.Args) == 4 && mailCallees[calleeName(x)] && !insideTransport(x) && !probe {
					res.violations = append(res.violations, at(x)+": "+calleeName(x)+"(to, from, subject, body) called directly — wrap it: alerts.Mail(alerts.<Name>, "+calleeName(x)+", ...)")
				}
			case *ast.CompositeLit:
				if isTest || inSelf {
					return true
				}
				if hasMailSend(x.Elts) && !insideTransport(x) {
					if _, ok := allowed[rel]; !ok {
						res.violations = append(res.violations, at(x)+": `mg mail send` argv outside alerts.Mail")
					}
				}
			}
			return true
		})
		return nil
	})
	sort.Strings(res.violations)
	return res, err
}

// isSeamCall reports a call to alerts.Mail, alerts.MailOutput or
// alerts.Withhold (the mute check a non-mail send makes) — or, inside the
// alerts package, to those unqualified.
func isSeamCall(c *ast.CallExpr, inSelf bool) bool {
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		id, ok := fn.X.(*ast.Ident)
		return ok && id.Name == "alerts" && seamFuncs[fn.Sel.Name]
	case *ast.Ident:
		return inSelf && seamFuncs[fn.Name]
	}
	return false
}

func selX(sel *ast.SelectorExpr) (string, bool) {
	if sel == nil {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

func calleeName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

func hasMailSend(exprs []ast.Expr) bool {
	for i := 0; i+1 < len(exprs); i++ {
		if lit(exprs[i]) == "mail" && lit(exprs[i+1]) == "send" {
			return true
		}
	}
	return false
}

func lit(e ast.Expr) string {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(b.Value)
	if err != nil {
		return ""
	}
	return s
}

// declaredConsts parses registry.go for its `X Name = "..."` constants.
func declaredConsts(t *testing.T) map[string]Name {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "registry.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Name{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Name" {
				continue
			}
			for i, n := range vs.Names {
				out[n.Name] = Name(lit(vs.Values[i]))
			}
		}
	}
	return out
}

func TestNoSendBypassesTheRegistry(t *testing.T) {
	root := moduleRoot(t)
	res, err := scan(root, "internal/alerts", probeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.violations {
		t.Errorf("send bypasses the alert registry: %s", v)
	}
}

// TestGuardFindsTheSeam is the scan's own control: a walker that silently
// stopped parsing (wrong root, a skipped tree) would find no violations AND
// no seam calls. A floor on the seam calls it does find tells those apart.
func TestGuardFindsTheSeam(t *testing.T) {
	res, err := scan(moduleRoot(t), "internal/alerts", probeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range res.seamCalls {
		total += n
	}
	const floor = 60
	if total < floor || res.files < 500 {
		t.Fatalf("guard saw %d seam call(s) in %d file(s); expected at least %d calls in 500 files — the scan is not reading the tree",
			total, res.files, floor)
	}
}

func TestRegistryMatchesTheSendSites(t *testing.T) {
	consts := declaredConsts(t)
	if len(consts) == 0 {
		t.Fatal("parsed no Name constants from registry.go")
	}
	res, err := scan(moduleRoot(t), "internal/alerts", probeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	for ident := range res.seamCalls {
		if _, ok := consts[ident]; !ok {
			t.Errorf("alerts.%s is used at a send site but is not a declared alert Name", ident)
		}
	}
	for ident, name := range consts {
		if _, ok := Lookup(name); !ok {
			t.Errorf("alerts.%s (%q) is declared but not in the registry", ident, name)
		}
		if res.seamCalls[ident] == 0 {
			t.Errorf("alerts.%s (%q) is registered but no send site uses it", ident, name)
		}
	}
	if len(registry) != len(consts) {
		t.Errorf("registry has %d entries for %d declared names", len(registry), len(consts))
	}
}

// TestGuardFlagsEachBypassShape is the positive control: each shape the guard
// claims to catch, planted in a scratch module, must be flagged — and the
// sanctioned forms beside them must not be.
func TestGuardFlagsEachBypassShape(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n")
	write("bad/bad.go", `package bad

import (
	"os/exec"
	"x/alerts"
	"x/client"
)

type w struct{ mail func(a, b, c, d string) error }

func f(x w, name string) {
	_ = client.SendMGMail("to", "from", "s", "b")
	_ = exec.Command("mg", "mail", "send", "to").Run()
	_ = []string{"--root", "r", "mail", "send", "to"}
	_ = x.mail("to", "from", "s", "b")
	_ = alerts.Transport("to", "from", "s", "b")
	_ = alerts.Mail(name, x.mail, "to", "from", "s", "b")
}
`)
	write("good/good.go", `package good

import (
	"os/exec"
	"x/alerts"
)

type w struct{ mail func(a, b, c, d string) error }

func f(x w) {
	_ = alerts.Mail(alerts.AckWatchFinding, x.mail, "to", "from", "s", "b")
	_ = alerts.Mail(alerts.RefusalWatchAlarm, func(to, from, s, b string) error {
		return exec.Command("mg", "mail", "send", to).Run()
	}, "to", "from", "s", "b")
	_, _ = alerts.MailOutput(alerts.OrphanPolecat, "to", "from", "s", "b")
	_ = exec.Command("mg", "mail", "list").Run()
}
`)
	res, err := scan(root, "alerts", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bad/bad.go:12: SendMGMail",
		"bad/bad.go:13: exec of `mg mail send`",
		"bad/bad.go:14: `mg mail send` argv",
		"bad/bad.go:15: mail(to, from, subject, body) called directly",
		"bad/bad.go:16: Transport(to, from, subject, body) called directly",
		"bad/bad.go:17: the alert name must be a declared",
	}
	for _, w := range want {
		found := false
		for _, v := range res.violations {
			if strings.HasPrefix(v, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("guard did not flag %q; got:\n  %s", w, strings.Join(res.violations, "\n  "))
		}
	}
	for _, v := range res.violations {
		if strings.HasPrefix(v, "good/") {
			t.Errorf("guard flagged a sanctioned send: %s", v)
		}
	}
	if res.seamCalls["AckWatchFinding"] != 1 || res.seamCalls["RefusalWatchAlarm"] != 1 || res.seamCalls["OrphanPolecat"] != 1 {
		t.Errorf("seam calls not counted: %v", res.seamCalls)
	}
}
