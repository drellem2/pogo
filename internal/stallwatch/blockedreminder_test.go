package stallwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// blockedCfg is a watcher config with the blocked-reminder on and both dispatch
// checks pushed far enough out that they cannot fire during these tests. That
// isolation is deliberate: every assertion below is about the reminder, and a
// stall nudge landing in the same recorder would make "one nudge to pm-pogo"
// pass for the wrong reason.
func blockedCfg() config.StallWatchConfig {
	return config.StallWatchConfig{
		Enabled:                   true,
		Agent:                     "mayor",
		UnclaimedItemAgeThreshold: 24 * time.Hour,
		UnreadMailAgeThreshold:    24 * time.Hour,
		MaxUnreadMailCount:        1000,
		NudgeCooldown:             time.Hour,
		RepeatBackoffCap:          4 * time.Hour,
		PriorityWakeEnabled:       false,
		BlockedReminderEnabled:    true,
		BlockedReminderCooldown:   time.Hour,
		BlockedReminderMaxNotices: config.DefaultBlockedReminderMaxNotices,
	}
}

// makeMailbox creates the maildir that marks an agent as one macguffin has
// corresponded with. hasMailbox tests exactly this, because macguffin's
// mail.Send has no roster — see checkBlockedReminders.
func makeMailbox(t *testing.T, mailRoot, agent string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(mailRoot, agent, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// blockedNudges returns the nudges whose message is a blocked-reminder.
func blockedNudges(rec *recorder) []nudge {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []nudge
	for _, n := range rec.nudges {
		if strings.HasPrefix(n.message, "blocked-reminder:") {
			out = append(out, n)
		}
	}
	return out
}

// TestBlockedReminderReachesTheNamedAgent is the whole point of mg-3844: the
// notice goes to the agent named in the assignee, NOT to the coordinator. Before
// this check, a `blocked:<agent>` item produced no signal to anyone at all.
func TestBlockedReminderReachesTheNamedAgent(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-e084", "blocked:pm-pogo", now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 blocked-reminder nudge, got %d: %+v", len(got), got)
	}
	if got[0].agent != "pm-pogo" {
		t.Errorf("reminder went to %q, want pm-pogo — a reminder delivered to the coordinator "+
			"is the dispatch nudge this check exists to be distinct from", got[0].agent)
	}
	if !strings.Contains(got[0].message, "mg-e084") {
		t.Errorf("message does not name the item: %q", got[0].message)
	}
	// The recipient is an agent that receives dispatch-shaped notices elsewhere,
	// so the message must rule that reading out explicitly.
	if !strings.Contains(got[0].message, "NOT a dispatch request") {
		t.Errorf("message does not disclaim dispatch: %q", got[0].message)
	}
}

// TestBlockedReminderFiresOnFirstSight pins the half mayor's mg-e084 instance
// showed matters most. The failure mode is a named agent who never LEARNED a
// decision was owed — not one who forgot — so the first notice must not wait out
// any age threshold. mg-e084 was filed and blocked in the same minute.
func TestBlockedReminderFiresOnFirstSight(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "architect")
	now := time.Now()
	// Zero age: written this instant.
	writeItem(t, workRoot, "mg-0001", "blocked:architect", now)

	w.Check(now)

	if got := blockedNudges(rec); len(got) != 1 {
		t.Fatalf("a just-blocked item drew %d reminders, want 1 immediately", len(got))
	}
}

// TestBlockedReminderIgnoresParkedAndHuman is mayor's caution against
// over-fixing, made executable. `parked` and `human` are gated too, and their
// silence is deliberate — mayor holds items on `human` precisely so they stop
// generating traffic. Treating all three gated assignees alike would convert an
// intentional silence into noise.
func TestBlockedReminderIgnoresParkedAndHuman(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "human")
	makeMailbox(t, mailRoot, "parked")
	now := time.Now()
	writeItem(t, workRoot, "mg-park", "parked", now)
	writeItem(t, workRoot, "mg-hum", "human", now)

	w.Check(now)

	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("parked/human drew %d blocked-reminders, want 0: %+v", len(got), got)
	}
}

