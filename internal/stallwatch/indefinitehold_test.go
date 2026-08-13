package stallwatch

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// holdCfg is a watcher config with the indefinite-hold report on and every other
// check pushed out of reach. The isolation matters more here than anywhere else
// in this package: the report's recipient is the coordinator, which is also the
// recipient of both dispatch nudges, so an assertion counting "one nudge to
// mayor" would pass for the wrong reason if a stall notice could land in the
// same recorder.
func holdCfg() config.StallWatchConfig {
	return config.StallWatchConfig{
		Enabled:                      true,
		Agent:                        "mayor",
		UnclaimedItemAgeThreshold:    365 * 24 * time.Hour,
		UnreadMailAgeThreshold:       365 * 24 * time.Hour,
		MaxUnreadMailCount:           1000,
		NudgeCooldown:                time.Hour,
		RepeatBackoffCap:             4 * time.Hour,
		PriorityWakeEnabled:          false,
		BlockedReminderEnabled:       false,
		IndefiniteHoldReportEnabled:  true,
		IndefiniteHoldAgeThreshold:   24 * time.Hour,
		IndefiniteHoldReportCooldown: 24 * time.Hour,
	}
}

// holdNudges returns the nudges whose message is an indefinite-hold report.
func holdNudges(rec *recorder) []nudge {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []nudge
	for _, n := range rec.nudges {
		if strings.HasPrefix(n.message, "indefinite-hold:") {
			out = append(out, n)
		}
	}
	return out
}

// TestIndefiniteHoldReportsAParkNothingWillRelease is mg-f398's whole point.
// Before this check a parked item produced no signal to anyone, ever: `mg
// schedule` sweeps `snooze` and `depends` and promotes what has opened, and
// `parked` has no such driver at all. The 22 items of the exhibit sat 2.5 days
// and moved only because a coordinator happened to trace one of them.
func TestIndefiniteHoldReportsAParkNothingWillRelease(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-e7f5", "parked", now.Add(-60*time.Hour))

	w.Check(now)

	got := holdNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 indefinite-hold nudge, got %d: %+v", len(got), got)
	}
	if got[0].agent != "mayor" {
		t.Errorf("report went to %q, want the coordinator", got[0].agent)
	}
	if !strings.Contains(got[0].message, "mg-e7f5") {
		t.Errorf("report does not name the held item: %q", got[0].message)
	}
	// mg-e7f5 is the load-bearing exhibit precisely because nothing about it was
	// malformed — "Reopen/clear assignee when the cap lifts" names a condition
	// entirely outside the item and is not circular in any way. It stranded for
	// exactly as long as the 21 that were. So the age is the fact that must
	// travel, not any reading of the item's text.
	if !strings.Contains(got[0].message, "held 2d12h") {
		t.Errorf("report does not carry the hold's AGE, which is the fact it exists to deliver: %q", got[0].message)
	}
	// The recipient receives dispatch-shaped notices about work items on this
	// same channel. The report must rule that reading out in its own text.
	if !strings.Contains(got[0].message, "NOT a dispatch request") {
		t.Errorf("report does not disclaim dispatch: %q", got[0].message)
	}
	if !strings.Contains(got[0].message, "NOT a release") {
		t.Errorf("report does not disclaim release; releasing stays a coordinator judgement: %q", got[0].message)
	}
}

// TestIndefiniteHoldChangesNoField is the prohibition stated as a test. mayor.md
// forbids a park-SWEEPER, and the boundary this check lives on is that it reads
// and never writes. A future edit that "helpfully" clears an assignee would pass
// every other assertion in this file.
func TestIndefiniteHoldChangesNoField(t *testing.T) {
	w, _, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-a14c", "parked", now.Add(-72*time.Hour))

	before, err := workitem.ListFrom(workRoot, "available")
	if err != nil {
		t.Fatal(err)
	}
	w.Check(now)
	after, err := workitem.ListFrom(workRoot, "available")
	if err != nil {
		t.Fatal(err)
	}

	if len(before) != len(after) {
		t.Fatalf("item count changed across the report: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("the report mutated a work item — it is a READER and must stay one.\nbefore: %+v\nafter:  %+v",
				before[i], after[i])
		}
	}
	if after[0].Assignee != "parked" {
		t.Errorf("assignee is now %q; the report must never unpark anything", after[0].Assignee)
	}
}

