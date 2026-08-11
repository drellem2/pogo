package turnlog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestOnlyTheAgentFacingCommandWritesTheTurnLog is the mechanical form of the
// rule that gives this artifact all of its value (mg-a270, ask 2):
//
//	It MUST be written BY THE AGENT as a consequence of finishing a turn. An
//	artifact pogod writes on the agent's behalf reintroduces the exact defect
//	this ticket exists to fix — pogod's own view was green for all 22h.
//
// The failure this guards against is not malice, it is helpfulness. The obvious
// "improvement" to make a week from now is for pogod to stamp a turnlog line
// when it observes an agent produce output, or when a nudge is acknowledged, or
// when a scheduler fire completes — each of which would look like tightening
// the signal and would in fact convert it back into another measurement of what
// the daemon did. During the 22-hour outage `scheduler_fire_delivered` logged
// 647 successful deliveries while every consuming turn died on an expired
// credential: the delivery was real, the turn was not, and a line written at
// delivery time would have read green throughout.
//
// So the writers are enumerated. `pogo turn-done` is a command an agent runs
// inside its own turn; nothing else may append.
func TestOnlyTheAgentFacingCommandWritesTheTurnLog(t *testing.T) {
	// Paths permitted to write a turnlog line, relative to the repo root.
	allowed := map[string]bool{
		filepath.Join("cmd", "pogo", "turndone.go"): true, // the agent-facing command
		filepath.Join("internal", "turnlog"):        true, // this package
	}
	offenders, err := scanForWriters(repoRoot(t), allowed)
	if err != nil {
		t.Fatalf("walking the repo: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("something other than the agent's own `pogo turn-done` writes the turn-completion "+
			"artifact:\n  %s\n\nA line written on an agent's behalf measures the writer, and the writer "+
			"is never the thing in doubt. If a daemon-side component needs to READ this artifact, that "+
			"is fine and is what turnlog.Scan is for — but nothing but the agent may append.",
			strings.Join(offenders, "\n  "))
	}
}

// TestTheWriterGuardCanFail is that guard's own positive control, and it is
// here for the reason the ticket gives for the check itself: a control that has
// never been observed failing is indistinguishable from one that cannot fail.
// A guard that silently stopped matching — a renamed helper, a walk that
// skipped a directory — would go on passing forever and read exactly like
// compliance.
func TestTheWriterGuardCanFail(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pogod := filepath.Join(root, "cmd", "pogod")
	if err := os.MkdirAll(pogod, 0755); err != nil {
		t.Fatal(err)
	}
	// A daemon-side "improvement": stamp the agent's turnlog when a nudge is
	// delivered. Plausible, helpful, and the whole defect.
	if err := os.WriteFile(filepath.Join(pogod, "nudge.go"),
		[]byte("package main\n\nfunc onDelivered(a string) { turnlog.Append(a, \"nudge delivered\", now()) }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	offenders, err := scanForWriters(root, map[string]bool{filepath.Join("internal", "turnlog"): true})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 1 || !strings.Contains(offenders[0], filepath.Join("cmd", "pogod", "nudge.go")) {
		t.Fatalf("THE GUARD DID NOT FIRE on a planted daemon-side writer. Until this arm fires, a "+
			"passing guard means nothing. offenders=%v", offenders)
	}
}

// scanForWriters reports non-test Go files under root, outside allowed, that
// append a turn-completion line. Shared by the real guard and its positive
// control so the control exercises the same code the guard runs.
func scanForWriters(root string, allowed map[string]bool) ([]string, error) {
	// Writing is these calls: Append/AppendIn, and FormatLine, which is the
	// only other way to produce a well-formed line.
	writers := []string{"turnlog.Append", "turnlog.FormatLine", "AppendIn(", "FormatLine("}

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "_testdata", "bin", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if allowed[rel] || allowed[filepath.Dir(rel)] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(data)
		for _, w := range writers {
			if strings.Contains(src, w) {
				offenders = append(offenders, rel+" contains "+w)
				break
			}
		}
		return nil
	})
	return offenders, err
}

// TestPogodDoesNotWriteTheTurnLog states the same rule about the one component
// it matters most for, so a failure names pogod rather than a generic path.
// pogod is the process whose every signal was green and truthful for 22 hours.
func TestPogodDoesNotWriteTheTurnLog(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "cmd", "pogod")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		if strings.Contains(src, "turnlog.Append") || strings.Contains(src, "turnlog.FormatLine") {
			t.Errorf("cmd/pogod/%s writes a turn-completion line. pogod writing this artifact "+
				"reproduces the exact defect it exists to fix: for the whole 22-hour outage every "+
				"pogod-side signal was green AND truthful, because they all describe pogod.", e.Name())
		}
	}
}

// repoRoot locates the module root from this test's own source path.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		t.Skipf("repo root not readable from %s (%v); this guard needs the source tree", abs, err)
	}
	return abs
}
