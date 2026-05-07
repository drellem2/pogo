package refinery

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// defaultMaxAttempts is the fallback retry budget when no per-repo
// max_attempts is configured. Bumped from 3 → 7 to absorb the ff-only
// retry race on repos whose CI auto-pushes a version-bump commit to
// main between our fetch and push (gh-issue #13). The retry is cheap
// when paired with [gates] skip_on_retry = true.
const defaultMaxAttempts = 7

// processMerge runs the full merge pipeline for a single MR:
// 1. Ensure worktree exists for the repo
// 2. Fetch, checkout branch, rebase onto latest target
// 3. Run quality gates on rebased code
// 4. Fast-forward merge to target ref
// 5. Push
// 6. Run the per-repo deploy hook (if configured) against the just-merged commit
//
// If another polecat merges to the target between our rebase and push,
// the ff-only merge or push will fail. We retry up to maxAttempts times
// (default 7, configurable via [gates] max_attempts) with a fresh
// fetch+rebase+(gates)+merge+push cycle. When [gates] skip_on_retry is
// set, attempts after the first skip the quality-gate phase — gates
// already passed on near-identical code; only the version-bump commit
// from main differs.
//
// Emits refinery_merge_attempted, refinery_merged, refinery_merge_failed,
// and (when a deploy hook runs) refinery_deploy_* events. Emission is
// best-effort and never propagates errors — see internal/events.Emit.
//
// Returns the captured gate output, a deploy error string (empty when no
// deploy ran or it succeeded — populates MergeRequest.DeployError), and the
// merge error (nil on success). Deploy failure does NOT cause processMerge
// to return an error: the merge has already landed remotely.
func (r *Refinery) processMerge(mr *MergeRequest) (string, string, error) {
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return "", "", fmt.Errorf("worktree setup: %w", err)
	}

	cfg := r.loadConfig(wtDir, mr.RepoPath)
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	skipGatesOnRetry := cfg.SkipGatesOnRetry

	var gateOutput string
	startTime := time.Now()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			log.Printf("refinery: MR %s step=retry attempt=%d/%d", mr.ID, attempt, maxAttempts)
		}

		emitMergeAttempted(mr, attempt)

		skipGates := skipGatesOnRetry && attempt > 1
		output, stage, sha, attemptErr := r.attemptMerge(wtDir, mr, attempt, skipGates)
		gateOutput = output
		if attemptErr == nil {
			emitMerged(mr, attempt, sha, time.Since(startTime).Seconds())
			// Run the per-repo post-merge deploy hook against the refinery's
			// clone (which now has the merged commit on the target ref). The
			// hook owns refreshing runtime snapshots like ~/.pogo/<repo>/bin/
			// so they reflect the just-merged code. Failure is reported via
			// DeployError + event but does not unwind the merge.
			deployErr := r.runDeploy(wtDir, mr)
			return gateOutput, deployErr, nil
		}

		retryRemaining := attempt < maxAttempts && isRetryable(attemptErr)
		emitMergeFailed(mr, attempt, stage, attemptErr, !retryRemaining, gateOutput)

		if retryRemaining {
			log.Printf("refinery: MR %s attempt %d failed (will retry): %v", mr.ID, attempt, attemptErr)
			continue
		}
		return gateOutput, "", attemptErr
	}
	finalErr := fmt.Errorf("merge failed after %d attempts", maxAttempts)
	emitMergeFailed(mr, maxAttempts, "unknown", finalErr, true, gateOutput)
	return gateOutput, "", finalErr
}

