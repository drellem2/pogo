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
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/ghtoken"
)

// Answer is one PR lookup: what GitHub said, and which repository it was
// asked about.
//
// The repository half exists because gh, left to itself, does not necessarily
// ask origin. With only its working directory to go on it picks a base repo by
// its own rules — a `gh repo set-default`, then an `upstream` or `github`
// remote, ahead of origin — so a clone with a second remote can be told "no
// PR" about a branch whose open PR is on origin (drellem2/pogo#259). Lookup
// therefore pins the question to origin with -R whenever it can read origin,
// and Answer says whether it could.
type Answer struct {
	// Number and State describe the PR whose head is the branch: State is
	// "OPEN", "MERGED" or "CLOSED". Both are zero when the branch has no PR.
	Number int
	State  string
	// Repo is the OWNER/REPO the lookup was pinned to with -R. Empty when it
	// was not pinned; Unpinned then says why.
	Repo string
	// Unpinned is why the lookup could not be pinned to origin, in which case
	// it ran as it did before pinning existed and gh chose the repository.
	Unpinned string
}

// Outcome is the answer in one word-ish token for an event or mail line:
// "open:#N", "not_open:<STATE>", or "none" for a branch with no PR at all.
func (a Answer) Outcome() string {
	switch {
	case a.Number > 0 && strings.EqualFold(a.State, "OPEN"):
		return fmt.Sprintf("open:#%d", a.Number)
	case a.Number > 0 || a.State != "":
		return "not_open:" + a.State
	default:
		return "none"
	}
}

// RepoNote names the repository the question went to: "repo:OWNER/REPO" when
// pinned, "repo:unpinned (<why>)" when gh chose, and "" when neither is known
// (a probe that is not Lookup, such as a test's).
func (a Answer) RepoNote() string {
	switch {
	case a.Repo != "":
		return "repo:" + a.Repo
	case a.Unpinned != "":
		return "repo:unpinned (" + a.Unpinned + ")"
	default:
		return ""
	}
}

// Ask looks up the GitHub PR whose head is branch, asking the repository dir's
// origin remote points at (see Answer for why that is pinned rather than left
// to gh). A branch with no PR at all is a zero Number and State with a nil
// error; anything else that goes wrong (gh not installed, no network, output
// drift, the timeout) is returned as an error, with the repository half of the
// Answer still filled in.
//
// AN ORIGIN THAT CANNOT BE READ OR PARSED DEGRADES TO THE UNPINNED CALL, never
// to "no PR": the reason lands on Answer.Unpinned and gh is asked exactly as
// it was before pinning existed. Pinning may only make the question more
// precise; it must not be able to invent an answer.
func Ask(dir, branch string, timeout time.Duration) (Answer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var a Answer
	args := []string{"pr", "view", branch, "--json", "state,number"}
	if repo, err := originRepo(ctx, dir); err != nil {
		a.Unpinned = err.Error()
	} else {
		a.Repo = repo
		args = append(args, "-R", repo)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = ghtoken.ChildEnvContext(ctx, append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"))
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			// gh exits 1 with this message when the branch simply has no
			// PR — a normal state for internal mg-track branches, not a
			// lookup failure.
			if strings.Contains(strings.ToLower(stderr), "no pull requests found") {
				return a, nil
			}
			return a, fmt.Errorf("gh pr view %s: %s: %w", branch, stderr, err)
		}
		return a, fmt.Errorf("gh pr view %s: %w", branch, err)
	}
	var pr struct {
		State  string `json:"state"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return a, fmt.Errorf("parse gh pr view output: %w", err)
	}
	a.Number, a.State = pr.Number, pr.State
	return a, nil
}

// Lookup returns the number and state ("OPEN", "MERGED", "CLOSED") of the
// GitHub PR whose head is branch, in the repository dir's origin points at. A
// branch with no PR at all is reported as (0, "", nil); anything else that
// goes wrong is returned as an error. See Ask, which also says which
// repository was asked.
func Lookup(dir, branch string, timeout time.Duration) (int, string, error) {
	a, err := Ask(dir, branch, timeout)
	return a.Number, a.State, err
}

// originRepo reads dir's origin URL and returns it as the OWNER/REPO gh's -R
// takes. It is a local read — no network.
func originRepo(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return "", fmt.Errorf("git remote get-url origin: %s", msg)
			}
		}
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return ParseGitHubRemote(strings.TrimSpace(string(out)))
}

// githubHosts are the hosts ParseGitHubRemote will pin to. Anything else — a
// GitHub Enterprise host, or an ssh alias from ~/.ssh/config such as
// "github-work" — is left to gh, which resolves those itself and which an -R
// built from the raw host name would break.
var githubHosts = map[string]bool{"github.com": true, "www.github.com": true, "ssh.github.com": true}

var repoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// redactURL drops any userinfo from a remote URL before it is quoted in an
// error. Errors from ParseGitHubRemote reach Answer.Unpinned and from there
// durable places — the stranded event log, alert mail — and token-in-URL
// origins (https://x-access-token:<token>@github.com/...) are the normal
// shape of GitHub App and CI clones. Only the host and path are ever quoted.
func redactURL(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		auth := rest
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			auth = rest[:slash]
		}
		if at := strings.LastIndexByte(auth, '@'); at >= 0 {
			return url[:i+3] + "<redacted>@" + rest[at+1:]
		}
		return url
	}
	if c := strings.IndexByte(url, ':'); c > 0 && !strings.Contains(url[:c], "/") {
		if at := strings.LastIndexByte(url[:c], '@'); at >= 0 {
			return "<redacted>@" + url[at+1:]
		}
	}
	return url
}

// ParseGitHubRemote turns a github.com remote URL into OWNER/REPO. It accepts
// the shapes git itself writes: https://[user@]github.com/OWNER/REPO[.git],
// ssh://[user@]github.com[:port]/OWNER/REPO[.git], git://github.com/...,
// and the scp-like [user@]github.com:OWNER/REPO[.git]. Anything else is an
// error, and the caller falls back to an unpinned lookup.
//
// Its errors never quote the URL's userinfo (see redactURL).
func ParseGitHubRemote(url string) (string, error) {
	shown := redactURL(url)
	var host, path string
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			return "", fmt.Errorf("origin %q has no repository path", shown)
		}
		host, path = rest[:slash], rest[slash+1:]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		if c := strings.IndexByte(host, ':'); c >= 0 {
			host = host[:c]
		}
	} else if c := strings.IndexByte(url, ':'); c > 0 && !strings.Contains(url[:c], "/") {
		host, path = url[:c], url[c+1:]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
	} else {
		return "", fmt.Errorf("origin %q is not a URL gh can be pinned to", shown)
	}
	if !githubHosts[strings.ToLower(host)] {
		return "", fmt.Errorf("origin host %q is not github.com", host)
	}
	path = strings.TrimSuffix(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	owner, repo, ok := strings.Cut(path, "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(repo) {
		return "", fmt.Errorf("origin %q does not name OWNER/REPO", shown)
	}
	return owner + "/" + repo, nil
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
