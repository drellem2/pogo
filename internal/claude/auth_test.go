package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// The shapes below were measured from claude 2.1.283 (mg-ecc8, re-measured for
// mg-b968): a fresh HOME exits 1 with loggedIn:false; a logged-in HOME, and an
// API-key-only HOME, exit 0 with loggedIn:true.
const (
	freshAuthStatus    = `{"loggedIn": false, "authMethod": "none", "apiProvider": "firstParty"}`
	loggedInAuthStatus = `{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty"}`
)

func TestClassifyAuthStatus(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		out    string
		apiKey string
		want   agent.AuthState
	}{
		{"fresh profile is a positive not-logged-in", 1, freshAuthStatus, "", agent.AuthNotLoggedIn},
		{"logged in", 0, loggedInAuthStatus, "", agent.AuthLoggedIn},
		// Everything below FAILS OPEN — the PM ruling's list, one row each.
		{"another exit status", 2, freshAuthStatus, "", agent.AuthUnknown},
		{"garbage output", 1, "zzz not json", "", agent.AuthUnknown},
		{"older CLI with no auth subcommand", 1, "error: unknown command 'auth'", "", agent.AuthUnknown},
		{"json without loggedIn", 1, `{"authMethod": "none"}`, "", agent.AuthUnknown},
		{"empty output", 0, "", "", agent.AuthUnknown},
		{"api key set makes a false inconclusive", 1, freshAuthStatus, "ANTHROPIC_API_KEY", agent.AuthUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAuthStatus(tc.exit, []byte(tc.out), tc.apiKey)
			if got.State != tc.want {
				t.Fatalf("state = %v (%s), want %v", got.State, got.Detail, tc.want)
			}
		})
	}
}

func TestAPIKeySet(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if k := apiKeySet(get); k != "" {
		t.Fatalf("no key set, got %q", k)
	}
	env["CLAUDE_CODE_USE_BEDROCK"] = "1"
	if k := apiKeySet(get); k != "CLAUDE_CODE_USE_BEDROCK" {
		t.Fatalf("got %q", k)
	}
}

// writeStubClaude writes an executable named `claude` whose `auth status`
// prints authOut and exits authExit, and which otherwise behaves like `cat`
// (a PTY-holding process that never exits on its own).
func writeStubClaude(t *testing.T, authOut string, authExit int) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ]; then\n  cat <<'JSON'\n" + authOut +
		"\nJSON\n  exit " + itoa(authExit) + "\nfi\nexec cat\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestAuthStatusExec(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	ctx := context.Background()
	if r := AuthStatus(ctx, writeStubClaude(t, freshAuthStatus, 1)); r.State != agent.AuthNotLoggedIn {
		t.Errorf("fresh: %v (%s)", r.State, r.Detail)
	}
	if r := AuthStatus(ctx, writeStubClaude(t, loggedInAuthStatus, 0)); r.State != agent.AuthLoggedIn {
		t.Errorf("logged in: %v (%s)", r.State, r.Detail)
	}
	if r := AuthStatus(ctx, writeStubClaude(t, "garbage", 2)); r.State != agent.AuthUnknown {
		t.Errorf("garbage exit 2: %v (%s)", r.State, r.Detail)
	}
	if r := AuthStatus(ctx, filepath.Join(t.TempDir(), "claude")); r.State != agent.AuthUnknown {
		t.Errorf("missing binary: %v (%s)", r.State, r.Detail)
	}
	// A hung probe is inconclusive, not a refusal.
	dir := t.TempDir()
	hang := filepath.Join(dir, "claude")
	os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)
	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if r := AuthStatus(tctx, hang); r.State != agent.AuthUnknown {
		t.Errorf("timeout: %v (%s)", r.State, r.Detail)
	}
}

