package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole comparison is exercised against a temp dir on any platform, on purpose.
// An audit whose correctness can only be demonstrated on the box that has the bug is an
// audit nobody can change safely — the same argument auditLaunchAgent's tests make one
// file over.

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

const runnerSource = `#!/bin/sh
# child_pids PID — the pids whose parent is PID. ` + "`ps`" + `, not ` + "`pgrep -P`" + ` (mg-19e4).
# A ROOT THE BLIND WALK COULD NOT REACH. ` + "`pgrep -P 0`" + ` and ` + "`pgrep -P 1`" + ` both miss it.
child_pids() { ps -Ao pid=,ppid= | awk -v p="$1" '$2==p{print $1}'; }
# the fleet bounce is keyed on liveness (mg-a854)
`

const runnerInstalled = `#!/bin/sh
child_pids() { for child in $(pgrep -P "$1" 2>/dev/null); do echo "$child"; done; }
`

func TestPayloadAuditReportsAStaleInstalledCopy(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.sh", runnerSource)
	inst := writeFile(t, dir, "inst.sh", runnerInstalled)

	a := auditPayloadScript("com.pogo.deploy", "pogo-deploy.sh", inst, src, nil,
		"pogo service install-deploy", "scripts/launchd/pogo-deploy.sh", true)

	if a.Status != PayloadStale {
		t.Fatalf("status = %q, want %q — detail: %s", a.Status, PayloadStale, a.Detail)
	}
	if a.Source != src {
		t.Errorf("Source = %q, want %q: a verdict that does not name what it compared against is the claim this package exists to stop", a.Source, src)
	}
	// The detail has to name the JOB, not only the file: "pogo-deploy.sh differs" is
	// a fact about a path, and what a reader needs to act is that com.pogo.deploy
	// fires this file every night.
	if !strings.Contains(a.Detail, "com.pogo.deploy") {
		t.Errorf("the drift detail does not name the job that executes the file:\n%s", a.Detail)
	}
	if !strings.Contains(a.Detail, "pogo service install-deploy") {
		t.Errorf("the drift detail does not name a remedy:\n%s", a.Detail)
	}
}

// The instrument that must NOT be a frequency comparison.
//
// `grep -c 'pgrep -P'` over this exact pair returns 2 for the FIXED source and 1 for the
// BROKEN installed copy: mg-19e4's fix is a prohibition, and a prohibition fix ADDS
// occurrences of the string it prohibits. A reader who counts gets the answer exactly
// backwards, and on 2026-09-08 two agents independently nearly did. Set difference over
// work-item ids is position-free and does not reverse.
func TestPayloadAuditNamesTheMissingWorkItemsAndNotTheCounts(t *testing.T) {
	fixed, broken := strings.Count(runnerSource, "pgrep -P"), strings.Count(runnerInstalled, "pgrep -P")
	if fixed <= broken {
		t.Fatalf("fixture drift: the FIXED source must contain MORE occurrences of the prohibited string than the broken copy (fixed %d, broken %d), or this test is not pinning the trap it exists for", fixed, broken)
	}
	if broken != 1 {
		t.Fatalf("fixture drift: the BROKEN installed copy should carry exactly one LIVE call (got %d)", broken)
	}

	missing := missingWorkItemIDs([]byte(runnerSource), []byte(runnerInstalled))
	want := []string{"mg-19e4", "mg-a854"}
	if len(missing) != len(want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("missing = %v, want %v (sorted, so a set difference cannot depend on where in the file an id appears)", missing, want)
		}
	}
}

// The list is a LOWER BOUND, and the caveat must travel attached to it. A bare list of
// ids reads as "these are the missing fixes"; the true claim is "at least these, and a
// fix that left no id comment does not appear at all". mg-769a is exactly such a fix one
// file over, and reading its absence from a list as evidence it was undeployed is how
// this ticket came to assert something that was never true.
func TestMissingIDsNoteCarriesItsOwnLowerBoundCaveat(t *testing.T) {
	withIDs := missingIDsNote([]string{"mg-19e4"})
	if !strings.Contains(withIDs, "LOWER BOUND") {
		t.Errorf("the id list ships without its caveat:\n%s", withIDs)
	}
	// And the empty list must not read as a clean bill either.
	empty := missingIDsNote(nil)
	if !strings.Contains(empty, "NOT a report that the two agree") {
		t.Errorf("an empty id list reads as agreement:\n%s", empty)
	}
}

