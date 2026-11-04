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

// nudgeAllowed are the places that type into an agent's terminal WITHOUT the
// nudge seam, keyed by file ("path") or by one function in it ("path:Func"),
// each with why. Two kinds belong here:
//
//   - nudge TRANSPORTS: the function a nudge alert's seam call is handed, as
//     alerts.Transport is for mail. The seam has already muted or routed the
//     nudge by the time they run;
//   - nudges that are NOT alerts (drellem2/pogo#262, pm-pogo ruling): they
//     deliver something else — a mail pointer, a prompt, a keystroke — and
//     carry no news of pogod's, so there is nothing to mute or route.
//
// Anything else that calls a nudge primitive is a nudge nobody can mute.
var nudgeAllowed = map[string]nudgeAllow{
	"internal/agent/nudge.go":                              {why: "the nudge primitives themselves"},
	"internal/agent/wakepolicy.go":                         {why: "the nudge primitives themselves"},
	"internal/agent/startverify.go:verifyStartAndRenudge":  {why: "not an alert: the kickoff prompt's re-sent Enter", delivery: AgentKickoff},
	"internal/agent/agent.go:Spawn":                        {why: "not an alert: the kickoff prompt", delivery: AgentKickoff},
	"internal/agent/agent.go:respawn":                      {why: "not an alert: the kickoff prompt", delivery: AgentKickoff},
	"internal/agent/api.go:handleNudge":                    {why: "not an alert: an operator's own `pogo nudge`", delivery: OperatorNudge},
	"cmd/pogod/wakewatchwire.go:wakeWatchNudge":            {why: "not an alert: the transport of wake-watch's mail-arrival pointers", delivery: WakeWatchPointer},
	"internal/wakewatch/wakewatch.go:sendThen":             {why: "not an alert: mail-arrival pointers (the mail is the message)", delivery: WakeWatchPointer},
	"internal/midsessionwedge/source.go:RegistryRecover":   {why: "not an alert: the mid-session-wedge bare submit", delivery: MidSessionWedgeSubmit},
	"internal/scheduler/deliverer.go:DeliverOutcome":       {why: "not an alert: a scheduler delivery", delivery: SchedulerNudgeDelivery},
	"internal/scheduler/deliverer.go:nudgeAgent":           {why: "not an alert: a scheduler delivery", delivery: SchedulerNudgeDelivery},
	"cmd/pogo/main.go:main":                                {why: "not an alert: the operator CLI's `pogo nudge` and `pogo doctor <message>` (client.NudgeRunning)", delivery: OperatorNudge},
	"internal/stallwatch/stallwatch.go:deliver":            {why: "transport: handed to each stall-watch category's alerts.NudgeVia"},
	"cmd/pogod/main.go:newStallNudgerWithTimeoutAndDamper": {why: "transport: the stall-watch nudger (alerts.NudgeVia in internal/stallwatch)"},
	"cmd/pogod/main.go:wakeConditionAddressee":             {why: "transport: the condition wake (alerts.Nudge in cmd/pogod/conditionnotify.go)"},
	"cmd/pogod/crewresetwire.go:crewResetNudge":            {why: "transport: the crew-reset pointer (alerts.Nudge in internal/crewreset)"},
}

// nudgeAllow is one nudgeAllowed entry. delivery names the declared delivery
// mechanic a non-alert entry types, so `pogo alerts --all` lists every one of
// them (TestNonAlertNudgesAreListedAsDeliveries); it is empty for the
// primitives and for transports, which carry an alert's name at the seam.
type nudgeAllow struct {
	why      string
	delivery Name
}

// nudgeTransports are named functions that type a nudge when called with
// (to, text): pogod's nudge transports and the client's NudgeRunning. A seam
// call is handed them as a value; CALLING one directly is a nudge nobody can
// mute. Matched with any argument count.
var nudgeTransports = map[string]bool{"wakeConditionAddressee": true, "NudgeRunning": true}

// nudgeTransportMakers build a nudge transport. Wiring hands the result to a
// watcher; invoking the result on the spot — crewResetNudge(reg)(to, text) —
// is a direct nudge.
var nudgeTransportMakers = map[string]bool{
	"crewResetNudge": true, "wakeWatchNudge": true,
	"newStallNudger": true, "newStallNudgerWithTimeoutAndDamper": true,
}

