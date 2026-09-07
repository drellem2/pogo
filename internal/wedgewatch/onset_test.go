package wedgewatch

import (
	"testing"
	"time"
)

// The 2026-09-07 fleet-wide poisoned-credential wedge, and the reading that was
// taken off it (mg-3222).
//
// # What the log holds
//
// The refresh grant lapsed at 10:39:14Z — observed, not inferred:
// `cred_expiry_warned` at 10:43:40Z carries tier=lapsed, remaining="already
// lapsed", expires_at=2026-09-07T10:39:14Z. The six crew agents took their last
// real turns between 10:46:10Z and 10:53:49Z and resumed between 16:22:23Z and
// 16:34:15Z. In between, from `~/.pogo/events.log`:
//
//	10:49:40Z  wedge_watch_pending  crew-mayor …
//	11:00:40Z  wedge_watch_fired    agents=[mayor]                cause=poisoned_credential
//	11:06:10Z  wedge_watch_fired    agents=[all six]              cause=poisoned_credential
//	11:22:10Z  synthetic_failure_detected  crew-mayor  "Login expired · Please run /login"  (#1 of 23)
//	  … sixteen more wedge_watch_fired, every one carrying "routed_to": "nobody" …
//	14:56:10Z  wedge_watch_fired    agents=[all six]              cause=poisoned_credential
//	16:19:14Z  wedge_watch_cleared  crew-mayor                    cause=poisoned_credential
//	16:19:14Z  wedge_watch_fired    agents=[five]                 cause=unknown
//
// mg-3222 was filed on the last two lines, read as one event: "wedge_watch
// fired with cause=poisoned_credential and cleared at the same instant, ~5h20m
// after the stop". They are two events about two different things. The 16:19:14Z
// FIRED line is a DE-ESCALATION — `cred_refresh_valid` flipped false→true in that
// sample and the cause fell to `unknown`; the word poisoned_credential on that
// timestamp comes from the CLEARED line, which names the cause of the finding
// being retired for one agent.
//
// The same shape holds for the ticket's second claim, that an upstream signal had
// the answer 15 minutes early: `synthetic_failure_detected` first fired at
// 11:22:10Z, 21 minutes BEHIND wedge_watch, and the 16:03:44Z pair the ticket
// quotes are #22 and #23 of the 23 emitted that day.
//
// The detector's actual latency, from mayor's last turn at 10:46:10Z to the
// first `wedge_watch_fired`, is 14m30s — the marker hold-down (10m) plus the
// sampling interval (5m), which is what those constants are for. The ~5h20m is
// the credential's: pogod read the keychain live on every one of those samples
// (internal/credexpiry shells out to `security` per call and caches nothing) and
// got the LAPSED grant every time up to 14:56:10Z, and the renewed one — good
// until 2026-10-06T10:52:13Z — by 16:19:14Z.
//
// # What was actually missing
//
// Not detection. Two things: `"routed_to": "nobody"`, which is mg-fc8d item (3)
// and deliberately unbuilt; and the fact that NOTHING ON AN EMISSION SAID WHEN
// THE CONDITION STARTED. Every field advanced with the sample, and record()
// re-emits on every roster CHANGE — so reading backwards from the recovery lands
// on the last transition, and the last transition of a five-hour incident looks
// exactly like its first. These tests pin the fields that close that gap.

// sep7 is 10:49:40Z: the sample at which the fleet's counters were first
// observed frozen. Everything below is measured from it, so the numbers in the
// assertions are the numbers in the log.
var sep7 = time.Date(2026, 9, 7, 10, 49, 40, 0, time.UTC)

// lapsedCredAt is the credential as pogod actually read it for the first four
// hours of the wedge: readable, and saying of itself that the refresh grant went
// out of date at 10:39:14Z. It is the ONLY input that lets Classify name
// poisoned_credential — the cause is never inferred from the 401.
func lapsedCredAt(time.Time) CredentialView {
	return CredentialView{
		Readable:      true,
		RefreshValid:  false,
		RefreshExpiry: time.Date(2026, 9, 7, 10, 39, 14, 0, time.UTC),
	}
}