func TestPayloadAuditMatchIsByteEqualityAndNotTheIDList(t *testing.T) {
	dir := t.TempDir()
	// Same ids on both sides, different bytes: the id list is empty and the verdict
	// must still be STALE. The bytes are the predicate; the ids are the description.
	src := writeFile(t, dir, "src.sh", "#!/bin/sh\n# mg-19e4\necho fixed\n")
	inst := writeFile(t, dir, "inst.sh", "#!/bin/sh\n# mg-19e4\necho broken\n")

	a := auditPayloadScript("com.pogo.deploy", "pogo-deploy.sh", inst, src, nil,
		"pogo service install-deploy", "scripts/launchd/pogo-deploy.sh", true)
	if a.Status != PayloadStale {
		t.Fatalf("status = %q, want %q: byte equality is the predicate, and an empty id list must never promote a differing file to a match", a.Status, PayloadStale)
	}
	if len(a.MissingIDs) != 0 {
		t.Errorf("MissingIDs = %v, want empty", a.MissingIDs)
	}
}

func TestPayloadAuditMatchingCopyIsOK(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.sh", runnerSource)
	inst := writeFile(t, dir, "inst.sh", runnerSource)

	a := auditPayloadScript("com.pogo.deploy", "pogo-deploy.sh", inst, src, nil,
		"pogo service install-deploy", "scripts/launchd/pogo-deploy.sh", true)
	if a.Status != PayloadOK {
		t.Fatalf("status = %q, want %q — detail: %s", a.Status, PayloadOK, a.Detail)
	}
	if a.SourceLines == 0 || a.InstalledLines != a.SourceLines {
		t.Errorf("line counts = installed %d / source %d, want equal and non-zero", a.InstalledLines, a.SourceLines)
	}
}

// The state that must never read as a match. A plist is rendered from a Go template and
// is therefore comparable from any directory; a payload script's expectation is a PATH,
// and a path that does not resolve is how a subject stops being audited with nobody
// deciding it should be.
func TestPayloadAuditUnfindableSourceIsNotCheckedAndNeverOK(t *testing.T) {
	dir := t.TempDir()
	inst := writeFile(t, dir, "inst.sh", runnerInstalled)

	a := auditPayloadScript("com.pogo.deploy", "pogo-deploy.sh", inst, "",
		errors.New("pogo-deploy.sh not found in any of: [...]"),
		"pogo service install-deploy", "scripts/launchd/pogo-deploy.sh", true)

	if a.Status != PayloadUnknown {
		t.Fatalf("status = %q, want %q", a.Status, PayloadUnknown)
	}
	if !strings.Contains(a.Detail, "NOT CHECKED") {
		t.Errorf("an uncomparable payload does not say NOT CHECKED:\n%s", a.Detail)
	}
}

// A plist that names a program which is not on disk. launchd fires the job, the exec
// fails, and the failure is not a pogo log line — nothing downstream observes it. This
// outranks ordinary drift for that reason.
func TestPayloadAuditMissingScriptUnderAnInstalledPlistIsAnOrphan(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.sh", runnerSource)

	a := auditPayloadScript("com.pogo.reclaim", "pogo-reclaim.sh", filepath.Join(dir, "gone.sh"), src, nil,
		"pogo service install-reclaim", "scripts/launchd/pogo-reclaim.sh", true)
	if a.Status != PayloadOrphan {
		t.Fatalf("status = %q, want %q — detail: %s", a.Status, PayloadOrphan, a.Detail)
	}
	if !strings.Contains(a.Detail, "ORPHANED JOB") {
		t.Errorf("the orphan detail does not say so:\n%s", a.Detail)
	}
}

