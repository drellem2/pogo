package main

// The fleet answer to "which running agents are armed?".
//
// mg-d924's dead-recipient warning is installed as a harness hook at spawn, so
// it protects an agent only from that agent's next start. When it merged, every
// agent then running — mayor included, the one that had just sent five mails
// into a dead channel — kept the old behaviour, and the only instrument that
// could say so was `pogo hook mail-recipient --self-check`, run by hand, inside
// one agent, by someone who remembered to. A control with no coverage report is
// a control whose coverage nobody knows (mg-503d).
//
// So the state rides on every `pogo agent list` row, and the summary below is
// printed whether or not anything is wrong. The zero case is the one that
// matters: "0 of 9 armed" has to be as loud as a warning, because on the day
// this was written that was the true reading and it looked exactly like silence.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/hookarm"
)

// mailWarnCell is the per-row rendering of an agent's arming state.
//
// An empty state is rendered "?" rather than omitted. A blank column would read
// as "nothing to say", and what it actually means is that this pogod is too old
// to have measured — the exact substitution of silence for an answer that this
// whole ticket is about.
func mailWarnCell(a agent.AgentInfo) string {
	if a.Status != agent.StatusRunning {
		return ""
	}
	state := a.MailWarn
	if state == "" {
		state = "?"
	}
	return "  mail-warn=" + state
}

// printMailWarnSummary writes the fleet-level answer under a listing.
//
// It counts only running agents: a parked or exited entry has no session for
// the hook to be loaded into, and including them would move the denominator
// without changing who is protected.
func printMailWarnSummary(w io.Writer, agents []agent.AgentInfo) {
	running := make([]agent.AgentInfo, 0, len(agents))
	for _, a := range agents {
		if a.Status == agent.StatusRunning {
			running = append(running, a)
		}
	}
	if len(running) == 0 {
		return
	}

	byState := map[string][]string{}
	reported := 0
	for _, a := range running {
		if a.MailWarn == "" {
			continue
		}
		reported++
		byState[a.MailWarn] = append(byState[a.MailWarn], a.Name)
	}

	fmt.Fprintln(w)
	if reported == 0 {
		fmt.Fprintf(w, "mail-warn (mg-d924 dead-recipient warning): UNAVAILABLE for all %d running agent(s).\n",
			len(running))
		fmt.Fprint(w, "  This pogod does not report arming state — it predates mg-503d. That is not\n"+
			"  the same as \"all clear\": it means nothing measured. Deploy pogod onto a build\n"+
			"  that carries the field, or ask each agent with\n"+
			"  `pogo hook mail-recipient --self-check` one at a time, which is what this\n"+
			"  summary exists to replace.\n")
		return
	}

	armed := len(byState[string(hookarm.StateArmed)])
	fmt.Fprintf(w, "mail-warn (mg-d924 dead-recipient warning): %d of %d running agent(s) ARMED.\n",
		armed, len(running))

	for _, s := range []hookarm.State{hookarm.StateOff, hookarm.StatePending, hookarm.StateUnknown} {
		names := byState[string(s)]
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		fmt.Fprintf(w, "  %-8s %s\n", string(s), strings.Join(names, ", "))
		fmt.Fprintf(w, "           %s\n", mailWarnStateNote(s))
	}
	if reported < len(running) {
		fmt.Fprintf(w, "  %-8s %d running agent(s) reported no state at all\n", "?", len(running)-reported)
	}
	if armed < len(running) {
		fmt.Fprint(w, "  The hook is installed at spawn, so an unarmed agent stays unarmed until it\n"+
			"  RESTARTS onto a pogod that carries it. Mail from one to a stopped recipient\n"+
			"  reports Delivered and warns nobody.\n")
	}
}

// mailWarnStateNote says what a state costs the fleet, not what it is. A reader
// who already knows "off" learns nothing from "the hook is off"; what they need
// is that sends from those agents are silent.
func mailWarnStateNote(s hookarm.State) string {
	switch s {
	case hookarm.StateOff:
		return "no hook registered — sends to a stopped agent are silent, as before mg-d924"
	case hookarm.StatePending:
		return "registered, never seen running here — the process may predate the registration, or the command it names may not run"
	case hookarm.StateUnknown:
		return "the check could not run; `pogo agent diagnose <name> --json` carries the reason in mail_warn_detail"
	default:
		return ""
	}
}
