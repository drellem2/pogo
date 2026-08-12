package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"
)

// The disk-reclaim agent (mg-b7c3) is the FOURTH launchd job this package
// installs. It samples free space on the volume holding the Go module cache and
// runs `go clean -modcache` when — and only when — both a free-space floor and a
// cache-size floor are crossed.
//
// WHY IT IS ITS OWN JOB. The same argument that keeps deploy separate from
// recovery applies again: each job has to be installable on a box where the
// others are broken, and this one exists precisely for a box that is already
// unhealthy. Folding a cache reclaim into the nightly deploy would tie the one
// action that frees disk to the one job that needs a working checkout, a working
// network and a successful build — on a full volume, none of which are available.
//
// WHY THE SCHEDULE IS AN INTERVAL AND THE TRIGGER IS A SIZE. launchd has no size
// trigger; StartInterval is the sampler and pogo-reclaim.sh is the trigger. The
// script's header carries the reasoning for the two floors and why they are
// ANDed. What matters here is that this file installs BOTH artifacts — the plist
// and the runner — and that com.pogo.reclaim is registered in
// managedLaunchAgents() so `pogo doctor` audits the installed plist against what
// this build would render. mg-fc99 is the ticket for what happens when a job
// ships two artifacts on two install paths and only one of them lands.

const reclaimLabel = "com.pogo.reclaim"

// reclaimLogName — the file every fire's stdout and stderr land in. Named once
// and used by both the plist template and ReclaimLogPath for the same reason
// deployLogName is: a reader pointed at a different path than the plist writes
// gets an empty file, and an empty log is indistinguishable from a job that
// never fired.
const reclaimLogName = "pogo-reclaim.log"

// reclaimIntervalSeconds — 30 minutes.
//
// Chosen against the observed fill rate, not against a sense of tidiness: this
// volume went from healthy to 571 MiB free inside a working day, and every
// sample missed in that window is a window in which the refinery's merge gate
// fails as a link error that reads like a broken branch. A nightly fire could
// miss it entirely.
//
// Sampling this often is affordable only because the script orders its
// measurements cheap-first: one `df` per fire, and the `du` of a multi-gigabyte
// tree only after `df` has already established the disk is low. Reversing that
// order would make this interval a real cost.
const reclaimIntervalSeconds = 1800

// reclaimPlistTemplate mirrors the in-repo scripts/launchd/com.pogo.reclaim.plist
// with host-specific paths bound in.
//
// RunAtLoad=false is load-bearing: an operator rerunning the installer must not
// thereby delete a multi-gigabyte cache as a side effect of an install.
//
// POGO_HOME is exported because the script keeps its lock and its alert-cooldown
// stamp under $POGO_HOME/reclaim. This box exports POGO_HOME=$HOME from a stale
// profile, so a script that derived it from HOME alone would write its state
// somewhere other than where the installer created it.
const reclaimPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.ScriptPath}}</string>
    </array>
    <key>StartInterval</key>
    <integer>{{.IntervalSeconds}}</integer>
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
        <key>POGO_RECLAIM_LOG</key>
        <string>{{.LogDir}}/{{.LogName}}</string>
        <key>POGO_RECLAIM_INTERVAL_SEC</key>
        <string>{{.IntervalSeconds}}</string>
    </dict>
