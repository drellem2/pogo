package alerts

// Nudges (slice 4 of drellem2/pogo#262). A nudge alert is text pogod types
// into an agent's terminal to tell it something — a stall-watch check, a
// condition's wake, crew-reset's pointer. It is muted and routed under the same
// `[alerts."<name>"]` table as mail, through the seam below:
//
//   - MUTED: the nudge is not typed, an alert_muted event is recorded (as for
//     mail), and the caller is told so it can record its own event as muted
//     rather than delivered. The detector still runs.
//   - ROUTED: the nudge is typed at the route's agent instead. If that agent
//     has no mailbox, or the routed nudge fails (the agent is not running and,
//     for stall-watch, the mail fallback failed too), the nudge goes to the
//     default recipient and an alert_route_invalid event is recorded. A route
//     never drops a nudge.
//
// Which nudge is the routed one follows the same rule as mail (Primary): a
// $coordinator default moves only the nudge addressed to that box; a
// per-message default (the blocked-on agent, the crew agent, the condition's
// addressee) moves every nudge of the alert.
//
// Not every nudge is an alert — see the package comment. Those deliveries do
// not come through here.

// NudgeFunc types text at agent to. It is the shape crew-reset and the
// condition annunciator are handed.
type NudgeFunc = func(to, text string) error

// Nudge is the send seam for a nudge alert whose transport is a NudgeFunc.
// subject labels the nudge in the alert_muted / alert_route_invalid records.
// It returns the reason when the alert is muted — via was not called and err
// is nil — and "" with via's error otherwise.
func Nudge(name Name, via NudgeFunc, to, subject, text string) (muted string, err error) {
	_, muted, err = NudgeVia(name, to, subject, func(box string) (struct{}, error) {
		return struct{}{}, via(box, text)
	})
	return muted, err
}

// NudgeVia is Nudge for a transport that reports more than an error — the
// stall-watch nudger, which returns how the notice was delivered. send is
// called with the recipient, which a route may have changed; its result is
// returned unchanged.
func NudgeVia[T any](name Name, to, subject string, send func(to string) (T, error)) (out T, muted string, err error) {
	check(name)
	if why, ok := Withhold(name, to, subject); ok {
		return out, why, nil
	}
	if r, ok := routeFor(name, to); ok {
		out, err = sendRouted(name, r, subject, send)
		return out, "", err
	}
	out, err = send(to)
	return out, "", err
}
