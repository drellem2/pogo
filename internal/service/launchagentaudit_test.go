package service

// Tests for the activation audit (mg-fc99).
//
// THE REPRODUCTION IS THE POINT. Every assertion below that matters is anchored
// on the plist that was actually on Daniel's machine on 2026-08-05: a
// com.pogo.deploy with `Hour = 3` alone, written 2026-07-28, against shipped
// code whose `deployHours` said {3,4,5}. That plist is not hand-written here as
// a string — it is rendered through the SAME template with a one-element Hours
// list, which is precisely what the pre-mg-8f7e code produced. A fixture typed
// out by the person writing the detector can be typed to suit the detector; one
// rendered by the code that made the bug cannot.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// renderDeployPlistWithHours renders the shipped deploy template against a
// custom fire list. Hours{3} reproduces the pre-mg-8f7e plist byte for byte.
func renderDeployPlistWithHours(t *testing.T, hours []int) string {
	t.Helper()
	_, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	data.Hours = hours
	tmpl, err := template.New("deploy-plist").Parse(deployPlistTemplate)
	if err != nil {
		t.Fatalf("parsing deploy template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("executing deploy template: %v", err)
	}
	return buf.String()
}

func writePlist(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "com.pogo.deploy.plist")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("writing fixture plist: %v", err)
	}
	return p
}

// TestAuditCatchesTheInstalledPlistThatMissedTwoFires is mg-fc99 itself.
//
// The runner was written for 03/04/05 and launchd was given 03:00. The retry
// half of mg-8f7e was inert for five days and NOTHING on the machine disagreed,
// because a fire that does not happen leaves no trace: from inside the script an
// absent 04:00 retry is indistinguishable from a night that needed none. The
// audit must call this out from the plist alone.
func TestAuditCatchesTheInstalledPlistThatMissedTwoFires(t *testing.T) {
	shipped, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	installed := renderDeployPlistWithHours(t, []int{3}) // what was really on the box
	path := writePlist(t, installed)

	got := auditLaunchAgent(deployLabel, path, "pogo service install-deploy", shipped, nil)

	if got.Status != LaunchAgentStale {
		t.Fatalf("status = %q, want %q — an installed plist two fires short of the shipped code is not up to date", got.Status, LaunchAgentStale)
	}
	if !got.ScheduleDrift {
		t.Error("ScheduleDrift = false: the whole failure is that the FIRES differ, not that some cosmetic key moved")
	}
	if len(got.Installed.Calendar) != 1 {
		t.Errorf("decoded %d installed fire(s) (%s), want 1 — the fixture is the Jul 28 plist", len(got.Installed.Calendar), got.Installed)
	}
	if len(got.Expected.Calendar) != len(deployHours) {
		t.Errorf("decoded %d expected fire(s) (%s), want %d from deployHours=%v", len(got.Expected.Calendar), got.Expected, len(deployHours), deployHours)
	}
	// The detail has to be actionable without a second command: which fires are
	// there, which are not, and what re-renders them.
	for _, want := range []string{"04:00", "05:00", "INERT", "pogo service install-deploy"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail does not mention %q; detail = %q", want, got.Detail)
		}
	}
}

// TestAuditIsCleanOnAnInstalledPlistThatMatches is the negative control. A
// detector only ever observed firing is indistinguishable from one that reports
// drift unconditionally.
func TestAuditIsCleanOnAnInstalledPlistThatMatches(t *testing.T) {
	shipped, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	path := writePlist(t, shipped)

	got := auditLaunchAgent(deployLabel, path, "pogo service install-deploy", shipped, nil)
	if got.Status != LaunchAgentOK {
		t.Fatalf("status = %q, want %q; detail = %q", got.Status, LaunchAgentOK, got.Detail)
	}
	if got.ScheduleDrift {
		t.Error("ScheduleDrift = true on a byte-identical plist")
	}
	if !strings.Contains(got.Detail, "03:00") {
		t.Errorf("clean detail = %q, want it to state the schedule it verified — a bare \"ok\" cannot be told from a check that did nothing", got.Detail)
	}
}

// TestAuditAnswerWouldDifferOnEveryFireCountChange pins that the check is
// sensitive to the quantity it claims to measure, in BOTH directions. The
// mg-fc99 case is "installed has fewer fires than shipped"; the same audit has
// to catch an installed plist with MORE fires (an operator's hand-edit that a
// later re-render would silently revoke), which a "does the plist contain every
// expected hour" check would pass.
func TestAuditAnswerWouldDifferOnEveryFireCountChange(t *testing.T) {
	shipped, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	for _, hours := range [][]int{{3}, {3, 4}, {3, 4, 5, 6}, {2, 4, 5}} {
		path := writePlist(t, renderDeployPlistWithHours(t, hours))
		got := auditLaunchAgent(deployLabel, path, "remedy", shipped, nil)
		if !got.ScheduleDrift {
			t.Errorf("installed fires %v vs shipped %v reported no schedule drift; detail = %q", hours, deployHours, got.Detail)
		}
	}
}

