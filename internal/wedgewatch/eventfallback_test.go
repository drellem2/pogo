package wedgewatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// This file is mg-20eb: the documented event-log fallback had no production
// writer, so it never ran outside the test suite.
//
// The shape of the defect matters more than the defect. `Observation` carried
// an `EventsLastSeen` field; `stallOf` keyed its whole graceful-degradation
// branch on it; watcher_test.go set it by hand and passed. `observe()`, the
// only production constructor of an Observation, did not set it and never had.
// So `o.EventsLastSeen.IsZero()` was TRUE BY CONSTRUCTION for every agent that
// has ever existed on this box, and the counter-parse failure that the fallback
// exists to survive did not degrade this detector — it disabled it. First pass
// after the daemon reached this revision: 5 agents, 5 unjudgeable. Forty
// judgements over 25 minutes, zero verdicts.
//
// A test that builds its own Observation cannot see that, because inventing the
// field is exactly the step production was missing. Every test below therefore
// enters through a PRODUCTION assembly function — buildSnapshot or
// attachEventFallback — with observations shaped the way observe() shapes them,
// which is to say without the field.

// observedLikeProduction returns an Observation with exactly the fields
// observe() sets, and no others.
//
// The omissions are the point: no EventsLastSeen, no EventsRead. If a future
// change teaches observe() to fill those in directly, this helper is wrong and
// the tests below stop testing the assembly step — so observe()'s literal is
// the one place that must keep NOT setting them.
func observedLikeProduction(name, identity string, uptime time.Duration, out []byte, now time.Time) Observation {
	return Observation{
		Name:         name,
		Identity:     identity,
		Type:         "polecat",
		Alive:        true,
		Uptime:       uptime,
		Output:       out,
		LastOutputAt: now,
	}
}

// fixedEvents returns an EventsFunc over a hand-built index, counting calls so
// a test can assert the log was opened once, or not at all.
func fixedEvents(idx EventsIndex, calls *int) EventsFunc {
	return func() EventsIndex {
		if calls != nil {
			*calls++
		}
		return idx
	}
}

// TestTheProductionSnapshotWiresTheEventLogFallback is mg-20eb's reproduction.
//
// It is the 2026-08-09 fleet condition exactly: an agent whose declared-work
// counter cannot be parsed by any stem in the table. Before the fix that agent
// came out of every pass as an error — "could NOT be judged" — because the
// fallback the package doc promises was keyed on a field nothing assigned. The
// assertion is not that the agent is wedged; it is that the detector produced a
// VERDICT rather than a shrug.
func TestTheProductionSnapshotWiresTheEventLogFallback(t *testing.T) {
	obs := []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), t0)}
	idx := EventsIndex{Readable: true, LastSeen: map[string]time.Time{
		"cat-opaque": t0.Add(-3 * time.Hour),
	}}

	snap := buildSnapshot(t0, 1, obs, nil,
		func(context.Context) HostView { return roomyHost }, fixedEvents(idx, nil))

	if len(snap.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(snap.Agents))
	}
	got := snap.Agents[0]
	if got.EventsLastSeen.IsZero() {
		t.Fatal("the production snapshot left EventsLastSeen at zero — that is mg-20eb itself: " +
			"stallOf's documented degradation path is unreachable, so an unparseable counter " +
			"disables this detector rather than coarsening it")
	}
	if !got.EventsRead {
		t.Error("EventsRead is false after the log was read, so a blind message cannot say whether it looked")
	}

	// And the whole way through: the watcher must now reach a verdict.
	stalled, source, established := stallOf(got, false, 0, t0)
	if !established {
		t.Fatal("stallOf still could not establish a stall from a wired event-log fallback")
	}
	if source != "events_stale" {
		t.Errorf("stall_source = %q, want events_stale", source)
	}
	if stalled != 3*time.Hour {
		t.Errorf("stalled_for = %s, want 3h", stalled)
	}
}

// TestAnUnparseableCounterDegradesRatherThanBlindsTheWatcher runs the same
// condition through the RUNNER, which is where the 40 errors came from.
//
// The fleet is the one this box actually had: every agent's counter
// unreadable. What must come out is a finding for the agent with a dead-end
// marker on screen — the coarser detector the package doc promises — and not an
// error event.
func TestAnUnparseableCounterDegradesRatherThanBlindsTheWatcher(t *testing.T) {
	rec := &recorder{}
	idx := EventsIndex{Readable: true, LastSeen: map[string]time.Time{
		"cat-opaque": t0.Add(-3 * time.Hour),
	}}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		obs := []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour+now.Sub(t0),
			append(noCounterPTY(), []byte("Please run /login\r\n")...), now)}
		return attachEventFallback(obs, fixedEvents(idx, nil)), validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)

	if errs := rec.ofType(EventError); len(errs) != 0 {
		t.Errorf("wedge_watch_error events = %d, want 0 — an unparseable counter must make this "+
			"detector coarser, not silent; the whole of mg-20eb is 40 of these and no verdicts: %v",
			len(errs), errs[0].Details)
	}
	findings, _ := w.Latest()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1 via the event-log fallback", len(findings))
	}
	if findings[0].StallSource != "events_stale" {
		t.Errorf("stall_source = %q, want events_stale", findings[0].StallSource)
	}
}