// The same missing file WITHOUT a plist pointing at it is consistent, not broken — a job
// nobody installed. Reporting it as an orphan would put a permanent finding on every box
// that declined to install the reclaim job.
func TestPayloadAuditMissingScriptWithNoPlistIsAbsentNotOrphan(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.sh", runnerSource)

	a := auditPayloadScript("com.pogo.reclaim", "pogo-reclaim.sh", filepath.Join(dir, "gone.sh"), src, nil,
		"pogo service install-reclaim", "scripts/launchd/pogo-reclaim.sh", false)
	if a.Status != PayloadAbsent {
		t.Fatalf("status = %q, want %q — detail: %s", a.Status, PayloadAbsent, a.Detail)
	}
}

// Every registry row has to resolve to a real repo file, or the audit reports UNKNOWN
// forever on the very subject it was added for and nobody notices. This is the merge
// gate's copy of that check: it runs from the checkout, where the sources exist.
func TestEveryManagedPayloadScriptHasASourceInThisCheckout(t *testing.T) {
	repo := repoRootForTest(t)
	for _, s := range managedPayloadScripts() {
		if s.SourceNote == "" {
			t.Errorf("%s/%s has no SourceNote — the repo path is what a reader is sent to", s.Label, s.Name)
			continue
		}
		p := filepath.Join(repo, filepath.FromSlash(s.SourceNote))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s/%s names %s, which does not exist in this checkout (%v) — a moved or renamed script silently turns its audit row into a permanent NOT CHECKED", s.Label, s.Name, s.SourceNote, err)
		}
	}
}

// The two registries have to agree about which jobs exist. A payload row for a label
// managedLaunchAgents() does not carry would be audited against a plist nothing renders.
func TestEveryPayloadRowBelongsToAManagedJob(t *testing.T) {
	known := map[string]bool{}
	for _, a := range managedLaunchAgents() {
		known[a.Label] = true
	}
	for _, s := range managedPayloadScripts() {
		if !known[s.Label] {
			t.Errorf("payload %s is owned by %s, which is not in managedLaunchAgents() — the two registries have diverged", s.Name, s.Label)
		}
	}
}

// repoRootForTest walks up from the package directory to the checkout root.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the checkout root (no go.mod above the package directory)")
	return ""
}

// The fallback exists because the audit reproduced its own subject without it: run from
// a directory that is not a checkout, every payload row came back NOT CHECKED while
// three were stale — and pogo-self-deploy does not mail on UNKNOWN, so the nightly would
// have found the drift and silenced it. This pins that the nightly checkout is consulted
// and that using it is disclosed rather than inferred.
func TestPayloadSourceFallsBackToTheNightlyCheckoutAndSaysSo(t *testing.T) {
	src := t.TempDir()
	rel := "scripts/launchd/pogo-deploy.sh"
	if err := os.MkdirAll(filepath.Join(src, "scripts", "launchd"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(src, "scripts", "launchd"), "pogo-deploy.sh", runnerSource)
	t.Setenv("POGO_DEPLOY_SRC", src)

	s := managedPayloadScript{
		Label: "com.pogo.deploy", Name: "pogo-deploy.sh", SourceNote: rel,
		Source: func() (string, error) { return "", errors.New("not found beside this binary") },
	}
	got, fallback, err := payloadSourceOrFallback(s)
	if err != nil {
		t.Fatalf("payloadSourceOrFallback: %v", err)
	}
	if !fallback {
		t.Error("the nightly checkout was used and the result does not say so — a comparison against a lagging snapshot is worth something different from one against this build's tree")
	}
	if !strings.HasSuffix(got, filepath.FromSlash(rel)) {
		t.Errorf("source = %q, want a path under %s ending in %s", got, src, rel)
	}
}

// And when neither is there, it is still NOT CHECKED — the fallback widens the search,
// it does not invent an expectation.
func TestPayloadSourceFallbackStillFailsWhenNeitherExists(t *testing.T) {
	t.Setenv("POGO_DEPLOY_SRC", t.TempDir())
	s := managedPayloadScript{
		Label: "com.pogo.deploy", Name: "pogo-deploy.sh", SourceNote: "scripts/launchd/pogo-deploy.sh",
		Source: func() (string, error) { return "", errors.New("not found beside this binary") },
	}
	if _, fallback, err := payloadSourceOrFallback(s); err == nil || fallback {
		t.Errorf("got fallback=%v err=%v, want an error and no fallback", fallback, err)
	}
}