// nudgeCallees are the names a nudge is typed through: the Agent methods and
// the injected transports watchers hold. A zero-argument call is not a nudge
// (the refinery's r.wake() pokes its own queue loop).
var nudgeCallees = map[string]bool{
	"Nudge": true, "NudgeWake": true, "NudgeWithMode": true, "NudgeWithModeCorrelated": true,
	"nudge": true, "nudgeAgent": true, "wake": true,
}

// seamFuncs are the alerts functions a send site names its alert to.
var seamFuncs = map[string]bool{"Mail": true, "MailOutput": true, "Withhold": true, "Nudge": true, "NudgeVia": true}

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
// seam) but not from the SendMGMail rule. allowed lists mail probes;
// nudgeOK lists nudge transports and non-alert nudges (see nudgeAllowed).
func scan(root, self string, allowed map[string]string, nudgeOK map[string]nudgeAllow) (scanResult, error) {
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
		enclosing := func(n ast.Node) string {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && n.Pos() >= fd.Pos() && n.End() <= fd.End() {
					return fd.Name.Name
				}
			}
			return ""
		}
		nudgeExempt := func(n ast.Node) bool {
			if _, ok := nudgeOK[rel]; ok {
				return true
			}
			_, ok := nudgeOK[rel+":"+enclosing(n)]
			return ok
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
				if inner, ok := x.Fun.(*ast.CallExpr); ok && nudgeTransportMakers[calleeName(inner)] && !nudgeExempt(x) {
					res.violations = append(res.violations, at(x)+": "+calleeName(inner)+"(...)(...) invokes a nudge transport directly — hand it to alerts.Nudge")
				}
				if nudgeTransports[calleeName(x)] && !insideTransport(x) && !nudgeExempt(x) {
					res.violations = append(res.violations, at(x)+": "+calleeName(x)+"(...) calls a nudge transport directly — hand it to alerts.Nudge(alerts.<Name>, "+calleeName(x)+", ...)")
				} else if nudgeCallees[calleeName(x)] && len(x.Args) > 0 && !insideTransport(x) && !nudgeExempt(x) {
					res.violations = append(res.violations, at(x)+": "+calleeName(x)+"(...) types into a terminal outside alerts.Nudge — name the alert: alerts.Nudge(alerts.<Name>, ...)")
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

// isSeamCall reports a call to alerts.Mail, alerts.MailOutput,
// alerts.Nudge, alerts.NudgeVia or alerts.Withhold — or, inside the alerts
// package, to those unqualified.
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
	res, err := scan(root, "internal/alerts", probeAllowed, nudgeAllowed)
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
	res, err := scan(moduleRoot(t), "internal/alerts", probeAllowed, nudgeAllowed)
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
	res, err := scan(moduleRoot(t), "internal/alerts", probeAllowed, nudgeAllowed)
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
		if res.seamCalls[ident] == 0 && !deliveryNudgeAllowed(name) {
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

func f(x w, name string, a *agent.Agent, n nudger) {
	_ = client.SendMGMail("to", "from", "s", "b")
	_ = exec.Command("mg", "mail", "send", "to").Run()
	_ = []string{"--root", "r", "mail", "send", "to"}
	_ = x.mail("to", "from", "s", "b")
	_ = alerts.Transport("to", "from", "s", "b")
	_ = alerts.Mail(name, x.mail, "to", "from", "s", "b")
	_ = a.NudgeWake("hi", 0, 0, "")
	_ = n.nudge("to", "hi")
	_, _ = alerts.Nudge(name, n.nudge, "to", "s", "hi")
	_ = wakeConditionAddressee("mayor", "news")
	_ = client.NudgeRunning("mayor", "news", nil)
	_ = crewResetNudge(nil)("mayor", "news")
}
`)
	write("good/good.go", `package good

import (
	"os/exec"
	"x/alerts"
)

type w struct{ mail func(a, b, c, d string) error }

func f(x w, n nudger) {
	_, _ = alerts.Nudge(alerts.CrewResetPointer, n.nudge, "to", "s", "hi")
	_, _ = alerts.Nudge(alerts.ConditionWake, wakeConditionAddressee, "to", "s", "hi")
	n.nudge = crewResetNudge(nil)
	_, _, _ = alerts.NudgeVia(alerts.StallWatchUnreadMail, "to", "s", func(to string) (int, error) {
		return 0, n.nudge(to, "hi")
	})
	_ = alerts.Mail(alerts.AckWatchFinding, x.mail, "to", "from", "s", "b")
	_ = alerts.Mail(alerts.RefusalWatchAlarm, func(to, from, s, b string) error {
		return exec.Command("mg", "mail", "send", to).Run()
	}, "to", "from", "s", "b")
	_, _ = alerts.MailOutput(alerts.OrphanPolecat, "to", "from", "s", "b")
	_ = exec.Command("mg", "mail", "list").Run()
}
`)
	write("transport/t.go", `package transport

func Typer(a *agent.Agent) error { return a.Nudge("hi") }

func Other(a *agent.Agent) error { return a.Nudge("hi") }
`)
	write("delivery/d.go", `package delivery

func f(a *agent.Agent) error { return a.Nudge("") }
`)
	res, err := scan(root, "alerts", nil, map[string]nudgeAllow{
		"transport/t.go:Typer": {why: "transport"},
		"delivery/d.go":        {why: "not an alert", delivery: AgentKickoff},
	})
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
		"bad/bad.go:18: NudgeWake(...) types into a terminal outside alerts.Nudge",
		"bad/bad.go:19: nudge(...) types into a terminal outside alerts.Nudge",
		"bad/bad.go:20: the alert name must be a declared",
		"bad/bad.go:21: wakeConditionAddressee(...) calls a nudge transport directly",
		"bad/bad.go:22: NudgeRunning(...) calls a nudge transport directly",
		"bad/bad.go:23: crewResetNudge(...)(...) invokes a nudge transport directly",
		"transport/t.go:5: Nudge(...) types into a terminal outside alerts.Nudge",
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
		if strings.HasPrefix(v, "good/") || strings.HasPrefix(v, "delivery/") || strings.HasPrefix(v, "transport/t.go:3") {
			t.Errorf("guard flagged a sanctioned send: %s", v)
		}
	}
	if res.seamCalls["AckWatchFinding"] != 1 || res.seamCalls["RefusalWatchAlarm"] != 1 || res.seamCalls["OrphanPolecat"] != 1 ||
		res.seamCalls["CrewResetPointer"] != 1 || res.seamCalls["StallWatchUnreadMail"] != 1 {
		t.Errorf("seam calls not counted: %v", res.seamCalls)
	}
}

// deliveryNudgeAllowed reports a delivery-mechanic nudge named by a
// nudgeAllowed entry: such a name has no seam call by design — it is typed by
// the allowlisted code, not through the seam.
func deliveryNudgeAllowed(name Name) bool {
	d, ok := Lookup(name)
	if !ok || d.Class != ClassDelivery || d.Channel() != ChannelNudge {
		return false
	}
	for _, a := range nudgeAllowed {
		if a.delivery == name {
			return true
		}
	}
	return false
}

// TestNonAlertNudgesAreListedAsDeliveries ties the listing to the guard:
// every non-alert nudge the guard lets through is declared as a delivery
// mechanic (so `pogo alerts --all` shows it), every declared delivery nudge
// is typed by some allowlisted code, and none of them is listed as an alert.
func TestNonAlertNudgesAreListedAsDeliveries(t *testing.T) {
	used := map[Name]bool{}
	for key, a := range nudgeAllowed {
		if strings.HasPrefix(a.why, "not an alert") && a.delivery == "" {
			t.Errorf("nudgeAllowed[%q] is a non-alert nudge with no declared delivery name", key)
		}
		if a.delivery == "" {
			continue
		}
		d, ok := Lookup(a.delivery)
		if !ok || d.Class != ClassDelivery || d.Channel() != ChannelNudge {
			t.Errorf("nudgeAllowed[%q] names %q, which is not a declared delivery nudge (declared=%v class=%q)", key, a.delivery, ok, d.Class)
		}
		used[a.delivery] = true
	}
	for _, d := range All() {
		if d.Class == ClassDelivery && d.Channel() == ChannelNudge && !used[d.Name] {
			t.Errorf("%s is a declared delivery nudge that no allowlisted code types", d.Name)
		}
	}
	if len(used) < 5 {
		t.Fatalf("found %d delivery nudges, want at least 5", len(used))
	}
	// Listed under --all only, as deliveries.
	listed := map[Name]Row{}
	for _, r := range Rows(Settings{Coordinator: "mayor"}, true) {
		listed[r.Name] = r
	}
	for _, r := range Rows(Settings{Coordinator: "mayor"}, false) {
		if used[r.Name] {
			t.Errorf("%s is listed without --all, as if it were an alert", r.Name)
		}
	}
	for n := range used {
		r, ok := listed[n]
		if !ok || r.Class != ClassDelivery || r.Channel != ChannelNudge {
			t.Errorf("--all: %s listed=%v %+v, want class delivery, channel nudge", n, ok, r)
		}
	}
}
