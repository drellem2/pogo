package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// The nightly deploy agent (mg-42ac, designed under mg-b7d0) is the out-of-tree
// TRIGGER for scripts/pogo-self-deploy. It exists because that script cannot
// call itself: its first line is an ancestry guard that refuses any caller
// inside pogod's process tree (mg-1bbf), and every crew agent and polecat is
// such a descendant. A LaunchAgent is parented by launchd, so it clears the
// guard by construction.
//
// It is a THIRD job, not a mode of com.pogo.recovery. Recovery is the tier-3
// safety net that bounces a wedged pogod; a deploy rebuilds it from main. Fold
// them together and every emergency restart becomes a rebuild from whatever is
// on main at that moment — which is the last thing you want from the mechanism
// you reach for when the box is already broken (mg-cf48 declined exactly this).
//
// Kept distinct from Install() and InstallRecovery() for the same reason those
// two are distinct from each other: each of the three has to be installable on
// a box where the other two are broken.

const deployLabel = "com.pogo.deploy"

// deployLogName is the file the nightly's stdout AND stderr are redirected to.
//
// Named once and used both by the plist template below and by DeployLogPath,
// because the two must agree: the log is the only record that a night's fire
// HAPPENED, so a detector reading a different path than the plist writes gets
// an empty file — and an empty deploy log is indistinguishable from a deploy
// that never fired, which is the exact reading this name exists to make
// trustworthy. One file shipped under two paths has already caused that class
// of false silence here (mg-f766).
const deployLogName = "pogo-deploy.log"

// deployPlistTemplate mirrors the in-repo scripts/launchd/com.pogo.deploy.plist
// with host-specific paths bound in.
//
// Two keys carry the load-bearing decisions:
//
// RunAtLoad=false — installing or reloading this job must never bounce the
// fleet as a side effect. com.pogo.recovery sets it true because draining a
// leftover queue at login is harmless; here the equivalent would be a full
// rebuild+restart every time an operator reran the installer.
//
// PATH — copied from launchdPath(), the same helper the daemon and recovery
// plists use, so all three agree by construction. It has to resolve BOTH `go`
// (/opt/homebrew/bin) and mg/pogo (~/go/bin): the 07-23 manual redeploy failed
// outright with "go: command not found" under a thinner PATH.
//
// There is deliberately NO GH_TOKEN key. ~/Library/LaunchAgents is
// world-readable, so a token here is a token every process on the box can read;
// pogo-deploy.sh sources it from ~/.zshenv at run time instead.
const deployPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.ScriptPath}}</string>
    </array>
    <key>StartCalendarInterval</key>
    <array>
{{- range .Hours}}
        <dict>
            <key>Hour</key>
            <integer>{{.}}</integer>
            <key>Minute</key>
            <integer>{{$.Minute}}</integer>
        </dict>
{{- end}}
    </array>
    <key>RunAtLoad</key>
    <false/>
    <key>KeepAlive</key>
    <false/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>StandardOutPath</key>
    <string>{{.LogDir}}/{{.LogName}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogDir}}/{{.LogName}}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{{.Path}}</string>
        <key>HOME</key>
        <string>{{.Home}}</string>
        <key>POGO_HOME</key>
        <string>{{.PogoHome}}</string>
        <key>POGO_DEPLOY_SRC</key>
        <string>{{.DeploySrc}}</string>
    </dict>
