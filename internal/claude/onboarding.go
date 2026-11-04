package claude

// Seeding Claude Code's first-run onboarding flag (drellem2/pogo#286).
//
// A profile that has never been through Claude Code's first-run flow opens on
// the theme picker ("Choose the text style that looks best with your
// terminal"), and nothing in pogo answers it: the agent sits there until the
// initial-nudge timeout and never reaches its composer. pogod's auth preflight
// cannot see this. `claude auth status` reads the credential, not the
// onboarding state, and a profile authenticated only through
// CLAUDE_CODE_OAUTH_TOKEN is logged in without ever having run the first-run
// flow. Measured with claude 2.1.318 in a fresh HOME carrying only that token:
// `auth status` printed loggedIn:true, and the TUI stopped on the theme picker.
//
// The remedy is the one Orc uses (docs/orc-comparative-study-2026-07-28.md
// §5.3): MERGE hasCompletedOnboarding:true into the profile's .claude.json
// before the harness starts. Merge, never overwrite. Claude keeps the user's
// project history, trust answers, API-key approvals and more in that file, and
// several live agents share it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// onboardingKey is Claude Code's own name for "the first-run flow is done".
// Claude owns the key and does not document it as a contract. If a release
// renames it, the seed stops working and the theme picker comes back.
// TestClaudeOnboardingSeedSkipsThemePicker is the tripwire for that.
const onboardingKey = "hasCompletedOnboarding"

// SeedOnboarding is the Provider's ProfileSeedHook. env is the environment
// the harness will run with, in exec.Cmd form (on duplicates the last one
// wins). It merges hasCompletedOnboarding:true into the .claude.json Claude
// Code will read under that environment: $CLAUDE_CONFIG_DIR/.claude.json when
// that is set, else $HOME/.claude.json.
func SeedOnboarding(env []string) error {
	getenv := envLookup(env)
	home := getenv("HOME")
	if home == "" && getenv("CLAUDE_CONFIG_DIR") == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no HOME to locate .claude.json: %w", err)
		}
		home = h
	}
	_, err := MergeOnboardingComplete(ClaudeGlobalConfigPath(getenv, home))
	return err
}

// envLookup returns a getenv over an exec-style env slice, with the same
// last-assignment-wins rule exec.Cmd applies.
func envLookup(env []string) func(string) string {
	return func(key string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
				return v
			}
		}
		return ""
	}
}

// MergeOnboardingComplete makes sure the Claude Code config at path has
// hasCompletedOnboarding set to true, and reports whether it wrote anything.
//
// The rules exist so the merge cannot destroy what it merges into:
//   - A file that already says true is not touched. That is the steady state
//     on any profile that has been used once, so a fleet spawn rewrites
//     nothing.
//   - A missing file is created holding only the key.
//   - Every other top-level key is kept, value byte for byte. Go re-sorts the
//     keys, which Claude does not care about.
//   - A file that does not parse as a JSON object is left alone and an error
//     returned. Rewriting a file pogo cannot read would lose its contents.
//   - The write goes to a temp file in the same directory and is renamed into
//     place, so Claude never reads a half-written file. A symlinked
//     .claude.json (dotfile managers) is written through, so the link stays a
//     link. The file's permission bits are kept.
//
// One window is left open: Claude can write the file between pogo's read and
// pogo's rename, and that write is lost. It can happen only on the one spawn
// that finds the key missing, because the first write sets it.
func MergeOnboardingComplete(path string) (bool, error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}

	mode := fs.FileMode(0o600)
	cfg := map[string]json.RawMessage{}
	data, err := os.ReadFile(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A fresh profile. Claude creates this file itself on first run, so
		// pogo creating it holds nothing Claude would not have written.
	case err != nil:
		return false, fmt.Errorf("read %s: %w", target, err)
	default:
		if fi, err := os.Stat(target); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := json.Unmarshal(data, &cfg); err != nil || cfg == nil {
			return false, fmt.Errorf("%s is not a JSON object; leaving it untouched: %v", target, err)
		}
		if v, ok := cfg[onboardingKey]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("true")) {
			return false, nil
		}
	}

	cfg[onboardingKey] = json.RawMessage("true")
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", target, err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".claude.json.pogo-*")
	if err != nil {
		return false, fmt.Errorf("temp file beside %s: %w", target, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename has succeeded
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return false, fmt.Errorf("replace %s: %w", target, err)
	}
	return true, nil
}
