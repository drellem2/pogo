package progresswatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 14, 5, 18, 0, 0, time.UTC)

// blockedWorker is the shape the incident had: awake on its PTY, nothing
// written, old enough to be judged.
func blockedWorker(name string) Worker {
	return Worker{
		Name:        name,
		WorkItemID:  "mg-" + name,
		Age:         40 * time.Minute,
		PTYIdle:     4 * time.Minute,
		HasOutput:   true,
		WriteIdle:   15 * time.Minute,
		HasWrites:   true,
		WritesKnown: true,
	}
}

// incident reproduces the 05:17Z reading mayor took by hand: 7 polecats
// PTY-active within 4 minutes, none having written a file in 15, the fleet
// holding 0.10 of 10 cores, and no merge in ~30 minutes.
func incident() Snapshot {
	s := Snapshot{
		Now:              now,
		HostCores:        10,
		WorkerCores:      0.10,
		CoresKnown:       true,
		LastProgress:     now.Add(-31 * time.Minute),
		LastProgressWhat: "merge mr-0f2a landed",
		ProgressSince:    now.Add(-6 * time.Hour),
		ProgressKnown:    true,
	}
	for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"} {
		s.Workers = append(s.Workers, blockedWorker(n))
	}
	return s
}

// TestTheIncidentIsReported is the whole point of the package: the exact state
// of 2026-08-14 05:17Z, for which no instrument fired and none could have.
func TestTheIncidentIsReported(t *testing.T) {
	r := Evaluate(incident(), Thresholds{})
	if !r.Stalled {
		t.Fatalf("the 05:17Z incident did not report: held=%v blind=%v", r.Held, r.Blind)
	}
	if r.Blocked != 7 || r.Judged != 7 {
		t.Errorf("blocked=%d judged=%d, want 7/7", r.Blocked, r.Judged)
	}
	if len(r.Held) != 0 || len(r.Blind) != 0 {
		t.Errorf("a finding must have nothing held or blind: held=%v blind=%v", r.Held, r.Blind)
	}
	if r.SinceProgress != 31*time.Minute {
		t.Errorf("since_progress = %s, want 31m", r.SinceProgress)
	}
}

// TestReadingStatesWhatItMeasured is mg-c058's lesson as a test: the reported
// value carries the numbers, not a state token. A reading that says only
// "STALLED" invites the present-tense over-reading that paged a sleeping human.
func TestReadingStatesWhatItMeasured(t *testing.T) {
	r := Evaluate(incident(), Thresholds{})
	for _, want := range []string{
		"7 of 7 judged worker(s) alive and writing nothing",
		"worker subtrees at 0.10 of 10 cores",
		"nothing landed in 31m",
		"merge mr-0f2a landed",
	} {
		if !strings.Contains(r.Measurements(), want) {
			t.Errorf("measurement %q missing %q", r.Measurements(), want)
		}
	}
	// And the subject, which is the part that gets skimmed and forwarded.
	for _, want := range []string{"7 workers", "31m", "0.10 of 10 cores"} {
		if !strings.Contains(r.Subject(), want) {
			t.Errorf("subject %q missing %q", r.Subject(), want)
		}
	}
}

// TestCleanReadingStillCarriesTheNumbers is the other half of the ticket: the
// instrument mayor needed was one place showing all four measurements at once,
// including on a fleet that is fine. A reading that populates itself only when
// it fires is not that.
func TestCleanReadingStillCarriesTheNumbers(t *testing.T) {
	s := incident()
	s.WorkerCores = 6.2 // somebody is building
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("a fleet burning 6.2 cores must not report")
	}
	if !strings.Contains(r.Measurements(), "worker subtrees at 6.20 of 10 cores") {
		t.Errorf("clean reading dropped the cores measurement: %s", r.Measurements())
	}
	if !strings.Contains(r.Measurements(), "nothing landed in 31m") {
		t.Errorf("clean reading dropped the completion measurement: %s", r.Measurements())
	}
	if len(r.Held) == 0 || !strings.Contains(strings.Join(r.Held, " "), "computing") {
		t.Errorf("a clean reading must name the conjunct that rescued it, got %v", r.Held)
	}
}

