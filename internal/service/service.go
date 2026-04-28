package service

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/drellem2/pogo/internal/client"
)

const launchdLabel = "com.pogo.daemon"

// launchdPlistTemplate matches the mg-1416 spec: ProcessType=Interactive
// (prevents App Nap throttling of refinery polls + agent idle detection),
// unconditional KeepAlive (auto-restart on any exit), explicit PATH so
// spawned crew agents can find claude/git/mg, POGO_HOME and HOME so the
// daemon resolves the right state dir under launchd's minimal env.
const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.PogodPath}}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Interactive</string>
    <key>StandardOutPath</key>
    <string>{{.LogDir}}/pogod.log</string>
    <key>StandardErrorPath</key>
    <string>{{.LogDir}}/pogod.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{{.Path}}</string>
        <key>HOME</key>
        <string>{{.Home}}</string>
        <key>POGO_HOME</key>
        <string>{{.PogoHome}}</string>
        <key>POGO_PLUGIN_PATH</key>
        <string>{{.PluginPath}}</string>
    </dict>
</dict>
</plist>
`

const systemdUnitTemplate = `[Unit]
Description=Pogo code intelligence daemon
After=network.target

[Service]
Type=simple
ExecStart={{.PogodPath}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`

type launchdData struct {
	Label      string
	PogodPath  string
	LogDir     string
	Home       string
	PogoHome   string
	PluginPath string
	Path       string
}

type systemdData struct {
	PogodPath string
}

func findPogod() (string, error) {
	path, err := exec.LookPath("pogod")
	if err != nil {
		return "", fmt.Errorf("pogod not found in PATH: %w", err)
	}
	return filepath.Abs(path)
}

func launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func systemdUnitDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func systemdUnitPath() string {
	return filepath.Join(systemdUnitDir(), "pogo.service")
}

func pogoHome() string {
	if h := os.Getenv("POGO_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo")
}

// logDir is ~/Library/Logs/pogo on macOS — the Apple-standard location for
// user-scope app logs. Picked over ~/.pogo/log to avoid surprising users
// whose $HOME root may already contain unrelated files (e.g. a bare "log"
// file from another tool) and to follow the platform convention so Console.app
// surfaces the daemon's output naturally.
func logDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "pogo")
}

// launchdPath builds a PATH that includes the directories where pogod's
// children (claude, git, mg, pogo) actually live on a typical macOS dev
// box. The pogod binary itself is invoked by absolute path; this PATH is
// for the subprocesses it spawns.
func launchdPath() string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, ".local", "bin"), // claude CLI usually lands here
		filepath.Join(home, "go", "bin"),     // pogod, mg, pogo
		filepath.Join(home, ".pogo", "bin"),
		"/opt/homebrew/bin", // Apple Silicon Homebrew
		"/usr/local/bin",    // Intel Homebrew, common installs
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	return strings.Join(dirs, ":")
}

// Install generates and installs the appropriate service file for the current OS.
func Install() error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd()
	case "linux":
		return installSystemd()
	default:
		return fmt.Errorf("unsupported OS: %s (supported: darwin, linux)", runtime.GOOS)
	}
}

// stopRunningPogod best-effort stops a manually-started pogod so launchctl
// load doesn't immediately exit on lockfile/port collision. If no pogod is
// running this is a no-op.
func stopRunningPogod() {
	if err := client.HealthCheck(); err != nil {
		return // not running, nothing to do
	}
	fmt.Println("Stopping running pogod before installing service...")
	if err := client.StopServer(); err != nil {
		fmt.Printf("  warning: %v (continuing anyway)\n", err)
	}
}

// renderLaunchdPlist materializes the in-repo plist template against the
// current host (binary path, $HOME, $POGO_HOME). It's the source of truth
// for diff-aware idempotency: the on-disk plist is compared byte-for-byte
// against this output.
func renderLaunchdPlist() (string, launchdData, error) {
	pogodPath, err := findPogod()
	if err != nil {
		return "", launchdData{}, err
	}
	home, _ := os.UserHomeDir()
	data := launchdData{
		Label:      launchdLabel,
		PogodPath:  pogodPath,
		LogDir:     logDir(),
		Home:       home,
		PogoHome:   pogoHome(),
		PluginPath: filepath.Join(pogoHome(), "plugin"),
		Path:       launchdPath(),
	}
	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		return "", data, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", data, err
	}
	return buf.String(), data, nil
}

// isLaunchdLoaded reports whether launchd currently knows about the label.
// "Loaded" here just means `launchctl list LABEL` succeeds — the process
// behind it may be crash-looping or stopped.
func isLaunchdLoaded() bool {
	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		return false
	}
	return len(out) > 0 && !strings.Contains(string(out), "Could not find")
}

func installLaunchd() (retErr error) {
	// Self-report on the way out so a polecat can fire-and-forget the
	// install (`nohup setsid pogo service install ... &`) and have the
	// post-install mayor pick up the result via mail.
	defer func() {
		if retErr != nil {
			sendInstallFailureMail(retErr)
		}
	}()

	rendered, data, err := renderLaunchdPlist()
	if err != nil {
		return err
	}

	plistPath := launchdPlistPath()
	if err := os.MkdirAll(data.LogDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	existing, _ := os.ReadFile(plistPath)
	plistMatches := string(existing) == rendered
	loaded := isLaunchdLoaded()

	// Fast path: identical plist already loaded and pogod healthy → no-op.
	// Lets the post-install mayor rerun `pogo service install` as a probe
	// without bouncing the daemon.
	if plistMatches && loaded {
		if err := client.HealthCheck(); err == nil {
			fmt.Printf("Service already installed and healthy at %s — no changes.\n", plistPath)
			sendInstallSuccessMail(plistPath, data.LogDir, true)
			return nil
		}
	}

	// Either the plist content changed, the service is not loaded, or
	// it's loaded-but-unhealthy. In all cases we want a clean unload +
	// load cycle so launchd picks up any new plist content and respawns
	// pogod fresh.
	if loaded {
		fmt.Println("Existing service is loaded — unloading before reinstall.")
		exec.Command("launchctl", "unload", plistPath).Run() // best-effort
	}

	stopRunningPogod()

	if !plistMatches {
		if err := os.WriteFile(plistPath, []byte(rendered), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", plistPath, err)
		}
	}

	cmd := exec.Command("launchctl", "load", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %s: %w", string(out), err)
	}

	if err := verifyLaunchdRunning(); err != nil {
		return fmt.Errorf("service loaded but verification failed: %w", err)
	}

	fmt.Printf("Service installed: %s\n", plistPath)
	fmt.Printf("Logs: %s/pogod.log\n", data.LogDir)
	fmt.Println("The pogo daemon will now start on login and restart on crash.")
	sendInstallSuccessMail(plistPath, data.LogDir, false)
	return nil
}

// sendInstallMail is best-effort: if mg isn't on PATH or the mayor inbox
// doesn't exist yet, the install must still succeed. The mayor is just
// the fastest verification path; a human can read the log otherwise.
func sendInstallMail(subject, body string) {
	cmd := exec.Command("mg", "mail", "send", "mayor",
		"--from", "service-install",
		"--subject", subject,
		"--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: mail to mayor failed: %v: %s\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Printf("Mailed install report to mayor: %s\n", subject)
}

func sendInstallSuccessMail(plistPath, logd string, noChange bool) {
	rerun := "fresh install"
	if noChange {
		rerun = "no-op rerun (plist unchanged, service healthy)"
	}
	body := fmt.Sprintf("Plist:        %s\nLog dir:      %s\nResult:       %s\n\nlaunchctl list %s:\n%s",
		plistPath, logd, rerun, launchdLabel, launchctlListOutput())
	sendInstallMail("[install] com.pogo.daemon installed and running", body)
}

func sendInstallFailureMail(err error) {
	body := fmt.Sprintf("Error: %v\n\nlaunchctl print:\n%s\n\nLog tail (~%d bytes):\n%s",
		err, launchctlPrintOutput(), logTailBytes, logTail())
	sendInstallMail("[install] FAILED com.pogo.daemon", body)
}

func launchctlListOutput() string {
	out, _ := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	return strings.TrimRight(string(out), "\n")
}

func launchctlPrintOutput() string {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)
	out, _ := exec.Command("launchctl", "print", target).CombinedOutput()
	return strings.TrimRight(string(out), "\n")
}

const logTailBytes = 4096

func logTail() string {
	logPath := filepath.Join(logDir(), "pogod.log")
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Sprintf("(could not open %s: %v)", logPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Sprintf("(could not stat %s: %v)", logPath, err)
	}
	if fi.Size() > logTailBytes {
		if _, err := f.Seek(-int64(logTailBytes), io.SeekEnd); err != nil {
			return fmt.Sprintf("(could not seek %s: %v)", logPath, err)
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("(could not read %s: %v)", logPath, err)
	}
	return string(data)
}

// verifyLaunchdRunning confirms that launchctl knows about com.pogo.daemon
// and that pogod is reachable. Polls briefly because launchctl load returns
// before the child process is actually serving requests.
func verifyLaunchdRunning() error {
	listed := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
		if len(out) > 0 && !strings.Contains(string(out), "Could not find") {
			listed = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !listed {
		return fmt.Errorf("launchctl list %s did not return the service", launchdLabel)
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := client.HealthCheck(); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("pogod did not become healthy within 10s after launchctl load")
}

func installSystemd() error {
	pogodPath, err := findPogod()
	if err != nil {
		return err
	}

	unitPath := systemdUnitPath()

	if _, err := os.Stat(unitPath); err == nil {
		return fmt.Errorf("service already installed at %s\nRun 'pogo service uninstall' first to reinstall", unitPath)
	}

	if err := os.MkdirAll(systemdUnitDir(), 0755); err != nil {
		return fmt.Errorf("failed to create systemd user directory: %w", err)
	}

	data := systemdData{PogodPath: pogodPath}

	tmpl, err := template.New("unit").Parse(systemdUnitTemplate)
	if err != nil {
		return err
	}

	f, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", unitPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		os.Remove(unitPath)
		return err
	}

	// Reload and enable
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	cmd := exec.Command("systemctl", "--user", "enable", "--now", "pogo.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable failed: %s: %w", string(out), err)
	}

	fmt.Printf("Service installed: %s\n", unitPath)
	fmt.Println("The pogo daemon will now start on login and restart on crash.")
	return nil
}

// Uninstall removes the service file and stops the service.
func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func uninstallLaunchd() error {
	plistPath := launchdPlistPath()

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("no service installed at %s", plistPath)
	}

	cmd := exec.Command("launchctl", "unload", plistPath)
	cmd.Run() // best-effort unload

	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", plistPath, err)
	}

	fmt.Printf("Service removed: %s\n", plistPath)
	return nil
}

func uninstallSystemd() error {
	unitPath := systemdUnitPath()

	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return fmt.Errorf("no service installed at %s", unitPath)
	}

	exec.Command("systemctl", "--user", "disable", "--now", "pogo.service").Run()
	exec.Command("systemctl", "--user", "daemon-reload").Run()

	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", unitPath, err)
	}

	fmt.Printf("Service removed: %s\n", unitPath)
	return nil
}

// Restart restarts the service via the system service manager (launchd/systemd).
// Returns an error if the service is not installed.
func Restart() error {
	switch runtime.GOOS {
	case "darwin":
		return restartLaunchd()
	case "linux":
		return restartSystemd()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func restartLaunchd() error {
	plistPath := launchdPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed at %s", plistPath)
	}
	// kickstart -k forces a restart even if the service is stopped
	cmd := exec.Command("launchctl", "kickstart", "-k", "gui/"+fmt.Sprint(os.Getuid())+"/"+launchdLabel)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback: unload + load for older macOS
		exec.Command("launchctl", "unload", plistPath).Run()
		loadCmd := exec.Command("launchctl", "load", plistPath)
		if out2, err2 := loadCmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl load failed: %s (kickstart failed: %s): %w", string(out2), string(out), err2)
		}
	}
	return nil
}

func restartSystemd() error {
	unitPath := systemdUnitPath()
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed at %s", unitPath)
	}
	cmd := exec.Command("systemctl", "--user", "restart", "pogo.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart failed: %s: %w", string(out), err)
	}
	return nil
}

// Status returns whether the service is installed and its path.
func Status() (installed bool, path string) {
	switch runtime.GOOS {
	case "darwin":
		p := launchdPlistPath()
		_, err := os.Stat(p)
		return err == nil, p
	case "linux":
		p := systemdUnitPath()
		_, err := os.Stat(p)
		return err == nil, p
	default:
		return false, ""
	}
}
