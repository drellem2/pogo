package refinery

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// runDeployHook executes the per-repo post-merge deploy command in the
// refinery's worktree. The command is run via `sh -c` with cmd.Dir = wtDir
// and POGO_REFINERY=1 in the environment (mirrors runGate). Returns the
// combined stdout/stderr output and any error from the command.
func runDeployHook(wtDir, command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "POGO_REFINERY=1")

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// runDeploy looks up the configured deploy command for the MR's repo and runs
// it in the refinery's worktree. Emits refinery_deploy_attempted before the
// run and one of refinery_deployed / refinery_deploy_failed afterwards.
//
// The merge has already landed by the time this runs — failure here is
// reported (event + log + returned string) but does not roll back the merge.
// Returns a non-empty string only when a deploy was attempted and failed;
// callers populate MergeRequest.DeployError from the return.
func (r *Refinery) runDeploy(wtDir string, mr *MergeRequest) string {
	command := r.deployCommandFor(wtDir, mr.RepoPath)
	if command == "" {
		return ""
	}

	emitDeployAttempted(mr, command)
	start := time.Now()
	output, err := runDeployHook(wtDir, command)
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