// TestAuditIgnoresFireORDER: launchd holds StartCalendarInterval as a set and
// fires each entry independently, so a reordered list schedules identically.
// Reporting it as schedule drift would send an operator to re-render a plist
// that fires at exactly the right times — and a detector that cries about
// non-problems is one people learn to skip past.
func TestAuditIgnoresFireOrder(t *testing.T) {
	shipped, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	reversed := make([]int, len(deployHours))
	for i, h := range deployHours {
		reversed[len(deployHours)-1-i] = h
	}
	path := writePlist(t, renderDeployPlistWithHours(t, reversed))

	got := auditLaunchAgent(deployLabel, path, "remedy", shipped, nil)
	if got.Status != LaunchAgentStale {
		t.Errorf("status = %q, want %q — the bytes DO differ and re-running the installer would rewrite the file", got.Status, LaunchAgentStale)
	}
	if got.ScheduleDrift {
		t.Errorf("ScheduleDrift = true on a reordered but equivalent schedule; detail = %q", got.Detail)
	}
}

// TestAuditDistinguishesAbsentFromClean. "There is no plist" and "the plist
// matches" are the two answers this audit must never conflate: the second is a
// verdict about an artifact, the first is the absence of one. A job nobody
// installed is not drift, and this line must not read as a pass on a schedule
// it never saw.
func TestAuditDistinguishesAbsentFromClean(t *testing.T) {
	shipped, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "nothing-here.plist")

	got := auditLaunchAgent(deployLabel, missing, "pogo service install-deploy", shipped, nil)
	if got.Status != LaunchAgentAbsent {
		t.Fatalf("status = %q, want %q", got.Status, LaunchAgentAbsent)
	}
	if got.ScheduleDrift {
		t.Error("ScheduleDrift = true with no installed plist to compare against")
	}
	if !strings.Contains(got.Detail, "cannot tell") {
		t.Errorf("detail = %q, want it to state what this audit cannot see — it compares installed plists and has no opinion on a job never installed", got.Detail)
	}
}

// TestAuditReportsNotCheckedWhenItCannotRender. The render is how the audit
// learns what SHOULD be installed; without it there is no comparison, and a
// silent "ok" there would be the same defect one level up.
func TestAuditReportsNotCheckedWhenItCannotRender(t *testing.T) {
	got := auditLaunchAgent(deployLabel, "/does/not/matter", "remedy", "", errors.New("pogod not found in PATH"))
	if got.Status != LaunchAgentUnknown {
		t.Fatalf("status = %q, want %q", got.Status, LaunchAgentUnknown)
	}
	if !strings.Contains(got.Detail, "NOT CHECKED") {
		t.Errorf("detail = %q, want it to say NOT CHECKED out loud", got.Detail)
	}
}

// TestAuditSurvivesAnUndecodablePlist. A hand-edited or truncated plist must
// still be reported as drift — the byte comparison is what makes the audit's
// verdict independent of whether its own parser understood the file.
func TestAuditSurvivesAnUndecodablePlist(t *testing.T) {
	shipped, _, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}
	path := writePlist(t, "<?xml version=\"1.0\"?>\n<plist version=\"1.0\"><dict><key>Label</key>")

	got := auditLaunchAgent(deployLabel, path, "remedy", shipped, nil)
	if got.Status != LaunchAgentStale {
		t.Fatalf("status = %q, want %q", got.Status, LaunchAgentStale)
	}
	if got.Installed.Decoded {
		t.Error("Installed.Decoded = true on a truncated plist")
	}
	if !strings.Contains(got.Detail, "could not be decoded") {
		t.Errorf("detail = %q, want it to say the installed schedule is unknown rather than assert one", got.Detail)
	}
}

