package ghpr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseGitHubRemote(t *testing.T) {
	ok := map[string]string{
		"https://github.com/drellem2/pogo.git":      "drellem2/pogo",
		"https://github.com/drellem2/pogo":          "drellem2/pogo",
		"https://github.com/drellem2/pogo/":         "drellem2/pogo",
		"https://x-access-token@github.com/o/r.git": "o/r",
		"http://github.com/o/r.git":                 "o/r",
		"git@github.com:drellem2/pogo.git":          "drellem2/pogo",
		"git@github.com:drellem2/pogo":              "drellem2/pogo",
		"github.com:o/r.git":                        "o/r",
		"ssh://git@github.com/o/r.git":              "o/r",
		"ssh://git@ssh.github.com:443/o/r.git":      "o/r",
		"git://github.com/o/my.repo-name_2.git":     "o/my.repo-name_2",
		"https://GitHub.com/Owner/Repo.git":         "Owner/Repo",
	}
	for url, want := range ok {
		if got, err := ParseGitHubRemote(url); err != nil || got != want {
			t.Errorf("ParseGitHubRemote(%q) = (%q, %v), want (%q, nil)", url, got, err, want)
		}
	}
	// Every one of these must refuse, so the caller falls back to the unpinned
	// call rather than pinning to a repository it made up. An ssh alias and an
	// Enterprise host in particular: gh resolves those itself, and an -R built
	// from the raw host name would break a lookup that works today.
	for _, url := range []string{
		"",
		"/srv/git/pogo.git",
		"../pogo",
		"file:///srv/git/pogo.git",
		"git@github-work:o/r.git",
		"https://ghe.example.com/o/r.git",
		"https://gitlab.com/o/r.git",
		"https://github.com/o",
		"https://github.com/o/r/extra",
		"git@github.com:o/r name.git",
	} {
		if got, err := ParseGitHubRemote(url); err == nil {
			t.Errorf("ParseGitHubRemote(%q) = %q, want an error", url, got)
		}
	}
}

// loggingGH puts a stub gh first on PATH that records its argv (one line per
// call) and then runs script. It returns the log path.
func loggingGH(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gh.log")
	body := "#!/bin/sh\necho \"$@\" >> '" + logPath + "'\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func ghArgs(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// repoWithRemotes is a git repository with the given remotes (name -> URL).
func repoWithRemotes(t *testing.T, remotes map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for name, url := range remotes {
		run("remote", "add", name, url)
	}
	return dir
}

// TestAskPinsToOrigin: the lookup names origin's repository with -R, and an
// `upstream` remote — which gh, left to choose, prefers over origin — does not
// redirect it (drellem2/pogo#259).
func TestAskPinsToOrigin(t *testing.T) {
	dir := repoWithRemotes(t, map[string]string{
		"origin":   "git@github.com:fork-owner/pogo.git",
		"upstream": "https://github.com/drellem2/pogo.git",
	})
	logPath := loggingGH(t, `echo '{"state":"OPEN","number":218}'`)
	a, err := Ask(dir, "pr-218", time.Minute)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if a.Repo != "fork-owner/pogo" || a.Unpinned != "" || a.Number != 218 || a.Outcome() != "open:#218" {
		t.Errorf("Ask = %+v, want pinned to fork-owner/pogo with open #218", a)
	}
	if got := ghArgs(t, logPath); got != "pr view pr-218 --json state,number -R fork-owner/pogo" {
		t.Errorf("gh was called as %q, want it pinned to origin's repo", got)
	}
	if a.RepoNote() != "repo:fork-owner/pogo" {
		t.Errorf("RepoNote = %q", a.RepoNote())
	}
}

// TestAskUnparsableOriginKeepsTheUnpinnedCall: an origin that cannot be pinned
// degrades to exactly the call that existed before pinning — and gh's answer,
// whatever it is, still comes back. Above all it must not become "no PR".
func TestAskUnparsableOriginKeepsTheUnpinnedCall(t *testing.T) {
	for name, remotes := range map[string]map[string]string{
		"ssh alias": {"origin": "git@github-work:drellem2/pogo.git"},
		"no origin": {"upstream": "https://github.com/drellem2/pogo.git"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := repoWithRemotes(t, remotes)
			logPath := loggingGH(t, `echo '{"state":"OPEN","number":218}'`)
			a, err := Ask(dir, "pr-218", time.Minute)
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			if a.Number != 218 || a.Outcome() != "open:#218" {
				t.Errorf("unpinned lookup lost gh's answer: %+v", a)
			}
			if a.Repo != "" || a.Unpinned == "" || !strings.HasPrefix(a.RepoNote(), "repo:unpinned (") {
				t.Errorf("Ask = %+v, RepoNote %q; want unpinned with a reason", a, a.RepoNote())
			}
			if got := ghArgs(t, logPath); got != "pr view pr-218 --json state,number" {
				t.Errorf("gh was called as %q, want today's unpinned call", got)
			}
		})
	}
}

