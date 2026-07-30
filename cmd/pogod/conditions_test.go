package main

import (
	"strings"
	"testing"
)

// enumerationRow is one row of
// docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md §4
// Class A, and its disposition under mg-342d.
//
// THIS TABLE IS THE POINT OF THIS FILE. mg-c3f0's finding was that an
// enumeration dies inside a completed work item; the second-order version is an
// enumeration that survives as prose while the code drifts out from under it. So
// every row A1..A15 is listed here with wired or declined, and a change that
// silently drops one has to edit this table to compile — which is the only kind
// of guard that survives the people who wrote it. mg-9a04 deferred half its work
// to "a separate ticket" it never filed and shipped a false instruction for nine
// days; a declined row with no recorded reason is the same failure with fewer
// moving parts.
type enumerationRow struct {
	row      string
	wired    bool
	why      string
	conds    []pogodCondition // the conditions this row raises, for the routing pins below
	declined string
}

func enumerationDisposition() []enumerationRow {
	const to = "mayor"
	return []enumerationRow{
		{row: "A1", wired: true,
			why: "FIXED by mg-c3f0 in promptsyncnotify.go — this ticket carries the remainder"},

		{row: "A2", wired: true,
			why:   "scheduler load failed / scheduler disabled — mailed AND woken",
			conds: []pogodCondition{conditionSchedulerLoadFailed(to, "/p", "boom"), conditionSchedulerNoHome(to, "boom")}},

		{row: "A3", wired: true,
			why:   "ack-watch NOT armed. The listed deaf-watch site is UNREACHABLE in production (it is guarded on agentRegistry != nil, and pogod os.Exit(1)s if the registry fails to build), and the deaf-watch degradation that actually occurs under A2 is already on the spine as deaf_watch_error",
			conds: []pogodCondition{conditionAckWatchNotArmed(to)}},

		{row: "A4", wired: true,
			why:   "prompt refresh failed — every prompt stale, fleet-wide",
			conds: []pogodCondition{conditionPromptRefreshFailed(to, "boom")}},

		{row: "A5", wired: true,
			why: "auto-start failed. Solved when the failed agent is not the coordinator; when it IS, the mail waits in the coordinator's maildir and says so — see the declared residue in conditionAutoStartFailed",
			conds: []pogodCondition{
				conditionAutoStartFailed(to, "doctor", "boom", false),
				conditionAutoStartFailed(to, "mayor", "boom", true)}},

		{row: "A6", wired: true,
			why:   "restart failed — the respawn is one-shot, so the agent is gone",
			conds: []pogodCondition{conditionRestartFailed(to, "doctor", "boom")}},

		{row: "A7", wired: true,
			why:   "unknown provider — agents silently run on a different harness than configured",
			conds: []pogodCondition{conditionUnknownProvider(to, "cursor2", "claude", "config")}},

		{row: "A8", wired: false,
			declined: "NOT A FAULT, and Class C mislabelled as Class A. pogod does not degrade here: " +
				"it selects an alternate socket dir and logs that the sockets are 'still unique to " +
				"this POGO_HOME'. Nothing is lost, nothing is disabled, and there is no remedy an " +
				"agent can apply. It is also invariant for a given POGO_HOME, so annunciating it " +
				"would create a permanent standing alarm clearable only by moving POGO_HOME — the " +
				"firehose the investigation warns against, and the fastest way to get the whole " +
				"channel muted."},

		{row: "A9", wired: true,
			why: "git GC degraded — unbounded branch/worktree growth with no symptom until a disk fills",
			conds: []pogodCondition{
				conditionGitGCNoTicketIndex(to, "boom"),
				conditionGitGCNoWitness(to, "boom"),
				conditionGitGCOrphanScan(to, "boom"),
				conditionGitGCSweepFailed(to, "/repo", "boom")}},

		{row: "A10", wired: true,
			why:   "role-default pin failed — an unpinned role name can be renamed by an upgrade, and a name is also a mailbox",
			conds: []pogodCondition{conditionRolePinFailed(to, "boom")}},

		{row: "A11", wired: true,
			why:   "own-heartbeat write failed — pogod becomes undetectably alive to every external check",
			conds: []pogodCondition{conditionHeartbeatWriteFailed(to, "/p", "boom")}},

		{row: "A12", wired: false,
			declined: "ALWAYS-TRUE PRECONDITION on any host without the shim, so it would fire at " +
				"every boot forever and never clear — a standing alarm with no transition, which is " +
				"the one thing mg-c3f0's constraint forbids most directly. The fallback it degrades " +
				"to (heartbeat-only wake detection) is correct by design and is the documented " +
				"baseline, not a broken state; the code itself says 'hb alone is correct'. And there " +
				"is no remedy: an agent cannot install a platform shim. If the shim's ABSENCE ever " +
				"becomes a fault rather than a configuration, the right instrument is a wake-latency " +
				"measurement, not an annunciation of a boot-time capability check."},

		{row: "A13", wired: true,
			why:   "gh-issue teardown detector NOT armed. The one row NOT routed to the coordinator: [gh_teardown] notify_to is a deliberately-chosen mailbox for this subsystem's findings (mg-b586) and its not-armed condition belongs to the same reader",
			conds: []pogodCondition{conditionTeardownNotArmed("pm-pogo", "exec: gh not found")}},

		{row: "A14", wired: true,
			why:   "log rotation failed — the post-mortem log the other thirteen fall back to may be lost or unbounded",
			conds: []pogodCondition{conditionLogRotationFailed(to, "boom")}},

		{row: "A15", wired: true,
			why: "preserved/undetermined-worktree MAIL FAILURE — wired as a structured event " +
				"(worktree_notice_undelivered), not a mail. Mailing about a failed mail is a retry " +
				"dressed as an alarm and fails the same way; worktreecleanup.go previously emitted " +
				"no events at all, so the fix is to make the failure queryable rather than louder. " +
				"See emitWorktreeNoticeUndelivered."},
	}
}

