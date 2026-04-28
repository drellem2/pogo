package service

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// quiesceCrew tells the running pogod to stop orchestration (agents +
// refinery) so crew agents can't auto-respawn pogod via RunWithHealthCheck
// during the launchd handoff. Without this step, a crew agent's `mg`/`pogo`
// command issued between `pogo server stop` and `launchctl load` will
// trigger client.StartServer(), which spawns a non-launchd pogod that wins
// the :10000 bind and silently knocks launchd's pogod out (the deterministic
// race observed on mg-9cdc, 2026-04-28). No-op if pogod isn't running.
func quiesceCrew() {
	if err := client.HealthCheck(); err != nil {
		return
	}
	fmt.Println("Quiescing crew (stopping orchestration)...")
	if err := client.StopOrchestration(); err != nil {
		fmt.Printf("  warning: %v (continuing anyway)\n", err)
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

// pogodPort is the well-known port pogod binds. waitForSocketDrain polls it
// until nothing answers (i.e. it's free for the launchd-supervised pogod to
// claim) or until timeout.
const pogodPort = "127.0.0.1:10000"

// waitForPogodPortDrain is the production entry point — calls drainAddr
// against the real pogod port.
func waitForPogodPortDrain(timeout time.Duration) error {
	return drainAddr(pogodPort, timeout)
}

// drainAddr polls a TCP address until it is no longer accepting connections.
// Uses Dial (not Listen) so we don't momentarily own the port ourselves and
// create a fresh window for an outside racer to bind. Fails with a clear
// error on timeout — the caller must surface this rather than blindly run
// `launchctl load`, since a stranger holding the port will cause
// launchd-pogod to exit silently.
//
// Address-parameterized so tests can exercise the polling logic against a
// test-local listener without touching the real :10000.
func drainAddr(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return nil // port is free
		}
		c.Close()
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s to drain (something still owns the port)", timeout, addr)
		}
		time.Sleep(200 * time.Millisecond)
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

// isLaunchdSupervising reports whether launchd is the supervisor of the
// pogod that's actually answering /health. Stricter than isLaunchdLoaded
// (which only confirms the label is registered) and stricter than the
// initial mg-2c55 gate (which only checked whether `launchctl list
// LABEL` had a "PID" line).
//
// The mg-df4a regression revealed why the bare PID-line check is not
// enough: an orphan pogod can hold :10000 and answer /health while
// launchctl reports a different PID — or even no PID at all — for the
// launchd-supervised slot. Both states must produce a fall-through to
// the orchestrated reinstall, otherwise we treat the orphan as healthy.
//
// To close that gap we cross-check launchctl's PID against the running
// pogod's lockfile (the file at $TMPDIR/pogo.pid that pogod itself
// writes on startup, see cmd/pogod/main.go). The fast-path is safe iff
// launchctl claims a PID AND that PID matches the lockfile PID. Any
// mismatch — orphan answering /health, stale launchctl PID, missing
// lockfile — falls through to the orchestrated install.
func isLaunchdSupervising() bool {
	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		return false
	}
	runningPID, lockErr := readPogodLockfilePID()
	return launchctlSupervisesRunningPogod(string(out), runningPID, lockErr)
}

// launchctlSupervisesRunningPogod is the testable core of
// isLaunchdSupervising: given the raw `launchctl list LABEL` output and
// the PID read from the pogod lockfile, decide whether launchd is in
// fact supervising the running pogod. Split out so tests can exercise
// the orphan vs. supervised vs. missing-lockfile branches without
// shelling out.
func launchctlSupervisesRunningPogod(launchctlOut string, runningPID int, lockErr error) bool {
	launchctlPID, ok := parseLaunchctlListPID(launchctlOut)
	if !ok {
		return false
	}
	if lockErr != nil {
		return false
	}
	return launchctlPID == runningPID
}

// readPogodLockfilePID reads $TMPDIR/pogo.pid (pogod's lockfile, written
// by cmd/pogod/main.go on startup) and returns the PID stored there.
// Returns an error when the file is missing, unreadable, or doesn't
// contain a parseable integer.
//
// This is the source of truth for "which process is the running pogod"
// — the same lockfile client.StopServer reads to find pogod via
// lock.GetOwner. We re-read it directly (rather than going through
// nightlyone/lockfile) because the install fast-path only needs the
// PID, and loading the lockfile package's owner-resolution path adds
// process-existence checks that would mask a stale lockfile from us.
func readPogodLockfilePID() (int, error) {
	pidPath := filepath.Join(os.TempDir(), "pogo.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("malformed pogod lockfile %s: %w", pidPath, err)
	}
	return pid, nil
}

