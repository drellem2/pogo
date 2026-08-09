package wedgewatch

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/credexpiry"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/hostload"
)

// This file is the ONLY place wedgewatch touches live pogo state. The runner in
// watcher.go takes a Snapshot and nothing else, so every test in this package
// builds fixtures by hand — mg-6092, mg-e8e7 and mg-5336 are three separate
// tickets for tests that read the developer's live ~/.pogo, and this package
// does not add a fourth.

// OutputScanBytes is how much of each agent's PTY ring buffer is scanned.
//
// Large enough to hold several full screen redraws of a 200x50 PTY status
// region — the wedge signature must survive a spinner that keeps repainting
// over it — and small enough that scanning the whole fleet on a 5-minute tick
// is free. The ring itself is agent.OutputRingBytes (64KB); taking a slice of
// it rather than all of it keeps an agent that produced a burst of unrelated
// output from pushing the counter out of view less often than it otherwise
// would.
const OutputScanBytes = 16 * 1024

// ErrNoRegistry is returned when there is no agent registry to read. It is an
// ERROR and not an empty snapshot, on the same footing as
// agent.ErrNoMailCheckJudgement: a detector that cannot look has not found a
// healthy fleet.
var ErrNoRegistry = errors.New("wedgewatch: no agent registry to sample")

// CredFunc yields the fleet's credential view. Production binds
// SystemCredential; tests inject a fixture.
type CredFunc func(ctx context.Context) CredentialView

// HostFunc yields the host-contention view. Production binds SystemHost; tests
// inject a fixture.
type HostFunc func(ctx context.Context) HostView

// EventsFunc yields the event-log recency index. Production binds SystemEvents;
// tests inject a fixture.
type EventsFunc func() EventsIndex

// RegistrySource adapts an agent registry into a SourceFunc.
//
// The credential read is LAZY: cred is called only when some agent in the
// sample is actually showing an auth symptom. That matters because the
// production reader shells out to macOS `security`, which can block on a
// keychain authorization prompt (internal/credexpiry gives it a 10s timeout for
// exactly that reason). Reading it on every 5-minute tick regardless would put
// a blocking, prompt-capable subprocess on pogod's heartbeat to answer a
// question nobody asked.
//
// The HOST read is NOT lazy, and the asymmetry is deliberate. Host contention
// is what separates a wedged agent from a merely CPU-starved one, and those
// need opposite handling — so the measurement must be present on every finding,
// including the ones where it rules starvation OUT. It costs one sampling
// window of wall clock (hostload.DefaultWindow, 1s) on a goroutine that is
// already off the heartbeat's critical path, and it prompts for nothing.
// The EVENT-LOG read is lazy on the same terms as the credential read, and for
// a different reason: it is the fallback stall signal, consulted only for an
// agent whose declared-work counter could not be parsed. On a fleet where every
// counter reads, nothing needs it and the log is never opened. See
// attachEventFallback.
func RegistrySource(reg *agent.Registry, cred CredFunc, host HostFunc, ev EventsFunc) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		if reg == nil {
			return Snapshot{}, ErrNoRegistry
		}
		all := reg.List()
		var obs []Observation
		for _, a := range all {
			o, ok := observe(a, now)
			if !ok {
				continue
			}
			obs = append(obs, o)
		}
		return buildSnapshot(now, len(all), obs, cred, host, ev), nil
	}
}