</dict>
</plist>
`

// deployHours / deployMinute — 03:00 local and two RETRY fires behind it, inside
// the off-hours window Daniel already moved disruptive fleet ops into. Constants
// rather than options: a deploy that can be scheduled at any hour is a deploy
// that will eventually be scheduled during the working day, and the runner's own
// window guard is written against these numbers.
//
// The 04:00 and 05:00 fires exist because of mg-8f7e: on 2026-07-31 the 03:00
// run exited 7 (the drain timed out with three polecats still working) and the
// next attempt was 24 hours away, so ~24h of merges stayed inert. They are
// RETRIES, not extra deploy opportunities — pogo-deploy.sh records the night's
// outcome and a later fire proceeds only when the earlier one stalled on the
// drain, which is the one failure a retry can fix. Every other outcome (success,
// build failure, control-suite RED) settles the night on the first attempt, so
// the extra fires cost one log line and no second bounce.
//
// They are also cheap when the first attempt is still running: pogo-deploy.sh
// takes a lock and a fire that finds it held exits 0.
var deployHours = []int{3, 4, 5}

const deployMinute = 0

// DeploySchedule exposes the fire schedule to readers outside this package.
//
// The staleness witness (internal/staleness, mg-dd49) needs it to answer "when
// should the nightly have run?", which is the only way to see a fire that never
// happened: a deploy that does not fire writes no log line, so the absence can
// only be read against an expectation. It returns a copy — a detector must not
// be able to edit the schedule it is checking against.
//
// It reports the SHIPPED schedule, not the installed plist's. Those can differ
// (mg-fc99: the installed plist has one fire where the code declares three) and
// comparing them is that ticket's job, not this accessor's. For the witness the
// direction of the difference is safe: a shipped last-hour later than the
// installed one only makes the witness wait longer before calling a night due.
func DeploySchedule() (hours []int, minute int) {
	return append([]int(nil), deployHours...), deployMinute
}

type deployData struct {
	Label      string
	ScriptPath string
	DeploySrc  string
	LogDir     string
	LogName    string
	Path       string
	Home       string
	PogoHome   string
	Hours      []int
	Minute     int
}

// DeployLogPath is the file the installed nightly writes every line to —
// including the `pogo-deploy: start` line that is the ONLY evidence a night's
// fire happened at all.
//
// Exported for the no-fire detector (internal/driftwatch, mg-2416), which reads
// this log rather than the job's exit code. A fire that never happens has no
// exit code and no stamp, so every alarm indexed to the run's outcome is blind
// to it by construction; the absence of a start line for a night that was due
// is the only positive evidence of that absence, and it lives here.
//
// It reports where the SHIPPED plist template points, which is the same
// derivation InstallDeploy binds in, so the detector and the installer cannot
// disagree about which file to read.
func DeployLogPath() string {
	return filepath.Join(logDir(), deployLogName)
}

// retryFireList renders the fires after the first one, for the installer's
// summary. "Schedule: 03:00 local, daily" on its own now understates the job —
// an operator who reads it and then sees three fires in the plist has to work
// out whether that is a bug.
func retryFireList(hours []int, minute int) string {
	if len(hours) < 2 {
		return "none"
	}
	parts := make([]string, 0, len(hours)-1)
	for _, h := range hours[1:] {
		parts = append(parts, fmt.Sprintf("%02d:%02d", h, minute))
	}
	return strings.Join(parts, ", ")
}

func deployPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", deployLabel+".plist")
}

// deployScriptInstallPath — where install-deploy copies the bundled runner.
// Out of the repo on purpose: the job must keep working while the checkout it
// builds from is mid-fetch, mid-checkout, or broken.
func deployScriptInstallPath() string {
	return filepath.Join(pogoHome(), "bin", "pogo-deploy.sh")
}

// deploySrcDir — the DEDICATED checkout the nightly builds from, never the
// developer's working tree. ~/dev/pogo is a place a human works; a 03:00
// fetch/checkout/merge there can land on a half-finished edit or an in-progress
// rebase. The nightly gets its own tree that nothing else writes to, so "dirty"
// there means something genuinely unexplained and is treated as an abort.
func deploySrcDir() string {
	if d := os.Getenv("POGO_DEPLOY_SRC"); d != "" {
		return d
	}
	return filepath.Join(pogoHome(), "deploy-src")
}

// findDeployScriptSource locates the bundled pogo-deploy.sh. Mirrors
// findRecoveryScriptSource: prefer the in-repo copy so `go run ./cmd/pogo`
// works from a checkout, then fall back to a sibling of the running binary.
func findDeployScriptSource() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "scripts", "launchd", "pogo-deploy.sh"),
			filepath.Join(dir, "scripts", "launchd", "pogo-deploy.sh"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "scripts", "launchd", "pogo-deploy.sh"))
	}
	if env := os.Getenv("POGO_DEPLOY_SCRIPT"); env != "" {
		candidates = append([]string{env}, candidates...)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			if abs, err := filepath.Abs(c); err == nil {
				return abs, nil
			}
			return c, nil
		}
	}
	return "", fmt.Errorf("pogo-deploy.sh not found in any of: %v (set POGO_DEPLOY_SCRIPT to override)", candidates)
}

// renderDeployPlist materializes the template against the current host.
func renderDeployPlist() (string, deployData, error) {
	home, _ := os.UserHomeDir()
	data := deployData{
		Label:      deployLabel,
		ScriptPath: deployScriptInstallPath(),
		DeploySrc:  deploySrcDir(),
		LogDir:     logDir(),
		LogName:    deployLogName,
		Path:       launchdPath(),
		Home:       home,
		PogoHome:   pogoHome(),
		Hours:      deployHours,
		Minute:     deployMinute,
	}
	tmpl, err := template.New("deploy-plist").Parse(deployPlistTemplate)
	if err != nil {
		return "", data, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", data, err
	}
	return buf.String(), data, nil
}

// InstallDeploy sets up the nightly deploy agent: copies pogo-deploy.sh into
// ~/.pogo/bin/, writes the plist, and bootstraps it. Idempotent.
//
// It does NOT create or populate the deploy checkout. The runner clones it on
// first use, which keeps a network operation out of an install that an operator
// may be running precisely because the box is unhealthy.
func InstallDeploy() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("deploy agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}

	src, err := findDeployScriptSource()
	if err != nil {
		return err
	}

	for _, d := range []string{filepath.Dir(deployScriptInstallPath()), logDir()} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}

	scriptBytes, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}
	dst := deployScriptInstallPath()
	if err := os.WriteFile(dst, scriptBytes, 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", dst, err)
	}

	rendered, data, err := renderDeployPlist()
	if err != nil {
		return err
	}
	plistPath := deployPlistPath()
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
	exec.Command("launchctl", "bootout", target, plistPath).Run() // best-effort
	out, err := exec.Command("launchctl", "bootstrap", target, plistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %s: %w", string(out), err)
	}

	fmt.Printf("Deploy agent installed: %s\n", plistPath)
	fmt.Printf("Script:   %s\n", dst)
	fmt.Printf("Checkout: %s (cloned on first run)\n", data.DeploySrc)
	fmt.Printf("Schedule: %02d:%02d local, daily (retry fires at %s — see mg-8f7e)\n",
		data.Hours[0], data.Minute, retryFireList(data.Hours, data.Minute))
	fmt.Printf("Logs:     %s/pogo-deploy.log\n", data.LogDir)
	return nil
}

// UninstallDeploy removes the deploy plist and stops the agent. The checkout
// under ~/.pogo/deploy-src is left in place: it is a git tree, it may hold the
// evidence of whatever made an operator uninstall the job, and re-cloning it is
// cheap next time.
func UninstallDeploy() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("deploy agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}
	plistPath := deployPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("deploy agent not installed at %s", plistPath)
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", target, plistPath).Run() // best-effort
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", plistPath, err)
	}
	fmt.Printf("Deploy agent removed: %s\n", plistPath)
	fmt.Printf("Checkout under %s left in place.\n", deploySrcDir())
	return nil
}

// DeployStatus reports whether the deploy plist is on disk.
func DeployStatus() (installed bool, path string) {
	if runtime.GOOS != "darwin" {
		return false, ""
	}
	p := deployPlistPath()
	_, err := os.Stat(p)
	return err == nil, p
}
