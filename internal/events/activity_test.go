package events

import "testing"

// TestPogodsInterventionsAreNotAgentActivity is drellem2/pogo#138 at its root.
//
// Each type below is written to the log under the identity of the agent it was
// done TO, and each one fires only because that agent is stuck or failing. A
// liveness index that counts them reports the agent as freshly alive exactly
// when the intervention proves it was not — the detector's remediation
// destroying the detector's evidence.
func TestPogodsInterventionsAreNotAgentActivity(t *testing.T) {
	for _, tc := range []struct{ typ, why string }{
		{"modal_dismissed", "pogod cleared a modal that was eating the agent's input"},
		{"rating_dialog_dismissed", "the back-compat alias fireDismissal emits alongside modal_dismissed"},
		{"rate_limit_modal_dismissed", "the other back-compat alias, on the rate-limit path"},
		{"synthetic_failure_detected", "synthwatch records it under the FAILING agent's identity"},
	} {
		if CountsAsAgentActivity(tc.typ) {
			t.Errorf("%s counts as agent activity, but %s — so the reading gets FRESHER "+
				"precisely when the agent is worse off", tc.typ, tc.why)
		}
	}
}

// TestAgentLifecycleStillCountsAsActivity guards the other direction, which is
// the one an over-eager exclusion list would break.
//
// These are pogod-authored too, and excluding them on that basis would be the
// obvious generalisation and wrong: they are genuine transitions of the agent,
// and on this box a live agent's newest log line is very often its own
// agent_spawned. Dropping them would leave most identities absent from the
// index — which reports them as UNJUDGEABLE rather than as fresh. A different
// failure, not a fix.
func TestAgentLifecycleStillCountsAsActivity(t *testing.T) {
	for _, typ := range []string{
		"agent_spawned", "agent_stopped", "agent_restarted", "agent_crashed",
		"work_item_claimed", "mail_sent", "synthetic_failure_cleared",
	} {
		if !CountsAsAgentActivity(typ) {
			t.Errorf("%s stopped counting as agent activity; an identity whose only line is this "+
				"would drop out of every liveness index and read as unjudgeable", typ)
		}
	}
}

// TestUnknownEventTypesCountAsActivity pins the fail-open direction, which is
// the whole reason this is an exclusion list rather than an allow list.
//
// An allow list would age every agent whose newest line is an event type nobody
// remembered to enumerate — a detector inventing a wedge out of its own
// incompleteness. That is the same class of bug as the one being fixed, one
// level up, so the default must be "counts".
func TestUnknownEventTypesCountAsActivity(t *testing.T) {
	for _, typ := range []string{"", "some_event_type_invented_next_year", "work_item_done"} {
		if !CountsAsAgentActivity(typ) {
			t.Errorf("unknown event type %q failed CLOSED; the exclusion list must fail open, "+
				"or every event type added after this file was written silently ages its agent", typ)
		}
	}
}
