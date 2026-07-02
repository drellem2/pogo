package refinery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPersistent builds a Refinery with persistence enabled at statePath.
func newPersistent(t *testing.T, statePath string) *Refinery {
	t.Helper()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour, // won't tick in tests
		WorktreeDir:  t.TempDir(),
		StatePath:    statePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestStateSurvivesRestart is the restart repro from mg-abfd: submit, "kill"
// pogod (drop the instance), build a fresh Refinery from the same state file,
// and `refinery show <id>` (Get) must still return the MR.
func TestStateSurvivesRestart(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")

	r1 := newPersistent(t, statePath)
	id1, err := r1.Submit(MergeRequest{RepoPath: originDir, Branch: "feature-1", Author: "cat-a"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := r1.Submit(MergeRequest{RepoPath: originDir, Branch: "feature-2", Author: "cat-b"})
	if err != nil {
		t.Fatal(err)
	}
	// No Stop() — simulate an unclean death. Write-through persistence must
	// have already captured both submits.

	r2 := newPersistent(t, statePath)
	if mr := r2.Get(id1); mr == nil || mr.Branch != "feature-1" || mr.Status != StatusQueued {
		t.Fatalf("MR %s not restored after restart: %+v", id1, mr)
	}
	if mr := r2.Get(id2); mr == nil || mr.Branch != "feature-2" {
		t.Fatalf("MR %s not restored after restart: %+v", id2, mr)
	}
	queue := r2.Queue()
	if len(queue) != 2 {
		t.Fatalf("expected 2 queued items after restart, got %d", len(queue))
	}
	// FIFO order preserved.
	if queue[0].ID != id1 || queue[1].ID != id2 {
		t.Errorf("queue order not preserved: got %s, %s", queue[0].ID, queue[1].ID)
	}
}

func TestStateRestoresHistoryAndFailureCounts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	st := &persistedState{
		History: []*MergeRequest{
			{ID: "mr-old", Branch: "b1", Author: "cat-x", Status: StatusFailed, DoneTime: time.Now()},
		},
		FailureCounts: map[string]int{"cat-x": 2},
	}
	if err := (&store{path: statePath}).save(st); err != nil {
		t.Fatal(err)
	}

	r := newPersistent(t, statePath)
	if mr := r.Get("mr-old"); mr == nil || mr.Status != StatusFailed {
		t.Fatalf("history MR not restored: %+v", mr)
	}
	if got := r.AuthorFailureCount("cat-x"); got != 2 {
		t.Errorf("failure count not restored: got %d, want 2", got)
	}
}

func TestStateRefusesNewerVersion(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	data := `{"version": 99, "queue": [], "history": []}`
	if err := os.WriteFile(statePath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{WorktreeDir: t.TempDir(), StatePath: statePath})
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("expected refuse-newer-version error, got %v", err)
	}
	// The newer-version file must not have been overwritten.
	after, _ := os.ReadFile(statePath)
	if string(after) != data {
		t.Error("newer-version state file was modified")
	}
}

func TestStateCorruptFileBackedUpAndStartsEmpty(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{WorktreeDir: t.TempDir(), StatePath: statePath})
	if err != nil {
		t.Fatalf("corrupt state file should not fail New: %v", err)
	}
	if len(r.Queue()) != 0 || len(r.History()) != 0 {
		t.Error("expected empty state after corrupt file")
	}
	if _, err := os.Stat(statePath + ".corrupt"); err != nil {
		t.Errorf("corrupt file not backed up: %v", err)
	}
}

