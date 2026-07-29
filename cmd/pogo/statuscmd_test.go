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

// --live applies the filter on every refresh, not just the first frame. The
// process is ended with SIGINT, which is also how a human ends it, so the
// clean-exit path is exercised alongside the filtering.
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
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting live status: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting live status: %v", err)
	}
	_ = cmd.Wait()

	// json.Decoder reads a stream of concatenated objects, which is exactly
	// what --live --json emits.
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	frames := 0
	for {
		var obj map[string]interface{}
		if err := dec.Decode(&obj); err != nil {
			break
		}
		frames++
		items, _ := obj["work_items"].(string)
		if strings.Contains(items, "mg-0ffc") {
			t.Errorf("live frame %d was not filtered: %q", frames, items)
		}
		if !strings.Contains(items, "mg-2a50") {
			t.Errorf("live frame %d lost a matching item: %q", frames, items)
		}
		if _, ok := obj["filter"]; !ok {
			t.Errorf("live frame %d dropped the filter object", frames)
		}
	}
	if frames < 2 {
		t.Errorf("expected at least 2 live frames in 400ms at 50ms, got %d (output %q)", frames, out.String())
	}
}
