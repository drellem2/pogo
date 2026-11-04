package claude

// Live acceptance for drellem2/pogo#286: a token-only fresh install must not
// stop on Claude Code's first-run theme picker.
//
//	POGO_CLAUDE_ONBOARDING_E2E=1 go test ./internal/claude/ \
//	    -run TestClaudeOnboardingSeedSkipsThemePicker -v
//
// It spawns the real `claude` on PATH through agent.Registry.Spawn, the path
// pogod uses, with the real provider (so the trust-dialog hook runs as it does
// in production). The sandbox HOME is empty and the only credential is
// CLAUDE_CODE_OAUTH_TOKEN. The token is a dummy unless
// POGO_CLAUDE_ONBOARDING_E2E_TOKEN supplies a real one: the first-run screens
// are decided before any request is made, `claude auth status` already reports
// loggedIn:true for a dummy token, and nothing is ever submitted. Nothing is
// typed into the PTY by this test.
//
// Both halves are needed. The control runs the same spawn with the seed hook
// removed and must land on the theme picker: if it does not, the "composer
// reached" half would also pass on a Claude that dropped the picker or on a
// sandbox that leaked a used profile, and would prove nothing.
//
// Measured on claude 2.1.318: the control shows "Choose the text style", and
// the seeded spawn reaches the composer ("bypass permissions on").

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/testsandbox"
)

const onboardingE2EEnv = "POGO_CLAUDE_ONBOARDING_E2E"

// themePickerMarker is the theme picker's prompt (compared spaceless).
const themePickerMarker = "Choose the text style"

func TestClaudeOnboardingSeedSkipsThemePicker(t *testing.T) {
	if os.Getenv(onboardingE2EEnv) == "" {
		t.Skipf("live Claude Code tripwire; set %s=1 to run", onboardingE2EEnv)
	}
	bin, err := exec.LookPath(Provider.Binary)
	if err != nil {
		t.Skipf("%s not on PATH: %v", Provider.Binary, err)
	}
	if out, err := exec.Command(bin, "--version").Output(); err == nil {
		t.Logf("harness: %s", strings.TrimSpace(string(out)))
	}
	token := os.Getenv("POGO_CLAUDE_ONBOARDING_E2E_TOKEN")
	if token == "" {
		token = "sk-ant-oat01-pogo-e2e-dummy-not-a-real-token"
	}

	t.Run("positive control: without the seed the theme picker renders", func(t *testing.T) {
		p := Provider
		p.ProfileSeedHook = nil
		home, screen := spawnTokenOnly(t, bin, &p, token)
		if !containsSpaceless(screen, themePickerMarker) {
			t.Fatalf("no theme picker without the seed, so the seeded half proves "+
				"nothing (did the sandbox inherit a used profile, or did Claude drop "+
				"the picker?). Screen tail:\n%s", screenTail(screen))
		}
		if containsSpaceless(screen, composerFooterMarker) {
			t.Fatalf("the composer rendered without the seed. Screen tail:\n%s", screenTail(screen))
		}
		if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
			t.Logf("note: claude created %s itself while on the picker", filepath.Join(home, ".claude.json"))
		}
	})

	t.Run("with the seed the agent reaches its composer unattended", func(t *testing.T) {
		p := Provider
		home, screen := spawnTokenOnly(t, bin, &p, token)
		if containsSpaceless(screen, themePickerMarker) {
			t.Fatalf("the theme picker rendered despite the seed — Claude may have "+
				"renamed %s. Screen tail:\n%s", onboardingKey, screenTail(screen))
		}
		if !containsSpaceless(screen, composerFooterMarker) {
			t.Fatalf("the composer never rendered within %s. Screen tail:\n%s",
				bypassScreenBudget, screenTail(screen))
		}
		if got := readJSONObject(t, filepath.Join(home, ".claude.json")); got[onboardingKey] != true {
			t.Errorf(".claude.json after spawn = %v", got)
		}
	})
}

// spawnTokenOnly spawns claude through a Registry in a fresh sandbox HOME
// whose only credential is CLAUDE_CODE_OAUTH_TOKEN, waits for the theme
// picker or the composer, and returns the HOME and the ANSI-stripped screen.
func spawnTokenOnly(t *testing.T, bin string, p *agent.Provider, token string) (string, string) {
	t.Helper()
	sb := testsandbox.Isolate(t)
	// Strip every Claude/Anthropic variable the host carries, so the host's
	// own login and session cannot leak in. t.Setenv records the restore.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "ANTHROPIC_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", token)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("DISABLE_AUTOUPDATER", "1")

	home, err := filepath.EvalSymlinks(sb.Home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Fatalf("sandbox HOME %s is not fresh: .claude.json exists", home)
	}
	proj := filepath.Join(home, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	argv, err := agent.ExpandCommand(p.CommandTemplate,
		agent.CommandTemplateVars{PromptFile: writePromptFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	argv[0] = bin

	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.StopAll(5 * time.Second) })
	reg.RegisterProvider(p)
	a, err := reg.Spawn(agent.SpawnRequest{
		Name:     "onboarding-e2e",
		Type:     agent.TypePolecat,
		Command:  argv,
		Dir:      proj,
		Provider: p,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	deadline := time.Now().Add(bypassScreenBudget)
	var screen string
	for time.Now().Before(deadline) {
		screen = string(agent.StripANSI(a.RecentOutput(256 << 10)))
		if containsSpaceless(screen, themePickerMarker) || containsSpaceless(screen, composerFooterMarker) {
			break
		}
		select {
		case <-a.Done():
			return home, string(agent.StripANSI(a.RecentOutput(256 << 10)))
		case <-time.After(250 * time.Millisecond):
		}
	}
	return home, screen
}
