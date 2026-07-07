package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultWorker is the worker agent's DISPLAY name used when [agents]
// worker is not configured. Existing deployments are unaffected by a change to
// this value: the default-migration guard below pins their historical worker
// name into config.toml, so this flip only sets the default for fresh installs.
//
// Unlike DefaultCoordinator, this is a display value only: the worker's
// load-bearing identifiers (branch prefix polecat-, the ~/.pogo/polecats dir,
// the "polecat" agent-type/registry key, the cat- event-actor prefix, and
// POGO_ROLE=polecat) are frozen and NOT driven by this constant. See mg-6a24, mg-ce47.
const DefaultWorker = "pogocat"

// legacyCoordinatorDefault and legacyWorkerDefault are the FROZEN historical
// role names the migration guard pins into existing installs. They are
// deliberately DISTINCT symbols from the live DefaultCoordinator / DefaultWorker
// consts: the guard must pin a fixed historical value, not whatever the shipped
// default happens to be today. If the guard read the live consts, the gated
// flavor-rename flip (mg-ce47) — which changes exactly those consts — would
// defeat the guard, pinning the NEW name on both a same-release install and a
// version-skip upgrade. Keeping these literal and separate is what decouples the
// pinned value from the flip. Do NOT redefine these in terms of Default*.
const (
	legacyCoordinatorDefault = "mayor"
	legacyWorkerDefault      = "polecat"
)

// roleDefault pairs an [agents] role key with the frozen historical role name
// the guard pins for it. The default-migration guard writes these into
// config.toml on existing installs so a later change to the corresponding
// Default* constant (the gated flavor-rename flip, mg-ce47) cannot silently
// rename an install's roles.
type roleDefault struct {
	key     string // the [agents] key, e.g. "coordinator"
	current string // the frozen historical name to pin — a fixed legacy literal, NOT today's live Default*
}

// roleDefaults lists every [agents] role key the guard protects. It is generic
// on purpose: the guard covers the coordinator AND the worker (and any future
// role key added here), because mg-71ea shipped the coordinator key with no
// migration guard, so a coordinator default-flip is just as unsafe as a worker
// one for existing installs. The pinned values are the FROZEN legacy literals,
// not the live Default* consts, so a future flip cannot leak through the guard.
func roleDefaults() []roleDefault {
	return []roleDefault{
		{key: "coordinator", current: legacyCoordinatorDefault},
		{key: "worker", current: legacyWorkerDefault},
	}
}

// PinResult reports what PinRoleDefaultsIfExistingInstall did.
type PinResult struct {
	// Pinned lists the [agents] role keys newly written to config.toml, in the
	// order roleDefaults declares them. Empty when nothing needed pinning
	// (fresh install, or every key already present).
	Pinned []string
	// Path is the config file that was (or would have been) written.
	Path string
}

// promptStampMarkers are the first-line stamp prefixes InstallPrompts writes on
// every installed prompt/config file. Kept deliberately in sync with the
// canonical definitions in internal/agent/prompt.go (promptStampPrefix,
// promptStampPrefixTOML, promptHashPrefix, promptHashPrefixTOML). The config
// package cannot import agent — agent imports config — so the recognizable
// prefixes are duplicated here for the existing-install probe only.
var promptStampMarkers = []string{
	"<!-- pogo-prompt: ",
	"# pogo-prompt: ",
	"<!-- pogo-prompt-hash: ",
	"# pogo-prompt-hash: ",
}

// IsExistingInstall reports whether pogo has already been set up on this
// machine. It mirrors the signal pogod already trusts to gate prompt refresh:
//
//   - Primary: a config file exists at ConfigFilePath (Load would set Source).
//   - Fallback: at least one stamped prompt exists under $POGO_HOME/agents/,
//     covering installs that predate config.toml.
//
// A fresh install has neither signal. Callers that run this AFTER InstallPrompts
// (which creates stamped prompts) must snapshot the result BEFORE that call, or
// a fresh machine reads as existing — see cmd/pogo install.
func IsExistingInstall() bool {
	if path := ConfigFilePath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return hasStampedPrompt()
}

// hasStampedPrompt reports whether $POGO_HOME/agents/ holds at least one file
// carrying a pogo-prompt stamp — the fallback existing-install signal for
// installs predating config.toml.
func hasStampedPrompt() bool {
	agentsDir := filepath.Join(PogoHome(), "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if fileHasPromptStamp(filepath.Join(agentsDir, e.Name())) {
			return true
		}
	}
	return false
}

