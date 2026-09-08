package agent

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"

	"github.com/drellem2/pogo/internal/events"
)

// Panic isolation for the daemon's long-lived goroutines.
//
// Go terminates the whole process on an unrecovered panic in ANY goroutine, so
// before this file a panic in a provider hook, a PTY reader, an attach handler
// or the modal-dismissal watcher took pogod down with it. That is worse than a
// restart: a crashed pogod cannot re-adopt the agents it was running, because
// each agent's PTY master died with the process that held it (see orphan.go,
// adoptOrphan) — so one panic in one hook would strand EVERY running agent on
// the host, not just the one whose hook it was.
//
// Nothing here is a diagnosed cause. No panic has been observed in pogod and no
// stack trace exists; this is defence in depth for drellem2/pogo#166, chosen on
// the merits of the blast radius above rather than on a reproduction.
//
// The guard deliberately does NOT swallow quietly. A recovered panic is a bug
// that still needs finding, so every recovery logs the value and the full stack
// to the daemon's stderr and emits a durable `goroutine_panic` event
// (docs/event-log.md) naming the site. Silence would trade a loud crash for an
// invisible one, which is a worse deal than the crash.
//
// Why this lives in package agent rather than a new leaf package: it is the
// only package all three sweep sites can already reach. internal/claude and
// cmd/pogod both import internal/agent, and internal/agent imports neither, so
// no import edge is created by putting it here.

// PanicEventType is the event_type emitted when a guarded goroutine recovers a
// panic. Exported so readers of the event log (and tests) name it once.
const PanicEventType = "goroutine_panic"

// panicStackLimit bounds the stack text carried in the emitted event. The full
// stack always goes to the log; the event keeps a prefix so a single panic
// cannot write an unbounded line into events.log.
const panicStackLimit = 4096

// GoSafe runs fn on a new goroutine with a panic guard. It is the launcher
// every long-lived daemon goroutine should use instead of a bare `go`.
//
// site is a short, stable, greppable label for the launch point (for example
// "agent.readOutput"), used in the log line and in the event's details.site.
func GoSafe(site string, fn func()) {
	go Safely(site, fn)
}

// Safely runs fn on the CALLING goroutine under the same guard, and reports
// whether fn returned normally. Use it to wrap a foreign call inside a
// goroutine that has its own postconditions to finish — recovering at the call
// rather than at the top of the goroutine is what lets the rest of the
// goroutine run after a callback blows up.
func Safely(site string, fn func()) (ok bool) {
	return safely(site, "", "", "", fn)
}

// GoSafe is GoSafe with this agent's identity attached to the emitted event, so
// a panic in a per-agent goroutine is attributable to the agent it belonged to.
func (a *Agent) GoSafe(site string, fn func()) {
	go a.Safely(site, fn)
}

// Safely is Safely with this agent's identity attached to the emitted event.
func (a *Agent) Safely(site string, fn func()) bool {
	if a == nil {
		return Safely(site, fn)
	}
	return safely(site, a.eventAgent(), a.WorkItemID, a.SourceRepo, fn)
}

func safely(site, agentID, workItemID, repo string, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			reportPanic(site, agentID, workItemID, repo, r, debug.Stack())
			ok = false
		}
	}()
	fn()
	return true
}

// reportPanic writes the recovered panic to both channels: the daemon log (full
// stack, for whoever is reading stderr right now) and the event log (truncated
// stack, for whoever asks later why an agent stopped being watched).
func reportPanic(site, agentID, workItemID, repo string, val any, stack []byte) {
	who := site
	if agentID != "" {
		who = fmt.Sprintf("%s (agent %s)", site, agentID)
	}
	log.Printf("PANIC RECOVERED in %s: %v\n%s", who, val, stack)

	trimmed := string(stack)
	if len(trimmed) > panicStackLimit {
		trimmed = trimmed[:panicStackLimit]
	}
	events.Emit(context.Background(), events.Event{
		EventType:  PanicEventType,
		Agent:      eventAgentOrDaemon(agentID),
		WorkItemID: workItemID,
		Repo:       repo,
		Details: map[string]any{
			"site":  site,
			"panic": fmt.Sprint(val),
			"stack": trimmed,
		},
	})
}

// eventAgentOrDaemon keeps the envelope's required `agent` field populated for
// guarded goroutines that belong to no particular agent.
func eventAgentOrDaemon(agentID string) string {
	if agentID == "" {
		return "pogod"
	}
	return agentID
}