// TestLookupIsPinnedToo: Lookup, the refinery's entry point, runs through the
// same pin.
func TestLookupIsPinnedToo(t *testing.T) {
	dir := repoWithRemotes(t, map[string]string{
		"origin":   "https://github.com/drellem2/pogo.git",
		"upstream": "https://github.com/someone-else/pogo.git",
	})
	logPath := loggingGH(t, `echo '{"state":"MERGED","number":9}'`)
	if n, state, err := Lookup(dir, "b", time.Minute); err != nil || n != 9 || state != "MERGED" {
		t.Fatalf("Lookup = (%d, %q, %v)", n, state, err)
	}
	if got := ghArgs(t, logPath); !strings.HasSuffix(got, "-R drellem2/pogo") {
		t.Errorf("gh was called as %q, want -R drellem2/pogo", got)
	}
}

func TestAnswerOutcome(t *testing.T) {
	for a, want := range map[Answer]string{
		{}:                           "none",
		{Number: 3, State: "OPEN"}:   "open:#3",
		{Number: 3, State: "MERGED"}: "not_open:MERGED",
		{Number: 3, State: "CLOSED"}: "not_open:CLOSED",
	} {
		if got := a.Outcome(); got != want {
			t.Errorf("%+v.Outcome() = %q, want %q", a, got, want)
		}
	}
}

// TestParseGitHubRemoteNeverQuotesCredentials: a parse error lands in
// Answer.Unpinned, then in the stranded event log and alert mail. Token-in-URL
// origins are the normal shape of GitHub App and CI clones, so no error may
// carry the userinfo — whichever refusal path the URL takes.
func TestParseGitHubRemoteNeverQuotesCredentials(t *testing.T) {
	const secret = "FAKESECRET0000"
	for _, url := range []string{
		"https://x-access-token:" + secret + "@github.com/o",
		"https://x-access-token:" + secret + "@github.com",
		"https://x-access-token:" + secret + "@github.com/o/r/extra",
		"https://" + secret + "@ghe.example.com/o/r.git",
		"ssh://git:" + secret + "@github.com/o",
		"git:" + secret + "@github-work:o/r.git",
	} {
		_, err := ParseGitHubRemote(url)
		if err == nil {
			t.Errorf("ParseGitHubRemote(%q): want an error", url)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error for a credentialed URL quotes the secret: %v", err)
		}
	}
	// Positive control: the same URL shape with a well-formed path parses, so
	// the cases above fail for their path and not for the userinfo.
	if got, err := ParseGitHubRemote("https://x-access-token:" + secret + "@github.com/o/r.git"); err != nil || got != "o/r" {
		t.Errorf("credentialed well-formed URL = (%q, %v), want o/r", got, err)
	}
	if got := redactURL("https://u:p@github.com/o"); got != "https://<redacted>@github.com/o" {
		t.Errorf("redactURL = %q", got)
	}
}
