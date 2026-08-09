package sourcewatch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// plistTemplate is the shape of the real jobs: a Label, a ProgramArguments[0],
// and an EnvironmentVariables dict. Built by string assembly rather than
// hand-copied XML so a test can vary one key at a time.
func writePlist(t *testing.T, dir, label, program string, env map[string]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + label + `</string>
    <key>ProgramArguments</key>
    <array>
        <string>` + program + `</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
`)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("        <key>" + k + "</key>\n        <string>" + env[k] + "</string>\n")
	}
	b.WriteString(`    </dict>
</dict>
</plist>
`)
	p := filepath.Join(dir, label+".plist")
	if err := os.WriteFile(p, []byte(b.String()), 0644); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
}

// realWorldFixture reproduces the 2026-08-09 arrangement of this machine: two
// jobs running one program and disagreeing only about MAIL_DIR, beside jobs
// whose environment holds directories that are NOT data sources. It returns the
// LaunchAgents dir and the mail store root.
func realWorldFixture(t *testing.T) (agentsDir, mailRoot string) {
	t.Helper()
	root := t.TempDir()
	agentsDir = filepath.Join(root, "LaunchAgents")
	mailRoot = filepath.Join(root, "macguffin", "mail")
	mkdirs(t,
		agentsDir,
		filepath.Join(mailRoot, "daniel", "new"),
		filepath.Join(mailRoot, "human", "new"),
		filepath.Join(root, "pogo", "reminders-deadman"),
		filepath.Join(root, "pogo", "deploy-src"),
		filepath.Join(root, "pogo"),
	)

	poll := "/bin/poll-mail.sh"
	writePlist(t, agentsDir, "com.pogo.notify", poll, map[string]string{
		"PATH":          "/a/bin:/b/bin",
		"POLL_INTERVAL": "30",
		"MAIL_DIR":      filepath.Join(mailRoot, "daniel", "new"),
	})
	writePlist(t, agentsDir, "com.pogo.deadman", poll, map[string]string{
		"PATH":              "/a/bin:/b/bin",
		"POLL_INTERVAL":     "60",
		"MAIL_DIR":          filepath.Join(mailRoot, "human", "new"),
		"STATE_DIR":         filepath.Join(root, "pogo", "reminders-deadman"),
		"MIN_AGE_SECONDS":   "900",
		"TITLE_PREFIX":      "[UNPROCESSED] ",
		"HEARTBEAT_JOB":     "com.pogo.deadman",
		"NOT_A_PATH_AT_ALL": "imbox",
	})
	writePlist(t, agentsDir, "com.pogo.deploy", "/bin/pogo-deploy.sh", map[string]string{
		"PATH":            "/a/bin:/b/bin",
		"HOME":            root,
		"POGO_HOME":       filepath.Join(root, "pogo"),
		"POGO_DEPLOY_SRC": filepath.Join(root, "pogo", "deploy-src"),
	})
	return agentsDir, mailRoot
}

// TestDiscoverAdmitsOnlyDivergentDataSources is the admission rule, measured
// against the real arrangement. MAIL_DIR must be admitted on both jobs; nothing
// else may be, because a detector that reports on STATE_DIR and POGO_HOME
// alongside the finding is one whose findings get skimmed past.
func TestDiscoverAdmitsOnlyDivergentDataSources(t *testing.T) {
	agentsDir, mailRoot := realWorldFixture(t)

	consumers, err := Discover(agentsDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	got := map[string]string{}
	for _, c := range consumers {
		got[c.Label+"/"+c.SourceKey] = c.Source
	}
	want := map[string]string{
		"com.pogo.notify/MAIL_DIR":  filepath.Join(mailRoot, "daniel", "new"),
		"com.pogo.deadman/MAIL_DIR": filepath.Join(mailRoot, "human", "new"),
	}
	if len(got) != len(want) {
		t.Fatalf("admitted %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("admitted[%s] = %q, want %q", k, got[k], v)
		}
	}
}

// TestDiscoverAdmitsASoleConsumerWithSiblingBoxes. Rule 1 (a divergent binding
// between two instances of one program) is not the only way in, and it must not
// be: if the deadman were retired tomorrow, com.pogo.notify would be the only
// poll-mail.sh job on the box, and an audit that fell silent along with the peer
// that used to convict it would be exactly the arrangement this ticket is about.
func TestDiscoverAdmitsASoleConsumerWithSiblingBoxes(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "LaunchAgents")
	mailRoot := filepath.Join(root, "mail")
	mkdirs(t, agentsDir, filepath.Join(mailRoot, "daniel", "new"), filepath.Join(mailRoot, "human", "new"))

	writePlist(t, agentsDir, "com.pogo.notify", "/bin/poll-mail.sh", map[string]string{
		"MAIL_DIR": filepath.Join(mailRoot, "daniel", "new"),
	})

	consumers, err := Discover(agentsDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(consumers) != 1 || consumers[0].SourceKey != "MAIL_DIR" {
		t.Fatalf("consumers = %+v, want the lone notifier admitted via its sibling boxes", consumers)
	}
	peers := peersFor(consumers[0], consumers)
	if len(peers) != 1 || peers[0] != filepath.Join(mailRoot, "human", "new") {
		t.Errorf("peers = %v, want the sibling box", peers)
	}
}

// TestProcessEnvIsNeverASource. The sibling rule is structural, so it cannot
// tell a family of mailboxes from a family of directories that merely share a
// basename — and on that evidence it admitted a job's HOME. This pins the screen
// that fixed it, with a HOME that DOES have siblings so the test fails if the
// screen is removed and rule 2 is left to decide alone.
func TestProcessEnvIsNeverASource(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "LaunchAgents")
	mkdirs(t, agentsDir,
		filepath.Join(root, "accounts", "a", "home"),
		filepath.Join(root, "accounts", "b", "home"),
	)
	writePlist(t, agentsDir, "com.pogo.job", "/bin/job.sh", map[string]string{
		"HOME":   filepath.Join(root, "accounts", "a", "home"),
		"TMPDIR": filepath.Join(root, "accounts", "b", "home"),
	})

	consumers, err := Discover(agentsDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(consumers) != 0 {
		t.Errorf("admitted %+v; a job's HOME/TMPDIR is its environment, not the data it consumes", consumers)
	}
}

// TestSiblingsDeclineTheFilesystemRoot. HOME=/Users/daniel would otherwise glob
// /*/daniel — every account on the machine, which is not a family of boxes.
// Admitting it would bury the findings that matter under the ones that do not.
func TestSiblingsDeclineTheFilesystemRoot(t *testing.T) {
	if got := siblingDirs("/Users/daniel"); got != nil {
		t.Errorf("siblingDirs(/Users/daniel) = %v, want nil — a grandparent at the root is not a family", got)
	}
}

// TestDiscoverUnreadableDirIsAnError, not an empty slice. This package's
// founding bug is a quiet reading that actually means "I could not look".
func TestDiscoverUnreadableDirIsAnError(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("Discover on a missing directory returned no error; an unlooked-at machine must not read as a clean one")
	}
	if _, err := Discover(""); err == nil {
		t.Error("Discover(\"\") returned no error")
	}
}

