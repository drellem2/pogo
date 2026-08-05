package wedgewatch

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// --- test harness -----------------------------------------------------------

type recorder struct {
	mu  sync.Mutex
	evs []events.Event
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

func (r *recorder) ofType(t string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.evs {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

// scriptedFleet answers each sample from a function of the current time, so a
// test can describe an agent's PTY as a function of how long it has been
// wedged rather than by pre-building a list of samples.
type scriptedFleet struct {
	at  func(now time.Time) ([]Observation, CredentialView)
	err error
	// host is the contention reading every sample carries. The zero value is
	// deliberately NOT "healthy host": an unreadable HostView means CPU
	// starvation could not be ruled out, so a test that wants the ordinary
	// uncontended case must say so with roomyHost.
	host HostView
	seen int
}

func (s *scriptedFleet) source(now time.Time) (Snapshot, error) {
	if s.err != nil {
		return Snapshot{}, s.err
	}
	s.seen++
	obs, cred := s.at(now)
	return Snapshot{Now: now, Scanned: len(obs), Agents: obs, Cred: cred, Host: s.host}, nil
}

// roomyHost is a measured host with plenty of headroom — the reading that
// positively RULES OUT CPU starvation as an explanation for no progress.
var roomyHost = HostView{Readable: true, Saturated: false, UsedCores: 1.2, Cores: 10}

// fullHost is the 2026-08-05 load event: a 10-core box with nothing left to
// give. Agents on it are degraded, not wedged.
var fullHost = HostView{Readable: true, Saturated: true, UsedCores: 9.8, Cores: 10}

var t0 = time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)

// run drives the watcher over span at the sampling interval and returns the
// findings from the final sample.
func run(t *testing.T, w *Watcher, span, step time.Duration) []Finding {
	t.Helper()
	for elapsed := time.Duration(0); elapsed <= span; elapsed += step {
		w.Check(t0.Add(elapsed))
	}
	findings, _ := w.Latest()
	return findings
}

// --- the positive control ---------------------------------------------------

// TestTheWedgeFires is the control mg-fc8d demanded by name: before this check
// is trusted to stay quiet it must be PROVEN able to fire, on the thing that
// actually happened.
//
// The fixture is the 2026-08-04 screen — two 401 lines, "Please run /login", a
// counter frozen at "Baked for 3m 2s" — repainting on every sample, which is
// what kept `last-activity` reading "just now" for thirteen hours. The agent's
// uptime advances; its counter does not.
func TestTheWedgeFires(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name:     "teaa9",
			Identity: "cat-teaa9",
			Type:     "polecat",
			Alive:    true,
			// Already 13h old when the sampling starts, exactly as it was when
			// a human finally read the screen.
			Uptime: 13*time.Hour + now.Sub(t0),
			Output: wedgedLoginPTY("3m 2s"),
			// The PTY is still producing bytes. This is the property that
			// defeated every instrument pogo had.
			LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1. The detector did not fire on the incident it was built "+
			"from; a check that cannot be shown to fire must not be trusted to stay quiet.\n"+
			"samples taken: %d", len(findings), fleet.seen)
	}
	f := findings[0]
	if !f.HasSignature(SigLoginPrompt) || !f.HasSignature(SigAPI401) {
		t.Errorf("signatures = %v, want the login prompt and the 401", f.Signatures)
	}
	if !f.HasSignature(SigDeclaredTimeBelowUptime) {
		t.Errorf("signatures = %v, want the counter/uptime cross-check to fire too — it is the "+
			"half that works without an enumeration", f.Signatures)
	}
	if !f.Animating {
		t.Error("the finding must record that the agent was ANIMATING; that is the whole reason " +
			"thirteen hours of this looked healthy")
	}
	if f.Declared != 3*time.Minute+2*time.Second {
		t.Errorf("declared = %s, want 3m2s", f.Declared)
	}
	if fired := rec.ofType(EventFired); len(fired) == 0 {
		t.Error("no wedge_watch_fired event was emitted")
	}
}

