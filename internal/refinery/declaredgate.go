package refinery

import (
	"path/filepath"
	"strings"
)

// FallbackGateCommand is the gate a self-land runs when the repo declares
// none in .pogo/refinery.toml.
const FallbackGateCommand = "./build.sh"

// DeclaredGate returns the repo's declared merge gate as ONE shell command,
// read from the same place, with the same precedence, as the refinery's own
// gate lookup (loadConfig): <wtDir>/.pogo/refinery.toml wins, and
// <repoPath>/.pogo/refinery.toml fills in when the worktree declares nothing.
// Either path may be empty.
//
// declared is false when neither file names a gate. The command is then
// FallbackGateCommand, and the caller must say it fell back.
//
// Several declared gates are joined so that they run in order and the first
// failure stops the run, as the refinery runs them. Each is wrapped in a
// subshell so that a gate containing ";" or "||" cannot swallow an earlier
// gate's failure. One gate is returned exactly as written.
//
// This exists for the enth self_land template (mg-c07a5): enth-land-gate is
// told which command to run by its caller and never reads pogo config.
// Without it, an enth repo whose refinery.toml declares
// `ENTH_REQUIRE_WEB=1 ./build.sh` was gated by plain ./build.sh, a weaker
// command than the one it declares.
func DeclaredGate(wtDir, repoPath string) (command string, declared bool) {
	// loadConfig's Gates rule, without its warnings, and never resolving an
	// empty path against the caller's working directory.
	var gates []string
	for _, dir := range []string{wtDir, repoPath} {
		if dir == "" {
			continue
		}
		if gates = parseRefineryToml(filepath.Join(dir, ".pogo", "refinery.toml")); len(gates) > 0 {
			break
		}
	}
	var kept []string
	for _, g := range gates {
		if g = strings.TrimSpace(g); g != "" {
			kept = append(kept, g)
		}
	}
	switch len(kept) {
	case 0:
		return FallbackGateCommand, false
	case 1:
		return kept[0], true
	}
	parts := make([]string, len(kept))
	for i, g := range kept {
		parts[i] = "(" + g + ")"
	}
	return strings.Join(parts, " && "), true
}
