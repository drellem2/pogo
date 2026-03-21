package refinery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeMacguffinItem(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckQAGate_NoQAItem(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "work", "available"), 0755)

	status, _ := checkQAGate(mgDir, "mg-abc")
	if status != QANotRequired {
		t.Errorf("expected QANotRequired, got %d", status)
	}
}

func TestCheckQAGate_QADone(t *testing.T) {
	mgDir := t.TempDir()
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "done"), "mg-qa1.md", `---
id: mg-qa1
type: qa
source: mg-abc
created: 2026-03-21T00:00:00Z
---

# QA for mg-abc
`)

	status, qaID := checkQAGate(mgDir, "mg-abc")
	if status != QAPassed {
		t.Errorf("expected QAPassed, got %d", status)
	}
	if qaID != "mg-qa1" {
		t.Errorf("expected qaID mg-qa1, got %s", qaID)
	}
}

func TestCheckQAGate_QAArchived(t *testing.T) {
	mgDir := t.TempDir()
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "archive", "2026-03"), "mg-qa2.md", `---
id: mg-qa2
type: qa
source: mg-def
created: 2026-03-21T00:00:00Z
---

# QA for mg-def
`)

	status, qaID := checkQAGate(mgDir, "mg-def")
	if status != QAPassed {
		t.Errorf("expected QAPassed, got %d", status)
	}
	if qaID != "mg-qa2" {
		t.Errorf("expected qaID mg-qa2, got %s", qaID)
	}
}

func TestCheckQAGate_QAPending_Available(t *testing.T) {
	mgDir := t.TempDir()
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "available"), "mg-qa3.md", `---
id: mg-qa3
type: qa
source: mg-ghi
created: 2026-03-21T00:00:00Z
---

# QA for mg-ghi
`)

	status, qaID := checkQAGate(mgDir, "mg-ghi")
	if status != QAPending {
		t.Errorf("expected QAPending, got %d", status)
	}
	if qaID != "mg-qa3" {
		t.Errorf("expected qaID mg-qa3, got %s", qaID)
	}
}

func TestCheckQAGate_QAPending_Claimed(t *testing.T) {
	mgDir := t.TempDir()
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "claimed"), "mg-qa4.md.1234", `---
id: mg-qa4
type: qa
source: mg-jkl
created: 2026-03-21T00:00:00Z
---

# QA for mg-jkl
`)

	status, qaID := checkQAGate(mgDir, "mg-jkl")
	if status != QAPending {
		t.Errorf("expected QAPending, got %d", status)
	}
	if qaID != "mg-qa4" {
		t.Errorf("expected qaID mg-qa4, got %s", qaID)
	}
}

func TestCheckQAGate_NonQAItemIgnored(t *testing.T) {
	mgDir := t.TempDir()
	// A task item with source field should not trigger QA gate
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "available"), "mg-task1.md", `---
id: mg-task1
type: task
source: mg-abc
created: 2026-03-21T00:00:00Z
---

# Not a QA item
`)

	status, _ := checkQAGate(mgDir, "mg-abc")
	if status != QANotRequired {
		t.Errorf("expected QANotRequired for non-qa type, got %d", status)
	}
}

func TestCheckQAGate_EmptyInputs(t *testing.T) {
	status, _ := checkQAGate("", "mg-abc")
	if status != QANotRequired {
		t.Errorf("expected QANotRequired for empty macguffin dir")
	}

	status, _ = checkQAGate("/tmp/fake", "")
	if status != QANotRequired {
		t.Errorf("expected QANotRequired for empty work ID")
	}
}

func TestCheckQAGate_DoneTakesPriorityOverPending(t *testing.T) {
	mgDir := t.TempDir()
	// QA item in done
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "done"), "mg-qa5.md", `---
id: mg-qa5
type: qa
source: mg-mno
created: 2026-03-21T00:00:00Z
---

# QA done
`)
	// Another QA item (different id) for same source still in available
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "available"), "mg-qa6.md", `---
id: mg-qa6
type: qa
source: mg-mno
created: 2026-03-21T00:00:00Z
---

# QA pending
`)

	// done/ is checked first, so should pass
	status, qaID := checkQAGate(mgDir, "mg-mno")
	if status != QAPassed {
		t.Errorf("expected QAPassed (done takes priority), got %d", status)
	}
	if qaID != "mg-qa5" {
		t.Errorf("expected qaID mg-qa5, got %s", qaID)
	}
}

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{
			name: "basic",
			content: `---
id: mg-abc
type: qa
source: mg-def
---
# Title`,
			want: map[string]string{"id": "mg-abc", "type": "qa", "source": "mg-def"},
		},
		{
			name:    "no frontmatter",
			content: "# Just a title",
			want:    map[string]string{},
		},
		{
			name: "with arrays (ignored)",
			content: `---
id: mg-xyz
type: task
depends: [mg-abc, mg-def]
---`,
			want: map[string]string{"id": "mg-xyz", "type": "task", "depends": "[mg-abc, mg-def]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFrontmatter(tt.content)
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: want %q, got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestProcessNext_QAHoldRequeues(t *testing.T) {
	mgDir := t.TempDir()
	// Create a pending QA item for work item "mg-test1"
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "claimed"), "mg-qa-test.md.999", `---
id: mg-qa-test
type: qa
source: mg-test1
---

# QA in progress
`)

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Submit a merge request whose Author matches the QA source
	_, err = r.Submit(MergeRequest{
		RepoPath: "/tmp/fakerepo",
		Branch:   "polecat-mg-test1",
		Author:   "mg-test1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// processNext should hold and requeue
	r.processNext()

	// Item should still be in queue (requeued)
	queue := r.Queue()
	if len(queue) != 1 {
		t.Fatalf("expected 1 queued item after hold, got %d", len(queue))
	}
	if queue[0].Status != StatusQueued {
		t.Errorf("expected status queued, got %s", queue[0].Status)
	}

	// History should be empty (nothing processed)
	history := r.History()
	if len(history) != 0 {
		t.Errorf("expected empty history, got %d items", len(history))
	}
}

func TestProcessNext_QAPassedProceeds(t *testing.T) {
	mgDir := t.TempDir()
	// Create a done QA item for work item "mg-test2"
	writeMacguffinItem(t, filepath.Join(mgDir, "work", "done"), "mg-qa-done.md", `---
id: mg-qa-done
type: qa
source: mg-test2
---

# QA passed
`)

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Submit — this will fail at the merge step (no real repo) but should
	// get past the QA gate and attempt the merge
	_, err = r.Submit(MergeRequest{
		RepoPath: "/tmp/nonexistent-repo",
		Branch:   "polecat-mg-test2",
		Author:   "mg-test2",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	// Queue should be empty (item was processed, not held)
	queue := r.Queue()
	if len(queue) != 0 {
		t.Errorf("expected empty queue after QA pass, got %d items", len(queue))
	}

	// Should be in history (failed at merge step, but past QA gate)
	history := r.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(history))
	}
	if history[0].Status != StatusFailed {
		t.Errorf("expected failed (no real repo), got %s", history[0].Status)
	}
}

func TestProcessNext_NoQAItemProceeds(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "work", "available"), 0755)

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.Submit(MergeRequest{
		RepoPath: "/tmp/nonexistent-repo",
		Branch:   "polecat-mg-test3",
		Author:   "mg-test3",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	// Queue should be empty (item was processed normally)
	queue := r.Queue()
	if len(queue) != 0 {
		t.Errorf("expected empty queue, got %d items", len(queue))
	}

	// Should be in history
	history := r.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(history))
	}
}