// TestTheEventLogIsReadOnlyWhenACounterCannotBeParsed pins the laziness.
//
// The scan is ~700ms against this box's 76MB live log. It is the fallback, so
// it must cost nothing on the fleet it is not needed for — the same asymmetry
// RegistrySource already applies to the credential read, and for the stronger
// reason that this one grows with the log.
func TestTheEventLogIsReadOnlyWhenACounterCannotBeParsed(t *testing.T) {
	calls := 0
	readable := []Observation{observedLikeProduction("working", "cat-working", time.Hour, workingPTY("Baked for 2m 56s"), t0)}
	if _, ok := ParseDeclaredWork(readable[0].Output); !ok {
		t.Fatal("fixture guard: this PTY must parse, or the test proves nothing about laziness")
	}
	attachEventFallback(readable, fixedEvents(EventsIndex{Readable: true}, &calls))
	if calls != 0 {
		t.Errorf("event log opened %d times for a fleet whose counters all parse; want 0", calls)
	}

	mixed := []Observation{
		readable[0],
		observedLikeProduction("opaque", "cat-opaque", time.Hour, noCounterPTY(), t0),
	}
	attachEventFallback(mixed, fixedEvents(EventsIndex{Readable: true}, &calls))
	if calls != 1 {
		t.Errorf("event log opened %d times for one sample with one unparseable counter; want exactly 1 — "+
			"the index is fleet-wide and must not be rebuilt per agent", calls)
	}
}

// TestAnUnreadableEventLogIsNotAnEmptyOne is the absence-as-evidence rule this
// family of detectors is built on, applied to the newest input.
//
// A log that could not be scanned must leave the observation untouched, so the
// agent reports as unjudgeable. Folding it in as "no entry" would render every
// agent maximally stale the moment the log went missing, which is a detector
// inventing a fleet-wide wedge out of its own blindness.
func TestAnUnreadableEventLogIsNotAnEmptyOne(t *testing.T) {
	for _, idx := range []EventsIndex{
		{Reason: ReasonNoEventLogPath},
		{Reason: ReasonEventLogAbsent},
		{Reason: ReasonEventLogUnreadable},
	} {
		obs := []Observation{observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), t0)}
		got := attachEventFallback(obs, fixedEvents(idx, nil))[0]
		if got.EventsRead {
			t.Errorf("%s: EventsRead is true after a failed read", idx.Reason)
		}
		if !got.EventsLastSeen.IsZero() {
			t.Errorf("%s: an unreadable log produced a recency timestamp", idx.Reason)
		}
		if _, _, established := stallOf(got, false, 0, t0); established {
			t.Errorf("%s: a stall was established from a log that could not be read", idx.Reason)
		}
	}
}

// TestTheBlindMessageSaysOnlyWhatWasEstablished is mg-20eb's third defect.
//
// The single old message asserted that "the event log has no entry for this
// identity". Nothing had looked, and most of the identities it said that about
// DID have entries — so anyone diagnosing this checked the log, found the
// message false, and had to work out that the clause was a constant rather than
// an observation. The two states now read differently, and the never-looked one
// must not mention what the log contains.
func TestTheBlindMessageSaysOnlyWhatWasEstablished(t *testing.T) {
	base := observedLikeProduction("opaque", "cat-opaque", 9*time.Hour, noCounterPTY(), t0)

	neverLooked := base
	_, why, _ := stallOf(neverLooked, false, 0, t0)
	if strings.Contains(why, "has no entry") {
		t.Errorf("the no-fallback message claims the event log has no entry for this identity, "+
			"which it never checked: %q", why)
	}
	if !strings.Contains(why, "no event-log fallback is available") {
		t.Errorf("the no-fallback message does not say a fallback was unavailable: %q", why)
	}

	looked := base
	looked.EventsRead = true
	_, why, _ = stallOf(looked, false, 0, t0)
	if !strings.Contains(why, "WAS read") || !strings.Contains(why, "has no entry") {
		t.Errorf("after a successful scan that found nothing, the message must say so: %q", why)
	}
}

// --- the live reader --------------------------------------------------------