</dict>
</plist>
`

type reclaimData struct {
	Label           string
	ScriptPath      string
	LogDir          string
	LogName         string
	Path            string
	Home            string
	PogoHome        string
	IntervalSeconds int
}

// ReclaimLogPath is the file the installed job writes every line to, including
// the `runner:` line that is the only record of which COPY of the script ran.
func ReclaimLogPath() string {
	return filepath.Join(logDir(), reclaimLogName)
}

// ReclaimInterval reports the sampling interval the shipped code installs, in
// seconds. Exported so a caller can state the cadence without re-deriving it
// from the plist.
func ReclaimInterval() int { return reclaimIntervalSeconds }

func reclaimPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", reclaimLabel+".plist")
}

// reclaimScriptInstallPath — where install-reclaim copies the bundled runner.
// Out of the repo for the same reason the deploy runner is: the job must keep
// working while the checkout it was copied from is mid-fetch or broken. The
// price is the standing static-copy trap — a merge does not refresh this file —
// which the script's own `runner:` log line and the installer's summary both
// name out loud.
func reclaimScriptInstallPath() string {
	return filepath.Join(pogoHome(), "bin", "pogo-reclaim.sh")
}

// reclaimStateDir holds the runner's lock and its alert-cooldown stamp.
func reclaimStateDir() string {
	return filepath.Join(pogoHome(), "reclaim")
}

// findReclaimScriptSource locates the bundled pogo-reclaim.sh. Mirrors
// findDeployScriptSource and findRecoveryScriptSource deliberately: three
// runners installed by three near-identical paths is a shape where a divergence
// in HOW a file is found is how one of them silently stops being shipped.
func findReclaimScriptSource() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "scripts", "launchd", "pogo-reclaim.sh"),
			filepath.Join(dir, "scripts", "launchd", "pogo-reclaim.sh"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "scripts", "launchd", "pogo-reclaim.sh"))
	}
	if env := os.Getenv("POGO_RECLAIM_SCRIPT"); env != "" {
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
	return "", fmt.Errorf("pogo-reclaim.sh not found in any of: %v (set POGO_RECLAIM_SCRIPT to override)", candidates)
}

// renderReclaimPlist materializes the template against the current host.
func renderReclaimPlist() (string, reclaimData, error) {
	home, _ := os.UserHomeDir()
	data := reclaimData{
		Label:           reclaimLabel,
		ScriptPath:      reclaimScriptInstallPath(),
		LogDir:          logDir(),
		LogName:         reclaimLogName,
		Path:            launchdPath(),
		Home:            home,
		PogoHome:        pogoHome(),
		IntervalSeconds: reclaimIntervalSeconds,
	}
	tmpl, err := template.New("reclaim-plist").Parse(reclaimPlistTemplate)
	if err != nil {
		return "", data, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", data, err
	}
	return buf.String(), data, nil
}

// InstallReclaim sets up the disk-reclaim agent: copies pogo-reclaim.sh into
// ~/.pogo/bin/, creates the state dir, writes the plist, and bootstraps it.
// Idempotent — rerunning replaces the job in place.
//
// It does NOT perform a reclaim. RunAtLoad is false and nothing here shells out
// to `go clean`: installing a housekeeping job must not be a way to lose a
// multi-gigabyte cache.
func InstallReclaim() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("reclaim agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}

	src, err := findReclaimScriptSource()
	if err != nil {
		return err
	}

	for _, d := range []string{filepath.Dir(reclaimScriptInstallPath()), reclaimStateDir(), logDir()} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}

	scriptBytes, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}
	dst := reclaimScriptInstallPath()
	if err := os.WriteFile(dst, scriptBytes, 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", dst, err)
	}

	rendered, data, err := renderReclaimPlist()
	if err != nil {
		return err
	}
	plistPath := reclaimPlistPath()
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

	fmt.Printf("Reclaim agent installed: %s\n", plistPath)
	fmt.Printf("Script:   %s\n", dst)
	fmt.Printf("Sampling: every %d min; the reclaim itself fires only when free space AND cache size both cross their floors\n", data.IntervalSeconds/60)
	fmt.Printf("Logs:     %s\n", ReclaimLogPath())
	fmt.Printf("\n")
	fmt.Printf("This job reclaims the Go module cache and nothing else. On the box that\n")
	fmt.Printf("prompted it, that was 7.3G of a 422G fill — headroom, not a fix. When the\n")
	fmt.Printf("volume is low and the cache is not why, it refuses to fire and says so.\n")
	fmt.Printf("\n")
	fmt.Printf("%s is a STATIC COPY: a merge to main does not refresh it.\n", dst)
	fmt.Printf("Re-run `pogo service install-reclaim` after any change to the runner.\n")
	return nil
}

// UninstallReclaim removes the reclaim plist and stops the agent. State under
// ~/.pogo/reclaim (the alert-cooldown stamp) is left in place — it is the only
// record of when someone was last told the disk was low.
func UninstallReclaim() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("reclaim agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}
	plistPath := reclaimPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("reclaim agent not installed at %s", plistPath)
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", target, plistPath).Run() // best-effort
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", plistPath, err)
	}
	fmt.Printf("Reclaim agent removed: %s\n", plistPath)
	fmt.Printf("State under %s left in place.\n", reclaimStateDir())
	return nil
}

// ReclaimStatus reports whether the reclaim plist is on disk.
func ReclaimStatus() (installed bool, path string) {
	if runtime.GOOS != "darwin" {
		return false, ""
	}
	p := reclaimPlistPath()
	_, err := os.Stat(p)
	return err == nil, p
}