// TestBlockedReminderIgnoresDispatchableItems: an item merely OWNED by an agent
// is not blocked on them. That distinction is the entire reason the `blocked:`
// shape exists (mg-6fb0) — pm-template files every ticket with
// `--assignee=pm-<name>`, so a check on any named assignee would fire on nearly
// the whole queue and mean nothing.
func TestBlockedReminderIgnoresDispatchableItems(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-owned", "pm-pogo", now)
	writeItem(t, workRoot, "mg-none", "", now)

	w.Check(now)

	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("owned/unassigned items drew %d blocked-reminders, want 0: %+v", len(got), got)
	}
}

// TestBlockedReminderUnreachableBlockerGoesToCoordinator covers the hazard that
// nearly sank this design. macguffin's mail.Send validates a recipient as a path
// component and has no roster, so mailing an unrecognised name silently CREATES
// that mailbox — the reminder would look sent and reach nobody, which is the
// exact disease. So an unreachable name is reported to the coordinator instead.
func TestBlockedReminderUnreachableBlockerGoesToCoordinator(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, blockedCfg())
	now := time.Now()
	// No mailbox created for "danell" — a typo'd blocker.
	writeItem(t, workRoot, "mg-typo", "blocked:danell", now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 nudge, got %d: %+v", len(got), got)
	}
	if got[0].agent != "mayor" {
		t.Errorf("unreachable-blocker notice went to %q, want the coordinator", got[0].agent)
	}
	if !strings.Contains(got[0].message, "danell") {
		t.Errorf("message does not name the unreachable blocker: %q", got[0].message)
	}
	// Its recipient IS the dispatcher, and every other work-item notice it gets
	// means "dispatch this". The text must say the opposite.
	if !strings.Contains(got[0].message, "NOT a dispatch request") {
		t.Errorf("message does not disclaim dispatch to the dispatcher: %q", got[0].message)
	}
}

// TestBlockedReminderBareBlockedGoesToCoordinator: `blocked:` with no agent
// still gates (config.BlockedAssigneePrefix explains why), so it is a hold that
// nothing can ever remind about. That is worth saying out loud rather than
// dropping silently.
func TestBlockedReminderBareBlockedGoesToCoordinator(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, blockedCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-bare", "blocked:", now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 nudge, got %d: %+v", len(got), got)
	}
	if got[0].agent != "mayor" {
		t.Errorf("bare-blocked notice went to %q, want the coordinator", got[0].agent)
	}
	if !strings.Contains(got[0].message, "names no agent") {
		t.Errorf("message does not explain the bare shape: %q", got[0].message)
	}
}

