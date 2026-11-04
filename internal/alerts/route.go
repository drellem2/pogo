package alerts

import (
	"encoding/json"
	"log"
	"strings"
	"sync"

	"github.com/drellem2/pogo/internal/events"
)

// Routing (slice 3 of drellem2/pogo#262). `[alerts."<name>"] route = "<box>"`
// sends an alert's PRIMARY copy to another mailbox. pogod resolves the routes
// once at startup (cmd/pogod/alertroutes.go) and installs them with SetRoutes;
// Mail and MailOutput apply them.
//
// Which copy is the primary one is a property of the alert, stated by its
// Default rule (Primary):
//
//   - a rule that starts with $coordinator: the send addressed to the
//     coordinator (or the watcher's notify_to). The escalation box's copy of a
//     tiered watcher's finding, the merge author's copy of a failed merge and
//     the pm-<repo> copy of a stranded branch are NOT rerouted:
//     [agents] escalation_box stays the default target of every escalation.
//   - a rule that starts with $escalation_box: the send addressed to the
//     escalation box.
//   - any other rule names a per-message recipient (the filer, the crew agent,
//     the condition's addressee): every send of the alert is rerouted.
//
// MAIL IS NEVER DROPPED BY A ROUTE. A route to a box that does not exist, or a
// routed send that fails, is delivered to the route's Fallback instead (the
// alert's default recipient) and recorded as an alert_route_invalid event.

// EventRouteInvalid is recorded when a routed send fell back to the default
// recipient, and by pogod at startup for each route naming an unknown box.
const EventRouteInvalid = "alert_route_invalid"

// Route is one resolved route.
type Route struct {
	// To is the box the primary copy is sent to instead.
	To string `json:"to"`
	// Match is the recipient a send must be addressed to for the route to
	// apply — the alert's resolved primary box. Empty matches every send.
	Match string `json:"match,omitempty"`
	// Fallback receives the mail when To is unusable. Empty means the
	// recipient the send site addressed.
	Fallback string `json:"fallback,omitempty"`
	// Source is the config key that set the route, as a reader would find
	// it in config.toml.
	Source string `json:"source"`
}

// Primary is the role token naming the copy of name a route replaces —
// RoleCoordinator or RoleEscalationBox — or "" when every send is rerouted
// (the alert's recipient is per-message). See the package comment on routing.
func Primary(d Def) string {
	switch {
	case strings.HasPrefix(d.Default, RoleCoordinator):
		return RoleCoordinator
	case strings.HasPrefix(d.Default, RoleEscalationBox):
		return RoleEscalationBox
	}
	return ""
}

var (
	routesMu sync.RWMutex
	routes   map[Name]Route
)

// SetRoutes installs the resolved routes. pogod calls it once at startup; nil
// (the default, and every unit test that does not call it) routes nothing.
func SetRoutes(m map[Name]Route) {
	cp := make(map[Name]Route, len(m))
	for k, v := range m {
		cp[k] = v
	}
	routesMu.Lock()
	routes = cp
	routesMu.Unlock()
}

// RouteFor returns name's route, if one is installed.
func RouteFor(name Name) (Route, bool) {
	routesMu.RLock()
	defer routesMu.RUnlock()
	r, ok := routes[name]
	return r, ok
}

// routeFor returns the route that applies to a send of name to to.
func routeFor(name Name, to string) (Route, bool) {
	r, ok := RouteFor(name)
	if !ok || r.To == "" {
		return Route{}, false
	}
	if r.Match != "" && r.Match != to {
		return Route{}, false
	}
	if r.Fallback == "" {
		r.Fallback = to
	}
	// A route to the box the send already addresses changes nothing unless
	// it falls back somewhere else — which is what a notify_to alias is.
	if r.To == to && r.Fallback == to {
		return Route{}, false
	}
	return r, true
}

// BoxExists reports whether a mailbox exists. pogod sets it to MGBoxExists;
// nil (every unit test that does not set it) treats every box as existing, so
// only a failed send triggers the fallback.
var BoxExists func(box string) bool

var knownBoxes sync.Map

// usable says why box cannot take a routed send, or "" when it can. A box
// found once is remembered for the process — mailboxes are not deleted under a
// running fleet — so the probe runs once per box, not once per send. A box not
// found is asked about again next time: it may have been created since.
func usable(box string) string {
	if _, ok := knownBoxes.Load(box); ok {
		return ""
	}
	if BoxExists == nil || BoxExists(box) {
		knownBoxes.Store(box, true)
		return ""
	}
	return "no mailbox named " + box
}

// MGBoxExists asks mg whether box is a registered mailbox. It answers false on
// every failure, including mg being absent, and here that is the safe
// direction: a wrong false sends the mail to the default recipient, a wrong
// true sends it to a box nobody reads.
func MGBoxExists(box string) bool {
	out, err := execCommand("mg", "mail", "list", box, "--json").Output()
	if err != nil {
		return false
	}
	var probe struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return false
	}
	return probe.Exists
}

// sendRouted sends through send to r.To, falling back to r.Fallback when the
// box is unknown or the send fails. It returns the fallback send's result.
func sendRouted[T any](name Name, r Route, subject string, send func(to string) (T, error)) (T, error) {
	why := usable(r.To)
	if why == "" {
		out, err := send(r.To)
		if err == nil {
			return out, nil
		}
		why = "routed send failed: " + err.Error()
	}
	RecordRouteInvalid(name, r, subject, why)
	return send(r.Fallback)
}

var routeInvalidLogged sync.Map

// RecordRouteInvalid records that a send of name could not use its route and
// went to r.Fallback instead. It sends nothing.
func RecordRouteInvalid(name Name, r Route, subject, why string) {
	if _, seen := routeInvalidLogged.LoadOrStore(name, true); !seen {
		log.Printf("alerts: %s route to %s (%s) unusable: %s — sent to %s instead; recorded as %s in events.log (logged once per process)",
			name, r.To, r.Source, why, r.Fallback, EventRouteInvalid)
	}
	emitEvent(events.Event{
		EventType: EventRouteInvalid,
		Agent:     "pogod",
		Details: map[string]any{
			"alert":       string(name),
			"route":       r.To,
			"source":      r.Source,
			"fallback_to": r.Fallback,
			"subject":     subject,
			"reason":      why,
			"phase":       "send",
		},
	})
}
