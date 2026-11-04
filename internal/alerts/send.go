package alerts

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync"

	"github.com/drellem2/pogo/internal/events"
)

// MailFunc is the shape every mail transport in pogo has: the one watchers
// take as an injected Options field, and the one Transport implements.
type MailFunc = func(to, from, subject, body string) error

// Mail is THE send seam. Every alert mail goes through here, named.
//
// via is the transport the caller already holds — Transport in production, a
// recorder in a unit test, or a caller's own delivery (refusalwatch confirms
// its mail landed in the maildir). It is called exactly as the caller would
// have called it: there is no nil default, because a nil transport in a unit
// test silently becoming the real `mg` would mail a live inbox.
//
// A MUTED alert (SetMutes, slice 2 of drellem2/pogo#262) is not sent: via is
// not called, an `alert_muted` event is recorded in its place, and Mail returns
// nil — the caller's detector carries on exactly as if the mail had gone, so
// it still emits its own event and keeps its own suppression state. An
// unmuted alert is passed to via unchanged and via's error is returned
// unchanged.
//
// A ROUTED alert (SetRoutes, slice 3) is sent to its route instead of to, when
// to is the alert's primary recipient (see Primary). If the route's box does
// not exist or the routed send fails, the mail goes to the route's fallback —
// the default recipient — and an alert_route_invalid event is recorded: a
// route never drops mail. That is why a send that skips this function is a
// build failure (TestNoSendBypassesTheRegistry).
func Mail(name Name, via MailFunc, to, from, subject, body string) error {
	check(name)
	if _, muted := Withhold(name, to, subject); muted {
		return nil
	}
	if r, ok := routeFor(name, to); ok {
		_, err := sendRouted(name, r, subject, func(box string) (struct{}, error) {
			return struct{}{}, via(box, from, subject, body)
		})
		return err
	}
	return via(to, from, subject, body)
}

// MailOutput is Mail for the callers that log `mg`'s own output on failure:
// it runs `mg mail send <to> --from <from> --subject <subject> --body <body>`
// and returns the command's combined output and its error, exactly as those
// callers' own exec.Command did before the seam existed.
//
// A muted alert returns no output and no error, as Mail does; a routed one is
// sent to its route, with the same fallback as Mail.
func MailOutput(name Name, to, from, subject, body string) ([]byte, error) {
	check(name)
	if _, muted := Withhold(name, to, subject); muted {
		return nil, nil
	}
	send := func(box string) ([]byte, error) {
		cmd := execCommand("mg", "mail", "send", box,
			"--from", from, "--subject", subject, "--body", body)
		return cmd.CombinedOutput()
	}
	if r, ok := routeFor(name, to); ok {
		return sendRouted(name, r, subject, send)
	}
	return send(to)
}

// Transport sends one message with `mg mail send`. It is the production
// transport (formerly client.SendMGMail) and is what pogod injects wherever a
// watcher takes a MailFunc. Calling it directly is a bypass; pass it to Mail.
func Transport(to, from, subject, body string) error {
	cmd := execCommand("mg", "mail", "send", to, "--from="+from, "--subject="+subject, "--body="+body)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg mail send failed: %s (%w)", string(out), err)
	}
	return nil
}

// execCommand is a variable for testability.
var execCommand = exec.Command

var unknownLogged sync.Map

// check reports a name the registry does not declare. It never blocks a send:
// the guard test is what keeps undeclared names out, and dropping mail over a
// bookkeeping error would be a behaviour change.
func check(name Name) {
	if _, ok := byName[name]; ok {
		return
	}
	if _, seen := unknownLogged.LoadOrStore(name, true); !seen {
		log.Printf("alerts: %q is not a declared alert; sending it anyway", name)
	}
}

// EventMuted is the event recorded in place of a muted alert's send.
const EventMuted = "alert_muted"

var (
	mutesMu sync.RWMutex
	mutes   map[Name]string
)

// SetMutes installs the muted alerts and, for each, the config key that muted
// it ("[alerts.\"ack_watch.finding\"] mute = true", "[conditions] mail =
// false"). pogod calls it once at startup with the table it resolved; nil (the
// default, and every unit test that does not call it) mutes nothing.
func SetMutes(m map[Name]string) {
	cp := make(map[Name]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	mutesMu.Lock()
	mutes = cp
	mutesMu.Unlock()
}

// MuteReason returns the key that muted name, or "" when it is not muted. It
// records nothing; Withhold is the call for a send site.
func MuteReason(name Name) string {
	mutesMu.RLock()
	defer mutesMu.RUnlock()
	return mutes[name]
}

var mutedLogged sync.Map

// emitEvent is a variable so a test can observe the alert_muted record.
var emitEvent = func(e events.Event) { events.Emit(context.Background(), e) }

// Withhold is the mute check for a send that is not mail — the blocked-reminder
// nudge — and the one Mail and MailOutput make. When name is muted it records
// an `alert_muted` event (alert, recipient, subject and the key that muted it),
// logs the first withheld send per alert per process, and reports the reason
// and true: the caller must then skip its send and nothing else. When name is
// not muted it records nothing and returns "", false.
func Withhold(name Name, to, subject string) (string, bool) {
	why := MuteReason(name)
	if why == "" {
		return "", false
	}
	RecordMuted(name, to, subject, why)
	return why, true
}

// RecordMuted records that a send of name to to was withheld because of why.
// Withhold calls it; a caller that decides the mute itself — the condition
// annunciator, whose per-row mutes (condition.<id>) are finer than any one
// alert name — calls it directly, so every withheld send leaves the same
// record whichever layer withheld it. It sends nothing.
func RecordMuted(name Name, to, subject, why string) {
	if _, seen := mutedLogged.LoadOrStore(name, true); !seen {
		log.Printf("alerts: %s is MUTED (%s) — not sent to %s; recorded as %s in events.log (logged once per process)",
			name, why, to, EventMuted)
	}
	emitEvent(events.Event{
		EventType: EventMuted,
		Agent:     "pogod",
		Details: map[string]any{
			"alert":   string(name),
			"to":      to,
			"subject": subject,
			"reason":  why,
		},
	})
}
