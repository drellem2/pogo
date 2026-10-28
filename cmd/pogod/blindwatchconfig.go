package main

import (
	"fmt"

	"github.com/drellem2/pogo/internal/config"
)

// blindWatchStartupLine is pogod's startup line for blind-watch (mg-d616),
// including when it is OFF. drellem2/pogo#244 was filed asking for a switch
// that already existed (`[blind_watch] enabled`), because pogod said nothing
// when it was off; every state now prints a line, and `[blind_watch] mail` /
// `out_of_band` (#244) say which of its mail is withheld.
func blindWatchStartupLine(c config.BlindWatchConfig, escalationBox string) string {
	if !c.Enabled {
		return "pogod: blind-watch NOT armed (disabled by config: [blind_watch] enabled = false) — " +
			"no blind_watch_* events and no mail; nothing reads wedge_watch_error"
	}
	line := fmt.Sprintf("pogod: blind-watch enabled (interval=%s hold_down=%s stale_after=%s renotify=%s) — "+
		"wedge_watch_error finally has a consumer (mg-d616); a finding about the DETECTOR routes to %s, "+
		"report-only, and wedge-watch's own findings stay unrouted (mg-fc8d item 3)",
		c.Interval, c.HoldDown, c.StaleAfter, c.RenotifyAfter, escalationBox)
	switch {
	case !c.Mail:
		line += "; ALL blind-watch mail is OFF ([blind_watch] mail = false) — findings are recorded " +
			"in events.log only (notified=false, reason=mail_disabled)"
	case !c.OutOfBand:
		line += fmt.Sprintf("; no mail to %s ([blind_watch] out_of_band = false) — a finding routed only "+
			"there, such as a stopped or empty detector, is recorded in events.log only (mail_suppressed)",
			escalationBox)
	}
	return line
}
