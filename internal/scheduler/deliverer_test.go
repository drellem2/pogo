package scheduler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestMailAfterNudge pins the fallback policy a scheduled fire depends on.
// pm-pogo's 09:00 sweep only ran because mail caught a nudge that wait-idle
// refused to deliver, so the fallback stays for every outcome where nothing
// received the message — and is withheld for the single outcome where
// something did.
func TestMailAfterNudge(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"confirmed delivery needs no mail", nil, false},
		{
			"queued mid-turn: the harness has it, a mail copy would duplicate it",
			fmt.Errorf("nudge to %q: %w", "pm-pogo", agent.ErrNudgeQueued),
			false,
		},
		{
			"refused after the full escalation: nobody received it",
			fmt.Errorf("nudge to %q: %w", "pm-pogo", agent.ErrNudgeUnconfirmed),
			true,
		},
		{
			"the pre-existing wait-idle refusal still falls back",
			errors.New(`wait for idle: agent "pm-pogo" still producing output after 30s`),
			true,
		},
		{"a PTY write error falls back", errors.New("write to PTY: file already closed"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mailAfterNudge(tt.err); got != tt.want {
				t.Fatalf("mailAfterNudge(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
