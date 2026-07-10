package cursor_test

// End-to-end verification for the Cursor provider (mg-c146).
// TestCursorEndToEnd drives a real `agent` process through pogo's actual
// agent.Registry and the resolved cursor.Provider — the exact pipeline a
// `provider = "cursor"` polecat takes:
//
//	[agents.polecat] provider = "cursor"   (config tier of resolveProvider)
//	  -> ExpandCommand(provider.CommandTemplate, ...)
//	  -> Registry.Spawn  (writes .cursor/rules/pogo-persona.mdc)
//	  -> PostSpawnHook   (dismisses the workspace-trust dialog)
//	  -> initial task appended to argv (InitialPromptViaArgv)
//	  -> mid-session nudge (Agent.Nudge into the settled composer)
//
// The registry is wired exactly as cmd/pogod wires it (all providers
// registered, per-type config, claude as the global default), so the spawn
// resolving cursor proves the `[agents.polecat] provider = "cursor"` config
// tier — not just a hardcoded default.
//
// # Why this test is live, unlike internal/pi/e2e_test.go
//
// pi's e2e runs fully offline: pi supports custom OpenAI-compatible providers,
// so the test points it at a local mock server and asserts byte-level on the
// captured completion request. The `agent` binary admits no such rig.
//
// Cursor speaks a proprietary connectrpc/protobuf protocol to api2.cursor.sh,
// which alone would make a mock absurd. But the bundle DOES carry a hidden
// local-provider mode (--local-agent-base-url, CURSOR_LOCAL_AGENT_BASE_URL,
// ANTHROPIC_BASE_URL, CURSOR_ENABLE_AUTHLESS) that accepts an OpenAI-compatible
// endpoint — exactly what an offline rig needs. It is gated to a *different
// executable*, cursor-agent-local, on its own download channel. Measured against
// the `agent` binary this provider targets: the flag is rejected outright
// ("unknown option"), and every env-var route left a local mock server with zero
// requests while the real backend answered. See the calibration doc.
//
// So this test follows the *Codex* pattern instead: opt-in, real binary, real
// network, real Cursor plan credits. If pogo ever targets cursor-agent-local, a
// pi-style offline mock becomes available — a different binary, a different
// ticket.
//
// Losing byte-level capture costs the "did the persona reach the system
// prompt?" assertion. It is recovered BEHAVIOURALLY: the injected persona and
// the repo's own AGENTS.md each instruct the model to emit a distinct token,
// and the reply must carry both. That is a stronger statement than "the bytes
// were in the request" — it proves Cursor actually honoured both rule sources —
// and it is exactly the probe that settled the injection-collision question in
// docs/investigations/cursor-nudge-calibration.md.
//
// The purely on-disk half of the collision contract (persona lands in
// .cursor/rules/pogo-persona.mdc with `alwaysApply: true`; the repo's AGENTS.md
// survives byte-identical) needs no binary at all and is asserted ungated in
// integration_test.go, so it runs in the refinery gates and CI.
//
// The tests are opt-in only because they need an authenticated `agent` binary
// (`agent login`, or CURSOR_API_KEY) and consume Cursor plan credits:
//
//	POGO_CURSOR_E2E=1 go test ./internal/cursor/ -run 'TestCursor' -v
//
// There is deliberately no GitHub CI job for them (as with Codex, and unlike
// pi's offline `pi-e2e` job): CI has no Cursor account. CURSOR_DATA_DIR points
// Cursor's per-workspace state at a temp dir so the developer's real ~/.cursor
// trust decisions and chat history are untouched — and so the workspace-trust
// dialog reliably appears, which is what exercises TrustDialogHook.
//
// Calibrated against Cursor CLI 2026.07.09-a3815c0. The CLI is closed-source
// and churns fast (the command was renamed cursor-agent -> agent in 2026), so
// the assertions below pin the observable contract — binary name, trust-dialog
// text, composer placeholder, submit dialect — and fail loudly rather than
// silently degrading when Cursor changes its TUI.

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cursor"
	"github.com/drellem2/pogo/internal/providers"
)

