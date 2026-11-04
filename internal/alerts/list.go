package alerts

import (
	"sort"
	"strings"
)

// Settings is what the listing needs from the daemon's loaded config. pogod
// fills it; this package does not read config itself.
//
// Recipients come from the keys that exist today (notify_to, [agents]); mute
// state is the table the send seam enforces (SetMutes).
type Settings struct {
	// Coordinator and EscalationBox are the resolved [agents] coordinator and
	// [agents] escalation_box.
	Coordinator   string
	EscalationBox string
	// NotifyTo holds each watcher's resolved notify_to, keyed as Def.Key
	// spells it ("ack_watch.notify_to"). It replaces $coordinator for alerts
	// whose Key names it.
	NotifyTo map[string]string
	// Muted names the muted alerts, each with the key that muted it
	// ("[conditions] mail = false", "[alerts.\"ack_watch.finding\"] mute =
	// true"). A muted name the registry does not declare — a per-row
	// condition.<id> — is listed as a row of its own.
	Muted map[Name]string
}

// Row is one line of `pogo alerts` / GET /alerts.
type Row struct {
	Name    Name   `json:"name"`
	Class   string `json:"class"`
	Default string `json:"default"`
	// Effective is Default with its role tokens and any notify_to resolved
	// against the running config — who the mail goes to on this install.
	Effective string `json:"effective"`
	Muted     bool   `json:"muted"`
	// Source names the config key that made Effective or Muted differ from the
	// built-in default, or "default".
	Source  string `json:"source"`
	Key     string `json:"key,omitempty"`
	Summary string `json:"summary"`
}

// Rows lists every declared alert against s. Deliveries are included only when
// all is set. Muted rows sort first — a silenced alert is the row an operator
// most needs to see — then by name.
func Rows(s Settings, all bool) []Row {
	var out []Row
	for _, d := range All() {
		if d.Class != ClassAlert && !all {
			continue
		}
		r := Row{Name: d.Name, Class: d.Class, Default: d.Default, Key: d.Key, Summary: d.Summary, Source: "default"}
		first := s.Coordinator
		if v, ok := s.NotifyTo[d.Key]; ok && v != "" {
			first = v
			if v != s.Coordinator {
				r.Source = d.Key
			}
		}
		r.Effective = resolve(d.Default, first, s.EscalationBox)
		if key, ok := s.Muted[d.Name]; ok {
			r.Muted = true
			r.Source = key
		}
		out = append(out, r)
	}
	// Muted names with no declaration of their own: the per-row condition
	// mutes ("condition.scheduler_load_failed"), each a slice of
	// condition.notice.
	for n, why := range s.Muted {
		if _, declared := byName[n]; declared {
			continue
		}
		parent, _ := Lookup(ConditionNotice)
		if n.Family() != parent.Name.Family() {
			continue
		}
		out = append(out, Row{
			Name: n, Class: ClassAlert, Default: parent.Default,
			Effective: resolve(parent.Default, s.Coordinator, s.EscalationBox),
			Muted:     true, Source: why, Key: parent.Key,
			Summary: "condition row " + n.Kind() + " only (sent as " + string(ConditionNotice) + ")",
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Muted != out[j].Muted {
			return out[i].Muted
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func resolve(rule, coordinator, escalationBox string) string {
	r := rule
	if coordinator != "" {
		r = strings.ReplaceAll(r, RoleCoordinator, coordinator)
	}
	if escalationBox != "" {
		r = strings.ReplaceAll(r, RoleEscalationBox, escalationBox)
	}
	return r
}
