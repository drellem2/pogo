package service

// com.pogo.ghwatch (mg-257a8): the launchd job that runs `pogo gh-watch`, the
// gh-issue intake, teardown and carrier re-read watchers moved out of pogod.
//
// WHY A LOGIN SHELL. The whole reason these watchers left pogod is that
// launchd execs a job without a shell, so a job has no GitHub credential of its
// own — pogod had to fetch one at startup and hold it for its whole life. This
// job runs its command through `zsh -c -l`, which sources ~/.zshenv and
// ~/.zprofile (where this box keeps GH_TOKEN and its PATH), so each fire gets
// the credential the user's shell has at that moment. ProgramArguments is
// therefore a shell, not the pogo binary, and that is the job's defining
// property rather than an implementation detail.
//
// WHY StartCalendarInterval AND RunAtLoad. The watchers throttle themselves
// (intake every 15m, teardown and re-read hourly by default) from their
// persisted state, so the job only has to wake at least as often as the fastest
// of them, and a wall-clock fire at :00/:15/:30/:45 serves as well as one
// relative to load. RunAtLoad makes an install produce a record at once, which
// is what pogod's witness and the operator installing it both want to see; a
// fire at load is a read-only pass whose mail is still governed by the
// persisted renotify state.
//
// It was StartInterval (900s) until mg-d8160, and on the reference box that
// never fired: launchctl print showed `runs = 1` (the RunAtLoad) and
// `pended nondemand spawn = interval|speculative` for hours, while the
// StartCalendarInterval jobs beside it (com.pogo.mgbackup, com.pogo.deploy) kept
// running — the launchd wedge of mg-50e0. Every fire this job misses leaves
// gh-issue intake, teardown and carrier re-read dark, so it uses the trigger
// class that fires there. calendarEveryMinutes renders the fires.
//
// It is in the managed registry (launchagentaudit.go), so the nightly audit
// compares the installed plist against what this build renders.

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

const ghWatchLabel = "com.pogo.ghwatch"

const ghWatchLogName = "pogo-gh-watch.log"

// ghWatchIntervalMinutes is the job's wake cadence: the fastest watcher's
// default interval (ghintake.DefaultInterval, 15m). It is rendered as one
// StartCalendarInterval fire per multiple of it within the hour, not as a
// StartInterval (see the file header).
const ghWatchIntervalMinutes = 15

const ghWatchPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/zsh</string>
        <string>-c</string>
        <string>-l</string>
        <string>{{.Command}}</string>
    </array>
    <key>StartCalendarInterval</key>
    <array>
{{- range .Minutes}}
        <dict>
            <key>Minute</key>
            <integer>{{.}}</integer>
        </dict>
{{- end}}
    </array>
    <key>RunAtLoad</key>
    <true/>
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
    </dict>
</dict>
</plist>
`

type ghWatchData struct {
	Label    string
	Command  string
	LogDir   string
	LogName  string
	Path     string
	Home     string
	PogoHome string
	Minutes  []int // StartCalendarInterval Minute values, one fire each per hour
}

// GHWatchLogPath is the job's log.
func GHWatchLogPath() string { return filepath.Join(logDir(), ghWatchLogName) }

func ghWatchPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", ghWatchLabel+".plist")
}

// findPogo resolves the pogo CLI the job runs, by absolute path — the same
// resolution findPogod gives the daemon's plist, so the job does not depend on
// the login shell's PATH ordering to find the right binary.
func findPogo() (string, error) {
	path, err := exec.LookPath("pogo")
	if err != nil {
		return "", fmt.Errorf("pogo not found in PATH: %w", err)
	}
	return filepath.Abs(path)
}

// calendarEveryMinutes returns the Minute values of a StartCalendarInterval
// that fires every n minutes on the wall clock: 0, n, 2n, ... below 60. n must
// divide 60, or the gap across the hour would differ from the others; a caller
// passing anything else is a programming error, so it panics rather than
// render a schedule nobody chose.
func calendarEveryMinutes(n int) []int {
	if n <= 0 || n > 60 || 60%n != 0 {
		panic(fmt.Sprintf("calendarEveryMinutes(%d): n must divide 60", n))
	}
	out := make([]int, 0, 60/n)
	for m := 0; m < 60; m += n {
		out = append(out, m)
	}
	return out
}

// joinMinutes renders fire minutes as ":00/:15/:30/:45" for install summaries.
func joinMinutes(ms []int) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = fmt.Sprintf(":%02d", m)
	}
	return strings.Join(parts, "/")
}

// shellQuote single-quotes s for zsh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func renderGHWatchPlist() (string, ghWatchData, error) {
	pogoPath, err := findPogo()
	if err != nil {
		return "", ghWatchData{}, err
	}
	home, _ := os.UserHomeDir()
	data := ghWatchData{
		Label:    ghWatchLabel,
		Command:  "exec " + shellQuote(pogoPath) + " gh-watch --oneline",
		LogDir:   logDir(),
		LogName:  ghWatchLogName,
		Path:     launchdPath(),
		Home:     home,
		PogoHome: pogoHome(),
		Minutes:  calendarEveryMinutes(ghWatchIntervalMinutes),
	}
	tmpl, err := template.New("ghwatch-plist").Parse(ghWatchPlistTemplate)
	if err != nil {
		return "", data, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", data, err
	}
	return buf.String(), data, nil
}

// InstallGHWatch writes and (re)loads com.pogo.ghwatch. Idempotent: the plist
// is rewritten only when this build would render it differently.
func InstallGHWatch() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("gh-watch agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}
	rendered, data, err := renderGHWatchPlist()
	if err != nil {
		return err
	}
	for _, d := range []string{logDir(), filepath.Dir(ghWatchPlistPath())} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}
	plistPath := ghWatchPlistPath()
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

	fmt.Printf("gh-watch agent installed: %s\n", plistPath)
	fmt.Printf("Runs:     /bin/zsh -c -l %q at minutes %s of every hour, and once now (RunAtLoad)\n", data.Command, joinMinutes(data.Minutes))
	fmt.Printf("Logs:     %s (one line per fire)\n", GHWatchLogPath())
	fmt.Printf("Record:   %s/gh-watch/state.json (read by pogod)\n", data.PogoHome)
	fmt.Printf("\n")
	fmt.Printf("It runs through a LOGIN shell so it uses your shell's GitHub credential\n")
	fmt.Printf("(~/.zshenv), read afresh on every fire. Check one run by hand with\n")
	fmt.Printf("`pogo gh-watch --force`.\n")
	return nil
}

// UninstallGHWatch removes com.pogo.ghwatch. The record under
// $POGO_HOME/gh-watch is left in place; pogod will report the job as not
// reporting once the record goes stale, which is true.
func UninstallGHWatch() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("gh-watch agent is macOS-only (GOOS=%s)", runtime.GOOS)
	}
	plistPath := ghWatchPlistPath()
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("gh-watch agent not installed at %s", plistPath)
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", target, plistPath).Run() // best-effort
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("failed to remove %s: %w", plistPath, err)
	}
	fmt.Printf("gh-watch agent removed: %s\n", plistPath)
	fmt.Printf("The gh-issue detectors are no longer running. If they are enabled in config,\n")
	fmt.Printf("pogod will report the job as not reporting within the hour.\n")
	return nil
}
