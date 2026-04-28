package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"
)

func TestLaunchdPlistTemplate(t *testing.T) {
	data := launchdData{
		Label:      "com.pogo.daemon",
		PogodPath:  "/Users/test/go/bin/pogod",
		LogDir:     "/Users/test/.pogo/log",
		Home:       "/Users/test",
		PogoHome:   "/Users/test/.pogo",
		PluginPath: "/Users/test/.pogo/plugin",
		Path:       "/Users/test/.local/bin:/usr/local/bin:/usr/bin:/bin",
	}

	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	result := buf.String()

	checks := []struct {
		name  string
		want  string
		isRaw bool
	}{
		{"label", "<string>com.pogo.daemon</string>", false},
		{"pogod path", "<string>/Users/test/go/bin/pogod</string>", false},
		{"RunAtLoad", "<key>RunAtLoad</key>", false},
		// KeepAlive must be unconditional <true/>, not <dict><SuccessfulExit>false</SuccessfulExit></dict> —
		// see the table in scripts/launchd/README.md for why.
		{"KeepAlive unconditional", "<key>KeepAlive</key>\n    <true/>", false},
		// ProcessType=Interactive prevents App Nap from throttling refinery polling and agent idle detection.
		{"ProcessType Interactive", "<key>ProcessType</key>\n    <string>Interactive</string>", false},
		{"log path", "<string>/Users/test/.pogo/log/pogod.log</string>", false},
		{"PATH env", "<key>PATH</key>", false},
		{"HOME env", "<key>HOME</key>\n        <string>/Users/test</string>", false},
		{"POGO_HOME env", "<key>POGO_HOME</key>\n        <string>/Users/test/.pogo</string>", false},
		{"POGO_PLUGIN_PATH env", "<key>POGO_PLUGIN_PATH</key>\n        <string>/Users/test/.pogo/plugin</string>", false},
	}
	for _, c := range checks {
		if !strings.Contains(result, c.want) {
			t.Errorf("plist missing %s: expected substring %q\ngot:\n%s", c.name, c.want, result)
		}
	}

	// Guard against regressing to the old conditional KeepAlive form.
	if strings.Contains(result, "SuccessfulExit") {
		t.Error("plist still uses conditional KeepAlive (SuccessfulExit) — must be unconditional <true/>")
	}
}

func TestLaunchdPathIncludesCommonDirs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	p := launchdPath()
	required := []string{".local/bin", "go/bin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"}
	for _, dir := range required {
		if !strings.Contains(p, dir) {
			t.Errorf("launchdPath() missing %q: %s", dir, p)
		}
	}
}

func TestLogDirUnderPogoHome(t *testing.T) {
	// logDir() = pogoHome() + "/log". Test it tracks POGO_HOME so the plist
	// log path always lands inside the same state dir as the rest of pogo.
	t.Setenv("POGO_HOME", "/tmp/pogo-test-home")
	d := logDir()
	if d != "/tmp/pogo-test-home/log" {
		t.Errorf("logDir() with POGO_HOME=/tmp/pogo-test-home should be /tmp/pogo-test-home/log, got: %s", d)
	}
}

func TestSystemdUnitTemplate(t *testing.T) {
	data := systemdData{PogodPath: "/usr/local/bin/pogod"}

	tmpl, err := template.New("unit").Parse(systemdUnitTemplate)
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	result := buf.String()

	if !strings.Contains(result, "ExecStart=/usr/local/bin/pogod") {
		t.Error("unit missing ExecStart")
	}
	if !strings.Contains(result, "Restart=on-failure") {
		t.Error("unit missing Restart")
	}
	if !strings.Contains(result, "WantedBy=default.target") {
		t.Error("unit missing WantedBy")
	}
}

func TestStatusNotInstalled(t *testing.T) {
	// On a test machine, the service should not be installed
	installed, path := Status()
	if path == "" {
		t.Skip("unsupported OS for this test")
	}
	// Just verify it returns without panicking; installed state depends on environment
	_ = installed
}

func TestRenderLaunchdPlistDeterministic(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	// renderLaunchdPlist drives the diff-aware idempotency check — the
	// installer compares its output byte-for-byte against the on-disk
	// plist. Two consecutive renders against the same env must match
	// exactly, otherwise every install becomes a forced reload.
	t.Setenv("POGO_HOME", "/tmp/pogo-render-test")
	a, _, err := renderLaunchdPlist()
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	b, _, err := renderLaunchdPlist()
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if a != b {
		t.Fatalf("renderLaunchdPlist produced different output on consecutive calls\nfirst:\n%s\nsecond:\n%s", a, b)
	}
	if !strings.Contains(a, "/tmp/pogo-render-test/log/pogod.log") {
		t.Errorf("rendered plist did not pick up POGO_HOME override: %s", a)
	}
}

func TestLogTailReadsLastBytes(t *testing.T) {
	// logTail() is the failure-mail body builder — mayor needs to see the
	// most recent log lines, not the first ones, when an install fails.
	dir := t.TempDir()
	t.Setenv("POGO_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "log"), 0755); err != nil {
		t.Fatal(err)
	}

	// Write a log file larger than logTailBytes so we exercise the seek path.
	logPath := filepath.Join(dir, "log", "pogod.log")
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&sb, "line %04d filler\n", i)
	}
	expectedTail := "line 1999 filler\n"
	if err := os.WriteFile(logPath, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tail := logTail()
	if !strings.HasSuffix(tail, expectedTail) {
		t.Errorf("logTail did not end with most recent line; tail ends with: %q", tail[max(0, len(tail)-40):])
	}
	if len(tail) > logTailBytes+128 {
		t.Errorf("logTail returned %d bytes, expected ~%d", len(tail), logTailBytes)
	}
	if strings.Contains(tail, "line 0000 filler") {
		t.Error("logTail returned start of file instead of end")
	}
}

func TestLogTailMissingFile(t *testing.T) {
	// If the daemon never started, pogod.log won't exist. logTail must
	// return a human-readable note rather than panicking — the failure
	// mail still has to render.
	dir := t.TempDir()
	t.Setenv("POGO_HOME", dir)
	tail := logTail()
	if !strings.Contains(tail, "could not open") {
		t.Errorf("expected 'could not open' note for missing log, got: %s", tail)
	}
}

func TestServicePaths(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
		p := launchdPlistPath()
		if !strings.HasSuffix(p, "Library/LaunchAgents/com.pogo.daemon.plist") {
			t.Errorf("unexpected plist path: %s", p)
		}
	case "linux":
		p := systemdUnitPath()
		if !strings.HasSuffix(p, ".config/systemd/user/pogo.service") {
			t.Errorf("unexpected unit path: %s", p)
		}
	default:
		t.Skip("unsupported OS")
	}
}
