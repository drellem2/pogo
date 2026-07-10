package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

func TestValidateAgentName(t *testing.T) {
	tests := []struct {
		name    string
		agent   string
		wantErr bool
	}{
		{"empty", "", true},
		{"short", "pm-pogo", false},
		{"polecat work item", "ef80", false},
		{"exactly at the limit", strings.Repeat("a", config.MaxAgentNameLen), false},
		{"one byte over the limit", strings.Repeat("a", config.MaxAgentNameLen+1), true},
		{"reviewer's 30-char repro", strings.Repeat("a", 30), true},
		// sun_path is a byte budget, so the ceiling counts bytes, not runes:
		// 13 three-byte runes are 39 bytes and must be refused even though
		// they are well under MaxAgentNameLen runes.
		{"multibyte over the byte limit", strings.Repeat("日", 13), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentName(tt.agent)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateAgentName(%d bytes) error = %v, wantErr %v", len(tt.agent), err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidAgentName) {
				t.Errorf("error %v does not wrap ErrInvalidAgentName", err)
			}
		})
	}
}

// TestSpawnRejectsOverlongName pins the headline fix of mg-ef80. Before it,
// Spawn accepted any name and only the attach bind noticed — under a deep
// enough POGO_HOME the agent ran with attach permanently unavailable while the
// API reported success.
func TestSpawnRejectsOverlongName(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	name := strings.Repeat("a", config.MaxAgentNameLen+1)
	a, err := reg.Spawn(SpawnRequest{Name: name, Type: TypePolecat, Command: []string{"cat"}})
	if err == nil {
		t.Fatal("Spawn accepted a name over MaxAgentNameLen")
	}
	if a != nil {
		t.Errorf("Spawn returned an agent alongside its error: %+v", a)
	}
	if !errors.Is(err, ErrInvalidAgentName) {
		t.Errorf("Spawn error %v does not wrap ErrInvalidAgentName", err)
	}
	if reg.Get(name) != nil {
		t.Error("a rejected name left a registry entry behind")
	}
}

// TestSpawnRejectsEmptyName guards the check Spawn never had: the handlers
// rejected an empty name, but a direct Spawn would happily bind ".sock".
func TestSpawnRejectsEmptyName(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if _, err := reg.Spawn(SpawnRequest{Name: "", Type: TypePolecat, Command: []string{"cat"}}); !errors.Is(err, ErrInvalidAgentName) {
		t.Fatalf("Spawn(\"\") error = %v, want ErrInvalidAgentName", err)
	}
}

// TestSpawnAcceptsNameAtLimit is the other half of the boundary: the byte
// budget AgentSocketDir reserves is a promise, so a name of exactly
// MaxAgentNameLen must still spawn.
func TestSpawnAcceptsNameAtLimit(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	name := strings.Repeat("a", config.MaxAgentNameLen)
	if _, err := reg.Spawn(SpawnRequest{Name: name, Type: TypePolecat, Command: []string{"cat"}}); err != nil {
		t.Fatalf("Spawn(%d-byte name at the limit): %v", len(name), err)
	}
	if reg.Get(name) == nil {
		t.Error("a name at the limit did not register")
	}
}

