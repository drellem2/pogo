package main

// End-to-end regression tests for the spawn-polecat CLI contract (gh #28,
// gh #29 / mg-a1e4). These run the real compiled binary against a stub pogod
// so they lock the process-level behavior an orchestrator scripts against:
// a failed spawn must exit nonzero with the error on stderr (plain mode) or
// a JSON error object on stdout (--json mode), and a successful spawn must
// exit 0. Unit tests on internal/client can't see the exit code — it is set
// by cli.ExitWithError via os.Exit — hence the exec-based harness.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// pogoBin is the CLI binary compiled once in TestMain and shared by all tests.
var pogoBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pogo-cli-test")
	if err != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
		os.Exit(1)
	}
	pogoBin = filepath.Join(dir, "pogo")
	if out, err := exec.Command("go", "build", "-o", pogoBin, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building pogo CLI: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runPogo runs the compiled CLI against a stub pogod handler and returns the
// separated stdout/stderr streams plus the process exit code. HOME and
// XDG_CONFIG_HOME are pointed at a temp dir so the user's real config.toml
// can't leak in; POGO_PORT points the client at the stub server.
func runPogo(t *testing.T, handler http.HandlerFunc, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	cmd := exec.Command(pogoBin, args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("POGO_PORT=%d", port),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		// Clear any ambient POGO_HOME so the CLI's state dir stays under the
		// temp HOME above instead of the developer's real ~/.pogo (mg-3dc3).
		"POGO_HOME=",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v", pogoBin, err)
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// TestSpawnPolecat_FailureExitsNonzero locks the gh #28 contract: when pogod
// rejects the spawn, the CLI must exit nonzero (so orchestrators can script
// against `pogo agent spawn-polecat ... || handle-failure`) and the error
// must go to stderr, not stdout.
func TestSpawnPolecat_FailureExitsNonzero(t *testing.T) {
	stdout, stderr, code := runPogo(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "worktree creation failed: boom", http.StatusInternalServerError)
	}, "agent", "spawn-polecat", "testcat")

	if code == 0 {
		t.Error("failed spawn must exit nonzero, got 0")
	}
	if !strings.Contains(stderr, "spawn-polecat failed") {
		t.Errorf("expected failure message on stderr, got stderr=%q", stderr)
	}
	if strings.Contains(stdout, "failed") {
		t.Errorf("plain-mode failure must not print to stdout, got stdout=%q", stdout)
	}
}

// TestSpawnPolecat_FailureJSONExitsNonzero is the --json flavor of the gh #28
// contract: the error object goes to stdout (by design, for machine parsing)
// but the exit code must still be nonzero.
func TestSpawnPolecat_FailureJSONExitsNonzero(t *testing.T) {
	stdout, _, code := runPogo(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "worktree creation failed: boom", http.StatusInternalServerError)
	}, "--json", "agent", "spawn-polecat", "testcat")

	if code == 0 {
		t.Error("failed spawn must exit nonzero in --json mode, got 0")
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("expected JSON error object on stdout, got %q: %v", stdout, err)
	}
	if _, ok := obj["error"]; !ok {
		t.Errorf("expected \"error\" key in JSON output, got %q", stdout)
	}
}

// TestSpawnPolecat_SuccessExitsZero is the counterpart: a 201 from pogod must
// exit 0 and report the spawned polecat on stdout.
func TestSpawnPolecat_SuccessExitsZero(t *testing.T) {
	stdout, stderr, code := runPogo(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(agent.AgentInfo{Name: "testcat", PID: 12345})
	}, "agent", "spawn-polecat", "testcat")

	if code != 0 {
		t.Errorf("successful spawn must exit 0, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, "Spawned polecat testcat") {
		t.Errorf("expected spawn confirmation on stdout, got %q", stdout)
	}
}

// TestStatusLive_RejectsNonPositiveInterval locks the mg-c167 guard: --live
// with --interval <= 0 must exit with a clean error instead of the
// time.NewTicker panic it used to hit. The stub pogod is never consulted —
// validation happens before any fetch.
func TestStatusLive_RejectsNonPositiveInterval(t *testing.T) {
	for _, interval := range []string{"0", "-1s"} {
		t.Run(interval, func(t *testing.T) {
			stdout, stderr, code := runPogo(t, func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "should not be called", http.StatusInternalServerError)
			}, "status", "--live", "--interval", interval)

			if code == 0 {
				t.Errorf("--live --interval %s must exit nonzero, got 0 (stdout=%q)", interval, stdout)
			}
			if !strings.Contains(stderr, "--interval must be positive") {
				t.Errorf("expected interval error on stderr, got stderr=%q stdout=%q", stderr, stdout)
			}
			if strings.Contains(stdout, "panic") || strings.Contains(stderr, "panic") {
				t.Errorf("must not panic, got stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

// TestStatusLive_RejectsNonPositiveIntervalJSON is the --json flavor: the
// error object goes to stdout and the exit code is still nonzero.
func TestStatusLive_RejectsNonPositiveIntervalJSON(t *testing.T) {
	stdout, _, code := runPogo(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}, "--json", "status", "--live", "--interval", "0")

	if code == 0 {
		t.Error("--live --interval 0 must exit nonzero in --json mode, got 0")
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("expected JSON error object on stdout, got %q: %v", stdout, err)
	}
	if _, ok := obj["error"]; !ok {
		t.Errorf("expected \"error\" key in JSON output, got %q", stdout)
	}
}

// TestSpawnPolecat_ProviderHelpListsPi locks the gh #29 fix: the --provider
// flag help must enumerate every registered provider, including pi.
func TestSpawnPolecat_ProviderHelpListsPi(t *testing.T) {
	cmd := exec.Command(pogoBin, "agent", "spawn-polecat", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "(claude, codex, pi)") {
		t.Errorf("--provider help must list all providers (claude, codex, pi), got:\n%s", out)
	}
}
