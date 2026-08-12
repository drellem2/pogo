package service

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderReclaimPlistNeverRunsAtLoad pins the key that separates "install a
// housekeeping job" from "delete the module cache right now". An operator
// reruns install-reclaim precisely when the box is unhealthy; if RunAtLoad were
// true, the install itself would be a reclaim, and every login would be another
// one. KeepAlive must stay false for the ordinary reason: the script exits on
// every sample, so launchd would relaunch it in a tight loop.
func TestRenderReclaimPlistNeverRunsAtLoad(t *testing.T) {
	rendered, _, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	if !strings.Contains(rendered, "<key>RunAtLoad</key>\n    <false/>") {
		t.Error("RunAtLoad is not false: installing com.pogo.reclaim would run `go clean -modcache` as a side effect of the install")
	}
	if strings.Contains(rendered, "<key>KeepAlive</key>\n    <true/>") {
		t.Error("KeepAlive is true: the script exits on every sample, so launchd would respawn it in a loop")
	}
}

// TestRenderReclaimPlistHasNoSecrets — ~/Library/LaunchAgents is world-readable
// and a token written there stays disclosed until somebody notices. Nothing in
// this job needs one; this is the assertion that keeps it that way.
func TestRenderReclaimPlistHasNoSecrets(t *testing.T) {
	rendered, _, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	for _, forbidden := range []string{"GH_TOKEN", "OPENAI_API_KEY", "AWS_SECRET", "GITHUB_CLIENT_SECRET"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("rendered plist mentions %s — secrets must never be written to a world-readable LaunchAgent", forbidden)
		}
	}
}

// TestRenderReclaimPlistSamplesOnAnInterval pins the ticket's central decision
// in launchd's vocabulary.
//
// The trigger is SIZE, and launchd has no size trigger — so the schedule is a
// SAMPLER and the script is the trigger. That makes StartInterval right here for
// exactly the reason it is wrong for com.pogo.deploy (which pins
// StartCalendarInterval, because a nightly that fires N seconds after load is
// not a nightly).
//
// The interval is pinned as a number because the number is an argument: the
// volume went from healthy to 571 MiB free inside a working day, and a sampler
// slower than that misses the window in which every merge on the box is failing.
func TestRenderReclaimPlistSamplesOnAnInterval(t *testing.T) {
	rendered, data, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	if data.IntervalSeconds != 1800 {
		t.Errorf("interval = %ds; want 1800 (30 min)", data.IntervalSeconds)
	}
	if ReclaimInterval() != data.IntervalSeconds {
		t.Errorf("ReclaimInterval() = %d but the plist renders %d — a caller stating the cadence would state a different one than launchd runs", ReclaimInterval(), data.IntervalSeconds)
	}
	if !strings.Contains(rendered, "<key>StartInterval</key>") {
		t.Error("no StartInterval: the sampler has no schedule")
	}
	if strings.Contains(rendered, "StartCalendarInterval") {
		t.Error("StartCalendarInterval is present: a wall-clock nightly can miss the entire window in which the disk fills and every merge fails")
	}

	// The audit decodes the same plist. If it cannot see the interval, drift in
	// the sampling rate is invisible to `pogo doctor` — the mg-fc99 shape, where
	// a job is installed, loaded, listed by launchctl, and doing a fraction of
	// what the code believes.
	sched := parseLaunchSchedule([]byte(rendered))
	if !sched.Decoded {
		t.Fatal("the audit could not decode the plist this build renders")
	}
	if sched.Interval != data.IntervalSeconds {
		t.Errorf("audit decoded interval %d; plist renders %d", sched.Interval, data.IntervalSeconds)
	}
	if sched.RunAtLoad {
		t.Error("audit decoded RunAtLoad=true")
	}
}

// TestReclaimPlistIsValidXML — a plist launchd cannot parse is a job that never
// runs, and `launchctl bootstrap` reports that as a load error long after the
// install printed a success line.
func TestReclaimPlistIsValidXML(t *testing.T) {
	rendered, _, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	dec := xml.NewDecoder(strings.NewReader(rendered))
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("rendered plist is not valid XML: %v", err)
		}
	}
}

// TestReclaimLogPathMatchesThePlist. The log is the only record that a sample
// happened at all, and the only place the "what this does not fix" statement is
// written down. A reader pointed at a different path than the plist writes gets
// an empty file — and an empty log is indistinguishable from a job that never
// fired, which is the reading this whole ticket exists to make impossible.
func TestReclaimLogPathMatchesThePlist(t *testing.T) {
	rendered, _, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	want := ReclaimLogPath()
	if !strings.Contains(rendered, "<string>"+want+"</string>") {
		t.Errorf("ReclaimLogPath() = %s, which the rendered plist does not write to:\n%s", want, rendered)
	}
	if strings.Count(rendered, "<string>"+want+"</string>") < 3 {
		t.Errorf("expected the log path in StandardOutPath, StandardErrorPath and POGO_RECLAIM_LOG; got %d occurrences", strings.Count(rendered, "<string>"+want+"</string>"))
	}
}

// TestReclaimPlistPathsResolveGo is the failure the 2026-07-23 manual redeploy
// hit under a thinner PATH: "go: command not found". Without `go` this script
// cannot locate the module cache and could not reclaim it if it did — it reports
// UNKNOWN and does nothing, forever, silently enough that the job looks healthy.
func TestReclaimPlistPathsResolveGo(t *testing.T) {
	rendered, data, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	if !strings.Contains(rendered, data.Path) {
		t.Fatal("the rendered plist does not carry a PATH")
	}
	needed := map[string]string{
		"/opt/homebrew/bin": "`go` — without it the cache cannot be located or reclaimed",
		"go/bin":            "`pogo` and `mg` — without them the in-flight check cannot be made and a cannot-help verdict reaches nobody",
	}
	for dir, why := range needed {
		if !strings.Contains(data.Path, dir) {
			t.Errorf("PATH lacks %s, needed for %s: %s", dir, why, data.Path)
		}
	}
}

