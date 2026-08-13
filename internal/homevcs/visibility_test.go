package homevcs

// Tests for the remote-parsing and gh-asking mechanism (mg-015c). The audit
// that uses it is tested in publication_test.go.

import (
	"context"
	"strings"
	"testing"
)

// TestGitHubRepoParsesTheRemoteFormsGitActuallyWrites. The whole detector hangs
// off turning `remote get-url origin` into something `gh repo view` accepts, and
// git writes several shapes depending on how the clone was made. Both live
// subjects on this host are spelled differently from each other.
func TestGitHubRepoParsesTheRemoteFormsGitActuallyWrites(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   string
	}{
		{"https://github.com/drellem2/pogo-config.git", "drellem2/pogo-config"},
		{"https://github.com/drellem2/pogo-config", "drellem2/pogo-config"},
		{"https://github.com/drellem2/pogo-agent-memory.git", "drellem2/pogo-agent-memory"},
		{"git@github.com:drellem2/pogo-config.git", "drellem2/pogo-config"},
		{"git@github.com:drellem2/pogo-config", "drellem2/pogo-config"},
		{"ssh://git@github.com/drellem2/pogo-config.git", "drellem2/pogo-config"},
		{"https://github.com/drellem2/pogo-config.git\n", "drellem2/pogo-config"},
	} {
		got, ok := GitHubRepo(tc.remote)
		if !ok || got != tc.want {
			t.Errorf("GitHubRepo(%q) = %q,%v; want %q,true", tc.remote, got, ok, tc.want)
		}
	}
}

// TestGitHubRepoRefusesRemotesItCannotAnswerFor. Pointing a github.com query at
// a GitLab or GitHub Enterprise remote would produce a confident answer about a
// repository nobody asked about — strictly worse than the "could not establish"
// that refusing here produces.
func TestGitHubRepoRefusesRemotesItCannotAnswerFor(t *testing.T) {
	for _, remote := range []string{
		"",
		"git@gitlab.com:someone/pogo-config.git",
		"https://gitlab.com/someone/pogo-config.git",
		"https://github.example.com/someone/pogo-config.git",
		"git@github.enterprise.internal:someone/pogo-config.git",
		"/srv/git/pogo-config.git",
		"https://github.com/drellem2",
	} {
		if got, ok := GitHubRepo(remote); ok {
			t.Errorf("GitHubRepo(%q) = %q,true; want refusal — an answer here would be about the wrong server", remote, got)
		}
	}
}

// TestGhVisibilityRefusesNonGitHubWithoutRunningAnything checks the real
// resolver's one branch that needs no network: it must not shell out to `gh`
// with a remote gh cannot answer for, and a refusal must establish nothing.
func TestGhVisibilityRefusesNonGitHubWithoutRunningAnything(t *testing.T) {
	v, err := GhVisibility(context.Background(), "git@gitlab.com:someone/pogo-config.git")
	if err == nil {
		t.Fatalf("GhVisibility returned %q with no error for a non-GitHub remote", v)
	}
	if v != "" {
		t.Errorf("Visibility = %q alongside an error; a failure establishes nothing", v)
	}
	if !strings.Contains(err.Error(), "github.com") {
		t.Errorf("error = %q, want it to name why it declined", err)
	}
}