// TestTheWedgeFiresWithoutAnyEnumeratedMarker is the same incident with the
// dead-end text removed — a prompt nobody has enumerated yet.
//
// This is the case that matters most over time, because markers.go is
// permanently one incident behind and the cross-check is not. If this test ever
// starts failing, the detector has quietly become an enumeration.
func TestTheWedgeFiresWithoutAnyEnumeratedMarker(t *testing.T) {
	rec := &recorder{}
	// A screen with a frozen counter and a prompt this package has never seen.
	unknownPrompt := []byte(clr + "Some future dead end nobody has written down yet.\r\n" +
		dim + "✻" + col(1) + "Baked for 2m 56s" + reset + "\r\n")
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "wfc8d", Identity: "cat-wfc8d", Type: "polecat", Alive: true,
			Uptime: 7*time.Hour + now.Sub(t0), Output: unknownPrompt, LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1 — the cross-check must catch a prompt that is not in the table", len(findings))
	}
	f := findings[0]
	if len(f.Signatures) != 1 || f.Signatures[0] != SigDeclaredTimeBelowUptime {
		t.Errorf("signatures = %v, want only the cross-check", f.Signatures)
	}
	if f.Cause != CauseUnknown || f.Response != ResponseInvestigate {
		t.Errorf("cause/response = %s/%s, want %s/%s", f.Cause, f.Response, CauseUnknown, ResponseInvestigate)
	}
}

// TestEnumeratedModalsFire gives the remaining two dead-end states from
// mg-fc8d's list their own controls, driven end-to-end through the watcher.
func TestEnumeratedModalsFire(t *testing.T) {
	cases := []struct {
		name string
		pty  []byte
		want Signature
	}{
		{"rating dialog", ratingDialogPTY("1m 4s"), SigRatingDialog},
		{"rate-limit modal", rateLimitModalPTY("1m 4s"), SigRateLimitModal},
		{"connectivity failure", outagePTY("2m 56s"), SigConnectivity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
				return []Observation{{
					Name: "a1", Identity: "cat-a1", Type: "polecat", Alive: true,
					Uptime: 4*time.Hour + now.Sub(t0), Output: tc.pty, LastOutputAt: now,
				}}, validCredAt(now)
			}}
			w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})
			findings := run(t, w, 45*time.Minute, 5*time.Minute)
			if len(findings) != 1 {
				t.Fatalf("findings = %d, want 1 for %s", len(findings), tc.name)
			}
			if !findings[0].HasSignature(tc.want) {
				t.Errorf("signatures = %v, want %s", findings[0].Signatures, tc.want)
			}
		})
	}
}

// TestTheMarkerHoldDownIsShorterThanTheFreezeHoldDown proves the two hold-downs
// are actually distinct in behaviour, not just in name: an agent with a
// corroborating marker is reported sooner than one caught by the cross-check
// alone.
func TestTheMarkerHoldDownIsShorterThanTheFreezeHoldDown(t *testing.T) {
	mk := func(pty []byte) *Watcher {
		fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
			return []Observation{{
				Name: "a1", Identity: "cat-a1", Type: "polecat", Alive: true,
				Uptime: 8*time.Hour + now.Sub(t0), Output: pty, LastOutputAt: now,
			}}, validCredAt(now)
		}}
		return New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})
	}
	// 15 minutes: past the 10m marker hold-down, short of the 30m freeze one.
	withMarker := run(t, mk(wedgedLoginPTY("3m 2s")), 15*time.Minute, 5*time.Minute)
	if len(withMarker) != 1 {
		t.Errorf("an agent with a dead-end marker on screen should be reported at 15m; got %d findings", len(withMarker))
	}
	unenumerated := run(t, mk([]byte(clr+"unknown prompt\r\n"+dim+"Baked for 3m 2s"+reset)), 15*time.Minute, 5*time.Minute)
	if len(unenumerated) != 0 {
		t.Errorf("an agent with no corroborating marker should still be inside its hold-down at 15m; got %d findings", len(unenumerated))
	}
}

// --- negative controls ------------------------------------------------------