// fileHasPromptStamp reports whether the first line of path is a pogo-prompt
// stamp. It reads only the first line's worth of bytes.
func fileHasPromptStamp(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	firstLine, _, _ := strings.Cut(string(buf[:n]), "\n")
	for _, m := range promptStampMarkers {
		if strings.HasPrefix(firstLine, m) {
			return true
		}
	}
	return false
}

// PinRoleDefaultsIfExistingInstall pins the current role-name defaults into
// config.toml on an existing install, so a future change to a Default* constant
// (the gated flavor-rename flip, mg-ce47) cannot silently rename that install's
// roles. On a fresh install it is a no-op — fresh installs are meant to adopt
// the new defaults.
//
// It is idempotent: a role key already present under [agents] is left untouched
// (its presence is the durable done-signal), so re-running never rewrites a key
// or clobbers a value the operator set. Absent keys are appended without
// reformatting the rest of the file. The guard MUST roll out to existing
// installs before the default-flip ships; once flipped, the original name is
// unrecoverable if it was never pinned.
//
// existing is the IsExistingInstall determination, passed in rather than probed
// here so callers can snapshot it before InstallPrompts writes fresh prompts.
func PinRoleDefaultsIfExistingInstall(existing bool) (PinResult, error) {
	if !existing {
		return PinResult{}, nil
	}
	path := ConfigFilePath()
	if path == "" {
		return PinResult{}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return PinResult{Path: path}, err
	}
	content := string(data)

	present := agentsSectionKeys(content)
	var toPin []roleDefault
	for _, rd := range roleDefaults() {
		if !present[rd.key] {
			toPin = append(toPin, rd)
		}
	}
	if len(toPin) == 0 {
		return PinResult{Path: path}, nil
	}

	newContent := appendAgentsKeys(content, toPin)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return PinResult{Path: path}, err
	}
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return PinResult{Path: path}, err
	}

	pinned := make([]string, len(toPin))
	for i, rd := range toPin {
		pinned[i] = rd.key
	}
	return PinResult{Pinned: pinned, Path: path}, nil
}

// agentsSectionKeys returns the set of keys already present under the top-level
// [agents] table in content. Sub-tables like [agents.crew] are deliberately
// excluded — their keys are a different namespace. Section detection matches
// loadConfigFile's line-based parser so the two agree on what "[agents]" means.
func agentsSectionKeys(content string) map[string]bool {
	keys := map[string]bool{}
	section := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}
		if section != "agents" {
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			keys[strings.TrimSpace(line[:i])] = true
		}
	}
	return keys
}

// appendAgentsKeys returns content with the given role keys added under the
// [agents] table. If an [agents] header already exists, the keys are inserted
// right after it (keeping them inside the table, ahead of any [agents.*]
// sub-tables); otherwise a fresh [agents] block is appended. The rest of the
// file is left byte-for-byte unchanged.
func appendAgentsKeys(content string, toPin []roleDefault) string {
	keyLines := make([]string, len(toPin))
	for i, rd := range toPin {
		keyLines[i] = fmt.Sprintf("%s = %q", rd.key, rd.current)
	}
	const note = "# pinned by pogo default-migration guard (mg-7d95) — keeps this existing install"
	const note2 = "# on its current role names if a future pogo release changes the shipped defaults."

	lines := strings.Split(content, "\n")
	agentsIdx := -1
	for i, raw := range lines {
		if strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "[]")) == "agents" &&
			strings.HasPrefix(strings.TrimSpace(raw), "[") {
			agentsIdx = i
			break
		}
	}

	if agentsIdx >= 0 {
		out := make([]string, 0, len(lines)+len(keyLines)+2)
		out = append(out, lines[:agentsIdx+1]...)
		out = append(out, note, note2)
		out = append(out, keyLines...)
		out = append(out, lines[agentsIdx+1:]...)
		return strings.Join(out, "\n")
	}

	var b strings.Builder
	b.WriteString(content)
	if content != "" {
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(note + "\n")
	b.WriteString(note2 + "\n")
	b.WriteString("[agents]\n")
	for _, kl := range keyLines {
		b.WriteString(kl + "\n")
	}
	return b.String()
}
