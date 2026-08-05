package refinery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeScript drops an executable shell script into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestDefaultGatesRunNestedTestScriptOnce is the acceptance test for mg-da30,
// and it counts rather than inspects: a gate list is only worth what it runs,
// so the assertion is on how many times test.sh actually executed under a real
// runQualityGates, not on the strings the defaults returned.
//
// Against the previous defaults this fails with count 2 — build.sh's own call
// plus the gate list's second entry — which is the defect, stated as the one
// number a merge pays for it.
func TestDefaultGatesRunNestedTestScriptOnce(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	counter := filepath.Join(wtDir, "test-runs")

	writeScript(t, wtDir, "build.sh", "#!/bin/bash\nset -e\n./test.sh\necho built\n")
	writeScript(t, wtDir, "test.sh", "#!/bin/bash\necho ran >> "+counter+"\necho tested\n")

	mr := &MergeRequest{ID: "mr-nested", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	out, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err != nil {
		t.Fatalf("gates should pass: %v\n%s", err, out)
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("test.sh never ran at all — that is a coverage loss, not a saving: %v", err)
	}
	if n := len(strings.Fields(string(data))); n != 1 {
		t.Errorf("test.sh ran %d times, want exactly 1 (gates: %v)\n%s", n, ran, out)
	}

	// The suite must still have run — halving the cost is only correct if the
	// remaining half is the whole suite.
	if !strings.Contains(out, "tested") || !strings.Contains(out, "built") {
		t.Errorf("both the tests and the build must still be exercised, got:\n%s", out)
	}

	if len(ran) != 1 || ran[0] != "./build.sh" {
		t.Errorf("gates run = %v, want [./build.sh]", ran)
	}
	// The dropped gate is named in the merge's own output. A shorter gate list
	// with no explanation is indistinguishable from coverage going missing.
	if !strings.Contains(out, "omitting gate ./test.sh") {
		t.Errorf("the omission must be stated in the gate output, got:\n%s", out)
	}
}

// TestDefaultGatesKeepIndependentTestScript is the control, and it is the one
// that decides whether the change above is a saving or a coverage cut. FIVE of
// the seven repos on this fleet carrying both scripts have a build.sh that only
// compiles; a blanket "prefer ./build.sh" would stop testing every one of them.
func TestDefaultGatesKeepIndependentTestScript(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	counter := filepath.Join(wtDir, "test-runs")

	// A build.sh that compiles and does not test — the shape measured in
	// bridget, libdig, macguffin, pogo-sleepwake and rent-a-programmer-api.
	writeScript(t, wtDir, "build.sh", "#!/bin/bash\necho built\n")
	writeScript(t, wtDir, "test.sh", "#!/bin/bash\necho ran >> "+counter+"\necho tested\n")

	mr := &MergeRequest{ID: "mr-independent", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	out, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err != nil {
		t.Fatalf("gates should pass: %v\n%s", err, out)
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("test.sh must still run when build.sh does not run it: %v", err)
	}
	if n := len(strings.Fields(string(data))); n != 1 {
		t.Errorf("test.sh ran %d times, want exactly 1 (gates: %v)", n, ran)
	}
	if len(ran) != 2 {
		t.Errorf("both gates must run when they are independent, got %v", ran)
	}
	if strings.Contains(out, "omitting gate") {
		t.Errorf("nothing was omitted here; the output must not say so:\n%s", out)
	}
}

// TestDefaultGatesPresenceRules pins the cases that turn on which scripts exist
// at all, independent of any nesting.
func TestDefaultGatesPresenceRules(t *testing.T) {
	tests := []struct {
		name    string
		scripts map[string]string
		want    []string
	}{
		{
			name:    "neither script",
			scripts: map[string]string{},
			want:    nil,
		},
		{
			name:    "build.sh only",
			scripts: map[string]string{"build.sh": "#!/bin/bash\n./test.sh\n"},
			want:    []string{"./build.sh"},
		},
		{
			// The nesting rule must never fire here: with no build.sh there is
			// nothing running the tests but this gate.
			name:    "test.sh only",
			scripts: map[string]string{"test.sh": "#!/bin/bash\ngo test ./...\n"},
			want:    []string{"./test.sh"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.scripts {
				writeScript(t, dir, name, body)
			}
			got, note := defaultGates(dir)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("defaultGates = %v, want %v", got, tc.want)
			}
			if note != "" {
				t.Errorf("nothing was omitted, so there must be no note, got %q", note)
			}
		})
	}
}

// TestBuildScriptRunsTestsDetection pins the invocation forms, and — more
// importantly — the near misses. The two ways this check can be wrong are not
// equally costly: a missed invocation runs the suite twice, as before, while a
// phantom one removes a real gate. Every "want false" row below is a mention of
// test.sh that executes nothing.
func TestBuildScriptRunsTestsDetection(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"plain invocation", "#!/bin/bash\n./test.sh\n", true},
		{"guarded by exit", "#!/bin/bash\n./test.sh || exit 1\n", true},
		{"chained", "#!/bin/bash\n./fmt.sh && ./test.sh\n", true},
		{"indented inside a conditional", "#!/bin/bash\nif [ x = x ]; then\n  ./test.sh\nfi\n", true},
		{"via bash", "#!/bin/bash\nbash test.sh\n", true},
		{"via sh with path", "#!/bin/bash\nsh ./test.sh\n", true},
		{"in a subshell", "#!/bin/bash\n(./test.sh)\n", true},
		{"piped", "#!/bin/bash\n./test.sh | tee log\n", true},

		{"no mention at all", "#!/bin/bash\ngo build ./...\n", false},
		{"named in a trailing comment", "#!/bin/bash\ngo build ./... # run ./test.sh yourself\n", false},
		{"named in a whole-line comment", "#!/bin/bash\n# tests live in ./test.sh\ngo build ./...\n", false},
		{"only printed, not run", "#!/bin/bash\necho \"then run test.sh\"\n", false},
		{"a different script", "#!/bin/bash\n./integration_test.sh\n", false},
		{"a longer name with the same suffix", "#!/bin/bash\n./smoke-test.sh\n", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeScript(t, dir, "build.sh", tc.body)
			if got := buildScriptRunsTests(dir); got != tc.want {
				t.Errorf("buildScriptRunsTests(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestThisRepoBuildScriptIsDetected runs the check against the real build.sh in
// this repository rather than a fixture. The whole ticket rests on one concrete
// claim — that pogo's build.sh runs pogo's test.sh — and a fixture cannot pin
// it: the day someone removes that call, the fixtures above still pass while
// the defaults silently drop a gate that is no longer redundant.
func TestThisRepoBuildScriptIsDetected(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "build.sh")); err != nil {
		t.Skipf("no build.sh at %s: %v", root, err)
	}
	if !buildScriptRunsTests(root) {
		t.Errorf("%s/build.sh no longer invokes ./test.sh — the gate defaults now "+
			"drop ./test.sh for a build that does not run it", root)
	}

	gates, note := defaultGates(root)
	if len(gates) != 1 || gates[0] != "./build.sh" {
		t.Errorf("this repo's default gates = %v, want [./build.sh]", gates)
	}
	if !strings.Contains(note, "./test.sh") {
		t.Errorf("the note must name the gate it dropped, got %q", note)
	}
}