// TestCommandTemplateCarriesBypassWarningSettings pins part A of #173: the
// default spawn argv carries --settings with the JSON as ONE argument (the
// template is split on whitespace with no shell quoting), and the JSON parses.
func TestCommandTemplateCarriesBypassWarningSettings(t *testing.T) {
	argv, err := agent.ExpandCommand(Provider.CommandTemplate, agent.CommandTemplateVars{PromptFile: "/p.md"})
	if err != nil {
		t.Fatal(err)
	}
	var settings string
	for i, a := range argv {
		if a == "--settings" && i+1 < len(argv) {
			settings = argv[i+1]
		}
	}
	if settings == "" {
		t.Fatalf("no --settings argument in %q", argv)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(settings), &v); err != nil {
		t.Fatalf("--settings %q is not JSON: %v", settings, err)
	}
	if v["skipDangerousModePermissionPrompt"] != true {
		t.Fatalf("--settings %q does not set skipDangerousModePermissionPrompt:true", settings)
	}
	// Both halves the polecat-command validator requires must be present.
	for _, f := range Provider.NonInteractiveFlags {
		if !strings.Contains(Provider.CommandTemplate, f) {
			t.Errorf("default template lacks required non-interactive flag %q", f)
		}
	}
	if Provider.PermissionNotice == "" || Provider.AuthPreflight == nil {
		t.Error("claude provider must announce its permission posture and declare a login probe")
	}
}

type stubCommandConfig struct{ cmd string }

func (c stubCommandConfig) AgentCommand(string) string  { return c.cmd }
func (c stubCommandConfig) AgentProvider(string) string { return "" }

// TestAutoStartLoginPreflight is acceptance C of #173, against a stub `claude`
// in a sandbox HOME: a positive not-logged-in refuses the crew and spawns
// nothing; a logged-in harness and — the case PM ruled the most important —
// a garbage `auth status` exiting 2 both still spawn.
func TestAutoStartLoginPreflight(t *testing.T) {
	for _, tc := range []struct {
		name     string
		out      string
		exit     int
		want     agent.AutoStartStatus
		spawned  bool
		refusing bool
	}{
		{"fresh HOME refuses", freshAuthStatus, 1, agent.AutoStartStatusRefusedNotLoggedIn, false, true},
		{"logged in spawns", loggedInAuthStatus, 0, agent.AutoStartStatusStarted, true, false},
		{"garbage exit 2 fails open and spawns", "zzz\x01not json", 2, agent.AutoStartStatusStarted, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testsandbox.Isolate(t)
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
			t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
			if err := agent.InitPromptDirs(); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(agent.CrewPromptDir(), "scout.md"), "+++\nauto_start = true\n+++\n# scout\n")
			writeFile(t, filepath.Join(agent.CrewPromptDir(), "sentry.md"), "+++\nauto_start = true\n+++\n# sentry\n")

			reg, err := agent.NewRegistry(agenttest.SocketDir(t))
			if err != nil {
				t.Fatal(err)
			}
			defer reg.StopAll(2 * time.Second)
			stub := writeStubClaude(t, tc.out, tc.exit)
			reg.SetCommandConfig(stubCommandConfig{cmd: stub})
			p := agent.Provider{ID: "claude", Binary: "claude", Nudge: Provider.Nudge, AuthPreflight: AuthStatus}
			reg.RegisterProvider(&p)

			results := reg.AutoStartAgents()
			if len(results) != 2 {
				t.Fatalf("want 2 auto-start results, got %v", results)
			}
			for _, res := range results {
				if res.Status != tc.want {
					t.Errorf("%s: status %q (%s), want %q", res.Name, res.Status, res.Error, tc.want)
				}
				if (reg.Get(res.Name) != nil) != tc.spawned {
					t.Errorf("%s: registered=%v, want %v", res.Name, reg.Get(res.Name) != nil, tc.spawned)
				}
				if tc.refusing && !strings.Contains(res.Error, "log in") {
					t.Errorf("%s: refusal must say how to fix it: %q", res.Name, res.Error)
				}
			}
			// Hand-starting is never second-guessed: the operator is watching.
			if tc.refusing {
				a, err := reg.StartCrewAgent("scout")
				if err != nil || a == nil {
					t.Fatalf("manual StartCrewAgent refused: %v", err)
				}
				if errors.Is(err, agent.ErrHarnessNotLoggedIn) {
					t.Fatal("manual start ran the preflight")
				}
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
