package agent

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DetectorWindowBytes is wedgewatch.OutputScanBytes, restated as a literal
// because internal/wedgewatch imports this package and a test in `package
// agent` cannot import it back.
//
// It is EXPORTED so it is not a second, drifting copy: the external test
// package next door (api_output_ext_test.go, `package agent_test`) can name
// both this and the real constant, and fails if they ever disagree.
const DetectorWindowBytes = 16 * 1024

// outputTestRegistry returns a registry holding a single agent whose PTY ring
// contains exactly the given bytes.
//
// The agent is constructed rather than spawned: this endpoint reads only the
// output ring, and a real `cat` under a PTY writes its own bytes into that ring
// at times the test does not control — which is precisely what a byte-exact
// window assertion cannot tolerate.
func outputTestRegistry(t *testing.T, name string, out []byte) *Registry {
	t.Helper()
	a := &Agent{
		Name:      name,
		Type:      TypePolecat,
		outputBuf: NewRingBuffer(OutputRingBytes),
	}
	a.outputBuf.Write(out)
	return &Registry{agents: map[string]*Agent{name: a}}
}

func getOutput(t *testing.T, reg *Registry, name, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/agents/" + name + "/output"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("name", name)
	rr := httptest.NewRecorder()
	reg.handleOutput(rr, req)
	return rr
}

