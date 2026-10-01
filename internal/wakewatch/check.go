package wakewatch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/mailbox"
)

// The shadow join behind `pogo check-wakewatch` — phase 2's gate (mg-5496).
//
// THE QUESTION: every mail a TIMER-DRIVEN mail-check turn read — was a pointer
// already sent for it? A read with no pointer before it is a MISS: the timer
// found work the pointer hook did not announce, which is exactly the leak that
// must be zero before the timers come off.
//
// A read is attributed to a timer when it lands in one of the boxes a
// `mail-check-*` fire told its agent to read (the agent's own name, and for
// `mail-check-mg-<id>` the work-item box) within CheckReadWindow of that fire.
// A read that happens to fall in that window for another reason is judged too;
// that can only add rows, never hide a miss.
//
// Everything wakewatch declined to point at is recorded as an event, so a row
// that is not COVERED names WHY rather than just counting it. Only the MISS
// classes fail the gate.

// Row classes.
const (
	ClassCovered = "COVERED"
	// ClassMiss: no pointer was attempted for this mail at all.
	ClassMiss = "MISS"
	// ClassMissLate: the only pointer came after the read, and none was
	// delivered.
	ClassMissLate = "MISS-LATE"
	// ClassDeliveredLate: no pointer was delivered before the read, but one
	// WAS delivered after it — the timer won a race the pointer would have
	// covered (mg-35a7e). Not a miss; the row carries the send-to-pointer lag
	// so the race's size is on the page, not a manual join away.
	ClassDeliveredLate = "DELIVERED-LATE"
	// ClassMissFailed: a pointer was attempted before the read, not delivered,
	// and never delivered after it either.
	ClassMissFailed = "MISS-FAILED"
	// ClassBounced: the recipient was not running when it was sent.
	ClassBounced = "BOUNCED"
	// ClassSkipped: deliberately not pointed at (self-sent, not an agent box).
	ClassSkipped = "SKIPPED"
	// ClassPreArm: sent while wakewatch was not armed — nothing to judge.
	ClassPreArm = "PRE-ARM"
	// ClassNoSend: the read has no mail.sent in the store (outside the window,
	// or a rebuilt store). Undecidable, reported rather than absorbed.
	ClassNoSend = "NO-SEND-RECORD"
)

// IsMiss reports whether a class fails the gate.
func IsMiss(class string) bool {
	return class == ClassMiss || class == ClassMissLate || class == ClassMissFailed
}

// CheckReadWindow is how long after a mail-check fire a read is attributed to
// that fire.
const CheckReadWindow = 10 * time.Minute

// CheckRow is one timer-driven read.
type CheckRow struct {
	MsgID      string    `json:"msg_id"`
	Box        string    `json:"box"`
	From       string    `json:"from,omitempty"`
	Agent      string    `json:"agent"`
	ScheduleID string    `json:"schedule_id"`
	Sent       time.Time `json:"sent,omitempty"`
	Fire       time.Time `json:"fire"`
	Read       time.Time `json:"read"`
	// Pointer is the first delivered (or queued) pointer for a DELIVERED-LATE
	// row, and PointerLagS its distance from the send, in seconds.
	Pointer     time.Time `json:"pointer,omitempty"`
	PointerLagS int       `json:"pointer_lag_s,omitempty"`
	Class       string    `json:"class"`
	Detail      string    `json:"detail,omitempty"`
}

// CheckReport is the join's result.
type CheckReport struct {
	Since     time.Time      `json:"since"`
	Now       time.Time      `json:"now"`
	Fires     int            `json:"mail_check_fires"`
	Reads     int            `json:"timer_driven_reads"`
	Pointers  int            `json:"pointers_sent"`
	Renudges  int            `json:"renudges"`
	Bounces   int            `json:"bounces"`
	Unconsume int            `json:"unconsumed"`
	MaxLen    int            `json:"max_pointer_len"`
	OverCap   int            `json:"pointers_over_cap"`
	Armed     []time.Time    `json:"armed,omitempty"`
	Counts    map[string]int `json:"counts"`
	Rows      []CheckRow     `json:"rows"`
	// Blind lists every reason the report cannot speak for the window. A
	// non-empty Blind means "no data", never "no misses".
	Blind []string `json:"blind,omitempty"`
}

// Misses returns the rows that fail the gate.
func (r CheckReport) Misses() []CheckRow {
	var out []CheckRow
	for _, row := range r.Rows {
		if IsMiss(row.Class) {
			out = append(out, row)
		}
	}
	return out
}

type fire struct {
	at    time.Time
	agent string
	sched string
}

type attempt struct {
	at      time.Time
	outcome string
}

type storeLine struct {
	storeEvent
	at time.Time
}

