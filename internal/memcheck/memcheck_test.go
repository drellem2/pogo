package memcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildIndex returns a MEMORY.md-shaped body whose estimated token cost is at
// least wantTokens, built from realistic index lines "- [Title N](fileN.md) —
// hook". The first line is deliberately the heaviest so tests can assert the
// fattest line is surfaced.
//
// It grows by TOKENS, not bytes, because tokens are what the threshold compares
// against; sizing a fixture in bytes is what made the original check untestable
// against real behaviour.
func buildIndex(wantTokens int) []byte {
	var b strings.Builder
	b.WriteString("# Memory index\n\n")
	// A deliberately fat first line — a hook that grew into a paragraph.
	b.WriteString("- [The one that grew into a paragraph](fat.md) — " +
		strings.Repeat("this hook kept accreting clauses and never got trimmed, ", 8) + "\n")
	i := 0
	for EstimateTokens([]byte(b.String())) < wantTokens {
		fmt.Fprintf(&b, "- [Memory %d](file-%d.md) — a reasonably sized one-line hook for entry %d\n", i, i, i)
		i++
	}
	return []byte(b.String())
}

// buildIndexChars is buildIndex sized against the CHARACTER budget instead of
// the token one. It exists because the two budgets are in different units and a
// fixture that is "small" in one can be over the cliff in the other — which is
// precisely the confusion mg-9a89 found in this package.
func buildIndexChars(wantChars int) []byte {
	var b strings.Builder
	b.WriteString("# Memory index\n\n")
	b.WriteString("- [The one that grew into a paragraph](fat.md) — " +
		strings.Repeat("this hook kept accreting clauses and never got trimmed, ", 8) + "\n")
	i := 0
	for b.Len() < wantChars {
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
	data := buildIndex(HarnessReadCapTokens + 1)
	if EstimateTokens(data) <= WarnThresholdTokens() {
		t.Fatalf("fixture too small: ~%d tokens, want > threshold %d", EstimateTokens(data), WarnThresholdTokens())
	}
	r := Check("MEMORY.md", data)
	if !r.Approaching {
		t.Fatalf("positive control FAILED: check did not fire on a ~%d-token index (threshold %d, cap %d) — a check that cannot fire is worthless",
			r.EstTokens, r.ThresholdTokens, r.CapTokens)
	}
	if len(r.FattestLines) == 0 {
		t.Fatalf("fired but named no fat lines; acceptance requires naming the longest index lines, not just a total")
	}
}

// TestNamesTheFattestLines: on firing, the longest index line must be surfaced
// first, so the fix has a concrete target.
func TestNamesTheFattestLines(t *testing.T) {
	data := buildIndex(HarnessReadCapTokens + 1)
	r := Check("MEMORY.md", data)
	if !r.Approaching {
		t.Fatal("expected firing")
	}
	// Longest-first ordering.
	for i := 1; i < len(r.FattestLines); i++ {
		if r.FattestLines[i-1].Tokens < r.FattestLines[i].Tokens {
			t.Fatalf("fattest lines not sorted heaviest-first: %d before %d", r.FattestLines[i-1].Tokens, r.FattestLines[i].Tokens)
		}
	}
	if !strings.Contains(r.FattestLines[0].Text, "grew into a paragraph") {
		t.Fatalf("the fattest line was not surfaced first; got %q", r.FattestLines[0].Text)
	}
}

// TestSilentUnderThreshold: a healthy index does not fire (checked AFTER the
// positive control, per the ordering the acceptance demands).
//
// "Healthy" now means under BOTH budgets. Sizing this fixture by tokens alone
// is what it used to do, and that made it a ~26000-character index — one the
// auto-inject path truncates — asserted to be fine. The fixture is sized
// against the binding (character) budget for that reason.
func TestSilentUnderThreshold(t *testing.T) {
	// Comfortably under both warn thresholds.
	data := buildIndexChars(AutoInjectWarnThresholdChars() / 2)
	if EstimateTokens(data) >= WarnThresholdTokens() {
		t.Fatalf("healthy fixture unexpectedly large: ~%d tokens", EstimateTokens(data))
	}
	if len(data) >= AutoInjectWarnThresholdChars() {
		t.Fatalf("healthy fixture unexpectedly long: %d chars", len(data))
	}
	r := Check("MEMORY.md", data)
	if r.Approaching {
		t.Fatalf("false positive: fired on a healthy index (~%d tokens vs threshold %d; %d chars vs threshold %d)",
			r.EstTokens, r.ThresholdTokens, r.Chars, r.ThresholdChars)
	}
	if len(r.FattestLines) != 0 {
		t.Fatalf("healthy index should not name fat lines; got %d", len(r.FattestLines))
	}
}

// TestBoundaryAtThreshold: at exactly the TOKEN threshold we warn (>=), one
// token under we do not. Pins the comparison so an off-by-one can't silently
// flip it.
//
// The fixture is built from a body of single-token-per-line filler so the
// estimate lands on an exact, controllable count. It asserts on
// ApproachingRead specifically: this filler is also long enough to trip the
// character budget, and reading the combined Approaching flag here would let a
// broken token comparison pass on the strength of the other budget firing.
func TestBoundaryAtThreshold(t *testing.T) {
	th := WarnThresholdTokens()
	// "x\n" costs tokensPerAlphaChar + tokensPerLinePrefix per line; find the
	// smallest body whose estimate reaches exactly the threshold.
	grow := func(target int) []byte {
		var b strings.Builder
		for EstimateTokens([]byte(b.String())) < target {
			b.WriteString("word\n")
		}
		return []byte(b.String())
	}
	at := grow(th)
	if got := EstimateTokens(at); got < th {
		t.Fatalf("fixture construction failed: %d < %d", got, th)
	}
	if !Check("m", at).ApproachingRead {
		t.Fatalf("at/over the threshold (%d tokens) the read check must fire", th)
	}
	// Trim back until strictly under the threshold; it must go silent.
	body := string(at)
	for EstimateTokens([]byte(body)) >= th {
		idx := strings.LastIndex(strings.TrimSuffix(body, "\n"), "\n")
		if idx < 0 {
			break
		}
		body = body[:idx+1]
	}
	if Check("m", []byte(body)).ApproachingRead {
		t.Fatalf("under the threshold (~%d tokens < %d) the read check must stay silent", EstimateTokens([]byte(body)), th)
	}
}

// TestBoundaryAtAutoInjectThreshold is the same pin for the CHARACTER budget:
// at the threshold it fires, one character under it does not. The character
// count is exact, so this boundary is exact — there is no estimator slop to
// leave room for.
func TestBoundaryAtAutoInjectThreshold(t *testing.T) {
	th := AutoInjectWarnThresholdChars()
	at := []byte(strings.Repeat("a", th))
	if r := Check("m", at); !r.ApproachingAutoInject {
		t.Fatalf("at the threshold (%d chars) the auto-inject check must fire; got chars=%d threshold=%d", th, r.Chars, r.ThresholdChars)
	}
	under := []byte(strings.Repeat("a", th-1))
	if r := Check("m", under); r.ApproachingAutoInject {
		t.Fatalf("one char under the threshold (%d < %d) the auto-inject check must stay silent", r.Chars, r.ThresholdChars)
	}
}

// TestCharsCountedAsCharactersNotBytes: the auto-inject budget is denominated
// in CHARACTERS, so multi-byte content must not be charged by its byte length.
// An em dash is one character and three bytes; a check that counted bytes would
// fire ~3x early on the em-dash-separated hooks every real index is full of —
// the same class of false alarm mg-b938 removed from the token path.
func TestCharsCountedAsCharactersNotBytes(t *testing.T) {
	body := []byte(strings.Repeat("—", AutoInjectWarnThresholdChars()-1))
	r := Check("m", body)
	if r.Chars != AutoInjectWarnThresholdChars()-1 {
		t.Fatalf("Chars must count characters: got %d, want %d", r.Chars, AutoInjectWarnThresholdChars()-1)
	}
	if r.SizeBytes <= r.Chars {
		t.Fatalf("fixture is not multi-byte (%d bytes for %d chars) — it cannot discriminate the units", r.SizeBytes, r.Chars)
	}
	if r.ApproachingAutoInject {
		t.Fatalf("false alarm: %d chars is under the %d-char threshold, but the check fired — it is counting bytes (%d), not characters",
			r.Chars, r.ThresholdChars, r.SizeBytes)
	}
}

// TestThresholdTracksTheLimit is the acceptance's "threshold tracks the limit":
// the warn point is DERIVED from the cap, so if the cap constant changes the
// warn point moves with it. We can't reassign a const at runtime, so we verify
// the derivation formula holds for a range of hypothetical caps — both sides of
// a changed limit.
func TestThresholdTracksTheLimit(t *testing.T) {
	// The live derivation must equal cap * fraction.
	if got, want := WarnThresholdTokens(), int(float64(HarnessReadCapTokens)*WarnFraction); got != want {
		t.Fatalf("live threshold %d != derived %d — threshold is not tracking the cap", got, want)
	}
	// Both sides of a changed limit: a smaller cap yields a smaller warn point,
	// a larger cap a larger one. Same body, different cap => different verdict.
	tokens := 21000 // between 80% of 25000 (=20000) and 80% of 30000 (=24000)
	lowerCap := 25000
	higherCap := 30000
	if !(tokens >= int(float64(lowerCap)*WarnFraction)) {
		t.Fatalf("with cap %d, a %d-token file should be over the warn point", lowerCap, tokens)
	}
	if tokens >= int(float64(higherCap)*WarnFraction) {
		t.Fatalf("with cap %d, a %d-token file should be UNDER the warn point — the warn point failed to track the raised limit", higherCap, tokens)
	}
}

// TestCheckFileDoesNotModify proves the report-only guarantee: CheckFile reads
// the file and never writes it — content, size, and mtime are unchanged even
// when the file is over threshold and the check fires.
func TestCheckFileDoesNotModify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")
	orig := buildIndex(HarnessReadCapTokens + 1)
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

// seed creates path with a MEMORY.md-shaped body and returns it.
func seed(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Memory index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func has(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// TestLocateGlobsCallerSuppliedAndPogoRoots: a caller-supplied harness glob and
// pogo's own agent-memory root are both globbed. The harness glob here is
// Claude's, matching production.
func TestLocateGlobsCallerSuppliedAndPogoRoots(t *testing.T) {
	home := t.TempDir()
	claudeGlob := ".claude/projects/*/memory/MEMORY.md"
	claudeMem := seed(t, filepath.Join(home, ".claude", "projects", "-x", "memory", "MEMORY.md"))
	pogoMem := seed(t, filepath.Join(home, ".pogo", "agents", "pm", "pm-x", "memory", "MEMORY.md"))

	got := Locate(home, []string{claudeGlob})
	if !has(got, claudeMem) {
		t.Errorf("Locate missed the caller-supplied harness index %q; got %v", claudeMem, got)
	}
	if !has(got, pogoMem) {
		t.Errorf("Locate missed the pogo agent memory index %q; got %v", pogoMem, got)
	}
}

// TestPositiveControl_NonClaudeHarnessIsCovered is the harness-neutrality
// positive control, and it is the point of the whole change: memcheck must
// cover a harness that is NOT Claude. Before the fix this was impossible —
// the only harness root was a ~/.claude literal, so a non-Claude harness's
// index could not be found however it was configured. Demonstrating the Claude
// path still works would NOT have tested this requirement.
func TestPositiveControl_NonClaudeHarnessIsCovered(t *testing.T) {
	home := t.TempDir()
	// A hypothetical non-Claude harness with its own dotdir and layout.
	otherGlob := ".otherharness/workspaces/*/mem/MEMORY.md"
	otherMem := seed(t, filepath.Join(home, ".otherharness", "workspaces", "w1", "mem", "MEMORY.md"))
	// A Claude index also present, to prove coverage is additive rather than
	// one-harness-at-a-time.
	claudeMem := seed(t, filepath.Join(home, ".claude", "projects", "-x", "memory", "MEMORY.md"))

	got := Locate(home, []string{".claude/projects/*/memory/MEMORY.md", otherGlob})
	if !has(got, otherMem) {
		t.Fatalf("harness-neutrality FAILED: the non-Claude harness index %q was not located; got %v", otherMem, got)
	}
	if !has(got, claudeMem) {
		t.Errorf("adding a second harness dropped the Claude index %q; got %v", claudeMem, got)
	}
}

// TestLocateNoHarnessGlobsSkipsClaudePath: on an install whose providers
// declare no memory root, Locate must not glob ~/.claude at all. This is the
// negative half of the neutrality requirement — the old literal fired here.
func TestLocateNoHarnessGlobsSkipsClaudePath(t *testing.T) {
	home := t.TempDir()
	claudeMem := seed(t, filepath.Join(home, ".claude", "projects", "-x", "memory", "MEMORY.md"))
	pogoMem := seed(t, filepath.Join(home, ".pogo", "agents", "pm", "pm-x", "memory", "MEMORY.md"))

	got := Locate(home, nil)
	if has(got, claudeMem) {
		t.Fatalf("Locate globbed the Claude path with no harness glob supplied — the hard-coded root is still there; got %v", got)
	}
	if !has(got, pogoMem) {
		t.Errorf("pogo's own agent-memory root is harness-independent and must always be globbed; got %v", got)
	}
}

// TestLocateDeduplicates: overlapping provider globs must not yield the same
// path twice, or doctor would warn about one file twice.
func TestLocateDeduplicates(t *testing.T) {
	home := t.TempDir()
	g := ".claude/projects/*/memory/MEMORY.md"
	seed(t, filepath.Join(home, ".claude", "projects", "-x", "memory", "MEMORY.md"))
	got := Locate(home, []string{g, g})
	if len(got) != 1 {
		t.Fatalf("expected 1 de-duplicated path, got %d: %v", len(got), got)
	}
}

func TestLocateEmptyHomeNoError(t *testing.T) {
	if got := Locate(t.TempDir(), []string{".claude/projects/*/memory/MEMORY.md"}); len(got) != 0 {
		t.Fatalf("expected no matches under an empty home, got %v", got)
	}
}
