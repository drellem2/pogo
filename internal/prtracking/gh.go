package prtracking

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// OpenPRs returns the open pull requests of one `owner/name` repository, in the
// shape Classify reads.
//
// One `gh` invocation per repo, not one per PR: `gh pr list --json comments`
// returns comment bodies inline (verified against drellem2/macguffin, which
// returned both comments of PR #28 with their bodies). A per-PR `gh pr view`
// loop would multiply the rate-limit cost of a sweep by the size of the repo's
// PR list for no extra field.
func OpenPRs(slug string, limit int) ([]PR, error) {
	if limit <= 0 {
		limit = 100
	}
	out, err := exec.Command("gh", "pr", "list",
		"--repo", slug,
		"--state", "open",
		"--limit", fmt.Sprint(limit),
		"--json", "number,title,headRefName,body,comments",
	).Output()
	if err != nil {
		// Returned rather than swallowed. `gh` unavailable, unauthenticated, or
		// refused by SAML is a repo we did NOT measure, and the pass records
		// that under gaps; folding it into "no open PRs" would report a clean
		// sweep over a repo nobody looked at.
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return nil, fmt.Errorf("gh pr list --repo %s: %w: %s", slug, err, stderr)
		}
		return nil, fmt.Errorf("gh pr list --repo %s: %w", slug, err)
	}
	return parseGHList(slug, out)
}

// parseGHList converts `gh pr list --json …` output into PRs.
func parseGHList(slug string, data []byte) ([]PR, error) {
	var rows []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		HeadRefName string `json:"headRefName"`
		Body        string `json:"body"`
		Comments    []struct {
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parsing gh output for %s: %w", slug, err)
	}
	prs := make([]PR, 0, len(rows))
	for _, r := range rows {
		pr := PR{
			Repo: slug, Number: r.Number,
			HeadRefName: r.HeadRefName, Title: r.Title, Body: r.Body,
		}
		for _, c := range r.Comments {
			pr.Comments = append(pr.Comments, c.Body)
		}
		prs = append(prs, pr)
	}
	return prs, nil
}
