package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// The "What runs by default" table in docs/CONFIGURATION.md (drellem2/pogo#199)
// answers the first question a downstream operator asks when adopting a newer
// pogod: what turns on when my config.toml does not mention it? A table that
// drifts from the code answers that question wrongly, and the reader acts on
// it before the first start. So these tests pin it three ways:
//
//  1. to the PARSER — every section parseConfigFileInto reads has a row, every
//     row names a section it reads, and every key a row names is a key that
//     section's switch accepts;
//  2. to Config — every struct-typed Config field maps to a section with a row,
//     so a new section cannot ship unlisted;
//  3. to Load() — each row's Default is what Load() returns for a config file
//     that does not name the key.
//
// The Kind column (observes / ACTS) is not derivable from code and is not
// pinned here; each ACTS row cites its code path for review instead.

const defaultsTableDoc = "../../docs/CONFIGURATION.md"

type defaultsRow struct {
	line    int
	section string
	key     string // "—" when the section has no switch
	def     string
	kind    string
	what    string
}

// configFieldSection maps every struct-typed Config field to the section that
// configures it. TestDefaultsTableCoversEveryConfigSection fails when a field is
// missing here — which is the point: adding a section means adding a row.
var configFieldSection = map[string]string{
	"Refinery":            "refinery",
	"Agents":              "agents",
	"Heartbeat":           "heartbeat",
	"GitGC":               "gitgc",
	"StallWatch":          "stall_watch",
	"Reaper":              "reaper",
	"Reconcile":           "reconcile",
	"DriftWatch":          "drift_watch",
	"CredExpiry":          "cred_expiry",
	"GHTeardown":          "gh_teardown",
	"GHIntake":            "gh_intake",
	"CarrierDrift":        "carrier_drift",
	"ReviewDecl":          "review_decl",
	"PromptEdit":          "prompt_edit",
	"PromptStale":         "prompt_stale",
	"AckWatch":            "ack_watch",
	"DeafWatch":           "deaf_watch",
	"HeartWatch":          "heart_watch",
	"BlindWatch":          "blind_watch",
	"AbsentWatch":         "absent_watch",
	"ProgressWatch":       "progress_watch",
	"FirstTurn":           "first_turn",
	"SynthWatch":          "synth_watch",
	"RefusalWatch":        "refusal_watch",
	"TurnWatch":           "turn_watch",
	"WedgeWatch":          "wedge_watch",
	"MidSessionWedge":     "midsession_wedge",
	"DoneReap":            "done_reap",
	"OrchestrationResume": "orchestration_resume",
	"DispatchPairing":     "dispatch_pairing",
	"DispatchCap":         "dispatch",
	"AuditSuccessor":      "audit_successor",
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func inertIfEmpty(n int) string {
	if n == 0 {
		return "inert"
	}
	return "on"
}

// switchDefaults answers the Default column for every switch that is NOT an
// `enabled` key. `enabled` rows are answered generically from the section's
// Config field (see enabledDefault), so only these need spelling out.
var switchDefaults = map[string]func(*Config) string{
	"agents/autostart":                           func(c *Config) string { return onOff(c.Agents.AutoStart) },
	"stall_watch/priority_wake_enabled":          func(c *Config) string { return onOff(c.StallWatch.PriorityWakeEnabled) },
	"stall_watch/blocked_reminder_enabled":       func(c *Config) string { return onOff(c.StallWatch.BlockedReminderEnabled) },
	"stall_watch/indefinite_hold_report_enabled": func(c *Config) string { return onOff(c.StallWatch.IndefiniteHoldReportEnabled) },
	"dispatch/max_polecats_per_repo":             func(c *Config) string { return onOff(c.DispatchCap.Armed()) },
	"dispatch_pairing/repos":                     func(c *Config) string { return inertIfEmpty(len(c.DispatchPairing.Repos)) },
	"audit_successor/repos":                      func(c *Config) string { return inertIfEmpty(len(c.AuditSuccessor.Repos)) },
	"reconcile/mirrors":                          func(c *Config) string { return inertIfEmpty(len(c.Reconcile.Mirrors)) },
}

// enabledDefault reads the Enabled field of the Config section a row names.
func enabledDefault(t *testing.T, c *Config, section string) (string, bool) {
	t.Helper()
	for field, sec := range configFieldSection {
		if sec != section {
			continue
		}
		f := reflect.ValueOf(c).Elem().FieldByName(field).FieldByName("Enabled")
		if !f.IsValid() || f.Kind() != reflect.Bool {
			return "", false
		}
		return onOff(f.Bool()), true
	}
	return "", false
}

func stripTicks(s string) string { return strings.Trim(strings.TrimSpace(s), "`") }

func readDefaultsTable(t *testing.T) (rows []defaultsRow, doc string) {
	t.Helper()
	raw, err := os.ReadFile(defaultsTableDoc)
	if err != nil {
		t.Fatalf("read %s: %v", defaultsTableDoc, err)
	}
	doc = string(raw)
	in := false
	for i, line := range strings.Split(doc, "\n") {
		switch strings.TrimSpace(line) {
		case "<!-- defaults-table:begin -->":
			in = true
			continue
		case "<!-- defaults-table:end -->":
			in = false
			continue
		}
		if !in || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 5 {
			t.Fatalf("%s:%d: want 5 cells, got %d: %q", defaultsTableDoc, i+1, len(cells), line)
		}
		sec := stripTicks(cells[0])
		if sec == "Section" || strings.HasPrefix(sec, "---") {
			continue
		}
		if !strings.HasPrefix(sec, "[") || !strings.HasSuffix(sec, "]") {
			t.Fatalf("%s:%d: section cell %q is not spelled `[name]`", defaultsTableDoc, i+1, cells[0])
		}
		rows = append(rows, defaultsRow{
			line:    i + 1,
			section: strings.Trim(sec, "[]"),
			key:     stripTicks(cells[1]),
			def:     strings.TrimSpace(cells[2]),
			kind:    strings.TrimSpace(cells[3]),
			what:    strings.TrimSpace(cells[4]),
		})
	}
	if len(rows) == 0 {
		t.Fatalf("%s: no rows between the defaults-table markers — the parser found nothing to check", defaultsTableDoc)
	}
	return rows, doc
}

// parserSections returns section -> accepted keys, read from the source of
// parseConfigFileInto: the `switch currentSection` cases, and within each the
// string literals of a `switch key` or a `key == "..."` comparison.
func parserSections(t *testing.T) map[string]map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "config.go", nil, 0)
	if err != nil {
		t.Fatalf("parse config.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "parseConfigFileInto" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("parseConfigFileInto not found in config.go")
	}
	isIdent := func(e ast.Expr, name string) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == name
	}
	lit := func(e ast.Expr) (string, bool) {
		bl, ok := e.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(bl.Value)
		return s, err == nil
	}
	out := map[string]map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || !isIdent(sw.Tag, "currentSection") {
			return true
		}
		for _, st := range sw.Body.List {
			cc := st.(*ast.CaseClause)
			keys := map[string]bool{}
			for _, body := range cc.Body {
				ast.Inspect(body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.SwitchStmt:
						if isIdent(x.Tag, "key") {
							for _, s := range x.Body.List {
								for _, e := range s.(*ast.CaseClause).List {
									if v, ok := lit(e); ok {
										keys[v] = true
									}
								}
							}
						}
					case *ast.BinaryExpr:
						if x.Op == token.EQL && isIdent(x.X, "key") {
							if v, ok := lit(x.Y); ok {
								keys[v] = true
							}
						}
					}
					return true
				})
			}
			for _, e := range cc.List {
				if v, ok := lit(e); ok {
					out[v] = keys
				}
			}
		}
		return false
	})
	if len(out) == 0 {
		t.Fatal("found no `switch currentSection` cases in parseConfigFileInto — the extraction is broken, not the table")
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

// TestDefaultsTableMatchesParser: the table and parseConfigFileInto name the
// same sections, and every key the table names is one the parser accepts —
// `[gitgc]`, not `[git_gc]`, is exactly the spelling a reader copies.
func TestDefaultsTableMatchesParser(t *testing.T) {
	rows, _ := readDefaultsTable(t)
	parsed := parserSections(t)

	// Positive control: sections known to exist must be found, or an empty
	// extraction would pass the set comparison below vacuously.
	for _, want := range []string{"server", "gitgc", "stall_watch", "done_reap"} {
		if _, ok := parsed[want]; !ok {
			t.Fatalf("parser extraction missed [%s]; extracted %v", want, len(parsed))
		}
	}
	if !parsed["gitgc"]["enabled"] || !parsed["synth_watch"]["enabled"] {
		t.Fatal("key extraction missed a known `enabled` key (switch or `key ==` form)")
	}

	inTable := map[string]bool{}
	for _, r := range rows {
		inTable[r.section] = true
		keys, ok := parsed[r.section]
		if !ok {
			t.Errorf("%s:%d: [%s] is not a section parseConfigFileInto reads", defaultsTableDoc, r.line, r.section)
			continue
		}
		if r.key != "—" && !keys[r.key] {
			t.Errorf("%s:%d: [%s] has no key %q; the parser accepts %v", defaultsTableDoc, r.line, r.section, r.key, sortedKeys(keys))
		}
		switch r.kind {
		case "plumbing", "observes", "ACTS":
		default:
			t.Errorf("%s:%d: Kind %q is not plumbing, observes or ACTS", defaultsTableDoc, r.line, r.kind)
		}
	}
	for sec, keys := range parsed {
		if !inTable[sec] {
			t.Errorf("parseConfigFileInto reads [%s] but %s's defaults table has no row for it", sec, defaultsTableDoc)
		}
		// A section with an on/off switch must be listed BY that switch, so the
		// table names the key an operator sets to turn it off.
		// The same goes for every other on/off switch (`*_enabled`, such as
		// [stall_watch]'s sub-switches), so a new one cannot ship unlisted.
		for key := range keys {
			if key != "enabled" && !strings.HasSuffix(key, "_enabled") {
				continue
			}
			found := false
			for _, r := range rows {
				if r.section == sec && r.key == key {
					found = true
				}
			}
			if !found {
				t.Errorf("[%s] has an on/off key %q but no table row with that Key", sec, key)
			}
		}
	}
}

// TestDefaultsTableCoversEveryConfigSection: every struct-typed Config field is
// configured by a section the table lists. A new FooConfig field fails here
// until it is mapped in configFieldSection and given a row.
func TestDefaultsTableCoversEveryConfigSection(t *testing.T) {
	rows, _ := readDefaultsTable(t)
	inTable := map[string]bool{}
	for _, r := range rows {
		inTable[r.section] = true
	}
	typ := reflect.TypeOf(Config{})
	structs := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Struct {
			continue
		}
		structs++
		sec, ok := configFieldSection[f.Name]
		if !ok {
			t.Errorf("Config.%s has no entry in configFieldSection — map it to its config.toml section and add a row to %s's defaults table", f.Name, defaultsTableDoc)
			continue
		}
		if !inTable[sec] {
			t.Errorf("Config.%s is configured by [%s], which has no row in %s's defaults table", f.Name, sec, defaultsTableDoc)
		}
	}
	if structs == 0 {
		t.Fatal("found no struct-typed Config fields — the reflection is broken, not the table")
	}
	for field := range configFieldSection {
		if _, ok := typ.FieldByName(field); !ok {
			t.Errorf("configFieldSection names Config.%s, which does not exist", field)
		}
	}
}

