package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadHookPrompt(t *testing.T) {
	got, ok := readHookPrompt(strings.NewReader(`{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"he rest normally.\nWhen"}`), time.Second)
	if !ok || got != "he rest normally.\nWhen" {
		t.Errorf("payload: %q %v", got, ok)
	}
	for name, in := range map[string]string{"empty": "", "not json": "hello", "no prompt": `{"session_id":"s"}`} {
		if _, ok := readHookPrompt(strings.NewReader(in), time.Second); ok {
			t.Errorf("%s: want ok=false", name)
		}
	}
	// A stdin nobody closes must not hold the hook (and the agent's prompt).
	pr, pw := io.Pipe()
	defer pw.Close()
	start := time.Now()
	if _, ok := readHookPrompt(pr, 100*time.Millisecond); ok || time.Since(start) > 2*time.Second {
		t.Errorf("open stdin: ok=%v after %s", ok, time.Since(start))
	}
}
