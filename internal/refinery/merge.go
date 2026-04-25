package refinery

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// processMerge runs the full merge pipeline for a single MR:
// 1. Ensure worktree exists for the repo
// 2. Fetch, checkout branch, rebase onto latest target
// 3. Run quality gates on rebased code
// 4. Fast-forward merge to target ref
// 5. Push
//
// If another polecat merges to the target between our rebase and push,
// the ff-only merge or push will fail. We retry up to 3 times with a
// fresh fetch+rebase+gates cycle to handle this race.
//
// Emits refinery_merge_attempted, refinery_merged, and refinery_merge_failed
// events as the pipeline progresses. Emission is best-effort and never
// propagates errors — see internal/events.Emit.
func (r *Refinery) processMerge(mr *MergeRequest) (string, error) {
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return "", fmt.Errorf("worktree setup: %w", err)
	}

	var gateOutput string
	const maxAttempts = 3
	startTime := time.Now()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			log.Printf("refinery: MR %s step=retry attempt=%d/%d", mr.ID, attempt, maxAttempts)
		}

		emitMergeAttempted(mr, attempt)

		output, stage, sha, attemptErr := r.attemptMerge(wtDir, mr, attempt)
		gateOutput = output
		if attemptErr == nil {
			emitMerged(mr, attempt, sha, time.Since(startTime).Seconds())
			return gateOutput, nil
		}

		retryRemaining := attempt < maxAttempts && isRetryable(attemptErr)
		emitMergeFailed(mr, attempt, stage, attemptErr, !retryRemaining, gateOutput)

		if retryRemaining {
			log.Printf("refinery: MR %s attempt %d failed (will retry): %v", mr.ID, attempt, attemptErr)
			continue
		}
		return gateOutput, attemptErr
	}
	finalErr := fmt.Errorf("merge failed after %d attempts", maxAttempts)
	emitMergeFailed(mr, maxAttempts, "unknown", finalErr, true, gateOutput)
	return gateOutput, finalErr
}