// TestDefaultsTableMatchesLoad: each row's Default is what Load() returns when a
// config file exists and does not name the key — the reporter's "our file plus
// your defaults". The file must exist: with no file at all pogod also skips crew
// auto-start and stall-watch arming, which the prose above the table says.
func TestDefaultsTableMatchesLoad(t *testing.T) {
	rows, _ := readDefaultsTable(t)

	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("POGO_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("POGO_AGENT_AUTOSTART", "")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# names no section\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	if cfg.Source == "" {
		t.Fatal("Load() read no config file — the sandbox is wrong, and the defaults below would be the no-file case")
	}

	for _, r := range rows {
		var want string
		switch {
		case r.key == "—":
			want = "—"
		case r.key == "enabled":
			var ok bool
			if want, ok = enabledDefault(t, cfg, r.section); !ok {
				t.Errorf("%s:%d: [%s] has Key `enabled` but no Config field with an Enabled bool maps to it in configFieldSection", defaultsTableDoc, r.line, r.section)
				continue
			}
		default:
			fn, ok := switchDefaults[r.section+"/"+r.key]
			if !ok {
				t.Errorf("%s:%d: no expectation for [%s] %s — add it to switchDefaults", defaultsTableDoc, r.line, r.section, r.key)
				continue
			}
			want = fn(cfg)
		}
		if r.def != want {
			t.Errorf("%s:%d: [%s] %s: table says Default %q, Load() gives %q", defaultsTableDoc, r.line, r.section, r.key, r.def, want)
		}
	}

	// The dispatch row states the cap as a number; keep that number honest.
	for _, r := range rows {
		if r.section == "dispatch" {
			wantCap := "`max_polecats_per_repo = " + strconv.Itoa(cfg.DispatchCap.MaxPolecatsPerRepo) + "`"
			if !strings.Contains(r.what, wantCap) {
				t.Errorf("%s:%d: [dispatch] row does not state the default %s", defaultsTableDoc, r.line, wantCap)
			}
		}
	}
}

