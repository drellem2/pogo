package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/hookarm"
)

// presubmitTemplates are the shipped prompt templates that carry the
// pre-submit check (mg-c184d). Each must pass on pogod's own artefacts and
// still refuse real work (mg-54263).
var presubmitTemplates = []string{
	"polecat.md",
	"polecat-architect.md",
}

// presubmitFunc extracts the presubmit_check shell function from a shipped
// template, verbatim apart from its markdown indentation, so the test runs the
// text a polecat runs rather than a copy of it.
func presubmitFunc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "agent", "prompts", "templates", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "presubmit_check() {" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s: no presubmit_check() function", name)
	}
	indent := lines[start][:len(lines[start])-len(strings.TrimLeft(lines[start], " "))]
	var fn []string
	for _, l := range lines[start:] {
		fn = append(fn, strings.TrimPrefix(l, indent))
		if l == indent+"}" {
			body := strings.Join(fn, "\n")
			if strings.Contains(body, "{{") {
				t.Fatalf("%s: presubmit_check carries template actions; render it before running", name)
			}
			return body
		}
	}
	t.Fatalf("%s: presubmit_check() is not closed", name)
	return ""
}

// presubmitRepo is a clone with an origin, on a pushed branch one commit ahead
// of origin/main — the shape presubmit_check passes when the tree is clean. Its
// .gitignore does NOT list .claude/, and the user's global excludes are off
// (this host's lists **/.claude/settings.local.json, which would make the
// settings half pass with no code under test doing anything).
func presubmitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	clone := filepath.Join(root, "clone")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "--bare", "-b", "main", origin)
	run(root, "clone", "-q", origin, clone)
	run(clone, "config", "core.excludesFile", os.DevNull)
	run(clone, "config", "user.email", "t@example.com")
	run(clone, "config", "user.name", "t")
	run(clone, "checkout", "-q", "-b", "main")
	writeFile(t, clone, "sub/keep.txt", "keep\n")
	writeFile(t, clone, ".gitignore", "bin/\n")
	run(clone, "add", ".")
	run(clone, "commit", "-q", "-m", "init")
	run(clone, "push", "-q", "origin", "main")
	run(clone, "checkout", "-q", "-b", "polecat-x")
	writeFile(t, clone, "work.txt", "work\n")
	run(clone, "add", "work.txt")
	run(clone, "commit", "-q", "-m", "work")
	run(clone, "push", "-q", "origin", "polecat-x")
	return clone
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePogodArtefacts writes what pogod puts into an agent's tree: the hook
// settings at the root, a stamp beside them, and a stamp under a subdirectory
// the session cd'd into (the stamp follows the cwd, gh #240).
func writePogodArtefacts(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, hookarm.SettingsRelPath, "{}\n")
	writeFile(t, dir, filepath.Join(".claude", hookarm.StampName), "")
	writeFile(t, dir, filepath.Join("sub", ".claude", hookarm.StampName), "")
}

// runPresubmit runs a template's presubmit_check in cwd with the freshly built
// pogo first on PATH, and returns its combined output and whether it passed.
func runPresubmit(t *testing.T, fn, cwd string) (string, bool) {
	t.Helper()
	cmd := exec.Command("bash", "-c", fn+"\npresubmit_check main")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(pogoBin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BRANCH=polecat-x",
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"POGO_HOME=",
		// The clone's own config already switches excludes off; keep the
		// global file out as well so nothing ambient can ignore an artefact.
		"GIT_CONFIG_GLOBAL="+os.DevNull,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running presubmit_check: %v", err)
		}
		return string(out), false
	}
	return string(out), true
}

// TestPresubmitCheckPassesOnPogodArtefacts is mg-54263 (gh #240): a polecat
// whose tree holds only pogod's own .claude files — including a nested stamp —
// in a repo whose .gitignore lacks .claude/ must be allowed to submit, and a
// real untracked file or tracked modification must still refuse.
func TestPresubmitCheckPassesOnPogodArtefacts(t *testing.T) {
	for _, name := range presubmitTemplates {
		t.Run(name, func(t *testing.T) {
			fn := presubmitFunc(t, name)
			if strings.Contains(fn, "git status --porcelain") {
				t.Errorf("%s: presubmit_check still runs a bare git status --porcelain", name)
			}

			clone := presubmitRepo(t)
			if out, ok := runPresubmit(t, fn, clone); !ok {
				t.Fatalf("a clean pushed branch must pass:\n%s", out)
			}

			writePogodArtefacts(t, clone)
			// Positive control: git itself sees these files, so the fixture
			// is the shape that used to refuse.
			raw, err := exec.Command("git", "-C", clone, "-c", "core.excludesFile="+os.DevNull,
				"status", "--porcelain").Output()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), ".claude/") {
				t.Fatalf("fixture invalid: git status does not show the artefacts:\n%s", raw)
			}

			if out, ok := runPresubmit(t, fn, clone); !ok || !strings.Contains(out, "pre-submit OK") {
				t.Fatalf("pogod's own artefacts must not refuse a submit:\n%s", out)
			}
			// The polecat's shell is often cd'd into a subdirectory.
			if out, ok := runPresubmit(t, fn, filepath.Join(clone, "sub")); !ok {
				t.Fatalf("pogod's own artefacts must not refuse a submit run from a subdirectory:\n%s", out)
			}

			for _, tc := range []struct {
				what  string
				dirty func(dir string)
				want  string
			}{
				{"an untracked file", func(d string) { writeFile(t, d, "notes.txt", "x\n") }, "notes.txt"},
				{"another untracked file under .claude/", func(d string) {
					writeFile(t, d, filepath.Join(".claude", "other.json"), "{}\n")
				}, ".claude/other.json"},
				{"a tracked modification", func(d string) { writeFile(t, d, "work.txt", "changed\n") }, "work.txt"},
			} {
				t.Run(tc.what, func(t *testing.T) {
					clone := presubmitRepo(t)
					writePogodArtefacts(t, clone)
					tc.dirty(clone)
					out, ok := runPresubmit(t, fn, clone)
					if ok {
						t.Fatalf("%s beside pogod's artefacts must refuse:\n%s", tc.what, out)
					}
					if !strings.Contains(out, "REFUSING TO SUBMIT: uncommitted work") ||
						!strings.Contains(out, tc.want) {
						t.Errorf("refusal must name %s, got:\n%s", tc.want, out)
					}
					if strings.Contains(out, hookarm.StampName) {
						t.Errorf("refusal must not list pogod's stamp as work:\n%s", out)
					}
				})
			}
		})
	}
}

// TestWorktreeDirtyRefusesOutsideAWorktree pins the fail-closed half: when
// dirtiness cannot be determined the command exits non-zero with nothing on
// stdout, so `dirty=$(pogo worktree-dirty) || refuse` refuses instead of
// reading an empty answer as clean.
func TestWorktreeDirtyRefusesOutsideAWorktree(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runPogoEnv(t, nil, []string{"GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)},
		"worktree-dirty", dir)
	if code == 0 {
		t.Fatalf("worktree-dirty outside a git worktree must exit non-zero; stdout=%q", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout must be empty on failure, got %q", stdout)
	}
	if !strings.Contains(stderr, "not inside a git worktree") {
		t.Errorf("stderr must say why, got %q", stderr)
	}
}