// Check runs the join over pogod events (the wake_* and scheduler events in
// the window) and macguffin's events.jsonl.
func Check(pogoEvents []events.Event, storePath string, since, now time.Time) CheckReport {
	rep := CheckReport{Since: since, Now: now, Counts: map[string]int{}}

	firesByBox := map[string][]fire{}
	attempts := map[string][]attempt{}
	bounced := map[string]bool{}
	skipped := map[string]string{}
	for _, ev := range pogoEvents {
		at, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if err != nil || at.Before(since) {
			continue
		}
		d := ev.Details
		switch ev.EventType {
		case "scheduler_fire_delivered":
			sched := str(d["schedule_id"])
			if !strings.HasPrefix(sched, "mail-check-") {
				continue
			}
			agent := str(d["to"])
			rep.Fires++
			f := fire{at: at, agent: agent, sched: sched}
			for _, b := range fireBoxes(sched, agent) {
				firesByBox[b] = append(firesByBox[b], f)
			}
		case EventArmed:
			rep.Armed = append(rep.Armed, at)
		case EventPointerSent, EventRenudge:
			if ev.EventType == EventPointerSent {
				rep.Pointers++
			} else {
				rep.Renudges++
			}
			if n := num(d["text_len"]); n > 0 {
				if n > rep.MaxLen {
					rep.MaxLen = n
				}
				if n > MaxPointerLen {
					rep.OverCap++
				}
			}
			for _, id := range strs(d["msg_ids"]) {
				attempts[id] = append(attempts[id], attempt{at: at, outcome: str(d["outcome"])})
			}
		case EventBounce:
			rep.Bounces++
			bounced[str(d["msg_id"])] = true
		case EventPointerSkipped:
			if id := str(d["msg_id"]); id != "" {
				skipped[id] = str(d["reason"])
			}
		case EventUnconsumed:
			rep.Unconsume++
		}
	}
	for b := range firesByBox {
		fs := firesByBox[b]
		sort.Slice(fs, func(i, j int) bool { return fs[i].at.Before(fs[j].at) })
	}

	lines, err := readStore(storePath)
	if err != nil {
		rep.Blind = append(rep.Blind, fmt.Sprintf("macguffin events %s unreadable: %v — no data, NOT zero misses", storePath, err))
		return rep
	}
	if len(rep.Armed) == 0 {
		rep.Blind = append(rep.Blind, "wakewatch did not arm in this window (no wake_watch_armed event) — every read is PRE-ARM; this pogod does not run wakewatch, or ran it before the window")
	}
	sort.Slice(rep.Armed, func(i, j int) bool { return rep.Armed[i].Before(rep.Armed[j]) })

	sent := map[string]storeLine{}
	for _, l := range lines {
		if l.Type == "mail.sent" && l.MsgID != "" {
			if _, ok := sent[l.MsgID]; !ok {
				sent[l.MsgID] = l
			}
		}
	}
	for _, l := range lines {
		if l.Type != "mail.read" || l.at.Before(since) {
			continue
		}
		box := mailbox.Canonical(l.Mailbox)
		f, ok := attribute(firesByBox[box], l.at)
		if !ok {
			continue
		}
		rep.Reads++
		row := CheckRow{MsgID: l.MsgID, Box: l.Mailbox, Agent: f.agent, ScheduleID: f.sched, Fire: f.at, Read: l.at}
		s, have := sent[l.MsgID]
		if have {
			row.Sent = s.at
			row.From = s.From
		}
		row.Class, row.Detail, row.Pointer = classify(have, s.at, l.at, rep.Armed, attempts[l.MsgID], bounced[l.MsgID], skipped[l.MsgID])
		if !row.Pointer.IsZero() {
			row.PointerLagS = int(row.Pointer.Sub(s.at).Seconds())
		}
		rep.Counts[row.Class]++
		rep.Rows = append(rep.Rows, row)
	}
	sort.Slice(rep.Rows, func(i, j int) bool { return rep.Rows[i].Read.Before(rep.Rows[j].Read) })
	return rep
}

// classify returns the row's class, its detail, and — for DELIVERED-LATE —
// when the first late pointer was delivered.
func classify(have bool, sentAt, readAt time.Time, armed []time.Time, atts []attempt, bounced bool, skipReason string) (string, string, time.Time) {
	var none time.Time
	if !have {
		return ClassNoSend, "no mail.sent for this id in the store", none
	}
	if len(armed) == 0 || sentAt.Before(armed[0]) {
		return ClassPreArm, "sent before wakewatch armed", none
	}
	if bounced {
		return ClassBounced, "recipient was not running when it was sent", none
	}
	if skipReason != "" {
		return ClassSkipped, skipReason, none
	}
	sorted := append([]attempt(nil), atts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].at.Before(sorted[j].at) })
	var late, failed int
	var lateOK time.Time
	for _, a := range sorted {
		ok := a.outcome == OutcomeDelivered || a.outcome == OutcomeQueued
		if a.at.After(readAt) {
			late++
			if ok && lateOK.IsZero() {
				lateOK = a.at
			}
			continue
		}
		if ok {
			return ClassCovered, a.outcome, none
		}
		failed++
	}
	switch {
	case !lateOK.IsZero():
		d := fmt.Sprintf("pointer delivered %s after the send, %s after the read",
			lateOK.Sub(sentAt).Round(time.Second), lateOK.Sub(readAt).Round(time.Second))
		if failed > 0 {
			d += fmt.Sprintf(", after %d failed attempt(s) before the read", failed)
		}
		return ClassDeliveredLate, d, lateOK
	case failed > 0:
		return ClassMissFailed, "a pointer was attempted before the read and never delivered", none
	case late > 0:
		return ClassMissLate, "the only pointer came after the read, and was not delivered", none
	}
	return ClassMiss, "no pointer was sent for this mail", none
}

