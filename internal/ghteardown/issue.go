package ghteardown

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ghIssue is the subset of `gh issue view --json` this package reads.
type ghIssue struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

// GHLookup asks GitHub for one issue's state via the `gh` CLI.
//
// Every path that is not a positive, parsed, recognised state returns
// StateUnknown WITH an error. That is the whole discipline of this function:
//
//	rc != 0                  -> Unknown  (auth expired, rate limited, offline,
//	                                      repo renamed, issue deleted, issue
//	                                      transferred out of the referenced repo)
//	rc == 0, unparseable     -> Unknown  (gh changed its output shape)
//	rc == 0, state unknown   -> Unknown  (GitHub added a state we do not model)
//	rc == 0, state OPEN      -> Open
//	rc == 0, state CLOSED    -> Closed
//
// The tempting bug is to test for "OPEN" and treat everything else as closed.
// Under that parse, every one of the failure rows above reads as "the issue is
// closed, teardown ran, all clear" — the detector reports success precisely
// when it is least entitled to. Empirically (2026-07-21) `gh issue view` exits
// 1 for both a nonexistent issue number and a nonexistent repo, so the exit
// code genuinely does carry the signal; it just has to be read.
//
// Transfers deserve their own mention because they produce the most convincing
// wrong answer. GitHub redirects a transferred issue, so a lookup usually
// follows it and answers correctly. When it does not resolve — the ref names a
// repo the issue has left, and no redirect applies — the honest answer is
// Unknown. "Gone" must never be shortened to "closed": an issue transferred to
// another repo and still open there is a teardown miss that has merely changed
// address.
func GHLookup(repo string, number int) (IssueState, error) {
	if repo == "" || number <= 0 {
		return StateUnknown, fmt.Errorf("unresolvable gh ref %q#%d — carrier cannot be checked", repo, number)
	}

	cmd := exec.Command("gh", "issue", "view", fmt.Sprint(number),
		"--repo", repo, "--json", "number,state")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return StateUnknown, fmt.Errorf("gh issue view %s#%d failed: %s", repo, number, msg)
	}

	var issue ghIssue
	if err := json.Unmarshal(out, &issue); err != nil {
		return StateUnknown, fmt.Errorf("gh issue view %s#%d: unparseable output: %w", repo, number, err)
	}

	switch strings.ToUpper(strings.TrimSpace(issue.State)) {
	case "OPEN":
		return StateOpen, nil
	case "CLOSED":
		return StateClosed, nil
	case "":
		return StateUnknown, fmt.Errorf("gh issue view %s#%d: no state in response", repo, number)
	default:
		return StateUnknown, fmt.Errorf("gh issue view %s#%d: unrecognised state %q", repo, number, issue.State)
	}
}
