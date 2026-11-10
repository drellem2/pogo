package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIsLoopbackBind(t *testing.T) {
	orig := lookupHost
	t.Cleanup(func() { lookupHost = orig })
	lookupHost = func(h string) ([]string, error) {
		switch h {
		case "loop.test":
			return []string{"127.0.0.1", "::1"}, nil
		case "mixed.test":
			return []string{"127.0.0.1", "10.0.0.5"}, nil
		case "public.test":
			return []string{"203.0.113.7"}, nil
		}
		return nil, errors.New("no such host")
	}

	cases := []struct {
		bind string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"[::1]", true},
		{"::1%lo0", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{"loop.test", true},
		{"", false},
		{"  ", false},
		{"0.0.0.0", false},
		{"::", false},
		{"[::]", false},
		{"192.168.1.10", false},
		{"10.0.0.1", false},
		{"mixed.test", false},
		{"public.test", false},
		{"unresolvable.test", false}, // fails closed
	}
	for _, c := range cases {
		if got := isLoopbackBind(c.bind); got != c.want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", c.bind, got, c.want)
		}
	}
}

func TestCheckBindExposure(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == allowUnauthenticatedBindEnv {
				return v
			}
			return ""
		}
	}

	// Loopback: allowed, silent, opt-in irrelevant.
	for _, v := range []string{"", "1"} {
		if warn, err := checkBindExposure("127.0.0.1", env(v)); err != nil || warn != "" {
			t.Errorf("loopback with opt-in %q: warn=%q err=%v, want neither", v, warn, err)
		}
	}

	// Non-loopback without (or with a non-true) opt-in: refused, naming the variable.
	for _, bind := range []string{"0.0.0.0", "::", ""} {
		for _, v := range []string{"", "0", "false", "no", "bogus"} {
			_, err := checkBindExposure(bind, env(v))
			if err == nil {
				t.Errorf("bind %q opt-in %q: no error, want refusal", bind, v)
				continue
			}
			if !strings.Contains(err.Error(), allowUnauthenticatedBindEnv) {
				t.Errorf("refusal does not name %s: %v", allowUnauthenticatedBindEnv, err)
			}
		}
	}

	// Non-loopback with the opt-in: allowed, with a loud warning.
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		warn, err := checkBindExposure("0.0.0.0", env(v))
		if err != nil {
			t.Errorf("opt-in %q: err=%v, want nil", v, err)
		}
		if !strings.Contains(warn, "UNAUTHENTICATED") || !strings.Contains(warn, "0.0.0.0") {
			t.Errorf("opt-in %q: warning %q is not loud enough", v, warn)
		}
	}
}

// bootBindPogod starts the real pogod with -bind 0.0.0.0, the shipped
// Dockerfile's command line, and the given opt-in value ("" = unset). It
// returns the running command (nil if it already exited), its log path, and
// its port.
func bootBindPogod(t *testing.T, bin, optIn string) (*exec.Cmd, string, int) {
	t.Helper()
	sb := t.TempDir()
	state := filepath.Join(sb, "state")
	ws := filepath.Join(sb, "ws")
	for _, d := range []string{state, ws, filepath.Join(sb, ".config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(sb, "pogod.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })

	port := freePort(t)
	cmd := exec.Command(bin, "-bind", "0.0.0.0", "-port", strconv.Itoa(port))
	cmd.Dir = ws
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, allowUnauthenticatedBindEnv+"=") || strings.HasPrefix(kv, "POGO_BIND=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+sb,
		"XDG_CONFIG_HOME="+filepath.Join(sb, ".config"),
		"POGO_HOME="+state,
		"TMPDIR="+shortTmpdir(t),
	)
	if optIn != "" {
		env = append(env, allowUnauthenticatedBindEnv+"="+optIn)
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting pogod: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	})
	return cmd, logPath, port
}

// TestBindGuard_RefusesNonLoopbackWithoutOptIn is the acceptance test: the
// image's command line (`pogod --bind 0.0.0.0`) with no opt-in exits non-zero
// and never listens; the same line with the opt-in serves the API (the
// positive control), and logs the warning.
func TestBindGuard_RefusesNonLoopbackWithoutOptIn(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real pogod; skipped under -short")
	}
	bin := buildPogodUnderTest(t)

	t.Run("no opt-in refuses", func(t *testing.T) {
		cmd, logPath, port := bootBindPogod(t, bin, "")
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() == 0 {
				t.Fatalf("pogod exited with %v, want a non-zero exit\n--- log ---\n%s", err, readFile(t, logPath))
			}
		case <-time.After(60 * time.Second):
			t.Fatalf("pogod did not exit on a non-loopback bind without %s\n--- log ---\n%s",
				allowUnauthenticatedBindEnv, readFile(t, logPath))
		}
		log := readFile(t, logPath)
		if !strings.Contains(log, "refusing to listen on non-loopback address 0.0.0.0") ||
			!strings.Contains(log, allowUnauthenticatedBindEnv) {
			t.Errorf("refusal not explained in the log\n--- log ---\n%s", log)
		}
		if strings.Contains(log, "pogod listening on") {
			t.Errorf("pogod reported listening despite refusing\n--- log ---\n%s", log)
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
			c.Close()
			t.Errorf("port %d accepts connections after a refused bind", port)
		}
	})

	t.Run("opt-in serves", func(t *testing.T) {
		_, logPath, port := bootBindPogod(t, bin, "1")
		want := fmt.Sprintf("pogod listening on 0.0.0.0:%d", port)
		if !waitForLog(t, logPath, want, 60*time.Second) {
			t.Fatalf("pogod with %s=1 never logged %q\n--- log ---\n%s",
				allowUnauthenticatedBindEnv, want, readFile(t, logPath))
		}
		log := readFile(t, logPath)
		if !strings.Contains(log, "UNAUTHENTICATED") {
			t.Errorf("opt-in boot did not log the exposure warning\n--- log ---\n%s", log)
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
		if err != nil {
			t.Fatalf("opt-in pogod logged listening but refused a connection: %v", err)
		}
		c.Close()
	})
}
