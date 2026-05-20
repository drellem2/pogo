package codex

import "testing"

func TestMatchesTrustDialog(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name: "trust dialog body with normal spacing",
			input: "Working with untrusted contents comes with higher risk of prompt " +
				"injection. Trusting the directory allows project-local config.",
			want: true,
		},
		{
			// Codex draws the dialog body glyph-by-glyph; once ANSI is
			// stripped the spaces are gone. This is the real on-PTY form.
			name:  "trust dialog body rendered glyph-by-glyph (no spaces)",
			input: "Doyoutrustthecontentsofthisdirectory?Workingwithuntrustedcontentscomeswith",
			want:  true,
		},
		{
			name:  "trusting-the-directory glyph-by-glyph",
			input: "Trustingthedirectoryallowsproject-localconfig,hooks,andexecpolicies",
			want:  true,
		},
		{
			name:  "trust dialog with ANSI escapes",
			input: "\x1b[1mTrusting\x1b[0mthe\x1b[32mdirectory\x1b[0m allows ...",
			want:  true,
		},
		{
			name:  "no match - normal composer output",
			input: "OpenAI Codex (v0.132.0)  model: gpt-5.5  permissions: YOLO mode",
			want:  false,
		},
		{
			name:  "no match - the word trust alone",
			input: "You can trust the explorer results without re-verifying them.",
			want:  false,
		},
		{
			name:  "empty output",
			input: "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesTrustDialog([]byte(tt.input))
			if got != tt.want {
				t.Errorf("matchesTrustDialog(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
