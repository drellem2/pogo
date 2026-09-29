package main

import (
	"context"
	"log"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/reaper"
	"github.com/drellem2/pogo/internal/service"
)

// startReaper wires the tier-1 heartbeat reaper (mg-d18b) into pogod. It runs
// as a goroutine on its own ticker — deliberately NOT a LaunchAgent, because
// the nondemand-spawn wedge (mg-50e0) means anything relying on being spawned
// by launchd never starts. The reaper only KICKSTARTS (a demand spawn, which
// works), so it lives inside the already-running pogod.
//
// The reaper library (internal/reaper) is launchd-free and fully unit-tested;
// pogod supplies the two real-world seams: service.KickstartJob (the only
// launchctl call) and client.SendMGMail (the escalation channel). Its give-up
// mail goes to coordinator ([agents] coordinator) and to escalationBox ([agents]
// escalation_box), the box every other watcher escalation goes to
// (drellem2/pogo#148).
func startReaper(ctx context.Context, cfg config.ReaperConfig, coordinator, escalationBox string) {
	if !cfg.Enabled {
		log.Printf("pogod: reaper disabled")
		return
	}
	r := reaper.New(reaperOptions(cfg, coordinator, escalationBox))
	go r.Run(ctx, cfg.Interval)
}

// reaperOptions builds the production reaper.Options from config: the real
// kickstart and mail seams, and the give-up mail routed to coordinator and
// escalationBox.
func reaperOptions(cfg config.ReaperConfig, coordinator, escalationBox string) reaper.Options {
	jobs := make([]reaper.Job, 0, len(cfg.Jobs))
	for _, j := range cfg.Jobs {
		jobs = append(jobs, reaper.Job{
			Label:     j.Label,
			Heartbeat: j.Heartbeat,
			Period:    j.Period,
		})
	}

	return reaper.Options{
		Jobs:          jobs,
		MaxKickstarts: cfg.MaxKickstarts,
		Kickstart:     service.KickstartJob,
		Mail:          client.SendMGMail,
		EscalateTo:    escalationBox,
		Coordinator:   coordinator,
	}
}