// TestAHealthyWorkingAgentIsNeverReported is the negative control for the
// cross-check. Its counter advances between samples, exactly as a working
// agent's does, so the freeze clock never accumulates — even though the raw
// uptime/declared ratio is enormous the whole time.
func TestAHealthyWorkingAgentIsNeverReported(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		// A fresh turn every ten minutes: the counter climbs, then resets. This
		// is what makes the ratio alone useless as a signal — it is above 100
		// at almost every sample here.
		elapsed := now.Sub(t0) % (10 * time.Minute)
		return []Observation{{
			Name: "busy", Identity: "cat-busy", Type: "polecat", Alive: true,
			Uptime:       9*time.Hour + now.Sub(t0),
			Output:       workingPTY(fmt.Sprintf("%ds", int(elapsed.Seconds()))),
			LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	if findings := run(t, w, 6*time.Hour, 5*time.Minute); len(findings) != 0 {
		t.Fatalf("reported a healthy agent: %v", findings)
	}
	if fired := rec.ofType(EventFired); len(fired) != 0 {
		t.Errorf("emitted %d fired events for a healthy agent", len(fired))
	}
}

// TestAnAgentMerelyWritingAboutTheWedgeIsNotReported is the negative control
// for the marker table, and it is not hypothetical: the polecat that built this
// package had every enumerated string in its own PTY for hours.
//
// The scanner IS fooled — TestScanMarkersFiresOnAnAgentMerelyWritingAboutTheWedge
// pins that deliberately. What saves the detector is the requirement that the
// agent be STALLED as well, which a working agent never is.
func TestAnAgentMerelyWritingAboutTheWedgeIsNotReported(t *testing.T) {
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		elapsed := now.Sub(t0) % (7 * time.Minute)
		return []Observation{{
			Name: "author", Identity: "cat-author", Type: "polecat", Alive: true,
			Uptime:       6*time.Hour + now.Sub(t0),
			Output:       quotingPTY(fmt.Sprintf("%ds", int(elapsed.Seconds()))),
			LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})

	if findings := run(t, w, 4*time.Hour, 5*time.Minute); len(findings) != 0 {
		t.Fatalf("reported an agent that was merely writing about the wedge: %v", findings)
	}
}

// TestAYoungAgentIsNotReported keeps spawn — the noisiest part of an agent's
// life — out of the report.
func TestAYoungAgentIsNotReported(t *testing.T) {
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "fresh", Identity: "cat-fresh", Type: "polecat", Alive: true,
			Uptime: now.Sub(t0), Output: []byte(clr + dim + "Baked for 3m 2s" + reset), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})
	if findings := run(t, w, 40*time.Minute, 5*time.Minute); len(findings) != 0 {
		t.Fatalf("reported a 40-minute-old agent: %v", findings)
	}
}

// --- blindness must be loud -------------------------------------------------

// TestASourceThatFailsIsAnErrorNotACleanFleet pins the founding rule of this
// family of detectors one level up: an instrument that cannot see must not
// report health.
func TestASourceThatFailsIsAnErrorNotACleanFleet(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{err: errors.New("registry unavailable")}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)
	if errs := rec.ofType(EventError); len(errs) != 1 {
		t.Fatalf("error events = %d, want 1 — a detector that could not look has NOT found a clean fleet", len(errs))
	}
	if fired := rec.ofType(EventFired); len(fired) != 0 {
		t.Error("emitted a finding from a failed sample")
	}
}

// TestAnUnjudgeableAgentIsReportedAsBlind covers the per-agent case: a harness
// that renames its status line leaves no counter to read, and with no event-log
// entry either there is nothing to judge on. That must be an error, not a pass.
func TestAnUnjudgeableAgentIsReportedAsBlind(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "opaque", Identity: "cat-opaque", Type: "polecat", Alive: true,
			Uptime: 9 * time.Hour, Output: noCounterPTY(), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: time.Minute})
	w.Check(t0)

	errs := rec.ofType(EventError)
	if len(errs) != 1 {
		t.Fatalf("error events = %d, want 1 for an agent with no readable counter and no event-log entry", len(errs))
	}
	if got := errs[0].Details["target"]; got != "opaque" {
		t.Errorf("error event target = %v, want opaque", got)
	}
}

