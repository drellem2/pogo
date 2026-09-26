package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"
)

// AuthState is what a harness's login probe established — and, as important,
// what it did NOT establish (drellem2/pogo#173).
type AuthState int

const (
	// AuthUnknown means the probe could not say either way: the binary
	// errored for some other reason, the subcommand does not exist on this
	// version, the probe timed out, or its output did not parse. It is the
	// zero value on purpose — every reading that is not affirmatively one of
	// the two below lands here, and AuthUnknown FAILS OPEN.
	AuthUnknown AuthState = iota
	// AuthLoggedIn is a positive reading that the harness has credentials.
	AuthLoggedIn
	// AuthNotLoggedIn is a POSITIVE reading that the harness has none. It is
	// the only state that refuses a crew auto-start.
	AuthNotLoggedIn
)

func (s AuthState) String() string {
	switch s {
	case AuthLoggedIn:
		return "logged_in"
	case AuthNotLoggedIn:
		return "not_logged_in"
	default:
		return "unknown"
	}
}

// AuthReading is one probe's verdict plus a human-readable account of how it
// was reached — the account is what the warning or the refusal prints.
type AuthReading struct {
	State  AuthState
	Detail string
}

// authPreflightTimeout bounds one login probe. A probe that hangs is an
// AuthUnknown reading, and AuthUnknown spawns: the timeout exists so a wedged
// harness CLI delays the boot sweep by seconds, not forever.
const authPreflightTimeout = 15 * time.Second

// ErrHarnessNotLoggedIn is returned (wrapped) when a crew auto-start was
// refused because the harness positively reported that it has no login. An
// agent spawned into that state never reaches its composer — a fresh Claude
// Code profile shows the theme picker and then a browser OAuth flow that only
// a human can complete — so the spawn would stall silently until the 60s
// initial-nudge timeout, and be misrecorded as prompt-sentinel drift.
var ErrHarnessNotLoggedIn = errors.New("agent harness is not logged in")

// authPreflight caches login readings for the length of ONE auto-start sweep,
// keyed by the resolved harness binary, so a crew of five costs one probe.
//
// It deliberately does not outlive the sweep. A refusal is re-checked on the
// next auto-start attempt, never latched until pogod restarts: the operator
// who reads the page, logs in, and runs `pogo server start` must get a fleet.
type authPreflight struct {
	readings map[string]AuthReading
}

func newAuthPreflight() *authPreflight {
	return &authPreflight{readings: map[string]AuthReading{}}
}

// check refuses the spawn of name ONLY on a positive AuthNotLoggedIn reading.
// Every other outcome — no probe declared, a custom command that is not the
// provider's own binary, AuthUnknown — returns nil and the spawn proceeds.
//
// The asymmetry is the design (PM ruling on mg-ecc8): a false refusal stops
// the WHOLE crew on a machine that works, silently, which is the failure class
// of the 18-day outage; a false spawn costs one stalled agent.
func (a *authPreflight) check(name string, p *Provider, cmd []string) error {
	if a == nil || p == nil || p.AuthPreflight == nil || len(cmd) == 0 {
		return nil
	}
	// Probe only when the configured command actually runs this provider's
	// binary. An [agents] command of `sleep 600` (sandboxes, tests) or a
	// wrapper script says nothing about the harness login, and running
	// `sleep auth status` would be nonsense.
	if filepath.Base(cmd[0]) != p.Binary {
		return nil
	}
	reading, ok := a.readings[cmd[0]]
	if !ok {
		ctx, cancel := context.WithTimeout(context.Background(), authPreflightTimeout)
		reading = p.AuthPreflight(ctx, cmd[0])
		cancel()
		a.readings[cmd[0]] = reading
		switch reading.State {
		case AuthLoggedIn:
			log.Printf("autostart: %s login preflight: logged in", p.ID)
		case AuthUnknown:
			log.Printf("autostart: WARNING: %s login preflight inconclusive (%s) — "+
				"starting crew anyway; if an agent sits at a login screen, run `%s` once "+
				"in a terminal and log in", p.ID, reading.Detail, p.Binary)
		}
	}
	if reading.State != AuthNotLoggedIn {
		return nil
	}
	return fmt.Errorf("%w: %s reports no login (%s) — refusing to auto-start %s, which "+
		"would stall at the harness's first-run login screen. Run `%s` once in a terminal, "+
		"log in, then `pogo server start`", ErrHarnessNotLoggedIn, p.ID, reading.Detail,
		name, p.Binary)
}
