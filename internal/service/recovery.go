package service

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"
	"time"
)

// The recovery agent is the tier-3 fallback in mg-f5fc's three-tier
// supervision model. It lives in a separate launchd job so that even if
// pogod is wedged, a polecat can still drop a .req file into the queue
// and trigger a controlled `launchctl kickstart -k com.pogo.daemon`.
//
// Independence is the whole point: any signal channel that requires pogod
// (or pogod-owned state) defeats the purpose. The recovery agent only
// touches the kernel (file writes, flock, launchctl). install-recovery is
// kept distinct from install so a wedged pogod can be reset by an operator
// without going through the regular install path.

const recoveryLabel = "com.pogo.recovery"

// recoveryPlistTemplate mirrors the in-repo scripts/launchd/com.pogo.recovery.plist
// but with the host-specific paths bound in. WatchPaths is the trigger
// (edge-triggered file event), KeepAlive=false because the script is
// one-shot per trigger, ProcessType=Background because recovery reacts to
// file events rather than needing timer fidelity.
//
// POGO_RECOVERY_DIR is exported because pogo-recovery.sh otherwise falls back
// to $HOME/.pogo/recovery. Under a POGO_HOME that resolves elsewhere, launchd
// would watch {{.QueueDir}} while the script drained a different directory —
// every request would spawn the job and log "queue empty", draining nothing.
// Binding it here keeps watched and drained dirs the same by construction.
const recoveryPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.ScriptPath}}</string>
    </array>
    <key>WatchPaths</key>
    <array>
        <string>{{.QueueDir}}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>StandardOutPath</key>
    <string>{{.LogDir}}/recovery.log</string>
    <key>StandardErrorPath</key>
    <string>{{.LogDir}}/recovery.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{{.Path}}</string>
        <key>HOME</key>
        <string>{{.Home}}</string>
        <key>POGO_RECOVERY_DIR</key>
        <string>{{.RecoveryDir}}</string>
    </dict>
