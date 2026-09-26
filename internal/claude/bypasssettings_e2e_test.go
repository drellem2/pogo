package claude

// Live tripwire for part A of #173: does Claude Code still honour
// `--settings {"skipDangerousModePermissionPrompt":true}` (mg-2991)?
//
// pogo pre-accepts the "Bypass Permissions mode" warning per session through
// that flag rather than writing ~/.claude/settings.json. The key is
// Claude-owned and undocumented as a contract, so it can drift: if a release
// renames it or stops reading it from --settings, every spawn on a profile that
// never accepted the warning lands on "❯ No, exit" and dies — and nothing in
// the unit suite can see that, because it never runs the real binary. This is
// the triage's acceptance case C2 plus its positive control, as code:
//
//	POGO_CLAUDE_SETTINGS_E2E=1 go test ./internal/claude/ \
//	    -run TestClaudeBypassWarningSettingsStillHonoured -v
//
// It is opt-in because it runs the real `claude` binary (whatever version is on
// PATH — record it with the result), not because it is expensive: it needs no
// credentials and spends no model turn. A sandbox HOME seeded with
// hasCompletedOnboarding and a pre-trusted project skips the theme/login and
// trust gates, so the bypass warning is the ONLY gate left between spawn and
// the composer. Measured on 2.1.283 with no login: the control renders the
// warning, and the flagged spawn reaches the composer ("Not logged in · Run
// /login" in the footer, which is fine — nothing is submitted).
//
// Both halves are needed. "Composer reached with the flag" alone would also
// pass on a Claude that stopped showing the warning at all, or on a sandbox
// that accidentally inherited an accepted profile; the control proves the
// warning is really there to suppress.

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/drellem2/pogo/internal/agent"
)

const bypassSettingsE2EEnv = "POGO_CLAUDE_SETTINGS_E2E"

// Screen markers, compared with ALL whitespace removed on both sides: Ink
// positions words with cursor moves, so StripANSI output loses the spaces
// between them (gh#76/mg-d06a).
const (
	// bypassWarningMarker is the warning's title. Case matters: the composer
	// footer says "bypass permissions on", lower-case.
	bypassWarningMarker = "Bypass Permissions mode"
	// composerFooterMarker is the status line Claude renders under a live
	// composer in bypass mode.
	composerFooterMarker = "bypass permissions on"
)

// bypassScreenBudget bounds each spawn's wait for either marker. The measured
// time to either screen is 2–7s; the rest is headroom for a cold start.
const bypassScreenBudget = 45 * time.Second

func TestClaudeBypassWarningSettingsStillHonoured(t *testing.T) {
	if os.Getenv(bypassSettingsE2EEnv) == "" {
		t.Skipf("live Claude Code tripwire; set %s=1 to run", bypassSettingsE2EEnv)
	}
	bin, err := exec.LookPath(Provider.Binary)
	if err != nil {
		t.Skipf("%s not on PATH: %v", Provider.Binary, err)
	}
	if out, err := exec.Command(bin, "--version").Output(); err == nil {
		t.Logf("harness: %s", strings.TrimSpace(string(out)))
	}

	// The real spawn argv, so drift in the template is covered too.
	argv, err := agent.ExpandCommand(Provider.CommandTemplate,
		agent.CommandTemplateVars{PromptFile: writePromptFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	withoutSettings := dropSettingsArg(argv)
	if len(withoutSettings) != len(argv)-2 {
		t.Fatalf("default spawn argv %q carries no --settings pair to test", argv)
	}

	t.Run("positive control: without --settings the warning renders", func(t *testing.T) {
		home := seedClaudeHome(t)
		screen := runUntilScreen(t, bin, withoutSettings[1:], home)
		if !containsSpaceless(screen, bypassWarningMarker) {
			t.Fatalf("the bypass warning never rendered without --settings, so the "+
				"flagged half proves nothing (did the sandbox inherit an accepted "+
				"profile, or did Claude drop the warning?). Screen tail:\n%s", screenTail(screen))
		}
	})

	t.Run("with --settings the composer is reached and nothing is written", func(t *testing.T) {
		home := seedClaudeHome(t)
		screen := runUntilScreen(t, bin, argv[1:], home)
		if containsSpaceless(screen, bypassWarningMarker) {
			t.Fatalf("Claude showed the bypass warning despite %s — the key has drifted "+
				"and every spawn on a never-accepted profile will exit. Screen tail:\n%s",
				BypassWarningSettings, screenTail(screen))
		}
		if !containsSpaceless(screen, composerFooterMarker) {
			t.Fatalf("the composer never rendered within %s. Screen tail:\n%s",
				bypassScreenBudget, screenTail(screen))
		}
		// Session-only is the point of --settings: the operator's own profile
		// must not be rewritten behind their back.
		filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte("skipDangerousModePermissionPrompt")) {
				t.Errorf("--settings persisted the key to %s", p)
			}
			return nil
		})
	})
}

// dropSettingsArg returns argv without its `--settings <json>` pair.
func dropSettingsArg(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--settings" && i+1 < len(argv) {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func writePromptFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(p, []byte("# e2e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// seedClaudeHome builds a sandbox HOME whose only remaining first-run gate is
// the bypass warning: onboarding complete, and a project directory already
// trusted. It returns the HOME; the project is <home>/proj.
func seedClaudeHome(t *testing.T) string {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{
		"hasCompletedOnboarding": true,
		"theme":                  "dark",
		"projects":               map[string]any{proj: map[string]any{"hasTrustDialogAccepted": true}},
	})
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// runUntilScreen spawns claude on a PTY in the sandbox HOME and returns the
// ANSI-stripped screen once either marker appears or the budget runs out. The
// process is killed before returning; nothing is ever typed into it.
func runUntilScreen(t *testing.T, bin string, args []string, home string) string {
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Join(home, "proj")
	cmd.Env = sandboxClaudeEnv(home)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	tee := newTeeBuf()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				tee.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	defer func() {
		cmd.Process.Kill()
		<-exited
		ptmx.Close()
		<-copied
	}()

	deadline := time.Now().Add(bypassScreenBudget)
	for time.Now().Before(deadline) {
		screen := string(agent.StripANSI(tee.snapshot()))
		if containsSpaceless(screen, bypassWarningMarker) || containsSpaceless(screen, composerFooterMarker) {
			return screen
		}
		select {
		case <-exited:
			return string(agent.StripANSI(tee.snapshot()))
		case <-time.After(250 * time.Millisecond):
		}
	}
	return string(agent.StripANSI(tee.snapshot()))
}

// sandboxClaudeEnv is the parent environment with HOME redirected and every
// credential or Claude-behaviour variable removed, so the host's real login,
// API key and settings cannot leak into the measurement.
func sandboxClaudeEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "HOME", k == "XDG_CONFIG_HOME", k == "TERM",
			strings.HasPrefix(k, "ANTHROPIC_"), strings.HasPrefix(k, "CLAUDE"):
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"TERM=xterm-256color",
		"DISABLE_AUTOUPDATER=1",
	)
}

func containsSpaceless(haystack, needle string) bool {
	return strings.Contains(strings.Join(strings.Fields(haystack), ""),
		strings.Join(strings.Fields(needle), ""))
}

func screenTail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 1500 {
		s = "…" + s[len(s)-1500:]
	}
	return s
}
