package wedgewatch

import (
	"context"
	"errors"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/credexpiry"
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

// RegistrySource adapts an agent registry into a SourceFunc.
//
// The credential read is LAZY: cred is called only when some agent in the
// sample is actually showing an auth symptom. That matters because the
// production reader shells out to macOS `security`, which can block on a
// keychain authorization prompt (internal/credexpiry gives it a 10s timeout for
// exactly that reason). Reading it on every 5-minute tick regardless would put
// a blocking, prompt-capable subprocess on pogod's heartbeat to answer a
// question nobody asked.
func RegistrySource(reg *agent.Registry, cred CredFunc) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		if reg == nil {
			return Snapshot{}, ErrNoRegistry
		}
		all := reg.List()
		snap := Snapshot{Now: now, Scanned: len(all)}
		needCred := false
		for _, a := range all {
			o, ok := observe(a, now)
			if !ok {
				continue
			}
			snap.Agents = append(snap.Agents, o)
			if !needCred {
				sigs := Signatures(ScanMarkers(o.Output, nil))
				needCred = hasSig(sigs, SigLoginPrompt) || hasSig(sigs, SigAPI401)
			}
		}
		if needCred && cred != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			snap.Cred = cred(ctx)
		}
		return snap, nil
	}
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
