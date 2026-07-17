package gitceiling

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ambient guard's reach, pinned to the property it silently depends on
// (mg-ca7d).
//
// WHY THIS TEST EXISTS. Ensure sets GIT_CEILING_DIRECTORIES on the pogod/pogo
// process, and that one call is supposed to bound git for the whole fleet:
// pogod's own git invocations, the refinery's, gc's, and every agent it spawns.
// The entire reach of that claim rests on one unstated property of this
// codebase — every subprocess inherits this process's environment, because each
// cmd.Env either is left nil or is built by appending onto os.Environ().
//
// Nothing in the compiler enforces that. A future call site written as
//
//	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
//
// is ordinary-looking, reviews clean, and silently drops the ceiling for that
// subprocess — restoring the escaping walk for one call site while every test
// above still passes and the startup log still says the guard is on. That is
// strictly worse than no guard: it is an unguarded call site wearing a guard's
// reassurance.
//
// This is the failure mode the ticket is about. The alternative mechanism —
// explicit -C plus a toplevel assert at each site — loses to exactly this: it
// covers the sites someone remembered, and the next tool that walks up is a new
// ticket. Choosing the ambient mechanism means the inheritance property IS the
// coverage, so it gets a test rather than a comment.
//
// Scope note: this asserts inheritance, not that any particular command is
// git. That is deliberate — a non-git command today may shell out to git
// tomorrow, and the ceiling costs nothing to carry.

// TestSubprocessEnvironmentsInheritTheCeiling walks the module's non-test
// sources and fails on any exec.Cmd.Env that does not root in os.Environ().
//
// WHAT THIS CHECK CANNOT SEE. It matches syntax, not types — resolving types
// would mean adding golang.org/x/tools to the module for one test. So it finds
// an exec.Cmd's Env two ways: a composite literal `exec.Cmd{Env: ...}`, and an
// assignment `x.Env = ...` where x was produced by exec.Command/CommandContext
// in the same function. It does NOT see a *exec.Cmd handed to a helper that
// sets Env on it, nor an environment assembled behind an interface. Every
// subprocess in this repo today is built by one of the two forms it does see;
// if that stops being true, this check narrows silently. It is a ratchet
// against the easy regression, not a proof.
//
// The scoping is deliberate rather than incidental: an earlier, looser version
// of this check matched any struct field named Env and flagged SpawnRequest.Env
// and SpawnAPIRequest.Env — which are not subprocess environments at all, but
// the EXTRA vars that agent.go appends onto os.Environ(). Those are additive and
// correct. A check that cries wolf on correct code gets deleted, and takes the
// real guard with it.
//
// Test files are exempt: a test may legitimately seal a subprocess environment
// to keep the developer's shell from deciding the outcome — gitTopleveled in
// this package does precisely that, and must, or it would not be measuring what
// it claims to measure.
func TestSubprocessEnvironmentsInheritTheCeiling(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	var offenders []string
	inspected := 0
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "bin", "_testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		for _, value := range subprocessEnvs(file) {
			inspected++
			if !rootsInEnviron(value) {
				offenders = append(offenders, fset.Position(value.Pos()).String())
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}

	// A walk that found nothing would report a clean tree just as confidently as
	// a walk that checked every file. pogod spawns subprocesses, so zero here
	// means the walk broke (a moved module root, a changed layout), not that the
	// hazard went away. The floor is >0, not a count: the number of call sites
	// is a measurement and would rot on the next one added.
	if inspected == 0 {
		t.Fatalf("inspected 0 subprocess environments under %s — this check is passing "+
			"vacuously and is not guarding anything", root)
	}
	t.Logf("inspected %d subprocess environment(s); all inherit the ceiling", inspected)

	if len(offenders) > 0 {
		t.Fatalf("these subprocess environments do not root in os.Environ(), so they drop\n"+
			"GIT_CEILING_DIRECTORIES and their git invocations can walk out of POGO_HOME\n"+
			"(see gitceiling.Ensure — the guard is ambient and inherited, not per-call-site):\n  %s\n\n"+
			"Build the environment as append(os.Environ(), ...) instead. If a sealed\n"+
			"environment is genuinely required, carry the ceiling into it explicitly:\n"+
			"  append(env, gitceiling.EnvVar+\"=\"+os.Getenv(gitceiling.EnvVar))",
			strings.Join(offenders, "\n  "))
	}
}

// subprocessEnvs returns every expression assigned to an exec.Cmd's Env field
// in file — and, importantly, nothing else. See the scoping note on
// TestSubprocessEnvironmentsInheritTheCeiling for what it deliberately does not
// match and why.
func subprocessEnvs(file *ast.File) []ast.Expr {
	var envs []ast.Expr

	// `exec.Cmd{Env: ...}` — matched on the literal's type, so a struct that
	// merely has an Env field cannot be mistaken for a subprocess.
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isExecCmdType(lit.Type) {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Env" {
				envs = append(envs, kv.Value)
			}
		}
		return true
	})

	// `x.Env = ...` where x came from exec.Command(...). Scoped per function so
	// a local named cmd in one function cannot vouch for a same-named local in
	// another.
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		cmds := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range assign.Rhs {
				if i >= len(assign.Lhs) || !isExecCommandCall(rhs) {
					continue
				}
				if id, ok := assign.Lhs[i].(*ast.Ident); ok {
					cmds[id.Name] = true
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Env" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && cmds[id.Name] {
				envs = append(envs, assign.Rhs[0])
			}
			return true
		})
		return true
	})

	return envs
}