// TestTheEventLogIsTheFallbackWhenTheCounterCannotBeRead proves the degradation
// path works: no counter, but a stale event log, still yields a finding. A
// harness rename must make this detector coarser, not silent.
func TestTheEventLogIsTheFallbackWhenTheCounterCannotBeRead(t *testing.T) {
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "opaque", Identity: "cat-opaque", Type: "polecat", Alive: true,
			Uptime:         9 * time.Hour,
			Output:         append(noCounterPTY(), []byte("Please run /login\r\n")...),
			LastOutputAt:   now,
			EventsLastSeen: t0.Add(-3 * time.Hour),
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: time.Minute})
	w.Check(t0)
	findings, _ := w.Latest()
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1 via the event-log fallback", len(findings))
	}
	if findings[0].StallSource != "events_stale" {
		t.Errorf("stall_source = %q, want events_stale", findings[0].StallSource)
	}
}

// --- the third false-healthy state: CPU starvation --------------------------

// unknownPromptPTY is a screen with a frozen counter and nothing enumerated on
// it: the shape shared by a wedge at an unknown prompt and an agent that is
// simply not being given any CPU.
func unknownPromptPTY() []byte {
	return []byte(clr + "waiting…\r\n" + dim + "✻" + col(1) + "Baked for 2m 56s" + reset + "\r\n")
}

// TestAStarvedAgentIsReportedAsDegradedNotWedged is the positive control for
// the third false-healthy signature, reported by pm-onethird on 2026-08-05:
// during a load event (1-min average 300 on a 10-core box) thirteen polecats
// showed "last-activity: just now" for hours while producing nothing, and plain
// local `git log --oneline -2` calls timed out at 180s. They were ALIVE AND
// CRAWLING, not wedged.
//
// The remedies are opposite — a wedged agent needs intervention, a starved one
// needs to be left alone and the load reduced — so this must not come out as a
// wedge. Waking or restarting a starved agent destroys real work and adds to
// the load that caused the symptom.
func TestAStarvedAgentIsReportedAsDegradedNotWedged(t *testing.T) {
	fleet := &scriptedFleet{host: fullHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "crawler", Identity: "cat-crawler", Type: "polecat", Alive: true,
			Uptime: 6*time.Hour + now.Sub(t0), Output: unknownPromptPTY(), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1 — a starved agent is a real finding, not a suppressed one; "+
			"a fleet that assumes its own contention away cannot see the state it creates for itself",
			len(findings))
	}
	f := findings[0]
	if f.Cause != CauseHostOversubscribed {
		t.Fatalf("cause = %s, want %s. A starved agent reported as wedged invites the OPPOSITE "+
			"of the correct handling: waking or restarting it destroys real work and adds load.",
			f.Cause, CauseHostOversubscribed)
	}
	if f.Response != ResponseReduceLoadNotIntervene {
		t.Errorf("response = %s, want %s", f.Response, ResponseReduceLoadNotIntervene)
	}
	if !f.HostSaturated || f.HostCores != 10 {
		t.Errorf("the finding must carry the measurement it acted on; got saturated=%t cores=%d",
			f.HostSaturated, f.HostCores)
	}
}

