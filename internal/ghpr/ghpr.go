// Package ghpr asks GitHub, through the gh CLI, whether a branch has a pull
// request and what state it is in.
//
// It was extracted from the refinery (internal/refinery/merge.go), where it
// decides whether to push a rebased head back to a PR branch and whether to
// close a PR after a merge, so that the stranded-work detector could ask the
// same question with the same parsing (drellem2/pogo#147, mg-dbb75): a branch
// whose head is an open PR is AWAITING REVIEW, not stranded, and the detector
// could not tell the two apart.
//
// Every call is bounded by a timeout and every failure is returned rather than
// folded into an answer. What a caller does with a failure is the caller's
// decision, and the two callers decide in opposite directions on purpose: the
// refinery skips PR cosmetics (a merge must never wait on gh), while the
// stranded-work detector keeps its alert (a suppression it could not verify is
// not one it may apply).
package ghpr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/ghtoken"
)

// Lookup returns the number and state ("OPEN", "MERGED", "CLOSED") of the
// GitHub PR whose head is branch. gh infers the GitHub repo from dir's origin
// remote. A branch with no PR at all is reported as (0, "", nil); anything else
// that goes wrong (gh not installed, no network, non-GitHub remote, output
// drift, the timeout) is returned as an error.
func Lookup(dir, branch string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "state,number")
	cmd.Dir = dir
	cmd.Env = ghtoken.ChildEnv(append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"))
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			// gh exits 1 with this message when the branch simply has no
			// PR — a normal state for internal mg-track branches, not a
			// lookup failure.
			if strings.Contains(strings.ToLower(stderr), "no pull requests found") {
				return 0, "", nil
			}
			return 0, "", fmt.Errorf("gh pr view %s: %s: %w", branch, stderr, err)
		}
		return 0, "", fmt.Errorf("gh pr view %s: %w", branch, err)
	}
	var pr struct {
		State  string `json:"state"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return 0, "", fmt.Errorf("parse gh pr view output: %w", err)
	}
	return pr.Number, pr.State, nil
}

// OpenNumber returns the number of the open GitHub PR whose head is branch, or
// 0 when the branch has a PR that is not open or no PR at all. Anything else
// that goes wrong is an error. See Lookup.
func OpenNumber(dir, branch string, timeout time.Duration) (int, error) {
	num, state, err := Lookup(dir, branch, timeout)
	if err != nil || !strings.EqualFold(state, "OPEN") {
		return 0, err
	}
	return num, nil
}
