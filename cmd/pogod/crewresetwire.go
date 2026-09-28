package main

import (
	"context"
	"errors"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/crewreset"
	"github.com/drellem2/pogo/internal/events"
)

// crewResetPopulation is the set of agents [crew_reset] may ask to stop
// themselves (mg-5b58d): running crew agents for which `pogo agent stop` is a
// RESET, not an outage. That means restart_on_crash (the supervisor respawns
// them), auto_start in the desired state (not an on-demand agent someone turned
// on by hand) and no park flag. Polecats are out of scope.
//
// An unreadable prompt tree fails the whole sample rather than guessing: an
// agent we cannot classify is not asked.
func crewResetPopulation(reg *agent.Registry) func() ([]crewreset.Present, error) {
	return func() ([]crewreset.Present, error) {
		if reg == nil {
			return nil, errors.New("no agent registry")
		}
		var out []crewreset.Present
		for _, a := range reg.List() {
			if a.Type != agent.TypeCrew || a.GetStatus() != agent.StatusRunning {
				continue
			}
			if !a.RestartOnCrash || agent.IsParked(a.Name) {
				continue
			}
			expected, err := agent.DesiredStateFor(a.Name)
			if err != nil {
				return nil, err
			}
			if !expected {
				continue
			}
			out = append(out, crewreset.Present{Name: a.Name, StartedAt: a.StartTime})
		}
		return out, nil
	}
}

// crewResetNudge types the short pointer with confirmed delivery. A mid-turn
// write the harness takes without a receipt counts as delivered — the mail is
// the notice; the pointer only shortens the wait for it.
func crewResetNudge(reg *agent.Registry) func(name, text string) error {
	return func(name, text string) error {
		a := reg.Get(name)
		if a == nil || a.GetStatus() != agent.StatusRunning {
			return errors.New("agent is not running")
		}
		err := a.NudgeWithModeCorrelated(text, agent.NudgeConfirm, agent.DefaultNudgeTimeout, "crewreset")
		if errors.Is(err, agent.ErrNudgeQueued) {
			return nil
		}
		return err
	}
}

// startCrewReset arms the crew context-reset notice if [crew_reset] is enabled.
// wakeWatchOn decides the pointer: wake-watch already types one for every mail
// to a running agent, so crew-reset types its own only when wake-watch is off.
func startCrewReset(ctx context.Context, cfg config.CrewResetConfig, wakeWatchOn bool, reg *agent.Registry, logf func(string, ...any)) *crewreset.Watcher {
	if !cfg.Enabled {
		logf("pogod: crew-reset DISABLED by [crew_reset] enabled = false — crew agents are not asked to reset their context (mg-5b58d)")
		return nil
	}
	if reg == nil {
		logf("pogod: crew-reset not armed: no agent registry")
		return nil
	}
	o := crewreset.Options{
		After:         cfg.After,
		RenoticeAfter: cfg.RenoticeAfter,
		Interval:      cfg.Interval,
		Exclude:       cfg.Exclude,
		Population:    crewResetPopulation(reg),
		Mail:          client.SendMGMail,
		Emit: func(eventType string, d map[string]any) {
			events.Emit(context.Background(), events.Event{EventType: eventType, Agent: "pogod", Details: d})
		},
		Logf: logf,
	}
	pointer := "wake-watch's"
	if !wakeWatchOn {
		o.Nudge = crewResetNudge(reg)
		pointer = "its own"
	}
	w := crewreset.New(o)
	go w.Run(ctx)
	eff := w.Options()
	logf("pogod: crew-reset armed (mg-5b58d) — asks running auto_start restart_on_crash crew agents to note-and-restart "+
		"after %s, one reminder after %s, then quiet; sample every %s; exclude=%v; pointer: %s",
		eff.After, eff.RenoticeAfter, eff.Interval, eff.Exclude, pointer)
	return w
}