// firedAt returns the emitted wedge_watch_fired events in order, with their
// timestamps parsed.
func firedEvents(t *testing.T, rec *recorder) []struct {
	At      time.Time
	Details map[string]any
} {
	t.Helper()
	var out []struct {
		At      time.Time
		Details map[string]any
	}
	for _, e := range rec.ofType(EventFired) {
		at, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			t.Fatalf("wedge_watch_fired carried an unparseable timestamp %q: %v", e.Timestamp, err)
		}
		out = append(out, struct {
			At      time.Time
			Details map[string]any
		}{At: at, Details: e.Details})
	}
	return out
}

func causeSinceOf(t *testing.T, details map[string]any, cause Cause) time.Time {
	t.Helper()
	raw, ok := details["cause_since"].(map[string]string)
	if !ok {
		t.Fatalf("wedge_watch_fired has no cause_since map; details keys present: %v", keysOf(details))
	}
	s, ok := raw[string(cause)]
	if !ok {
		t.Fatalf("cause_since has no entry for %s; it holds %v", cause, raw)
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("cause_since[%s] = %q, which does not parse: %v", cause, s, err)
	}
	return at
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// wedgedFleet scripts the two agents this file needs: one that stays frozen for
// the whole incident, and one whose counter twitches partway through.
//
// The twitch is not decoration. A session that cannot authenticate does not
// hang — it COMPLETES turns, in about ten milliseconds, each one ending "Login
// expired · Please run /login" — and a completed turn moves the declared-work
// counter it is being judged by. That is why the live roster bounced sixteen
// times between 11:00Z and 14:56Z rather than holding six agents steady, and it
// is exactly what defeats a per-agent onset clock.
func wedgedFleet(twitchAt, twitchFor time.Duration) func(now time.Time) ([]Observation, CredentialView) {
	return func(now time.Time) ([]Observation, CredentialView) {
		elapsed := now.Sub(sep7)
		mayorDeclared := "1m 8s"
		if elapsed >= twitchAt && elapsed < twitchAt+twitchFor {
			mayorDeclared = "4s"
		}
		return []Observation{
			{
				Name: "mayor", Identity: "crew-mayor", Type: "crew", Alive: true,
				Uptime: 149*time.Hour + elapsed, Output: wedgedLoginPTY(mayorDeclared), LastOutputAt: now,
			},
			{
				Name: "architect", Identity: "crew-architect", Type: "crew", Alive: true,
				Uptime: 149*time.Hour + elapsed, Output: wedgedLoginPTY("1m 2s"), LastOutputAt: now,
			},
		}, lapsedCredAt(now)
	}
}

// TestThePoisonedCredentialWedgeIsNamedInMinutesNotHours is the measurement
// mg-3222 inverted, run against the code that produced the log it was read from.
//
// The ticket's headline is ~5h20m of detector latency. The floor the constants
// impose is MarkerHoldDown + one sampling Interval, and nothing in the wedge
// path adds to it: the counter freeze is observed on the first sample, the
// hold-down runs from there, and the finding is emitted on the sample that
// clears it. 10m + 5m = 15m, and the live event log agrees to the second —
// mayor's last turn 10:46:10Z, first `wedge_watch_fired` 11:00:40Z, 14m30s.
func TestThePoisonedCredentialWedgeIsNamedInMinutesNotHours(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: wedgedFleet(0, 0)}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	for elapsed := time.Duration(0); elapsed <= 6*time.Hour; elapsed += 5 * time.Minute {
		w.Check(sep7.Add(elapsed))
	}

	fired := firedEvents(t, rec)
	if len(fired) == 0 {
		t.Fatalf("no wedge_watch_fired in six hours of a fleet-wide login-prompt wedge on a lapsed " +
			"credential — the detector did not fire on the incident it is named for")
	}
	latency := fired[0].At.Sub(sep7)
	if latency > 15*time.Minute {
		t.Fatalf("first wedge_watch_fired at +%s. The bound is MarkerHoldDown(10m) + Interval(5m) = 15m; "+
			"anything larger means something in the wedge path is adding delay, which is the claim "+
			"mg-3222 made (~5h20m) and the live log refutes (14m30s: last turn 10:46:10Z, fired "+
			"11:00:40Z)", latency)
	}
	agents, _ := fired[0].Details["agents"].([]string)
	if len(agents) != 2 {
		t.Errorf("first emission named %v; both wedged agents should be in it", agents)
	}

	// cred_readable is the CONTROL for cred_refresh_valid, and an emission
	// carrying the second without the first is uninterpretable.
	//
	// pm-onethird re-derived this incident independently and produced the
	// objection that nearly broke it: cred_refresh_valid also reads false at
	// 00:06:09Z, 03:45:39Z and 08:53:40Z on the same day — hours BEFORE the
	// 10:39:14Z lapse, with cred_expiry_warned at 03:07:09Z (tier=24h,
	// remaining="7h 32m") proving the grant was good then. Those are
	// cred_readable=FALSE: unreadable-defaults, not negative readings. Only the
	// sixteen readable-and-invalid samples from 11:00:40Z to 14:56:10Z are a
	// measurement. CredentialView.Readable exists so that "I could not look" is
	// never rendered as "fine", and this pins that both halves reach the log
	// together — the split a re-deriver has to make first.
	if got := fired[0].Details["cred_readable"]; got != true {
		t.Errorf("cred_readable = %v, want true. Without it on the line, cred_refresh_valid=false "+
			"cannot be told from 'the credential was not inspected', and eight such samples earlier "+
			"the same day would read as evidence the grant had lapsed before it had", got)
	}
	if got := fired[0].Details["cred_refresh_valid"]; got != false {
		t.Errorf("cred_refresh_valid = %v, want false", got)
	}
	findings, _ := w.Latest()
	for _, f := range findings {
		if f.Cause != CausePoisonedCredential {
			t.Errorf("%s: cause = %s, want %s. The credential itself reported its refresh grant "+
				"lapsed at 2026-09-07T10:39:14Z, which is the only evidence that licenses this cause",
				f.Name, f.Cause, CausePoisonedCredential)
		}
		if f.Response != ResponseStopAndRedispatch {
			t.Errorf("%s: response = %s, want %s", f.Name, f.Response, ResponseStopAndRedispatch)
		}
	}
}

