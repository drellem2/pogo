package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
)

// Muting (slice 2 of drellem2/pogo#262). Every alert can be muted with
// `[alerts."<name>"] mute = true`; the older per-section keys that already
// silenced some alerts are ALIASES for that mute, resolved here into one table
// that the send seam (alerts.Mail / alerts.Withhold) enforces:
//
//	[conditions] mail = false             condition.notice, condition.out_of_band
//	[conditions] out_of_band = false      condition.out_of_band
//	[conditions] disable = ["<id>", ...]  condition.<id>, one row each
//	[blind_watch] mail = false            blind_watch.finding, .cleared, .out_of_band
//	[blind_watch] out_of_band = false     blind_watch.out_of_band
//	[stall_watch] blocked_reminder_enabled = false
//	                                      stall_watch.blocked_reminder
//
// Precedence: an [alerts] entry wins over an alias key, and pogod warns when
// `mute = false` overrides an alias that muted the alert. An install with no
// [alerts] table and none of the alias keys set mutes nothing.
//
// No alert is unmutable (Daniel, #262 Q2). The safety net instead is loud:
// mutedAlertsLine names every muted alert at startup, `pogo alerts` lists them
// first, and every withheld send is recorded as an `alert_muted` event.
//
// `enabled` switches are NOT aliases: they turn a detector off, so it emits
// nothing at all. A mute leaves the detector running.

// conditionRowPrefix is the family of the per-row condition names:
// `condition.<row id>` mutes one annunciator row, as `[conditions] disable`
// listing that id does. These names are not in the registry — every row
// sends as condition.notice — so they are validated by family alone.
const conditionRowPrefix = "condition."

// alertMuteTable is the resolved mute state.
type alertMuteTable struct {
	// Muted maps each muted alert name to the key that muted it, as a reader
	// would find it in config.toml.
	Muted map[alerts.Name]string
	// Warnings are startup lines about the [alerts] table: unknown names,
	// names that cannot be muted, and overrides of an alias key.
	Warnings []string
}

func (t alertMuteTable) reason(n alerts.Name) string { return t.Muted[n] }

// resolveAlertMutes builds the mute table from cfg.
func resolveAlertMutes(cfg *config.Config) alertMuteTable {
	t := alertMuteTable{Muted: map[alerts.Name]string{}}
	legacy := legacyAlertMutes(cfg)
	for _, n := range sortedMuteNames(legacy) {
		why := legacy[n]
		if id, ok := strings.CutPrefix(string(n), conditionRowPrefix); ok && !isDeclared(n) && !knownConditionID(id) {
			// A [conditions] disable entry that names no condition row
			// would mute nothing while being listed as muted.
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ %s, which names no condition row — ignored; %s", why, conditionIDsHint()))
			delete(legacy, n)
			continue
		}
		t.Muted[n] = why
	}

	names := make([]string, 0, len(cfg.Alerts))
	for n := range cfg.Alerts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, raw := range names {
		a := cfg.Alerts[raw]
		if !a.MuteSet {
			continue
		}
		n := alerts.Name(raw)
		if problem := mutableAlertName(n); problem != "" {
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ [alerts.%q] %s — ignored; `pogo alerts` lists the names", raw, problem))
			continue
		}
		if a.Mute {
			t.Muted[n] = fmt.Sprintf("[alerts.%q] mute = true", raw)
			continue
		}
		if why, ok := legacy[n]; ok {
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ [alerts.%q] mute = false overrides %s — %s is NOT muted", raw, why, raw))
		}
		delete(t.Muted, n)
	}
	// condition.wake wakes the addressee to read condition.notice, so the
	// annunciator never wakes for a notice it withheld. List that as the
	// mute it is — even over [alerts."condition.wake"] mute = false, which
	// cannot take effect and is reported as such.
	if why := t.Muted[alerts.ConditionNotice]; why != "" {
		a, set := cfg.Alerts[string(alerts.ConditionWake)]
		if set && a.MuteSet && !a.Mute {
			t.Warnings = append(t.Warnings, fmt.Sprintf(
				"pogod: ⚠ [alerts.%q] mute = false has no effect while condition.notice is muted (%s) — there is no notice to wake anyone for",
				string(alerts.ConditionWake), why))
		}
		if _, muted := t.Muted[alerts.ConditionWake]; !muted {
			t.Muted[alerts.ConditionWake] = "follows condition.notice: " + why
		}
	}
	return t
}

