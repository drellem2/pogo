package config

import "testing"

// TestBlockedOn pins the shape's reader. IsDispatchGated only asks "does this
// gate?"; BlockedOn is the half that makes the shape worth having over a third
// sentinel — WHO the item is waiting on is readable back out of the field, so
// `mg list --assignee=blocked:mayor` is an answerable question in the same way
// mg-a3a2 made `--assignee=parked` one.
func TestBlockedOn(t *testing.T) {
	tests := []struct {
		name     string
		assignee string
		wantWho  string
		wantOK   bool
	}{
		{"names the agent", "blocked:mayor", "mayor", true},
		{"names a pm", "blocked:pm-pogo", "pm-pogo", true},
		{"names a person", "blocked:daniel", "daniel", true},

		// Normalization mirrors IsDispatchGated: prefix case-insensitive,
		// whitespace trimmed on both sides of the colon. The agent name keeps
		// its written casing — its consumers are messages and mg queries.
		{"prefix case is ignored", "BLOCKED:mayor", "mayor", true},
		{"padding is trimmed", "  blocked:  mayor  ", "mayor", true},
		{"agent casing is preserved", "blocked:Mayor", "Mayor", true},

		// A blocked shape naming nobody. It still reports as the shape (so it
		// still gates) with an empty agent, which is what lets a caller
		// complain about it specifically rather than treating it as an owner.
		{"bare prefix is the shape with no agent", "blocked:", "", true},
		{"prefix with only spaces after it", "blocked:   ", "", true},

		// Not the shape.
		{"unassigned is not the shape", "", "", false},
		{"an owner is not the shape", "mayor", "", false},
		{"a sentinel is not the shape", "human", "", false},
		{"the other sentinel is not the shape", "parked", "", false},
		{"the tag idiom is not the shape", "blocked-on-daniel", "", false},
		{"a bare word is not the shape", "blocked", "", false},
		{"a mid-string colon is not the shape", "pm:blocked:mayor", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			who, ok := BlockedOn(tt.assignee)
			if ok != tt.wantOK || who != tt.wantWho {
				t.Errorf("BlockedOn(%q) = (%q, %v), want (%q, %v)",
					tt.assignee, who, ok, tt.wantWho, tt.wantOK)
			}
		})
	}
}

// TestBlockedOnAgreesWithTheGate: the shape has two readers — the gate decides,
// BlockedOn explains — and a shape that gated without being readable, or was
// readable without gating, would be the two-answers-from-one-field defect
// mg-6fb0 exists to stop, reintroduced one level down.
func TestBlockedOnAgreesWithTheGate(t *testing.T) {
	for _, a := range []string{"blocked:mayor", "blocked:", "BLOCKED:x", " blocked: y "} {
		if _, ok := BlockedOn(a); !ok {
			t.Fatalf("BlockedOn(%q) does not read as the shape", a)
		}
		if !IsDispatchGated(a, nil) {
			t.Errorf("%q reads as the blocked shape but does not gate", a)
		}
	}
	for _, a := range []string{"mayor", "pm-pogo", "blocked-on-daniel", ""} {
		if _, ok := BlockedOn(a); ok {
			t.Errorf("%q must not read as the blocked shape", a)
		}
	}
}