// isExecCmdType matches `exec.Cmd` and `&exec.Cmd` composite-literal types.
func isExecCmdType(e ast.Expr) bool {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Cmd" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "exec"
}

// isExecCommandCall matches exec.Command(...) and exec.CommandContext(...).
func isExecCommandCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "exec"
}

// rootsInEnviron reports whether an environment expression ultimately derives
// from the current process's environment. It accepts:
//
//	os.Environ()                 the base case
//	append(<rooted>, ...)        any depth of appending onto one
//	cmd.Env / x.Env              appending onto an env already set (merge.go
//	                             does this to add the git identity)
//	nil                          os/exec's own "inherit the parent" signal
//
// An identifier it cannot follow is NOT accepted. This test would rather be
// loud about an environment it cannot vouch for than vouch for one it did not
// actually check — the same reasoning as mg-8f09: unknown is never clean.
func rootsInEnviron(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.CallExpr:
		fn, ok := v.Fun.(*ast.Ident)
		if ok && fn.Name == "append" && len(v.Args) > 0 {
			return rootsInEnviron(v.Args[0])
		}
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "os" && sel.Sel.Name == "Environ"
	case *ast.SelectorExpr:
		// `cmd.Env` on the right-hand side: appending onto an environment that
		// an earlier assignment already had to satisfy this same rule.
		return v.Sel.Name == "Env"
	case *ast.Ident:
		// os/exec documents a nil Env as "inherit the parent's environment",
		// which is exactly what the guard needs.
		return v.Name == "nil"
	default:
		return false
	}
}

// TestInheritCheckCatchesASealedEnvironment is the control: it proves the check
// above can FAIL. A guard demonstrated only in its passing state is not
// evidence — it would pass just as quietly if the AST matching were broken and
// it inspected nothing at all.
func TestInheritCheckCatchesASealedEnvironment(t *testing.T) {
	const sealed = `package p
import "os/exec"
func f() {
	cmd := exec.Command("git", "status")
	cmd.Env = []string{"PATH=/usr/bin"}
}
func g() {
	c := &exec.Cmd{Path: "git", Env: []string{"PATH=/usr/bin"}}
	_ = c
}`
	envs := parseEnvs(t, sealed)
	if len(envs) != 2 {
		t.Fatalf("the matcher found %d subprocess environments in the fixture, want 2 "+
			"(the assignment form and the composite-literal form)", len(envs))
	}
	for _, env := range envs {
		if rootsInEnviron(env) {
			t.Fatal("a sealed []string{} environment was NOT flagged — the check cannot fail, so its passing means nothing")
		}
	}
}

// TestInheritCheckIgnoresNonSubprocessEnvFields pins the false positive that the
// first version of this check produced. SpawnRequest.Env and SpawnAPIRequest.Env
// are extra vars that agent.go appends ONTO os.Environ() — they are supposed to
// be sealed lists, and flagging them would make this check wrong about correct
// code.
func TestInheritCheckIgnoresNonSubprocessEnvFields(t *testing.T) {
	const requestStructs = `package p
func f() {
	_ = SpawnRequest{Name: "cat", Env: []string{"POGO_X=1"}}
	req := SpawnAPIRequest{}
	req.Env = []string{"POGO_Y=2"}
}`
	if envs := parseEnvs(t, requestStructs); len(envs) != 0 {
		t.Fatalf("flagged %d non-subprocess Env field(s); these carry extra vars for a "+
			"process env that is built from os.Environ() elsewhere and must not be flagged", len(envs))
	}
}

func parseEnvs(t *testing.T, src string) []ast.Expr {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return subprocessEnvs(file)
}

// TestInheritCheckAcceptsTheRealPatterns is the other half of the control: the
// forms this repo actually uses must pass, or the check would be noise and get
// deleted the first time it blocked a legitimate diff.
func TestInheritCheckAcceptsTheRealPatterns(t *testing.T) {
	accepted := map[string]string{
		"plain inherit":     `os.Environ()`,
		"one append":        `append(os.Environ(), "POGO_REFINERY=1")`,
		"variadic append":   `append(os.Environ(), append(injected, req.Env...)...)`,
		"append onto a set": `append(cmd.Env, gitIdentityEnv()...)`,
		"explicit nil":      `nil`,
	}
	for name, src := range accepted {
		t.Run(name, func(t *testing.T) {
			expr, err := parser.ParseExpr(src)
			if err != nil {
				t.Fatal(err)
			}
			if !rootsInEnviron(expr) {
				t.Fatalf("%s is a legitimate inheriting pattern but was flagged", src)
			}
		})
	}
}
