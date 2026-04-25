package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Filter selects events for the list/tail commands.
//
// A zero Filter matches every event. SinceMin is exclusive on the lower bound
// (events strictly newer than SinceMin pass) so callers can compute it as
// `time.Now().Add(-d)` without worrying about edge equality. Type and Agent
// are exact-match strings; "" disables that dimension.
type Filter struct {
	SinceMin time.Time
	Type     string
	Agent    string
}

// Match reports whether ev passes the filter.
func (f Filter) Match(ev Event) bool {
	if f.Type != "" && ev.EventType != f.Type {
		return false
	}
	if f.Agent != "" && ev.Agent != f.Agent {
		return false
	}
	if !f.SinceMin.IsZero() {
		ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if err != nil {
			// Unparseable timestamps fail the time filter so they don't
			// silently sneak through. They still pass when SinceMin is unset.
			return false
		}
		if !ts.After(f.SinceMin) {
			return false
		}
	}
	return true
}

// ParseLine decodes one JSONL line into an Event.
func ParseLine(line []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// ReadFiltered scans path top-to-bottom and returns events that match f, in
// file order. Malformed lines are skipped (they're written to stderr so the
// caller can see corruption, but they don't abort the read — matching the
// log's append-only, best-effort spirit).
//
// If path does not exist, returns (nil, nil) to mean "no events yet".
func ReadFiltered(path string, f Filter) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out []Event
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		ev, perr := ParseLine(raw)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "events: skipping malformed line %d: %v\n", lineNum, perr)
			continue
		}
		if !f.Match(ev) {
			continue
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// FormatPretty renders ev as a single human-readable line:
//
//	TIMESTAMP  EVENT_TYPE  AGENT  [work_item_id=…]  [repo=…]  details…
//
// Timestamp is truncated to seconds; details is rendered as compact JSON,
// truncated past 200 chars so the line stays scannable.
func FormatPretty(ev Event) string {
	ts := ev.Timestamp
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		ts = t.UTC().Format("2006-01-02T15:04:05Z")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-20s  %-26s  %-22s", ts, truncate(ev.EventType, 26), truncate(ev.Agent, 22))
	if ev.WorkItemID != "" {
		fmt.Fprintf(&b, "  work_item=%s", ev.WorkItemID)
	}
	if ev.Repo != "" {
		fmt.Fprintf(&b, "  repo=%s", ev.Repo)
	}
	if summary := summarizeDetails(ev.Details); summary != "" {
		fmt.Fprintf(&b, "  %s", summary)
	}
	return b.String()
}

// summarizeDetails renders ev.Details as compact `key=value` pairs in sorted
// key order, falling back to compact JSON when a value is itself an object or
// array. Returns "" if there are no details to show.
func summarizeDetails(d map[string]any) string {
	if len(d) == 0 {
		return ""
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, valueStr(d[k])))
	}
	joined := strings.Join(parts, " ")
	return truncate(joined, 200)
}

func valueStr(v any) string {
	switch x := v.(type) {
	case string:
		// Keep short strings unquoted for readability; quote anything with
		// embedded whitespace so the field stays parseable by eye.
		if strings.ContainsAny(x, " \t\n=") {
			return strconvQuote(x)
		}
		return x
	case nil:
		return "null"
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprintf("%v", x)
		}
		return string(b)
	}
}

// strconvQuote returns a JSON-style quoted string (re-uses encoding/json so we
// don't import strconv just for this).
func strconvQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// Follow streams new lines appended to path to lineSink for the lifetime of
// stop.
//
// Implementation: open the file (creating it if missing so callers never race
// against the first writer), seek to end, then poll for new data on
// pollInterval, scanning whatever bytes have arrived. When EOF is reached the
// goroutine sleeps; when stop is closed it exits.
//
// A line is delivered to lineSink only after it's terminated by '\n' — partial
// trailing writes are buffered until they complete, so we never emit a
// half-line.
func Follow(path string, pollInterval time.Duration, stop <-chan struct{}, lineSink func(line []byte)) error {
	if pollInterval <= 0 {
		pollInterval = 200 * time.Millisecond
	}

	// O_CREATE lets us tail before the first writer has appeared. We open
	// for read+write only because some platforms balk at O_CREATE|O_RDONLY;
	// no actual writes happen here.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek end: %w", err)
	}

	reader := bufio.NewReader(f)
	var pending []byte

	timer := time.NewTimer(pollInterval)
	defer timer.Stop()

	for {
		// Drain whatever's available right now.
		for {
			chunk, err := reader.ReadBytes('\n')
			if len(chunk) > 0 {
				pending = append(pending, chunk...)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}
			// Got a complete line — strip the newline, deliver it.
			line := pending[:len(pending)-1]
			lineSink(line)
			pending = nil
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(pollInterval)

		select {
		case <-stop:
			return nil
		case <-timer.C:
			// continue polling
		}
	}
}
