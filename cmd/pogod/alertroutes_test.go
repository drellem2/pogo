package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
)

// drellem2/pogo#262 slice 3: route, notify_to as its alias, load validation.

func routesCfg() *config.Config {
	cfg := defaultsCfg()
	cfg.Agents.Coordinator = "mayor"
	cfg.Agents.EscalationBox = "human"
	return cfg
}

func TestAlertRoutes_DefaultRoutesNothing(t *testing.T) {
	sandboxHome(t)
	tbl := resolveAlertRoutes(config.Load(), func(string) bool { return false })
	if len(tbl.Routes) != 0 || len(tbl.Warnings) != 0 || len(tbl.Invalid) != 0 {
		t.Errorf("an unconfigured install: %+v", tbl)
	}
	if line := routedAlertsLine(tbl); line != "" {
		t.Errorf("startup line %q on an install that routes nothing", line)
	}
}

// The route #259 part 2 waits on: stranded work and MERGED notices to the
// escalation box, written as the role so a re-pointed box follows.
func TestAlertRoutes_RoleTokensAndPrimary(t *testing.T) {
	cfg := routesCfg()
	cfg.Alerts = map[string]config.AlertConfig{
		"stranded_work.branch":   {Route: "$escalation_box", RouteSet: true},
		"refinery.merged":        {Route: "$escalation_box", RouteSet: true},
		"cred_expiry.warning":    {Route: "$coordinator", RouteSet: true},
		"filer_notify.completed": {Route: "pm-pogo", RouteSet: true},
	}
	tbl := resolveAlertRoutes(cfg, nil)
	want := map[alerts.Name]alerts.Route{
		alerts.StrandedWorkBranch:   {To: "human", Match: "mayor", Fallback: "mayor", Source: `[alerts."stranded_work.branch"] route = "$escalation_box"`},
		alerts.RefineryMerged:       {To: "human", Match: "mayor", Fallback: "mayor", Source: `[alerts."refinery.merged"] route = "$escalation_box"`},
		alerts.CredExpiryWarning:    {To: "mayor", Match: "human", Fallback: "human", Source: `[alerts."cred_expiry.warning"] route = "$coordinator"`},
		alerts.FilerNotifyCompleted: {To: "pm-pogo", Source: `[alerts."filer_notify.completed"] route = "pm-pogo"`},
	}
	if len(tbl.Routes) != len(want) {
		t.Errorf("routes %+v", tbl.Routes)
	}
	for n, w := range want {
		if tbl.Routes[n] != w {
			t.Errorf("%s = %+v, want %+v", n, tbl.Routes[n], w)
		}
	}
	if len(tbl.Warnings) != 0 {
		t.Errorf("warnings %v", tbl.Warnings)
	}
	line := routedAlertsLine(tbl)
	for _, w := range []string{"4 alert(s) routed", "stranded_work.branch -> human", "refinery.merged -> human"} {
		if !strings.Contains(line, w) {
			t.Errorf("startup line %q lacks %q", line, w)
		}
	}
}

// notify_to is an alias of route: it is installed as one (so an unknown box
// falls back to the coordinator), an [alerts] route wins over it with a
// warning, and route = "" clears it.
func TestAlertRoutes_NotifyToIsAnAlias(t *testing.T) {
	cfg := routesCfg()
	cfg.AckWatch.NotifyTo = "pm-pogo"
	cfg.AbsentWatch.NotifyTo = "pm-pogo"
	cfg.DeafWatch.NotifyTo = "mayor" // the default: no alias
	cfg.Alerts = map[string]config.AlertConfig{
		"absent_watch.finding": {Route: "ops", RouteSet: true},
		"absent_watch.cleared": {Route: "", RouteSet: true},
	}
	tbl := resolveAlertRoutes(cfg, nil)

	if r := tbl.Routes[alerts.AckWatchFinding]; r != (alerts.Route{To: "pm-pogo", Match: "pm-pogo", Fallback: "mayor", Source: "[ack_watch] notify_to"}) {
		t.Errorf("ack_watch.finding alias = %+v", r)
	}
	if r := tbl.Routes[alerts.AbsentWatchDeclared]; r.To != "pm-pogo" || r.Source != "[absent_watch] notify_to" {
		t.Errorf("every name the watcher sends takes the alias: %+v", r)
	}
	if r := tbl.Routes[alerts.AbsentWatchFinding]; r.To != "ops" || r.Match != "pm-pogo" || r.Fallback != "mayor" {
		t.Errorf("[alerts] route over notify_to = %+v (it must match the box the watcher addresses)", r)
	}
	if _, ok := tbl.Routes[alerts.AbsentWatchCleared]; ok {
		t.Error(`route = "" did not clear the alias`)
	}
	if _, ok := tbl.Routes[alerts.DeafWatchFinding]; ok {
		t.Error("a notify_to equal to the coordinator was installed as a route")
	}
	joined := strings.Join(tbl.Warnings, "\n")
	for _, w := range []string{
		`[alerts."absent_watch.finding"] route = "ops" overrides [absent_watch] notify_to (pm-pogo)`,
		`[alerts."absent_watch.cleared"] route = "" overrides [absent_watch] notify_to`,
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings lack %q:\n%s", w, joined)
		}
	}
	// Aliases are not repeated on the startup line; only [alerts] routes are.
	if line := routedAlertsLine(tbl); line != "pogod: 1 alert(s) routed by [alerts]: absent_watch.finding -> ops" {
		t.Errorf("startup line %q", line)
	}
}