// TestALaterEmissionStillDatesTheIncidentToItsOnset is the fix for the actual
// defect, stated as the reading that went wrong.
//
// A reader who finds the LAST emission before a recovery must be able to tell,
// from that emission alone, that the condition had been standing for five hours.
// Before this, every field on it advanced with the sample and the emission
// timestamp was the only date on the line — so the last transition of a long
// incident was indistinguishable from a fresh one, which is how 14m30s was
// reported as 5h20m.
func TestALaterEmissionStillDatesTheIncidentToItsOnset(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: wedgedFleet(0, 0)}
	w := New(Options{
		Enabled: true, Source: fleet.source, Emit: rec.emit,
		Interval: 5 * time.Minute,
		// Shorter than the 6h default purely so an unchanging roster re-emits
		// inside the test's span. It does not affect what is being pinned.
		RenotifyAfter: time.Hour,
	})

	for elapsed := time.Duration(0); elapsed <= 5*time.Hour+30*time.Minute; elapsed += 5 * time.Minute {
		w.Check(sep7.Add(elapsed))
	}

	fired := firedEvents(t, rec)
	if len(fired) < 2 {
		t.Fatalf("want at least two emissions over 5h30m with RenotifyAfter=1h, got %d", len(fired))
	}
	first, last := fired[0], fired[len(fired)-1]
	if last.At.Sub(first.At) < 4*time.Hour {
		t.Fatalf("the last emission is only %s after the first; this test needs the wide separation "+
			"that made the misreading possible", last.At.Sub(first.At))
	}

	onset := causeSinceOf(t, last.Details, CausePoisonedCredential)
	if !onset.Equal(first.At) {
		t.Errorf("cause_since on the emission at %s = %s, want the onset %s. An emission that cannot "+
			"be dated to the start of its own condition is what mg-3222 was derived from",
			last.At.Format(time.RFC3339), onset.Format(time.RFC3339), first.At.Format(time.RFC3339))
	}
	if got, _ := last.Details["reported_for"].(string); got != last.At.Sub(first.At).Round(time.Second).String() {
		t.Errorf("reported_for = %q, want %q", got, last.At.Sub(first.At).Round(time.Second).String())
	}
	if _, ok := last.Details["reported_since"].(string); !ok {
		t.Errorf("no reported_since on the emission; details keys: %v", keysOf(last.Details))
	}
	if caveat, _ := last.Details["onset_caveat"].(string); caveat == "" {
		t.Errorf("no onset_caveat on the emission. The clocks are in-memory floors and the emission " +
			"must say so, or a reader checks one against a pogod restart and stops trusting the rest")
	}
}

