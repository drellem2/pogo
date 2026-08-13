package homevcs

// Asking a git host whether a repository is published (mg-015c). The reason
// this question is asked at all is in publication.go; this file is the
// mechanism.
//
// WHAT IT COSTS. This is the only place in the package that leaves the machine:
// one read-only `gh repo view` per distinct remote. The package's read-only
// contract still holds for the *host* — nothing here writes, fetches or mutates
// an index — but the call is authenticated and outbound, so it carries its own
// deadline rather than inheriting doctor's context-with-no-deadline.
//
// WHY EVERY FAILURE IS AN ERROR AND NEVER A VISIBILITY. An unauthenticated or
// rate-limited `gh` cannot see a private repo *or* a public one; GitHub answers
// a missing token with "Could not resolve to a Repository", which reads like
// absence and is not. Returning any Visibility on a failure path would enrol
// this in the class of instruments that go quiet exactly when they stop being
// able to see (mg-afd0, mg-4e02) — the class it was filed against.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Visibility is a hosted repository's publication state, spelled the way the
// host spells it. The zero value means "not established" and is never a synonym
// for PRIVATE — see RepoPublication.Unknown for why that distinction is the
// whole point.
type Visibility string

const (
	VisibilityPrivate Visibility = "PRIVATE"
	VisibilityPublic  Visibility = "PUBLIC"
	// VisibilityInternal is GitHub's org-wide state. It is not world-readable
	// and it is not private either, so it gets named rather than bucketed.
	VisibilityInternal Visibility = "INTERNAL"
)

// VisibilityFunc answers whether the repository behind a git remote URL is
// published. An error means "could not establish", which the audit records as
// its own state.
type VisibilityFunc func(ctx context.Context, remote string) (Visibility, error)

// ghVisibilityTimeout bounds the single network call this package makes.
// `pogo doctor --check` passes context.Background(), so without a deadline here
// a hung TLS handshake would hang the whole checklist — a detector that can
// wedge the instrument it reports on has made itself the bigger problem.
const ghVisibilityTimeout = 15 * time.Second

// GhVisibility asks GitHub, via the `gh` CLI, whether remote's repository is
// published. It is the default resolver; tests inject their own.
func GhVisibility(ctx context.Context, remote string) (Visibility, error) {
	nwo, ok := GitHubRepo(remote)
	if !ok {
		return "", fmt.Errorf("origin %s is not a github.com remote, and pogo only knows how to ask GitHub whether a repository is published — check this host's remote by hand", remote)
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh is not on PATH, so nothing asked whether %s is published", nwo)
	}

	ctx, cancel := context.WithTimeout(ctx, ghVisibilityTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", "repo", "view", nwo, "--json", "visibility")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return "", fmt.Errorf("`gh repo view %s --json visibility` failed, so %s's publication state is unestablished: %s", nwo, nwo, firstLine(reason))
	}

	var payload struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("`gh repo view %s --json visibility` returned output this build cannot read, so %s's publication state is unestablished: %v", nwo, nwo, err)
	}
	// An absent or empty field is a changed `gh` contract, not a private repo.
	v := Visibility(strings.ToUpper(strings.TrimSpace(payload.Visibility)))
	if v == "" {
		return "", fmt.Errorf("`gh repo view %s --json visibility` reported no visibility at all, so %s's publication state is unestablished", nwo, nwo)
	}
	return v, nil
}

// GitHubRepo extracts owner/name from a git remote URL, and only when that URL
// names github.com itself. A GitHub Enterprise host or a non-GitHub forge
// returns false — better an explicit "could not establish" than an answer
// produced by pointing a github.com query at somebody else's server.
func GitHubRepo(remote string) (string, bool) {
	r := strings.TrimSpace(remote)
	if r == "" {
		return "", false
	}

	var rest string
	// scp-style: git@github.com:owner/name.git — not a URL, url.Parse reads
	// the whole thing as an opaque path.
	if strings.HasPrefix(r, "git@") || (!strings.Contains(r, "://") && strings.Contains(r, ":")) {
		host, path, found := strings.Cut(r, ":")
		if !found {
			return "", false
		}
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		if !isGitHubHost(host) {
			return "", false
		}
		rest = path
	} else {
		u, err := url.Parse(r)
		if err != nil || u.Host == "" {
			return "", false
		}
		host := u.Host
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h // strip a port
		}
		if !isGitHubHost(host) {
			return "", false
		}
		rest = u.Path
	}

	rest = strings.Trim(rest, "/")
	rest = strings.TrimSuffix(rest, ".git")
	owner, name, found := strings.Cut(rest, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return owner + "/" + name, true
}

func isGitHubHost(h string) bool {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "github.com", "www.github.com":
		return true
	}
	return false
}

// firstLine keeps a multi-line gh error from turning one checklist row into a
// paragraph. The first line is where gh puts the cause.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " (…)"
	}
	return s
}
