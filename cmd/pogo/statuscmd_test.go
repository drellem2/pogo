package main

// Process-level tests for `pogo status --assignee` (mg-589d).
//
// These run the real binary against a stub pogod AND a stub `mg` placed at the
// front of PATH, so what is asserted is the frame an operator actually sees —
// including the three section headers, which is where the "this filter reached
// one section of three" statement lives. A unit test on the filter alone
// cannot see that, and a filter whose scope is invisible is the defect this
// ticket names.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mgFixture is what the stub `mg list` prints: two human items, one parked,
// one unassigned, spread over two status groups. ANSI is verbatim mg output.
const mgFixture = "available:\n" +
	"  mg-2a50    task     RED LINE BREACHED \x1b[2m[pogo, red-line]\x1b[0m \x1b[34mhuman\x1b[0m\n" +
	"  mg-0ffc    task     FOLLOW-UP from mg-4938 \x1b[2m[pogo, ops]\x1b[0m \x1b[2mparked\x1b[0m\n" +
	"claimed:\n" +
	"  mg-589d    task     pogo status takes an optional --assignee filter \x1b[2m[pogo, cli]\x1b[0m\n" +
	"  mg-7d62    task     DANIEL DECISION about GH_TOKEN \x1b[2m[pogo, ops]\x1b[0m \x1b[34mhuman\x1b[0m\n"

// stubMGEnv writes an `mg` that prints mgFixture regardless of its arguments,
// and returns the env entry that puts it first on PATH. Ignoring arguments is
// deliberate: it means a test would still see the full listing if the CLI ever
// started pushing --assignee down into mg, so the "filtering is a display
// concern" property is enforced rather than assumed.
func stubMGEnv(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(mgFixture) + "\n"
	path := filepath.Join(dir, "mg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub mg: %v", err)
	}
	return []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// stubPogod answers the three dashboard endpoints with fixed, non-empty data,
// so the agent and refinery sections are visibly populated while the work-item
// section is being narrowed.
func stubPogod(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body interface{}
		switch r.URL.Path {
		case "/agents":
			body = []map[string]interface{}{
				{"name": "mayor", "pid": 111, "type": "crew", "status": "running", "uptime": "3h"},
				{"name": "589d", "pid": 222, "type": "polecat", "status": "running", "uptime": "5m"},
			}
		case "/refinery/status":
			body = map[string]interface{}{"running": true, "enabled": true, "queue_len": 1, "history_len": 7}
		case "/refinery/queue":
			body = []map[string]interface{}{
				{"id": "mr-1", "branch": "polecat-589d", "status": "queued", "author": "mg-589d"},
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding %s: %v", r.URL.Path, err)
		}
	}
}

// workItemsSection returns just the work-item block of a rendered frame.
// Assertions about what the filter kept have to be scoped to it: the refinery
// section prints branch and author strings that legitimately contain work-item
// ids, so a whole-output `strings.Contains` reports a leak that is not one.
func workItemsSection(t *testing.T, stdout string) string {
	t.Helper()
	i := strings.Index(stdout, "=== Work Items")
	if i < 0 {
		t.Fatalf("no work-item section in output:\n%s", stdout)
	}
	rest := stdout[i:]
	if j := strings.Index(rest[1:], "\n==="); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}

// The headline case: only human-assigned items survive.
func TestStatusAssignee_FiltersWorkItems(t *testing.T) {
	stdout, stderr, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "status", "--assignee=human")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr)
	}
	items := workItemsSection(t, stdout)
	for _, id := range []string{"mg-2a50", "mg-7d62"} {
		if !strings.Contains(items, id) {
			t.Errorf("human-assigned %s missing from --assignee=human output:\n%s", id, items)
		}
	}
	for _, id := range []string{"mg-0ffc", "mg-589d"} {
		if strings.Contains(items, id) {
			t.Errorf("non-human %s survived --assignee=human:\n%s", id, items)
		}
	}
	if !strings.Contains(stdout, "=== Work Items (assignee=human) ===") {
		t.Errorf("work-item header does not name the active filter:\n%s", stdout)
	}
}

// The positive control the ticket asks for: a filter matching nothing must
// produce an empty section and say so. An implementation that ignored the flag
// entirely would print the full listing here and fail.
func TestStatusAssignee_NoMatchIsEmptyAndSaysSo(t *testing.T) {
	stdout, stderr, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "status", "--assignee=nobody")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr)
	}
	items := workItemsSection(t, stdout)
	for _, id := range []string{"mg-2a50", "mg-0ffc", "mg-589d", "mg-7d62"} {
		if strings.Contains(items, id) {
			t.Errorf("%s survived a filter that matches nothing — the flag is being ignored:\n%s", id, items)
		}
	}
	if !strings.Contains(items, "0 matching assignee=nobody") {
		t.Errorf("expected an explicit 0-matching line, got:\n%s", items)
	}
	if strings.Contains(items, "No work items.") {
		t.Errorf("an empty filter result must not read as an empty backlog:\n%s", items)
	}
	// The other two sections are still fully populated, which is exactly why
	// the output has to say the filter did not reach them.
	if !strings.Contains(stdout, "mayor") || !strings.Contains(stdout, "mr-1") {
		t.Errorf("agents and refinery must stay unfiltered:\n%s", stdout)
	}
}