// TestReclaimIsRegisteredForAudit is the mg-fc99 lesson applied to a job that
// has its exact shape.
//
// com.pogo.reclaim ships TWO artifacts on TWO install paths — the plist and the
// runner script — which is precisely how mg-8f7e left the deploy job's retry
// fires inert for five days while the ticket read as closed. Registration in
// managedLaunchAgents() is what makes `pogo doctor` compare the installed plist
// against what this build would render, so a plist that never landed (or landed
// once and went stale) is a reported fact rather than a silence.
func TestReclaimIsRegisteredForAudit(t *testing.T) {
	var found *managedLaunchAgent
	for i, a := range managedLaunchAgents() {
		if a.Label == reclaimLabel {
			found = &managedLaunchAgents()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("%s is not in managedLaunchAgents(): its installed plist is audited by nothing", reclaimLabel)
	}
	if found.Remedy != "pogo service install-reclaim" {
		t.Errorf("remedy = %q; want the command that actually re-renders and reloads this job", found.Remedy)
	}
	rendered, err := found.Render()
	if err != nil {
		t.Fatalf("registered renderer failed: %v", err)
	}
	direct, _, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	if rendered != direct {
		t.Error("the registered renderer and renderReclaimPlist disagree — the audit would compare against bytes the installer never writes")
	}
	if found.Path() != reclaimPlistPath() {
		t.Errorf("registered path %s != %s", found.Path(), reclaimPlistPath())
	}
}

// TestAuditReclaimDetectsIntervalDrift proves the registration is worth having
// rather than merely present. A plist whose StartInterval was hand-edited to
// once a day is installed, loaded, listed by launchctl, and samples 1/48th as
// often as the code believes — the state that has to be visible.
func TestAuditReclaimDetectsIntervalDrift(t *testing.T) {
	rendered, _, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	drifted := strings.Replace(rendered,
		"<key>StartInterval</key>\n    <integer>1800</integer>",
		"<key>StartInterval</key>\n    <integer>86400</integer>", 1)
	if drifted == rendered {
		t.Fatal("could not construct a drifted plist — the StartInterval spelling changed")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, reclaimLabel+".plist")
	if err := os.WriteFile(path, []byte(drifted), 0644); err != nil {
		t.Fatal(err)
	}

	res := auditLaunchAgent(reclaimLabel, path, "pogo service install-reclaim", rendered, nil)
	if res.Status != LaunchAgentStale {
		t.Errorf("status = %q; want %q for a plist sampling once a day where the code says every 30 min", res.Status, LaunchAgentStale)
	}
	if !res.ScheduleDrift {
		t.Error("ScheduleDrift is false: an interval that samples 1/48th as often was reported as a mere byte difference")
	}
	if !strings.Contains(res.Detail, "install-reclaim") {
		t.Errorf("detail does not name the remedy: %s", res.Detail)
	}
}

// TestFindReclaimScriptSourceHonoursOverride covers the lookup that decides
// WHICH bytes get copied to ~/.pogo/bin/. Three runners are now installed by
// three near-identical find-then-copy paths, and a divergence in how one of them
// locates its source is how that one silently stops being shipped.
func TestFindReclaimScriptSourceHonoursOverride(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "pogo-reclaim.sh")
	if err := os.WriteFile(custom, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POGO_RECLAIM_SCRIPT", custom)
	got, err := findReclaimScriptSource()
	if err != nil {
		t.Fatalf("findReclaimScriptSource: %v", err)
	}
	if got != custom {
		t.Errorf("found %s; want the override %s", got, custom)
	}

	t.Setenv("POGO_RECLAIM_SCRIPT", filepath.Join(dir, "does-not-exist.sh"))
	if _, err := findReclaimScriptSource(); err != nil {
		// Falling through to the repo copy is correct — an override pointing at
		// nothing must not be fatal when a real source exists. Only assert the
		// error names the override when NOTHING was found.
		if !strings.Contains(err.Error(), "POGO_RECLAIM_SCRIPT") {
			t.Errorf("error does not mention the override knob: %v", err)
		}
	}
}

// TestReclaimStateIsUnderPogoHome. The runner keeps its lock and its
// alert-cooldown stamp under $POGO_HOME/reclaim, and the plist exports
// POGO_HOME for exactly that reason: this box exports POGO_HOME=$HOME from a
// stale profile, so a job deriving the path from HOME alone would write its
// state somewhere other than where the installer created it — and a lock in the
// wrong place is no lock.
func TestReclaimStateIsUnderPogoHome(t *testing.T) {
	rendered, data, err := renderReclaimPlist()
	if err != nil {
		t.Fatalf("renderReclaimPlist: %v", err)
	}
	if !strings.Contains(rendered, "<key>POGO_HOME</key>") {
		t.Error("the plist does not export POGO_HOME: the runner's lock and cooldown stamp would land under a HOME-derived path the installer never created")
	}
	if !strings.Contains(rendered, "<string>"+data.PogoHome+"</string>") {
		t.Errorf("plist does not bind POGO_HOME=%s", data.PogoHome)
	}
	if want := filepath.Join(data.PogoHome, "reclaim"); reclaimStateDir() != want {
		t.Errorf("reclaimStateDir() = %s; want %s", reclaimStateDir(), want)
	}
	if want := filepath.Join(data.PogoHome, "bin", "pogo-reclaim.sh"); reclaimScriptInstallPath() != want {
		t.Errorf("reclaimScriptInstallPath() = %s; want %s", reclaimScriptInstallPath(), want)
	}
}
