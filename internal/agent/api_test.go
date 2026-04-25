package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// catCommandConfig is a test AgentCommandConfig that runs `cat` for any
// agent type, so spawn-driven tests can succeed without invoking `claude`.
type catCommandConfig struct{}

func (catCommandConfig) AgentCommand(string) string { return "cat" }

func TestAgentInfoLastActivity(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "activity-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Nudge to generate output
	if err := a.Nudge("hello"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	info := ExportInfo(a)
	if info.LastActivity == "" {
		t.Error("expected LastActivity to be set after output")
	}
	if !strings.Contains(info.LastActivity, "ago") && info.LastActivity != "just now" {
		t.Errorf("unexpected LastActivity format: %q", info.LastActivity)
	}
}

func TestAgentInfoLastActivityEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a process that exits immediately without producing visible output
	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-activity",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check info immediately — the ring buffer's lastWrite is zero before any PTY output
	// Note: PTY setup may produce some initial output, so we just verify the field
	// is either empty or a valid "ago" string.
	info := ExportInfo(a)
	if info.LastActivity != "" && !strings.Contains(info.LastActivity, "ago") && info.LastActivity != "just now" {
		t.Errorf("unexpected LastActivity format: %q", info.LastActivity)
	}
}

func TestFormatLastActivity(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"just now", 0, "just now"},
		{"seconds", 5 * time.Second, "5s ago"},
		{"minutes", 2*time.Minute + 30*time.Second, "2m30s ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLastActivity(time.Now().Add(-tt.ago))
			if got != tt.want {
				t.Errorf("formatLastActivity(-%v) = %q, want %q", tt.ago, got, tt.want)
			}
		})
	}
}

// runGit runs a git command with a stable identity for tests.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// makeRepoWithOrigin creates a bare "origin" repo plus a working clone whose
// origin remote points at it. Returns (workDir, originDir).
func makeRepoWithOrigin(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	originDir := filepath.Join(root, "origin.git")
	workDir := filepath.Join(root, "work")

	if out, err := exec.Command("git", "init", "--bare", "-b", "main", originDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "init", "-b", "main", workDir).CombinedOutput(); err != nil {
		t.Fatalf("init work: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "remote", "add", "origin", originDir).CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v\n%s", err, out)
	}

	// Seed initial commit and push so origin/main exists.
	if err := os.WriteFile(filepath.Join(workDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "seed.txt")
	runGit(t, workDir, "commit", "-m", "seed")
	runGit(t, workDir, "push", "-u", "origin", "main")
	return workDir, originDir
}

// TestResolvePolecatBaseRef_PrefersOriginBranch verifies the helper returns
// origin/<branch> when a target branch is supplied, even if the local checkout
// is behind origin. This is the core fix for mg-58a3.
func TestResolvePolecatBaseRef_PrefersOriginBranch(t *testing.T) {
	workDir, originDir := makeRepoWithOrigin(t)

	// Make a second clone that pushes a commit to origin/main directly.
	otherDir := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", originDir, otherDir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "added.txt"), []byte("merged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, otherDir, "add", "added.txt")
	runGit(t, otherDir, "commit", "-m", "merged via origin")
	runGit(t, otherDir, "push", "origin", "main")

	// At this point workDir's local main is BEHIND origin/main.
	// resolvePolecatBaseRef should fetch and return origin/main.
	got := resolvePolecatBaseRef(workDir, "main")
	if got != "origin/main" {
		t.Fatalf("resolvePolecatBaseRef = %q, want origin/main", got)
	}

	// Verify origin/main now contains the new commit (i.e. fetch happened).
	out, err := exec.Command("git", "-C", workDir, "log", "origin/main", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("log origin/main: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "merged via origin") {
		t.Errorf("expected origin/main to include merged commit after fetch, got:\n%s", out)
	}
}

// TestResolvePolecatBaseRef_DefaultBranch verifies the helper falls back to
// origin/HEAD's branch when no explicit branch is supplied.
func TestResolvePolecatBaseRef_DefaultBranch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	// Set origin/HEAD so symbolic-ref works.
	runGit(t, workDir, "remote", "set-head", "origin", "main")

	got := resolvePolecatBaseRef(workDir, "")
	if got != "origin/main" {
		t.Fatalf("resolvePolecatBaseRef(empty branch) = %q, want origin/main", got)
	}
}

