package service

import (
	"fmt"
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

// logDir is ~/.pogo/log per mg-1416 spec — colocates daemon logs with
// the rest of pogo state instead of scattering them under ~/.local/share.
func logDir() string {
	return filepath.Join(pogoHome(), "log")
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

func installLaunchd() error {
	pogodPath, err := findPogod()
	if err != nil {
		return err
	}

	plistPath := launchdPlistPath()
	home, _ := os.UserHomeDir()
	logd := logDir()

	if err := os.MkdirAll(logd, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Idempotent reinstall: if the plist already exists, unload + remove
	// it before writing the new one. This makes `pogo service install`
	// safe to rerun after a plist content update.
	if _, err := os.Stat(plistPath); err == nil {
		fmt.Printf("Existing service found at %s — replacing.\n", plistPath)
		exec.Command("launchctl", "unload", plistPath).Run() // best-effort
		if err := os.Remove(plistPath); err != nil {
			return fmt.Errorf("failed to remove existing plist: %w", err)
		}
	}

	stopRunningPogod()

	data := launchdData{
		Label:      launchdLabel,
		PogodPath:  pogodPath,
		LogDir:     logd,
		Home:       home,
		PogoHome:   pogoHome(),
		PluginPath: filepath.Join(pogoHome(), "plugin"),
		Path:       launchdPath(),
	}

	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		return err
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", plistPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		os.Remove(plistPath)
		return err
	}

	cmd := exec.Command("launchctl", "load", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %s: %w", string(out), err)
	}

	if err := verifyLaunchdRunning(); err != nil {
		return fmt.Errorf("service loaded but verification failed: %w", err)
	}

	fmt.Printf("Service installed: %s\n", plistPath)
	fmt.Printf("Logs: %s/pogod.log\n", logd)
	fmt.Println("The pogo daemon will now start on login and restart on crash.")
	return nil
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
