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
	"strings"
	"text/template"
	"time"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
)

const launchdLabel = "com.pogo.daemon"

// LaunchdLabel exports the daemon's launchd label for callers that must ask
// launchd about the SAME job this package installs. Exported rather than
// duplicated as a literal at the call site: a check that asks about a label
// this package does not install would report on a job nobody manages, and the
// disagreement would be invisible — which is the exact failure shape mg-fa79
// is about, one level up.
func LaunchdLabel() string { return launchdLabel }

// launchdPlistTemplate matches the mg-1416 spec: ProcessType=Interactive
// (prevents App Nap throttling of refinery polls + agent idle detection),
// unconditional KeepAlive (auto-restart on any exit), explicit PATH so
// spawned crew agents can find the agent harness binary, git, and mg,
// POGO_HOME and HOME so the daemon resolves the right state dir under
// launchd's minimal env.
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

// LauncherEnv overrides the program the installed service execs. It outranks
// the [service] launcher config key. See resolveLauncher.
const LauncherEnv = "POGOD_LAUNCHER"

// launcher is the program the installed launchd plist / systemd unit execs, and
// where that choice came from. Source is printed in every message that names
// Path, because "which knob produced this path" is the first question a reader
// of a refusal has.
type launcher struct {
	Path   string
	Source string
}

const (
	launcherSourceEnv     = LauncherEnv
	launcherSourceConfig  = "[service] launcher"
	launcherSourcePath    = "pogod on PATH"
	launcherSourceAdopted = "adopted from the installed plist (--adopt-launcher)"
)

// resolveLauncher picks the program the service execs: $POGOD_LAUNCHER, then
// the [service] launcher config key, then pogod on PATH (drellem2/pogo#105).
//
// Before the override existed, the installer always wrote pogod's PATH location
// into ProgramArguments. On a host whose plist named a credential-injecting
// wrapper, every `pogo service install` replaced the wrapper with bare pogod and
// agents lost their credentials with nothing reporting it. An override is
// checked for existence and the executable bit here, at install time: launchd
// reports a missing program only as a job that never starts.
func resolveLauncher() (launcher, error) {
	if p := strings.TrimSpace(os.Getenv(LauncherEnv)); p != "" {
		return checkedLauncher(p, launcherSourceEnv)
	}
	if p := strings.TrimSpace(config.Load().Service.Launcher); p != "" {
		return checkedLauncher(p, launcherSourceConfig)
	}
	p, err := findPogod()
	if err != nil {
		return launcher{}, err
	}
	return launcher{Path: p, Source: launcherSourcePath}, nil
}

// checkedLauncher validates an explicitly named launcher and makes it absolute.
// A leading ~/ is expanded because config.toml values are written by hand, and
// launchd does no expansion of its own.
func checkedLauncher(p, source string) (launcher, error) {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return launcher{}, fmt.Errorf("launcher %q (from %s): %w", p, source, err)
	}
	if err := checkExecutable(abs); err != nil {
		return launcher{}, fmt.Errorf("launcher %s (from %s) %w", abs, source, err)
	}
	return launcher{Path: abs, Source: source}, nil
}

func checkExecutable(path string) error {
	fi, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("does not exist")
	case err != nil:
		return fmt.Errorf("cannot be read: %w", err)
	case fi.IsDir():
		return fmt.Errorf("is a directory, not a program")
	case fi.Mode().Perm()&0111 == 0:
		return fmt.Errorf("is not executable (mode %s)", fi.Mode().Perm())
	}
	return nil
}

// InstallOptions are the `pogo service install` flags that decide what happens
// to an installed plist whose program is neither pogod nor the configured
// launcher. At most one may be set.
type InstallOptions struct {
	// ForceLauncher overwrites the installed program with the resolved
	// launcher. The previous plist is still backed up.
	ForceLauncher bool
	// AdoptLauncher carries the installed program forward into the rendered
	// plist instead of replacing it.
	AdoptLauncher bool
}