// githubSlug approximates GitHub's heading anchors: lowercase, drop punctuation
// other than '-' and '_', spaces to '-'. Em dashes are dropped, which is why
// "Dispatch cap — how" becomes "dispatch-cap--how".
func githubSlug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestDefaultsTableAnchorsResolve: every in-page link in the table points at a
// heading that exists, so "See [X](#x)" never lands a reader at the top.
func TestDefaultsTableAnchorsResolve(t *testing.T) {
	rows, doc := readDefaultsTable(t)
	slugs := map[string]bool{}
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "#") {
			slugs[githubSlug(strings.TrimSpace(strings.TrimLeft(line, "#")))] = true
		}
	}
	if !slugs["stall-watcher"] {
		t.Fatal("slug extraction missed the known heading \"Stall watcher\"")
	}
	links := 0
	for _, r := range rows {
		rest := r.what
		for {
			i := strings.Index(rest, "](#")
			if i < 0 {
				break
			}
			rest = rest[i+3:]
			j := strings.Index(rest, ")")
			if j < 0 {
				break
			}
			links++
			if anchor := rest[:j]; !slugs[anchor] {
				t.Errorf("%s:%d: link #%s matches no heading", defaultsTableDoc, r.line, anchor)
			}
			rest = rest[j:]
		}
	}
	if links == 0 {
		t.Fatal("found no in-page links in the table — the link scan is broken")
	}
}
