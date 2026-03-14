package service

import (
	"runtime"
	"strings"
	"testing"
	"text/template"
)

func TestLaunchdPlistTemplate(t *testing.T) {
	data := launchdData{
		Label:     "com.pogo.daemon",
		PogodPath: "/usr/local/bin/pogod",
		LogDir:    "/home/user/.local/share/pogo/logs",
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

	if !strings.Contains(result, "<string>com.pogo.daemon</string>") {
		t.Error("plist missing label")
	}
	if !strings.Contains(result, "<string>/usr/local/bin/pogod</string>") {
		t.Error("plist missing pogod path")
	}
	if !strings.Contains(result, "<key>RunAtLoad</key>") {
		t.Error("plist missing RunAtLoad")
	}
	if !strings.Contains(result, "<key>KeepAlive</key>") {
		t.Error("plist missing KeepAlive")
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