// TestHandleStartRejectsOverlongName pins the API contract: 400, not the 201
// that used to lie.
func TestHandleStartRejectsOverlongName(t *testing.T) {
	// Hermetic HOME: the name must be refused before the prompt lookup, so a
	// regression that moved the check later would otherwise fail against the
	// developer's real ~/.pogo with a confusing 404.
	t.Setenv("HOME", t.TempDir())

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	name := strings.Repeat("a", config.MaxAgentNameLen+1)
	body, _ := json.Marshal(StartAPIRequest{Name: name})
	req := httptest.NewRequest("POST", "/agents/start", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleStart(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("handleStart status = %d, want %d; body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if reg.Get(name) != nil {
		t.Error("a rejected name left a registry entry behind")
	}
}

// TestHandleSpawnPolecatRejectsOverlongName also guards that the rejection
// happens before any side effects: no worktree, no agent dir, no prompt file.
func TestHandleSpawnPolecatRejectsOverlongName(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	name := strings.Repeat("a", config.MaxAgentNameLen+1)
	body, _ := json.Marshal(SpawnPolecatAPIRequest{Name: name, NoWorktree: true, Task: "t"})
	req := httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("handleSpawnPolecat status = %d, want %d; body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(PromptDir(), name)); !os.IsNotExist(err) {
		t.Errorf("a rejected name left an agent dir behind (stat err = %v)", err)
	}
}

// TestHandleAgentsPostRejectsOverlongName covers the generic POST /agents
// path, which mapped every Spawn error to 409 before mg-ef80.
func TestHandleAgentsPostRejectsOverlongName(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	name := strings.Repeat("a", config.MaxAgentNameLen+1)
	body, _ := json.Marshal(SpawnAPIRequest{Name: name, Type: TypePolecat, Command: []string{"cat"}})
	req := httptest.NewRequest("POST", "/agents", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleAgents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("handleAgents POST status = %d, want %d; body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

// TestHandleAgentsPostStillConflictsOnDuplicate guards that routing the
// invalid-name case to 400 did not steal the 409 the duplicate-name case
// still owes its callers.
func TestHandleAgentsPostStillConflictsOnDuplicate(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if _, err := reg.Spawn(SpawnRequest{Name: "dupe", Type: TypePolecat, Command: []string{"cat"}}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	body, _ := json.Marshal(SpawnAPIRequest{Name: "dupe", Type: TypePolecat, Command: []string{"cat"}})
	req := httptest.NewRequest("POST", "/agents", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	reg.handleAgents(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("handleAgents POST on a live duplicate = %d, want %d; body=%s", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

// socketDirOfLen returns a creatable directory path of exactly n bytes. It
// builds from a short /tmp base rather than t.TempDir(), whose darwin path is
// already longer than the lengths this helper's callers need to hit exactly.
func socketDirOfLen(t *testing.T, n int) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "pogo-len-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })

	pad := n - len(base) - 1 // -1 for the separator filepath.Join adds
	if pad < 1 || pad > 255 {
		// A single path component cannot exceed NAME_MAX.
		t.Fatalf("cannot build a %d-byte dir from a %d-byte base in one component", n, len(base))
	}
	dir := filepath.Join(base, strings.Repeat("s", pad))
	if len(dir) != n {
		t.Fatalf("test bug: built a %d-byte dir, want %d", len(dir), n)
	}
	return dir
}

// TestSpawnFailsWhenAttachSocketCannotBind is the mg-ef80 backstop. A name
// within MaxAgentNameLen still overruns sun_path if the socket dir is deep
// enough — which is what would happen should the byte budget in
// internal/config ever drift from the real limit. The spawn must fail loudly
// rather than return a running agent nobody can attach to.
//
// The test also proves the teardown reaps the process: Spawn kills and waits
// for it before returning, so a Kill that did not land would hang here rather
// than leak an orphan past the end of the run.
func TestSpawnFailsWhenAttachSocketCannotBind(t *testing.T) {
	// 120 bytes is past sun_path on every supported platform (104 on darwin,
	// 108 on linux), so Go refuses the bind before it reaches the kernel.
	const socketPathLen = 120
	name := "unbindable"
	dir := socketDirOfLen(t, socketPathLen-len("/"+name+".sock"))

	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if err := ValidateAgentName(name); err != nil {
		t.Fatalf("test bug: %q must be a legal name, got %v", name, err)
	}

	a, err := reg.Spawn(SpawnRequest{Name: name, Type: TypePolecat, Command: []string{"cat"}})
	if err == nil {
		t.Fatal("Spawn succeeded despite an attach socket that cannot bind")
	}
	if a != nil {
		t.Errorf("Spawn returned an agent alongside its error: %+v", a)
	}
	if !errors.Is(err, ErrAttachSocketUnusable) {
		t.Errorf("Spawn error %v does not wrap ErrAttachSocketUnusable", err)
	}
	if reg.Get(name) != nil {
		t.Error("a failed spawn left a registry entry behind")
	}
	if got := len(reg.List()); got != 0 {
		t.Errorf("registry holds %d agents after a failed spawn, want 0", got)
	}
}

// TestSpawnSurvivesTransientBindFailure guards the mg-d216 contract that
// mg-ef80's fatal-bind path must not swallow: a bind failure that may clear on
// its own keeps the agent alive, and the supervisor rebinds later. Here the
// socket path is occupied by a non-empty directory, so the pre-bind unlink
// fails and net.Listen reports EADDRINUSE — recoverable, not fatal.
func TestSpawnSurvivesTransientBindFailure(t *testing.T) {
	dir := shortSocketDir(t)
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	name := "blocked"
	blocker := filepath.Join(dir, name+".sock")
	if err := os.MkdirAll(filepath.Join(blocker, "child"), 0700); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	a, err := reg.Spawn(SpawnRequest{Name: name, Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatalf("Spawn must survive a recoverable bind failure, got: %v", err)
	}
	if a == nil || reg.Get(name) == nil {
		t.Fatal("a recoverable bind failure lost the agent")
	}
	if !a.alive() {
		t.Error("agent process is not running after a recoverable bind failure")
	}
}
