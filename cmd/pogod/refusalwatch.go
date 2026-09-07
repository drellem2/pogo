package main

import (
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refusalwatch"
)

// This file wires the consecutive-refusal alarm (mg-6f3d, the successor mg-6616
// was closed to produce) into pogod.
//
// It is a SIBLING of the synthetic-failure-turn detector next door, not a
// replacement, and the two are deliberately not merged. synthwatch counts
// failing turns in a trailing 30-minute WINDOW and its job is to suppress a
// restart that would destroy a live session's context. This one counts a RUN of
// consecutive turns with no window at all, and its job is to reach a person.
// They fail differently and want different evidence: a window's count is bounded
// by how busy the agent is, so the 661-turn run of 2026-08-14..08-19 reads out of
// a 30m window as "2 errors in 30m", which is also what one bad afternoon looks
// like.
//
// The reason this is a separate alarm rather than another detector is measured.
// On 2026-09-07 the wedge detector fired correctly in 14m30s and then emitted
// sixteen more findings over 3h55m, every one carrying "routed_to": "nobody".
// Detection was never the missing piece.

// refusalTargets enumerates the running agents worth scanning.
//
// Exited agents are skipped for the same reason synthTargets skips them: this
// class is about a LIVE agent that cannot complete a turn, and an exited agent's
// last transcript state is the respawn gate's business.
func refusalTargets(reg *agent.Registry) []refusalwatch.Target {
	if reg == nil {
		return nil
	}
	var out []refusalwatch.Target
	for _, a := range reg.List() {
		if a == nil || a.Status != agent.StatusRunning {
			continue
		}
		out = append(out, refusalwatch.Target{
			Name:       a.Name,
			Identity:   a.EventAgent(),
			Workdir:    a.Dir,
			WorkItemID: a.WorkItemID,
		})
	}
	return out
}

// refusalSinks are the out-of-band delivery channels, in the order they are
// tried — and ALL of them are tried, because two channels that fail
// independently is the only reason to have two.
//
// Neither requires an agent turn. The mail sink writes into the `human` mailbox
// that the out-of-process com.pogo.deadman launchd job polls, which is the
// channel with a measured record of working while the fleet was down; the ledger
// is a plain append under ~/.pogo/alarms, which needs no `mg`, no mailbox, and no
// notifier at all.
//
// The mail sink does NOT pass --create. An unknown recipient must stay a refusal
// (mg-d639): if `human` is not registered on this box, the correct outcome is a
// loud undelivered alarm, not a phantom mailbox minted at the moment of an
// outage and read by nobody thereafter.
func refusalSinks() []refusalwatch.Sink {
	return []refusalwatch.Sink{
		refusalwatch.MailSink{To: "human", From: "pogod"},
		refusalwatch.LedgerSink{},
	}
}