// TestIndefiniteHoldReadsNoItemText pins the second prohibition. mayor.md
// rejects a warning that infers a temporal park by matching "until"/"after" in a
// title, because it rots on the next phrasing and fires on legitimate rows. This
// check reports the FACT and AGE of a hold and never its meaning, so two items
// whose titles differ only in temporal wording must produce identical treatment.
func TestIndefiniteHoldReadsNoItemText(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)

	// Two items, same gate, same age. Their bodies are what writeItemIn puts
	// there — the id as an h1 — so the only thing the check could possibly key
	// on beyond assignee and mtime is the id itself.
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	writeItem(t, workRoot, "mg-until", "parked", old)
	writeItem(t, workRoot, "mg-plain", "parked", old)

	w.Check(now)

	got := holdNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 report covering both items, got %d: %+v", len(got), got)
	}
	for _, id := range []string{"mg-until", "mg-plain"} {
		if !strings.Contains(got[0].message, id) {
			t.Errorf("report omits %s: two identically-held items must be treated identically, "+
				"whatever their wording: %q", id, got[0].message)
		}
	}
	// The rejected mechanism, stated positively: nothing in the emitted text may
	// claim to know what a hold is waiting for.
	for _, forbidden := range []string{"temporal", "looks like it should be a snooze"} {
		if strings.Contains(got[0].message, forbidden) {
			t.Errorf("report infers meaning from item text (%q) — that is the guard mayor.md rejects: %q",
				forbidden, got[0].message)
		}
	}
}

// TestIndefiniteHoldSkipsBlockedAssignees keeps the two gated-population readers
// disjoint. `blocked:<agent>` is the one gated value that carries a RECIPIENT,
// and mg-3844 already tells that agent a decision is owed. Reporting it here too
// would be two notices about one hold, to two recipients, on two cadences.
func TestIndefiniteHoldSkipsBlockedAssignees(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-e084", "blocked:pm-pogo", now.Add(-96*time.Hour))

	w.Check(now)

	if got := holdNudges(rec); len(got) != 0 {
		t.Errorf("want no indefinite-hold report for a `blocked:` item — mg-3844 already reads it; got %+v", got)
	}
}

// TestIndefiniteHoldCoversHumanToo pins the population as a RULE rather than a
// list. `human` is mayor.md's fourth row and has no driver either; it is in for
// the same reason `parked` is, and stating membership as "assignee-gated, minus
// the shape something already chases" is what gives a gate value added to
// `non_dispatchable_assignees` next year a reader for free.
func TestIndefiniteHoldCoversHumanToo(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-0001", "human", now.Add(-30*time.Hour))
	writeItem(t, workRoot, "mg-0002", "parked", now.Add(-30*time.Hour))

	w.Check(now)

	got := holdNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d: %+v", len(got), got)
	}
	for _, want := range []string{"mg-0001", "mg-0002", "human", "parked"} {
		if !strings.Contains(got[0].message, want) {
			t.Errorf("report omits %q: %q", want, got[0].message)
		}
	}
}

// TestIndefiniteHoldRespectsAConfiguredVocabulary is the same rule from the
// other side: replace the vocabulary and the report follows it, because
// membership goes through config.IsDispatchGated rather than a second copy of
// the word "parked".
func TestIndefiniteHoldRespectsAConfiguredVocabulary(t *testing.T) {
	cfg := holdCfg()
	cfg.NonDispatchableAssignees = []string{"icebox"}
	w, rec, workRoot, _ := testEnv(t, cfg)
	now := time.Now()
	writeItem(t, workRoot, "mg-cold", "icebox", now.Add(-48*time.Hour))
	// No longer in the vocabulary, so no longer gated — and an ungated item is
	// not held at all.
	writeItem(t, workRoot, "mg-warm", "parked", now.Add(-48*time.Hour))

	w.Check(now)

	got := holdNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].message, "mg-cold") {
		t.Errorf("report omits the configured gate value: %q", got[0].message)
	}
	if strings.Contains(got[0].message, "mg-warm") {
		t.Errorf("report names an assignee this daemon does not gate: %q", got[0].message)
	}
}

// TestIndefiniteHoldWaitsForTheAgeThreshold keeps the channel quiet about
// ordinary coordination. A hold of a few hours is not the finding; 2.5 days is.
func TestIndefiniteHoldWaitsForTheAgeThreshold(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-fresh", "parked", now.Add(-2*time.Hour))

	w.Check(now)

	if got := holdNudges(rec); len(got) != 0 {
		t.Errorf("a 2h-old park is ordinary coordination and must not fire: %+v", got)
	}
}

