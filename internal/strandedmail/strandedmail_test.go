package strandedmail

import (
	"errors"
	"strings"
	"testing"
)

// TestDetectFindsTheLiveOrphan reconstructs the exact case doctor's sweep found
// on 2026-08-05: agent wb468's mail-check was repointed to `wb468`, and the
// message already delivered to the work-item-derived box `b468` was left
// behind — an urgent correction to a builder mid-flight, now readable by nobody.
// Repointing alone produced this; the whole point of the sweep is that the
// repoint's residue is otherwise invisible.
func TestDetectFindsTheLiveOrphan(t *testing.T) {
	checks := []MailCheck{{Agent: "wb468", ScheduleID: "mail-check-mg-b468", Polled: []string{"wb468"}}}
	boxes := []Mailbox{
		{Name: "wb468", Unread: 0, Exists: true},
		{Name: "b468", Unread: 1, Exists: true},
	}
	msgs := map[string][]Message{
		"b468": {{ID: "b468/1785.1", From: "mayor", Subject: "CORRECTION: retracting the causal claim in your brief"}},
	}

	rep := Detect(checks, boxes, func(m string) ([]Message, error) { return msgs[m], nil })
	if !rep.Actionable() {
		t.Fatalf("sweep reported clean while b468 holds an unread correction: %+v", rep)
	}
	if len(rep.Findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(rep.Findings), rep.Findings)
	}
	f := rep.Findings[0]
	if f.Mailbox != "b468" || f.Agent != "wb468" || f.Polls != "wb468" {
		t.Errorf("finding = %+v, want mailbox=b468 agent=wb468 polls=wb468", f)
	}
	if f.Unread != 1 {
		t.Errorf("unread = %d, want 1", f.Unread)
	}
	if len(f.Messages) != 1 || f.Messages[0].From != "mayor" {
		t.Errorf("messages = %+v, want the mayor's correction named", f.Messages)
	}
	out := rep.Render()
	for _, want := range []string{"STRANDED MAIL", "b468", "wb468", "mg mail read b468/1785.1 --force", "mayor"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

// TestDetectStaysSilentOnHealthyFleets is the control the test above is
// worthless without. A sweep that fires on an empty abandoned box, or on a
// polecat whose name happens to equal its work item id, is a sweep that gets
// muted within a week — and the mail-check it was built to protect goes back to
// being unwatched.
func TestDetectStaysSilentOnHealthyFleets(t *testing.T) {
	tests := []struct {
		name   string
		checks []MailCheck
		boxes  []Mailbox
	}{
		{
			// The state every correctly-registered polecat is in: the schedule
			// is keyed on the work item, the mailbox is the agent name, and the
			// work-item box was never written to. THIS is the case the sweep
			// must never call stranded — it is the normal post-fix shape.
			name:   "shadow box never existed",
			checks: []MailCheck{{Agent: "waa96", ScheduleID: "mail-check-mg-aa96", Polled: []string{"waa96"}}},
			boxes:  []Mailbox{{Name: "waa96", Unread: 0, Exists: true}},
		},
		{
			name:   "shadow box exists but is genuinely empty",
			checks: []MailCheck{{Agent: "waa96", ScheduleID: "mail-check-mg-aa96", Polled: []string{"waa96"}}},
			boxes:  []Mailbox{{Name: "waa96", Unread: 2, Exists: true}, {Name: "aa96", Unread: 0, Exists: true}},
		},
		{
			// The historically-agreeing case: agent name == work item minus
			// "mg-", so the shadow IS the polled box. Nothing is abandoned.
			name:   "agent name equals the work item id",
			checks: []MailCheck{{Agent: "d2f0", ScheduleID: "mail-check-mg-d2f0", Polled: []string{"d2f0"}}},
			boxes:  []Mailbox{{Name: "d2f0", Unread: 3, Exists: true}},
		},
		{
			name:   "message names no mailbox, so the agent reads its own",
			checks: []MailCheck{{Agent: "doctor", ScheduleID: "mail-check-doctor"}},
			boxes:  []Mailbox{{Name: "doctor", Unread: 5, Exists: true}},
		},
		{
			// mg strips a leading "mg-", so these are one mailbox, not two.
			name:   "shadow differs from the polled box only by the mg- prefix",
			checks: []MailCheck{{Agent: "mg-aa96", ScheduleID: "mail-check-mg-aa96", Polled: []string{"aa96"}}},
			boxes:  []Mailbox{{Name: "aa96", Unread: 4, Exists: true}},
		},
		{
			// mg-4f8c's shape, and the one that matters most for the sweep's
			// credibility: the mail-check reads BOTH boxes deliberately, and
			// the work-item box has unread mail in it. That mail is NOT
			// stranded — the schedule opens it every ten minutes. A sweep that
			// took only the first polled box would flag this on every polecat
			// under the current template, and a report that cries wolf on the
			// healthy majority is a report nobody reads by the time the real
			// one arrives.
			name: "both boxes polled, and the work-item box has mail",
			checks: []MailCheck{{
				Agent:      "p4f8c",
				ScheduleID: "mail-check-mg-4f8c",
				Polled:     []string{"p4f8c", "mg-4f8c"},
			}},
			boxes: []Mailbox{{Name: "p4f8c", Unread: 1, Exists: true}, {Name: "4f8c", Unread: 3, Exists: true}},
		},
		{
			// Same, with the message naming the work-item box first. Order is
			// prose, not meaning.
			name: "both boxes polled, work-item box named first",
			checks: []MailCheck{{
				Agent:      "p4f8c",
				ScheduleID: "mail-check-mg-4f8c",
				Polled:     []string{"mg-4f8c", "p4f8c"},
			}},
			boxes: []Mailbox{{Name: "4f8c", Unread: 3, Exists: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := Detect(tc.checks, tc.boxes, func(string) ([]Message, error) {
				t.Error("messages were enumerated for a mailbox that should not have been flagged")
				return nil, nil
			})
			if rep.Actionable() {
				t.Fatalf("sweep flagged a healthy fleet: %+v", rep.Findings)
			}
			if !strings.Contains(rep.Render(), "No stranded mail") {
				t.Errorf("render should say the fleet is clean:\n%s", rep.Render())
			}
		})
	}
}

// TestDetectFindsTheWholeFleetsResidue replays the eight live mismatches with
// mail left behind in each abandoned box. It is the difference between "the
// repoint fixed the running fleet" and "the repoint fixed where they LOOK": all
// eight boxes still hold their mail afterwards, and the sweep is the only thing
// that can say so.
func TestDetectFindsTheWholeFleetsResidue(t *testing.T) {
	fleet := []struct{ agent, workItem string }{
		{"gd2f0", "mg-d2f0"}, {"wb468", "mg-b468"}, {"o9d7b", "mg-9d7b"}, {"wfc99", "mg-fc99"},
		{"wfc8d", "mg-fc8d"}, {"g109", "mg-b4cc"}, {"d0d70", "mg-0d70"}, {"gc23c", "mg-c23c"},
	}
	var checks []MailCheck
	var boxes []Mailbox
	for _, f := range fleet {
		checks = append(checks, MailCheck{Agent: f.agent, ScheduleID: "mail-check-" + f.workItem, Polled: []string{f.agent}})
		boxes = append(boxes,
			Mailbox{Name: f.agent, Unread: 0, Exists: true},
			Mailbox{Name: strings.TrimPrefix(f.workItem, "mg-"), Unread: 1, Exists: true},
		)
	}
	rep := Detect(checks, boxes, nil)
	if len(rep.Findings) != len(fleet) {
		t.Fatalf("findings = %d, want %d (one per abandoned box): %+v", len(rep.Findings), len(fleet), rep.Findings)
	}
	if rep.Checked != len(fleet) {
		t.Errorf("checked = %d, want %d", rep.Checked, len(fleet))
	}
}

// TestDetectReportsWhenMessagesCannotBeRead keeps a read failure from muting
// the finding. mg's unread count already proves mail is there; failing to open
// it is a second problem, not a reason to say the box is fine.
func TestDetectReportsWhenMessagesCannotBeRead(t *testing.T) {
	rep := Detect(
		[]MailCheck{{Agent: "wb468", ScheduleID: "mail-check-mg-b468", Polled: []string{"wb468"}}},
		[]Mailbox{{Name: "b468", Unread: 1, Exists: true}},
		func(string) ([]Message, error) { return nil, errors.New("mg exited 1") },
	)
	if !rep.Actionable() {
		t.Fatal("a mailbox whose messages could not be read was reported as clean")
	}
	if rep.Findings[0].ReadError == "" {
		t.Error("finding should carry the read error")
	}
	if !strings.Contains(rep.Render(), "could not enumerate") {
		t.Errorf("render should surface the read failure:\n%s", rep.Render())
	}
}

// TestEmptySweepIsNotAnAllClear guards the distinction the whole mg-aa96
// lineage turns on: "nothing is wrong" and "I could not look" must not render
// identically. A sweep with no schedules to judge has made no claim about the
// fleet, and saying "✓ no stranded mail" there would be this detector
// reproducing, inside itself, the failure it was built to catch.
func TestEmptySweepIsNotAnAllClear(t *testing.T) {
	rep := Detect(nil, []Mailbox{{Name: "waa96", Unread: 3, Exists: true}}, nil)
	if rep.Actionable() {
		t.Fatal("a sweep with no schedules should have no findings")
	}
	out := rep.Render()
	if strings.Contains(out, "✓") {
		t.Errorf("a sweep that judged nothing must not print an all-clear:\n%s", out)
	}
	if !strings.Contains(out, "nothing was checked") {
		t.Errorf("render should say nothing was checked:\n%s", out)
	}
}

// TestReadTokenIsWhatMgAccepts guards the recovery command itself. `mg mail
// list <box> --json` emits a BARE id, and `mg mail read` rejects it with
// "expected AGENT/MSG-ID format" — so the report's one actionable line has to
// compose the two. This was found by running the sweep against the live fleet
// and trying the command it printed; a report nobody can act on is only
// marginally better than the silence it replaced.
func TestReadTokenIsWhatMgAccepts(t *testing.T) {
	if got, want := ReadToken("b468", "1785951344970787000.49622.7000"), "b468/1785951344970787000.49622.7000"; got != want {
		t.Errorf("ReadToken = %q, want %q", got, want)
	}
	// An id that already carries its mailbox (the human-formatted `mg mail
	// list` output prints this shape) must not be prefixed twice.
	if got, want := ReadToken("b468", "b468/1785.1"), "b468/1785.1"; got != want {
		t.Errorf("ReadToken = %q, want %q", got, want)
	}
	// --force is load-bearing, not decoration: mg refuses a cross-box read
	// without it, and nobody reading this report IS the abandoned mailbox.
	if got, want := ReadCommand("b468", "1785.1"), "mg mail read b468/1785.1 --force"; got != want {
		t.Errorf("ReadCommand = %q, want %q", got, want)
	}
}
