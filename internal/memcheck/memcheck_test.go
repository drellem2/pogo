package memcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildIndex returns a MEMORY.md-shaped body whose total size is at least
// wantBytes, built from realistic index lines "- [Title N](fileN.md) — hook".
// The first line is deliberately the longest so tests can assert the fattest
// line is surfaced.
func buildIndex(wantBytes int) []byte {
	var b strings.Builder
	b.WriteString("# Memory index\n\n")
	// A deliberately fat first line — a hook that grew into a paragraph.
	b.WriteString("- [The one that grew into a paragraph](fat.md) — " +
		strings.Repeat("this hook kept accreting clauses and never got trimmed, ", 8) + "\n")
	i := 0
	for b.Len() < wantBytes {
		fmt.Fprintf(&b, "- [Memory %d](file-%d.md) — a reasonably sized one-line hook for entry %d\n", i, i, i)
		i++
	}
	return []byte(b.String())
}

// TestPositiveControl_FiresOverThreshold is the required positive control: a
// size check is trivial to write so that it can NEVER fire, so we prove it CAN
// fire on an over-threshold fixture BEFORE trusting its silence on a healthy
// one. See [[a-check-needs-a-positive-control]].
func TestPositiveControl_FiresOverThreshold(t *testing.T) {
	// One byte past the cap: unambiguously over the warn threshold.
	data := buildIndex(HarnessReadCapBytes + 1)
	if len(data) <= WarnThresholdBytes() {
		t.Fatalf("fixture too small: %d bytes, want > threshold %d", len(data), WarnThresholdBytes())
	}
	r := Check("MEMORY.md", data)
	if !r.Approaching {
		t.Fatalf("positive control FAILED: check did not fire on a %d-byte index (threshold %d, cap %d) — a check that cannot fire is worthless",
			r.SizeBytes, r.ThresholdBytes, r.CapBytes)
	}
	if len(r.FattestLines) == 0 {
		t.Fatalf("fired but named no fat lines; acceptance requires naming the longest index lines, not just a total")
	}
}

// TestNamesTheFattestLines: on firing, the longest index line must be surfaced
// first, so the fix has a concrete target.
func TestNamesTheFattestLines(t *testing.T) {
	data := buildIndex(HarnessReadCapBytes + 1)
	r := Check("MEMORY.md", data)
	if !r.Approaching {
		t.Fatal("expected firing")
	}
	// Longest-first ordering.
	for i := 1; i < len(r.FattestLines); i++ {
		if r.FattestLines[i-1].Bytes < r.FattestLines[i].Bytes {
			t.Fatalf("fattest lines not sorted longest-first: %d before %d", r.FattestLines[i-1].Bytes, r.FattestLines[i].Bytes)
		}
	}
	if !strings.Contains(r.FattestLines[0].Text, "grew into a paragraph") {
		t.Fatalf("the fattest line was not surfaced first; got %q", r.FattestLines[0].Text)
	}
}

// TestSilentUnderThreshold: a healthy index does not fire (checked AFTER the
// positive control, per the ordering the acceptance demands).
func TestSilentUnderThreshold(t *testing.T) {
	// Comfortably under the warn threshold.
	data := buildIndex(WarnThresholdBytes() / 2)
	if len(data) >= WarnThresholdBytes() {
		t.Fatalf("healthy fixture unexpectedly large: %d bytes", len(data))
	}
	r := Check("MEMORY.md", data)
	if r.Approaching {
		t.Fatalf("false positive: fired on a healthy %d-byte index (threshold %d)", r.SizeBytes, r.ThresholdBytes)
	}
	if len(r.FattestLines) != 0 {
		t.Fatalf("healthy index should not name fat lines; got %d", len(r.FattestLines))
	}
}

// TestBoundaryAtThreshold: at exactly the threshold we warn (>=), one byte under
// we do not. Pins the comparison so an off-by-one can't silently flip it.
func TestBoundaryAtThreshold(t *testing.T) {
	th := WarnThresholdBytes()
	at := Check("m", make([]byte, th))
	if !at.Approaching {
		t.Fatalf("at exactly the threshold (%d) the check must fire", th)
	}
	under := Check("m", make([]byte, th-1))
	if under.Approaching {
		t.Fatalf("one byte under the threshold (%d) the check must stay silent", th-1)
	}
}

// TestThresholdTracksTheLimit is the acceptance's "threshold tracks the limit":
// the warn point is DERIVED from the cap, so if the cap constant changes the
// warn point moves with it. We can't reassign a const at runtime, so we verify
// the derivation formula holds for a range of hypothetical caps — both sides of
// a changed limit.
func TestThresholdTracksTheLimit(t *testing.T) {
	// The live derivation must equal cap * fraction.
	if got, want := WarnThresholdBytes(), int(float64(HarnessReadCapBytes)*WarnFraction); got != want {
		t.Fatalf("live threshold %d != derived %d — threshold is not tracking the cap", got, want)
	}
	// Both sides of a changed limit: a smaller cap yields a smaller warn point,
	// a larger cap a larger one. Same body, different cap => different verdict.
	body := make([]byte, 21000) // between 80% of 25000 (=20000) and 80% of 30000 (=24000)
	lowerCap := 25000
	higherCap := 30000
	if !(len(body) >= int(float64(lowerCap)*WarnFraction)) {
		t.Fatalf("with cap %d, a %d-byte file should be over the warn point", lowerCap, len(body))
	}
	if len(body) >= int(float64(higherCap)*WarnFraction) {
		t.Fatalf("with cap %d, a %d-byte file should be UNDER the warn point — the warn point failed to track the raised limit", higherCap, len(body))
	}
}

// TestCheckFileDoesNotModify proves the report-only guarantee: CheckFile reads
// the file and never writes it — content, size, and mtime are unchanged even
// when the file is over threshold and the check fires.
func TestCheckFileDoesNotModify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")
	orig := buildIndex(HarnessReadCapBytes + 1)
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	r, err := CheckFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Approaching {
		t.Fatal("expected the over-threshold fixture to fire")
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("CheckFile changed file size: %d -> %d", before.Size(), after.Size())
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("CheckFile changed mtime: %v -> %v", before.ModTime(), after.ModTime())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(orig) {
		t.Fatal("CheckFile changed file contents")
	}
}

func TestLocateGlobsBothRoots(t *testing.T) {
	home := t.TempDir()
	claudeMem := filepath.Join(home, ".claude", "projects", "-x", "memory", "MEMORY.md")
	pogoMem := filepath.Join(home, ".pogo", "agents", "pm", "pm-x", "memory", "MEMORY.md")
	for _, p := range []string{claudeMem, pogoMem} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# Memory index\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Locate(home)
	found := map[string]bool{}
	for _, g := range got {
		found[g] = true
	}
	if !found[claudeMem] {
		t.Errorf("Locate missed the Claude auto-memory index %q; got %v", claudeMem, got)
	}
	if !found[pogoMem] {
		t.Errorf("Locate missed the pogo agent memory index %q; got %v", pogoMem, got)
	}
}

func TestLocateEmptyHomeNoError(t *testing.T) {
	if got := Locate(t.TempDir()); len(got) != 0 {
		t.Fatalf("expected no matches under an empty home, got %v", got)
	}
}
