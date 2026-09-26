package stallwatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// preFix190 is the commit immediately before the gh drellem2/pogo#190 fix
// (9b25b59, mg-00d2): the stallwatch package with the flat 5m unread-mail
// cooldown that counted its own notices.
const preFix190 = "c4455d4a3eed2f2e617214c9b8e9f4e043fa7b63"

// preFix190Driver is dropped into the pre-fix package as its only test file.
// It is the same 13h30m offline-mailbox replay as simulate190, written against
// the API that package had, and it writes the subjects of every notice sent to
// $PREFIX190_OUT as JSON.
const preFix190Driver = `package stallwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

func TestPreFix190Driver(t *testing.T) {
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	if err := os.MkdirAll(filepath.Join(workRoot, "available"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.StallWatchConfig{
		Enabled:                   true,
		Agent:                     "mayor",
		UnclaimedItemAgeThreshold: 10 * time.Minute,
		UnreadMailAgeThreshold:    10 * time.Minute,
		MaxUnreadMailCount:        5,
		NudgeCooldown:             5 * time.Minute,
		RepeatBackoffCap:          4 * time.Hour,
	}
	write := func(name, from string, at time.Time) {
		dir := filepath.Join(mailRoot, cfg.Agent, "new")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		body := fmt.Sprintf("Message-Id: %s\nFrom: %s\nSubject: s\nDate: %s\n\nbody\n", name, from, at.UTC().Format(time.RFC3339))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	var now time.Time
	var subjects []string
	// pogod's offline road: each notice lands, From: stall-watch, in the very
	// maildir the check reads.
	nudge := func(agent string, n Notice) (Delivery, error) {
		subjects = append(subjects, n.Subject)
		write(fmt.Sprintf("notice-%04d", len(subjects)), "stall-watch", now)
		return Delivery{Channel: DeliveryMail}, nil
	}
	w := New(cfg, Options{WorkRoot: workRoot, MailRoot: mailRoot, Nudge: nudge, Emit: func(events.Event) {}})

	start := time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC)
	write("real-0001", "pm-pogo", start)
	window := 13*time.Hour + 30*time.Minute
	for elapsed := time.Duration(0); elapsed <= window; elapsed += 30 * time.Second {
		now = start.Add(elapsed)
		w.Check(now)
	}
	out, err := json.Marshal(subjects)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("PREFIX190_OUT"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}
`

// TestUnreadMail190ControlRunsThePreFixCode is the positive control for
// TestUnreadMailSelfFeedingRepeatsAreBounded, and it runs the OLD code, not new
// code configured to imitate it (mg-4d5e). It extracts the stallwatch package
// and its module-internal imports from preFix190 with `git archive`, drops
// preFix190Driver in as the package's only test file, runs it with the Go
// toolchain that built this test, and asserts what #190 reported:
//
//   - 161 notices over the 13h30m replay — one per flat 5m cooldown from 10m
//     to 810m — which the new bound of 9 must not admit;
//   - a subject that counts the watcher's own notices: the Nth notice says
//     "N unread mail" with a single real message in the box.
//
// It needs the commit in the local object store. The merge gate runs from a
// full clone and has it; a shallow CI checkout does not, and there the test
// SKIPS saying so rather than pretending to have measured anything.
func TestUnreadMail190ControlRunsThePreFixCode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the pre-fix package with the go toolchain; skipped in -short")
	}
	gitOut := func(args ...string) (string, error) {
		out, err := exec.Command("git", args...).Output()
		return strings.TrimSpace(string(out)), err
	}
	top, err := gitOut("rev-parse", "--show-toplevel")
	if err != nil {
		t.Skipf("not in a git checkout (%v): cannot reach the pre-fix code, so this control measured nothing", err)
	}
	if _, err := gitOut("-C", top, "cat-file", "-e", preFix190+"^{commit}"); err != nil {
		t.Skipf("pre-fix commit %s is not in this clone (shallow checkout?): this control measured nothing", preFix190)
	}

	// stallwatch and the module-internal packages it imported at preFix190
	// (config, events and workitem; events pulls in testtmp). A fixed list is
	// safe because the commit is fixed: if one were missing the build below
	// would fail, loudly, not measure something else.
	tree := t.TempDir()
	paths := []string{"go.mod", "go.sum"}
	for _, pkg := range []string{"config", "events", "stallwatch", "testtmp", "workitem"} {
		paths = append(paths, "internal/"+pkg)
	}
	archive := exec.Command("git", append([]string{"-C", top, "archive", "--format=tar", preFix190, "--"}, paths...)...)
	var tarball, stderr bytes.Buffer
	archive.Stdout, archive.Stderr = &tarball, &stderr
	if err := archive.Run(); err != nil {
		t.Fatalf("git archive %s: %v\n%s", preFix190, err, stderr.String())
	}
	untar := exec.Command("tar", "-x", "-C", tree)
	untar.Stdin = &tarball
	if out, err := untar.CombinedOutput(); err != nil {
		t.Fatalf("untar: %v\n%s", err, out)
	}
	pkgDir := filepath.Join(tree, "internal", "stallwatch")
	// Only the package's production code runs; its own tests are not ours.
	old, _ := filepath.Glob(filepath.Join(pkgDir, "*_test.go"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "prefix190_driver_test.go"), []byte(preFix190Driver), 0o644); err != nil {
		t.Fatal(err)
	}

	outFile := filepath.Join(t.TempDir(), "subjects.json")
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	run := exec.Command(goBin, "test", "-count=1", "-p=1", "-run", "^TestPreFix190Driver$", "./internal/stallwatch/")
	run.Dir = tree
	run.Env = append(os.Environ(),
		"PREFIX190_OUT="+outFile,
		"GOTOOLCHAIN=local", // the toolchain that built this test, not a download
		"GOWORK=off",
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off", // the module cache already holds this module's deps
	)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("running the pre-fix package failed — the control measured nothing: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("driver wrote no result: %v", err)
	}
	var subjects []string
	if err := json.Unmarshal(raw, &subjects); err != nil {
		t.Fatalf("driver result: %v", err)
	}

	if len(subjects) != 161 {
		t.Fatalf("pre-fix code drew %d notices over 13h30m; want 161 (one per 5m from 10m to 810m)", len(subjects))
	}
	for i, s := range subjects {
		want := fmt.Sprintf("stall-watch: %d unread mail,", i+1)
		if !strings.HasPrefix(s, want) {
			t.Fatalf("pre-fix notice %d subject %q; want prefix %q — the old code counting its own notices", i+1, s, want)
		}
	}
}