// TestIndefiniteHoldRepeatsWithoutACap is the one place this check deliberately
// diverges from the blocked-reminder, whose four-notice cap exists because
// nagging an agent who has decided to wait is noise. Here the SILENCE is the
// defect, so a cap would restore it. The cost is bounded by shape instead: one
// digest naming every held item, so a permanent hold is one line per cycle
// rather than a mail of its own.
func TestIndefiniteHoldRepeatsWithoutACap(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	start := time.Now()
	writeItem(t, workRoot, "mg-forever", "parked", start.Add(-48*time.Hour))

	// Ten cycles is well past the blocked-reminder's cap of four.
	for i := 0; i < 10; i++ {
		w.Check(start.Add(time.Duration(i) * 25 * time.Hour))
	}

	got := holdNudges(rec)
	if len(got) != 10 {
		t.Fatalf("want 10 reports across 10 cycles, got %d — a cap here re-creates the silence "+
			"this check exists to close", len(got))
	}
	// A repeat must be distinguishable from a first notice, or the reader learns
	// to discount the channel.
	if !strings.Contains(got[9].message, "[repeat]") {
		t.Errorf("the 10th report is textually a first notice: %q", got[9].message)
	}
}

// TestIndefiniteHoldCooldownSuppressesWithinTheWindow pins the other half: the
// report is a daily digest, not a per-tick one. With the 24h base against the 4h
// repeat_backoff_cap, repeatCooldown takes its `capDur < base` branch and the
// cadence is flat rather than doubling — which is the intended shape and is
// implicit enough to be worth a test.
func TestIndefiniteHoldCooldownSuppressesWithinTheWindow(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	start := time.Now()
	writeItem(t, workRoot, "mg-held", "parked", start.Add(-48*time.Hour))

	w.Check(start)
	w.Check(start.Add(time.Hour))
	w.Check(start.Add(12 * time.Hour))
	if got := holdNudges(rec); len(got) != 1 {
		t.Fatalf("want 1 report inside the 24h window, got %d: %+v", len(got), got)
	}

	// Just past a flat 24h — not 48h, which is where a doubling backoff would
	// have put the second notice.
	w.Check(start.Add(25 * time.Hour))
	if got := holdNudges(rec); len(got) != 2 {
		t.Fatalf("want a 2nd report at 25h (flat 24h cadence, not doubling), got %d", len(got))
	}
}

// TestIndefiniteHoldNamesEveryHeldItemNotJustTheDueOnes pins the digest's
// completeness. `due` decides only WHETHER to send; the message lists every held
// item. A digest that showed a subset would answer "what is being held?" with a
// number that depends on notice timing — the one question it exists to answer,
// given a misleading answer by its own rate limiter.
func TestIndefiniteHoldNamesEveryHeldItemNotJustTheDueOnes(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	start := time.Now()
	writeItem(t, workRoot, "mg-old", "parked", start.Add(-48*time.Hour))

	w.Check(start) // mg-old reported, now inside its own cooldown

	// A second park arrives an hour later and crosses the threshold at once. Its
	// first notice is never delayed, so the report fires — and must carry BOTH.
	writeItem(t, workRoot, "mg-new", "parked", start.Add(-30*time.Hour))
	w.Check(start.Add(time.Hour))

	got := holdNudges(rec)
	if len(got) != 2 {
		t.Fatalf("want 2 reports, got %d: %+v", len(got), got)
	}
	for _, want := range []string{"mg-new", "mg-old"} {
		if !strings.Contains(got[1].message, want) {
			t.Errorf("2nd report omits %s; the digest must be complete even when only one item is due: %q",
				want, got[1].message)
		}
	}
	if !strings.Contains(got[1].message, "2 work item(s)") {
		t.Errorf("2nd report miscounts the held population: %q", got[1].message)
	}
}

// TestIndefiniteHoldOrdersOldestFirst — the reader's question is "what has been
// sitting longest", and the order answers it without them scanning the ages.
func TestIndefiniteHoldOrdersOldestFirst(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-aaa", "parked", now.Add(-30*time.Hour))
	writeItem(t, workRoot, "mg-zzz", "parked", now.Add(-200*time.Hour))

	w.Check(now)

	got := holdNudges(rec)
	if len(got) != 1 {
		t.Fatalf("want 1 report, got %d", len(got))
	}
	if strings.Index(got[0].message, "mg-zzz") > strings.Index(got[0].message, "mg-aaa") {
		t.Errorf("oldest hold is not listed first (id order won): %q", got[0].message)
	}
}