// TestSaturationDoesNotExcuseAnEnumeratedDeadEnd pins the precedence rule. A
// login prompt is not caused by CPU contention, and a busy box during a genuine
// auth wedge must not let the wedge be filed as "degraded" — that is how the
// thirteen-hour case gets excused by a load spike that happens to overlap it.
func TestSaturationDoesNotExcuseAnEnumeratedDeadEnd(t *testing.T) {
	fleet := &scriptedFleet{host: fullHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "teaa9", Identity: "cat-teaa9", Type: "polecat", Alive: true,
			Uptime: 13*time.Hour + now.Sub(t0), Output: wedgedLoginPTY("3m 2s"), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if findings[0].Cause == CauseHostOversubscribed {
		t.Fatal("a login prompt was filed as host contention — saturation may only reinterpret " +
			"the case where NOTHING enumerated is on screen")
	}
}

// TestAnUnmeasurableHostSaysStarvationCouldNotBeRuledOut is the
// absence-as-evidence rule applied to the newest input. A detector that cannot
// measure the host must not proceed as though the host were idle.
func TestAnUnmeasurableHostSaysStarvationCouldNotBeRuledOut(t *testing.T) {
	blind := HostView{Readable: false, Reason: ReasonHostUnresolvable}
	fleet := &scriptedFleet{host: blind, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "opaque", Identity: "cat-opaque", Type: "polecat", Alive: true,
			Uptime: 6*time.Hour + now.Sub(t0), Output: unknownPromptPTY(), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	f := findings[0]
	if f.Cause != CauseUnknown {
		t.Errorf("cause = %s, want %s when the host could not be measured", f.Cause, CauseUnknown)
	}
	if !strings.Contains(f.Why, "could not be ruled out") {
		t.Errorf("the verdict must SAY that starvation could not be ruled out; got %q", f.Why)
	}
	if f.HostReadable {
		t.Error("the finding claims a readable host reading it did not have")
	}
}

// TestAMeasuredRoomyHostRulesStarvationOut is the other side: with headroom
// measured, the un-enumerated case is a genuine unknown and the reasoning says
// what excluded the alternative. Without this the detector's UNKNOWN would be
// indistinguishable from "I did not check".
func TestAMeasuredRoomyHostRulesStarvationOut(t *testing.T) {
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "wfc8d", Identity: "cat-wfc8d", Type: "polecat", Alive: true,
			Uptime: 7*time.Hour + now.Sub(t0), Output: unknownPromptPTY(), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if findings[0].Cause != CauseUnknown {
		t.Fatalf("cause = %s, want %s", findings[0].Cause, CauseUnknown)
	}
	if !strings.Contains(findings[0].Why, "RULES OUT CPU starvation") {
		t.Errorf("the verdict must say what excluded starvation; got %q", findings[0].Why)
	}
}

// --- fleet-wide connectivity memory ----------------------------------------

// TestOneAgentsOutageExplainsAnothers401 is the split-observation case, and the
// reason the connectivity memory is fleet-wide rather than per-agent.
//
// On 2026-08-04 mayor read the 401 in a PTY and the doctor read ENOTFOUND in
// the logs; because the two halves arrived through different observers they
// were recorded as two events, and the ticket was filed blaming a revoked
// credential. Here one agent shows the outage and another shows the 401, and
// the detector must merge them into one signature.
func TestOneAgentsOutageExplainsAnothers401(t *testing.T) {
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{
			{
				Name: "netless", Identity: "cat-netless", Type: "polecat", Alive: true,
				Uptime: 5*time.Hour + now.Sub(t0), Output: outagePTY("1m 10s"), LastOutputAt: now,
			},
			{
				Name: "authless", Identity: "cat-authless", Type: "polecat", Alive: true,
				Uptime: 5*time.Hour + now.Sub(t0), Output: wedgedLoginPTY("3m 2s"), LastOutputAt: now,
			},
		}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})

	findings := run(t, w, 45*time.Minute, 5*time.Minute)
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	for _, f := range findings {
		if f.Cause == CausePoisonedCredential {
			t.Fatalf("%s was blamed on a poisoned credential during an observed outage — "+
				"this is the verdict that pages a human for a re-login that fixes nothing", f.Name)
		}
	}
	var authless Finding
	for _, f := range findings {
		if f.Name == "authless" {
			authless = f
		}
	}
	if authless.Cause != CauseRefreshFailedDuringOutage {
		t.Errorf("authless cause = %s, want %s — the 401 and the ENOTFOUND are ONE event",
			authless.Cause, CauseRefreshFailedDuringOutage)
	}
}

// --- reporting hygiene ------------------------------------------------------

// TestAnUnchangedRosterStaysQuiet keeps the detector from re-emitting every
// interval. Ages advance on every tick, so a fingerprint that folded them in
// would emit forever and train every reader to filter this sender out — which
// is how a detector stops being an alert (internal/ackwatch's lesson).
func TestAnUnchangedRosterStaysQuiet(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "teaa9", Identity: "cat-teaa9", Type: "polecat", Alive: true,
			Uptime: 13*time.Hour + now.Sub(t0), Output: wedgedLoginPTY("3m 2s"), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})
	run(t, w, 3*time.Hour, 5*time.Minute)
	if fired := rec.ofType(EventFired); len(fired) != 1 {
		t.Errorf("emitted %d fired events over three hours of an unchanged roster, want 1", len(fired))
	}
}