// calibratedVersion is the Cursor CLI this provider's NudgeProfile was measured
// against. A mismatch is logged, not failed: the point is a breadcrumb when a
// future CLI changes the TUI out from under the calibration.
const calibratedVersion = "2026.07.09-a3815c0"

// Tokens the model is instructed to emit. personaToken proves the pogo persona
// (delivered via .cursor/rules/pogo-persona.mdc) reached the model; repoToken
// proves the repo's own AGENTS.md still reached it alongside — the injection
// must be additive, not a substitution.
const (
	personaToken = "POGOPERSONAOK"
	repoToken    = "REPOAGENTSOK"
)

// polecatCursorConfig mirrors a config.toml with `[agents] provider = "claude"`
// and `[agents.polecat] provider = "cursor"` — the mixed-fleet shape the
// per-type provider tier exists for (it satisfies agent.AgentCommandConfig the
// same way config.AgentsConfig does).
type polecatCursorConfig struct{}

func (polecatCursorConfig) AgentCommand(string) string { return "" }
func (polecatCursorConfig) AgentProvider(agentType string) string {
	if agentType == string(agent.TypePolecat) {
		return "cursor"
	}
	return "claude"
}

// requireCursor skips unless the e2e gate is set and an `agent` binary is on
// PATH.
func requireCursor(t *testing.T) {
	t.Helper()
	if os.Getenv("POGO_CURSOR_E2E") != "1" {
		t.Skip("set POGO_CURSOR_E2E=1 to run the live Cursor end-to-end test")
	}
	if _, err := exec.LookPath(cursor.Provider.Binary); err != nil {
		t.Skipf("%s binary not on PATH", cursor.Provider.Binary)
	}
	out, err := exec.Command(cursor.Provider.Binary, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version failed — is the CLI healthy? %v", cursor.Provider.Binary, err)
	}
	got := strings.TrimSpace(string(out))
	if got != calibratedVersion {
		t.Logf("NOTE: Cursor CLI is %q; the provider was calibrated against %q. "+
			"If this test fails on the TUI assertions (trust dialog, composer "+
			"placeholder, submit dialect), re-run the calibration in "+
			"docs/investigations/cursor-nudge-calibration.md.", got, calibratedVersion)
	}
}