// TestIndefiniteHoldReportsAnUnagedHoldRatherThanDroppingIt is this check
// applied to itself. Its finding is that a hold nothing reads is invisible; an
// item whose file cannot be stat'd has a zero ModTime, and both obvious
// treatments reproduce that finding or something worse — dropping it makes a
// real hold invisible for want of one field, and arithmetic on the zero time
// reports a fresh hold as ~739000 days old, which would be the loudest thing
// this notice could say and would be false.
func TestIndefiniteHoldReportsAnUnagedHoldRatherThanDroppingIt(t *testing.T) {
	w, rec, _, _ := testEnv(t, holdCfg())
	now := time.Now()

	// Built by hand: a zero ModTime means ListFrom could not stat the file, which
	// is not reproducible through the filesystem.
	w.checkIndefiniteHolds(now, []workitem.WorkItem{
		{ID: "mg-nostat", Status: "available", Assignee: "parked"},
	})

	got := holdNudges(rec)
	if len(got) != 1 {
		t.Fatalf("an unstattable hold is still a hold and must be reported; got %d nudges", len(got))
	}
	if !strings.Contains(got[0].message, "mg-nostat") {
		t.Errorf("report omits the unaged item: %q", got[0].message)
	}
	if !strings.Contains(got[0].message, "age UNKNOWN") {
		t.Errorf("report does not say the age is unknown, so a missing number reads as a measured one: %q",
			got[0].message)
	}
	if strings.Contains(got[0].message, "held 739") {
		t.Errorf("report did arithmetic on the zero time: %q", got[0].message)
	}
}

// TestIndefiniteHoldEventIsCountable. A notice nobody can count is the mg-1693
// shape, and this check's own subject matter is invisibility — so its fire has
// to be answerable from events.log alone.
func TestIndefiniteHoldEventIsCountable(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	now := time.Now()
	writeItem(t, workRoot, "mg-p1", "parked", now.Add(-48*time.Hour))
	writeItem(t, workRoot, "mg-h1", "human", now.Add(-48*time.Hour))

	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var found int
	for _, ev := range rec.events {
		if ev.Details["category"] != categoryIndefiniteHold {
			continue
		}
		found++
		if got := ev.Details["item_count"]; got != 2 {
			t.Errorf("item_count = %v, want 2", got)
		}
		gates, ok := ev.Details["hold_gates"].(map[string]int)
		if !ok {
			t.Fatalf("hold_gates missing or wrong type: %#v", ev.Details["hold_gates"])
		}
		if gates["parked"] != 1 || gates["human"] != 1 {
			t.Errorf("hold_gates = %v, want one parked and one human — the two mean opposite "+
				"things about who has to act", gates)
		}
		if _, ok := ev.Details["oldest_age_seconds"]; !ok {
			t.Error("oldest_age_seconds missing; the age is the fact this event exists to record")
		}
		if _, ok := ev.Details["nudge_subject"]; !ok {
			t.Error("nudge_subject missing (mg-b6f8)")
		}
	}
	if found != 1 {
		t.Errorf("want 1 indefinite_hold event, got %d", found)
	}
}

// TestIndefiniteHoldOffByDefaultInAZeroConfig mirrors the priority-wake and
// blocked-reminder contract: New() cannot tell an unset bool from an explicit
// false, so the production default lives in config.Load() and a hand-built
// config stays silent.
func TestIndefiniteHoldOffByDefaultInAZeroConfig(t *testing.T) {
	cfg := holdCfg()
	cfg.IndefiniteHoldReportEnabled = false
	w, rec, workRoot, _ := testEnv(t, cfg)
	now := time.Now()
	writeItem(t, workRoot, "mg-quiet", "parked", now.Add(-48*time.Hour))

	w.Check(now)

	if got := holdNudges(rec); len(got) != 0 {
		t.Errorf("report fired with the feature disabled: %+v", got)
	}
}

// The other half of the pair above — that the shipped default is ARMED — is
// pinned in config.TestIndefiniteHoldDefaults, where Load() is testable. It is
// the assertion that matters operationally: a reader that ships disarmed is this
// check's own finding re-created one level up, a hold nothing looks at plus a
// looker nothing turns on.

// TestIndefiniteHoldForgetsAReleasedItem. selectDue prunes a category's keys for
// items that are no longer candidates, which is what makes a hold that is
// released and later re-applied read as NEW rather than as a continuation of the
// old one — the second park is a fresh decision and its clock starts over.
func TestIndefiniteHoldForgetsAReleasedItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, holdCfg())
	start := time.Now()
	writeItem(t, workRoot, "mg-cycle", "parked", start.Add(-48*time.Hour))
	w.Check(start)

	// Released by a coordinator — this check did not do it, and cannot.
	writeItem(t, workRoot, "mg-cycle", "", start.Add(-48*time.Hour))
	w.Check(start.Add(time.Hour))

	// Re-parked two hours later, still well inside the 24h cooldown that would
	// otherwise be suppressing it.
	writeItem(t, workRoot, "mg-cycle", "parked", start.Add(-46*time.Hour))
	w.Check(start.Add(2 * time.Hour))

	got := holdNudges(rec)
	if len(got) != 2 {
		t.Fatalf("want 2 reports (the re-park is a fresh hold), got %d: %+v", len(got), got)
	}
	if strings.Contains(got[1].message, "[repeat]") {
		t.Errorf("the re-park was reported as a continuation of the released hold: %q", got[1].message)
	}
}
