package refinery

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// useTempEventLog redirects events.Emit to a temp file for the test and
// returns its path.
func useTempEventLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	events.SetLogPathForTesting(path)
	t.Cleanup(func() { events.SetLogPathForTesting("") })
	return path
}

// readEvents parses every JSONL line in path into an events.Event.
// Returns nil if the file does not exist.
func readEvents(t *testing.T, path string) []events.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var out []events.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e events.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal %q: %v", scanner.Text(), err)
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// filterEvents returns only events whose EventType matches one of the given.
func filterEvents(all []events.Event, types ...string) []events.Event {
	want := make(map[string]struct{}, len(types))
	for _, t := range types {
		want[t] = struct{}{}
	}
	var out []events.Event
	for _, e := range all {
		if _, ok := want[e.EventType]; ok {
			out = append(out, e)
		}
	}
	return out
}

// TestEmitsRefineryMergedEvents exercises the success path and verifies that
// refinery_merge_attempted + refinery_merged are written to the log.
func TestEmitsRefineryMergedEvents(t *testing.T) {
	logPath := useTempEventLog(t)

	originDir := initBareOrigin(t, "main")

	// Push a gate-pass build.sh and a feature branch.
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add build")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "polecat-mg-aaaa")
	os.WriteFile(filepath.Join(workDir, "feat.txt"), []byte("feat"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat (mg-aaaa)")
	run(t, workDir, "git", "push", "origin", "polecat-mg-aaaa")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-mg-aaaa",
		TargetRef: "main",
		Author:    "mg-aaaa",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	mr := r.Get(id)
	if mr == nil || mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %+v", mr)
	}

	all := readEvents(t, logPath)
	attempted := filterEvents(all, "refinery_merge_attempted")
	merged := filterEvents(all, "refinery_merged")
	failed := filterEvents(all, "refinery_merge_failed")

	if len(attempted) != 1 {
		t.Fatalf("expected 1 refinery_merge_attempted, got %d", len(attempted))
	}
	if len(merged) != 1 {
		t.Fatalf("expected 1 refinery_merged, got %d", len(merged))
	}
	if len(failed) != 0 {
		t.Errorf("expected 0 refinery_merge_failed, got %d", len(failed))
	}

	// Validate refinery_merge_attempted envelope and details.
	a := attempted[0]
	if a.Agent != "refinery" {
		t.Errorf("attempted.agent = %q, want refinery", a.Agent)
	}
	if a.Repo != originDir {
		t.Errorf("attempted.repo = %q, want %q", a.Repo, originDir)
	}
	if a.WorkItemID != "mg-aaaa" {
		t.Errorf("attempted.work_item_id = %q, want mg-aaaa", a.WorkItemID)
	}
	wantAttemptDetails := map[string]any{
		"merge_request_id": id,
		"branch":           "polecat-mg-aaaa",
		"target":           "main",
		"author":           "mg-aaaa",
	}
	for k, v := range wantAttemptDetails {
		if got := a.Details[k]; got != v {
			t.Errorf("attempted.details[%q] = %v, want %v", k, got, v)
		}
	}
	if attempt, _ := a.Details["attempt"].(float64); attempt != 1 {
		t.Errorf("attempted.details.attempt = %v, want 1", a.Details["attempt"])
	}

	// Validate refinery_merged envelope and details.
	m := merged[0]
	if m.Agent != "refinery" {
		t.Errorf("merged.agent = %q, want refinery", m.Agent)
	}
	if m.WorkItemID != "mg-aaaa" {
		t.Errorf("merged.work_item_id = %q, want mg-aaaa", m.WorkItemID)
	}
	if m.Details["merge_request_id"] != id {
		t.Errorf("merged.details.merge_request_id = %v, want %s", m.Details["merge_request_id"], id)
	}
	sha, _ := m.Details["merge_commit"].(string)
	if len(sha) != 40 {
		t.Errorf("merged.details.merge_commit = %q (len %d), want 40-char SHA", sha, len(sha))
	}
	if attempt, _ := m.Details["attempt"].(float64); attempt != 1 {
		t.Errorf("merged.details.attempt = %v, want 1", m.Details["attempt"])
	}
	if _, ok := m.Details["duration_seconds"].(float64); !ok {
		t.Errorf("merged.details.duration_seconds missing or wrong type: %v", m.Details["duration_seconds"])
	}
}

// TestEmitsRefineryMergeFailedEvent verifies the failure path emits a
// terminal refinery_merge_failed with a stage and reason.
func TestEmitsRefineryMergeFailedEvent(t *testing.T) {
	logPath := useTempEventLog(t)

	// Bare origin with a failing build.sh on main.
	originDir := initBareOrigin(t, "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\necho boom\nexit 1\n"), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add failing build")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "polecat-cat-bbbb")
	os.WriteFile(filepath.Join(workDir, "bad.txt"), []byte("x"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: broken")
	run(t, workDir, "git", "push", "origin", "polecat-cat-bbbb")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-cat-bbbb",
		TargetRef: "main",
		Author:    "cat-bbbb",
	}); err != nil {
		t.Fatal(err)
	}

	r.processNext()

	all := readEvents(t, logPath)
	attempted := filterEvents(all, "refinery_merge_attempted")
	merged := filterEvents(all, "refinery_merged")
	failed := filterEvents(all, "refinery_merge_failed")

	if len(attempted) == 0 {
		t.Fatalf("expected ≥1 refinery_merge_attempted, got 0")
	}
	if len(merged) != 0 {
		t.Errorf("expected 0 refinery_merged, got %d", len(merged))
	}
	if len(failed) == 0 {
		t.Fatalf("expected ≥1 refinery_merge_failed, got 0")
	}

	// The last failed event should be terminal.
	last := failed[len(failed)-1]
	if last.Agent != "refinery" {
		t.Errorf("failed.agent = %q, want refinery", last.Agent)
	}
	if last.WorkItemID != "bbbb" {
		// "cat-bbbb" → strip "cat-" prefix → "bbbb"
		t.Errorf("failed.work_item_id = %q, want bbbb", last.WorkItemID)
	}
	if terminal, _ := last.Details["terminal"].(bool); !terminal {
		t.Errorf("failed.details.terminal = %v, want true", last.Details["terminal"])
	}
	if stage, _ := last.Details["stage"].(string); stage != "build" && stage != "test" {
		t.Errorf("failed.details.stage = %q, want build or test", stage)
	}
	if reason, _ := last.Details["reason"].(string); reason == "" {
		t.Error("failed.details.reason is empty")
	}
}

// TestSummarizeReason caps long messages and collapses to a single line.
func TestSummarizeReason(t *testing.T) {
	if got := summarizeReason(nil); got != "" {
		t.Errorf("summarizeReason(nil) = %q, want empty", got)
	}
	if got := summarizeReason(errors.New("first line\nsecond line")); got != "first line" {
		t.Errorf("summarizeReason multiline = %q, want first line", got)
	}
	long := strings.Repeat("a", reasonCap+50)
	got := summarizeReason(errors.New(long))
	if len(got) != reasonCap {
		t.Errorf("summarizeReason long len = %d, want %d", len(got), reasonCap)
	}
}

// TestGateStage classifies gate commands.
func TestGateStage(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "test"},
		{[]string{"./build.sh"}, "build"},
		{[]string{"./test.sh"}, "test"},
		{[]string{"./build.sh", "./test.sh"}, "test"},
		{[]string{"build"}, "build"},
		{[]string{"go test ./..."}, "test"},
		{[]string{"./custom-gate.sh"}, "test"},
	}
	for _, c := range cases {
		if got := gateStage(c.in); got != c.want {
			t.Errorf("gateStage(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEventEmissionIsBestEffort ensures Emit failures cannot stop a merge —
// when the log path resolves to a directory that cannot be created (we set
// it to an unwritable place), processMerge should still complete normally.
func TestEventEmissionIsBestEffort(t *testing.T) {
	// Point the log at /dev/null/events.log — opening will fail because
	// /dev/null is not a directory. Emit should swallow that failure.
	events.SetLogPathForTesting("/dev/null/events.log")
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	originDir := initBareOrigin(t, "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "init")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "polecat-cccc")
	os.WriteFile(filepath.Join(workDir, "f.txt"), []byte("f"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat")
	run(t, workDir, "git", "push", "origin", "polecat-cccc")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-cccc",
		TargetRef: "main",
		Author:    "cat-cccc",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()
	mr := r.Get(id)
	if mr == nil || mr.Status != StatusMerged {
		t.Fatalf("expected merge to succeed despite event-log error, got %+v", mr)
	}
}