// seedWorktree creates a worktree carrying its own AGENTS.md (the collision
// case) and a prompt file holding the pogo persona. Both instruct the model to
// emit a distinct token in every reply.
func seedWorktree(t *testing.T, base string) (workDir, promptFile, agentsMD string, repoAgentsMD []byte) {
	t.Helper()
	workDir = filepath.Join(base, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	repoAgentsMD = []byte("# repo context\n\nAlways include the token " + repoToken +
		" somewhere in every reply you write.\n")
	agentsMD = filepath.Join(workDir, "AGENTS.md")
	if err := os.WriteFile(agentsMD, repoAgentsMD, 0644); err != nil {
		t.Fatalf("write repo AGENTS.md: %v", err)
	}

	persona := "# pogo polecat persona\n\nYou are a pogo polecat agent.\n" +
		"Always include the token " + personaToken + " somewhere in every reply you write.\n"
	promptFile = filepath.Join(base, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	return workDir, promptFile, agentsMD, repoAgentsMD
}

func TestCursorEndToEnd(t *testing.T) {
	requireCursor(t)

	base := t.TempDir()
	workDir, promptFile, agentsMDPath, repoAgentsMD := seedWorktree(t, base)
	dataDir := filepath.Join(base, "cursor-state")

	provider, ok := providers.Resolve("cursor")
	if !ok || provider.ID != "cursor" {
		t.Fatalf("providers.Resolve(\"cursor\") = (%v, %v), want the cursor provider", provider, ok)
	}

	// Wire the registry exactly as cmd/pogod does at startup: every known
	// provider registered, the per-type/global provider config installed, and
	// claude as the global default. cursor being resolved for the polecat spawn
	// below therefore proves the `[agents.polecat] provider = "cursor"` config
	// tier, not a registry-global default.
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, p := range providers.All() {
		reg.RegisterProvider(p)
	}
	reg.SetCommandConfig(polecatCursorConfig{})
	reg.SetDefaultProvider("claude")
	defer reg.StopAll(3 * time.Second)

	cmd, err := agent.ExpandCommand(provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  "e2e",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    workDir,
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	t.Logf("expanded command: %v", cmd)

	const task = "Greet me in one short sentence."
	if _, err := reg.Spawn(agent.SpawnRequest{
		Name:         "e2e",
		Type:         agent.TypePolecat,
		Command:      cmd,
		PromptFile:   promptFile,
		Dir:          workDir,
		Env:          []string{"CURSOR_DATA_DIR=" + dataDir},
		InitialNudge: task,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	a := reg.Get("e2e")
	if a == nil {
		t.Fatal("agent not in registry after spawn")
	}

	// Acceptance (provider registry): the spawn resolved cursor through the
	// per-type config tier — not claude, the registry's global default.
	if got := a.ProviderID(); got != "cursor" {
		t.Fatalf("resolved provider = %q, want cursor via the [agents.polecat] config tier", got)
	}

	// Acceptance (argv delivery): Spawn appended the task to the argv, so task
	// delivery never depends on a PTY idle window that the silent trust dialog
	// would otherwise poison.
	if got := a.Command[len(a.Command)-1]; got != task {
		t.Fatalf("last command element = %q, want the argv-delivered task %q", got, task)
	}

	// Acceptance (persona injection, on disk): Spawn wrote the rule file, with
	// frontmatter, next to — not over — the repo's own AGENTS.md.
	rule, err := os.ReadFile(filepath.Join(workDir, ".cursor", "rules", "pogo-persona.mdc"))
	if err != nil {
		t.Fatalf("persona rule file not written by Spawn: %v", err)
	}
	if !strings.Contains(string(rule), "alwaysApply: true") {
		t.Errorf("persona rule file lacks `alwaysApply: true`:\n%s", rule)
	}

	// Acceptance (trust dialog): Cursor blocked on workspace trust, and the
	// provider's PostSpawnHook dismissed it. If the dialog never appeared,
	// Cursor changed its trust behaviour and the hook is now dead code; if it
	// appeared but the composer never followed, the hook failed to dismiss it.
	if !waitFor(t, a, 20*time.Second, "Workspace Trust Required") {
		t.Error("Cursor did not show the workspace-trust dialog — TrustDialogHook " +
			"may now be dead code; re-check the trust behaviour before removing it")
	}

	// Acceptance (calibrated PromptReadySentinel): the composer placeholder
	// renders once the input loop is up, which also proves the trust dialog was
	// dismissed. Argv delivery means no initial nudge waits on this marker, but
	// the profile retains it — this catches it going stale.
	if !waitFor(t, a, 40*time.Second, provider.Nudge.PromptReadySentinel) {
		t.Fatalf("PromptReadySentinel %q never appeared — the trust dialog was not "+
			"dismissed, or Cursor changed its composer placeholder.\noutput tail:\n%s",
			provider.Nudge.PromptReadySentinel, outputTail(a, 2000))
	}

	// Acceptance (persona reached the model, behavioural): the argv-delivered
	// task starts a turn, and the reply carries BOTH tokens — the persona's,
	// from .cursor/rules/pogo-persona.mdc, and the repo's, from its own
	// AGENTS.md. This is the injection-collision contract: additive, not
	// substitutive. Cursor's protocol is closed, so this reply-level assertion
	// stands in for pi's byte-level request capture.
	if !waitFor(t, a, 90*time.Second, personaToken) {
		t.Errorf("persona token %q never appeared in Cursor's reply — the persona "+
			"in .cursor/rules/pogo-persona.mdc did not reach the model.\noutput tail:\n%s",
			personaToken, outputTail(a, 2000))
	}
	if !waitFor(t, a, 30*time.Second, repoToken) {
		t.Errorf("repo token %q never appeared in Cursor's reply — persona injection "+
			"shadowed the repo's own AGENTS.md.\noutput tail:\n%s",
			repoToken, outputTail(a, 2000))
	}

	// Acceptance (collision regression, on disk): the repo's AGENTS.md survived
	// the whole run byte-identical.
	if got, rerr := os.ReadFile(agentsMDPath); rerr != nil {
		t.Errorf("repo AGENTS.md unreadable after spawn: %v", rerr)
	} else if !bytes.Equal(got, repoAgentsMD) {
		t.Errorf("repo AGENTS.md was clobbered by the spawn; contents now:\n%s", got)
	}

	// Acceptance (NudgeProfile calibration): a settled Cursor composer emits
	// zero PTY output. Polled for a quiet WINDOW rather than sampled once — the
	// same de-flake pi needed (mg-8cc0), since a post-turn repaint can land
	// between two samples.
	assertComposerSettles(t, a)

	// Acceptance (nudge dialect, mid-session): Agent.Nudge into the settled
	// composer — the calibrated body + "\r" split-write — triggers a second
	// turn. This is the path every later `pogo nudge` / coordinator message to a
	// running cursor polecat takes. A combined body+CR write would be swallowed
	// by Cursor's paste-burst detection, so this also guards SubmitDelay > 0.
	const followUp = "Reply with the word SECONDTURNOK."
	before := len(a.RecentOutput(1 << 20))
	if err := a.Nudge(followUp); err != nil {
		t.Fatalf("mid-session Nudge: %v", err)
	}
	if !waitFor(t, a, 90*time.Second, "SECONDTURNOK") {
		t.Fatalf("mid-session nudge never produced a reply — submit terminator or "+
			"split-write delay regressed (buffer grew %d bytes).\noutput tail:\n%s",
			len(a.RecentOutput(1<<20))-before, outputTail(a, 2000))
	}

	// Acceptance: the registry shuts the agent down cleanly.
	stopped := make(chan struct{})
	go func() {
		reg.StopAll(10 * time.Second)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Log("registry stopped the cursor agent cleanly")
	case <-time.After(15 * time.Second):
		t.Error("registry did not stop the cursor agent within 15s — unclean shutdown")
	}
}

// TestCursorHeadless verifies the provider's spawn flags compose with Cursor's
// non-interactive mode: the expanded CommandTemplate plus `--trust -p
// --output-format text` and a positional task executes one turn and exits — no
// PTY, no registry, no nudge, no trust dialog. It pins two things the TUI test
// cannot isolate: that --force is accepted outside the TUI, and that --trust is
// the print-mode-only trust bypass (which is precisely why the interactive
// template must not carry it — see TestProviderCommandOmitsTrustFlag).
//
// The persona rule file is written by hand here, exactly as
// writeContextFilePrompt would, because there is no registry in this path.
func TestCursorHeadless(t *testing.T) {
	requireCursor(t)

	base := t.TempDir()
	workDir, promptFile, _, _ := seedWorktree(t, base)
	dataDir := filepath.Join(base, "cursor-state")

	persona, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	ruleDir := filepath.Join(workDir, ".cursor", "rules")
	if err := os.MkdirAll(ruleDir, 0755); err != nil {
		t.Fatal(err)
	}
	rule := append([]byte(nil), cursor.Provider.PromptInjection.ContextFileHeader...)
	rule = append(rule, persona...)
	if err := os.WriteFile(filepath.Join(ruleDir, "pogo-persona.mdc"), rule, 0644); err != nil {
		t.Fatal(err)
	}

	argv, err := agent.ExpandCommand(cursor.Provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  "headless",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    workDir,
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	const task = "Greet me in one short sentence."
	// Test-only flags on top of the provider's real template. --trust is
	// accepted here and only here: it is rejected outside --print/headless mode.
	argv = append(argv, "--trust", "-p", "--output-format", "text", task)
	t.Logf("headless command: %v", argv)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "CURSOR_DATA_DIR="+dataDir)
	raw, err := cmd.CombinedOutput()
	out := string(agent.StripANSI(raw))
	if err != nil {
		t.Fatalf("headless cursor run failed: %v\noutput:\n%s", err, tail(out, 2000))
	}

	// Both rule sources reached the model: the pogo persona (from
	// .cursor/rules/pogo-persona.mdc) and the repo's own AGENTS.md.
	if !strings.Contains(out, personaToken) {
		t.Errorf("persona token %q missing from headless reply — the .cursor/rules "+
			"persona did not reach the model.\noutput:\n%s", personaToken, tail(out, 2000))
	}
	if !strings.Contains(out, repoToken) {
		t.Errorf("repo token %q missing from headless reply — persona injection "+
			"shadowed the repo's AGENTS.md.\noutput:\n%s", repoToken, tail(out, 2000))
	}
}

// TestCursorRejectsTrustFlagInTUI pins the measured constraint that shapes the
// command template: --trust is print-mode-only, and Cursor exits before
// rendering the TUI when it is passed interactively. If a future Cursor starts
// accepting it, this test fails and the provider can drop TrustDialogHook in
// favour of the flag — a strictly simpler design. It makes no model call:
// Cursor rejects the flag during argument parsing.
//
// It must run on a PTY. Cursor decides between TUI and print mode by whether
// stdout is a terminal, so a plain exec.Command here would land in print mode
// and report a different error ("No prompt provided for print mode") — passing
// for the wrong reason.
func TestCursorRejectsTrustFlagInTUI(t *testing.T) {
	if os.Getenv("POGO_CURSOR_E2E") != "1" {
		t.Skip("set POGO_CURSOR_E2E=1 to run the live Cursor flag-contract test")
	}
	if _, err := exec.LookPath(cursor.Provider.Binary); err != nil {
		t.Skipf("%s binary not on PATH", cursor.Provider.Binary)
	}

	base := t.TempDir()
	workDir, _, _, _ := seedWorktree(t, base)

	cmd := exec.Command(cursor.Provider.Binary, "--force", "--trust")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "CURSOR_DATA_DIR="+filepath.Join(base, "cursor-state"))

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 200, Rows: 50})
	if err != nil {
		t.Fatalf("pty.StartWithSize: %v", err)
	}
	defer func() { _ = f.Close() }()

	var buf bytes.Buffer
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, f); close(copied) }()

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case err := <-waitErr:
		<-copied
		out := string(agent.StripANSI(buf.Bytes()))
		if err == nil {
			t.Fatalf("`agent --force --trust` exited 0 on a PTY; Cursor now accepts "+
				"interactive --trust, so cursor.Provider can carry the flag and drop "+
				"TrustDialogHook.\noutput:\n%s", tail(out, 1000))
		}
		if !strings.Contains(out, "--trust can only be used with --print") {
			t.Errorf("expected Cursor's print-mode-only --trust rejection, got err=%v\noutput:\n%s",
				err, tail(out, 1000))
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("`agent --force --trust` did not exit on a PTY within 30s — Cursor may "+
			"now accept interactive --trust and render the TUI.\noutput:\n%s",
			tail(string(agent.StripANSI(buf.Bytes())), 1000))
	}
}

