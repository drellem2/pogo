package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// Claude Code's "Detected a custom API key" gate (mg-2037).
//
// When ANTHROPIC_API_KEY is set and this Claude Code profile has never answered
// for that key, the harness stops before its composer and asks:
//
//	Detected a custom API key in your environment
//	ANTHROPIC_API_KEY: sk-ant-...<last 20>
//	Do you want to use this API key?
//	  Yes
//	❯ No (recommended)
//
// Captured from 2.1.283 on a PTY in a sandbox HOME (mg-2037). This is the gate
// drellem2/pogo#173's `claude auth status` preflight cannot see: auth status
// reports loggedIn:true for an API-key user whether or not the key was ever
// approved, so the preflight passes and the agent stalls here.
//
// pogo deliberately does NOT answer it. Unlike the workspace-trust dialog, the
// two rows are two different billing choices: "Yes" bills the API key, "No"
// falls back to whatever login the profile holds (or to no auth at all). Either
// keystroke would decide something that is the operator's to decide, so the
// remedy is to name the gate loudly — at spawn (the trust hook's watch, below)
// and before any spawn (`pogo doctor --check`) — and let a human answer it once
// in a terminal. Claude Code remembers the answer per key.

// APIKeyGateName is the gate's name as agent.HoldAtPreComposerGate records it.
const APIKeyGateName = "Detected a custom API key"

// apiKeyPromptMarker is the gate's title, whitespace-collapsed and lower-cased
// for the same space-collapse reason as trustDialogMarker.
const apiKeyPromptMarker = "detectedacustomapikey"

// matchesAPIKeyPrompt reports whether PTY output contains the custom-API-key
// prompt. Like matchesTrustDialog it matches TEXT, so it is only ever consulted
// while composerReady is false — an echoed kickoff prompt that merely mentions
// the gate (this work item's own title does) arrives after the composer.
func matchesAPIKeyPrompt(output []byte) bool {
	return strings.Contains(strings.ToLower(collapse(string(agent.StripANSI(output)))), apiKeyPromptMarker)
}

// APIKeyApproval is what a Claude Code profile has on record for the
// ANTHROPIC_API_KEY in an environment.
type APIKeyApproval int

const (
	// APIKeyUnset: no ANTHROPIC_API_KEY, so there is no gate.
	APIKeyUnset APIKeyApproval = iota
	// APIKeyApproved: the profile will use the key without asking.
	APIKeyApproved
	// APIKeyRejected: the profile will ignore the key without asking.
	APIKeyRejected
	// APIKeyPending: the profile has no answer for this key, so an interactive
	// spawn stops at the prompt with "No (recommended)" highlighted.
	APIKeyPending
	// APIKeyUnknown: the profile's config could not be read or parsed. Not
	// evidence either way.
	APIKeyUnknown
)

// apiKeyConfigSuffix is how many trailing characters of the trimmed key Claude
// Code stores in customApiKeyResponses — `e.trim().slice(-20)` in 2.1.283
// (mg-2037). Storing the full key would put a credential in ~/.claude.json.
const apiKeyConfigSuffix = 20

// normalizeAPIKey mirrors Claude Code's own normalisation of a key for its
// approved/rejected lists.
func normalizeAPIKey(key string) string {
	k := strings.TrimSpace(key)
	if len(k) > apiKeyConfigSuffix {
		k = k[len(k)-apiKeyConfigSuffix:]
	}
	return k
}

// ClaudeGlobalConfigPath is the file Claude Code keeps customApiKeyResponses
// in: $CLAUDE_CONFIG_DIR/.claude.json when that is set, else ~/.claude.json.
// Same resolution as Claude Code 2.1.283 (`join(configDir || homedir(),
// ".claude.json")`).
func ClaudeGlobalConfigPath(getenv func(string) string, home string) string {
	if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

// ClassifyAPIKeyApproval decides what Claude Code would do with apiKey given
// the contents of its global config. readErr is the error from reading that
// file: a missing file means a fresh profile with no answers (pending), any
// other read failure is unknown.
//
// It never returns any part of the key.
func ClassifyAPIKeyApproval(apiKey string, config []byte, readErr error) APIKeyApproval {
	if strings.TrimSpace(apiKey) == "" {
		return APIKeyUnset
	}
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return APIKeyPending
		}
		return APIKeyUnknown
	}
	var cfg struct {
		CustomAPIKeyResponses *struct {
			Approved []any `json:"approved"`
			Rejected []any `json:"rejected"`
		} `json:"customApiKeyResponses"`
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return APIKeyUnknown
	}
	if cfg.CustomAPIKeyResponses == nil {
		return APIKeyPending
	}
	want := normalizeAPIKey(apiKey)
	has := func(list []any) bool {
		for _, v := range list {
			if s, ok := v.(string); ok && s == want {
				return true
			}
		}
		return false
	}
	// Approved is consulted first, as Claude Code does.
	if has(cfg.CustomAPIKeyResponses.Approved) {
		return APIKeyApproved
	}
	if has(cfg.CustomAPIKeyResponses.Rejected) {
		return APIKeyRejected
	}
	return APIKeyPending
}

// APIKeyApprovalRemedy is the one instruction every surface of this gate gives.
const APIKeyApprovalRemedy = "run `claude` once in a terminal with the same ANTHROPIC_API_KEY and answer " +
	"\"Detected a custom API key\" (Yes to bill the key, No to use your Claude login); " +
	"Claude Code remembers the answer for that key"

// APIKeyApprovalLine is the `pogo doctor --check` row for the gate: a status
// ("pass" or "warn") and a detail. configPath is named in the detail so the
// reader knows which profile was consulted; the key itself never is.
//
// Doctor runs in the caller's shell, and agents inherit pogod's environment,
// not the shell's — so a pass here is a statement about this shell's key. The
// spawn-time watch in the trust hook is the check that sees the real one.
func APIKeyApprovalLine(state APIKeyApproval, configPath string) (status, detail string) {
	switch state {
	case APIKeyUnset:
		return "pass", "ANTHROPIC_API_KEY not set in this shell — no custom-API-key prompt"
	case APIKeyApproved:
		return "pass", fmt.Sprintf("ANTHROPIC_API_KEY is approved in %s", configPath)
	case APIKeyRejected:
		return "pass", fmt.Sprintf("ANTHROPIC_API_KEY was answered No in %s — Claude Code ignores it and uses the profile's login", configPath)
	case APIKeyPending:
		return "warn", fmt.Sprintf("ANTHROPIC_API_KEY is set but %s has no answer for it, so every Claude agent will stop at "+
			"\"Detected a custom API key\" (default No) and never take its prompt — %s", configPath, APIKeyApprovalRemedy)
	default:
		return "warn", fmt.Sprintf("could not read customApiKeyResponses from %s — cannot tell whether Claude agents will stop at \"Detected a custom API key\"", configPath)
	}
}

// CheckAPIKeyApproval reads this process's environment and Claude Code's
// global config and classifies the key. The doctor's entry point.
func CheckAPIKeyApproval() (APIKeyApproval, string) {
	home, _ := os.UserHomeDir()
	path := ClaudeGlobalConfigPath(os.Getenv, home)
	key := os.Getenv("ANTHROPIC_API_KEY")
	if strings.TrimSpace(key) == "" {
		return APIKeyUnset, path
	}
	data, err := os.ReadFile(path)
	return ClassifyAPIKeyApproval(key, data, err), path
}
