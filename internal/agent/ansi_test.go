package agent

import "testing"

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no escapes",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "SGR color codes",
			input: "\x1b[31mred text\x1b[0m",
			want:  "red text",
		},
		{
			name:  "cursor movement",
			input: "\x1b[2J\x1b[Hhello",
			want:  "hello",
		},
		{
			name:  "bold and underline",
			input: "\x1b[1m\x1b[4mbold underline\x1b[0m",
			want:  "bold underline",
		},
		{
			name:  "256 color",
			input: "\x1b[38;5;196mhot red\x1b[0m",
			want:  "hot red",
		},
		{
			name:  "OSC title sequence BEL",
			input: "\x1b]0;window title\x07some text",
			want:  "some text",
		},
		{
			name:  "OSC title sequence ST",
			input: "\x1b]0;window title\x1b\\some text",
			want:  "some text",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "only escapes",
			input: "\x1b[31m\x1b[0m",
			want:  "",
		},
		{
			name:  "mixed content",
			input: "line1\n\x1b[32mgreen\x1b[0m\nline3",
			want:  "line1\ngreen\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(StripANSI([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
