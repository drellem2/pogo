package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// The two repositories these tests contend over. Both held three polecats
// against a cap of three when mg-dd77 was observed, which is why all 57 aging
// items in that notice were undispatchable.
const (
	capRepoA = "/Users/daniel/dev/pogo"
	capRepoB = "/Users/daniel/research/onethird_program"
)

// fakeCapacity is a scripted Capacity probe. It records the repos it was asked
// about so a test can assert the probe is not called per item.
type fakeCapacity struct {
	mu     sync.Mutex
	byRepo map[string]RepoCapacity
	// unknown names repos whose occupancy cannot be established.
	unknown map[string]bool
	asked   []string
}

func (f *fakeCapacity) CapacityFor(repo string) (RepoCapacity, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, repo)
	if f.unknown[repo] {
		return RepoCapacity{Repo: repo}, false
	}
	c, ok := f.byRepo[repo]
	if !ok {
		// Unlisted repos have room. Tests name the interesting repos only.
		return RepoCapacity{Repo: repo, Count: 0, Cap: 3}, true
	}
	return c, true
}

func (f *fakeCapacity) askedRepos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.asked...)
	return out
}

// atCap builds a saturated repository with named occupants, exactly as
// agent.RepoOccupancy reports one.
func atCap(repo string, workers ...string) RepoCapacity {
	return RepoCapacity{Repo: repo, Count: len(workers), Cap: len(workers), Polecats: workers, AtCap: true}
}

// capEnv is testEnv plus a capacity probe.
func capEnv(t *testing.T, cfg config.StallWatchConfig, cap Capacity) (*Watcher, *recorder, string) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(workRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	w := New(cfg, Options{
		WorkRoot: workRoot,
		MailRoot: mailRoot,
		Nudge:    rec.nudge,
		Emit:     rec.emit,
		Capacity: cap,
	})
	return w, rec, workRoot
}

// writeRepoItem writes an available item carrying a repo (and optional
// priority), which the shared writeItem helper does not.
func writeRepoItem(t *testing.T, workRoot, id, repo, priority string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(workRoot, "available", id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: pm-pogo\nrepo: %s\n", id, repo)
	if priority != "" {
		content += fmt.Sprintf("priority: %s\n", priority)
	}
	content += fmt.Sprintf("---\n# %s\n", id)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func stallCfg() config.StallWatchConfig {
	return config.StallWatchConfig{
		Enabled:                   true,
		Agent:                     "mayor",
		UnclaimedItemAgeThreshold: 10 * time.Minute,
		NudgeCooldown:             time.Hour,
	}
}

func priorityCfg() config.StallWatchConfig {
	cfg := stallCfg()
	cfg.PriorityWakeEnabled = true
	cfg.HighPriorityWakeDelay = time.Minute
	cfg.HighPriorityWakeCooldown = 5 * time.Minute
	cfg.FastPriorities = []string{"high"}
	return cfg
}

func lastNudge(t *testing.T, rec *recorder) string {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.nudges) == 0 {
		t.Fatal("no nudge was sent")
	}
	return rec.nudges[len(rec.nudges)-1].message
}

func lastDetails(t *testing.T, rec *recorder) map[string]any {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) == 0 {
		t.Fatal("no event was emitted")
	}
	return rec.events[len(rec.events)-1].Details
}

