package heartwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ProbeResult is the outcome of the positive control.
//
// mg-a270 made this an acceptance requirement rather than a nicety, and
// mg-d616 is the reason it is repeated here:
//
//	A liveness check that has never been observed failing is a presence check
//	until proven otherwise.
//
// The heartbeat check spent its whole life in that state. It was written down
// in mayor.md, it was never observed firing, and when it was finally measured
// it had not fired for two agents across fourteen days. "Green" and "cannot go
// red" were indistinguishable from outside for the entire window — and here
// they were indistinguishable because the executor had stopped, which is the
// one cause a threshold cannot fix.
//
// The fixture holds four agents in one population:
//
//	probe-fresh    heartbeat touched just now              -> want fresh
//	probe-stale    heartbeat older than T_stall            -> want stale
//	probe-cold     heartbeat older than T_restart          -> want restart_due
//	probe-missing  present, publishes no sweep.log at all  -> want missing
//
// probe-missing is the important one, and it is present in the population while
// absent from the tree — so a scan that enumerated files instead of the
// population would score it as nothing at all. probe-fresh is the other half of
// the control: without it, a probe that reported RED unconditionally would also
// pass.
//
// It calls Scan. It does not reimplement any part of it. A probe that
// reimplemented the check would vouch for the copy.
type ProbeResult struct {
	Root string `json:"root"`

	FreshVerdict   Verdict `json:"fresh_verdict"`
	StaleVerdict   Verdict `json:"stale_verdict"`
	ColdVerdict    Verdict `json:"cold_verdict"`
	MissingVerdict Verdict `json:"missing_verdict"`

	// WentRed is true when the check reported every one of the three agents
	// whose heartbeat is late or absent.
	WentRed bool `json:"went_red"`
	// StayedGreen is true when the agent with a fresh heartbeat was not
	// reported. A control that reddens everything is not a control.
	StayedGreen bool `json:"stayed_green"`
	// Passed is WentRed && StayedGreen.
	Passed bool `json:"passed"`

	Findings int    `json:"findings"`
	Detail   string `json:"detail"`
}

// Probe runs the positive control in root, which the caller owns and should
// remove. root must be empty or nonexistent.
func Probe(root string) (ProbeResult, error) {
	res := ProbeResult{Root: root}
	now := time.Now().UTC()

	// The two shapes the live tree grew, both exercised: a PM under
	// agents/pm/<name>/ and a non-PM in its own home dir. If PathsIn ever
	// stopped covering one of them, this probe reports `missing` for an agent
	// whose heartbeat is on disk and fresh — which is the failure worth
	// catching, since the whole detector is a path lookup.
	write := func(rel string, mtime time.Time) error {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("probe heartbeat\n"), 0644); err != nil {
			return err
		}
		return os.Chtimes(path, mtime, mtime)
	}
	for _, f := range []struct {
		rel   string
		mtime time.Time
	}{
		{filepath.Join("pm", "probe-fresh", HeartbeatFile), now.Add(-time.Minute)},
		{filepath.Join("probe-stale", HeartbeatFile), now.Add(-DefaultStallAfter - 5*time.Minute)},
		{filepath.Join("pm", "probe-cold", HeartbeatFile), now.Add(-DefaultRestartAfter - time.Hour)},
		// probe-missing gets NO file. That absence is the fixture.
	} {
		if err := write(f.rel, f.mtime); err != nil {
			return res, fmt.Errorf("probe fixture: %w", err)
		}
	}

	rep, err := Scan(ScanOptions{
		Root: root,
		Now:  now,
		Population: func() ([]Present, error) {
			// StartedAt is set well outside the grace window: an agent still
			// inside grace is reported fresh by design, and leaving it zero
			// here would make the control depend on that branch being skipped
			// rather than on the thresholds it means to exercise.
			started := now.Add(-24 * time.Hour)
			return []Present{
				{Name: "probe-fresh", Type: "crew", StartedAt: started},
				{Name: "probe-stale", Type: "crew", StartedAt: started},
				{Name: "probe-cold", Type: "crew", StartedAt: started},
				{Name: "probe-missing", Type: "crew", StartedAt: started},
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
	res.FreshVerdict = byName["probe-fresh"].Verdict
	res.StaleVerdict = byName["probe-stale"].Verdict
	res.ColdVerdict = byName["probe-cold"].Verdict
	res.MissingVerdict = byName["probe-missing"].Verdict

	res.WentRed = res.StaleVerdict == VerdictStale &&
		res.ColdVerdict == VerdictRestartDue &&
		res.MissingVerdict == VerdictMissing
	res.StayedGreen = res.FreshVerdict == VerdictFresh
	res.Passed = res.WentRed && res.StayedGreen

	switch {
	case res.Passed:
		res.Detail = "the check reported the late heartbeat, the one past T_restart and the agent " +
			"that publishes none, and left the fresh one alone"
	case !res.WentRed:
		res.Detail = fmt.Sprintf("THE CHECK DID NOT GO RED: stale=%s (want %s), cold=%s (want %s), "+
			"missing=%s (want %s). Until this arm fires, a green reading from this check means nothing",
			res.StaleVerdict, VerdictStale, res.ColdVerdict, VerdictRestartDue,
			res.MissingVerdict, VerdictMissing)
	default:
		res.Detail = fmt.Sprintf("the check reddened an agent whose heartbeat is FRESH: fresh=%s (want %s). "+
			"A check that reports everyone is not measuring anything", res.FreshVerdict, VerdictFresh)
	}
	return res, nil
}
