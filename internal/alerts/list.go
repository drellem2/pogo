package alerts

import (
	"sort"
	"strings"
)

// Settings is what the listing needs from the daemon's loaded config. pogod
// fills it; this package does not read config itself.
//
// Slice 1 lists only what is ALREADY in force: there is no [alerts] section
// yet, so every value here comes from an existing key.
type Settings struct {
	// Coordinator and EscalationBox are the resolved [agents] coordinator and
	// [agents] escalation_box.
	Coordinator   string
	EscalationBox string
	// NotifyTo holds each watcher's resolved notify_to, keyed as Def.Key
	// spells it ("ack_watch.notify_to"). It replaces $coordinator for alerts
	// whose Key names it.
	NotifyTo map[string]string
	// Muted names the alerts an existing key already silences, with that key
	// ("conditions.mail = false"). Listing only — slice 1 changes no send.
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