// TestResolvePolecatBaseRef_NoOrigin returns empty when origin is missing,
// allowing the caller to fall back to local HEAD (e.g. test fixtures).
func TestResolvePolecatBaseRef_NoOrigin(t *testing.T) {
	workDir := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", workDir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if got := resolvePolecatBaseRef(workDir, "main"); got != "" {
		t.Fatalf("resolvePolecatBaseRef = %q, want empty (no origin)", got)
	}
}

// startAgentViaAPI calls handleStart for the named agent and returns the
// spawned Agent. Fails the test if the response is not 201.
func startAgentViaAPI(t *testing.T, reg *Registry, name string) *Agent {
	t.Helper()
	body, _ := json.Marshal(StartAPIRequest{Name: name})
	req := httptest.NewRequest("POST", "/agents/start", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleStart(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("handleStart status = %d, body=%s", rr.Code, rr.Body.String())
	}
	a := reg.Get(name)
	if a == nil {
		t.Fatalf("agent %q not registered after start", name)
	}
	return a
}

// TestHandleStart_NudgeOnStartFromFrontmatter verifies that handleStart
// uses nudge_on_start from a prompt file's TOML frontmatter when present.
func TestHandleStart_NudgeOnStartFromFrontmatter(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	promptPath := filepath.Join(CrewPromptDir(), "frontnudge.md")
	content := "+++\nnudge_on_start = \"hello from frontmatter\"\n+++\nbody text\n"
	if err := os.WriteFile(promptPath, []byte(content), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := startAgentViaAPI(t, reg, "frontnudge")
	if a.InitialNudge != "hello from frontmatter" {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, "hello from frontmatter")
	}
}

// TestHandleStart_MayorFallbackNudge verifies that the mayor without
// frontmatter still receives the legacy coordination-loop nudge.
func TestHandleStart_MayorFallbackNudge(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	mayorPath := filepath.Join(PromptDir(), "mayor.md")
	if err := os.WriteFile(mayorPath, []byte("# mayor\n"), 0644); err != nil {
		t.Fatalf("write mayor prompt: %v", err)
	}

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := startAgentViaAPI(t, reg, "mayor")
	want := "You are now running. Begin your coordination loop."
	if a.InitialNudge != want {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, want)
	}
}

// TestHandleStart_CrewFallbackNudge verifies that a crew agent without
// frontmatter receives the legacy generic mail-checking nudge.
func TestHandleStart_CrewFallbackNudge(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	promptPath := filepath.Join(CrewPromptDir(), "plain.md")
	if err := os.WriteFile(promptPath, []byte("# plain crew\n"), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := startAgentViaAPI(t, reg, "plain")
	want := "You are now running. Check your mail with `mg mail list plain` and begin your work."
	if a.InitialNudge != want {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, want)
	}
}

// spawnPolecatViaAPI calls handleSpawnPolecat with the given request and
// returns the spawned Agent. Fails the test if the response is not 201.
func spawnPolecatViaAPI(t *testing.T, reg *Registry, spawnReq SpawnPolecatAPIRequest) *Agent {
	t.Helper()
	body, _ := json.Marshal(spawnReq)
	req := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("handleSpawnPolecat status = %d, body=%s", rr.Code, rr.Body.String())
	}
	a := reg.Get(spawnReq.Name)
	if a == nil {
		t.Fatalf("agent %q not registered after spawn", spawnReq.Name)
	}
	return a
}

// writeTemplate writes a polecat template under the configured TemplateDir().
func writeTemplate(t *testing.T, name, content string) {
	t.Helper()
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	path := filepath.Join(TemplateDir(), name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}
}

// TestHandleSpawnPolecat_NudgeOnStartFromFrontmatter verifies that the polecat's
// initial nudge comes from the template's frontmatter when set.
func TestHandleSpawnPolecat_NudgeOnStartFromFrontmatter(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "fronted", "+++\nnudge_on_start = \"custom polecat nudge\"\n+++\nbody {{.Id}}\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-fronted",
		Template: "fronted",
		Id:       "wi-1",
	})
	if a.InitialNudge != "custom polecat nudge" {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, "custom polecat nudge")
	}
}

// TestHandleSpawnPolecat_NudgeOnStartTemplateExpanded verifies that {{.Id}}
// and other TemplateVars placeholders in nudge_on_start are expanded with
// the same context as the prompt body. This is what makes the shipped
// templates/polecat.md frontmatter behave identically to the previous
// hardcoded "...for this work item: <id>" message.
func TestHandleSpawnPolecat_NudgeOnStartTemplateExpanded(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "templated", "+++\nnudge_on_start = \"work item: {{.Id}}\"\n+++\nbody\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-templated",
		Template: "templated",
		Id:       "wi-99",
	})
	want := "work item: wi-99"
	if a.InitialNudge != want {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, want)
	}
}

