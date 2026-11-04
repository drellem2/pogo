package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// Routing (slice 3 of drellem2/pogo#262). `[alerts."<name>"] route = "<box>"`
// sends an alert's primary copy (alerts.Primary) to another agent or mailbox.
// A nudge alert (slice 4) is routed the same way: it is typed at the route's
// agent instead, and a box that does not exist or a nudge that fails falls
// back to the default recipient.
// The value is one box name, or a role token: "$coordinator" or
// "$escalation_box", resolved here against [agents].
//
// Every watcher's `notify_to` is an ALIAS: it routes that watcher's alerts
// (every name whose registry Key is "<watcher>.notify_to"). Precedence is the
// mute table's: an [alerts] route wins over notify_to, and pogod warns when
// they disagree. [agents] escalation_box is NOT an alias and is not replaced:
// it stays the default target of every escalation copy, and a route moves only
// the primary copy.
//
// An unknown box is reported at load — a startup warning and an
// alert_route_invalid event per route — and the route is still installed: at
// send time the seam re-checks the box and, if it is still unknown (or the
// routed send fails), delivers to the default recipient instead. Mail is never
// dropped by a route.

// alertRouteTable is the resolved route state.
type alertRouteTable struct {
	Routes map[alerts.Name]alerts.Route
	// Invalid names each route whose box was not found at load, with why.
	Invalid map[alerts.Name]string
	// Warnings are startup lines about [alerts] routes and their aliases.
	Warnings []string
}

// watcherNotifyTo maps each registry Key of the form "<watcher>.notify_to" to
// that watcher's resolved notify_to.
func watcherNotifyTo(cfg *config.Config) map[string]string {
	return map[string]string{
		"gh_teardown.notify_to":       cfg.GHTeardown.NotifyTo,
		"gh_intake.notify_to":         cfg.GHIntake.NotifyTo,
		"carrier_drift.notify_to":     cfg.CarrierDrift.NotifyTo,
		"review_decl.notify_to":       cfg.ReviewDecl.NotifyTo,
		"ack_watch.notify_to":         cfg.AckWatch.NotifyTo,
		"deaf_watch.notify_to":        cfg.DeafWatch.NotifyTo,
		"absent_watch.notify_to":      cfg.AbsentWatch.NotifyTo,
		"progress_watch.notify_to":    cfg.ProgressWatch.NotifyTo,
		"first_turn.notify_to":        cfg.FirstTurn.NotifyTo,
		"mid_session_wedge.notify_to": cfg.MidSessionWedge.NotifyTo,
	}
}

// notifyToKeyName is how a reader finds a notify_to Key in config.toml:
// "ack_watch.notify_to" -> "[ack_watch] notify_to".
func notifyToKeyName(key string) string {
	section, k, _ := strings.Cut(key, ".")
	return fmt.Sprintf("[%s] %s", section, k)
}

// resolveAlertRoutes builds the route table from cfg. exists probes a box; nil
// skips load-time validation (every route is then checked only at send).
func resolveAlertRoutes(cfg *config.Config, exists func(string) bool) alertRouteTable {
	t := alertRouteTable{Routes: map[alerts.Name]alerts.Route{}, Invalid: map[alerts.Name]string{}}
	coordinator := cfg.Agents.CoordinatorName()
	escalation := cfg.Agents.EscalationBoxName()
	notifyTo := watcherNotifyTo(cfg)

	// primary is the box a send of d is addressed to when it is the copy a
	// route replaces; "" for an alert whose recipient is per-message.
	primary := func(d alerts.Def) string {
		switch alerts.Primary(d) {
		case alerts.RoleCoordinator:
			if d.Key == alerts.StallWatchAgentKey && cfg.StallWatch.Agent != "" {
				return cfg.StallWatch.Agent
			}
			if v := notifyTo[d.Key]; v != "" {
				return v
			}
			return coordinator
		case alerts.RoleEscalationBox:
			return escalation
		}
		return ""
	}
	// fallback is the alert's default recipient: the box a send falls back to
	// when its route is unusable. "" means the recipient the send addressed.
	fallback := func(d alerts.Def) string {
		switch alerts.Primary(d) {
		case alerts.RoleCoordinator:
			// A stall-watch nudge falls back to the agent it watches: that
			// is its default recipient, and [stall_watch] agent is not a
			// route alias.
			if d.Key == alerts.StallWatchAgentKey && cfg.StallWatch.Agent != "" {
				return cfg.StallWatch.Agent
			}
			return coordinator
		case alerts.RoleEscalationBox:
			return escalation
		}
		return ""
	}

	// Aliases first: a watcher's notify_to, where it names someone other than
	// the coordinator it defaults to. The watcher already addresses that box;
	// installing it as a route adds what a route has and notify_to never had —
	// an unknown box falls back to the coordinator instead of being refused.
	for _, d := range alerts.All() {
		v := notifyTo[d.Key]
		if d.Class != alerts.ClassAlert || v == "" || v == coordinator || alerts.Primary(d) != alerts.RoleCoordinator {
			continue
		}
		t.Routes[d.Name] = alerts.Route{To: v, Match: v, Fallback: coordinator, Source: notifyToKeyName(d.Key)}
	}

	names := make([]string, 0, len(cfg.Alerts))
	for n := range cfg.Alerts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, raw := range names {
		a := cfg.Alerts[raw]
		if !a.RouteSet {
			continue
		}
		n := alerts.Name(raw)
		d, problem := routableAlert(n)
		if problem != "" {
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ [alerts.%q] route %s — ignored; `pogo alerts` lists the names", raw, problem))
			continue
		}
		source := fmt.Sprintf("[alerts.%q] route = %q", raw, a.Route)
		if a.Route == "" {
			// route = "" clears a route, and with it the notify_to alias.
			if old, ok := t.Routes[n]; ok {
				t.Warnings = append(t.Warnings, fmt.Sprintf(
					"pogod: ⚠ [alerts.%q] route = \"\" overrides %s — %s goes to its default recipient", raw, old.Source, raw))
			}
			delete(t.Routes, n)
			continue
		}
		to, problem := resolveRouteValue(a.Route, coordinator, escalation)
		if problem != "" {
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ [alerts.%q] route = %q %s — ignored; %s keeps its default recipient", raw, a.Route, problem, raw))
			continue
		}
		if old, ok := t.Routes[n]; ok && old.To != to {
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ [alerts.%q] route = %q overrides %s (%s) — %s goes to %s", raw, a.Route, old.Source, old.To, raw, to))
		}
		t.Routes[n] = alerts.Route{To: to, Match: primary(d), Fallback: fallback(d), Source: source}
	}

	if exists != nil {
		checked := map[string]bool{}
		for _, n := range sortedRouteNames(t.Routes) {
			r := t.Routes[n]
			ok, seen := checked[r.To]
			if !seen {
				ok = exists(r.To)
				checked[r.To] = ok
			}
			if ok {
				continue
			}
			fb := r.Fallback
			if fb == "" {
				fb = "the recipient each send addresses"
			}
			why := "no mailbox named " + r.To
			t.Invalid[n] = why
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ %s routes %s to %s, but there is %s — until it exists, %s is sent to %s instead (alert_route_invalid)",
				r.Source, n, r.To, why, n, fb))
		}
	}
	return t
}