// TestGatingWorkerIsNotAFinding is mayor's lesson (1). A worker running the
// gate reads negative on PTY activity and on worktree writes BY CONSTRUCTION —
// it writes to /tmp and the build cache and its output may be captured. p27c0
// was nearly declared stalled on exactly that pair while `go test` ran with
// live children. The subtree CPU is what rescues it.
func TestGatingWorkerIsNotAFinding(t *testing.T) {
	s := incident()
	s.WorkerCores = 3.9 // one worker's `go test` subtree
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("a worker whose SUBTREE is burning cores must not be declared stalled")
	}
}

// TestOneQuietWorkerIsNotAFleetSignal is mayor's lesson (2): the same
// zero-writes shape was benign on p0f24 at 10 minutes. What made 05:18Z
// diagnostic was seven at once.
func TestOneQuietWorkerIsNotAFleetSignal(t *testing.T) {
	s := incident()
	s.Workers = s.Workers[:1]
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("one quiet worker must not be a fleet finding")
	}
	if !strings.Contains(strings.Join(r.Held, " "), "fleet signal") {
		t.Errorf("expected the population guard in Held, got %v", r.Held)
	}
}

// TestYoungWorkerIsNotJudged is the other half of lesson (2): a worker still
// reading its ticket has written no file, and that is correct.
func TestYoungWorkerIsNotJudged(t *testing.T) {
	s := incident()
	for i := range s.Workers {
		s.Workers[i].Age = 5 * time.Minute
		s.Workers[i].HasWrites = false
	}
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("a fleet of newborn workers must not report")
	}
	if r.Judged != 0 {
		t.Errorf("judged = %d, want 0 — nobody was old enough", r.Judged)
	}
	if r.LiveWorkers != 7 {
		t.Errorf("live = %d, want 7 — the young ones are still counted in the population", r.LiveWorkers)
	}
	if !strings.Contains(r.Measurements(), "7 live worker(s), 7 too young to judge") {
		t.Errorf("the reading must state who it declined to judge: %s", r.Measurements())
	}
}

// TestUnwrittenWorktreeCountsOnceTheWorkerIsOldEnough: a tree that has never
// been written is the same fact as one last written that long ago, once the
// worker has had the time.
func TestUnwrittenWorktreeCountsOnceTheWorkerIsOldEnough(t *testing.T) {
	s := incident()
	for i := range s.Workers {
		s.Workers[i].HasWrites = false
		s.Workers[i].WriteIdle = 0
	}
	r := Evaluate(s, Thresholds{})
	if !r.Stalled {
		t.Fatalf("a 40-minute-old worker with an untouched worktree is quiet: held=%v", r.Held)
	}
	if r.MinWriteIdle != 40*time.Minute {
		t.Errorf("min_write_idle = %s, want the worker's whole age (40m)", r.MinWriteIdle)
	}
}

// TestQuietPTYDropsAWorkerFromThePopulation: the signature is workers
// demonstrably AWAKE and producing nothing. A worker that has gone quiet on its
// PTY too is a different fault (stall/wedge) with its own detectors, and must
// not be counted here.
func TestQuietPTYDropsAWorkerFromThePopulation(t *testing.T) {
	s := incident()
	for i := range s.Workers {
		s.Workers[i].PTYIdle = 45 * time.Minute
	}
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("workers that are not PTY-active are not this detector's fault")
	}
	if r.Blocked != 0 {
		t.Errorf("blocked = %d, want 0", r.Blocked)
	}
}

// TestNeverWrittenPTYIsNotAliveness: a worker that has NEVER written to its PTY
// has an unmeasurable idle time, not a short one — it may be seconds into spawn
// or wedged before its first turn (mg-ce61). agent.PolecatActivity refuses to
// read that zero as "idle for no time" and so must this.
func TestNeverWrittenPTYIsNotAliveness(t *testing.T) {
	s := incident()
	for i := range s.Workers {
		s.Workers[i].HasOutput = false
		s.Workers[i].PTYIdle = 0
	}
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("a zero PTYIdle with HasOutput=false must not count as alive")
	}
}

// TestUnmeasurableCPUIsNotAFinding, and is not a clean fleet either. An
// unresolvable hostload sample reports zeros that mean "this host cannot tell".
func TestUnmeasurableCPUIsNotAFinding(t *testing.T) {
	s := incident()
	s.CoresKnown = false
	s.WorkerCores = 0
	s.CoresError = "ps resolution 1s cannot resolve a 1s window"
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("an unmeasured conjunct must not produce a finding")
	}
	if len(r.Blind) == 0 || !strings.Contains(strings.Join(r.Blind, " "), "cannot resolve") {
		t.Errorf("blindness must be recorded with its reason, got %v", r.Blind)
	}
	if !strings.Contains(r.Measurements(), "worker CPU not measured") {
		t.Errorf("the reading must say it did not measure: %s", r.Measurements())
	}
	if !strings.HasPrefix(r.String(), "NOT MEASURED") {
		t.Errorf("a blind reading must not read as a clean one: %s", r.String())
	}
}