// --assignee=none selects the items nobody has been given.
func TestStatusAssignee_None(t *testing.T) {
	stdout, stderr, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "status", "--assignee=none")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr)
	}
	items := workItemsSection(t, stdout)
	if !strings.Contains(items, "mg-589d") {
		t.Errorf("the unassigned item is missing from --assignee=none:\n%s", items)
	}
	for _, id := range []string{"mg-2a50", "mg-0ffc", "mg-7d62"} {
		if strings.Contains(items, id) {
			t.Errorf("assigned %s survived --assignee=none:\n%s", id, items)
		}
	}
}

// A filter that reaches one section of three must say which. Without this, an
// empty work-item list beside a running fleet is a display that lies by
// omission.
func TestStatusAssignee_DeclaresItsScope(t *testing.T) {
	stdout, _, _ := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "status", "--assignee=human")
	for _, want := range []string{
		"Filter: assignee=human",
		"work items only; agents and refinery",
		"=== Agents (unfiltered) ===",
		"=== Refinery (unfiltered) ===",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("scope statement %q missing from filtered output:\n%s", want, stdout)
		}
	}
}

// Without the flag, the frame is byte-identical to what it was before the flag
// existed — no banner, no "(unfiltered)" suffixes, nothing.
func TestStatus_UnfilteredOutputUnchanged(t *testing.T) {
	stdout, stderr, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "status")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr)
	}
	want := "=== Agents ===\n" +
		"  2 total (1 crew, 1 polecat), 2 running\n" +
		"  mayor                 crew      running     pid=111     uptime=3h\n" +
		"  589d                  polecat   running     pid=222     uptime=5m\n" +
		"\n" +
		"=== Work Items ===\n" +
		"  available:\n" +
		"  " + strings.Split(mgFixture, "\n")[1] + "\n" +
		"  " + strings.Split(mgFixture, "\n")[2] + "\n" +
		"  claimed:\n" +
		"  " + strings.Split(mgFixture, "\n")[4] + "\n" +
		"  " + strings.Split(mgFixture, "\n")[5] + "\n" +
		"\n" +
		"=== Refinery ===\n"
	if !strings.HasPrefix(stdout, want) {
		t.Errorf("unfiltered frame changed.\n got: %q\nwant prefix: %q", stdout, want)
	}
	for _, forbidden := range []string{"Filter:", "unfiltered", "matching assignee"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("unfiltered output leaked filter chrome %q:\n%s", forbidden, stdout)
		}
	}
}

// --json under a filter is still one object with every section present, and it
// carries the filter so a consumer need not have been told one was applied.
func TestStatusAssignee_JSONShape(t *testing.T) {
	stdout, stderr, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "--json", "status", "--assignee=human")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("filtered --json is not one object: %v\n%s", err, stdout)
	}
	for _, key := range []string{"filter", "agents", "work_items", "refinery", "refinery_queue"} {
		if _, ok := obj[key]; !ok {
			t.Errorf("key %q missing from filtered --json:\n%s", key, stdout)
		}
	}
	filter, ok := obj["filter"].(map[string]interface{})
	if !ok {
		t.Fatalf("filter is not an object: %#v", obj["filter"])
	}
	if filter["assignee"] != "human" {
		t.Errorf("filter.assignee = %#v, want \"human\"", filter["assignee"])
	}
	if filter["matched"] != float64(2) {
		t.Errorf("filter.matched = %#v, want 2", filter["matched"])
	}
	items, _ := obj["work_items"].(string)
	if !strings.Contains(items, "mg-2a50") || strings.Contains(items, "mg-0ffc") {
		t.Errorf("work_items was not filtered: %q", items)
	}
}

// The key that an empty section would have dropped: under a filter matching
// nothing, work_items is present and empty rather than absent.
func TestStatusAssignee_JSONKeepsEmptySection(t *testing.T) {
	stdout, _, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "--json", "status", "--assignee=nobody")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("not one object: %v\n%s", err, stdout)
	}
	v, ok := obj["work_items"]
	if !ok {
		t.Fatalf("work_items was dropped because it was empty:\n%s", stdout)
	}
	if v != "" {
		t.Errorf("work_items = %#v, want \"\"", v)
	}
	if _, ok := obj["agents"]; !ok {
		t.Errorf("agents dropped:\n%s", stdout)
	}
	filter := obj["filter"].(map[string]interface{})
	if filter["matched"] != float64(0) {
		t.Errorf("filter.matched = %#v, want 0", filter["matched"])
	}
}