// TestBlockedReminderGroupsByRecipient: one message per agent, not one per item.
// Three items blocked on the same agent is one interruption.
func TestBlockedReminderGroupsByRecipient(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	makeMailbox(t, mailRoot, "architect")
	now := time.Now()
	writeItem(t, workRoot, "mg-0001", "blocked:pm-pogo", now)
	writeItem(t, workRoot, "mg-0002", "blocked:pm-pogo", now)
	writeItem(t, workRoot, "mg-0003", "blocked:architect", now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 2 {
		t.Fatalf("want 2 nudges (one per recipient), got %d: %+v", len(got), got)
	}
	byAgent := map[string]string{}
	for _, n := range got {
		byAgent[n.agent] = n.message
	}
	pm, ok := byAgent["pm-pogo"]
	if !ok {
		t.Fatalf("no nudge for pm-pogo: %+v", got)
	}
	if !strings.Contains(pm, "mg-0001") || !strings.Contains(pm, "mg-0002") {
		t.Errorf("pm-pogo's nudge does not carry both its items: %q", pm)
	}
	if strings.Contains(pm, "mg-0003") {
		t.Errorf("pm-pogo's nudge leaked another agent's item: %q", pm)
	}
}

// TestBlockedReminderStopsAtMaxNotices is the stop condition mayor asked for.
// RepeatBackoffCap bounds the RATE and never terminates; a hold left for a week
// would draw a notice every cap-interval forever, which is the mg-1693 shape on
// a new recipient — and worse here, because an agent waiting on purpose has no
// way to say "I know" short of clearing a block it is not ready to clear.
func TestBlockedReminderStopsAtMaxNotices(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderMaxNotices = 3
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	base := time.Now()
	writeItem(t, workRoot, "mg-hold", "blocked:pm-pogo", base)

	// Walk far past every backoff step, many more times than the cap allows.
	now := base
	for i := 0; i < 12; i++ {
		w.Check(now)
		now = now.Add(24 * time.Hour)
	}

	got := blockedNudges(rec)
	if len(got) != 3 {
		t.Fatalf("a permanently-blocked item drew %d notices, want exactly the cap of 3", len(got))
	}
	// The cap is a deliberate silence, and mg-1693's lesson is that a silence
	// nobody can count is indistinguishable from a detector that stopped working.
	if !hasCapEvent(rec, "mg-hold") {
		t.Error("no event records that the notice cap silenced mg-hold — the suppression must stay countable")
	}
}

// TestBlockedReminderCapCanBeDisabled: a negative cap means no cap. The `!= 0`
// merge test in config.Load exists so this value survives the layer merge
// instead of being silently replaced by the default.
func TestBlockedReminderCapCanBeDisabled(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderMaxNotices = -1
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	base := time.Now()
	writeItem(t, workRoot, "mg-hold", "blocked:pm-pogo", base)

	now := base
	for i := 0; i < 8; i++ {
		w.Check(now)
		now = now.Add(24 * time.Hour)
	}

	if got := blockedNudges(rec); len(got) != 8 {
		t.Fatalf("uncapped reminder drew %d notices over 8 well-separated checks, want 8", len(got))
	}
}

// TestBlockedReminderBacksOffBetweenNotices: the second notice waits a base
// cooldown. Without this the reminder would fire every heartbeat tick, which is
// the defect mg-1693 measured on the dispatch categories.
func TestBlockedReminderBacksOffBetweenNotices(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	base := time.Now()
	writeItem(t, workRoot, "mg-hold", "blocked:pm-pogo", base)

	w.Check(base)
	w.Check(base.Add(time.Minute))
	w.Check(base.Add(30 * time.Minute))
	if got := blockedNudges(rec); len(got) != 1 {
		t.Fatalf("inside the 1h base cooldown the reminder fired %d times, want 1", len(got))
	}
	w.Check(base.Add(90 * time.Minute))
	if got := blockedNudges(rec); len(got) != 2 {
		t.Fatalf("after the base cooldown the reminder fired %d times, want 2", len(got))
	}
}

// TestBlockedReminderOffByDefaultInZeroConfig mirrors PriorityWakeEnabled: New()
// cannot tell an unset bool from an explicit false, so a hand-built config
// leaves the reminder off and only config.Load() turns it on.
func TestBlockedReminderOffByDefaultInZeroConfig(t *testing.T) {
	cfg := blockedCfg()
	cfg.BlockedReminderEnabled = false
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-0001", "blocked:pm-pogo", now)

	w.Check(now)

	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("disabled reminder fired %d times: %+v", len(got), got)
	}
}

// TestBlockedReminderDoesNotUngateDispatch is the property the park-sweeper
// rejection is really about. Adding a second reader of the gated population must
// not make those items dispatchable or visible to the dispatch checks: a
// `blocked:` item still draws no stall nudge and no priority wake, whatever the
// reminder does with it.
func TestBlockedReminderDoesNotUngateDispatch(t *testing.T) {
	cfg := blockedCfg()
	cfg.UnclaimedItemAgeThreshold = time.Minute
	cfg.PriorityWakeEnabled = true
	cfg.HighPriorityWakeDelay = time.Second
	cfg.HighPriorityWakeCooldown = time.Minute
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-e084", "blocked:pm-pogo", "high", now.Add(-time.Hour))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, n := range rec.nudges {
		if strings.HasPrefix(n.message, "stall-watch:") || strings.HasPrefix(n.message, "priority-wake:") {
			t.Errorf("a blocked item drew a dispatch notice %q to %s — the gate must still silence both", n.message, n.agent)
		}
	}
}

