package service

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestRenderDeployPlistNeverRunsAtLoad pins the key that separates "install the
// nightly" from "bounce the fleet right now". com.pogo.recovery sets
// RunAtLoad=true because draining a leftover queue at login is harmless; the
// same value here would mean every install, every reinstall, and every login
// triggered a full rebuild + fleet-wide restart as a side effect of touching
// the plist. It must be false, and it must stay false.
func TestRenderDeployPlistNeverRunsAtLoad(t *testing.T) {
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	if !strings.Contains(rendered, "<key>RunAtLoad</key>\n    <false/>") {
		t.Error("RunAtLoad is not false: installing or reloading com.pogo.deploy would bounce the fleet as a side effect")
	}
	if strings.Contains(rendered, "<key>KeepAlive</key>\n    <true/>") {
		t.Error("KeepAlive is true: the runner exits on every no-drift night, so launchd would relaunch it in a loop")
	}
}

// TestRenderDeployPlistHasNoSecrets is the one assertion that cannot be
// recovered after the fact. ~/Library/LaunchAgents is world-readable (0644, in
// a directory every local process can traverse), so a token written here is a
// token disclosed to everything on the box — and it stays disclosed until
// somebody notices and rotates it. The runner sources GH_TOKEN from ~/.zshenv
// at run time precisely so this file never has to hold one.
func TestRenderDeployPlistHasNoSecrets(t *testing.T) {
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	for _, forbidden := range []string{"GH_TOKEN", "OPENAI_API_KEY", "AWS_SECRET", "GITHUB_CLIENT_SECRET"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("rendered plist mentions %s — secrets must never be written to a world-readable LaunchAgent", forbidden)
		}
	}
}

// TestRenderDeployPlistSchedulesOffHours pins 03:00 and the two retry fires
// behind it. A redeploy bounces every agent on the box; the hours are not a
// preference, they are the reason the job is tolerable at all. It also pins
// that the schedule is a StartCalendarInterval rather than a StartInterval — an
// interval job fires N seconds after load regardless of wall-clock time, which
// is how a "nightly" ends up running at 14:30 on the day somebody reinstalls it.
//
// Every hour has to sit inside pogo-deploy.sh's own window guard (2-6). A fire
// scheduled outside it is dropped by the runner on arrival, so a plist and a
// guard that disagree produce a job that appears scheduled and never deploys —
// the failure the guard's own tests exist to keep distinguishable.
func TestRenderDeployPlistSchedulesOffHours(t *testing.T) {
	rendered, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	want := []int{3, 4, 5}
	if len(data.Hours) != len(want) {
		t.Fatalf("schedule has %d fires (%v); want %v — 03:00 plus the mg-8f7e retries", len(data.Hours), data.Hours, want)
	}
	for i, h := range want {
		if data.Hours[i] != h {
			t.Errorf("fire %d = %02d:00; want %02d:00", i, data.Hours[i], h)
		}
	}
	if data.Minute != 0 {
		t.Errorf("minute = %d; want 0", data.Minute)
	}
	// The window guard is half-open [2,6): every fire must be >= 2 and < 6.
	for _, h := range data.Hours {
		if h < 2 || h >= 6 {
			t.Errorf("fire at %02d:00 falls outside pogo-deploy.sh's window guard [2,6) — launchd would fire it and the runner would drop it, so the job would look scheduled and deploy nothing", h)
		}
	}
	// Ascending order is load-bearing: next_fire_hour in the runner walks the
	// list and returns the first entry after the current hour, so an unsorted
	// list makes the RED alert promise a retry that already happened.
	for i := 1; i < len(data.Hours); i++ {
		if data.Hours[i] <= data.Hours[i-1] {
			t.Errorf("fire hours %v are not strictly ascending — the runner's next_fire_hour walk assumes they are", data.Hours)
		}
	}
	if !strings.Contains(rendered, "<key>StartCalendarInterval</key>") {
		t.Error("plist has no StartCalendarInterval — a wall-clock schedule is what makes this off-hours")
	}
	if strings.Contains(rendered, "<key>StartInterval</key>") {
		t.Error("plist uses StartInterval: that fires relative to load time, not the clock, so the 'nightly' would drift into the working day")
	}
	// The retries only exist if launchd is actually given more than one fire.
	// A <dict> here renders one schedule and silently drops the rest.
	if strings.Count(rendered, "<key>Hour</key>") != len(want) {
		t.Errorf("plist renders %d Hour keys; want %d — StartCalendarInterval must be an <array> of dicts for launchd to schedule every fire",
			strings.Count(rendered, "<key>Hour</key>"), len(want))
	}
}

// TestRenderDeployPlistIsWellFormedXML is the check that the template edit did
// not break the file for launchd. The plist is assembled by string template, so
// a mismatched or unclosed tag compiles, renders, installs, and is then rejected
// by launchctl at bootstrap — leaving a job that is "installed" and never fires.
// mg-8f7e turned a single <dict> schedule into an <array> of them by hand, which
// is exactly the edit that gets this wrong.
func TestRenderDeployPlistIsWellFormedXML(t *testing.T) {
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	dec := xml.NewDecoder(strings.NewReader(rendered))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("rendered plist is not well-formed XML: %v\n\n%s", err, rendered)
		}
	}
}

