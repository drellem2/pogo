package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// logf is the package's log sink, indirected so tests can capture the very
// lines this instrument exists to produce. Production leaves it at log.Printf.
var logf = log.Printf

// Event types emitted to ~/.pogo/events.log for run-mode changes.
//
// WHY AN EVENT AND NOT ONLY A LOG LINE (mg-293c). Both are emitted, on purpose,
// and the event is the artifact of record:
//
//   - events.log is written by the process to a path it resolves itself, and it
//     is queryable (`pogo events list --type=server_mode_changed`). pogod.log
//     is whatever the parent pointed stderr at.
//   - That distinction is not theoretical here. The transition sites have logged
//     "server: transitioning to index-only mode" since mg-ce65 (March 2026), and
//     four months of ~/Library/Logs/pogo/pogod.log* contain ZERO such lines —
//     including across the 2026-08-07 03:00 window in which POST /agents/drain
//     was demonstrably answered 503 by RequireOrchestration. A log line that
//     depends on inherited stderr can go missing without anyone noticing; the
//     event does not depend on it.
//
// The log line is kept because it is what an operator tailing the daemon sees,
// and because losing it would be a regression in the ordinary case.
const (
	// EventModeChanged records an actual transition between run modes. It is
	// emitted only when the mode really changed — a stop against an
	// already-stopped daemon changes nothing and records nothing, so the
	// presence of this event always means dispatch availability moved.
	EventModeChanged = "server_mode_changed"
	// EventModeBoot records the mode a pogod process booted into, emitted
	// unconditionally at server construction. Without it, "which mode did it
	// boot into" is only answerable by inferring from the absence of a
	// transition, which is exactly the inference mg-293c exists to retire.
	EventModeBoot = "server_mode_boot"
)

// Attribution headers stamped by internal/client on the two mode-transition
// requests. They exist because Go's default HTTP client identifies itself as
// "Go-http-client/1.1" and nothing else — enough to know a program called, not
// enough to know which one. The 2026-08-07 investigation spent two days
// narrowing "who stopped orchestration" to a leading candidate it could not
// demonstrate; these headers are the difference between that and a name.
const (
	HeaderActorAgent = "X-Pogo-Agent"  // POGO_AGENT_NAME of the calling agent, if any
	HeaderActorPid   = "X-Pogo-Pid"    // pid of the calling process
	HeaderActorCmd   = "X-Pogo-Client" // argv-derived command, e.g. "pogo service install"
)

// Emitter writes a mode event to the shared log. Defaults to events.Emit;
// tests substitute a collector.
type Emitter func(events.Event)

func defaultEmitter(e events.Event) { events.Emit(context.Background(), e) }

// Cause describes what triggered a mode transition.
//
// It is carried to the TRANSITION SITE rather than logged at the call site.
// Call sites are open-ended: pm-pogo enumerated them as "the two HTTP handlers"
// and was then corrected by an in-tree caller nobody had grepped for
// (internal/service's quiesceCrew, which stops fleet-wide dispatch as a side
// effect of installing a launchd job). There is exactly one place every
// transition must pass through, and that is where the record belongs.
type Cause struct {
	// Trigger is the coarse class of cause: "http", "startup", or
	// "unattributed" for a direct in-process SetMode with no caller context.
	Trigger string
	// Detail names the specific route or reason, e.g.
	// "POST /server/stop-orchestration".
	Detail string
	// Agent is the calling agent's POGO_AGENT_NAME, when it sent one.
	Agent string
	// Client is the caller's argv-derived command, e.g. "pogo service install".
	Client string
	// ClientPid is the caller's process id, as a string (it arrives as a
	// header and is never arithmetic).
	ClientPid string
	// RemoteAddr is the HTTP peer address.
	RemoteAddr string
	// UserAgent is the HTTP User-Agent header.
	UserAgent string
}