// Unfiltered --json keeps today's shape exactly: no "filter" key.
func TestStatus_JSONUnfilteredHasNoFilterKey(t *testing.T) {
	stdout, _, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "--json", "status")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("not one object: %v\n%s", err, stdout)
	}
	if _, ok := obj["filter"]; ok {
		t.Errorf("unfiltered --json grew a \"filter\" key:\n%s", stdout)
	}
	items, _ := obj["work_items"].(string)
	if !strings.Contains(items, "mg-0ffc") {
		t.Errorf("unfiltered work_items lost an item: %q", items)
	}
}

// lockedBuffer collects a child's stdout while the test reads it concurrently.
// Writes come from the goroutine os/exec runs to drain the pipe, so the buffer
// cannot be read without a lock.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// decodeFrames reads the stream of concatenated JSON objects that
// `--live --json` emits. A trailing partial object simply ends the decode, so
// this is safe to call against a buffer that is still being written.
func decodeFrames(s string) []map[string]interface{} {
	dec := json.NewDecoder(strings.NewReader(s))
	var out []map[string]interface{}
	for {
		var obj map[string]interface{}
		if err := dec.Decode(&obj); err != nil {
			return out
		}
		out = append(out, obj)
	}
}

// liveFrameDeadline is how long the test will WAIT for the frames it needs. It
// is deliberately enormous next to the 50ms refresh interval, because it is a
// deadline and not a budget — see the comment on the test below.
const liveFrameDeadline = 60 * time.Second

// --live applies the filter on every refresh, not just the first frame. The
// process is ended with SIGINT, which is also how a human ends it, so the
// clean-exit path is exercised alongside the filtering.
//
// # It WAITS for the second frame; it does not budget a window for it (mg-6c90)
//
// This used to sleep 400ms and then require that at least 2 frames had landed
// at a 50ms interval — an 8x margin on a quiet box, and none at all on a busy
// one. It failed 3/3 on this branch and 3/3 on a clean origin/main worktree,
// same box, same minute: a fixed count of scheduler-driven events inside a
// fixed wall-clock window is a demand that the host schedule the child
// promptly, and a loaded host will not.
//
// It is the same defect as the absolute CPU floor in
// internal/refinery/queueview_test.go, in a different currency, and it comes
// under the same rule: an assertion over a shared resource must not require a
// minimum share of it inside a fixed window. Here the fix is to wait for the
// frames instead of counting what arrived, which costs nothing on a quiet box
// (the second frame lands in ~100ms) and simply takes longer on a loaded one.
//
// The test can still fail, and fails for the right reason. If the live loop
// renders once and stops — the actual defect this test guards, a filter applied
// to the first frame only — no amount of waiting produces a second frame and
// the deadline fires with the output attached.
func TestStatusAssignee_LiveAppliesPerRefresh(t *testing.T) {
	ts := httptest.NewServer(stubPogod(t))
	defer ts.Close()
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	cmd := exec.Command(pogoBin, "--json", "status", "--live", "--interval", "50ms", "--assignee=human")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("POGO_PORT=%d", port),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"POGO_HOME=",
	)
	cmd.Env = append(cmd.Env, stubMGEnv(t)...)
	out := &lockedBuffer{}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting live status: %v", err)
	}
	var waitOnce sync.Once
	wait := func() { waitOnce.Do(func() { _ = cmd.Wait() }) }
	defer func() {
		_ = cmd.Process.Kill()
		wait()
	}()

	// Two frames is the smallest number that can distinguish "the filter is
	// applied on every refresh" from "the filter is applied to the first
	// frame". Wait for them.
	const wantFrames = 2
	var frames []map[string]interface{}
	start := time.Now()
	deadline := start.Add(liveFrameDeadline)
	for {
		frames = decodeFrames(out.String())
		if len(frames) >= wantFrames {
			// Logged because it is the measurement that justifies waiting:
			// this is how much of the old 400ms budget the host actually
			// consumed on the run in front of you.
			t.Logf("%d live frames after %s (the old form allowed 400ms)", len(frames), time.Since(start).Round(time.Millisecond))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %d live frames at a 50ms refresh interval and saw %d — the live "+
				"loop is not refreshing (output %q)", liveFrameDeadline, wantFrames, len(frames), out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting live status: %v", err)
	}
	wait()

	// Re-read after the clean exit so every frame the process emitted is
	// asserted on, not merely the two that were waited for.
	frames = decodeFrames(out.String())
	if len(frames) < wantFrames {
		t.Fatalf("frames went backwards after SIGINT: %d (output %q)", len(frames), out.String())
	}
	for i, obj := range frames {
		items, _ := obj["work_items"].(string)
		if strings.Contains(items, "mg-0ffc") {
			t.Errorf("live frame %d was not filtered: %q", i+1, items)
		}
		if !strings.Contains(items, "mg-2a50") {
			t.Errorf("live frame %d lost a matching item: %q", i+1, items)
		}
		if _, ok := obj["filter"]; !ok {
			t.Errorf("live frame %d dropped the filter object", i+1)
		}
	}
}
