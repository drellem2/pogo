package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEventsChunk(t *testing.T, p string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func eventLine(typ string, at time.Time) string {
	return fmt.Sprintf(`{"schema_version":1,"timestamp":%q,"event_type":%q,"agent":"t","details":{}}`,
		at.UTC().Format(time.RFC3339Nano), typ)
}

// TestEventsListSinceReadsRotatedFiles is mg-50b9 end to end: the matches
// sit in events.log.1, and `pogo events list --since` must print them.
func TestEventsListSinceReadsRotatedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	now := time.Now()
	writeEventsChunk(t, path+".1",
		eventLine("investigation_search", now.Add(-48*time.Hour)),
		eventLine("investigation_search", now.Add(-47*time.Hour)))
	writeEventsChunk(t, path, eventLine("other", now.Add(-time.Hour)))

	stdout, stderr, code := runPogo(t, func(http.ResponseWriter, *http.Request) {},
		"events", "list", "--file", path, "--since", "72h", "--type", "investigation_search", "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if n := strings.Count(stdout, "investigation_search"); n != 2 {
		t.Errorf("want the 2 events from events.log.1, got %d:\n%s", n, stdout)
	}
}

// TestEventsListSinceBeforeRetainedHistorySaysSo: once rotation has discarded
// a chunk, a window reaching past the oldest retained record must not return
// a clean zero — it says where history starts and exits ExitUnknown.
func TestEventsListSinceBeforeRetainedHistorySaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	now := time.Now()
	oldest := now.Add(-100 * time.Hour).UTC()
	for i := 5; i >= 1; i-- {
		writeEventsChunk(t, fmt.Sprintf("%s.%d", path, i), eventLine("other", now.Add(-time.Duration(i*20)*time.Hour)))
	}
	writeEventsChunk(t, path, eventLine("other", now.Add(-time.Hour)))

	stdout, stderr, code := runPogo(t, func(http.ResponseWriter, *http.Request) {},
		"events", "list", "--file", path, "--since", "200h", "--type", "investigation_search")
	if code != 3 {
		t.Errorf("want exit 3 (could not establish), got %d", code)
	}
	if stdout != "" {
		t.Errorf("no matching events, stdout should be empty, got %q", stdout)
	}
	want := "window starts before retained history at " + oldest.Format(time.RFC3339)
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr must contain %q, got %q", want, stderr)
	}

	// Inside retained history: complete, clean, exit 0.
	_, stderr, code = runPogo(t, func(http.ResponseWriter, *http.Request) {},
		"events", "list", "--file", path, "--since", "50h", "--type", "investigation_search")
	if code != 0 || stderr != "" {
		t.Errorf("window inside retained history: want exit 0 and no warning, got %d %q", code, stderr)
	}
}