// repeatingOutput builds n bytes of output whose every position is identifiable,
// so a returned window can be located within the whole.
func repeatingOutput(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// TestHandleOutput_DefaultWindowUnchanged pins the pre-mg-8a56 behaviour for
// callers that name no window: they still get the last 4KB, so adding the
// params cannot move anything under them.
func TestHandleOutput_DefaultWindowUnchanged(t *testing.T) {
	full := repeatingOutput(OutputRingBytes)
	reg := outputTestRegistry(t, "def", full)

	rr := getOutput(t, reg, "def", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Body.Len(); got != DefaultOutputBytes {
		t.Fatalf("default window = %d bytes, want %d", got, DefaultOutputBytes)
	}
	if !bytes.Equal(rr.Body.Bytes(), full[len(full)-DefaultOutputBytes:]) {
		t.Error("default window is not the TAIL of the ring")
	}
}

// TestHandleOutput_BytesReachesTheDetectorsWindow is the control this ticket
// exists for: the window pogod's wedge detector judges an agent on must be
// retrievable by a caller outside pogod.
//
// Before the fix, n was hardcoded to 4096 and ?bytes= was accepted, ignored,
// and answered 200 — so the largest window any external caller could obtain was
// a quarter of what wedgewatch.OutputScanBytes actually scans, and nobody
// debugging a PTY-reading detector could see its input.
func TestHandleOutput_BytesReachesTheDetectorsWindow(t *testing.T) {
	full := repeatingOutput(OutputRingBytes)
	reg := outputTestRegistry(t, "scan", full)

	rr := getOutput(t, reg, "scan", "bytes=16384")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.Len(); got != DetectorWindowBytes {
		t.Fatalf("?bytes=16384 returned %d bytes, want %d (the detector's window)",
			got, DetectorWindowBytes)
	}
	if !bytes.Equal(rr.Body.Bytes(), full[len(full)-DetectorWindowBytes:]) {
		t.Error("?bytes= window is not the tail of the ring")
	}
	// The regression this replaces: the two windows must not be the same size.
	if DetectorWindowBytes == DefaultOutputBytes {
		t.Fatal("this test proves nothing if the detector's window equals the default")
	}
}

// TestHandleOutput_BytesClampsToRing verifies an over-large ?bytes= is answered
// with everything retained rather than refused, and that the clamp is the ring
// capacity — the honest ceiling, since nothing beyond it exists to return.
func TestHandleOutput_BytesClampsToRing(t *testing.T) {
	full := repeatingOutput(OutputRingBytes)
	reg := outputTestRegistry(t, "clamp", full)

	rr := getOutput(t, reg, "clamp", "bytes=99999999")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.Len(); got != OutputRingBytes {
		t.Fatalf("clamped window = %d bytes, want %d (OutputRingBytes)", got, OutputRingBytes)
	}
}

// TestHandleOutput_BytesShorterThanRetained covers the small-window case: a
// caller asking for less than is retained gets exactly that much.
func TestHandleOutput_BytesShorterThanRetained(t *testing.T) {
	reg := outputTestRegistry(t, "small", []byte("0123456789"))

	rr := getOutput(t, reg, "small", "bytes=4")

	if got := rr.Body.String(); got != "6789" {
		t.Fatalf("?bytes=4 = %q, want %q", got, "6789")
	}
}

func TestHandleOutput_Lines(t *testing.T) {
	reg := outputTestRegistry(t, "lines", []byte("one\ntwo\nthree\nfour\n"))

	tests := []struct {
		query string
		want  string
	}{
		{"lines=1", "four\n"},
		{"lines=2", "three\nfour\n"},
		{"lines=4", "one\ntwo\nthree\nfour\n"},
		// More lines than exist is the whole ring, not an error.
		{"lines=99", "one\ntwo\nthree\nfour\n"},
	}
	for _, tc := range tests {
		rr := getOutput(t, reg, "lines", tc.query)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body=%s", tc.query, rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// TestHandleOutput_LinesUnterminatedTail covers output that does not end in a
// newline — the normal state of a live PTY, whose last line is still being
// written.
func TestHandleOutput_LinesUnterminatedTail(t *testing.T) {
	reg := outputTestRegistry(t, "partial", []byte("one\ntwo\nthree"))

	if got := getOutput(t, reg, "partial", "lines=1").Body.String(); got != "three" {
		t.Errorf("lines=1 = %q, want %q", got, "three")
	}
	if got := getOutput(t, reg, "partial", "lines=2").Body.String(); got != "two\nthree" {
		t.Errorf("lines=2 = %q, want %q", got, "two\nthree")
	}
}

// TestHandleOutput_LinesReadsTheWholeRing verifies ?lines= is resolved against
// everything retained rather than against the 4KB default — a line-oriented
// caller would otherwise inherit the ceiling this ticket is about.
func TestHandleOutput_LinesReadsTheWholeRing(t *testing.T) {
	// One line per 100 bytes, filling most of the ring. The oldest lines are
	// far outside the 4KB default window.
	var buf bytes.Buffer
	for i := 0; buf.Len() < OutputRingBytes-200; i++ {
		buf.WriteString(strings.Repeat("x", 90))
		buf.WriteString("\n")
	}
	body := buf.Bytes()
	reg := outputTestRegistry(t, "manylines", body)

	rr := getOutput(t, reg, "manylines", "lines=500")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() <= DefaultOutputBytes {
		t.Fatalf("?lines=500 returned %d bytes; a whole-ring line window must exceed the %d-byte default",
			rr.Body.Len(), DefaultOutputBytes)
	}
	if got := bytes.Count(rr.Body.Bytes(), []byte("\n")); got != 500 {
		t.Errorf("?lines=500 returned %d newlines, want 500", got)
	}
}

// TestHandleOutput_RejectsUnusableParams is the anti-regression for the shape
// of the original defect: a param the handler cannot honour must fail loudly.
// Accepted-and-ignored is what made this bug invisible for its whole life, and
// accepted-and-guessed would keep it invisible.
func TestHandleOutput_RejectsUnusableParams(t *testing.T) {
	reg := outputTestRegistry(t, "bad", []byte("some output\n"))

	for _, query := range []string{
		"bytes=16384&lines=10", // ambiguous
		"bytes=abc",
		"bytes=0",
		"bytes=-1",
		"bytes=",
		"lines=abc",
		"lines=0",
		"lines=-5",
		"lines=",
	} {
		rr := getOutput(t, reg, "bad", query)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body=%q)", query, rr.Code, rr.Body.String())
		}
	}
}

// TestHandleOutput_PlainComposesWithBytes verifies the pre-existing ?plain=
// param still applies, and applies to the selected window rather than to the
// old fixed one.
func TestHandleOutput_PlainComposesWithBytes(t *testing.T) {
	reg := outputTestRegistry(t, "ansi", []byte("\x1b[31mred\x1b[0m\nplain\n"))

	rr := getOutput(t, reg, "ansi", "bytes=16384&plain=true")

	if got := rr.Body.String(); got != "red\nplain\n" {
		t.Errorf("plain+bytes = %q, want %q", got, "red\nplain\n")
	}
}

func TestHandleOutput_UnknownAgentStill404(t *testing.T) {
	reg := outputTestRegistry(t, "known", []byte("hi"))

	if rr := getOutput(t, reg, "nobody", "bytes=16384"); rr.Code != http.StatusNotFound {
		t.Errorf("unknown agent: status = %d, want 404", rr.Code)
	}
}

func TestLastLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 3, ""},
		{"no newline at all", "single line", 2, "single line"},
		{"exact count", "a\nb\n", 2, "a\nb\n"},
		{"fewer lines than asked", "a\nb\n", 9, "a\nb\n"},
		{"blank lines count", "a\n\n\nb\n", 2, "\nb\n"},
		{"leading partial line is kept when everything is asked for", "artial\nb\n", 9, "artial\nb\n"},
		{"trailing newline only", "\n", 1, "\n"},
	}
	for _, tc := range tests {
		if got := string(lastLines([]byte(tc.in), tc.n)); got != tc.want {
			t.Errorf("%s: lastLines(%q, %d) = %q, want %q", tc.name, tc.in, tc.n, got, tc.want)
		}
	}
}
