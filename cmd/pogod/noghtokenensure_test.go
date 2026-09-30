package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPogodNeverWritesGHTokenIntoItsOwnEnvironment pins mg-37183: pogod must
// not call ghtoken.Ensure, which os.Setenv's the token into pogod itself and so
// into every agent, gate and hook it spawns afterwards (and holds it stale
// across a rotation, mg-4d59). Its gh and https-git children get a credential
// per call from ghtoken.ChildEnv instead.
//
// The positive control parses a snippet that DOES call it, so a walker that
// silently matched nothing could not pass.
func TestPogodNeverWritesGHTokenIntoItsOwnEnvironment(t *testing.T) {
	control := "package main\nimport \"x/ghtoken\"\nfunc f() { _ = ghtoken.Ensure() }\n"
	if n := ensureCalls(t, "control.go", control); n != 1 {
		t.Fatalf("control: found %d ghtoken.Ensure calls in a snippet with one — the walker is blind", n)
	}

	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no pogod sources found: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := ensureCalls(t, f, string(src)); n != 0 {
			t.Errorf("%s calls ghtoken.Ensure %d time(s); give the child a per-call credential with "+
				"ghtoken.ChildEnv instead (mg-37183)", f, n)
		}
	}
}

func ensureCalls(t *testing.T, name, src string) int {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "ghtoken" && sel.Sel.Name == "Ensure" {
			n++
		}
		return true
	})
	return n
}
