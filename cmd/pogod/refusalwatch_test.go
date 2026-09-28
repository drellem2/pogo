package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/refusalwatch"
)

// The consecutive-refusal alarm's mail sink goes where [agents] escalation_box
// points, not to a hard-coded `human` (drellem2/pogo#148), and the ledger sink
// stays alongside it. The zero-value config is the positive control that the
// default is still `human`.
func TestRefusalSinksFollowEscalationBox(t *testing.T) {
	for _, tc := range []struct{ box, want string }{
		{"", "human"},
		{"daniel-phone", "daniel-phone"},
	} {
		agents := config.AgentsConfig{EscalationBox: tc.box}
		sinks := refusalSinks(agents.EscalationBoxName())

		var mail *refusalwatch.MailSink
		ledger := false
		for _, s := range sinks {
			switch v := s.(type) {
			case refusalwatch.MailSink:
				mail = &v
			case refusalwatch.LedgerSink:
				ledger = true
			}
		}
		if mail == nil {
			t.Fatalf("escalation_box=%q: no mail sink in %v", tc.box, sinks)
		}
		if mail.To != tc.want || mail.Name() != "mg-mail:"+tc.want {
			t.Errorf("escalation_box=%q: mail sink To=%q Name=%q, want %q", tc.box, mail.To, mail.Name(), tc.want)
		}
		// A mistyped box must stay a loud refusal, never a minted mailbox.
		if mail.Create {
			t.Errorf("escalation_box=%q: mail sink passes --create against the live store", tc.box)
		}
		if !ledger {
			t.Errorf("escalation_box=%q: ledger sink dropped: %v", tc.box, sinks)
		}
	}
}