// legacyAlertMutes is what the alias keys alone mute.
func legacyAlertMutes(cfg *config.Config) map[alerts.Name]string {
	m := map[alerts.Name]string{}
	switch {
	case !cfg.Conditions.Mail:
		m[alerts.ConditionNotice] = "[conditions] mail = false"
		m[alerts.ConditionOutOfBand] = "[conditions] mail = false"
	case !cfg.Conditions.OutOfBand:
		m[alerts.ConditionOutOfBand] = "[conditions] out_of_band = false"
	}
	for _, id := range cfg.Conditions.Disable {
		if id = strings.TrimSpace(id); id != "" {
			m[alerts.Name(conditionRowPrefix+id)] = "[conditions] disable lists " + id
		}
	}
	switch {
	case !cfg.BlindWatch.Mail:
		m[alerts.BlindWatchFinding] = "[blind_watch] mail = false"
		m[alerts.BlindWatchCleared] = "[blind_watch] mail = false"
		m[alerts.BlindWatchOutOfBand] = "[blind_watch] mail = false"
	case !cfg.BlindWatch.OutOfBand:
		m[alerts.BlindWatchOutOfBand] = "[blind_watch] out_of_band = false"
	}
	if !cfg.StallWatch.BlockedReminderEnabled {
		m[alerts.StallWatchBlockedReminder] = "[stall_watch] blocked_reminder_enabled = false"
	}
	return m
}

// mutableAlertName says why n cannot be muted, or "" when it can: a declared
// alert, or a per-row condition name.
func mutableAlertName(n alerts.Name) string {
	if d, ok := alerts.Lookup(n); ok {
		if d.Class != alerts.ClassAlert {
			return fmt.Sprintf("is a %s mechanic, not an alert, and cannot be muted", d.Class)
		}
		return ""
	}
	if id, ok := strings.CutPrefix(string(n), conditionRowPrefix); ok && id != "" {
		if knownConditionID(id) {
			return ""
		}
		return fmt.Sprintf("names no condition row (%q is not a condition id; %s)", id, conditionIDsHint())
	}
	return "names no alert pogod sends"
}

func conditionIDsHint() string {
	return fmt.Sprintf("the ids are %s, or %s followed by a subject",
		strings.Join(conditionIDs, ", "), strings.Join(conditionIDPrefixes, ", "))
}

func isDeclared(n alerts.Name) bool {
	_, ok := alerts.Lookup(n)
	return ok
}

func sortedMuteNames(m map[alerts.Name]string) []alerts.Name {
	out := make([]alerts.Name, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// conditionRowMutes are the per-row condition mutes, keyed by row id (the
// part after "condition."), for the annunciator.
func (t alertMuteTable) conditionRowMutes() map[string]string {
	rows := map[string]string{}
	for n, why := range t.Muted {
		if _, declared := alerts.Lookup(n); declared {
			continue
		}
		if id, ok := strings.CutPrefix(string(n), conditionRowPrefix); ok {
			rows[id] = why
		}
	}
	return rows
}

// mutedAlertsLine is the loud startup line naming every muted alert, or ""
// when none is. It is printed whatever muted them, because a muted safety
// alert (stranded work, a scheduler that did not load) has no reader at all
// but events.log.
func mutedAlertsLine(t alertMuteTable) string {
	if len(t.Muted) == 0 {
		return ""
	}
	names := make([]string, 0, len(t.Muted))
	for n := range t.Muted {
		names = append(names, string(n))
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s (%s)", n, t.Muted[alerts.Name(n)]))
	}
	return fmt.Sprintf("pogod: ⚠ %d ALERT(S) MUTED — sent to NOBODY; their detectors still run and each withheld send is recorded as %s in events.log: %s",
		len(names), alerts.EventMuted, strings.Join(parts, "; "))
}

// effectiveBlindWatch is c with Mail and OutOfBand restated from the mute
// table, so the blind-watch startup line describes what the seam will do
// rather than what the alias keys alone said.
func effectiveBlindWatch(c config.BlindWatchConfig, t alertMuteTable) config.BlindWatchConfig {
	oob := t.reason(alerts.BlindWatchOutOfBand) != ""
	c.Mail = !(oob && t.reason(alerts.BlindWatchFinding) != "" && t.reason(alerts.BlindWatchCleared) != "")
	c.OutOfBand = !oob
	return c
}

// stallWatchConfigForMutes keeps the blocked-reminder DETECTOR running.
// `blocked_reminder_enabled = false` used to switch the check off; it is now
// an alias for muting stall_watch.blocked_reminder, so the check still runs,
// still emits its stall_watch_fired events, and only the nudge is withheld.
func stallWatchConfigForMutes(c config.StallWatchConfig) config.StallWatchConfig {
	c.BlockedReminderEnabled = true
	return c
}