// buildSnapshot assembles the reading from already-observed agents.
//
// It is split out from RegistrySource because it is the WHOLE production
// snapshot pipeline minus the one step that needs a live registry, and mg-20eb
// is what happens when that pipeline has a step nothing exercises: Observation
// carried an EventsLastSeen field, stallOf's documented fallback keyed on it,
// and no production code path ever assigned it — so the fallback was reachable
// only from a test, and the detector's first 40 judgements on this box were all
// "could not judge". Everything that fills in an Observation beyond what one
// agent can tell you about itself happens HERE, where a test can drive it with
// hand-built observations and no registry.
func buildSnapshot(now time.Time, scanned int, obs []Observation, cred CredFunc, host HostFunc, ev EventsFunc) Snapshot {
	snap := Snapshot{Now: now, Scanned: scanned, Agents: attachEventFallback(obs, ev)}
	if len(snap.Agents) == 0 {
		return snap
	}
	needCred := false
	for _, o := range snap.Agents {
		sigs := Signatures(ScanMarkers(o.Output, nil))
		if hasSig(sigs, SigLoginPrompt) || hasSig(sigs, SigAPI401) {
			needCred = true
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if needCred && cred != nil {
		snap.Cred = cred(ctx)
	}
	if host != nil {
		snap.Host = host(ctx)
	} else {
		snap.Host = HostView{Readable: false, Reason: ReasonNoHostSampler}
	}
	return snap
}

// attachEventFallback fills in each observation's event-log fallback fields.
//
// It reads the index AT MOST ONCE per sample, and only when some agent's
// declared-work counter could not be parsed — the sole condition under which
// stallOf consults it. That laziness is why re-parsing the counter here is
// acceptable: on a healthy fleet the parse is the only cost and the log is never
// opened, and on a blind fleet the parse is negligible beside the scan it gates.
//
// EventsRead is set on every observation once the index has been consulted,
// including the ones with no entry in it. The distinction it carries — "looked
// and found nothing" versus "could not look" — is what stops the blind message
// from asserting a fact about the event log it never checked.
func attachEventFallback(obs []Observation, ev EventsFunc) []Observation {
	if len(obs) == 0 || ev == nil {
		return obs
	}
	need := false
	for _, o := range obs {
		if _, ok := ParseDeclaredWork(o.Output); !ok {
			need = true
			break
		}
	}
	if !need {
		return obs
	}
	idx := ev()
	if !idx.Readable {
		return obs
	}
	for i := range obs {
		obs[i].EventsRead = true
		obs[i].EventsLastSeen = idx.LastSeen[obs[i].identity()]
	}
	return obs
}

// observe converts one live agent into an Observation, or reports that it is
// not judgeable.
//
// Exited agents are skipped: this detector is about a process that is running
// and producing output while doing nothing, and a dead one is a different fault
// that other things already see. An agent with no buffered output at all is
// also skipped — there is nothing to read, and inventing a verdict from an
// empty buffer is the failure mode this package exists to stop.
func observe(a *agent.Agent, now time.Time) (Observation, bool) {
	if a == nil || !a.Alive() {
		return Observation{}, false
	}
	out := a.RecentOutput(OutputScanBytes)
	if len(out) == 0 {
		return Observation{}, false
	}
	return Observation{
		Name:         a.Name,
		Identity:     a.EventAgent(),
		Type:         string(a.Type),
		Alive:        true,
		Uptime:       now.Sub(a.StartTime),
		Output:       out,
		LastOutputAt: a.LastOutputAt(),
	}, true
}

// SystemCredential reads the live credential through internal/credexpiry and
// reduces it to the two facts the classifier is allowed to use.
//
// Only the REFRESH-grant expiry is consulted. The 8-hour access-token expiry is
// deliberately ignored, and that is not an oversight: internal/credexpiry's
// package doc records that it is routinely in the past on a perfectly healthy
// machine because the harness re-mints on demand without rewriting the stored
// blob. On 2026-08-04 the access token was in fact valid with 7.7h remaining
// while every agent on the box was showing "401 ... has been revoked", so
// reading it either way would have been noise — and reading it the wrong way
// would have manufactured exactly the false "credential is bad" verdict this
// package refuses to give.
//
// StateAbsent and StateUnreadable both yield Readable=false. Neither is a claim
// that the credential is fine; both make the classifier fall through to
// CauseUnknown, which is the correct answer when you could not look.
func SystemCredential(ctx context.Context) CredentialView {
	return credentialFrom(credexpiry.SystemReader(ctx), time.Now())
}

// credentialFrom is the pure conversion, split out so tests can drive every
// State without a keychain.
func credentialFrom(st credexpiry.Status, now time.Time) CredentialView {
	if st.State != credexpiry.StatePresent {
		return CredentialView{Readable: false, Reason: st.Reason}
	}
	return CredentialView{
		Readable:      true,
		RefreshValid:  st.RefreshExpiry.After(now),
		RefreshExpiry: st.RefreshExpiry,
	}
}

// Fixed reason vocabulary for a host view that could not be measured. Drawn
// from constants so a reason can never carry command output.
const (
	// ReasonNoHostSampler means no sampler was wired at all.
	ReasonNoHostSampler = "no host sampler is wired, so CPU starvation could not be ruled out"
	// ReasonHostReadFailed means the process table could not be read.
	ReasonHostReadFailed = "the host's process table could not be read"
	// ReasonHostUnresolvable means the sample was taken but its numbers are
	// quantisation noise rather than a measurement — hostload's Unresolvable
	// case. Treating that as "no CPU in use" is how a guard goes blind
	// (mg-79e3), so it is reported as UNREADABLE instead.
	ReasonHostUnresolvable = "the host's CPU column is too coarse for the sampling window; the reading is noise, not a measurement"
	// ReasonHostNoCores means the core count came back non-positive, leaving no
	// denominator to compare against.
	ReasonHostNoCores = "the host reported no logical cores, so there is no denominator for saturation"
)

// SystemHost measures live host contention through internal/hostload and
// reduces it to the one fact the classifier is allowed to use: whether the box
// is full.
//
// It reads USED CORES against CORE COUNT, at hostload's own SaturatedAt
// threshold — deliberately NOT the load average, which is the instrument a
// reader of the 2026-08-05 incident report ("1-min average 300 on a 10-core
// box") would reach for. hostload disqualified that one with a measurement on
// this very host (mg-1b8c): a load average of 214 coincided with roughly 7.5 of
// 10 cores actually in use, because Darwin's load average counts
// uninterruptible-sleep tasks as well as runnable ones, and part of what it
// counted belonged to a VPN extension and the system indexer rather than to the
// fleet. Deciding on it would report a full box whenever something was doing
// heavy I/O.
//
// It measures the WHOLE host rather than the fleet's share, which is the
// opposite of what the dispatch guard wants and correct here: dispatch asks
// "would pausing our own work help", and this asks "is there CPU for this agent
// to make progress with". An agent starved by someone else's compiler is just
// as starved.
func SystemHost(ctx context.Context) HostView {
	return hostViewFrom((&hostload.Reader{Roots: []int{os.Getpid()}}).Read(ctx))
}

// Fixed reason vocabulary for an event-log index that could not be built.
const (
	// ReasonNoEventLogPath means the log's location could not be resolved.
	ReasonNoEventLogPath = "the event log's path could not be resolved"
	// ReasonEventLogAbsent means there is no event log on this box yet.
	ReasonEventLogAbsent = "there is no event log on this host yet"
	// ReasonEventLogUnreadable means the log exists but could not be scanned.
	ReasonEventLogUnreadable = "the event log could not be read"
)

// SystemEvents builds the fallback stall index from the live event log.
//
// # What this measures, and what it does NOT
//
// The last event-log line carrying an agent's identity. That is a COARSER
// signal than the declared-work counter and is only ever used in its place: on
// this box most identities' newest line is their own `agent_spawned`, so for a
// live agent "events stale" usually reduces to "nothing has been recorded about
// this agent since it started". Which is exactly the intended degradation — a
// harness that renames its status line leaves this detector with the marker
// check plus a crude age, rather than with nothing.
//
// Note what is deliberately NOT counted as recency: pogod's scheduler traffic.
// `scheduler_fire_delivered` and its siblings are attributed to `pogod`, not to
// the agent they were delivered to, so they cannot keep a wedged agent's clock
// warm. That is the mg-fc8d failure mode one level down — a delivery record
// proves the sender is alive, never the receiver — and it is why this index
// keys on the event's own agent field rather than on anything addressed AT an
// identity.
//
// Only the LIVE log is scanned, not the rotated files. An identity whose last
// line has rotated out therefore reads as absent, which reports the agent as
// unjudgeable rather than as stale. That understates a very stale agent, and it
// is the safe direction: the alternative is scanning up to 600MB (six retained
// files at maxLogBytes) on every sample to sharpen a signal that is already the
// fallback.
func SystemEvents() EventsIndex {
	path, err := events.LogPath()
	if err != nil {
		return EventsIndex{Reason: ReasonNoEventLogPath}
	}
	return eventsIndexFrom(path)
}

// eventsIndexFrom is the scan, split out so tests can drive it against a
// fixture log rather than the fleet's.
//
// A missing file is UNREADABLE, not an empty index. The difference decides
// whether a blind agent's error says the log has no entry for it — a claim
// about the log — or that no fallback was available, and mg-20eb is a ticket
// about the first sentence being printed 40 times by a detector that had never
// opened the log at all.
func eventsIndexFrom(path string) EventsIndex {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return EventsIndex{Reason: ReasonEventLogAbsent}
		}
		return EventsIndex{Reason: ReasonEventLogUnreadable}
	}
	last := map[string]time.Time{}
	if err := events.ScanFile(path, func(ev events.Event) {
		if ev.Agent == "" {
			return
		}
		ts, perr := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if perr != nil {
			return
		}
		if prev, ok := last[ev.Agent]; !ok || ts.After(prev) {
			last[ev.Agent] = ts
		}
	}); err != nil {
		// A partial index is worse than none: an identity missing because the
		// scan aborted is indistinguishable from one the log never mentioned,
		// and the second reads as staleness.
		return EventsIndex{Reason: ReasonEventLogUnreadable}
	}
	return EventsIndex{Readable: true, LastSeen: last}
}

// hostViewFrom is the pure conversion, split out so tests can drive every
// outcome without a real host.
func hostViewFrom(s hostload.Sample, err error) HostView {
	if err != nil {
		// Note what is NOT here: err.Error(). A reason is drawn from the fixed
		// vocabulary above so it cannot carry process output.
		return HostView{Readable: false, Reason: ReasonHostReadFailed}
	}
	if !s.Resolved() {
		return HostView{Readable: false, Reason: ReasonHostUnresolvable}
	}
	if s.Cores <= 0 {
		return HostView{Readable: false, Reason: ReasonHostNoCores}
	}
	used := s.UsedCores()
	return HostView{
		Readable:  true,
		Saturated: used/float64(s.Cores) >= hostload.SaturatedAt,
		UsedCores: used,
		Cores:     s.Cores,
	}
}