// TestUnreadableWorktreeIsNotAnUnwrittenOne.
func TestUnreadableWorktreeIsNotAnUnwrittenOne(t *testing.T) {
	s := incident()
	s.Workers[0].WritesKnown = false
	s.Workers[0].WritesError = "permission denied"
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("a worker whose worktree could not be read must block the finding, not join it")
	}
	if len(r.Blind) == 0 || !strings.Contains(strings.Join(r.Blind, " "), "p1") {
		t.Errorf("blind must name the worker, got %v", r.Blind)
	}
}

// TestUnreadableCompletionsAreNotZeroCompletions.
func TestUnreadableCompletionsAreNotZeroCompletions(t *testing.T) {
	s := incident()
	s.ProgressKnown = false
	s.LastProgress = time.Time{}
	s.ProgressError = "refinery history unavailable"
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("unreadable completions must not be read as no completions")
	}
	if !strings.Contains(r.Measurements(), "fleet completions not read") {
		t.Errorf("the reading must say so: %s", r.Measurements())
	}
}

// TestNoWindowToMeasureAgainstIsBlind: with no completion in the window AND no
// window, "nothing landed in 0s" would be a fabricated measurement.
func TestNoWindowToMeasureAgainstIsBlind(t *testing.T) {
	s := incident()
	s.LastProgress = time.Time{}
	s.ProgressSince = time.Time{}
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("no observable window means no measurement")
	}
	if len(r.Blind) == 0 {
		t.Error("expected the missing window to be recorded as blindness")
	}
}

// TestEmptyWindowMeasuresFromItsStart: a fleet that has landed nothing since
// pogod started is a measurement, stated against the window it had.
func TestEmptyWindowMeasuresFromItsStart(t *testing.T) {
	s := incident()
	s.LastProgress = time.Time{}
	s.LastProgressWhat = ""
	s.ProgressSince = now.Add(-45 * time.Minute)
	r := Evaluate(s, Thresholds{})
	if !r.Stalled {
		t.Fatalf("45 minutes of observed silence is a finding: held=%v blind=%v", r.Held, r.Blind)
	}
	if !strings.Contains(r.Measurements(), "nothing landed in the 45m observed") {
		t.Errorf("the reading must state the window it measured: %s", r.Measurements())
	}
}

// TestRecentCompletionRescuesTheFleet.
func TestRecentCompletionRescuesTheFleet(t *testing.T) {
	s := incident()
	s.LastProgress = now.Add(-4 * time.Minute)
	r := Evaluate(s, Thresholds{})
	if r.Stalled {
		t.Fatal("a merge four minutes ago is the fleet producing")
	}
	if !strings.Contains(strings.Join(r.Held, " "), "landed something 4m ago") {
		t.Errorf("expected the completion in Held, got %v", r.Held)
	}
}

// TestMergeInFlightExcusesTheFleetOnlyWhileItIsYoungerThanTheWindow. A gate
// holding the refinery's serial slot runs in pogod's subtree, where no worker
// measurement can see it, so it needs an explicit excuse. But a gate that has
// held the slot longer than the fleet has been silent is not evidence of
// progress — it is part of what is being reported.
func TestMergeInFlightExcusesTheFleetOnlyWhileItIsYoungerThanTheWindow(t *testing.T) {
	s := incident()
	s.InFlight = "mr-9f1c"
	s.InFlightSince = now.Add(-6 * time.Minute)
	if r := Evaluate(s, Thresholds{}); r.Stalled {
		t.Fatal("a merge in flight for 6m is the fleet producing")
	}

	s.InFlightSince = now.Add(-90 * time.Minute)
	r := Evaluate(s, Thresholds{})
	if !r.Stalled {
		t.Fatalf("a merge stuck in flight for 90m must not excuse the fleet: held=%v", r.Held)
	}
	if !strings.Contains(r.Measurements(), "merge mr-9f1c in flight for 1h30m") {
		t.Errorf("the reading must carry the stuck merge: %s", r.Measurements())
	}
}