// TestAFlappingAgentDoesNotRedateTheIncident is why there are two clocks.
//
// On 2026-09-07 the roster bounced sixteen times: each agent's failing turns
// completed in milliseconds and moved the counter it was judged by, so agents
// left the roster and came back. A per-agent clock alone dates such an incident
// to the most recent bounce — 1h23m, in the live case — which is a smaller
// version of the same error mg-3222 made. cause_since is what survives it,
// because the fleet held at least one poisoned_credential finding in every
// single sample across the whole window.
func TestAFlappingAgentDoesNotRedateTheIncident(t *testing.T) {
	rec := &recorder{}
	// mayor's counter twitches for one sample at +2h, exactly as a completed
	// auth-failure turn moves it. architect stays frozen throughout.
	fleet := &scriptedFleet{host: roomyHost, at: wedgedFleet(2*time.Hour, 5*time.Minute)}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	for elapsed := time.Duration(0); elapsed <= 4*time.Hour; elapsed += 5 * time.Minute {
		w.Check(sep7.Add(elapsed))
	}

	fired := firedEvents(t, rec)
	if len(fired) < 3 {
		t.Fatalf("want the onset emission plus the two the flap produces, got %d", len(fired))
	}
	onsetAt := fired[0].At

	// The flap must actually have happened, or this test proves nothing.
	cleared := rec.ofType(EventCleared)
	if len(cleared) == 0 {
		t.Fatalf("mayor never left the roster; the counter twitch that drives this test did not " +
			"clear the finding, so the flap it is about was never exercised")
	}

	last := fired[len(fired)-1]
	if onset := causeSinceOf(t, last.Details, CausePoisonedCredential); !onset.Equal(onsetAt) {
		t.Errorf("cause_since = %s after a mid-incident flap, want the original onset %s. The cause "+
			"clock is the one that must survive an agent dropping out",
			onset.Format(time.RFC3339), onsetAt.Format(time.RFC3339))
	}

	// And the per-agent clock is pinned to the OPPOSITE behaviour, so the two
	// are never confused: it dates the agent's current reported run, and a
	// clear resets it. Both facts are load-bearing and both are stated on the
	// emission.
	byName := map[string]map[string]any{}
	for _, row := range last.Details["findings"].([]map[string]any) {
		byName[row["name"].(string)] = row
	}
	arch, ok := byName["architect"]
	if !ok {
		t.Fatalf("architect is missing from the final emission: %v", byName)
	}
	if got := arch["first_reported_at"]; got != onsetAt.UTC().Format(time.RFC3339Nano) {
		t.Errorf("architect first_reported_at = %v, want the onset %s — it never left the roster",
			got, onsetAt.UTC().Format(time.RFC3339Nano))
	}
	mayor, ok := byName["mayor"]
	if !ok {
		t.Fatalf("mayor is missing from the final emission: %v", byName)
	}
	if got := mayor["first_reported_at"]; got == onsetAt.UTC().Format(time.RFC3339Nano) {
		t.Errorf("mayor first_reported_at = %v, but mayor left the roster and came back — the "+
			"per-agent clock dates the CURRENT run and must have reset. If it survives a clear it "+
			"is claiming continuous observation it did not have", got)
	}
}