// attemptMerge runs a single fetch→rebase→gates→merge→push cycle. Returns
// the captured gate output, the pipeline stage that ran (or failed), the
// merge commit SHA on success (empty otherwise), and any error.
//
// When skipGates is true, the quality-gate phase is bypassed — used on
// retries when [gates] skip_on_retry is set, on the principle that gates
// already passed on near-identical code and only the version-bump commit
// from main differs.
func (r *Refinery) attemptMerge(wtDir string, mr *MergeRequest, attempt int, skipGates bool) (output string, stage string, sha string, err error) {
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

	// Run quality gates (on the rebased branch — tests what will actually
	// land). On retries with skip_on_retry set, bypass: gates already
	// passed on attempt 1 over near-identical code; the only change is
	// the version-bump commit fetched from main.
	var gateOutput string
	if skipGates {
		log.Printf("refinery: MR %s step=quality-gates attempt=%d skipped (skip_on_retry=true)", mr.ID, attempt)
		gateOutput = "(quality gates skipped on retry — skip_on_retry=true)"
	} else {
		log.Printf("refinery: MR %s step=quality-gates attempt=%d", mr.ID, attempt)
		out, gates, qerr := r.runQualityGates(wtDir, mr.RepoPath)
		gateOutput = out
		if qerr != nil {
			return gateOutput, gateStage(gates), "", fmt.Errorf("quality gate: %w", qerr)
		}
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
		// Auth failures don't recover on retry — surface the actionable
		// error immediately rather than burning attempts.
		if isAuthFailure(out) {
			return gateOutput, "push", "", formatPushAuthError(out)
		}
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
	cfg := r.loadConfig(wtDir, repoPath)
	if len(cfg.Gates) > 0 {
		return cfg.Gates
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

// loadConfig returns the merged refinery config for a repo. Worktree
// values win on a per-field basis, with origin filling in fields the
// worktree does not set. Used for both per-merge knobs (max_attempts,
// skip_on_retry) and the gate/deploy lookups.
func (r *Refinery) loadConfig(wtDir, repoPath string) refineryConfig {
	wt := parseRefineryConfig(filepath.Join(wtDir, ".pogo", "refinery.toml"))
	orig := parseRefineryConfig(filepath.Join(repoPath, ".pogo", "refinery.toml"))
	if len(wt.Gates) == 0 {
		wt.Gates = orig.Gates
	}
	if wt.DeployCommand == "" {
		wt.DeployCommand = orig.DeployCommand
	}
	if wt.MaxAttempts == 0 {
		wt.MaxAttempts = orig.MaxAttempts
	}
	if !wt.SkipGatesOnRetry {
		wt.SkipGatesOnRetry = orig.SkipGatesOnRetry
	}
	return wt
}

// refineryConfig holds parsed values from a .pogo/refinery.toml file.
type refineryConfig struct {
	Gates            []string
	DeployCommand    string
	MaxAttempts      int  // [gates] max_attempts — 0 means use defaultMaxAttempts
	SkipGatesOnRetry bool // [gates] skip_on_retry — bypass gates on attempt > 1
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
	return parseRefineryConfig(path).Gates
}

// parseRefineryConfig reads a .pogo/refinery.toml and extracts all known
// configuration. Recognized sections:
//
//	[gates]
//	commands       = ["./build.sh", "./test.sh"]
//	max_attempts   = 7      # ff-only retry budget; default 7 if omitted
//	skip_on_retry  = true   # bypass gates on attempts > 1 (race recovery)
//
//	[deploy]
//	command = "./deploy.sh"
//
// Or simpler top-level keys:
//
//	quality_gate = "./build.sh"
//
// Returns a zero-value config when the file is missing or unreadable;
// missing sections are not an error.
func parseRefineryConfig(path string) refineryConfig {
	var cfg refineryConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	section := ""

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")

		switch {
		case key == "quality_gate":
			cfg.Gates = append(cfg.Gates, val)
		case section == "gates" && key == "commands":
			// Parse simple array: ["./build.sh", "./test.sh"]
			arr := strings.Trim(val, "[]")
			for _, cmd := range strings.Split(arr, ",") {
				cmd = strings.TrimSpace(cmd)
				cmd = strings.Trim(cmd, "\"")
				if cmd != "" {
					cfg.Gates = append(cfg.Gates, cmd)
				}
			}
		case section == "gates" && key == "max_attempts":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.MaxAttempts = n
			}
		case section == "gates" && key == "skip_on_retry":
			cfg.SkipGatesOnRetry = parseTomlBool(val)
		case section == "deploy" && key == "command":
			cfg.DeployCommand = val
		}
	}

	return cfg
}

// parseTomlBool parses a TOML-ish bool from a string. Accepts true/false
// (case-insensitive) and 1/0. Anything else is treated as false.
func parseTomlBool(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// DeployCommand returns the configured post-merge deploy command for a repo,
// read from <repoPath>/.pogo/refinery.toml. Returns empty string when no
// [deploy] section is present or the file is missing — not an error, since
// most repos won't have a deploy hook.
func (r *Refinery) DeployCommand(repoPath string) string {
	return parseRefineryConfig(filepath.Join(repoPath, ".pogo", "refinery.toml")).DeployCommand
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
	// pogod runs under launchd/systemd with no TTY. Without disabling
	// interactive prompts, an HTTPS remote with no credentials makes git
	// hang forever waiting for a username on stdin. Force prompts off so
	// auth failures fail fast and we can detect them via isAuthFailure.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if err != nil {
		log.Printf("refinery: git %v failed: %s: %v", args, out, err)
	}
	return out, err
}

// authFailurePatterns match git stderr emitted when a remote requires
// credentials that pogod can't supply (no TTY, no askpass, no helper).
// Patterns are matched case-insensitively against combined stdout/stderr.
var authFailurePatterns = []string{
	"could not read username",
	"could not read password",
	"authentication failed",
	"invalid username or password",
	"terminal prompts disabled",
	"support for password authentication was removed",
}

// isAuthFailure reports whether git output indicates a credential or
// authentication failure against the remote. Such failures don't recover
// on retry — they need a user-side fix (SSH remote, credential helper,
// or GIT_ASKPASS exported into pogod's env).
func isAuthFailure(output string) bool {
	s := strings.ToLower(output)
	for _, pat := range authFailurePatterns {
		if strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

// formatPushAuthError wraps a raw git-stderr auth failure with actionable
// next-steps text. The actionable summary is at the top so it survives
// truncation; the raw git output is preserved verbatim at the bottom for
// debugging.
func formatPushAuthError(gitOutput string) error {
	return fmt.Errorf(
		"refinery push failed: git could not authenticate against the HTTPS remote.\n"+
			"pogod runs under launchd / systemd and does not see your interactive shell credentials.\n"+
			"Fix one of these:\n"+
			"  a) Switch the remote to SSH:\n"+
			"       git -C <repo> remote set-url origin git@github.com:<owner>/<repo>.git\n"+
			"  b) Configure git's credential helper for non-interactive use:\n"+
			"       git config --global credential.helper osxkeychain   # macOS\n"+
			"       git config --global credential.helper store         # Linux/BSD\n"+
			"       gh auth setup-git\n"+
			"  c) Export GIT_ASKPASS in pogod's environment to a script that emits your token on stdin.\n"+
			"\n"+
			"git output:\n%s",
		gitOutput,
	)
}
