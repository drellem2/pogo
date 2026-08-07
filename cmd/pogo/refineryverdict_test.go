package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSubmitVerdict(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(good, []byte("{\"verdict\": \"pass\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("neither flag yields no verdict", func(t *testing.T) {
		got, err := readSubmitVerdict("", "")
		if err != nil || got != nil {
			t.Errorf("got (%s, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("inline", func(t *testing.T) {
		got, err := readSubmitVerdict(`{"verdict":"pass"}`, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `{"verdict":"pass"}` {
			t.Errorf("verdict altered in transit: %s", got)
		}
	})

	t.Run("file, trailing newline trimmed", func(t *testing.T) {
		got, err := readSubmitVerdict("", good)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `{"verdict": "pass"}` {
			t.Errorf("got %s", got)
		}
	})

	// Both flags is a mistake with two plausible resolutions, so it is refused
	// rather than resolved by precedence: silently ignoring one of them would
	// submit a verdict the author did not write.
	t.Run("both flags refused", func(t *testing.T) {
		_, err := readSubmitVerdict(`{"a":1}`, good)
		if err == nil || !strings.Contains(err.Error(), "alternatives") {
			t.Errorf("got %v, want a refusal naming both flags", err)
		}
	})

	// A flag that resolved to nothing — an empty file, an unexpanded variable —
	// must not submit silently. That would be this ticket's failure mode with
	// the author believing it had recorded a verdict.
	t.Run("empty file refused", func(t *testing.T) {
		_, err := readSubmitVerdict("", empty)
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("got %v, want a refusal", err)
		}
	})

	t.Run("missing file refused", func(t *testing.T) {
		_, err := readSubmitVerdict("", filepath.Join(dir, "nope.json"))
		if err == nil || !strings.Contains(err.Error(), "verdict-file") {
			t.Errorf("got %v, want an error naming the flag", err)
		}
	})

	// Validated here as well as in the daemon: an unmarshalable verdict makes
	// json.Marshal of the whole SubmitRequest fail, so without this the author
	// is told about the transport instead of about its argument.
	t.Run("malformed refused locally", func(t *testing.T) {
		_, err := readSubmitVerdict(`{"verdict":`, "")
		if err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
			t.Errorf("got %v, want the shape rule", err)
		}
	})

	t.Run("empty object refused", func(t *testing.T) {
		_, err := readSubmitVerdict(`{}`, "")
		if err == nil || !strings.Contains(err.Error(), "records nothing") {
			t.Errorf("got %v, want a refusal", err)
		}
	})
}