// TestClearedIsNotAnAllClear pins the other half of the 16:19:14Z misreading.
//
// `wedge_watch_cleared` names the cause of the finding it is RETIRING, for ONE
// agent. Read beside a `wedge_watch_fired` at the same instant it looks like a
// fleet-wide release, which is what it was taken for. The event now says whose
// it is, how long that agent had been reported, and — in the `why` — that it is
// not a statement about the underlying condition at all.
func TestClearedIsNotAnAllClear(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: wedgedFleet(90*time.Minute, 10*time.Minute)}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	for elapsed := time.Duration(0); elapsed <= 3*time.Hour; elapsed += 5 * time.Minute {
		w.Check(sep7.Add(elapsed))
	}

	cleared := rec.ofType(EventCleared)
	if len(cleared) == 0 {
		t.Fatalf("no wedge_watch_cleared; the flap this test is about did not happen")
	}
	c := cleared[0]
	if got := c.Details["target"]; got != "mayor" {
		t.Errorf("cleared target = %v, want mayor", got)
	}
	if got := c.Details["cause"]; got != string(CausePoisonedCredential) {
		t.Errorf("cleared cause = %v, want %s — it names what is being retired", got, CausePoisonedCredential)
	}
	first, ok := c.Details["first_reported_at"].(string)
	if !ok || first == "" {
		t.Fatalf("wedge_watch_cleared carries no first_reported_at, so an all-clear still cannot be "+
			"dated without pairing it against an earlier emission by hand. keys: %v", keysOf(c.Details))
	}
	if got, ok := c.Details["reported_for"].(string); !ok || got == "" {
		t.Errorf("wedge_watch_cleared carries no reported_for")
	}
	why, _ := c.Details["why"].(string)
	if why == "the agent's declared work counter advanced again, or the dead-end marker left the screen" {
		t.Errorf("the cleared `why` is still the bare pre-mg-3222 sentence. It has to say that this " +
			"is about ONE AGENT leaving the roster and not about the condition ending, because a " +
			"failing session moves its own counter and that is what produced this event")
	}

	// The fleet is still wedged at the moment of the clear: architect never
	// left. An all-clear reading of this event is therefore false, and the
	// emission at the same instant proves it.
	findings, _ := w.Latest()
	if len(findings) == 0 {
		t.Errorf("the roster emptied; this test needs a still-wedged fleet for the clear to be " +
			"misreadable as a release")
	}
}

// TestTheOnsetClockUnderstatesRatherThanOverstates pins the direction of the
// only error the cause clock can make.
//
// It is in-memory and it is dropped the first time a sample carries no finding
// under that cause, so a condition that genuinely goes quiet and returns is
// re-dated to the return. That makes every age it reports a FLOOR. The
// alternative — carrying a cause across an observed-clean sample — would let the
// field assert continuous observation it did not have, which is the failure mode
// this whole ticket is about, one level down.
func TestTheOnsetClockUnderstatesRatherThanOverstates(t *testing.T) {
	rec := &recorder{}
	healed := 90 * time.Minute
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		elapsed := now.Sub(sep7)
		if elapsed >= healed && elapsed < healed+30*time.Minute {
			// Genuinely working: no marker, and a counter that advances.
			return []Observation{{
				Name: "mayor", Identity: "crew-mayor", Type: "crew", Alive: true,
				Uptime: 149*time.Hour + elapsed, Output: workingPTY("1m " + elapsed.Round(time.Second).String()),
				LastOutputAt: now,
			}}, lapsedCredAt(now)
		}
		return []Observation{{
			Name: "mayor", Identity: "crew-mayor", Type: "crew", Alive: true,
			Uptime: 149*time.Hour + elapsed, Output: wedgedLoginPTY("1m 8s"), LastOutputAt: now,
		}}, lapsedCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	for elapsed := time.Duration(0); elapsed <= 4*time.Hour; elapsed += 5 * time.Minute {
		w.Check(sep7.Add(elapsed))
	}

	fired := firedEvents(t, rec)
	if len(fired) < 2 {
		t.Fatalf("want an emission on each side of the healthy window, got %d", len(fired))
	}
	last := fired[len(fired)-1]
	onset := causeSinceOf(t, last.Details, CausePoisonedCredential)
	if !onset.After(sep7.Add(healed)) {
		t.Errorf("cause_since = %s, which predates the 30 minutes in which NO agent was reported "+
			"under this cause. The clock must restart there: carrying it across would claim "+
			"continuous observation the detector did not have", onset.Format(time.RFC3339))
	}
}
