// Package zonelabel is the POSITIVE CONTROL for the unlabeled-time-layout
// check in cmd/pogo (mg-0235). It is not compiled: files under testdata are
// invisible to the go tool, and this one is only ever read by go/parser.
//
// Every Format call in this file is deliberately defective, in one of the three
// ways the check knows about. If the check stops flagging any of them it has
// stopped working, and a green run over cmd/pogo would mean nothing.
package zonelabel

import (
	"fmt"
	"time"
)

type event struct {
	SubmitTime  time.Time
	DoneTime    time.Time
	MergedAt    time.Time
	ScheduledAt time.Time
}

// unlabeled prints digits whose zone depends on how the value was
// deserialized, with nothing on the line saying which. This is the shape that
// made `refinery history` and `refinery history --since` disagree by an hour.
func unlabeled(e event) string {
	return fmt.Sprintf("submitted=%s", e.SubmitTime.Format("2006-01-02 15:04"))
}

// normalizedButStillBare is deterministic across surfaces — .UTC() kills the
// two-surfaces-disagree mode — but the rendered line is still unlabeled, so a
// reader on another clock can still read it as impossible. The check must flag
// it: normalization alone is not enough.
func normalizedButStillBare(e event) string {
	return fmt.Sprintf("done=%s", e.DoneTime.UTC().Format("15:04:05"))
}

// mislabeled asserts UTC in the layout and never converts the value, so on a
// BST host it prints the local clock and labels it Z. This is the exact defect
// the audit-successors comment describes having been fixed once already.
func mislabeled(e event) string {
	return fmt.Sprintf("merged=%s", e.MergedAt.Format("2006-01-02 15:04Z"))
}

// hoistedLayout puts the layout behind a variable, which is how a defect would
// evade a check that only reads literals. The check reports it as unjudgeable
// rather than passing it.
func hoistedLayout(e event) string {
	layout := "2006-01-02 15:04"
	return fmt.Sprintf("scheduled=%s", e.ScheduledAt.Format(layout))
}