// TestRenderDeployPlistBindsDeploySrc pins the anti-clobber invariant. The
// runner defaults POGO_DEPLOY_SRC to $HOME/.pogo/deploy-src on its own, so an
// unbound plist looks harmless — right up until POGO_HOME points somewhere
// else, at which point the two disagree about which tree is being built. Bind
// it here and the plist and the script cannot drift apart.
//
// It must also never name the developer's working tree: ~/dev/pogo is a place a
// human edits, and a 03:00 fetch/checkout/merge there can land on a
// half-finished change.
func TestRenderDeployPlistBindsDeploySrc(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("POGO_HOME", custom)
	os.Unsetenv("POGO_DEPLOY_SRC")

	rendered, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	want := filepath.Join(custom, "deploy-src")
	if data.DeploySrc != want {
		t.Errorf("DeploySrc = %q; want %q", data.DeploySrc, want)
	}
	if !strings.Contains(rendered, "<key>POGO_DEPLOY_SRC</key>") {
		t.Error("rendered plist does not export POGO_DEPLOY_SRC; the script's own default could name a different tree")
	}
	if strings.Contains(rendered, "/dev/pogo") {
		t.Error("rendered plist names a dev working tree as the build source — the nightly must build from a dedicated checkout it alone writes to")
	}
}

// TestRenderDeployPlistPathResolvesGoAndTools is the 07-23 regression. That
// manual redeploy failed outright with "go: command not found" because the
// job's PATH lacked Homebrew, and separately gh calls fail unauthenticated
// without the tools on ~/go/bin. launchd hands a job a minimal PATH; every
// binary this deploy needs has to be named here or it is not there.
func TestRenderDeployPlistPathResolvesGoAndTools(t *testing.T) {
	_, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	for _, dir := range []string{"/opt/homebrew/bin", "go/bin"} {
		if !strings.Contains(data.Path, dir) {
			t.Errorf("PATH %q omits %s", data.Path, dir)
		}
	}
	// Same helper the daemon and recovery plists use, so all three agree by
	// construction rather than by three hand-maintained copies.
	if data.Path != launchdPath() {
		t.Errorf("PATH diverges from launchdPath(): %q vs %q", data.Path, launchdPath())
	}
}

// TestDeployIsNotRecovery pins the separation mg-cf48 declined to collapse.
// Two labels, two plists, two logs: if the deploy ever shared com.pogo.recovery's
// label it would replace the tier-3 safety net on install, and the mechanism
// you reach for when the box is already broken would start rebuilding from main.
func TestDeployIsNotRecovery(t *testing.T) {
	if deployLabel == recoveryLabel {
		t.Fatalf("deploy and recovery share the label %q", deployLabel)
	}
	if deployPlistPath() == recoveryPlistPath() {
		t.Errorf("deploy and recovery share a plist path: %s", deployPlistPath())
	}
	if deployScriptInstallPath() == recoveryScriptInstallPath() {
		t.Errorf("deploy and recovery share a script path: %s", deployScriptInstallPath())
	}
	rendered, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	// The recovery job is edge-triggered on a queue directory; the deploy job
	// is not, and must not be. A WatchPaths deploy would rebuild pogod every
	// time a polecat asked for a restart.
	if strings.Contains(rendered, "<key>WatchPaths</key>") {
		t.Error("deploy plist has WatchPaths: a file event must not be able to trigger a rebuild")
	}
}

// TestFindDeployScriptSourceHonorsOverride keeps the installer testable and
// gives an operator a way out when the bundled copy cannot be located (a `go
// install`ed pogo has no scripts/ sibling).
func TestFindDeployScriptSourceHonorsOverride(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pogo-deploy.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"), 0755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("POGO_DEPLOY_SCRIPT", script)

	got, err := findDeployScriptSource()
	if err != nil {
		t.Fatalf("findDeployScriptSource: %v", err)
	}
	if got != script {
		t.Errorf("findDeployScriptSource() = %q; want the override %q", got, script)
	}
}

