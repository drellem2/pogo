package turnlog

import (
	"fmt"
	"os"
	"time"
)

// Probe is the POSITIVE CONTROL, and mg-a270 made it an acceptance requirement
// rather than a nicety:
//
//	A liveness check that has never been observed failing is a presence check
//	until proven otherwise.
//
// Every signal that read green through the 22-hour outage was a check nobody
// had ever seen go red. Each was correct about the thing it measured and none
// had a demonstrated failing arm, so "green" and "cannot go red" were
// indistinguishable from the outside for the entire window. This probe removes
// that ambiguity for THIS check by constructing the failure and requiring the
// real Scan to report it.
//
// It builds a throwaway turnlog tree containing three agents in one population:
//
//	probe-live      wrote a turn-completion line just now         -> want live
//	probe-stale     wrote one, long before MaxAge                 -> want stale
//	probe-silent    present, never wrote one at all               -> want silent
//
// probe-silent is the important one. It is the exact state mayor, pa and
// architect were in — present, running, and producing no artifact a completed
// turn is needed for — and it is present in the population while absent from
// the turnlog directory, so a scan that enumerated files instead of the
// population would score it as nothing at all. probe-live is the other half of
// the control: without it a probe that reported RED unconditionally would also
// pass.
//
// It calls Scan. It does not reimplement any part of it. A probe that
// reimplemented the check would vouch for the copy.
type ProbeResult struct {
	Root string `json:"root"`

	// Verdicts observed for the three fixtures.
	LiveVerdict   Verdict `json:"live_verdict"`
	StaleVerdict  Verdict `json:"stale_verdict"`
	SilentVerdict Verdict `json:"silent_verdict"`

	// WentRed is true when the check reported a finding for BOTH the agent
	// that stopped completing turns and the agent that never started.
	WentRed bool `json:"went_red"`
	// StayedGreen is true when the agent that did complete a turn was not
	// reported. A control that reddens everything is not a control.
	StayedGreen bool `json:"stayed_green"`
	// Passed is WentRed && StayedGreen: this check can distinguish a fleet
	// that is working from one that is merely present.
	Passed bool `json:"passed"`

	Findings int    `json:"findings"`
	Detail   string `json:"detail"`
}

// Probe runs the positive control in root, which the caller owns and should
// remove. root must be empty or nonexistent.
func Probe(root string) (ProbeResult, error) {
	res := ProbeResult{Root: root}
	now := time.Now().UTC()

	if err := os.MkdirAll(root, 0755); err != nil {
		return res, fmt.Errorf("probe fixture: %w", err)
	}
	if err := AppendIn(root, "probe-live", "probe: completed turn", now.Add(-time.Second)); err != nil {
		return res, fmt.Errorf("probe fixture: %w", err)
	}
	// Old enough to be stale under any threshold this probe would be run
	// with, and old enough to sit outside the 22h window that motivated the
	// ticket.
	if err := AppendIn(root, "probe-stale", "probe: last turn before the bounce", now.Add(-48*time.Hour)); err != nil {
		return res, fmt.Errorf("probe fixture: %w", err)
	}
	// probe-silent gets NO file. That absence is the fixture.

	rep, err := Scan(Options{
		Root:   root,
		Now:    now,
		MaxAge: DefaultMaxAge,
		Population: func() ([]Present, error) {
			return []Present{
				{Name: "probe-live", Type: "crew"},
				{Name: "probe-stale", Type: "crew"},
				{Name: "probe-silent", Type: "crew"},
			}, nil
		},
	})
	if err != nil {
		return res, fmt.Errorf("probe scan: %w", err)
	}
	res.Findings = rep.Findings

	byName := map[string]State{}
	for _, s := range rep.Agents {
		byName[s.Agent] = s
	}
	res.LiveVerdict = byName["probe-live"].Verdict
	res.StaleVerdict = byName["probe-stale"].Verdict
	res.SilentVerdict = byName["probe-silent"].Verdict

	res.WentRed = res.StaleVerdict == VerdictStale && res.SilentVerdict == VerdictSilent
	res.StayedGreen = res.LiveVerdict == VerdictLive
	res.Passed = res.WentRed && res.StayedGreen

	switch {
	case res.Passed:
		res.Detail = "the check reported the agent that stopped completing turns and the agent that never started one, and left the agent that did alone"
	case !res.WentRed:
		res.Detail = fmt.Sprintf("THE CHECK DID NOT GO RED: stale=%s (want %s), silent=%s (want %s). "+
			"Until this arm fires, a green reading from this check means nothing",
			res.StaleVerdict, VerdictStale, res.SilentVerdict, VerdictSilent)
	default:
		res.Detail = fmt.Sprintf("the check reddened an agent that HAD completed a turn: live=%s (want %s). "+
			"A check that reports everyone is not measuring anything", res.LiveVerdict, VerdictLive)
	}
	return res, nil
}
