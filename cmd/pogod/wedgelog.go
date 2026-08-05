package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/wedgewatch"
)

// The daemon-log consumer for the wedged-agent detector (mg-fc8d).
//
// It exists because the detector deliberately has no mail seam. Item (3) of
// mg-fc8d — escalating a fleet-level wedge OUTSIDE the wedged party — is an
// alerting-policy decision reserved to Daniel and unruled, so nothing here
// picks a recipient. What it CAN do without making that decision is put the
// finding somewhere a person already looks: pogod's log, beside the event
// spine.
//
// That is not a substitute for routing and this file does not pretend
// otherwise. The 2026-08-04 lesson is precisely that a correct alarm delivered
// only to somewhere nobody was reading is not an alarm — stall-watch fired every
// five minutes for thirteen hours and was right the whole time. Every line
// printed here says so, so that a reader who finds one does not assume somebody
// else was told.

var wedgeLogState struct {
	mu          sync.Mutex
	lastPrinted string
	lastAt      time.Time
}

// wedgeLogRepeat is how long an unchanged roster stays out of the log. The
// heartbeat ticks every ~30s and the detector samples every 5m; printing the
// same roster on each sample would put ~290 identical lines a day into pogod's
// log and train every reader to scroll past them.
const wedgeLogRepeat = time.Hour

func logWedgeFindings(w *wedgewatch.Watcher, now time.Time) {
	if w == nil {
		return
	}
	findings, at := w.Latest()
	if at.IsZero() {
		return
	}
	if len(findings) == 0 {
		wedgeLogState.mu.Lock()
		if wedgeLogState.lastPrinted != "" {
			log.Printf("pogod: wedge-watch: no agent is wedged (the previously reported ones are taking turns again)")
			wedgeLogState.lastPrinted = ""
			wedgeLogState.lastAt = time.Time{}
		}
		wedgeLogState.mu.Unlock()
		return
	}

	var b strings.Builder
	for _, f := range findings {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	print := b.String()

	wedgeLogState.mu.Lock()
	repeat := print == wedgeLogState.lastPrinted && now.Sub(wedgeLogState.lastAt) < wedgeLogRepeat
	if !repeat {
		wedgeLogState.lastPrinted = print
		wedgeLogState.lastAt = now
	}
	wedgeLogState.mu.Unlock()
	if repeat {
		return
	}

	log.Printf("pogod: wedge-watch: %d agent(s) are ANIMATING BUT NOT WORKING. "+
		"NOBODY HAS BEEN TOLD — this detector is report-only and mg-fc8d's escalation policy is "+
		"unruled, so this log line and the event spine are the whole of the notification.\n%s"+
		"Each line pairs the process uptime with the agent's OWN work counter; that pairing is the "+
		"evidence. Read a PTY with `pogo agent output <name>`.",
		len(findings), print)
}
