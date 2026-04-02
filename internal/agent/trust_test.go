package agent

import (
	"testing"
)

func TestTrustDialogMarker(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "plain text trust dialog",
			input: "Quick safety check: Is this a project you created or one you trust?",
			want:  true,
		},
		{
			name:  "trust dialog with ANSI escapes",
			input: "\x1b[1mQuick \x1b[0msafety check\x1b[32m: Is this a project...",
			want:  true,
		},
		{
			name:  "safety check substring",
			input: "Running safety check now",
			want:  true,
		},
		{
			name:  "no match - normal output",
			input: "Hello, I am Claude. How can I help you today?",
			want:  false,
		},
		{
			name:  "empty output",
			input: "",
			want:  false,
		},
		{
			name:  "ansi only",
			input: "\x1b[2J\x1b[H",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean := StripANSI([]byte(tt.input))
			got := trustDialogMarker.Match(clean)
			if got != tt.want {
				t.Errorf("trustDialogMarker.Match(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestOutputContains(t *testing.T) {
	a := &Agent{
		outputBuf: NewRingBuffer(1024),
	}

	// Write some output with ANSI escapes
	a.outputBuf.Write([]byte("\x1b[1mQuick \x1b[0msafety check\x1b[0m: trust?"))

	if !a.OutputContains([]byte("safety check"), 1024) {
		t.Error("OutputContains should find 'safety check' in ANSI-escaped output")
	}

	if a.OutputContains([]byte("nonexistent"), 1024) {
		t.Error("OutputContains should not find 'nonexistent'")
	}
}
