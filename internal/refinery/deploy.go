package refinery

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// The deploy hook's landing variables (enth-vcs §7). POGO_LANDED_VCS is
// "git" or "enth"; POGO_LANDED_ID is the git merge commit or the enth land's
// `new`. Both are unset when the landing is unknown. No variable carries a
// git sha for an enth MR.
const (
	envLandedVCS = "POGO_LANDED_VCS"
	envLandedID  = "POGO_LANDED_ID"
)

// runDeployHook executes the per-repo post-merge deploy command in the
// refinery's worktree. The command is run via `sh -c` with cmd.Dir = wtDir
// and POGO_REFINERY=1 in the environment (mirrors runGate), plus
// POGO_LANDED_VCS/POGO_LANDED_ID naming what landed. Any copies of those two
// inherited from pogod's own environment are dropped first, so a hook never
// reads a landing that is not this MR's. Returns the combined stdout/stderr
// output and any error from the command.
func runDeployHook(wtDir, command string, kind vcs.Kind, landedID string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = wtDir
	cmd.Env = deployEnv(os.Environ(), kind, landedID)

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// deployEnv is base with POGO_REFINERY=1 and the landing variables set.
func deployEnv(base []string, kind vcs.Kind, landedID string) []string {
	env := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if strings.HasPrefix(kv, envLandedVCS+"=") || strings.HasPrefix(kv, envLandedID+"=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "POGO_REFINERY=1")
	if landedID != "" {
		env = append(env, envLandedVCS+"="+string(kind), envLandedID+"="+landedID)
	}
	return env
}

// runDeploy looks up the configured deploy command for the MR's repo and runs
// it in the refinery's worktree. Emits refinery_deploy_attempted before the
// run and one of refinery_deployed / refinery_deploy_failed afterwards.
//
// The merge has already landed by the time this runs — failure here is
// reported (event + log + returned string) but does not roll back the merge.
// Returns a non-empty string only when a deploy was attempted and failed;
// callers populate MergeRequest.DeployError from the return. kind and
// landedID name what landed; the hook reads them as POGO_LANDED_VCS and
// POGO_LANDED_ID. They are passed rather than read off mr because the merge
// outcome is copied onto the MR only after the pipeline returns.
func (r *Refinery) runDeploy(wtDir string, mr *MergeRequest, kind vcs.Kind, landedID string) string {
	command := r.deployCommandFor(wtDir, mr.RepoPath)
	if command == "" {
		return ""
	}

	emitDeployAttempted(mr, command)
	start := time.Now()
	output, err := runDeployHook(wtDir, command, kind, landedID)
	duration := time.Since(start).Seconds()

	if err != nil {
		log.Printf("refinery: MR %s deploy failed: %v", mr.ID, err)
		emitDeployFailed(mr, err, output)
		return err.Error()
	}

	log.Printf("refinery: MR %s deployed in %.2fs", mr.ID, duration)
	emitDeployed(mr, duration)
	return ""
}

// deployCommandFor returns the deploy command to run for an MR. Priority
// follows loadGateConfig: the refinery's worktree (which has the freshly
// merged commit on the target ref) takes precedence, with the source repo as
// a fallback. The worktree-first order matters because the source repo can
// have uncommitted local edits to .pogo/refinery.toml that disagree with what
// actually landed on main.
func (r *Refinery) deployCommandFor(wtDir, repoPath string) string {
	if cmd := parseRefineryConfig(filepath.Join(wtDir, ".pogo", "refinery.toml")).DeployCommand; cmd != "" {
		return cmd
	}
	return r.DeployCommand(repoPath)
}