// parseLaunchctlListPID extracts the PID assignment from `launchctl
// list LABEL` output. Returns (pid, true) when a numeric "PID" key is
// present (process supervised and running), or (0, false) when absent
// (loaded but not running — e.g. never-spawned, or post-crash before
// launchd's restart kicks in).
//
// Sample output (supervised):
//
//	{
//	    "Label" = "com.pogo.daemon";
//	    "PID" = 12345;
//	    "Program" = "...";
//	};
//
// Sample output (orphan / not running): same dict minus the "PID" line.
func parseLaunchctlListPID(output string) (int, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"PID"`) {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		rest := strings.TrimSpace(line[eq+1:])
		rest = strings.TrimSuffix(rest, ";")
		rest = strings.TrimSpace(rest)
		var pid int
		if _, err := fmt.Sscanf(rest, "%d", &pid); err != nil {
			continue
		}
		return pid, true
	}
	return 0, false
}

func installLaunchd() (retErr error) {
	// Self-report on the way out so a polecat can fire-and-forget the
	// install (`pogo service install --detach`) and have the post-install
	// mayor pick up the result via mail.
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

	// Fast path: identical plist already supervised by launchd AND pogod
	// healthy → no-op. Lets the post-install mayor rerun `pogo service
	// install` as a probe without bouncing the daemon.
	//
	// The supervising check (mg-2c55, tightened in mg-df4a) is the
	// strict gate: just "loaded" is not enough, because an orphan pogod
	// (PPID=1, started outside launchd by crew-respawn) keeps the plist
	// registered AND answers /health on :10000 — but launchd has no PID
	// assigned, or assigns a PID that doesn't match the actual running
	// pogod. Accepting that state as healthy silently defeats mg-ae84's
	// orchestration; require launchctl's PID to match the running
	// pogod's lockfile PID.
	if plistMatches && loaded && isLaunchdSupervising() {
		if err := client.HealthCheck(); err == nil {
			fmt.Printf("Service already installed and healthy at %s — no changes.\n", plistPath)
			sendInstallSuccessMail(plistPath, data.LogDir, true)
			return nil
		}
	}

	// Orchestrated install sequence — prevents the crew/launchd race
	// (architect's analysis 2026-04-28T11:37Z, mg-ae84). Each step blocks
	// until the previous one is complete so launchd-pogod boots into a clean
	// environment with no other process racing to claim :10000.
	//
	// Step 1: Quiesce crew. Tell the running pogod to drop crew agents so
	// they can't issue a `pogo`/`mg` command that auto-respawns a non-launchd
	// pogod via client.RunWithHealthCheck.
	quiesceCrew()

	// Step 2: Unload any prior plist. Best-effort — handles the
	// loaded-and-running, loaded-and-stopped, and loaded-with-stale-config
	// cases uniformly. Subsumes mg-6095 (idempotency against pre-loaded
	// plist).
	if loaded {
		fmt.Println("Existing service is loaded — unloading before reinstall.")
		exec.Command("launchctl", "unload", plistPath).Run() // best-effort
	}

	// Step 3: Stop any pogod still running (manual or formerly-launchd).
	stopRunningPogod()

	// Step 4: Wait for :10000 to drain. If a stranger holds the port past
	// the timeout, fail fast — loading the plist now would just produce
	// another silent launchd-pogod exit.
	if err := waitForPogodPortDrain(10 * time.Second); err != nil {
		return err
	}

	// Step 5: Write plist (if it changed) and load it.
	if !plistMatches {
		if err := os.WriteFile(plistPath, []byte(rendered), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", plistPath, err)
		}
	}
	cmd := exec.Command("launchctl", "load", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load failed: %s: %w", string(out), err)
	}

	// Step 6: Verify launchd-pogod is bound and answering on /health.
	if err := verifyLaunchdRunning(); err != nil {
		return fmt.Errorf("service loaded but verification failed: %w", err)
	}

	// Step 7: Crew agents auto-restart under the new pogod via
	// auto_start=true in their prompt frontmatter (mayor.md, pm-template.md).
	// pogod boots in ModeFull (server.New), so refinery + agent registry
	// are already running by the time verifyLaunchdRunning returns.

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