// TestThresholdsTravelWithTheReading: a reading must be judgeable without
// consulting this source file.
func TestThresholdsTravelWithTheReading(t *testing.T) {
	r := Evaluate(incident(), Thresholds{MinWorkers: 2})
	if r.Thresholds.MinWorkers != 2 {
		t.Errorf("min_workers = %d, want the caller's 2", r.Thresholds.MinWorkers)
	}
	if r.Thresholds.NoProgressFor != DefaultNoProgressFor {
		t.Errorf("unset thresholds must fall back to the default, got %s", r.Thresholds.NoProgressFor)
	}
}

// TestFreshestWriteIsReportedNotAnAverage: the number worth printing is the one
// that could falsify the finding.
func TestFreshestWriteIsReportedNotAnAverage(t *testing.T) {
	s := incident()
	s.Workers[3].WriteIdle = 11 * time.Minute
	s.Workers[5].WriteIdle = 55 * time.Minute
	s.Workers[2].PTYIdle = 9 * time.Minute
	r := Evaluate(s, Thresholds{})
	if !r.Stalled {
		t.Fatalf("still the incident: held=%v", r.Held)
	}
	if r.MinWriteIdle != 11*time.Minute {
		t.Errorf("min_write_idle = %s, want the freshest write (11m)", r.MinWriteIdle)
	}
	if r.MaxPTYIdle != 9*time.Minute {
		t.Errorf("max_pty_idle = %s, want the stalest PTY write (9m)", r.MaxPTYIdle)
	}
}

// --- the JSON tri-state (mg-e75b) -------------------------------------------

// TestJSONSeparatesCleanFromBlind is the machine-readable twin of
// TestTheThreeRendersAreGreppableApart, and it is here because the render was
// fixed (mg-516e) while the JSON was left with the identical collision: a clean
// reading and a blind one both carry `stalled:false`, and the only thing that
// used to separate them was the PRESENCE of `blind` — an omitempty array, so
// the distinguishing evidence was absent in exactly the case that looked
// healthy.
//
// This asserts the collision still exists on `stalled` (it must: `stalled` is
// the conjunction, and a blind run has no conjunction to report) AND that
// `verdict` resolves it.
func TestJSONSeparatesCleanFromBlind(t *testing.T) {
	clean := incident()
	clean.WorkerCores = 6.2

	blind := incident()
	blind.CoresKnown = false
	blind.CoresError = "ps resolution cannot resolve a 1s window"

	cleanJSON := mustMarshal(t, Evaluate(clean, Thresholds{}))
	blindJSON := mustMarshal(t, Evaluate(blind, Thresholds{}))

	// The premise. If this ever stops holding the test above is measuring
	// nothing, so it is asserted rather than assumed.
	if cleanJSON["stalled"] != false || blindJSON["stalled"] != false {
		t.Fatalf("premise gone: stalled is no longer false for both (clean=%v blind=%v)",
			cleanJSON["stalled"], blindJSON["stalled"])
	}
	if _, ok := blindJSON["blind"]; !ok {
		t.Error("a blind reading must name what it could not measure")
	}
	if _, ok := cleanJSON["blind"]; ok {
		t.Error("a clean reading must not carry a blind list")
	}

	if got := cleanJSON["verdict"]; got != string(VerdictClean) {
		t.Errorf("clean reading emitted verdict=%v, want %q", got, VerdictClean)
	}
	if got := blindJSON["verdict"]; got != string(VerdictBlind) {
		t.Errorf("blind reading emitted verdict=%v, want %q", got, VerdictBlind)
	}
}

// TestJSONVerdictIsAlwaysPresent. The failure this whole field exists to remove
// is evidence that goes MISSING in the healthy case, so `verdict` must never be
// omitempty and must never be the empty string — including for the zero
// Reading, which is what a caller holds alongside an error.
func TestJSONVerdictIsAlwaysPresent(t *testing.T) {
	stalledSnap := incident()

	cleanSnap := incident()
	cleanSnap.WorkerCores = 6.2

	blindSnap := incident()
	blindSnap.ProgressKnown = false

	cases := []struct {
		name string
		r    Reading
		want Verdict
	}{
		{"stalled", Evaluate(stalledSnap, Thresholds{}), VerdictStalled},
		{"clean", Evaluate(cleanSnap, Thresholds{}), VerdictClean},
		{"blind", Evaluate(blindSnap, Thresholds{}), VerdictBlind},
		{"zero value", Reading{}, VerdictUnknown},
	}
	for _, c := range cases {
		m := mustMarshal(t, c.r)
		got, ok := m["verdict"]
		if !ok {
			t.Errorf("%s: no verdict key at all — the field is omitempty somewhere", c.name)
			continue
		}
		if got == "" {
			t.Errorf("%s: verdict is the empty string", c.name)
		}
		if got != string(c.want) {
			t.Errorf("%s: verdict=%v, want %q", c.name, got, c.want)
		}
	}
}