// TestHandleSpawnPolecat_FallbackNudge verifies that a template without a
// nudge_on_start field still produces the legacy work-item nudge.
func TestHandleSpawnPolecat_FallbackNudge(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "plainpc", "# plain polecat\nbody {{.Id}}\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-plain",
		Template: "plainpc",
		Id:       "wi-42",
	})
	want := "Look at the system prompt and complete the steps for this work item: wi-42"
	if a.InitialNudge != want {
		t.Errorf("InitialNudge = %q, want %q", a.InitialNudge, want)
	}
}

// TestHandleSpawnPolecat_WorktreeFalseSkipsCreation verifies that
// worktree=false in the template's frontmatter prevents worktree creation
// even when a Repo is supplied.
func TestHandleSpawnPolecat_WorktreeFalseSkipsCreation(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workDir, _ := makeRepoWithOrigin(t)

	writeTemplate(t, "noworktree", "+++\nworktree = false\n+++\n# polecat without worktree\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-nowt",
		Template: "noworktree",
		Repo:     workDir,
		Id:       "wi-nowt",
	})

	if a.WorktreeDir != "" {
		t.Errorf("WorktreeDir = %q, want empty (worktree=false in frontmatter)", a.WorktreeDir)
	}
	expectedWorktree := filepath.Join(tmpHome, ".pogo", "polecats", "pc-nowt")
	if _, err := os.Stat(expectedWorktree); err == nil {
		t.Errorf("worktree dir %s should not exist", expectedWorktree)
	}
	// Verify no worktree branch was registered in the source repo either.
	out, err := exec.Command("git", "-C", workDir, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("worktree list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "polecat-pc-nowt") {
		t.Errorf("did not expect polecat worktree in:\n%s", out)
	}
}

// TestHandleSpawnPolecat_WorktreeTrueCreatesWorktree verifies that
// worktree=true in frontmatter (the default) creates the isolated worktree.
func TestHandleSpawnPolecat_WorktreeTrueCreatesWorktree(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workDir, _ := makeRepoWithOrigin(t)

	writeTemplate(t, "wantsworktree", "+++\nworktree = true\n+++\n# polecat with worktree\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-wt",
		Template: "wantsworktree",
		Repo:     workDir,
		Id:       "wi-wt",
	})
	expectedWorktree := filepath.Join(tmpHome, ".pogo", "polecats", "pc-wt")
	if a.WorktreeDir != expectedWorktree {
		t.Errorf("WorktreeDir = %q, want %q", a.WorktreeDir, expectedWorktree)
	}
	if _, err := os.Stat(expectedWorktree); err != nil {
		t.Errorf("expected worktree at %s: %v", expectedWorktree, err)
	}
}

// TestHandleSpawnPolecat_WorktreeDefaultWhenRepoSet verifies the legacy
// behavior is preserved when a template declares no worktree field.
func TestHandleSpawnPolecat_WorktreeDefaultWhenRepoSet(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workDir, _ := makeRepoWithOrigin(t)

	writeTemplate(t, "defaultwt", "# polecat with default worktree behavior\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-def",
		Template: "defaultwt",
		Repo:     workDir,
		Id:       "wi-def",
	})
	expectedWorktree := filepath.Join(tmpHome, ".pogo", "polecats", "pc-def")
	if a.WorktreeDir != expectedWorktree {
		t.Errorf("WorktreeDir = %q, want %q (default behavior when Repo set)", a.WorktreeDir, expectedWorktree)
	}
}

// TestHandleSpawnPolecat_FrontmatterStrippedFromBody verifies that the
// frontmatter block does not leak into the rendered prompt file.
func TestHandleSpawnPolecat_FrontmatterStrippedFromBody(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	writeTemplate(t, "stripfm", "+++\nnudge_on_start = \"go\"\n+++\nID is {{.Id}}\n")

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:     "pc-strip",
		Template: "stripfm",
		Id:       "wi-strip",
	})
	data, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "+++") {
		t.Errorf("expected frontmatter fences to be stripped, got: %q", body)
	}
	if strings.Contains(body, "nudge_on_start") {
		t.Errorf("expected frontmatter keys to be stripped, got: %q", body)
	}
	if !strings.Contains(body, "ID is wi-strip") {
		t.Errorf("expected expanded body, got: %q", body)
	}
}