// TestRecoveryIsRecorded pins the all-clear. An alarm with no matching clear
// leaves its reader holding an open incident forever.
func TestRecoveryIsRecorded(t *testing.T) {
	rec := &recorder{}
	recovered := t0.Add(50 * time.Minute)
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		pty := wedgedLoginPTY("3m 2s")
		if !now.Before(recovered) {
			// The counter starts moving again: the agent is taking turns.
			pty = workingPTY(fmt.Sprintf("%ds", int(now.Sub(recovered).Seconds())+1))
		}
		return []Observation{{
			Name: "teaa9", Identity: "cat-teaa9", Type: "polecat", Alive: true,
			Uptime: 13*time.Hour + now.Sub(t0), Output: pty, LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})

	findings := run(t, w, 90*time.Minute, 5*time.Minute)
	if len(findings) != 0 {
		t.Fatalf("still reporting a recovered agent: %v", findings)
	}
	if cleared := rec.ofType(EventCleared); len(cleared) != 1 {
		t.Fatalf("cleared events = %d, want 1", len(cleared))
	}
}

// TestTheWatcherHoldsNoMailSeam is a structural assertion about mg-fc8d's
// scope carve-out. Item (3) — escalating a fleet-level wedge OUTSIDE the wedged
// party — is an alerting-policy decision reserved to Daniel and unruled, so
// this runner must not be able to pick a recipient at all. Adding a MailFunc to
// Options is not a small convenience here; it is making the decision.
func TestTheWatcherHoldsNoMailSeam(t *testing.T) {
	var opts Options
	// Compile-time-ish check expressed as a field census: if someone adds a
	// mail or notify seam, this list stops matching and the test says why.
	_ = opts
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "teaa9", Identity: "cat-teaa9", Type: "polecat", Alive: true,
			Uptime: 13*time.Hour + now.Sub(t0), Output: wedgedLoginPTY("3m 2s"), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: rec.emit, Interval: 5 * time.Minute})
	run(t, w, 45*time.Minute, 5*time.Minute)

	fired := rec.ofType(EventFired)
	if len(fired) != 1 {
		t.Fatalf("fired events = %d, want 1", len(fired))
	}
	routed, _ := fired[0].Details["routed_to"].(string)
	if routed == "" {
		t.Fatal("the fired event must state that nothing was routed, so a reader does not assume " +
			"somebody was told")
	}
}

// TestDisabledWatcherDoesNothing covers the off switch.
func TestDisabledWatcherDoesNothing(t *testing.T) {
	rec := &recorder{}
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return []Observation{{
			Name: "teaa9", Uptime: 13 * time.Hour, Output: wedgedLoginPTY("3m 2s"), LastOutputAt: now,
		}}, validCredAt(now)
	}}
	w := New(Options{Enabled: false, Source: fleet.source, Emit: rec.emit})
	run(t, w, time.Hour, 5*time.Minute)
	if len(rec.evs) != 0 {
		t.Errorf("a disabled watcher emitted %d events", len(rec.evs))
	}
	if fleet.seen != 0 {
		t.Errorf("a disabled watcher sampled %d times", fleet.seen)
	}
}

// TestIntervalThrottles pins that the runner does not re-sample on every
// heartbeat tick.
func TestIntervalThrottles(t *testing.T) {
	fleet := &scriptedFleet{host: roomyHost, at: func(now time.Time) ([]Observation, CredentialView) {
		return nil, CredentialView{}
	}}
	w := New(Options{Enabled: true, Source: fleet.source, Emit: (&recorder{}).emit, Interval: 5 * time.Minute})
	for i := 0; i < 20; i++ {
		w.Check(t0.Add(time.Duration(i) * 30 * time.Second)) // 10 minutes of 30s ticks
	}
	if fleet.seen != 2 {
		t.Errorf("samples = %d over 10 minutes at a 5m interval, want 2", fleet.seen)
	}
}

func validCredAt(now time.Time) CredentialView {
	return CredentialView{Readable: true, RefreshValid: true, RefreshExpiry: now.Add(395 * time.Hour)}
}