func TestAlertRoutes_RefusedNamesAndValues(t *testing.T) {
	cfg := routesCfg()
	cfg.Alerts = map[string]config.AlertConfig{
		"nope.never":                      {Route: "x", RouteSet: true},
		"scheduler.delivery":              {Route: "x", RouteSet: true},
		"stall_watch.blocked_reminder":    {Route: "x", RouteSet: true},
		"condition.scheduler_load_failed": {Route: "x", RouteSet: true},
		"refinery.merged":                 {Route: "$mayor", RouteSet: true},
		"refinery.merge_failed":           {Route: `["a", "b"]`, RouteSet: true},
		"ack_watch.finding":               {Route: `"pm"  # comment`, RouteSet: true},
		"deaf_watch.finding":              {Mute: true, MuteSet: true}, // no route key
	}
	tbl := resolveAlertRoutes(cfg, nil)
	if len(tbl.Routes) != 0 {
		t.Errorf("routes installed from refused entries: %+v", tbl.Routes)
	}
	joined := strings.Join(tbl.Warnings, "\n")
	for _, w := range []string{
		`[alerts."nope.never"] route names no alert`,
		`[alerts."scheduler.delivery"] route is a delivery mechanic`,
		`[alerts."stall_watch.blocked_reminder"] route is a nudge`,
		`[alerts."condition.scheduler_load_failed"] route cannot be set on one condition row`,
		`[alerts."refinery.merged"] route = "$mayor" is not a role`,
		`[alerts."refinery.merge_failed"] route = "[\"a\", \"b\"]" is not one mailbox name`,
		`[alerts."ack_watch.finding"] route = "\"pm\"  # comment" is not one mailbox name`,
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings lack %q:\n%s", w, joined)
		}
	}
	if len(tbl.Warnings) != 7 {
		t.Errorf("%d warnings, want 7:\n%s", len(tbl.Warnings), joined)
	}
}

// An unknown box is reported at load — a warning and an event — and the route
// stays installed, so the seam falls back at send rather than the route
// silently vanishing.
func TestAlertRoutes_UnknownBoxReportedAtLoad(t *testing.T) {
	cfg := routesCfg()
	cfg.AckWatch.NotifyTo = "pm-gone"
	cfg.Alerts = map[string]config.AlertConfig{
		"refinery.merged":      {Route: "esc-typo", RouteSet: true},
		"stranded_work.branch": {Route: "human", RouteSet: true},
	}
	probed := map[string]int{}
	tbl := resolveAlertRoutes(cfg, func(box string) bool { probed[box]++; return box == "human" })

	if _, ok := tbl.Routes[alerts.RefineryMerged]; !ok {
		t.Error("an unknown box's route was dropped; it must stay installed and fall back at send")
	}
	if tbl.Invalid[alerts.RefineryMerged] == "" || tbl.Invalid[alerts.AckWatchFinding] == "" {
		t.Errorf("invalid = %v", tbl.Invalid)
	}
	if _, bad := tbl.Invalid[alerts.StrandedWorkBranch]; bad {
		t.Error("control: a known box was reported invalid")
	}
	if probed["pm-gone"] != 1 {
		t.Errorf("one box probed %d times; probe once per box", probed["pm-gone"])
	}
	joined := strings.Join(tbl.Warnings, "\n")
	for _, w := range []string{
		`[alerts."refinery.merged"] route = "esc-typo" routes refinery.merged to esc-typo, but there is no mailbox named esc-typo`,
		"is sent to mayor instead",
		"[ack_watch] notify_to routes ack_watch.finding to pm-gone",
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings lack %q:\n%s", w, joined)
		}
	}
	evs := routeInvalidAtLoadEvents(tbl)
	found := false
	for _, e := range evs {
		if e.EventType != alerts.EventRouteInvalid || e.Details["phase"] != "load" {
			t.Errorf("event %+v", e)
		}
		if e.Details["alert"] == "refinery.merged" && e.Details["route"] == "esc-typo" && e.Details["fallback_to"] == "mayor" {
			found = true
		}
	}
	if !found || len(evs) != len(tbl.Invalid) {
		t.Errorf("events %+v", evs)
	}
	if line := routedAlertsLine(tbl); !strings.Contains(line, "refinery.merged -> esc-typo (UNKNOWN BOX") {
		t.Errorf("startup line %q", line)
	}
}