// TestZeroReadingIsNotClean pins the half a derived field could most easily get
// wrong. A verdict computed from Stalled and Blind would answer "clean" for a
// Reading nobody ever evaluated, because both are zero there — a default-
// constructed value that reads as healthy is the same defect one level down.
func TestZeroReadingIsNotClean(t *testing.T) {
	if v := (Reading{}).Verdict(); v == VerdictClean {
		t.Fatal("the zero Reading answers clean; a value nobody measured must never read as healthy")
	}
}

// TestVerdictNeverDisagreesWithTheBooleans. `verdict` is a second way of saying
// what `stalled` and `blind` already say, and a second source of truth that can
// drift is a worse false green than the ambiguity it replaced. Deriving rather
// than storing is what makes this hold; this asserts it over every state.
func TestVerdictNeverDisagreesWithTheBooleans(t *testing.T) {
	stalledSnap := incident()

	cleanSnap := incident()
	cleanSnap.WorkerCores = 6.2

	blindSnap := incident()
	blindSnap.CoresKnown = false

	// The invariant the struct comment states: Stalled is never true while
	// Blind is set, so the three are genuinely exclusive.
	for _, s := range []Snapshot{stalledSnap, cleanSnap, blindSnap} {
		r := Evaluate(s, Thresholds{})
		switch r.Verdict() {
		case VerdictBlind:
			if len(r.Blind) == 0 || r.Stalled {
				t.Errorf("verdict=blind but blind=%v stalled=%v", r.Blind, r.Stalled)
			}
		case VerdictStalled:
			if !r.Stalled || len(r.Blind) > 0 {
				t.Errorf("verdict=stalled but blind=%v stalled=%v", r.Blind, r.Stalled)
			}
		case VerdictClean:
			if r.Stalled || len(r.Blind) > 0 {
				t.Errorf("verdict=clean but blind=%v stalled=%v", r.Blind, r.Stalled)
			}
		default:
			t.Errorf("evaluated reading answered %q", r.Verdict())
		}
	}
}

// TestVerdictsAreGreppableApart. mg-516e's defect was that the blind headline
// was a PREFIX of the clean one, so a `grep` matched where an equality test
// would not have. The same trap is available to four short tokens, and a
// consumer piping --json through grep is the likeliest reader of this field.
func TestVerdictsAreGreppableApart(t *testing.T) {
	all := []Verdict{VerdictClean, VerdictStalled, VerdictBlind, VerdictUnknown}
	for _, a := range all {
		if a == "" {
			t.Error("a verdict is the empty string, which is a substring of everything")
		}
		for _, b := range all {
			if a == b {
				continue
			}
			if strings.Contains(string(a), string(b)) {
				t.Errorf("verdict %q contains %q — grep cannot tell the two states apart", a, b)
			}
		}
	}
}

// TestVerdictSurvivesAnOlderDaemon. The CLI decodes this struct from whatever
// pogod is deployed, and a daemon predating this field sends no `verdict` at
// all. Because the value is derived rather than stored, the decoded reading
// still answers correctly instead of falling back to a silent zero.
func TestVerdictSurvivesAnOlderDaemon(t *testing.T) {
	// Exactly what an older pogod put on the wire for a blind reading: the
	// booleans and the blind list, and no verdict key.
	const oldWire = `{"now":"2026-08-14T05:18:00Z","stalled":false,` +
		`"blind":["worker CPU unmeasurable: ps resolution cannot resolve a 1s window"]}`
	var r Reading
	if err := json.Unmarshal([]byte(oldWire), &r); err != nil {
		t.Fatalf("decoding an older daemon's reading: %v", err)
	}
	if got := r.Verdict(); got != VerdictBlind {
		t.Errorf("a pre-mg-e75b blind reading decoded as %q, want %q", got, VerdictBlind)
	}
}

func mustMarshal(t *testing.T, r Reading) map[string]any {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshalling reading: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("re-reading the emitted JSON: %v\n%s", err, b)
	}
	return m
}
