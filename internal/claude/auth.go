package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// BypassWarningSettings is the spawn argument that pre-accepts Claude Code's
// "Bypass Permissions mode" warning for one session (drellem2/pogo#173).
//
// pogo already passes --dangerously-skip-permissions; on a profile that has
// never accepted it, Claude then shows a warning whose highlighted default is
// "No, exit", so an unattended first-run spawn exits. --settings layers this
// key onto the session only: nothing is written to ~/.claude/settings.json, so
// the operator's own interactive sessions are unchanged and no live claude
// process races a pogo write. The workspace trust dialog is NOT affected by it
// — TrustDialogHook still answers that.
//
// It must stay free of whitespace: ExpandCommand splits the template on
// whitespace and performs no shell quoting, so the JSON reaches argv verbatim.
const BypassWarningSettings = `--settings {"skipDangerousModePermissionPrompt":true}`

// bypassWarningSettingsKey is what a custom [agents] command carrying its own
// --settings JSON must include to satisfy the same requirement.
const bypassWarningSettingsKey = `"skipDangerousModePermissionPrompt":true`

// PermissionNotice is the sentence `pogo install` prints and
// `pogo doctor --check` shows. PM ruling on mg-ecc8: suppressing the warning is
// informed, not silent — and not an interactive consent step either, because
// install also runs non-interactively.
const PermissionNotice = "pogo's agents run Claude Code in bypass-permissions mode " +
	"(--dangerously-skip-permissions); the bypass warning is pre-accepted per " +
	"session via --settings, and ~/.claude/settings.json is not modified"

// apiKeyEnv are the environment variables under which Claude Code
// authenticates without an OAuth login. With one set, `claude auth status` was
// measured (2.1.283, mg-ecc8) to report loggedIn:true — but a false would be a
// candidate false negative, and a false refusal stops the whole crew, so under
// any of these a loggedIn:false is read as unknown rather than refused.
var apiKeyEnv = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
}

// AuthStatus runs `<binary> auth status --json` and classifies it.
//
// Measured on 2.1.283: a fresh profile exits 1 with {"loggedIn": false, ...};
// a logged-in profile, or one with ANTHROPIC_API_KEY set, exits 0 with
// {"loggedIn": true, ...}. Only that first shape — parseable JSON carrying an
// explicit "loggedIn": false, from an exit status of 0 or 1 — is
// AuthNotLoggedIn. Everything else is AuthUnknown and fails open.
func AuthStatus(ctx context.Context, binary string) agent.AuthReading {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "auth", "status", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ctx.Err() != nil {
			return agent.AuthReading{State: agent.AuthUnknown,
				Detail: fmt.Sprintf("`%s auth status` did not complete: %v", binary, err)}
		}
		exit = ee.ExitCode()
	}
	return classifyAuthStatus(exit, stdout.Bytes(), apiKeySet(os.Getenv))
}

func apiKeySet(getenv func(string) string) string {
	for _, k := range apiKeyEnv {
		if getenv(k) != "" {
			return k
		}
	}
	return ""
}

// classifyAuthStatus is AuthStatus's decision, separated from the exec so the
// fail-open cases are testable without a binary.
func classifyAuthStatus(exit int, out []byte, apiKeyVar string) agent.AuthReading {
	if exit != 0 && exit != 1 {
		return agent.AuthReading{State: agent.AuthUnknown,
			Detail: fmt.Sprintf("`claude auth status` exited %d", exit)}
	}
	var status struct {
		LoggedIn   *bool  `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &status); err != nil || status.LoggedIn == nil {
		return agent.AuthReading{State: agent.AuthUnknown,
			Detail: fmt.Sprintf("`claude auth status` (exit %d) printed no readable loggedIn field: %q",
				exit, truncate(string(out), 120))}
	}
	if *status.LoggedIn {
		detail := "loggedIn: true"
		if status.AuthMethod != "" {
			detail += ", authMethod: " + status.AuthMethod
		}
		return agent.AuthReading{State: agent.AuthLoggedIn, Detail: detail}
	}
	if apiKeyVar != "" {
		return agent.AuthReading{State: agent.AuthUnknown,
			Detail: fmt.Sprintf("`claude auth status` says loggedIn: false, but %s is set, "+
				"so it may authenticate anyway", apiKeyVar)}
	}
	return agent.AuthReading{State: agent.AuthNotLoggedIn,
		Detail: fmt.Sprintf("`claude auth status` exit %d, loggedIn: false", exit)}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