// TestStallNoticeAtCapIsNotADispatchRequest is the ticket's headline case: 57
// items aging in two repos, both saturated, every one of them undispatchable.
// The notice must still be SENT — the finding is true and worth having — and
// must not name a remedy pogod refuses.
func TestStallNoticeAtCapIsNotADispatchRequest(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "c538e", "c6d7b", "cbe37"),
	}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))
	writeRepoItem(t, workRoot, "mg-bbbb", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if strings.Contains(msg, "claim or dispatch them") {
		t.Errorf("at cap, the notice still names the refused remedy: %s", msg)
	}
	// The reader has to be able to check the claim and to ask the useful
	// question, so the numbers and the occupants must both be in the text.
	for _, want := range []string{
		"worker cap", capRepoA, "cap of 3", "c538e", "c6d7b", "cbe37",
		"throughput observation, not a dispatch request", "wedged",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in at-cap notice: %s", want, msg)
		}
	}
	// The two destructive ways to satisfy "dispatch now" at cap are ruled out
	// by name — see atCapAdvice.
	for _, want := range []string{"preempt", "snooze"} {
		if !strings.Contains(msg, want) {
			t.Errorf("at-cap notice must rule out %q: %s", want, msg)
		}
	}
	// And it must not invent a mechanism to replace the one it withdrew.
	// Nothing in pogod auto-dispatches a work item, so "no action required, it
	// will dispatch when a slot frees" would be this ticket's defect inverted.
	if !strings.Contains(msg, "nothing auto-dispatches these") || !strings.Contains(msg, "LATER") {
		t.Errorf("at-cap notice must say LATER and name who acts, not promise self-dispatch: %s", msg)
	}
	// The FINDING survives: no item is dropped from the message.
	for _, id := range []string{"mg-aaaa", "mg-bbbb"} {
		if !strings.Contains(msg, id) {
			t.Errorf("at-cap notice dropped item %s: %s", id, msg)
		}
	}
}

// TestPriorityWakeAtCapDropsTheImperative covers the second surface. It is the
// worse of the two — the most imperative wording, on the highest-value items,
// with the shortest backoff — so a fix that repaired only stall-watch would
// leave the louder half of the defect running.
func TestPriorityWakeAtCapDropsTheImperative(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "c538e", "c6d7b", "cbe37"),
	}}
	w, rec, workRoot := capEnv(t, priorityCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aab5", capRepoA, "high", now.Add(-10*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if !strings.HasPrefix(msg, "priority-wake:") {
		t.Fatalf("expected the priority-wake notice, got: %s", msg)
	}
	if strings.Contains(msg, "claim or dispatch now") {
		t.Errorf("at cap, priority-wake still says dispatch now: %s", msg)
	}
	// The priority information is KEPT: a coordinator wants to know a
	// high-priority item is waiting on a slot.
	for _, want := range []string{"high-priority", "mg-aab5", capRepoA, "c538e", "at capacity"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in at-cap priority notice: %s", want, msg)
		}
	}
}

// TestFreeSlotsKeepTheImperativeVerbatim is the other polarity, and it is the
// one that keeps this fix from being a blanket softening: below cap, aging
// items mean work is being NEGLECTED, and the message must still say so in the
// words it always used.
func TestFreeSlotsKeepTheImperativeVerbatim(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: {Repo: capRepoA, Count: 1, Cap: 3, Polecats: []string{"c538e"}},
	}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))
	writeRepoItem(t, workRoot, "mg-bbbb", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	want := "stall-watch: 2 available work item(s) have sat unclaimed for over 10m0s — claim or dispatch them: mg-aaaa, mg-bbbb"
	if got := lastNudge(t, rec); got != want {
		t.Errorf("below cap the wording must be unchanged.\n got: %s\nwant: %s", got, want)
	}
}

// TestNoCapacityProbeKeepsTheImperative: a daemon with no cap to read must not
// claim anything about capacity. This is the wiring-absent path, and it is what
// every existing stall-watch test exercises.
func TestNoCapacityProbeKeepsTheImperative(t *testing.T) {
	w, rec, workRoot := capEnv(t, stallCfg(), nil)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	want := "stall-watch: 1 available work item(s) have sat unclaimed for over 10m0s — claim or dispatch them: mg-aaaa"
	if got := lastNudge(t, rec); got != want {
		t.Errorf("with no probe the wording must be unchanged.\n got: %s\nwant: %s", got, want)
	}
	if _, ok := lastDetails(t, rec)["capacity_probed"]; ok {
		t.Error("an unprobed notice must not stamp capacity_probed")
	}
}