// writeFixtureLog writes JSONL events where events.LogPath() will find them, so
// SystemEvents — the function pogod binds, resolving its own path — can be
// driven without a second entry point that only tests use.
//
// The destination is events.log under the SANDBOX's POGO_HOME: the same
// filepath.Join(config.PogoHome(), "events.log") shape production resolves,
// pointed at the throwaway root TestMain pins and TestSandboxIsEstablished
// verifies. The containment check below is not ceremony — this helper writes a
// file at whatever path the resolver hands back, and the resolver's whole job
// is to hand back the fleet's audit spine.
func writeFixtureLog(t *testing.T, evs []events.Event) string {
	t.Helper()
	path := filepath.Join(config.PogoHome(), "events.log")
	if !sandbox.Contains(path) {
		t.Fatalf("refusing to write %s — it is outside the sandbox root %s", path, sandbox.Root)
	}
	events.SetLogPathForTesting(path)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	resolved, err := events.LogPath()
	if err != nil {
		t.Fatalf("resolve log path: %v", err)
	}
	if resolved != path {
		t.Fatalf("events.LogPath() = %s, want the sandbox fixture %s — SystemEvents would read elsewhere",
			resolved, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var b strings.Builder
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(line)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

func ev(agent, typ string, at time.Time) events.Event {
	return events.Event{EventType: typ, Agent: agent, Timestamp: at.UTC().Format(time.RFC3339Nano)}
}

// TestSystemEventsIndexesTheLastLinePerIdentity drives the PRODUCTION reader —
// the one pogod binds — against a fixture log.
//
// The fixture is the shape this box's log actually has for a live agent: a
// spawn line and, for some, one later line of its own. Ordering is deliberately
// not monotonic per identity, because the log is appended by several processes
// and an out-of-order pair must not make an identity look fresher or staler
// than its newest line.
func TestSystemEventsIndexesTheLastLinePerIdentity(t *testing.T) {
	spawn := t0.Add(-9 * time.Hour)
	writeFixtureLog(t, []events.Event{
		ev("cat-opaque", "agent_spawned", spawn),
		ev("crew-mayor", "agent_spawned", spawn),
		ev("crew-mayor", "work_item_claimed", t0.Add(-20*time.Minute)),
		// Out of order on purpose: an older line arriving after a newer one.
		ev("crew-mayor", "mail_sent", t0.Add(-2*time.Hour)),
		ev("pogod", "scheduler_fire_delivered", t0.Add(-time.Minute)),
	})

	idx := SystemEvents()
	if !idx.Readable {
		t.Fatalf("SystemEvents unreadable: %s", idx.Reason)
	}
	if got := idx.LastSeen["crew-mayor"]; !got.Equal(t0.Add(-20 * time.Minute)) {
		t.Errorf("crew-mayor last seen = %s, want the NEWEST of its lines (%s), not the last one written",
			got, t0.Add(-20*time.Minute))
	}
	if got := idx.LastSeen["cat-opaque"]; !got.Equal(spawn) {
		t.Errorf("cat-opaque last seen = %s, want %s", got, spawn)
	}
	if _, ok := idx.LastSeen["cat-never-heard-of"]; ok {
		t.Error("an identity the log never mentions is present in the index")
	}
}

// TestSchedulerTrafficCannotKeepAWedgedAgentsClockWarm is the mg-fc8d failure
// mode one level down, and the reason this index keys on the event's own agent
// field rather than on anything addressed AT an identity.
//
// pogod fires a mail-check at every polecat every ten minutes and records it —
// 64,194 `scheduler_fire_delivered` lines on this box. A delivery proves the
// SENDER ran. If those counted as the receiver's recency, a wedged agent would
// read as active within ten minutes forever, which is precisely the mistake
// `last-activity` made with PTY animation for thirteen hours. They are logged
// against `pogod`, and this pins that they stay out of the index.
func TestSchedulerTrafficCannotKeepAWedgedAgentsClockWarm(t *testing.T) {
	writeFixtureLog(t, []events.Event{
		ev("cat-opaque", "agent_spawned", t0.Add(-9*time.Hour)),
		ev("pogod", "scheduler_fire_delivered", t0.Add(-time.Minute)),
		ev("pogod", "scheduler_fire_completed", t0.Add(-30*time.Second)),
	})

	idx := SystemEvents()
	if !idx.Readable {
		t.Fatalf("SystemEvents unreadable: %s", idx.Reason)
	}
	if got := idx.LastSeen["cat-opaque"]; !got.Equal(t0.Add(-9 * time.Hour)) {
		t.Errorf("cat-opaque last seen = %s, want its 9h-old spawn line. pogod's scheduler traffic was "+
			"credited to the agent it was delivered to, which would make every wedged polecat read "+
			"as active within one mail-check interval, forever.", got)
	}
}

// TestAMissingEventLogIsUnreadableNotEmpty pins the distinction at the reader,
// where it originates. An empty index and an unreadable one lead to opposite
// blind messages, and only one of them is a claim about the log's contents.
func TestAMissingEventLogIsUnreadableNotEmpty(t *testing.T) {
	got := eventsIndexFrom(filepath.Join(t.TempDir(), "events.log"))
	if got.Readable {
		t.Error("a nonexistent event log read as a successful scan that found nothing")
	}
	if got.Reason != ReasonEventLogAbsent {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonEventLogAbsent)
	}
}