// routableAlert says why n cannot be routed, or "" with its declaration when
// it can.
func routableAlert(n alerts.Name) (alerts.Def, string) {
	d, ok := alerts.Lookup(n)
	if !ok {
		if strings.HasPrefix(string(n), conditionRowPrefix) {
			return d, fmt.Sprintf("cannot be set on one condition row — every row is sent as %s; route that", alerts.ConditionNotice)
		}
		return d, "names no alert pogod sends"
	}
	if d.Class != alerts.ClassAlert {
		return d, fmt.Sprintf("is a %s mechanic, not an alert, and cannot be routed", d.Class)
	}
	return d, ""
}

// resolveRouteValue turns a route value into a box name: a role token is
// replaced by its configured box; anything else must be one plain name.
func resolveRouteValue(v, coordinator, escalation string) (string, string) {
	switch v {
	case alerts.RoleCoordinator:
		return coordinator, ""
	case alerts.RoleEscalationBox:
		return escalation, ""
	}
	if strings.HasPrefix(v, "$") {
		return "", fmt.Sprintf("is not a role (the roles are %s and %s)", alerts.RoleCoordinator, alerts.RoleEscalationBox)
	}
	if strings.ContainsAny(v, " \t\"'[],#") {
		return "", "is not one mailbox name (route takes a single agent or box, without an inline comment)"
	}
	return v, ""
}

func sortedRouteNames(m map[alerts.Name]alerts.Route) []alerts.Name {
	out := make([]alerts.Name, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// routeInvalidAtLoadEvents are the alert_route_invalid events pogod records at
// startup, one per route whose box was not found.
func routeInvalidAtLoadEvents(t alertRouteTable) []events.Event {
	var out []events.Event
	for _, n := range sortedRouteNames(t.Routes) {
		why, bad := t.Invalid[n]
		if !bad {
			continue
		}
		r := t.Routes[n]
		out = append(out, events.Event{
			EventType: alerts.EventRouteInvalid,
			Agent:     "pogod",
			Details: map[string]any{
				"alert":       string(n),
				"route":       r.To,
				"source":      r.Source,
				"fallback_to": r.Fallback,
				"reason":      why,
				"phase":       "load",
			},
		})
	}
	return out
}

// routedAlertsLine is the startup line naming every route set by an [alerts]
// entry, or "" when none is. notify_to aliases are not repeated here: they
// are the configuration every install already had, and `pogo alerts` lists
// them.
func routedAlertsLine(t alertRouteTable) string {
	var parts []string
	for _, n := range sortedRouteNames(t.Routes) {
		r := t.Routes[n]
		if !strings.HasPrefix(r.Source, "[alerts.") {
			continue
		}
		p := fmt.Sprintf("%s -> %s", n, r.To)
		if _, bad := t.Invalid[n]; bad {
			p += " (UNKNOWN BOX: falls back to the default)"
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("pogod: %d alert(s) routed by [alerts]: %s", len(parts), strings.Join(parts, "; "))
}