// plistProgram returns ProgramArguments[0] of a plist, or "" when there is no
// plist, it does not decode, or it names no program.
func plistProgram(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	doc, err := decodePlistDict(data)
	if err != nil {
		return ""
	}
	args, _ := doc["ProgramArguments"].([]any)
	if len(args) == 0 {
		return ""
	}
	s, _ := args[0].(string)
	return strings.TrimSpace(s)
}

// chooseInstallLauncher decides which program the plist about to be written
// will exec, given the plist already installed. It is the #105 guard: when the
// installed ProgramArguments[0] is neither the pogod on PATH nor the resolved
// launcher, it is something a person put there — in the reported case a wrapper
// that injects agent credentials — and rewriting it silently is the bug. So the
// install refuses, naming the path, unless told which way to go.
//
// pogodPath may be "" when pogod is not on PATH (a host that runs pogod only
// through a configured launcher).
//
// A pogod at a DIFFERENT path from the one on PATH (a moved binary) is refused
// too. That is deliberate: the guard cannot tell a moved pogod from a wrapper
// that happens to be named after it, and the refusal says how to proceed.
func chooseInstallLauncher(installed []byte, resolved launcher, pogodPath, plistPath string, opts InstallOptions) (launcher, error) {
	if opts.ForceLauncher && opts.AdoptLauncher {
		return launcher{}, fmt.Errorf("--force-launcher and --adopt-launcher are mutually exclusive")
	}
	current := plistProgram(installed)
	if current == "" || current == resolved.Path || (pogodPath != "" && current == pogodPath) {
		return resolved, nil
	}
	switch {
	case opts.AdoptLauncher:
		if err := checkExecutable(current); err != nil {
			return launcher{}, fmt.Errorf("--adopt-launcher: the installed launcher %s (ProgramArguments[0] of %s) %w", current, plistPath, err)
		}
		fmt.Printf("Keeping the installed launcher %s (--adopt-launcher). Set [service] launcher = %q in config.toml so later installs and `pogo doctor` agree with it.\n", current, current)
		return launcher{Path: current, Source: launcherSourceAdopted}, nil
	case opts.ForceLauncher:
		fmt.Printf("Replacing the installed launcher %s with %s (%s) — --force-launcher.\n", current, resolved.Path, resolved.Source)
		return resolved, nil
	}
	configured := "no launcher is configured"
	if resolved.Source != launcherSourcePath {
		configured = fmt.Sprintf("the configured launcher is %s (from %s)", resolved.Path, resolved.Source)
	}
	return launcher{}, fmt.Errorf("refusing to install: %s runs a custom launcher, %s (its ProgramArguments[0]), which is not pogod on PATH (%s), and %s. "+
		"Installing would replace it, and anything that launcher does before starting pogod — such as injecting credentials — would silently stop. Nothing was changed. Choose one:\n"+
		"  - keep it: set [service] launcher = %q in config.toml (or export %s), then re-run `pogo service install`\n"+
		"  - keep it for this install only: `pogo service install --adopt-launcher`\n"+
		"  - replace it: `pogo service install --force-launcher` (the old plist is kept as a .bak)",
		plistPath, current, orNone(pogodPath), configured, current, LauncherEnv)
}

func orNone(s string) string {
	if s == "" {
		return "not found"
	}
	return s
}

// backupTimeLayout is the timestamp suffix on plist backups: compact ISO-8601,
// sortable, and free of ':' (same layout as installed-prompt backups).
const backupTimeLayout = "2006-01-02T150405Z"

// backupNow is the clock for backup names; tests replace it.
var backupNow = func() time.Time { return time.Now().UTC() }