</dict>
</plist>
`

type recoveryData struct {
	Label       string
	ScriptPath  string
	QueueDir    string
	RecoveryDir string
	LogDir      string
	Path        string
	Home        string
}

func recoveryPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", recoveryLabel+".plist")
}

func recoveryDir() string {
	if d := os.Getenv("POGO_RECOVERY_DIR"); d != "" {
		return d
	}
	return filepath.Join(pogoHome(), "recovery")
}

func recoveryQueueDir() string {
	return filepath.Join(recoveryDir(), "queue")
}

func recoveryProcessedDir() string {
	return filepath.Join(recoveryDir(), "processed")
}

func recoveryFailedDir() string {
	return filepath.Join(recoveryDir(), "failed")
}

// recoveryScriptInstallPath is where install-recovery copies the bundled
// pogo-recovery.sh. The plist points at this absolute path; keeping it
// inside ~/.pogo/bin/ matches the convention pogod's PATH already uses
// for tools its children need.
func recoveryScriptInstallPath() string {
	return filepath.Join(pogoHome(), "bin", "pogo-recovery.sh")
}

// findRecoveryScriptSource locates the bundled pogo-recovery.sh on the
// filesystem. We prefer the in-repo copy (so a `go run ./cmd/pogo` works
// from a checkout) and fall back to a sibling of the running pogo binary.
// The recovery script is small (~80 lines) and version-locked to whatever
// pogo binary installed it; users should rerun install-recovery after
// upgrading.
func findRecoveryScriptSource() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "scripts", "launchd", "pogo-recovery.sh"),
			filepath.Join(dir, "scripts", "launchd", "pogo-recovery.sh"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "scripts", "launchd", "pogo-recovery.sh"))
	}
	if env := os.Getenv("POGO_RECOVERY_SCRIPT"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs, nil
			}
			return c, nil
		}
	}
	return "", fmt.Errorf("pogo-recovery.sh not found in any of: %v (set POGO_RECOVERY_SCRIPT to override)", candidates)
}

// renderRecoveryPlist materializes the in-package template against the
// current host. Mirrors renderLaunchdPlist for the daemon plist.
func renderRecoveryPlist() (string, recoveryData, error) {
	home, _ := os.UserHomeDir()
	data := recoveryData{
		Label:       recoveryLabel,
		ScriptPath:  recoveryScriptInstallPath(),
		QueueDir:    recoveryQueueDir(),
		RecoveryDir: recoveryDir(),
		LogDir:      logDir(),
		Path:        launchdPath(),
		Home:        home,
	}
	tmpl, err := template.New("recovery-plist").Parse(recoveryPlistTemplate)
	if err != nil {
		return "", data, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", data, err
	}
	return buf.String(), data, nil
}

// InstallRecovery sets up the tier-3 recovery agent: copies pogo-recovery.sh
// into ~/.pogo/bin/, makes the queue/processed/failed dirs, writes the
// plist, and bootstraps it via launchctl. Idempotent — rerunning is safe
// and replaces the agent in place.
//
// Kept separate from Install() on purpose: the whole point of the recovery
// agent is to be independent of pogod's process tree. If install-recovery
// were folded into install, a wedged pogod would block its own recovery
// install. Operators reset a wedged box by running install-recovery alone.
func InstallRecovery() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("recovery agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}

	src, err := findRecoveryScriptSource()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(recoveryScriptInstallPath()), 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(recoveryScriptInstallPath()), err)
	}
	for _, d := range []string{recoveryQueueDir(), recoveryProcessedDir(), recoveryFailedDir(), logDir()} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}

	scriptBytes, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}
	dst := recoveryScriptInstallPath()
	if err := os.WriteFile(dst, scriptBytes, 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", dst, err)
	}

	rendered, _, err := renderRecoveryPlist()
	if err != nil {
		return err
	}
	plistPath := recoveryPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(plistPath), err)
	}
	existing, _ := os.ReadFile(plistPath)
	if string(existing) != rendered {
		if err := os.WriteFile(plistPath, []byte(rendered), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", plistPath, err)
		}
	}

	target := fmt.Sprintf("gui/%d", os.Getuid())
	// Best-effort bootout first so a stale plist gets replaced cleanly.
	exec.Command("launchctl", "bootout", target, plistPath).Run()
	out, err := exec.Command("launchctl", "bootstrap", target, plistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %s: %w", string(out), err)
	}

	fmt.Printf("Recovery agent installed: %s\n", plistPath)
	fmt.Printf("Script: %s\n", dst)
	fmt.Printf("Queue:  %s\n", recoveryQueueDir())
	fmt.Printf("Logs:   %s/recovery.log\n", logDir())
	return nil
}

// UninstallRecovery removes the recovery plist and stops the agent. State
// under ~/.pogo/recovery/ (queue, processed/, failed/, last_restart) is
// left in place — operators may want to inspect it after the fact.
func UninstallRecovery() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("recovery agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}
	plistPath := recoveryPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("recovery agent not installed at %s", plistPath)
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", target, plistPath).Run() // best-effort
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", plistPath, err)
	}
	fmt.Printf("Recovery agent removed: %s\n", plistPath)
	fmt.Printf("State under %s left in place.\n", recoveryDir())
	return nil
}

// RecoveryStatus reports whether the recovery plist is on disk.
func RecoveryStatus() (installed bool, path string) {
	if runtime.GOOS != "darwin" {
		return false, ""
	}
	p := recoveryPlistPath()
	_, err := os.Stat(p)
	return err == nil, p
}

// EnqueueRecoveryRequest writes a *.req file into the recovery queue using
// the temp-then-rename pattern so launchd's WatchPaths trigger never sees
// a partial file. Returns the final path of the queued .req file.
//
// This is the ~15-line entry point referenced in the design — keeps callers
// from hand-rolling the temp/rename dance and ensures the on-disk format
// stays consistent.
func EnqueueRecoveryRequest(requester, reason string) (string, error) {
	if requester == "" {
		requester = "unknown"
	}
	queue := recoveryQueueDir()
	if err := os.MkdirAll(queue, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", queue, err)
	}
	rb := make([]byte, 4)
	if _, err := rand.Read(rb); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(rb)
	tmp := filepath.Join(queue, fmt.Sprintf(".tmp-%d-%s", os.Getpid(), suffix))
	now := time.Now().UTC()
	body := fmt.Sprintf("requester=%s;reason=%s;ts=%s\n", requester, reason, now.Format(time.RFC3339))
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", tmp, err)
	}
	final := filepath.Join(queue, fmt.Sprintf("%s-%s-%s.req",
		now.Format("20060102T150405Z"), sanitizeRequester(requester), suffix))
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("failed to rename to %s: %w", final, err)
	}
	return final, nil
}

// sanitizeRequester restricts the requester name to a small filename-safe
// charset so the .req filename stays readable in `ls`.
func sanitizeRequester(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}
