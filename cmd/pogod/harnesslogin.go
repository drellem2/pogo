package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// harnessNotLoggedInConditionID is the condition a crew auto-start sweep raises
// when it refused agents because their harness positively reported no login
// (drellem2/pogo#173). One id for the whole refusal, not one per agent: the
// cause is the single missing login, and a page per refused agent is N pages
// for one fix.
const harnessNotLoggedInConditionID = "harness_not_logged_in"

// annunciateHarnessLogin raises or clears harness_not_logged_in from one
// auto-start sweep's results. It runs after EVERY sweep — the boot sweep and
// each `pogo server start` — so a refusal is re-checked on the next attempt
// rather than latched, and the condition clears as soon as a sweep refuses
// nothing.
//
// Paging is once per episode, not once per retry: the fingerprint is constant,
// so a second sweep that refuses the same way is suppressed by the annunciator
// until its renotify window, and only a Clear (a sweep that refused nothing)
// ends the episode.
func annunciateHarnessLogin(a *conditionAnnunciator, coordinator string, results []agent.AutoStartResult, now time.Time) {
	var refused []string
	detail := ""
	for _, r := range results {
		if r.Status != agent.AutoStartStatusRefusedNotLoggedIn {
			continue
		}
		refused = append(refused, r.Name)
		if detail == "" {
			detail = r.Error
		}
	}
	if len(refused) == 0 {
		a.Clear(harnessNotLoggedInConditionID, now)
		return
	}
	a.Raise(conditionHarnessNotLoggedIn(coordinator, refused, detail), now)
}

// conditionHarnessNotLoggedIn — drellem2/pogo#173. OutOfBand because the
// coordinator is normally among the refused: it cannot read a notice about
// its own absence, so the escalation box (read by a launchd job, not the
// fleet) is the reader that matters.
func conditionHarnessNotLoggedIn(to string, refused []string, detail string) pogodCondition {
	names := strings.Join(refused, ", ")
	var b strings.Builder
	fmt.Fprintf(&b, "Crew auto-start REFUSED %d agent(s): %s.\n\n", len(refused), names)
	b.WriteString("DETAIL\n  " + detail + "\n\n")
	b.WriteString("WHAT IT COSTS WHILE UNFIXED\n")
	b.WriteString("  None of those agents is running. The agent harness has no login, so a spawned\n")
	b.WriteString("  agent would sit at its first-run login screen — a browser OAuth flow only a\n")
	b.WriteString("  human can complete — and never reach its composer. pogod refuses the spawn\n")
	b.WriteString("  instead of starting a fleet that silently cannot work.\n\n")
	b.WriteString("WHAT TO DO\n")
	b.WriteString("  1. On this machine, as the user pogod runs as: run `claude` once in a\n")
	b.WriteString("     terminal and complete the login.\n")
	b.WriteString("  2. `pogo server start` — the sweep re-checks the login on every attempt;\n")
	b.WriteString("     nothing is latched, and this condition clears on the first sweep that\n")
	b.WriteString("     refuses nothing.\n")
	b.WriteString("  3. `pogo doctor --check` shows the same login reading as a row.\n\n")
	b.WriteString("WHY ONLY NOW\n")
	b.WriteString("  pogod refuses ONLY on a positive not-logged-in reading from the harness.\n")
	b.WriteString("  Anything it cannot read — an error, an older CLI, a timeout, garbage —\n")
	b.WriteString("  starts the crew anyway. You get this once per episode, not per retry.\n")

	return pogodCondition{
		ID:          harnessNotLoggedInConditionID,
		Row:         "mg-b968",
		To:          to,
		OutOfBand:   true,
		Detail:      fmt.Sprintf("auto-start refused %s: %s", names, detail),
		Fingerprint: harnessNotLoggedInConditionID,
		Subject: fmt.Sprintf("[pogod] CREW NOT STARTED — the agent harness is not logged in "+
			"(%d agent(s) refused)", len(refused)),
		Body: b.String(),
	}
}