// TestDeployFireHoursAgreeAcrossEveryArtifactThatCarriesThem is the check
// mg-b201 was filed for the absence of.
//
// THREE artifacts in this repo independently state when the nightly fires, and
// until this test none of them was compared to the others:
//
//	deployHours                          (here — what the installer writes)
//	scripts/launchd/com.pogo.deploy.plist (the reference copy for a hand-install)
//	FIRE_HOURS in scripts/launchd/pogo-deploy.sh (what the RUNNER believes)
//
// The third one is not documentation. `retry_will_follow` consults FIRE_HOURS to
// decide whether a failed attempt gets a RED alert, and a runner that believes
// in a fire the schedule does not have takes the WORST branch available: a
// stalled drain exits 7, logs "the 04:00 fire will retry. Not alerting yet.",
// suppresses the alert — and then nothing fires at 04:00. The night fails in
// silence. That is strictly worse than the pre-retry behaviour, which at least
// mailed somebody. The divergence buys a silent nightly, which is the exact
// failure this whole family of tickets exists to end.
//
// It is deliberately NOT a check against the installed plist on this box. That
// comparison is auditLaunchAgent's job and it cannot live in a unit test: the
// installed copy is machine state, and a test that read it would pass or fail
// for reasons that have nothing to do with the commit under test. This test
// pins what the repo SAYS; keeping the box in line with the repo is an install,
// and nothing performs that install on its own (see AuditLaunchAgents).
func TestDeployFireHoursAgreeAcrossEveryArtifactThatCarriesThem(t *testing.T) {
	shippedPlist := readRepoFile(t, "../../scripts/launchd/com.pogo.deploy.plist")
	runner := readRepoFile(t, "../../scripts/launchd/pogo-deploy.sh")

	// Comments are stripped first: this plist explains its own schedule at
	// length, and a check that cannot tell an explanation from a declaration
	// has to be deleted the first time somebody documents the thing it guards.
	plistHours := parsePlistFireHours(stripXMLComments(shippedPlist))
	if !sameInts(plistHours, deployHours) {
		t.Errorf("scripts/launchd/com.pogo.deploy.plist fires at %v; deployHours says %v.\n"+
			"An operator who hand-installs the in-repo copy gets a different schedule from one who runs `pogo service install-deploy`.", plistHours, deployHours)
	}

	runnerHours, ok := parseRunnerFireHours(runner)
	if !ok {
		t.Fatalf("could not find the FIRE_HOURS default in scripts/launchd/pogo-deploy.sh — if it was renamed, this check stopped guarding anything and must be updated, not dropped")
	}
	if !sameInts(runnerHours, deployHours) {
		t.Errorf("pogo-deploy.sh believes the fires are %v; the schedule this build installs is %v.\n"+
			"retry_will_follow reads FIRE_HOURS to decide whether to SUPPRESS the RED alert, so a runner that believes in a fire that does not exist turns a failed night into a silent one.", runnerHours, deployHours)
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// stripXMLComments removes <!-- ... --> spans. Unterminated comments are dropped
// to end-of-file rather than ignored: leaving the tail in would feed commented
// prose to the Hour scan, which is the failure mode this exists to avoid.
func stripXMLComments(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i+4:]
		j := strings.Index(rest, "-->")
		if j < 0 {
			return b.String()
		}
		s = rest[j+3:]
	}
}

var plistHourRE = regexp.MustCompile(`(?s)<key>Hour</key>\s*<integer>\s*(\d+)\s*</integer>`)

func parsePlistFireHours(s string) []int {
	var out []int
	for _, m := range plistHourRE.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

var runnerFireHoursRE = regexp.MustCompile(`(?m)^FIRE_HOURS="\$\{POGO_DEPLOY_FIRE_HOURS:-([0-9 ]+)\}"`)

func parseRunnerFireHours(s string) ([]int, bool) {
	m := runnerFireHoursRE.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	var out []int
	for _, f := range strings.Fields(m[1]) {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDeployFireHourParsersSeeDivergence proves the check above can fail. A
// consistency test whose parsers silently return nothing passes forever on
// artifacts that have drifted apart — which is the same defect it was written
// to catch, one level up.
func TestDeployFireHourParsersSeeDivergence(t *testing.T) {
	const plist = `<!-- 04:00 and 05:00 are RETRIES.
	     <key>Hour</key><integer>9</integer> in a comment is prose, not a fire. -->
	<key>StartCalendarInterval</key>
	<array>
	    <dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>
	    <dict><key>Hour</key><integer>4</integer><key>Minute</key><integer>0</integer></dict>
	</array>`

	got := parsePlistFireHours(stripXMLComments(plist))
	if !sameInts(got, []int{3, 4}) {
		t.Errorf("parsePlistFireHours = %v; want [3 4] — the commented Hour must not count as a fire", got)
	}
	if sameInts(got, []int{3, 4, 5}) {
		t.Error("a two-fire plist compared equal to a three-fire schedule")
	}

	runnerHours, ok := parseRunnerFireHours("set -u\nFIRE_HOURS=\"${POGO_DEPLOY_FIRE_HOURS:-3 4 5}\"\n")
	if !ok || !sameInts(runnerHours, []int{3, 4, 5}) {
		t.Fatalf("parseRunnerFireHours = %v, ok=%v; want [3 4 5], true", runnerHours, ok)
	}
	if sameInts(runnerHours, got) {
		t.Error("a runner believing in three fires compared equal to a two-fire plist — this is exactly the drift that suppresses the RED alert")
	}

	// A renamed or restructured assignment must be loud, not absent.
	if _, ok := parseRunnerFireHours("FIRE_HOURS=\"3 4 5\"\n"); ok {
		t.Error("parseRunnerFireHours matched a form the runner does not use; the check would drift out of contact with the file it guards")
	}
}