// TestBlockIntentMismatch is the POSITIVE CONTROL for the advisory, and it is
// deliberately one test carrying both directions.
//
// mg-6fb0's requirement was explicit that a warning only ever observed
// not-firing has not been tested. So the firing cases and the legitimate-quiet
// cases ride in the same table and cannot be deleted apart. The quiet cases are
// the ones that decide whether this is usable at all: pm-template files every
// ticket with `--assignee=pm-<name>`, so a check that fired on a named assignee
// alone would fire on nearly the whole queue.
func TestBlockIntentMismatch(t *testing.T) {
	tests := []struct {
		name     string
		assignee string
		tags     []string
		gates    []string
		wantTag  string
		wantFire bool
		why      string
	}{
		// FIRES. mg-a96c is this shape verbatim, in the live store: it carries
		// `assignee: pm-pogo` with `blocked-on-daniel-confirm`. The author said
		// "blocked" in the only channel that could carry it, and the gate could
		// not hear it.
		{"agent owner plus a block tag fires", "pm-pogo",
			[]string{"pogo", "blocked-on-daniel-confirm"}, nil,
			"blocked-on-daniel-confirm", true,
			"the item declares a block and is still dispatchable — mg-a96c's exact shape"},
		{"the coordinator plus a block tag fires", "mayor",
			[]string{"blocked-on-daniel"}, nil,
			"blocked-on-daniel", true,
			"mg-bf5e set assignee=mayor meaning 'yours to route'; a block tag on top is the mismatch"},
		{"unassigned plus a block tag fires", "",
			[]string{"blocked-on-redeploy"}, nil,
			"blocked-on-redeploy", true,
			"unowned and declared-blocked is dispatchable too — mg-8c75's shape"},
		{"tag case is ignored", "pm-pogo",
			[]string{"Blocked-On-Daniel"}, nil,
			"Blocked-On-Daniel", true,
			"a hand-edited tag must not escape the check on capitalization"},

		// QUIET, and each for a different reason. These are what make the
		// advisory something other than noise.
		{"an ordinary owned item is quiet", "pm-pogo",
			[]string{"pogo", "cli"}, nil, "", false,
			"ownership is not a block; this is the overwhelming majority of the queue"},
		{"an ordinary unassigned item is quiet", "",
			[]string{"pogo"}, nil, "", false,
			"the case the fleet runs on"},
		{"no tags at all is quiet", "mayor", nil, nil, "", false,
			"nothing declared, nothing to contradict"},
		{"human plus a block tag is quiet", "human",
			[]string{"blocked-on-daniel"}, nil, "", false,
			"already gated — the tag and the gate agree, there is nothing to warn about"},
		{"parked plus a block tag is quiet", "parked",
			[]string{"blocked-on-mg-01f7"}, nil, "", false,
			"mg-0ffc's live shape: parked AND tagged. Warning here would train the reader to ignore it"},
		{"the new shape plus a block tag is quiet", "blocked:daniel",
			[]string{"blocked-on-daniel"}, nil, "", false,
			"the repair itself must not trip the interim check — this is what (1) introduces"},
		{"the new shape alone is quiet", "blocked:mayor", nil, nil, "", false,
			"gated by shape, declaring nothing contradictory"},
		{"a configured gate plus a block tag is quiet", "legal-review",
			[]string{"blocked-on-daniel"}, []string{"legal-review"}, "", false,
			"the check reads the caller's vocabulary, not a hardcoded one"},
		{"a value dropped from the vocabulary now fires", "parked",
			[]string{"blocked-on-daniel"}, []string{"human"}, "blocked-on-daniel", true,
			"a deployment that dropped 'parked' left this item dispatchable; that is worth saying"},

		// Near-misses on the tag, so the check is not matching loosely.
		{"a tag that merely mentions blocking is quiet", "pm-pogo",
			[]string{"unblocked", "blocking"}, nil, "", false,
			"the prefix is blocked-on-, not a substring search"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, fired := BlockIntentMismatch(tt.assignee, tt.tags, tt.gates)
			if fired != tt.wantFire || tag != tt.wantTag {
				t.Errorf("BlockIntentMismatch(%q, %v, %v) = (%q, %v), want (%q, %v)\nwhy: %s",
					tt.assignee, tt.tags, tt.gates, tag, fired, tt.wantTag, tt.wantFire, tt.why)
			}
		})
	}
}

// TestSuggestBlockedAssignee: the advice has to be right, not merely present.
// A `blocked-on-mg-1234` tag is waiting on another ITEM, and `mg new --depends`
// already expresses that — telling someone to write `assignee=blocked:mg-1234`
// would be pointing them at the wrong field, so it declines instead.
func TestSuggestBlockedAssignee(t *testing.T) {
	tests := []struct {
		tag  string
		want string
	}{
		{"blocked-on-daniel", "blocked:daniel"},
		{"blocked-on-daniel-confirm", "blocked:daniel-confirm"},
		{"blocked-on-mayor", "blocked:mayor"},
		{"Blocked-On-Daniel", "blocked:daniel"},
		{"blocked-on-redeploy", "blocked:redeploy"},

		// Declines: a work-item dependency, an empty tail, and a non-tag.
		{"blocked-on-mg-01f7", ""},
		{"blocked-on-MG-01F7", ""},
		{"blocked-on-", ""},
		{"blocked-on", ""},
		{"pogo", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := SuggestBlockedAssignee(tt.tag); got != tt.want {
				t.Errorf("SuggestBlockedAssignee(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

// TestSuggestedAssigneeActuallyGates closes the loop: every suggestion the
// advisory makes must be a value that gates once written. Advice that produced
// a still-dispatchable item would be worse than silence — the author would
// believe they had blocked it.
func TestSuggestBlockedAssigneeProducesAGatingValue(t *testing.T) {
	for _, tag := range []string{"blocked-on-daniel", "blocked-on-mayor", "blocked-on-pm-pogo", "Blocked-On-X"} {
		got := SuggestBlockedAssignee(tag)
		if got == "" {
			t.Fatalf("SuggestBlockedAssignee(%q) declined; expected a suggestion", tag)
		}
		if !IsDispatchGated(got, nil) {
			t.Errorf("suggested assignee %q (from tag %q) does not gate", got, tag)
		}
		if who, ok := BlockedOn(got); !ok || who == "" {
			t.Errorf("suggested assignee %q does not name an agent back", got)
		}
	}
}
