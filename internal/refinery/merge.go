package refinery

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// processMerge runs the full merge pipeline for a single MR:
// 1. Ensure worktree exists for the repo
// 2. Fetch the branch
// 3. Checkout branch and run quality gates
// 4. Fast-forward merge to target ref
// 5. Push
func (r *Refinery) processMerge(mr *MergeRequest) error {
	log.Printf("refinery: MR %s step=worktree repo=%s", mr.ID, mr.RepoPath)
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return fmt.Errorf("worktree setup: %w", err)
	}

	// Fetch latest from origin
	log.Printf("refinery: MR %s step=fetch", mr.ID)
	if err := gitCmd(wtDir, "fetch", "origin"); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	// Checkout the branch to test
	log.Printf("refinery: MR %s step=checkout branch=%s", mr.ID, mr.Branch)
	if err := gitCmd(wtDir, "checkout", "-B", mr.Branch, "origin/"+mr.Branch); err != nil {
		return fmt.Errorf("checkout branch: %w", err)
	}

	// Rebase onto latest target so the branch is a direct descendant of main.
	// Polecat branches fork from main at spawn time and may be behind by the
	// time they reach the refinery.
	log.Printf("refinery: MR %s step=rebase target=%s", mr.ID, mr.TargetRef)
	if err := gitCmd(wtDir, "rebase", "origin/"+mr.TargetRef); err != nil {
		return fmt.Errorf("rebase onto %s: %w", mr.TargetRef, err)
	}

	// Run quality gates (on the rebased branch — tests what will actually land)
	log.Printf("refinery: MR %s step=quality-gates", mr.ID)
	gateOutput, err := r.runQualityGates(wtDir, mr.RepoPath)
	mr.GateOutput = gateOutput
	if err != nil {
		return fmt.Errorf("quality gate: %w", err)
	}

	// Checkout target ref for merge
	log.Printf("refinery: MR %s step=checkout-target ref=%s", mr.ID, mr.TargetRef)
	if err := gitCmd(wtDir, "checkout", mr.TargetRef); err != nil {
		return fmt.Errorf("checkout target: %w", err)
	}

	// Pull latest target
	log.Printf("refinery: MR %s step=pull-target ref=%s", mr.ID, mr.TargetRef)
	if err := gitCmd(wtDir, "pull", "--ff-only", "origin", mr.TargetRef); err != nil {
		return fmt.Errorf("pull target: %w", err)
	}

	// Fast-forward merge — guaranteed to work after rebase
	log.Printf("refinery: MR %s step=merge branch=%s -> %s", mr.ID, mr.Branch, mr.TargetRef)
	if err := gitCmd(wtDir, "merge", "--ff-only", mr.Branch); err != nil {
		return fmt.Errorf("merge (ff-only): %w", err)
	}

	// Push to origin
	log.Printf("refinery: MR %s step=push ref=%s", mr.ID, mr.TargetRef)
	if err := gitCmd(wtDir, "push", "origin", mr.TargetRef); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	return nil
}

// ensureWorktree creates or validates a worktree for the given repo.
// Uses a clone (not git-worktree) so the refinery is fully independent.
// The clone's origin remote is set to the original repo's remote URL
// so that push/fetch operations go to the actual remote (e.g. GitHub),
// not the local filesystem path.
func (r *Refinery) ensureWorktree(repoPath string) (string, error) {
	// Use the repo basename as the worktree directory name
	repoName := filepath.Base(repoPath)
	wtDir := filepath.Join(r.cfg.WorktreeDir, repoName)

	if _, err := os.Stat(filepath.Join(wtDir, ".git")); err == nil {
		// Already cloned — ensure origin points at the real remote
		if err := fixRemoteURL(wtDir, repoPath); err != nil {
			return "", fmt.Errorf("fix remote url: %w", err)
		}
		return wtDir, nil
	}

	// Clone the repo into the worktree dir
	if err := os.MkdirAll(r.cfg.WorktreeDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	cmd := exec.Command("git", "clone", repoPath, wtDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("clone: %w", err)
	}

	// Fix origin to point at the actual remote, not the local path
	if err := fixRemoteURL(wtDir, repoPath); err != nil {
		return "", fmt.Errorf("fix remote url after clone: %w", err)
	}

	return wtDir, nil
}

// fixRemoteURL ensures the worktree clone's origin points at the real remote
// (e.g. GitHub) rather than a local filesystem path. If the source repo has
// an origin remote configured, that URL is propagated to the worktree clone.
// If the source repo has no origin (e.g. a bare repo used directly), the
// worktree's origin is left unchanged.
func fixRemoteURL(wtDir, repoPath string) error {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		// Source repo has no origin remote — nothing to fix
		return nil
	}
	remoteURL := strings.TrimSpace(string(out))
	if remoteURL == "" || remoteURL == repoPath {
		// Empty or self-referential — nothing to fix
		return nil
	}
	return gitCmd(wtDir, "remote", "set-url", "origin", remoteURL)
}