// writePlistWithBackup writes rendered to path, first copying the previous
// contents (if any, and if different) to <path>.bak.<timestamp>. Every launchd
// installer in this package writes through it, so no install destroys a plist
// it cannot give back (drellem2/pogo#105). The backup keeps a timestamp rather
// than overwriting one <path>.bak: the plist worth keeping is usually the
// first one replaced, and a second install would otherwise overwrite it.
//
// The backup's name does not end in .plist, so launchd does not load it from
// ~/Library/LaunchAgents as a second job with the same label.
//
// It returns the backup path, or "" when nothing was backed up.
func writePlistWithBackup(path string, existing []byte, rendered string) (string, error) {
	if string(existing) == rendered {
		return "", nil
	}
	backup := ""
	if len(existing) > 0 {
		backup = path + ".bak." + backupNow().Format(backupTimeLayout)
		if err := os.WriteFile(backup, existing, 0644); err != nil {
			return "", fmt.Errorf("failed to back up %s to %s (nothing was overwritten): %w", path, backup, err)
		}
		fmt.Printf("Backed up the previous %s to %s\n", filepath.Base(path), backup)
	}
	if err := os.WriteFile(path, []byte(rendered), 0644); err != nil {
		return backup, fmt.Errorf("failed to write %s: %w", path, err)
	}
	return backup, nil
}

func launchdPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// kickstartLaunchdTarget returns the gui/$UID/<label> service target used by
// `launchctl kickstart` and `launchctl print`. The gui/$UID prefix scopes
// the operation to the current GUI session domain — without it launchctl
// errors with "Could not find specified service". Centralized so install
// and restart paths use one canonical format.
func kickstartLaunchdTarget() string {
	return kickstartTargetForLabel(launchdLabel)
}

// kickstartTargetForLabel returns the gui/$UID/<label> service target for an
// arbitrary launchd label. Same domain form as kickstartLaunchdTarget; the
// tier-1 reaper (internal/reaper) uses it to kickstart jobs other than pogod.
func kickstartTargetForLabel(label string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

// KickstartJob forces a demand-spawn restart of the launchd job `label`
// (gui/$UID/<label>) and returns the pid launchd assigns afterward.
//
// `kickstart -k` kills the current instance (if any) and re-runs the job. It is
// a DEMAND spawn, so it works on this host even though the nondemand-spawn wedge
// (mg-50e0) blocks KeepAlive/RunAtLoad/StartInterval. This is the operation the
// tier-1 reaper drives; centralizing it here keeps every launchctl call in one
// package and lets the reaper stay launchd-free and unit-testable.
//
// The returned pid is best-effort: kickstart itself prints nothing useful, so
// the pid is read from a follow-up `launchctl list <label>`. A zero pid with a
// nil error means the restart was issued but launchd had not yet assigned a pid
// by the time we looked; the reaper's next sweep will observe the heartbeat and
// judge liveness regardless — the pid is for the log line, not the decision.
func KickstartJob(label string) (int, error) {
	target := kickstartTargetForLabel(label)
	if out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("launchctl kickstart -k %s: %s: %w", target, strings.TrimSpace(string(out)), err)
	}
	out, err := exec.Command("launchctl", "list", label).CombinedOutput()
	if err != nil {
		return 0, nil
	}
	pid, _ := parseLaunchctlListPID(string(out))
	return pid, nil
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

// pogoHome delegates to config.PogoHome so the launchd plist, recovery
// queue, and every other service-managed path agree with the daemon's own
// state-dir resolution (including the legacy POGO_HOME=$HOME normalization).
func pogoHome() string {
	return config.PogoHome()
}

// logDir is ~/Library/Logs/pogo on macOS — the Apple-standard location for
// user-scope app logs. Picked over ~/.pogo/log to avoid surprising users
// whose $HOME root may already contain unrelated files (e.g. a bare "log"
// file from another tool) and to follow the platform convention so Console.app
// surfaces the daemon's output naturally.
func logDir() string {
	return config.PogodLogDir()
}

// launchdPath builds a PATH that includes the directories where pogod's
// children (the agent harness binary, git, mg, pogo) actually live on a
// typical macOS dev box. The pogod binary itself is invoked by absolute
// path; this PATH is for the subprocesses it spawns.
//
// The list is harness-agnostic by design: agent-harness CLIs install into
// one of ~/.local/bin, a global npm bin, or Homebrew, all of which are
// covered below. A future provider that lands its binary somewhere exotic
// could contribute extra dirs (see docs/design/multi-provider-architecture-survey.md
// §2.4 C13); none does today, so no provider plumbing is wired here.
func launchdPath() string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, ".local", "bin"), // agent-harness CLIs (e.g. claude) often land here
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
func Install(opts InstallOptions) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(opts)
	case "linux":
		return installSystemd()
	default:
		return fmt.Errorf("unsupported OS: %s (supported: darwin, linux)", runtime.GOOS)
	}
}