// TestMixedReposSeparateTheAskFromTheObservation: the two situations produce
// identical mail today, and the whole point is that they stop doing so — even
// inside one notice.
func TestMixedReposSeparateTheAskFromTheObservation(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "c538e", "c6d7b", "cbe37"),
		capRepoB: {Repo: capRepoB, Count: 1, Cap: 3, Polecats: []string{"cdead"}},
	}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-full", capRepoA, "", now.Add(-30*time.Minute))
	writeRepoItem(t, workRoot, "mg-open", capRepoB, "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if !strings.Contains(msg, "1 can be dispatched now: mg-open") {
		t.Errorf("the dispatchable item must be named as the ask: %s", msg)
	}
	if !strings.Contains(msg, "mg-full") || !strings.Contains(msg, "worker cap") {
		t.Errorf("the capped item must still be reported: %s", msg)
	}
	// mg-full must not be inside the imperative clause.
	ask := msg[strings.Index(msg, "can be dispatched now:"):]
	if before, _, ok := strings.Cut(ask, ". "); ok && strings.Contains(before, "mg-full") {
		t.Errorf("a capped item appeared in the dispatch imperative: %s", msg)
	}

	d := lastDetails(t, rec)
	assertIDs(t, d, "dispatchable_ids", []string{"mg-open"})
	assertIDs(t, d, "at_cap_ids", []string{"mg-full"})
}

// TestUnknownOccupancySaysSoRatherThanDefaulting is the third branch. Defaulting
// to "dispatch them" when occupancy cannot be read would be the same defect one
// layer down — a confident remedy on missing information.
func TestUnknownOccupancySaysSoRatherThanDefaulting(t *testing.T) {
	fake := &fakeCapacity{unknown: map[string]bool{capRepoA: true}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if strings.Contains(msg, "claim or dispatch them") {
		t.Errorf("unknown occupancy must not produce the flat imperative: %s", msg)
	}
	for _, want := range []string{"could not be determined", capRepoA, "mg-aaaa", "read the refusal"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in unknown-occupancy notice: %s", want, msg)
		}
	}
	assertIDs(t, lastDetails(t, rec), "occupancy_unknown_ids", []string{"mg-aaaa"})
}

// TestUncertainCountStaysDispatchableButSaysSo: the cap FAILS OPEN on a bad
// witness read, so the notice must too — the uncertainty travels with the
// advice instead of replacing it.
func TestUncertainCountStaysDispatchableButSaysSo(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: {Repo: capRepoA, Count: 1, Cap: 3, Polecats: []string{"c538e"},
			Uncertain: "2 live worker(s) could not be attributed to any repo: cx, cy"},
	}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if !strings.Contains(msg, "claim or dispatch them: mg-aaaa") {
		t.Errorf("an uncertain count must not withdraw a dispatchable item: %s", msg)
	}
	if !strings.Contains(msg, "undercount") || !strings.Contains(msg, "could not be attributed") {
		t.Errorf("the uncertainty must be stated: %s", msg)
	}
}

// TestItemWithNoRepoIsNotProbed: an item naming no repository contends for no
// repo's test suite and the cap leaves it uncapped, so asking the probe about
// "" would only invite a wiring where the empty bucket answers for something
// else.
func TestItemWithNoRepoIsNotProbed(t *testing.T) {
	fake := &fakeCapacity{}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeItem(t, workRoot, "mg-norepo", "pm-pogo", now.Add(-30*time.Minute))

	w.Check(now)

	if got := fake.askedRepos(); len(got) != 0 {
		t.Errorf("probed %v for an item that names no repo", got)
	}
	if !strings.Contains(lastNudge(t, rec), "claim or dispatch them: mg-norepo") {
		t.Errorf("an unrepoed item must stay a dispatch request: %s", lastNudge(t, rec))
	}
}