// attribute finds the latest fire at or before t within CheckReadWindow.
func attribute(fs []fire, t time.Time) (fire, bool) {
	var best fire
	found := false
	for _, f := range fs {
		if f.at.After(t) {
			break
		}
		if t.Sub(f.at) <= CheckReadWindow {
			best, found = f, true
		}
	}
	return best, found
}

// fireBoxes are the boxes a mail-check fire sends its agent to.
func fireBoxes(sched, agent string) []string {
	out := []string{}
	if agent != "" {
		out = append(out, mailbox.Canonical(agent))
	}
	rest := strings.TrimPrefix(sched, "mail-check-")
	if c := mailbox.Canonical(rest); c != "" && c != mailbox.Canonical(agent) {
		out = append(out, c)
	}
	return out
}

func readStore(path string) ([]storeLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []storeLine
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var ev storeEvent
			if json.Unmarshal(line, &ev) == nil {
				if at, perr := time.Parse(time.RFC3339, ev.TS); perr == nil {
					out = append(out, storeLine{storeEvent: ev, at: at})
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Render prints the report as text.
func (r CheckReport) Render(w io.Writer) {
	fmt.Fprintf(w, "wakewatch shadow check (mg-5496 phase 2 gate), window %s .. %s\n",
		r.Since.UTC().Format(time.RFC3339), r.Now.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "  mail-check fires: %d   timer-driven reads: %d\n", r.Fires, r.Reads)
	fmt.Fprintf(w, "  pointers: %d   re-nudges: %d   bounces: %d   unconsumed: %d   longest pointer: %d bytes (cap %d, over cap: %d)\n",
		r.Pointers, r.Renudges, r.Bounces, r.Unconsume, r.MaxLen, MaxPointerLen, r.OverCap)
	if len(r.Armed) > 0 {
		fmt.Fprintf(w, "  wakewatch armed: %d time(s), first %s\n", len(r.Armed), r.Armed[0].UTC().Format(time.RFC3339))
	}
	classes := []string{ClassCovered, ClassDeliveredLate, ClassMiss, ClassMissLate, ClassMissFailed, ClassBounced, ClassSkipped, ClassPreArm, ClassNoSend}
	var parts []string
	for _, c := range classes {
		if n := r.Counts[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", c, n))
		}
	}
	if len(parts) == 0 {
		parts = []string{"none"}
	}
	fmt.Fprintf(w, "  reads by class: %s\n", strings.Join(parts, " "))
	for _, b := range r.Blind {
		fmt.Fprintf(w, "  BLIND: %s\n", b)
	}
	var late []CheckRow
	maxLag := 0
	for _, row := range r.Rows {
		if row.Class == ClassDeliveredLate {
			late = append(late, row)
			if row.PointerLagS > maxLag {
				maxLag = row.PointerLagS
			}
		}
	}
	if len(late) > 0 {
		fmt.Fprintf(w, "DELIVERED-LATE: %d (not misses: a pointer was delivered, after the timer-driven read; longest send-to-pointer lag %s)\n",
			len(late), (time.Duration(maxLag) * time.Second).String())
		for _, m := range late {
			fmt.Fprintf(w, "  %-14s %s box=%s agent=%s sent=%s read=%s pointer=%s lag=%ds — %s\n",
				m.Class, m.MsgID, m.Box, m.Agent, m.Sent.UTC().Format(time.RFC3339), m.Read.UTC().Format(time.RFC3339),
				m.Pointer.UTC().Format(time.RFC3339), m.PointerLagS, m.Detail)
		}
	}
	misses := r.Misses()
	if len(misses) == 0 {
		if len(r.Blind) == 0 {
			fmt.Fprintln(w, "MISSES: 0 — every timer-driven read of a mail sent while armed had a pointer before it")
		} else {
			fmt.Fprintln(w, "MISSES: 0 judged — but see BLIND above: this is not a clean gate")
		}
		return
	}
	fmt.Fprintf(w, "MISSES: %d (mail read by a timer-driven turn with no pointer before it)\n", len(misses))
	for _, m := range misses {
		fmt.Fprintf(w, "  %-11s %s box=%s from=%s agent=%s sent=%s read=%s (%s) — %s\n",
			m.Class, m.MsgID, m.Box, m.From, m.Agent,
			m.Sent.UTC().Format(time.RFC3339), m.Read.UTC().Format(time.RFC3339), m.ScheduleID, m.Detail)
	}
}
