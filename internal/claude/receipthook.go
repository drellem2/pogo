package claude

// The Claude Code half of confirmed nudge delivery.
//
// Claude Code fires a UserPromptSubmit hook once per prompt the user actually
// submits — not once per keystroke, not once per queued message, once per
// submission. That makes it the exact signal pogod lacks: proof that a nudge
// written to the PTY master reached the interactive input loop rather than
// sitting unsent in the composer. The hook's whole body is one `pogo hook
// prompt-submit`, which appends a line to the agent's receipt file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// settingsRelPath is where the hook is registered. settings.local.json is
// Claude Code's documented per-project, not-checked-in settings file — the
// right shelf for a daemon's private wiring, and one this repo's .gitignore
// (and every worktree's, via .claude/) already keeps out of a diff.
var settingsRelPath = filepath.Join(".claude", "settings.local.json")

// InstallSubmitReceiptHook registers hookCommand as a UserPromptSubmit hook in
// dir's Claude Code project settings.
//
// The existing file is MERGED, not replaced: an agent's working directory can
// be a real repository whose settings.local.json holds a human's permissions
// and hooks, and clobbering those to install a delivery receipt would be a
// spectacularly bad trade. Only pogo's own hook entry is added or refreshed —
// it is recognised by its command prefix, so a re-spawn updates the path of a
// moved binary instead of stacking a second copy that would double-count every
// prompt.
func InstallSubmitReceiptHook(dir, hookCommand string) error {
	if dir == "" {
		return errors.New("no working directory to install the receipt hook into")
	}
	if hookCommand == "" {
		return errors.New("no hook command to install")
	}

	path := filepath.Join(dir, settingsRelPath)
	settings, err := readSettings(path)
	if err != nil {
		return err
	}

	if err := upsertPromptSubmitHook(settings, hookCommand); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readSettings loads dir's settings.local.json, returning an empty object when
// there is none. A file that exists but does not parse is an error rather than
// something to overwrite: it is a human's, and pogo does not get to decide that
// unreadable means disposable.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s (refusing to overwrite it): %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

// pogoHookMarker identifies pogo's own hook entry among any others the user has
// registered. Matching on the command's tail rather than its full text lets the
// binary move between spawns without leaving a stale duplicate behind.
const pogoHookMarker = "hook prompt-submit"

// upsertPromptSubmitHook adds or refreshes pogo's entry in
// settings["hooks"]["UserPromptSubmit"], preserving every other matcher group
// and every other hook in pogo's own group.
func upsertPromptSubmitHook(settings map[string]any, hookCommand string) error {
	hooks, err := childObject(settings, "hooks")
	if err != nil {
		return err
	}

	groups, _ := hooks["UserPromptSubmit"].([]any)
	if hooks["UserPromptSubmit"] != nil && groups == nil {
		return fmt.Errorf("hooks.UserPromptSubmit is not a list; refusing to rewrite it")
	}

	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		entries, _ := group["hooks"].([]any)
		for i, e := range entries {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := entry["command"].(string)
			if hasSuffix(cmd, pogoHookMarker) {
				entries[i] = newHookEntry(hookCommand)
				group["hooks"] = entries
				hooks["UserPromptSubmit"] = groups
				settings["hooks"] = hooks
				return nil
			}
		}
	}

	groups = append(groups, map[string]any{
		"hooks": []any{newHookEntry(hookCommand)},
	})
	hooks["UserPromptSubmit"] = groups
	settings["hooks"] = hooks
	return nil
}

func newHookEntry(hookCommand string) map[string]any {
	return map[string]any{
		"type":    "command",
		"command": hookCommand,
	}
}

// childObject fetches settings[key] as an object, creating it when absent and
// refusing when it holds something else.
func childObject(settings map[string]any, key string) (map[string]any, error) {
	switch v := settings[key].(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return v, nil
	default:
		return nil, fmt.Errorf("settings.%s is not an object; refusing to rewrite it", key)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