// waitFor polls the agent's ANSI-stripped PTY output for a substring.
func waitFor(t *testing.T, a *agent.Agent, timeout time.Duration, want string) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(string(agent.StripANSI(a.RecentOutput(1<<20))), want) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func outputTail(a *agent.Agent, n int) string {
	return tail(string(agent.StripANSI(a.RecentOutput(1<<20))), n)
}

// assertComposerSettles polls for a quiet PTY window. A settled Cursor composer
// emits zero output (measured: 0 bytes over 10s), so a quiet window always
// arrives unless the TUI never settles — which is the real regression this
// guards. Polling a window rather than sampling once avoids straddling a
// post-turn repaint, the flake that bit pi's equivalent check (mg-8cc0).
func assertComposerSettles(t *testing.T, a *agent.Agent) {
	t.Helper()
	const (
		quietWindow  = 2 * time.Second
		quietBudget  = 2048 // far under a full 200x50 repaint, far over a stray cursor-blink escape
		settleBudget = 20 * time.Second
	)
	deadline := time.Now().Add(settleBudget)
	var growth int
	for {
		before := len(a.RecentOutput(1 << 20))
		time.Sleep(quietWindow)
		growth = len(a.RecentOutput(1<<20)) - before
		if growth <= quietBudget {
			t.Logf("idle check: composer settled (last %s window grew %d bytes)", quietWindow, growth)
			return
		}
		if !time.Now().Before(deadline) {
			t.Errorf("cursor composer never settled — buffer kept growing >%d bytes per "+
				"%s across %s of idle polling", quietBudget, quietWindow, settleBudget)
			return
		}
	}
}

// tail returns the last n bytes of s, for bounded failure output.
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