// runQualityGates runs the configured quality gates for the repo.
// Checks for per-repo .pogo/refinery.toml first, then falls back to defaults.
func (r *Refinery) runQualityGates(wtDir, repoPath string) (string, error) {
	gates := r.loadGateConfig(wtDir, repoPath)
	if len(gates) == 0 {
		// No gates configured — pass by default
		return "(no quality gates configured)", nil
	}

	var allOutput strings.Builder
	for _, gate := range gates {
		allOutput.WriteString(fmt.Sprintf("=== Running: %s ===\n", gate))
		output, err := runGate(wtDir, gate)
		allOutput.WriteString(output)
		allOutput.WriteString("\n")
		if err != nil {
			allOutput.WriteString(fmt.Sprintf("FAILED: %v\n", err))
			return allOutput.String(), fmt.Errorf("%s failed: %w", gate, err)
		}
		allOutput.WriteString("PASSED\n")
	}

	return allOutput.String(), nil
}

// loadGateConfig returns the quality gate commands to run.
// Priority: per-repo .pogo/refinery.toml > default build.sh
func (r *Refinery) loadGateConfig(wtDir, repoPath string) []string {
	// Check for per-repo refinery config in the worktree
	repoConfig := filepath.Join(wtDir, ".pogo", "refinery.toml")
	if gates := parseRefineryToml(repoConfig); len(gates) > 0 {
		return gates
	}

	// Check for per-repo refinery config in the original repo
	origConfig := filepath.Join(repoPath, ".pogo", "refinery.toml")
	if gates := parseRefineryToml(origConfig); len(gates) > 0 {
		return gates
	}

	// Fall back to common scripts
	var defaults []string
	for _, script := range []string{"./build.sh", "./test.sh"} {
		if _, err := os.Stat(filepath.Join(wtDir, script)); err == nil {
			defaults = append(defaults, script)
		}
	}
	return defaults
}

// parseRefineryToml reads a .pogo/refinery.toml and extracts gate commands.
// Format:
//
//	[gates]
//	commands = ["./build.sh", "./test.sh"]
//
// Or simpler:
//
//	quality_gate = "./build.sh"
func parseRefineryToml(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var gates []string
	inGatesSection := false

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			section := strings.TrimSpace(strings.Trim(line, "[]"))
			inGatesSection = section == "gates"
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")

		if key == "quality_gate" {
			gates = append(gates, val)
		}
		if inGatesSection && key == "commands" {
			// Parse simple array: ["./build.sh", "./test.sh"]
			val = strings.Trim(val, "[]")
			for _, cmd := range strings.Split(val, ",") {
				cmd = strings.TrimSpace(cmd)
				cmd = strings.Trim(cmd, "\"")
				if cmd != "" {
					gates = append(gates, cmd)
				}
			}
		}
	}

	return gates
}

// runGate executes a single quality gate command in the worktree directory.
func runGate(wtDir, command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "POGO_REFINERY=1")

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// gitCmd runs a git command in the given directory.
func gitCmd(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