// TestBlockedReminderNamesTheRecipientInTheEvent: a notice sent to somebody
// other than the watched agent must be countable as such, or the events log
// cannot distinguish "the mayor was told" from "the blocker was told".
func TestBlockedReminderNamesTheRecipientInTheEvent(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-e084", "blocked:pm-pogo", now)

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var found bool
	for _, e := range rec.events {
		if e.Details["category"] != categoryBlockedReminder {
			continue
		}
		found = true
		if e.Details["nudge_recipient"] != "pm-pogo" {
			t.Errorf("event does not record the recipient: %+v", e.Details)
		}
		if e.Details["blocked_on"] != "pm-pogo" {
			t.Errorf("event does not record who the item is blocked on: %+v", e.Details)
		}
	}
	if !found {
		t.Fatalf("no blocked_reminder event emitted: %+v", rec.events)
	}
}

// TestBlockedReminderNormalizesTheAgentName: the assignee is frontmatter, so it
// may arrive with stray case or spacing. config.BlockedOn already trims and
// case-folds the PREFIX; the agent name itself is returned as written, and this
// check adds delivery as its third consumer.
func TestBlockedReminderNormalizesTheAgentName(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-0001", "Blocked: PM-Pogo", now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 nudge, got %d: %+v", len(got), got)
	}
	if got[0].agent != "pm-pogo" {
		t.Errorf("recipient %q was not normalized to pm-pogo", got[0].agent)
	}
}

// TestBlockedReminderRefusesAPathTraversingName: the agent name is read out of
// frontmatter and then path-joined against the mail root, so a value containing
// a separator must not be treated as a mailbox. It falls through to the
// unreachable branch rather than escaping the root.
//
// The escape target is CREATED here on purpose. Without it the traversal would
// resolve to a nonexistent directory and the test would pass whether or not the
// guard exists — proving nothing. With it, an unguarded hasMailbox finds a real
// maildir outside the root and delivers to "../pm-pogo", so only the guard can
// make this pass.
func TestBlockedReminderRefusesAPathTraversingName(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	makeMailbox(t, filepath.Dir(mailRoot), "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-evil", "blocked:../pm-pogo", now)

	w.Check(now)

	got := blockedNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 nudge, got %d: %+v", len(got), got)
	}
	if got[0].agent != "mayor" {
		t.Errorf("a separator-bearing name resolved to recipient %q; want the coordinator", got[0].agent)
	}
}

// TestHasMailboxRejectsTraversalDirectly asserts the guard itself, so the
// property does not rest on the end-to-end test happening to route the way it
// does. The escape target exists, so a plain filepath.Join would find it.
func TestHasMailboxRejectsTraversalDirectly(t *testing.T) {
	w, _, _, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, filepath.Dir(mailRoot), "pm-pogo")

	if _, err := os.Stat(filepath.Join(mailRoot, "../pm-pogo", "new")); err != nil {
		t.Fatalf("test setup is not exercising the guard — the escape target does not resolve: %v", err)
	}
	for _, who := range []string{"../pm-pogo", "..", ".", "a/b", `a\b`, ""} {
		if w.hasMailbox(who) {
			t.Errorf("hasMailbox(%q) = true, want false", who)
		}
	}
}

// hasCapEvent reports whether any emitted event records id as silenced by the
// notice cap.
func hasCapEvent(rec *recorder, id string) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		ids, ok := e.Details["notice_cap_reached_ids"].([]string)
		if !ok {
			continue
		}
		for _, got := range ids {
			if got == id {
				return true
			}
		}
	}
	return false
}
