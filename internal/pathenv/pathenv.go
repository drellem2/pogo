// Package pathenv repairs the PATH for processes spawned by pogod.
//
// When pogod is launched by launchd (macOS) or systemd (Linux) it inherits a
// minimal or empty PATH. Any child it spawns by bare command name — most
// importantly `mg` for the scheduler/refinery mail-send fallback, plus `gh` and
// `git` — then fails to resolve with "executable file not found in $PATH".
//
// Ensure rewrites the current process's PATH so that:
//   - the directory containing the running pogod binary comes first (so an `mg`
//     shipped alongside pogod resolves), then
//   - whatever PATH was inherited, then
//   - per-user toolchain dirs that exist on disk (~/.local/bin, nvm's Node bin,
//     the npm global prefix, ~/.volta/bin) — these load only in interactive
//     shells, so Node-based harnesses like pi are otherwise unresolvable under
//     launchd (gh #25), then
//   - well-known install locations as a backstop for an empty inherited PATH.
//
// EnsureExtra additionally prepends the [agents] extra_path config entries once
// config is loaded, for runtimes the automatic probing cannot discover.
//
// Because Go's os/exec resolves bare command names against the parent process's
// PATH at exec.Command time, fixing the process PATH once at startup fixes every
// subprocess pogod spawns thereafter.
package pathenv

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// fallbackDirs lists common install locations for the tools pogod shells out to
// (mg, gh, git). They are appended last so a child resolves these even when the
// daemon's inherited PATH is empty, as happens under launchd.
func fallbackDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{
		"/opt/pogo/current/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}
}

// userToolchainDirs returns per-user tool install locations that exist on
// disk. Under launchd/systemd the daemon's inherited PATH has none of the
// user's shell-init additions — nvm in particular only loads in interactive
// shells — so Node-based harnesses like pi are unresolvable without probing
// these directly (gh #25).
func userToolchainDirs(home string) []string {
	if runtime.GOOS == "windows" || home == "" {
		return nil
	}
	var dirs []string
	add := func(d string) {
		if d == "" {
			return
		}
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			dirs = append(dirs, d)
		}
	}
	add(filepath.Join(home, ".local", "bin"))
	for _, d := range nvmBinDirs(home) {
		add(d)
	}
	add(npmPrefixBinDir(home))
	add(filepath.Join(home, ".npm-global", "bin"))
	add(filepath.Join(home, ".volta", "bin"))
	return dirs
}

// nvmBinDirs returns the bin directories of every nvm-installed Node version,
// highest version first. All versions are included because a global npm
// install (like pi) lands only in the version that was active at install time
// — which need not be nvm's default alias. Highest-first means the newest
// node wins a `#!/usr/bin/env node` shebang lookup, the choice most likely to
// satisfy the engines requirement of any installed CLI. Returns nil when nvm
// is absent.
func nvmBinDirs(home string) []string {
	nvmDir := os.Getenv("NVM_DIR")
	if nvmDir == "" {
		nvmDir = filepath.Join(home, ".nvm")
	}
	versionsDir := filepath.Join(nvmDir, "versions", "node")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return nil
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "v") {
			versions = append(versions, e.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	var dirs []string
	for _, v := range versions {
		dirs = append(dirs, filepath.Join(versionsDir, v, "bin"))
	}
	return dirs
}

// versionLess orders "vX.Y.Z" strings numerically per component, so v9 < v10.
func versionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

// npmPrefixBinDir reads the npm global prefix from ~/.npmrc (the standard
// "npm-global" setup writes `prefix=...` there) and returns its bin dir.
// Reading the file instead of running `npm prefix -g` avoids the
// chicken-and-egg of needing npm on PATH to discover npm's bin dir.
func npmPrefixBinDir(home string) string {
	data, err := os.ReadFile(filepath.Join(home, ".npmrc"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "prefix" {
			continue
		}
		prefix := expandHome(strings.Trim(strings.TrimSpace(v), `"'`), home)
		if prefix == "" {
			return ""
		}
		return filepath.Join(prefix, "bin")
	}
	return ""
}

// expandHome resolves the leading-~ and $HOME forms npm and TOML configs
// commonly use for user-relative paths.
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	p = strings.ReplaceAll(p, "${HOME}", home)
	p = strings.ReplaceAll(p, "$HOME", home)
	return p
}

// Augment builds an augmented PATH value from the current PATH and the directory
// of the running executable. exeDir (if non-empty) is placed first, then the
// entries of currentPATH, then any userDirs (per-user toolchain locations, ahead
// of the generic fallbacks so a user-installed node beats a system one — the
// order an interactive shell would produce), then the fallback directories.
// Empty and duplicate entries are dropped while order is otherwise preserved.
// It is pure so it can be unit tested without touching the process environment.
func Augment(currentPATH, exeDir string, userDirs ...string) string {
	var dirs []string
	if exeDir != "" {
		dirs = append(dirs, exeDir)
	}
	if currentPATH != "" {
		dirs = append(dirs, filepath.SplitList(currentPATH)...)
	}
	dirs = append(dirs, userDirs...)
	dirs = append(dirs, fallbackDirs()...)
	return strings.Join(dedupe(dirs), string(os.PathListSeparator))
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// exeDir returns the directory containing the running executable, or "" if it
// cannot be determined (e.g. the binary was removed). os.Executable resolves
// symlinks well enough for our purposes here.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// PATH returns the augmented PATH value for the current process.
func PATH() string {
	home, _ := os.UserHomeDir()
	return Augment(os.Getenv("PATH"), exeDir(), userToolchainDirs(home)...)
}

// Ensure rewrites the current process's PATH environment variable to the
// augmented value. Call it once, early, before any subprocess is spawned.
// Idempotent: re-running it folds the already-present entries back together via
// dedupe rather than stacking them.
func Ensure() error {
	return os.Setenv("PATH", PATH())
}

// EnsureExtra prepends the given directories to the process PATH. It backs the
// [agents] extra_path config knob (and its POGO_EXTRA_PATH env override) so a
// deployment can point pogod at harness runtimes the automatic probing in
// Ensure misses (gh #25). Prepended — ahead of even the exe dir — because
// explicit config should win over every discovered location. Entries get ~ and
// $HOME expansion; empty input is a no-op. Idempotent via dedupe, like Ensure.
func EnsureExtra(dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	var all []string
	for _, d := range dirs {
		if d = strings.TrimSpace(d); d != "" {
			all = append(all, expandHome(d, home))
		}
	}
	if len(all) == 0 {
		return nil
	}
	all = append(all, filepath.SplitList(os.Getenv("PATH"))...)
	return os.Setenv("PATH", strings.Join(dedupe(all), string(os.PathListSeparator)))
}
