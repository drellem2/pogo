package main

import (
	"context"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/filernotify"
	"github.com/drellem2/pogo/internal/workitem"
)

// newFilerNotifier builds the completion notifier with pogod's real probes
// (mg-f120). Kept out of main.go's body so that each seam is named next to what
// it does and why that source is the right one.
func newFilerNotifier(coordinator string, reg *agent.Registry) *filernotify.Notifier {
	n := filernotify.New(
		coordinator,
		client.SendMGMail,
		client.MGWorkItemFiling,
		workitem.ReadResult,
		agentKnown(reg),
	)
	n.SetRecorder(recordFilerNotice)
	return n
}

// agentKnown reports whether a name still denotes an agent this daemon knows.
//
// THE REGISTRY, NOT THE MAILBOX, and that is the whole rule for a creator that
// is a polecat that no longer exists. mg keeps a mailbox forever once it is
// made, so "a mailbox exists under this name" answers "was this name ever used"
// — it cannot answer "is anyone reading". The registry can: it holds crew
// agents whether they are running or parked (a parked pm reads its mail when it
// comes back, so it is a real recipient), and it holds polecats only while they
// live. A dead polecat's name resolves to nothing here, and the notifier
// redirects its mail to the coordinator rather than filing it into a box with
// no reader.
//
// A nil registry answers false for every name, which sends everything to the
// coordinator. That is the right degraded direction: a coordinator reading mail
// meant for somebody else is noise, and it is recoverable; a notification
// dropped because the daemon could not check is the defect this closes.
func agentKnown(reg *agent.Registry) func(string) bool {
	return func(name string) bool {
		if reg == nil || name == "" {
			return false
		}
		return reg.GetByWorkItemOrName(name) != nil
	}
}

// recordFilerNotice writes each notification outcome to the event log.
//
// It records the SKIPS as well as the sends, and that is not bookkeeping. This
// notifier exists because a completion path failed silently; a notifier that
// decided to say nothing and a notifier that never ran would otherwise look
// identical from outside, which is the same defect one layer up. The event log
// rather than the daemon log because pogod logs to inherited stderr, so a line
// there is not reliably a line anybody can go back and read.
func recordFilerNotice(c filernotify.Completion, o filernotify.Outcome) {
	details := map[string]any{
		"route":      string(c.Route),
		"creator":    o.Creator,
		"to":         o.To,
		"redirected": o.Redirected,
		"sent":       o.Sent(),
	}
	if c.Branch != "" {
		details["branch"] = c.Branch
	}
	if c.MergedSHA != "" {
		details["merged_sha"] = c.MergedSHA
	}
	if o.Skipped != "" {
		details["skipped"] = o.Skipped
	}
	if o.Err != nil {
		details["error"] = o.Err.Error()
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_completion_notice",
		Agent:      "pogod",
		WorkItemID: c.ItemID,
		Details:    details,
	})
}
