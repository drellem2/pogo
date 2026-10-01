package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkIntakeEnv builds the sandbox `pogo check-intake` runs in for these tests:
// a stub `gh` that records which repos it was asked to list and answers each
// with one open issue (#7), a stub `mg` whose store holds one item carrying
// `gh: <carried>#7`, and an XDG_CONFIG_HOME holding configTOML (none if empty).
//
// It returns the extra environment and the file the stub gh logs repos to.
func checkIntakeEnv(t *testing.T, configTOML, carried string) ([]string, string) {
	t.Helper()
	bin := t.TempDir()
	ghLog := filepath.Join(t.TempDir(), "gh-repos.log")

	gh := `#!/bin/sh
# gh issue list --repo R ... : log R, answer one open issue.
if [ "$1" = issue ] && [ "$2" = list ]; then
  shift 2
  while [ $# -gt 0 ]; do
    if [ "$1" = --repo ]; then echo "$2" >> "` + ghLog + `"; fi
    shift
  done
  echo '[{"number":7,"title":"t","url":"https://example.invalid/7","createdAt":"2026-01-01T00:00:00Z","author":{"login":"a"}}]'
  exit 0
fi
exit 1
`
	body, err := json.Marshal("workflow: gh-issue\ngh: " + carried + "#7\n")
	if err != nil {
		t.Fatal(err)
	}
	mg := `#!/bin/sh
# Strip a leading --root DIR.
if [ "$1" = --root ]; then shift 2; fi
case "$1" in
  list)
    if [ "$2" = --status=done ]; then echo '{"id":"mg-0001","status":"done","mtime":"1"}'; fi
    exit 0 ;;
  show)
    printf '%s\n' '{"id":"mg-0001","status":"done","body":` + string(body) + `}'
    exit 0 ;;
esac
exit 1
`
	for name, script := range map[string]string{"gh": gh, "mg": mg} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	xdg := t.TempDir()
	if configTOML != "" {
		if err := os.MkdirAll(filepath.Join(xdg, "pogo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(xdg, "pogo", "config.toml"), []byte(configTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + xdg,
		// An ambient credential, so ghtoken.Ensure asks no shell for one. A
		// dummy: the stub gh never checks it.
		"GH_TOKEN=dummy-for-test",
	}, ghLog
}

func ghRepos(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func runCheckIntake(t *testing.T, env []string, args ...string) (map[string]any, string, int) {
	t.Helper()
	noServer := func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unused", 500) }
	stdout, stderr, code := runPogoEnv(t, noServer, env, append([]string{"check-intake", "--json"}, args...)...)
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("check-intake --json output did not parse: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	return out, stderr, code
}

// drellem2/pogo#121, defect 1: `[gh_intake] repos` was ignored by the CLI. With
// it set and no --repo, the configured repos must be the ones listed, and the
// source must say "config".
func TestCheckIntakeReadsGHIntakeReposFromConfig(t *testing.T) {
	env, log := checkIntakeEnv(t, "[gh_intake]\nrepos = [\"acme/widgets\"]\n", "acme/widgets")
	out, stderr, code := runCheckIntake(t, env)

	if got := ghRepos(t, log); len(got) != 1 || got[0] != "acme/widgets" {
		t.Fatalf("gh listed %v, want [acme/widgets] from [gh_intake] repos (stderr=%s)", got, stderr)
	}
	if out["repo_source"] != "config" {
		t.Errorf("repo_source = %v, want config", out["repo_source"])
	}
	if out["blind_watch_list"] != false || out["actionable"] != false {
		t.Errorf("a configured, carried watch list is not blind: %v", out)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 — the one open issue is carried", code)
	}
}

// --repo beats config, and is labelled as the flag — not as "config", which is
// what the single-input ResolveRepos called it.
func TestCheckIntakeRepoFlagBeatsConfigAndSaysSo(t *testing.T) {
	env, log := checkIntakeEnv(t, "[gh_intake]\nrepos = [\"acme/widgets\"]\n", "other/thing")
	out, _, code := runCheckIntake(t, env, "--repo", "other/thing")

	if got := ghRepos(t, log); len(got) != 1 || got[0] != "other/thing" {
		t.Fatalf("gh listed %v, want only the --repo list", got)
	}
	if out["repo_source"] != "--repo" {
		t.Errorf("repo_source = %v, want --repo", out["repo_source"])
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

// drellem2/pogo#121, defect 2: with nothing configured the scan examines no repo
// and used to exit 0 with scanned=0. It must be actionable — exit 1 — and say
// why, in the JSON and in the human report.
func TestCheckIntakeWithAnEmptyWatchListIsActionable(t *testing.T) {
	env, log := checkIntakeEnv(t, "", "acme/widgets")
	out, _, code := runCheckIntake(t, env)

	if got := ghRepos(t, log); len(got) != 0 {
		t.Fatalf("gh listed %v with nothing configured", got)
	}
	if out["blind_watch_list"] != true || out["actionable"] != true {
		t.Errorf("an empty watch list must be a blind, actionable finding: %v", out)
	}
	if out["repo_source"] != "no repos configured" {
		t.Errorf("repo_source = %v", out["repo_source"])
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1 — a detector watching nothing is the finding", code)
	}

	noServer := func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unused", 500) }
	stdout, _, code := runPogoEnv(t, noServer, env, "check-intake")
	if code != 1 || !strings.Contains(stdout, "BLIND WATCH LIST") {
		t.Errorf("human report: exit %d, want 1 with a BLIND WATCH LIST banner; got:\n%s", code, stdout)
	}
}