// The listing reads the route table the seam enforces.
func TestAlertSettings_ListsRoutes(t *testing.T) {
	state := sandboxHome(t)
	body := "[agents]\ncoordinator = \"mayor\"\nescalation_box = \"human\"\n\n" +
		"[alerts.\"stranded_work.branch\"]\nroute = \"$escalation_box\"\n"
	if err := os.WriteFile(filepath.Join(state, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := alertSettingsFrom(config.Load())
	for _, r := range alerts.Rows(s, false) {
		if r.Name != alerts.StrandedWorkBranch {
			continue
		}
		if r.Route != "human" || !strings.HasPrefix(r.Effective, "human and ") || !strings.Contains(r.Source, "route") {
			t.Errorf("row %+v", r)
		}
		return
	}
	t.Fatal("stranded_work.branch not listed")
}

// mg-4d76c advisory #3: `condition.<typo>` used to be accepted as a mute name
// — muting nothing, warning nothing, and listed as muted. It is now refused
// with a warning, as is a [conditions] disable entry naming no row.
func TestAlertMutes_UnknownConditionRowIsRefused(t *testing.T) {
	cfg := defaultsCfg()
	cfg.Conditions.Disable = []string{"scheduler_load_failedd", "autostart_failed", "restart_failed:mayor"}
	cfg.Alerts = map[string]config.AlertConfig{
		"condition.schedular_load_failed":   {Mute: true, MuteSet: true},
		"condition.role_pin_failed":         {Mute: true, MuteSet: true},
		"condition.autostart_failed:doctor": {Mute: true, MuteSet: true},
		"condition.unknown_provider":        {Mute: true, MuteSet: true},
		"condition.pogod_log_not_written":   {Mute: true, MuteSet: true},
	}
	tbl := resolveAlertMutes(cfg)

	for _, n := range []alerts.Name{"condition.schedular_load_failed", "condition.scheduler_load_failedd"} {
		if _, muted := tbl.Muted[n]; muted {
			t.Errorf("%s (a typo) is listed as muted", n)
		}
	}
	// Positive controls: exact ids, prefix stems and prefixed subjects.
	for _, n := range []alerts.Name{"condition.role_pin_failed", "condition.autostart_failed:doctor",
		"condition.unknown_provider", "condition.pogod_log_not_written",
		"condition.autostart_failed", "condition.restart_failed:mayor"} {
		if tbl.Muted[n] == "" {
			t.Errorf("%s (a real row) was not muted", n)
		}
	}
	joined := strings.Join(tbl.Warnings, "\n")
	for _, w := range []string{
		`[alerts."condition.schedular_load_failed"] names no condition row ("schedular_load_failed" is not a condition id`,
		"[conditions] disable lists scheduler_load_failedd, which names no condition row",
		"scheduler_load_failed, ", // the hint lists the real ids
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings lack %q:\n%s", w, joined)
		}
	}
	if len(tbl.Warnings) != 2 {
		t.Errorf("%d warnings, want 2:\n%s", len(tbl.Warnings), joined)
	}
}

// conditionIDs / conditionIDPrefixes must list every condition id constant,
// or a real row would be refused as a typo. Read the constants from source so
// a new row cannot be added without this test noticing.
func TestConditionIDsListEveryRowConstant(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, id := range conditionIDs {
		listed[id] = true
	}
	for _, p := range conditionIDPrefixes {
		listed[p] = true
	}
	n := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				g, ok := decl.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, spec := range g.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if !(strings.HasPrefix(name.Name, "rowA") || strings.HasSuffix(name.Name, "ConditionID")) || i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, _ := strconv.Unquote(lit.Value)
						n++
						if !listed[v] {
							t.Errorf("condition id constant %s = %q is not in conditionIDs / conditionIDPrefixes", name.Name, v)
						}
					}
				}
			}
		}
	}
	if n < len(conditionIDs)+len(conditionIDPrefixes) {
		t.Errorf("found only %d condition id constants; the scan is not finding them", n)
	}
}