// unattributedCause is what the bare SetMode / StartOrchestration entry points
// record. It is deliberately conspicuous: a transition with no attribution is a
// finding, not a formatting detail.
func unattributedCause(detail string) Cause {
	return Cause{Trigger: "unattributed", Detail: detail}
}

// startupCause is recorded for the mode a process boots into.
func startupCause() Cause {
	return Cause{
		Trigger:   "startup",
		Detail:    "process start",
		ClientPid: fmt.Sprint(os.Getpid()),
	}
}

// causeFromRequest builds a Cause from an inbound mode-transition request.
func causeFromRequest(r *http.Request, detail string) Cause {
	c := Cause{Trigger: "http", Detail: detail}
	if r == nil {
		return c
	}
	c.Agent = r.Header.Get(HeaderActorAgent)
	c.Client = r.Header.Get(HeaderActorCmd)
	c.ClientPid = r.Header.Get(HeaderActorPid)
	c.RemoteAddr = r.RemoteAddr
	c.UserAgent = r.UserAgent()
	return c
}

// String renders the cause as compact key=value pairs for the log line. Empty
// fields are omitted, so an unattributed caller reads as short rather than as a
// row of blanks.
func (c Cause) String() string {
	parts := []string{}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+quoteIfSpaced(v))
		}
	}
	add("trigger", c.Trigger)
	add("detail", c.Detail)
	add("agent", c.Agent)
	add("client", c.Client)
	add("client_pid", c.ClientPid)
	add("remote_addr", c.RemoteAddr)
	add("user_agent", c.UserAgent)
	if len(parts) == 0 {
		return "trigger=unknown"
	}
	return strings.Join(parts, " ")
}

// details renders the cause into the event's details object, omitting empty
// fields so a reader can tell "not sent" from "sent empty".
func (c Cause) details() map[string]any {
	d := map[string]any{}
	add := func(k, v string) {
		if v != "" {
			d[k] = v
		}
	}
	add("trigger", c.Trigger)
	add("detail", c.Detail)
	add("actor_agent", c.Agent)
	add("actor_client", c.Client)
	add("actor_pid", c.ClientPid)
	add("remote_addr", c.RemoteAddr)
	add("user_agent", c.UserAgent)
	return d
}

// attributed reports whether the cause names anybody at all beyond the fact
// that a request arrived. Used to flag a transition nobody can be traced for.
func (c Cause) attributed() bool {
	return c.Agent != "" || c.Client != "" || c.ClientPid != ""
}

func quoteIfSpaced(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// recordTransition writes the audit record for a mode change that actually
// happened. from and to are the modes either side of the change.
func (s *Server) recordTransition(from, to config.RunMode, cause Cause) {
	suffix := ""
	if !cause.attributed() {
		suffix = " [UNATTRIBUTED: no caller identity — see mg-293c]"
	}
	logf("server: run mode changed %s -> %s (%s)%s", from, to, cause, suffix)

	d := cause.details()
	d["from"] = from.String()
	d["to"] = to.String()
	d["attributed"] = cause.attributed()
	s.emitter()(events.Event{
		EventType: EventModeChanged,
		Agent:     "pogod",
		Details:   d,
	})
}

// recordBootMode writes the audit record for the mode a process starts in.
func (s *Server) recordBootMode(mode config.RunMode) {
	cause := startupCause()
	logf("server: run mode at startup: %s (%s)", mode, cause)

	d := cause.details()
	d["mode"] = mode.String()
	s.emitter()(events.Event{
		EventType: EventModeBoot,
		Agent:     "pogod",
		Details:   d,
	})
}

func (s *Server) emitter() Emitter {
	s.mu.RLock()
	fn := s.emit
	s.mu.RUnlock()
	if fn == nil {
		return defaultEmitter
	}
	return fn
}

// SetEmitter overrides the events sink. Tests use it; production leaves it nil
// so the package default (events.Emit) applies.
func (s *Server) SetEmitter(fn Emitter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit = fn
}