// setupRecoveryOrigin builds a bare origin with main plus a pushed branch, and
// returns (originDir, gate marker path). The work clone gets a test.sh gate
// that touches the marker — recovery must never re-run gates, so the marker
// must not exist after a merged-resolution recovery.
func setupRecoveryOrigin(t *testing.T, branch string, mergeToMain bool) (string, string) {
	t.Helper()
	originDir := initBareOrigin(t, "main")
	marker := filepath.Join(t.TempDir(), "gates-ran")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "checkout", "-b", branch)
	os.WriteFile(filepath.Join(workDir, "test.sh"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("feature"), 0o644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feature commit")
	run(t, workDir, "git", "push", "origin", branch)
	if mergeToMain {
		// Simulate the post-push/pre-history crash window: the branch has
		// already been fast-forward merged and pushed to main.
		run(t, workDir, "git", "checkout", "main")
		run(t, workDir, "git", "merge", "--ff-only", branch)
		run(t, workDir, "git", "push", "origin", "main")
	}
	return originDir, marker
}

// writeInFlightState persists a state file whose Processing slot holds mr,
// simulating a daemon that died mid-merge.
func writeInFlightState(t *testing.T, statePath string, mr *MergeRequest) {
	t.Helper()
	if err := (&store{path: statePath}).save(&persistedState{Processing: mr}); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryCrashWindows is the crash-window table test from mg-abfd
// Decision 3: an in-flight MR found in the state file is resolved via the
// ancestor probe, not blindly re-run.
func TestRecoveryCrashWindows(t *testing.T) {
	fixedNow := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		mergedToMain bool
		deleteBranch bool
		wantStatus   MergeStatus
		wantLost     bool
		wantRequeued bool
		wantOnMerged bool
	}{
		{
			name:         "post-push pre-history crash resolves to merged without re-running gates",
			mergedToMain: true,
			wantStatus:   StatusMerged,
			wantOnMerged: true,
		},
		{
			name:         "crash before push re-queues at head",
			mergedToMain: false,
			wantStatus:   StatusQueued,
			wantRequeued: true,
		},
		{
			name:         "probe failure (branch deleted) moves MR to lost list",
			mergedToMain: false,
			deleteBranch: true,
			wantLost:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originDir, marker := setupRecoveryOrigin(t, "polecat-fix", tc.mergedToMain)
			if tc.deleteBranch {
				run(t, originDir, "git", "update-ref", "-d", "refs/heads/polecat-fix")
			}

			statePath := filepath.Join(t.TempDir(), "refinery-state.json")
			inFlight := &MergeRequest{
				ID:         "mr-inflight",
				RepoPath:   originDir,
				Branch:     "polecat-fix",
				TargetRef:  "main",
				Author:     "cat-mg-1",
				Status:     StatusProcessing,
				SubmitTime: fixedNow.Add(-time.Minute),
			}
			writeInFlightState(t, statePath, inFlight)

			r := newPersistent(t, statePath)
			r.nowFunc = func() time.Time { return fixedNow }

			// Before resolution the recovered item is visible as processing.
			if mr := r.Get("mr-inflight"); mr == nil {
				t.Fatal("in-flight MR not indexed after load")
			}

			var onMergedFired bool
			r.SetOnMerged(func(mr *MergeRequest) { onMergedFired = mr.ID == "mr-inflight" })

			r.resolveRecovered()

			if tc.wantLost {
				if r.Get("mr-inflight") != nil {
					t.Error("lost MR should no longer resolve via Get")
				}
				le := r.LostInfo("mr-inflight")
				if le == nil {
					t.Fatal("expected lost-list entry")
				}
				if le.Branch != "polecat-fix" || le.Author != "cat-mg-1" {
					t.Errorf("lost entry missing context: %+v", le)
				}
				return
			}

			mr := r.Get("mr-inflight")
			if mr == nil {
				t.Fatal("recovered MR vanished")
			}
			if mr.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", mr.Status, tc.wantStatus)
			}
			if tc.wantOnMerged {
				if !onMergedFired {
					t.Error("OnMerged not fired for recovered merge")
				}
				if mr.DoneTime != fixedNow {
					t.Errorf("DoneTime = %v, want nowFunc time %v", mr.DoneTime, fixedNow)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Error("quality gates re-ran during recovery of an already-merged MR")
				}
				if len(r.History()) != 1 {
					t.Errorf("expected 1 history entry, got %d", len(r.History()))
				}
			}
			if tc.wantRequeued {
				queue := r.Queue()
				if len(queue) != 1 || queue[0].ID != "mr-inflight" {
					t.Fatalf("expected MR re-queued at head, got %+v", queue)
				}
			}

			// Resolution must be persisted: a second restart sees the outcome,
			// not the in-flight item again.
			r2 := newPersistent(t, statePath)
			mr2 := r2.Get("mr-inflight")
			if mr2 == nil || mr2.Status != tc.wantStatus {
				t.Errorf("resolution not persisted across second restart: %+v", mr2)
			}
		})
	}
}

// TestRecoveryRequeueCleansCloneMidRebase verifies that recovery clears an
// in-progress rebase left behind by a crash — today ensureWorktree only
// checks .git existence, so without the cleanup every later git op fails.
func TestRecoveryRequeueCleansCloneMidRebase(t *testing.T) {
	originDir, _ := setupRecoveryOrigin(t, "polecat-fix", false)
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	inFlight := &MergeRequest{
		ID: "mr-inflight", RepoPath: originDir, Branch: "polecat-fix",
		TargetRef: "main", Author: "cat-mg-1", Status: StatusProcessing,
	}
	writeInFlightState(t, statePath, inFlight)

	r := newPersistent(t, statePath)

	// Fabricate crash debris: clone the repo into the refinery's worktree
	// slot and fake an in-progress rebase.
	wtDir, err := r.ensureWorktree(originDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtDir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wtDir, ".git", "rebase-merge", "head-name"), []byte("refs/heads/polecat-fix\n"), 0o644)

	r.resolveRecovered()

	queue := r.Queue()
	if len(queue) != 1 || queue[0].ID != "mr-inflight" {
		t.Fatalf("expected re-queued MR, got %+v", queue)
	}
	if _, err := os.Stat(filepath.Join(wtDir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Error("in-progress rebase not cleaned by recovery")
	}
}

