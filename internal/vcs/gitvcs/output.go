package gitvcs

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// This file is internal/strandedwork's git runner, moved here without change
// (slice 3, mg-d72cb). Unlike RunSplit it has no budget: strandedwork
// never had one, and adding one would be a behaviour change.

// Output runs `git -C repo args...` with stdin fed from a string (ignored when
// empty) and returns stdout. A failure carries git's trimmed stderr in front of
// the exec error ("<stderr>: exit status 128") when git wrote any, and the bare
// exec error otherwise.
func Output(repo, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(exitErr.Stderr)), err)
		}
		return "", err
	}
	return string(out), nil
}

// RefExists reports whether ref names a commit in repo.
//
// `git rev-parse --verify --quiet` exits 1 for an absent ref, which exec reports
// as an error indistinguishable from "git is broken". show-ref is used instead
// precisely because its contract is narrower: an exit status of 1 means "no such
// ref" and nothing else, so a genuinely broken repository still surfaces as an
// error rather than as a clean absence.
func RefExists(repo, ref string) (bool, error) {
	cmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("show-ref %s in %s: %w", ref, repo, err)
}
