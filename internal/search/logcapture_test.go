package search

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/go-hclog"
)

// logCapture is a concurrency-safe io.Writer standing in for os.Stderr, so a
// test can assert on the level and shape of what the indexer actually emits.
// The index path writes from the walk goroutine and from the write shards, so
// a bare bytes.Buffer would race.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// captureLogs redirects bs's logger into a fresh capture at the given
// threshold. Call it before any indexing starts: it writes bs.logger, which
// the index goroutines read.
func captureLogs(t *testing.T, bs *BasicSearch, level hclog.Level) *logCapture {
	t.Helper()
	cap := &logCapture{}
	bs.logger = hclog.New(&hclog.LoggerOptions{
		Level:      level,
		Output:     cap,
		JSONFormat: true,
	})
	return cap
}

// records parses everything written so far as hclog JSON lines.
func (c *logCapture) records(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	raw := c.buf.String()
	c.mu.Unlock()

	var out []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("logger emitted a line that is not JSON: %q (%v)", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func (c *logCapture) reset() {
	c.mu.Lock()
	c.buf.Reset()
	c.mu.Unlock()
}

func recLevel(rec map[string]any) string {
	s, _ := rec["@level"].(string)
	return s
}

func recMessage(rec map[string]any) string {
	s, _ := rec["@message"].(string)
	return s
}

// findLogged returns the records whose message is exactly msg, restricted to
// level when level is non-empty.
//
// Exact, never a substring. These messages embed the project root, and a
// project root is a path a test can also appear inside — the first draft of
// TestReindexingLineCarriesRootAsAField matched three records because
// t.TempDir() names the directory after the test and the test's own name
// contains "Reindexing". More generally it is the af0f444 house rule: match
// the complete message so a new variant of one of these lines is counted as a
// new line rather than silently folded into an existing assertion.
func findLogged(recs []map[string]any, level, msg string) []map[string]any {
	var out []map[string]any
	for _, rec := range recs {
		if level != "" && recLevel(rec) != level {
			continue
		}
		if recMessage(rec) == msg {
			out = append(out, rec)
		}
	}
	return out
}

// requireLogged fails unless exactly the expected number of records match.
func requireLogged(t *testing.T, recs []map[string]any, level, msg string, want int) {
	t.Helper()
	got := findLogged(recs, level, msg)
	if len(got) == want {
		return
	}
	t.Errorf("want %d %q record(s) with message %q, got %d:\n%s",
		want, level, msg, len(got), formatRecords(recs))
}

// msgIndexed and msgNoContentChanges rebuild the two save-path messages
// verbatim, so the assertions compare complete text rather than a fragment.
func msgIndexed(fileCount int, root string) string {
	return "Indexed " + strconv.Itoa(fileCount) + " files for " + root
}

func msgNoContentChanges(root string) string {
	return "No content changes detected, skipping zoekt rebuild for " + root
}

func formatRecords(recs []map[string]any) string {
	var b strings.Builder
	for _, rec := range recs {
		enc, _ := json.Marshal(rec)
		b.Write(enc)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return "  (nothing logged)\n"
	}
	return b.String()
}
