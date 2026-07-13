package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInitWorktree initializes a bare-bones git repo in a temp dir and returns
// its path. Enough for `git status` / info/exclude checks — no commits needed.
func gitInitWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "-q", "-b", "main")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return string(out)
}

// TestWriteContextFilePromptGitExcludes is the core regression guard for
// mg-9de9: after the persona is injected into a git worktree, `git status`
// must NOT report it as an untracked file, so a stray `git add -A` cannot
// commit pogo's internal prompt.
func TestWriteContextFilePromptGitExcludes(t *testing.T) {
	newPromptFile := func(t *testing.T) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "prompt.md")
		if err := os.WriteFile(f, []byte("# persona\n"), 0644); err != nil {
			t.Fatal(err)
		}
		return f
	}

	cases := []struct {
		name        string
		contextFile string
		header      string
	}{
		{"codex", "AGENTS.override.md", ""},
		{"cursor", ".cursor/rules/pogo-persona.mdc", "---\nalwaysApply: true\n---\n\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := gitInitWorktree(t)
			p := &Provider{
				ID: tc.name,
				PromptInjection: PromptInjection{
					Kind:              InjectContextFile,
					ContextFile:       tc.contextFile,
					ContextFileHeader: tc.header,
				},
			}
			if err := writeContextFilePrompt(p, newPromptFile(t), dir); err != nil {
				t.Fatalf("writeContextFilePrompt: %v", err)
			}

			// The persona file exists...
			if _, err := os.Stat(filepath.Join(dir, tc.contextFile)); err != nil {
				t.Fatalf("persona not written: %v", err)
			}
			// ...but git does not see it as untracked.
			if status := gitStatusPorcelain(t, dir); strings.TrimSpace(status) != "" {
				t.Errorf("git status is dirty after injection; the persona leaked into add -A:\n%s", status)
			}
			// info/exclude carries the anchored pattern exactly once.
			exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
			if err != nil {
				t.Fatalf("read info/exclude: %v", err)
			}
			want := "/" + tc.contextFile
			if n := strings.Count(string(exclude), want); n != 1 {
				t.Errorf("info/exclude contains %q %d times, want exactly 1:\n%s", want, n, exclude)
			}
		})
	}
}

// TestGitExcludeIdempotent verifies a respawn (re-delivery of the persona) does
// not append duplicate exclude lines or a duplicate comment.
func TestGitExcludeIdempotent(t *testing.T) {
	dir := gitInitWorktree(t)
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("# persona\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &Provider{
		ID: "codex",
		PromptInjection: PromptInjection{
			Kind:        InjectContextFile,
			ContextFile: "AGENTS.override.md",
		},
	}
	for i := 0; i < 3; i++ {
		if err := writeContextFilePrompt(p, promptFile, dir); err != nil {
			t.Fatalf("writeContextFilePrompt call %d: %v", i, err)
		}
	}
	exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(exclude), "/AGENTS.override.md"); n != 1 {
		t.Errorf("pattern appears %d times after 3 injections, want 1:\n%s", n, exclude)
	}
	if n := strings.Count(string(exclude), personaExcludeComment); n != 1 {
		t.Errorf("comment appears %d times, want 1:\n%s", n, exclude)
	}
}

// TestGitExcludeTwoProvidersShareComment verifies that when two different
// persona paths are excluded in the same worktree, both patterns land under a
// single shared comment.
func TestGitExcludeTwoProvidersShareComment(t *testing.T) {
	dir := gitInitWorktree(t)
	if err := ensureWorktreeGitExcluded(dir, "AGENTS.override.md"); err != nil {
		t.Fatalf("exclude codex: %v", err)
	}
	if err := ensureWorktreeGitExcluded(dir, ".cursor/rules/pogo-persona.mdc"); err != nil {
		t.Fatalf("exclude cursor: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(exclude)
	if n := strings.Count(s, personaExcludeComment); n != 1 {
		t.Errorf("comment appears %d times, want 1:\n%s", n, s)
	}
	for _, want := range []string{"/AGENTS.override.md", "/.cursor/rules/pogo-persona.mdc"} {
		if !strings.Contains(s, want) {
			t.Errorf("info/exclude missing %q:\n%s", want, s)
		}
	}
}

// TestGitExcludePreservesExistingContent verifies pogo's block is appended
// without clobbering a user's pre-existing info/exclude entries, and that a
// file lacking a trailing newline is handled.
func TestGitExcludePreservesExistingContent(t *testing.T) {
	dir := gitInitWorktree(t)
	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	// No trailing newline, to exercise the separator branch.
	if err := os.WriteFile(excludePath, []byte("*.log\nbuild/"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktreeGitExcluded(dir, "AGENTS.override.md"); err != nil {
		t.Fatalf("exclude: %v", err)
	}
	got, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{"*.log", "build/", "/AGENTS.override.md"} {
		if !strings.Contains(s, want) {
			t.Errorf("info/exclude missing %q after append:\n%s", want, s)
		}
	}
	// The pre-existing "build/" must not have been merged onto the same line as
	// pogo's comment.
	if strings.Contains(s, "build/# pogo") {
		t.Errorf("missing newline separator before pogo block:\n%s", s)
	}
}

// TestGitExcludeNonGitDirIsBestEffort verifies that injecting into a plain
// (non-git) directory does not fail: the persona is still written, and the
// exclude step reports an error the caller logs rather than a fatal one.
func TestGitExcludeNonGitDirIsBestEffort(t *testing.T) {
	dir := t.TempDir() // not a git repo
	// The exclude helper surfaces an error (no git repo)...
	if err := ensureWorktreeGitExcluded(dir, "AGENTS.override.md"); err == nil {
		t.Error("expected an error resolving info/exclude in a non-git dir")
	}
	// ...but the full injection path swallows it and still writes the persona.
	promptFile := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(promptFile, []byte("# persona\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := &Provider{
		ID:              "codex",
		PromptInjection: PromptInjection{Kind: InjectContextFile, ContextFile: "AGENTS.override.md"},
	}
	if err := writeContextFilePrompt(p, promptFile, dir); err != nil {
		t.Fatalf("writeContextFilePrompt must not fail on a non-git dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.override.md")); err != nil {
		t.Errorf("persona not written on non-git dir: %v", err)
	}
}