// TestParseLaunchScheduleReadsBothScheduleShapes. launchd accepts
// StartCalendarInterval as either a single <dict> or an <array> of them, and the
// single-dict form is what the plist on the box used. A parser that handled only
// the array form would decode zero fires there and report the drift for the
// wrong reason.
func TestParseLaunchScheduleReadsBothScheduleShapes(t *testing.T) {
	single := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
  <key>Label</key><string>com.example</string>
  <key>StartCalendarInterval</key>
  <dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>
  <key>RunAtLoad</key><false/>
</dict></plist>`
	got := parseLaunchSchedule([]byte(single))
	if !got.Decoded || len(got.Calendar) != 1 || got.Calendar[0].Hour != 3 || got.Calendar[0].Minute != 0 {
		t.Fatalf("single-dict schedule decoded as %+v", got)
	}
	if got.RunAtLoad {
		t.Error("RunAtLoad decoded true from <false/>")
	}

	array := strings.Replace(single,
		`<dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict>`,
		`<array><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict><dict><key>Hour</key><integer>4</integer><key>Minute</key><integer>0</integer></dict></array>`, 1)
	got = parseLaunchSchedule([]byte(array))
	if len(got.Calendar) != 2 {
		t.Fatalf("array schedule decoded %d fire(s): %+v", len(got.Calendar), got)
	}
	if got.String() != "03:00, 04:00" {
		t.Errorf("String() = %q, want %q", got.String(), "03:00, 04:00")
	}
}

// TestParseLaunchScheduleKeepsWildcardsDistinctFromZero. launchd treats an
// omitted key as "every value": a fire with no Hour runs 24 times a day, and one
// with Hour=0 runs at midnight. Decoding a missing key as 0 would make those two
// plists compare equal — the audit would report a job firing hourly as matching
// a job firing nightly.
func TestParseLaunchScheduleKeepsWildcardsDistinctFromZero(t *testing.T) {
	tmpl := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>StartCalendarInterval</key><dict>%s<key>Minute</key><integer>0</integer></dict></dict></plist>`
	hourly := parseLaunchSchedule([]byte(strings.Replace(tmpl, "%s", "", 1)))
	midnight := parseLaunchSchedule([]byte(strings.Replace(tmpl, "%s", "<key>Hour</key><integer>0</integer>", 1)))

	if hourly.Equal(midnight) {
		t.Fatalf("a wildcard hour (%s) compared equal to midnight (%s)", hourly, midnight)
	}
	if hourly.Calendar[0].Hour != -1 {
		t.Errorf("absent Hour decoded as %d, want -1", hourly.Calendar[0].Hour)
	}
}

// TestParseLaunchScheduleSeesAnIntervalSwap. Replacing a wall-clock schedule
// with StartInterval turns a nightly into a job that fires N seconds after every
// load — a "nightly" that runs at 14:30 on the day somebody reinstalls it. The
// two forms share no representation, so the audit has to carry both.
func TestParseLaunchScheduleSeesAnIntervalSwap(t *testing.T) {
	calendar := parseLaunchSchedule([]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>0</integer></dict></dict></plist>`))
	interval := parseLaunchSchedule([]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>StartInterval</key><integer>86400</integer></dict></plist>`))
	if calendar.Equal(interval) {
		t.Fatal("a StartCalendarInterval schedule compared equal to a StartInterval one")
	}
	if interval.Interval != 86400 {
		t.Errorf("StartInterval decoded as %d, want 86400", interval.Interval)
	}
	if !strings.Contains(interval.String(), "after load") {
		t.Errorf("String() = %q, want it to name the interval semantics", interval.String())
	}
}

// TestEveryRenderedPlistIsInTheAuditRegistry is the generalisable half of
// mg-fc99, enforced structurally.
//
// The defect was never "hour 3": it was a ticket shipping artifacts on SEPARATE
// activation paths where only one was witnessed. A check written for the deploy
// plist alone leaves the next such ticket exactly as invisible. So the audit
// ranges over a registry, and this test fails when a new launchd plist renderer
// appears in this package without a registry row — the moment a fourth job could
// otherwise start drifting unobserved.
func TestEveryRenderedPlistIsInTheAuditRegistry(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	re := regexp.MustCompile(`func (render[A-Za-z]*Plist)\(`)
	found := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			found[m[1]] = name
		}
	}
	if len(found) == 0 {
		t.Fatal("found no plist renderers by source scan — this guard has stopped guarding anything")
	}
	if len(found) != len(managedLaunchAgents()) {
		t.Errorf("this package has %d plist renderer(s) %v but the activation audit registers %d job(s); a launchd job outside managedLaunchAgents() is one whose install can silently never have run — add it there",
			len(found), found, len(managedLaunchAgents()))
	}
}

// TestManagedLaunchAgentsAreWellFormed: every row must name a label, resolve a
// path, and carry the command that reconciles it. A row with an empty remedy
// reports a problem and no way to act on it.
func TestManagedLaunchAgentsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range managedLaunchAgents() {
		if a.Label == "" || a.Path == nil || a.Render == nil {
			t.Fatalf("malformed registry row: %+v", a)
		}
		if seen[a.Label] {
			t.Errorf("label %q registered twice", a.Label)
		}
		seen[a.Label] = true
		if a.Remedy == "" {
			t.Errorf("%s has no remedy command — an operator reading the warning has nothing to run", a.Label)
		}
		if got := a.Path(); !strings.HasSuffix(got, a.Label+".plist") {
			t.Errorf("%s resolves to %q, which is not that label's plist", a.Label, got)
		}
	}
	for _, want := range []string{launchdLabel, recoveryLabel, deployLabel} {
		if !seen[want] {
			t.Errorf("%s is installed by this package but is not audited for activation", want)
		}
	}
}