// TestEnumerationIsFullyDisposed pins that all fifteen rows are accounted for and
// that every declined row carries a recorded reason.
func TestEnumerationIsFullyDisposed(t *testing.T) {
	rows := enumerationDisposition()
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.row] {
			t.Errorf("row %s listed twice", r.row)
		}
		seen[r.row] = true
		if r.wired && r.why == "" {
			t.Errorf("row %s is wired with no stated reason", r.row)
		}
		if !r.wired && r.declined == "" {
			t.Errorf("row %s is declined with NO REASON. An enumeration that shrinks without a "+
				"reason is how the remainder gets lost a second time", r.row)
		}
		if r.wired && r.declined != "" {
			t.Errorf("row %s is both wired and declined", r.row)
		}
	}
	for i := 1; i <= 15; i++ {
		row := "A" + itoa(i)
		if !seen[row] {
			t.Errorf("enumeration row %s is not disposed of at all — it is neither wired nor "+
				"declined, which is exactly the state mg-342d exists to end", row)
		}
	}
	if len(rows) != 15 {
		t.Errorf("disposed %d rows, want 15", len(rows))
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// TestNoConditionEverRoutesToHuman is the load-bearing routing pin. mg-c3f0
// measured `human` at 988 unread against 0 for the coordinator and 0-10 for every
// standing crew mailbox, so routing a fleet condition there is not a weaker fix —
// it reproduces the defect while auditing as instrumented. That measurement is
// the reason the constraint exists and it is not a preference to be relaxed.
func TestNoConditionEverRoutesToHuman(t *testing.T) {
	for _, r := range enumerationDisposition() {
		for _, c := range r.conds {
			if c.To == "human" {
				t.Errorf("row %s condition %s routes to `human` (988 unread) — "+
					"that is a channel measured NOT to be read", r.row, c.ID)
			}
			if c.To == "" {
				t.Errorf("row %s condition %s has no addressee", r.row, c.ID)
			}
		}
	}
}

// TestEveryConditionSaysWhatItCostsAndWhatToDo. A notice the recipient cannot act
// on is a log line with extra steps. The frame is shared (conditionBody) so this
// checks that every constructor actually uses it and fills it in.
func TestEveryConditionSaysWhatItCostsAndWhatToDo(t *testing.T) {
	for _, r := range enumerationDisposition() {
		for _, c := range r.conds {
			if c.Row != r.row {
				t.Errorf("condition %s carries row %q but is listed under %q — the event's row "+
					"field is how the enumeration and the daemon get reconciled", c.ID, c.Row, r.row)
			}
			if c.Subject == "" || c.Body == "" {
				t.Fatalf("row %s condition %s has no subject or body", r.row, c.ID)
			}
			for _, want := range []string{"WHAT IT COSTS WHILE UNFIXED", "WHAT TO DO", "WHY THIS IS MAIL"} {
				if !strings.Contains(c.Body, want) {
					t.Errorf("row %s condition %s body is missing the %q section", r.row, c.ID, want)
				}
			}
			if !strings.Contains(c.Body, "row "+r.row+" of docs/investigations/") &&
				!strings.Contains(c.Body, "Row "+r.row+" of docs/investigations/") {
				t.Errorf("row %s condition %s does not cite the enumeration row it carries, so a "+
					"recipient cannot find out why they are being mailed", r.row, c.ID)
			}
			if !strings.Contains(c.Body, "mg-342d") {
				t.Errorf("row %s condition %s does not name the work item that added it", r.row, c.ID)
			}
		}
	}
}

// TestOnlyA2AsksForAWake. The wake is a privileged channel — it interrupts a
// running agent's turn — and it is justified by exactly one argument: mail cannot
// be READ when the mail-check loop is a schedule and the scheduler is down. Any
// other condition claiming it has not made that argument.
func TestOnlyA2AsksForAWake(t *testing.T) {
	for _, r := range enumerationDisposition() {
		for _, c := range r.conds {
			if c.Wake && r.row != "A2" {
				t.Errorf("row %s condition %s asks for a PTY wake. Only A2 has the argument for "+
					"one (its own fault is what stops mail from being read); interrupting an "+
					"agent's turn for anything else is the firehose", r.row, c.ID)
			}
			if !c.Wake && r.row == "A2" {
				t.Errorf("A2 condition %s does not ask for a wake. Mail alone reaches a maildir "+
					"nothing will prompt the recipient to open — that is precisely where mg-c3f0 "+
					"§6 stopped", c.ID)
			}
		}
	}
}

// TestConditionIDsAreDistinctPerSubject. Two subjects sharing a suppression key
// means the first one's alarm silences the second's, which is a silent and total
// failure of the mechanism.
func TestConditionIDsAreDistinctPerSubject(t *testing.T) {
	if a, b := conditionAutoStartFailed("mayor", "doctor", "e", false),
		conditionAutoStartFailed("mayor", "pm-pogo", "e", false); a.ID == b.ID {
		t.Errorf("two different failed agents share the id %q", a.ID)
	}
	if a, b := conditionRestartFailed("mayor", "doctor", "e"),
		conditionRestartFailed("mayor", "pa", "e"); a.ID == b.ID {
		t.Errorf("two different crashed agents share the id %q", a.ID)
	}
	if a, b := conditionGitGCSweepFailed("mayor", "/r1", "e"),
		conditionGitGCSweepFailed("mayor", "/r2", "e"); a.ID == b.ID {
		t.Errorf("two different repos share the id %q", a.ID)
	}
	if a, b := conditionUnknownProvider("mayor", "p1", "claude", "config"),
		conditionUnknownProvider("mayor", "p2", "claude", "config"); a.ID == b.ID {
		t.Errorf("two different bad provider ids share the id %q", a.ID)
	}
	// A2's two sites have the same consequence but different causes, and must not
	// suppress each other: fixing one does not fix the other.
	if a, b := conditionSchedulerLoadFailed("mayor", "/p", "e"),
		conditionSchedulerNoHome("mayor", "e"); a.ID == b.ID {
		t.Errorf("A2's two sites share the id %q", a.ID)
	}
	// Every wired id must be unique across the whole catalogue.
	seen := map[string]string{}
	for _, r := range enumerationDisposition() {
		for _, c := range r.conds {
			if other, dup := seen[c.ID]; dup {
				t.Errorf("id %q is used by both row %s and row %s", c.ID, other, r.row)
			}
			seen[c.ID] = r.row
		}
	}
}

// TestA5NamesTheCaseThatCannotBeDeliveredLive. The ticket asked for a conclusion
// on A5 rather than a hedge, and the conclusion is a statement the notice itself
// has to make: when the coordinator is the agent that failed to start, nobody
// read this at the time, and a notice that implied otherwise would be a false
// reassurance in the one case that matters.
func TestA5NamesTheCaseThatCannotBeDeliveredLive(t *testing.T) {
	self := conditionAutoStartFailed("mayor", "mayor", "spawn failed", true)
	other := conditionAutoStartFailed("mayor", "doctor", "spawn failed", false)

	if self.To != "mayor" || other.To != "mayor" {
		t.Fatalf("both cases must address the coordinator's mailbox; got %q / %q", self.To, other.To)
	}
	if self.Subject == other.Subject {
		t.Error("the coordinator-failed case reads identically to an ordinary crew failure")
	}
	if !strings.Contains(self.Body, "no in-fleet reader") {
		t.Error("the coordinator-failed notice does not say that nothing read it at the time; " +
			"an alarm that overstates its own delivery is the defect being fixed")
	}
	if !strings.Contains(strings.ToLower(self.Body), "out-of-process") {
		t.Error("the coordinator-failed notice does not name the residual gap, so a recipient " +
			"seeing it twice has no way to know the recurrence needs a different class of fix")
	}
	if strings.Contains(self.Body, "pogo agent start mayor") {
		t.Error("the coordinator-failed notice hands out a start command for an agent that is " +
			"by construction already running by the time anyone reads it")
	}
}