// quiesceCrew and the restore that owes it live in installorchestration.go,
// alongside the install sequence that borrows fleet-wide dispatch (mg-6515).

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
//
// ProgramArguments[0] is the resolved launcher (see resolveLauncher), not
// necessarily pogod.
func renderLaunchdPlist() (string, launchdData, error) {
	l, err := resolveLauncher()
	if err != nil {
		return "", launchdData{}, err
	}
	return renderLaunchdPlistFor(l.Path)
}

// renderLaunchdPlistFor renders the daemon plist with program as its
// ProgramArguments[0].
func renderLaunchdPlistFor(program string) (string, launchdData, error) {
	home, _ := os.UserHomeDir()
	data := launchdData{
		Label:      launchdLabel,
		PogodPath:  program,
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

// launchctlListOutputForLabel returns the raw `launchctl list LABEL` output.
// Empty string on subprocess error so callers can treat missing as "not
// loaded" without a separate err path. This is the single source of truth
// the install fast-path consults — both the loaded check and the
// supervising check are derived from one snapshot to avoid a TOCTTOU window
// where an orphan transitions in/out between two separate launchctl calls.
func launchctlListOutputForLabel() string {
	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

// isLaunchdLoadedFromOutput reports whether launchctl knows the label,
// derived from a single `launchctl list LABEL` snapshot. "Loaded" here
// just means launchctl has the label registered — the process behind it
// may be missing, crash-looping, or never-spawned (orphan case).
func isLaunchdLoadedFromOutput(output string) bool {
	return len(output) > 0 && !strings.Contains(output, "Could not find")
}

// canSkipInstall reports whether installLaunchd can short-circuit to a
// no-op based on the on-disk plist comparison and the launchctl snapshot.
// The HTTP healthcheck is the caller's final gate (it requires live HTTP
// and is therefore not part of this pure predicate).
//
// Returns false in any of these cases:
//   - rendered plist differs from disk (must be rewritten + reloaded)
//   - launchctl has no record of the label (must be loaded)
//   - launchctl knows the label but has no PID assigned — the orphan /
//     never-spawned regression case (mg-2c55, mg-df4a). A pogod started
//     outside launchd by crew-respawn answers /health on :10000 AND
//     keeps the plist registered, but launchd has never spawned the
//     daemon (`runs = 0`, no PID line). The orchestrated install must
//     take over to replace the orphan with a real launchd-supervised
//     process.
//
// Detection is intentionally based on launchctl's PID assignment alone.
// PPID is not a reliable orphan signal on macOS — every launchd-spawned
// daemon also reports PPID=1 because launchd is PID 1. Only launchctl's
// internal PID-assignment field distinguishes "supervised by launchd"
// from "running outside launchd".
func canSkipInstall(plistMatches bool, launchctlListOutput string) bool {
	if !plistMatches {
		return false
	}
	if !isLaunchdLoadedFromOutput(launchctlListOutput) {
		return false
	}
	_, ok := parseLaunchctlListPID(launchctlListOutput)
	return ok
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

// CheckInstallLauncher runs the #105 launcher guard on its own, without
// touching anything. `pogo service install --detach` calls it in the parent so
// a refusal reaches the caller's terminal instead of only the detached log and
// the failure mail. It is a no-op off darwin.
func CheckInstallLauncher(opts InstallOptions) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	_, err := installLauncher(opts)
	return err
}

// installLauncher resolves the launcher and applies the guard against the
// installed plist.
func installLauncher(opts InstallOptions) (launcher, error) {
	resolved, err := resolveLauncher()
	if err != nil {
		return launcher{}, err
	}
	pogodPath, _ := findPogod()
	plistPath := launchdPlistPath()
	existing, _ := os.ReadFile(plistPath)
	return chooseInstallLauncher(existing, resolved, pogodPath, plistPath, opts)
}

func installLaunchd(opts InstallOptions) (retErr error) {
	// restore is what the orchestrated sequence did about the fleet-wide
	// dispatch it stopped. It is captured here so the failure mail carries
	// it: the mail is the only artifact a failed install leaves behind, and
	// "orchestration stopped + no install report" was the signature of the
	// silent variant (mg-6515).
	var restore orchestrationRestore

	// Self-report on the way out so a polecat can fire-and-forget the
	// install (`pogo service install --detach`) and have the post-install
	// mayor pick up the result via mail.
	defer func() {
		if retErr != nil {
			sendInstallFailureMail(retErr, restore)
		}
	}()

	// The launcher guard runs first — before the orchestration quiesce and
	// before anything is written — so a refusal leaves the fleet and the
	// installed plist exactly as they were (drellem2/pogo#105).
	l, err := installLauncher(opts)
	if err != nil {
		return err
	}
	rendered, data, err := renderLaunchdPlistFor(l.Path)
	if err != nil {
		return err
	}

	plistPath := launchdPlistPath()
	if err := os.MkdirAll(data.LogDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	existing, _ := os.ReadFile(plistPath)
	plistMatches := string(existing) == rendered
	listOutput := launchctlListOutputForLabel()
	loaded := isLaunchdLoadedFromOutput(listOutput)

	// Fast path: identical plist already supervised by launchd AND pogod
	// healthy → no-op. Lets the post-install mayor rerun `pogo service
	// install` as a probe without bouncing the daemon. canSkipInstall is
	// the strict gate (see its docstring): just "loaded" is not enough,
	// because an orphan pogod started outside launchd by crew-respawn
	// keeps the plist registered AND answers /health on :10000, but
	// launchd has never actually spawned it. mg-2c55/mg-df4a regression
	// — accepting that state as healthy silently defeats mg-ae84's
	// orchestration.
	if canSkipInstall(plistMatches, listOutput) {
		if err := client.HealthCheck(); err == nil {
			fmt.Printf("Service already installed and healthy at %s — no changes.\n", plistPath)
			sendInstallSuccessMail(plistPath, data.LogDir, true)
			return nil
		}
	}

	// Orchestrated install sequence — prevents the crew/launchd race
	// (architect's analysis 2026-04-28T11:37Z, mg-ae84). Each step blocks
	// until the previous one is complete so launchd-pogod boots into a clean
	// environment with no other process racing to claim :10000. The steps
	// themselves are wired here; the ordering, the quiesce and the restore
	// that owes it live in runOrchestratedInstall.
	steps := installSteps{
		unloadPrior: func() {
			if loaded {
				fmt.Println("Existing service is loaded — unloading before reinstall.")
				exec.Command("launchctl", "unload", plistPath).Run() // best-effort
			}
		},
		stopPogod: stopRunningPogod,
		drainPort: func() error { return waitForPogodPortDrain(10 * time.Second) },
		writePlist: func() error {
			_, err := writePlistWithBackup(plistPath, existing, rendered)
			return err
		},
		loadPlist: func() error {
			if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
				return fmt.Errorf("launchctl load failed: %s: %w", string(out), err)
			}
			return nil
		},
		kickstart: func() error {
			target := kickstartLaunchdTarget()
			if out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
				return fmt.Errorf("launchctl kickstart %s failed: %s: %w", target, string(out), err)
			}
			return nil
		},
		verify: func() error {
			if err := verifyLaunchdRunning(); err != nil {
				return fmt.Errorf("service loaded but verification failed: %w", err)
			}
			return nil
		},
	}

	restore, err = runInstallSequence(steps)
	if err != nil {
		return err
	}

	fmt.Printf("Service installed: %s\n", plistPath)
	fmt.Printf("Logs: %s/pogod.log\n", data.LogDir)
	fmt.Println("The pogo daemon will now start on login and restart on crash.")
	sendInstallSuccessMail(plistPath, data.LogDir, false)
	return nil
}

// runInstallSequence runs the orchestrated install against the live pogod. It
// is a variable so the drellem2/pogo#105 tests can drive the real installLaunchd
// up to this point — the launcher guard, the render, the plist read — and
// stop there, with no route to the live fleet, launchd, or :10000.
var runInstallSequence = func(steps installSteps) (orchestrationRestore, error) {
	return runOrchestratedInstall(liveOrchestrator{}, steps)
}

// installMailer delivers the install report. A variable for the same tests:
// a refusal still mails, and a test must read that mail rather than send it.
var installMailer = sendInstallMail

// sendInstallMail is best-effort: if mg isn't on PATH or the coordinator's
// inbox doesn't exist yet, the install must still succeed. The coordinator is
// just the fastest verification path; a human can read the log otherwise. The
// name comes from installMailCoordinator, which resolves it from the PINNED
// config (mg-e545).
func sendInstallMail(subject, body string) {
	coordinator := installMailCoordinator()
	cmd := exec.Command("mg", "mail", "send", coordinator,
		"--from", "service-install",
		"--subject", subject,
		"--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: mail to %s failed: %v: %s\n", coordinator, err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Printf("Mailed install report to %s: %s\n", coordinator, subject)
}

// installMailCoordinator resolves the coordinator's name for install-report
// mail from the PINNED config (PinAndLoad), not a raw Load: on the first process
// of a build that flipped the role defaults (mg-ce47), an existing install whose
// config.toml predates the [agents] keys would otherwise address the report to
// the NEW default's mailbox nobody reads, while the pinned config keeps the
// legacy one. PinAndLoad pins the frozen legacy names (idempotent — a no-op on a
// fresh install or once the keys are present) and re-reads, so we resolve what
// config actually carries. `pogo service install` writes no prompts, so
// IsExistingInstall reflects the true prior state here (mg-e545, xref mg-bc47 /
// 10d673f).
//
// The rename guard then runs for the same reason it runs at every other seam
// that resolves the coordinator's name: a name config invented while a
// differently-named coordinator is running addresses a mailbox nobody reads
// (mg-cf9e).
func installMailCoordinator() string {
	cfg, _, _ := config.PinAndLoad(config.IsExistingInstall())
	cfg, _ = config.GuardRunningCoordinator(cfg)
	return cfg.Agents.CoordinatorName()
}

func sendInstallSuccessMail(plistPath, logd string, noChange bool) {
	rerun := "fresh install"
	if noChange {
		rerun = "no-op rerun (plist unchanged, service healthy)"
	}
	body := fmt.Sprintf("Plist:        %s\nLog dir:      %s\nResult:       %s\n\nlaunchctl list %s:\n%s",
		plistPath, logd, rerun, launchdLabel, launchctlListOutput())
	installMailer("[install] com.pogo.daemon installed and running", body)
}

// sendInstallFailureMail reports a failed install. The orchestration line is
// second, above the launchctl dump, because it is the part that says whether
// the FLEET is down as well as the install: a failure after step 1 leaves
// dispatch stopped, and before mg-6515 the mail said nothing about it at all.
// The subject changes when dispatch is still down, so the state is visible in
// a mailbox listing without opening anything.
func sendInstallFailureMail(err error, restore orchestrationRestore) {
	body := fmt.Sprintf("Error: %v\n\n%s\n\nlaunchctl print:\n%s\n\nLog tail (~%d bytes):\n%s",
		err, restore.String(), launchctlPrintOutput(), logTailBytes, logTail())
	installMailer(installFailureSubject(restore), body)
}

// installFailureSubject escalates the subject line when the install left
// fleet-wide dispatch down. A reader skimming a mailbox sees the residual state
// without opening anything — and the residual state is the part that outlives
// the failed install.
func installFailureSubject(restore orchestrationRestore) string {
	if restore.Attempted && !restore.OK {
		return "[install] FAILED com.pogo.daemon — ORCHESTRATION STILL STOPPED"
	}
	return "[install] FAILED com.pogo.daemon"
}

func launchctlListOutput() string {
	out, _ := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	return strings.TrimRight(string(out), "\n")
}

func launchctlPrintOutput() string {
	out, _ := exec.Command("launchctl", "print", kickstartLaunchdTarget()).CombinedOutput()
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

// verifyLaunchdRunning confirms that launchctl knows about com.pogo.daemon,
// that pogod is reachable, and — since mg-ed4a — WHICH REVISION it is running.
// Polls briefly because launchctl load returns before the child process is
// actually serving requests.
//
// The first two questions are about existence: `launchctl list` says a job is
// registered, /health says something is listening. Neither says the right thing
// is listening, and for eight days on this box the answer was that it was not:
// a healthy pogod on a 2026-07-30 binary, 92 commits behind, passing both of
// these checks every time they ran. The revision check is the third question.
//
// It is REPORT-ONLY and does not affect this function's error. That is mg-ed4a's
// explicit instruction — installs currently succeed against a stale daemon and
// something may depend on that, so the observation ships first and the decision
// to gate on it is a separate change. See internal/service/revision.go.
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
			reportDaemonRevision("install", restartVerifyTimeout)
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("pogod did not become healthy within 10s after launchctl load")
}

func installSystemd() error {
	l, err := resolveLauncher()
	if err != nil {
		return err
	}
	pogodPath := l.Path

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

// restartLaunchd kickstarts com.pogo.daemon, then asks the daemon that comes
// back WHICH REVISION it is (mg-ed4a).
//
// Before that check this function verified nothing at all: `launchctl kickstart`
// returning 0 means launchd accepted the request, not that a healthy daemon
// exists afterwards and certainly not that it is running the binary you meant.
// A kickstart re-execs whatever is on disk, so silently reinstating a stale
// binary is its NORMAL behaviour when the disk is stale — which is how this box
// spent eight days healthy and 92 commits behind.
//
// REPORT-ONLY: the verdict does not become this function's error. Restart() is
// what `pogo server start` calls when /health is down, and failing a server
// start over a revision mismatch would refuse to start a server for a reason
// that is not about starting one. `pogo service verify-revision` is the same
// check with an exit code, for callers that want the gate.
func restartLaunchd() error {
	plistPath := launchdPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed at %s", plistPath)
	}
	// kickstart -k forces a restart even if the service is stopped
	cmd := exec.Command("launchctl", "kickstart", "-k", kickstartLaunchdTarget())
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback: unload + load for older macOS
		exec.Command("launchctl", "unload", plistPath).Run()
		loadCmd := exec.Command("launchctl", "load", plistPath)
		if out2, err2 := loadCmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl load failed: %s (kickstart failed: %s): %w", string(out2), string(out), err2)
		}
	}
	reportDaemonRevision("restart", restartVerifyTimeout)
	return nil
}

// restartSystemd gets the same post-restart revision report as the launchd
// path, on the same report-only terms. `systemctl restart` exiting 0 says the
// unit was restarted, which is the same not-quite-the-question answer
// `launchctl kickstart` gives.
func restartSystemd() error {
	unitPath := systemdUnitPath()
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed at %s", unitPath)
	}
	cmd := exec.Command("systemctl", "--user", "restart", "pogo.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart failed: %s: %w", string(out), err)
	}
	reportDaemonRevision("restart", restartVerifyTimeout)
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