// TestCapacityIsProbedOncePerRepoPerNotice bounds the cost: the probe reads the
// persisted witness from disk, and 57 items across two repos must be two reads,
// not 57.
func TestCapacityIsProbedOncePerRepoPerNotice(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "c538e", "c6d7b", "cbe37"),
	}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	for _, id := range []string{"mg-0001", "mg-0002", "mg-0003"} {
		writeRepoItem(t, workRoot, id, capRepoA, "", now.Add(-30*time.Minute))
	}
	writeRepoItem(t, workRoot, "mg-0004", capRepoB, "", now.Add(-30*time.Minute))

	w.Check(now)

	if got := len(fake.askedRepos()); got != 2 {
		t.Errorf("probed %d times for 4 items in 2 repos, want 2: %v", got, fake.askedRepos())
	}
	// And every item is still in the notice.
	msg := lastNudge(t, rec)
	for _, id := range []string{"mg-0001", "mg-0002", "mg-0003", "mg-0004"} {
		if !strings.Contains(msg, id) {
			t.Errorf("item %s dropped from the notice: %s", id, msg)
		}
	}
}

// TestAtCapDetailsMakeTheTwoSituationsCountable: at cap, aging items are the
// EXPECTED steady state and say nothing about coordinator diligence; below cap
// the same message means neglect. Prose alone leaves that distinction
// uncountable in events.log, which is how mg-1693 stayed invisible for a night.
func TestAtCapDetailsMakeTheTwoSituationsCountable(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "c538e", "c6d7b", "cbe37"),
	}}
	w, rec, workRoot := capEnv(t, stallCfg(), fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)

	d := lastDetails(t, rec)
	if d["capacity_probed"] != true {
		t.Errorf("capacity_probed = %v, want true", d["capacity_probed"])
	}
	assertIDs(t, d, "at_cap_ids", []string{"mg-aaaa"})
	if _, ok := d["dispatchable_ids"]; ok {
		t.Errorf("nothing was dispatchable, but dispatchable_ids = %v", d["dispatchable_ids"])
	}
	repos, ok := d["at_cap_repos"].([]map[string]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("at_cap_repos = %#v, want one repo", d["at_cap_repos"])
	}
	if repos[0]["repo"] != capRepoA || repos[0]["count"] != 3 || repos[0]["cap"] != 3 {
		t.Errorf("at_cap_repos[0] = %#v, want %s 3/3", repos[0], capRepoA)
	}
	// item_ids is unchanged: the FINDING is still reported in full.
	assertIDs(t, d, "item_ids", []string{"mg-aaaa"})
}

// TestAtCapStillFiresAndStillBacksOff: the fix changes the CONTENT of the
// notice, not whether it is sent or how often. A saturated repo must not become
// a silence — that would trade a noisy alarm for a missing one.
func TestAtCapStillFiresAndStillBacksOff(t *testing.T) {
	fake := &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: atCap(capRepoA, "c538e", "c6d7b", "cbe37"),
	}}
	cfg := stallCfg()
	cfg.NudgeCooldown = 10 * time.Minute
	cfg.RepeatBackoffCap = time.Hour
	w, rec, workRoot := capEnv(t, cfg, fake)
	now := time.Now()
	writeRepoItem(t, workRoot, "mg-aaaa", capRepoA, "", now.Add(-30*time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("nudges = %d after first check, want 1", rec.nudgeCount())
	}
	w.Check(now.Add(time.Minute))
	if rec.nudgeCount() != 1 {
		t.Errorf("nudges = %d inside the cooldown, want 1", rec.nudgeCount())
	}
	w.Check(now.Add(11 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Errorf("nudges = %d after the cooldown, want 2 — an at-cap item must not go silent", rec.nudgeCount())
	}
	if !strings.Contains(lastNudge(t, rec), "notice #2") {
		t.Errorf("the repeat notice must survive the at-cap rewording: %s", lastNudge(t, rec))
	}
}

// assertIDs checks a []string detail key equals want.
func assertIDs(t *testing.T, details map[string]any, key string, want []string) {
	t.Helper()
	got, ok := details[key].([]string)
	if !ok {
		t.Fatalf("%s = %#v, want %v", key, details[key], want)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}
