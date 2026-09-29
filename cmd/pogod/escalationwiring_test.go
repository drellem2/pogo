package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/reaper"
)

// escalationBoxIdent is the local main() resolves [agents] escalation_box into.
const escalationBoxIdent = "escalationBox"

// escalationFields are the Options fields through which a pogod component is
// told which box a PERSON reads. Every one of them, anywhere in main.go, must be
// handed escalationBox: each package's own default is `human`, so a field left
// out or given a literal compiles, passes that package's tests, and quietly
// sends to `human` on exactly the deployments that re-pointed escalations
// (drellem2/pogo#148).
var escalationFields = map[string]bool{"MailTo": true, "EscalateTo": true, "HumanBox": true}

// TestMainRoutesEveryEscalationThroughEscalationBox is the main-level half of
// the per-package routing tests: those prove each component honours the box it
// is GIVEN; this proves main.go gives it the right one. It parses main.go and
// checks
//
//   - every MailTo / EscalateTo / HumanBox field in a composite literal is
//     the identifier escalationBox;
//   - the senders mg-f0ac9 and mg-a586e routed actually set that field
//     (synthwatch, driftwatch, credexpiry), so deleting the line fails too;
//   - claude.SetUsageLimitMailTo, refusalSinks and startReaper are each
//     called with escalationBox.
//
// Each check has a positive control built in: the required set must be FOUND,
// so a parser that matched nothing cannot pass.
func TestMainRoutesEveryEscalationThroughEscalationBox(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	isBox := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == escalationBoxIdent
	}

	// Options types whose MailTo must be set (not merely correct when present).
	requiredMailTo := map[string]bool{
		"synthwatch.Options": false,
		"driftwatch.Options": false,
		"credexpiry.Options": false,
	}
	// Calls whose argument at the given index must be escalationBox.
	requiredCalls := map[string]struct {
		arg   int
		found bool
	}{
		"claude.SetUsageLimitMailTo": {arg: 0},
		"refusalSinks":               {arg: 0},
		"startReaper":                {arg: 2},
	}
	fields := 0

	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			typ := exprString(n.Type)
			for _, el := range n.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || !escalationFields[key.Name] {
					continue
				}
				fields++
				if !isBox(kv.Value) {
					t.Errorf("%s: %s.%s = %s, want %s — this escalation ignores [agents] escalation_box",
						fset.Position(kv.Pos()), typ, key.Name, exprString(kv.Value), escalationBoxIdent)
					continue
				}
				if key.Name == "MailTo" {
					if _, ok := requiredMailTo[typ]; ok {
						requiredMailTo[typ] = true
					}
				}
			}
		case *ast.CallExpr:
			name := exprString(n.Fun)
			want, ok := requiredCalls[name]
			if !ok {
				return true
			}
			if len(n.Args) <= want.arg || !isBox(n.Args[want.arg]) {
				t.Errorf("%s: %s is not called with %s as argument %d", fset.Position(n.Pos()), name, escalationBoxIdent, want.arg)
			}
			want.found = true
			requiredCalls[name] = want
		}
		return true
	})

	for typ, ok := range requiredMailTo {
		if !ok {
			t.Errorf("main.go builds no %s{MailTo: %s} — that sender falls back to its package default `human`", typ, escalationBoxIdent)
		}
	}
	for name, c := range requiredCalls {
		if !c.found {
			t.Errorf("main.go never calls %s — its escalation is unrouted", name)
		}
	}
	// The watchers wired before mg-f0ac9 (EscalateTo / HumanBox) number well
	// over ten; a handful would mean the walk stopped seeing them.
	if fields < 10 {
		t.Errorf("found only %d escalation fields in main.go — the AST walk is not seeing the wiring", fields)
	}
}

// exprString renders a selector/identifier expression ("pkg.Name", "name"), and
// "?" for anything else — enough to name a type or a callee.
func exprString(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.BasicLit:
		return e.Value
	}
	return "?"
}

// TestReaperOptionsFollowEscalationBox: the reaper's give-up mail — the sixth
// sender that hard-coded `human` (drellem2/pogo#148) — is routed by the options
// pogod builds, not only by the reaper's own default. The unset arm is the
// positive control that an unconfigured install still mails `human`.
func TestReaperOptionsFollowEscalationBox(t *testing.T) {
	for _, tc := range []struct{ box, want string }{
		{"", "human"},
		{"daniel-phone", "daniel-phone"},
	} {
		agents := config.AgentsConfig{EscalationBox: tc.box}
		opts := reaperOptions(config.ReaperConfig{}, agents.EscalationBoxName())
		if opts.EscalateTo != tc.want {
			t.Errorf("escalation_box=%q: reaper EscalateTo = %q, want %q", tc.box, opts.EscalateTo, tc.want)
		}
		if opts.Kickstart == nil || opts.Mail == nil {
			t.Errorf("escalation_box=%q: reaper options lost the production kickstart/mail seams", tc.box)
		}
	}
	if reaper.DefaultEscalateTo != config.DefaultEscalationBox {
		t.Errorf("reaper.DefaultEscalateTo = %q, want config.DefaultEscalationBox (%q)",
			reaper.DefaultEscalateTo, config.DefaultEscalationBox)
	}
}