// attemptMerge runs a single fetch→rebase→gates→merge→push cycle. Returns
// the captured gate output, the pipeline stage that ran (or failed), the
// merge commit SHA on success (empty otherwise), and any error.
func (r *Refinery) attemptMerge(wtDir string, mr *MergeRequest, attempt int) (output string, stage string, sha string, err error) {
	// Fetch latest from origin
	log.Printf("refinery: MR %s step=fetch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "fetch", "origin"); gerr != nil {
		return "", "fetch", "", fmt.Errorf("fetch: %s: %w", out, gerr)
	}

	// Checkout the branch fresh from origin
	log.Printf("refinery: MR %s step=checkout-branch branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "checkout", "-B", mr.Branch, "origin/"+mr.Branch); gerr != nil {
		return "", "fetch", "", fmt.Errorf("checkout branch: %s: %w", out, gerr)
	}

	// Rebase onto latest target so the branch is a direct descendant of main.
	// Polecat branches fork from main at spawn time and may be behind by the
	// time they reach the refinery.
	log.Printf("refinery: MR %s step=rebase target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "rebase", "origin/"+mr.TargetRef); gerr != nil {
		// Abort the failed rebase to leave worktree in a clean state
		gitCmdOutput(wtDir, "rebase", "--abort")
		rebaseErr := fmt.Errorf("rebase onto %s: %s: %w", mr.TargetRef, out, gerr)
		// "invalid upstream" can be transient — e.g. the target branch
		// hasn't been fetched yet or the ref is missing from the clone.
		// Treat it as retryable so a fresh fetch gets another chance.
		if strings.Contains(out, "invalid upstream") {
			return "", "rebase", "", &retryableError{rebaseErr}
		}
		return "", "rebase", "", rebaseErr
	}

	// Run quality gates (on the rebased branch — tests what will actually land)
	log.Printf("refinery: MR %s step=quality-gates attempt=%d", mr.ID, attempt)
	gateOutput, gates, qerr := r.runQualityGates(wtDir, mr.RepoPath)
	if qerr != nil {
		return gateOutput, gateStage(gates), "", fmt.Errorf("quality gate: %w", qerr)
	}

	// Checkout target ref for merge
	log.Printf("refinery: MR %s step=checkout-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "checkout", mr.TargetRef); gerr != nil {
		return gateOutput, "rebase", "", fmt.Errorf("checkout target: %s: %w", out, gerr)
	}

	// Pull latest target
	log.Printf("refinery: MR %s step=pull-target target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "pull", "--ff-only", "origin", mr.TargetRef); gerr != nil {
		return gateOutput, "fetch", "", fmt.Errorf("pull target: %s: %w", out, gerr)
	}

	// Fast-forward merge — guaranteed to work if target hasn't moved since fetch
	log.Printf("refinery: MR %s step=merge branch=%s attempt=%d", mr.ID, mr.Branch, attempt)
	if out, gerr := gitCmdOutput(wtDir, "merge", "--ff-only", mr.Branch); gerr != nil {
		return gateOutput, "rebase", "", &retryableError{fmt.Errorf("merge (ff-only): %s: %w", out, gerr)}
	}

	// Push to origin
	log.Printf("refinery: MR %s step=push target=%s attempt=%d", mr.ID, mr.TargetRef, attempt)
	if out, gerr := gitCmdOutput(wtDir, "push", "origin", mr.TargetRef); gerr != nil {
		return gateOutput, "push", "", &retryableError{fmt.Errorf("push: %s: %w", out, gerr)}
	}

	// Capture the merge commit SHA (HEAD on target after fast-forward).
	// Best-effort: if rev-parse fails, return empty SHA — the merge already
	// pushed successfully.
	headSHA, _ := gitCmdOutput(wtDir, "rev-parse", "HEAD")

	return gateOutput, "push", headSHA, nil
}

// gateStage maps quality gate commands to a refinery_merge_failed stage value.
// Returns "build" for build-* commands, "test" as the conservative default.
func gateStage(gates []string) string {
	if len(gates) == 0 {
		return "test"
	}
	last := strings.ToLower(strings.TrimSpace(gates[len(gates)-1]))
	last = strings.TrimPrefix(last, "./")
	if strings.HasPrefix(last, "build") {
		return "build"
	}
	return "test"
}

// retryableError wraps errors from merge/push failures that can be retried
// with a fresh rebase (e.g. target moved because another polecat merged first).
type retryableError struct {
	err error
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
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
		// If an older clone was made without --no-local, it may have git
		// alternates linking back to the source repo. This leaks worktree
		// metadata and causes "already checked out" errors when the source
		// has linked polecat worktrees. Re-clone to fix.
		if hasAlternates(wtDir) {
			log.Printf("refinery: worktree %s has alternates (stale clone), re-cloning", wtDir)
			if err := os.RemoveAll(wtDir); err != nil {
				return "", fmt.Errorf("remove stale clone: %w", err)
			}
			// Fall through to fresh clone below
		} else {
			// Already cloned — ensure origin points at the real remote
			if err := fixRemoteURL(wtDir, repoPath); err != nil {
				return "", fmt.Errorf("fix remote url: %w", err)
			}
			return wtDir, nil
		}
	}

	// Clone the repo into the worktree dir.
	// Use --no-local to prevent git from sharing the object store via
	// alternates, which can leak worktree metadata from the source repo
	// and cause "already checked out" errors.
	if err := os.MkdirAll(r.cfg.WorktreeDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	cmd := exec.Command("git", "clone", "--no-local", repoPath, wtDir)
	cloneOutput, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("clone: %s: %w", strings.TrimSpace(string(cloneOutput)), err)
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
//
// If the source repo has no usable remote and is not a bare repo, an error is
// returned — processing with a local dev repo as origin can cause "already
// checked out" failures when the dev repo has linked polecat worktrees.
func fixRemoteURL(wtDir, repoPath string) error {
	// Try known remote names in priority order.
	for _, remote := range []string{"origin", "upstream"} {
		cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", remote)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		remoteURL := strings.TrimSpace(string(out))
		if remoteURL == "" || remoteURL == repoPath {
			continue
		}
		// Found a usable remote URL — propagate it to the clone.
		if output, err := gitCmdOutput(wtDir, "remote", "set-url", "origin", remoteURL); err != nil {
			return fmt.Errorf("%s: %w", output, err)
		}
		return nil
	}

	// No usable remote found. If the source repo is a bare repo (typical in
	// tests), the clone's origin already points at the right place.
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--is-bare-repository")
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil
	}

	return fmt.Errorf(
		"source repo %s has no remote configured; "+
			"refinery cannot process MRs from repos without a push remote "+
			"(local paths cause 'already checked out' errors with linked worktrees)",
		repoPath,
	)
}