func TestLostEntriesAgeOutAfterRestarts(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	st := &persistedState{
		Lost: []LostEntry{{ID: "mr-gone", Branch: "b", Author: "cat-z", LostTime: time.Now()}},
	}
	if err := (&store{path: statePath}).save(st); err != nil {
		t.Fatal(err)
	}

	// Each New+Stop cycle is one pogod restart; the entry survives
	// lostMaxRestarts of them and then ages out.
	for i := 1; i <= lostMaxRestarts; i++ {
		r := newPersistent(t, statePath)
		if r.LostInfo("mr-gone") == nil {
			t.Fatalf("lost entry should survive restart %d", i)
		}
		r.Stop() // persists the incremented restart counter
	}
	r := newPersistent(t, statePath)
	if r.LostInfo("mr-gone") != nil {
		t.Errorf("lost entry should age out after %d restarts", lostMaxRestarts)
	}
}

func TestHandleMRLostReturns410(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	st := &persistedState{
		Lost: []LostEntry{{
			ID: "mr-gone", Branch: "polecat-x", Author: "cat-z",
			Reason: "branch not found on origin", LostTime: time.Now(),
		}},
	}
	if err := (&store{path: statePath}).save(st); err != nil {
		t.Fatal(err)
	}
	r := newPersistent(t, statePath)

	mux := http.NewServeMux()
	r.RegisterHandlers(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/refinery/mr/mr-gone")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410 Gone", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "lost" {
		t.Errorf(`body status = %q, want "lost"`, body["status"])
	}
	if body["branch"] != "polecat-x" || body["author"] != "cat-z" {
		t.Errorf("lost body missing resubmit context: %v", body)
	}
}

func TestHandleMRPrunedDistinctFromNotFound(t *testing.T) {
	r, err := New(Config{
		WorktreeDir:   t.TempDir(),
		StatePath:     filepath.Join(t.TempDir(), "refinery-state.json"),
		MaxHistoryLen: 1,
		MaxHistoryAge: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.mu.Lock()
	for _, id := range []string{"mr-1", "mr-2"} {
		mr := &MergeRequest{ID: id, Status: StatusMerged, DoneTime: now}
		r.history = append(r.history, mr)
		r.byID[id] = mr
	}
	r.mu.Unlock()
	r.pruneHistory() // MaxHistoryLen=1 prunes mr-1

	mux := http.NewServeMux()
	r.RegisterHandlers(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Pruned ID: still 404, but with a distinct message.
	resp, err := http.Get(srv.URL + "/refinery/mr/mr-1")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "pruned from history") {
		t.Errorf("pruned ID: status=%d body=%q, want 404 with pruned message", resp.StatusCode, body)
	}

	// Unknown ID: plain not found, no pruned wording.
	resp, err = http.Get(srv.URL + "/refinery/mr/mr-never-existed")
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || strings.Contains(body, "pruned") {
		t.Errorf("unknown ID: status=%d body=%q, want plain 404", resp.StatusCode, body)
	}

	// Pruned status survives a restart.
	r2 := newPersistent(t, r.cfg.StatePath)
	if !r2.WasPruned("mr-1") {
		t.Error("pruned ring not persisted across restart")
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// TestHeldItemsReplayedInOrder covers Decision 3 step 1: queued and held
// items replay in FIFO order after a restart, with held status preserved so
// they re-enter via the QA gate.
func TestHeldItemsReplayedInOrder(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "refinery-state.json")
	st := &persistedState{
		Queue: []*MergeRequest{
			{ID: "mr-1", Branch: "b1", Status: StatusQueued},
			{ID: "mr-2", Branch: "b2", Status: StatusHeld, Error: "held: QA item qa-1 not yet done"},
			{ID: "mr-3", Branch: "b3", Status: StatusQueued},
		},
	}
	if err := (&store{path: statePath}).save(st); err != nil {
		t.Fatal(err)
	}
	r := newPersistent(t, statePath)
	queue := r.Queue()
	if len(queue) != 3 {
		t.Fatalf("expected 3 queued items, got %d", len(queue))
	}
	for i, want := range []string{"mr-1", "mr-2", "mr-3"} {
		if queue[i].ID != want {
			t.Errorf("queue[%d] = %s, want %s", i, queue[i].ID, want)
		}
	}
	if queue[1].Status != StatusHeld {
		t.Errorf("held status not preserved: %s", queue[1].Status)
	}
}

// TestNoPersistenceWithoutStatePath keeps back-compat: an empty StatePath
// (unit tests, embedded use) never touches disk.
func TestNoPersistenceWithoutStatePath(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	r, err := New(Config{WorktreeDir: t.TempDir(), PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if r.store != nil {
		t.Fatal("store should be nil without StatePath")
	}
	if _, err := r.Submit(MergeRequest{RepoPath: originDir, Branch: "feature-1"}); err != nil {
		t.Fatal(err)
	}
	r.Stop() // must not panic on nil store
}
