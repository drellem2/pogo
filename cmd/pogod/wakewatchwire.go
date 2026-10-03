package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/wakewatch"
	"github.com/drellem2/pogo/internal/workitem"
)

// wakeWatchParams maps [wake_watch] onto the watcher's thresholds.
func wakeWatchParams(c config.WakeWatchConfig) wakewatch.Params {
	return wakewatch.Params{
		Coalesce:         c.Coalesce,
		RecoveryInterval: c.RecoveryInterval,
		RenudgeAfter:     c.RenudgeAfter,
		RenudgeEvery:     c.RenudgeEvery,
		MaxRenudges:      c.MaxRenudges,
		Lookback:         c.Lookback,
	}
}

// newWakeWatcher builds the pointer waker (mg-e00c) against pogod's real seams.
func newWakeWatcher(cfg config.WakeWatchConfig, reg *agent.Registry, coordinator string) *wakewatch.Watcher {
	root := agent.MacguffinStoreRoot()
	return wakewatch.New(wakeWatchParams(cfg), wakewatch.Deps{
		EventsPath:  filepath.Join(root, "events.jsonl"),
		StatePath:   filepath.Join(config.PogoHome(), "wakewatch", "state.json"),
		Agents:      wakeWatchAgents(reg),
		Crew:        wakeWatchCrew(reg),
		Item:        wakeWatchItem(filepath.Join(root, "work")),
		Subject:     func(box, id string) string { return wakewatch.ReadSubject(filepath.Join(root, "mail"), box, id) },
		Nudge:       wakeWatchNudge(reg),
		Mail:        client.SendMGMail,
		Emit:        emitWakeWatch,
		Coordinator: coordinator,
		Async:       true,
	})
}

func wakeWatchAgents(reg *agent.Registry) func() []wakewatch.AgentRef {
	return func() []wakewatch.AgentRef {
		if reg == nil {
			return nil
		}
		var out []wakewatch.AgentRef
		for _, a := range reg.List() {
			out = append(out, wakewatch.AgentRef{
				Name:       a.Name,
				WorkItemID: a.WorkItemID,
				Running:    a.GetStatus() == agent.StatusRunning,
				Started:    a.StartTime,
			})
		}
		return out
	}
}

// wakeWatchCrew is every configured crew agent, running or not — a parked or
// never-started crew agent is still an agent, so mail to it is bounced rather
// than read as mail to nobody.
func wakeWatchCrew(reg *agent.Registry) func() []string {
	return func() []string {
		if reg == nil {
			return nil
		}
		rep, err := reg.RosterReport()
		if err != nil {
			return nil
		}
		out := make([]string, 0, len(rep.Members))
		for _, m := range rep.Members {
			out = append(out, m.Name)
		}
		return out
	}
}

func wakeWatchItem(workRoot string) func(string) (wakewatch.Item, bool) {
	return func(id string) (wakewatch.Item, bool) {
		it, ok, err := workitem.FindLiveFrom(workRoot, id)
		if err != nil || !ok {
			return wakewatch.Item{}, false
		}
		return wakewatch.Item{Title: it.Title, Assignee: it.Assignee}, true
	}
}

// wakeWatchNudgeTimeout bounds one pointer's confirmed delivery.
const wakeWatchNudgeTimeout = agent.DefaultNudgeTimeout

// wakeWatchNudge types a pointer with CONFIRMED delivery, not through the wake
// policy (NudgeWake). The policy exists to stop re-waking a silence nobody has
// anything new for; a pointer is by construction something new — a message or
// an assignment that did not exist at the last wake — so suppressing it would
// hide work, which is the one outcome this component exists to prevent. The
// coalescing window and the three-try recovery budget are its own bound.
//
// Mid-turn, the harness takes the text and emits no receipt
// (agent.ErrNudgeQueued): recorded as queued, not failed, and not resent —
// recovery re-points it within renudge_after if it was in fact dropped.
func wakeWatchNudge(reg *agent.Registry) func(name, text string) (string, error) {
	return func(name, text string) (string, error) {
		if reg == nil {
			return wakewatch.OutcomeFailed, errors.New("no agent registry")
		}
		a := reg.Get(name)
		if a == nil || a.GetStatus() != agent.StatusRunning {
			return wakewatch.OutcomeFailed, errors.New("agent is not running")
		}
		err := a.NudgeWithModeCorrelated(text, agent.NudgeConfirm, wakeWatchNudgeTimeout, "wakewatch")
		switch {
		case err == nil:
			return wakewatch.OutcomeDelivered, nil
		case errors.Is(err, agent.ErrNudgeQueued):
			return wakewatch.OutcomeQueued, nil
		}
		return wakewatch.OutcomeFailed, err
	}
}

func emitWakeWatch(eventType, workItemID string, details map[string]any) {
	events.Emit(context.Background(), events.Event{
		EventType:  eventType,
		Agent:      "pogod",
		WorkItemID: workItemID,
		Details:    details,
	})
}

// startWakeWatch arms the waker if [wake_watch] is enabled.
func startWakeWatch(ctx context.Context, cfg config.WakeWatchConfig, reg *agent.Registry, coordinator string, logf func(string, ...any)) *wakewatch.Watcher {
	if !cfg.Enabled {
		logf("pogod: wake-watch DISABLED by [wake_watch] enabled = false — no pointer nudges, re-nudges or bounces (mg-e00c)")
		return nil
	}
	if reg == nil {
		logf("pogod: wake-watch not armed: no agent registry")
		return nil
	}
	w := newWakeWatcher(cfg, reg, coordinator)
	go w.Run(ctx)
	logf("%s", wakeWatchArmedBanner(w.Params(), coordinator))
	return w
}

// wakeWatchArmedBanner is the startup line for an armed waker. It states only
// what pogod knows: the waker is live and these are its pointer parameters.
// Whether an agent also keeps a mail-check timer is that agent's own
// registration, not something pogod asserts here — the line once said
// "SHADOW ... mail-check timers stay ON" long after phase 2 (mg-aa74) made both
// false (mg-e7f4a).
func wakeWatchArmedBanner(p wakewatch.Params, coordinator string) string {
	return fmt.Sprintf("pogod: wake-watch armed — enabled by [wake_watch] (mg-e00c); pointers <=%d bytes on mail/assignment arrival, coalesce=%s, "+
		"re-nudge after %s every %s up to %d then mail %s; bounces to non-running recipients",
		wakewatch.MaxPointerLen, p.Coalesce, p.RenudgeAfter, p.RenudgeEvery, p.MaxRenudges, coordinator)
}