// runQualityGates runs the configured quality gates for the repo.
// Checks for per-repo .pogo/refinery.toml first, then falls back to defaults.
// Returns combined output, the slice of gates run up to and including the
// failing one (or all of them on success), and any error.
func (r *Refinery) runQualityGates(wtDir, repoPath string) (string, []string, error) {
	gates := r.loadGateConfig(wtDir, repoPath)
	if len(gates) == 0 {
		// No gates configured — pass by default
		return "(no quality gates configured)", nil, nil
	}

	var allOutput strings.Builder
	var ran []string
	for _, gate := range gates {
		allOutput.WriteString(fmt.Sprintf("=== Running: %s ===\n", gate))
		ran = append(ran, gate)
		output, err := runGate(wtDir, gate)
		allOutput.WriteString(output)
		allOutput.WriteString("\n")
		if err != nil {
			allOutput.WriteString(fmt.Sprintf("FAILED: %v\n", err))
			return allOutput.String(), ran, fmt.Errorf("%s failed: %w", gate, err)
		}
		allOutput.WriteString("PASSED\n")
	}

	return allOutput.String(), ran, nil
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

// hasAlternates reports whether the git repo at dir has an alternates file,
// which indicates the clone shares its object store with another repo via
// hardlinks. This happens when git clone is used without --no-local on a
// local path. Shared object stores leak worktree metadata from the source
// repo, causing "already checked out" errors.
func hasAlternates(dir string) bool {
	// Alternates file lives at .git/objects/info/alternates for a regular repo.
	altPath := filepath.Join(dir, ".git", "objects", "info", "alternates")
	info, err := os.Stat(altPath)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// UnlinkWorktree removes the git worktree tracking for a polecat's worktree
// so the branch is no longer marked as "checked out" in the source repo.
// The worktree directory is left intact so the polecat process can continue
// running (it only needs git for push, which is already done by submit time).
func UnlinkWorktree(sourceRepo, worktreeDir string) error {
	if sourceRepo == "" || worktreeDir == "" {
		return nil
	}

	// Remove the .git file from the worktree (it's a pointer to the
	// source repo's .git/worktrees/<name>/ directory).
	gitFile := filepath.Join(worktreeDir, ".git")
	if err := os.Remove(gitFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove worktree .git file: %w", err)
	}

	// Prune stale worktree entries from the source repo.
	cmd := exec.Command("git", "-C", sourceRepo, "worktree", "prune")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree prune: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// gitCmdOutput runs a git command in the given directory and captures
// combined stdout/stderr output. Returns the output and any error.
// This ensures git error messages (e.g. push rejection reasons) are
// available for logging and stored in MergeRequest.Error, rather than
// being lost to pogod's stdout/stderr.
func gitCmdOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if err != nil {
		log.Printf("refinery: git %v failed: %s: %v", args, out, err)
	}
	return out, err
}