// TestAuditOnTheRealArrangement runs the whole path — discovery, sampling and
// judgement — over a filesystem built to have the fault, with nothing stubbed.
func TestAuditOnTheRealArrangement(t *testing.T) {
	agentsDir, mailRoot := realWorldFixture(t)

	// The fleet writes `human`; nothing has ever written `daniel`.
	for i := 0; i < 3; i++ {
		f := filepath.Join(mailRoot, "human", "new", "msg"+string(rune('a'+i)))
		if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
			t.Fatalf("writing %s: %v", f, err)
		}
	}
	// Age the empty box well past the window, mtime and all.
	old := time.Now().Add(-40 * time.Hour)
	if err := os.Chtimes(filepath.Join(mailRoot, "daniel", "new"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rep := Audit(agentsDir, time.Now(), DefaultWindow)
	if rep.Err != nil {
		t.Fatalf("Audit: %v", rep.Err)
	}
	f := rep.Findings()
	if len(f) != 1 || f[0].Consumer.Label != "com.pogo.notify" {
		t.Fatalf("Findings() = %+v, want com.pogo.notify starved", f)
	}
	if f[0].Status != StatusStarved {
		t.Errorf("status = %q, want %q", f[0].Status, StatusStarved)
	}
	if !strings.Contains(f[0].Detail, filepath.Join(mailRoot, "human", "new")) {
		t.Errorf("detail = %q, want it to name the box that IS receiving", f[0].Detail)
	}
}

// TestSampleDirCountsArrivalsAndIgnoresDotfiles. A source that looks live
// because somebody opened a Finder window on it is the same false green this
// package exists to remove, one layer down.
func TestSampleDirCountsArrivalsAndIgnoresDotfiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := sampleDir(dir, now, time.Hour)
	if a.Count != 0 {
		t.Errorf("Count = %d with only a dotfile present, want 0", a.Count)
	}
	if !a.Exists {
		t.Error("Exists = false for a real directory")
	}

	if err := os.WriteFile(filepath.Join(dir, "msg1"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	a = sampleDir(dir, now, time.Hour)
	if a.Count != 1 {
		t.Errorf("Count = %d after one arrival, want 1", a.Count)
	}
	if !a.Active(time.Now(), time.Hour) {
		t.Error("Active = false for a directory that just received a file")
	}

	// An empty directory whose own mtime is recent is a DRAINED source, not a
	// dead one — the mtime moves on unlink too. Removing the file leaves no
	// entries and a fresh directory mtime.
	if err := os.Remove(filepath.Join(dir, "msg1")); err != nil {
		t.Fatal(err)
	}
	a = sampleDir(dir, time.Now(), time.Hour)
	if a.Count != 0 {
		t.Fatalf("Count = %d after the drain, want 0", a.Count)
	}
	if !a.Active(time.Now(), time.Hour) {
		t.Error("a drained directory reads as inactive; a working consumer's source would be reported starved")
	}
}

// TestSampleDirOnAMissingPath: absent is absent, and is not an error.
func TestSampleDirOnAMissingPath(t *testing.T) {
	a := sampleDir(filepath.Join(t.TempDir(), "gone"), time.Now(), time.Hour)
	if a.Exists || a.Err != nil {
		t.Errorf("sampleDir on a missing path = %+v, want Exists=false with no error", a)
	}
	if a.Active(time.Now(), time.Hour) {
		t.Error("a missing directory reads as active")
	}
}

// TestSampleDirOnAFile. A plist value that names a file is not a source, and
// isCandidateDir screens it out — but sampleDir must not report a file as a live
// directory if one ever reaches it.
func TestSampleDirOnAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "conf")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if a := sampleDir(f, time.Now(), time.Hour); a.Exists {
		t.Errorf("sampleDir on a file = %+v, want Exists=false", a)
	}
	if isCandidateDir(f) {
		t.Error("isCandidateDir accepted an existing file as a source directory")
	}
}
