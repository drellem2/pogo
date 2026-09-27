package main

import (
	"fmt"
	"time"
)

// eventsWindowTruncatedMessage is what `pogo events list` says when its --since
// window reaches back past the oldest record rotation has kept. The matches
// printed above it are real, but events older than the floor may have existed
// and been discarded, so the count is a lower bound — and a zero is not a zero
// (mg-50b9).
func eventsWindowTruncatedMessage(since, floor time.Time, found int) string {
	at := "an unreadable first record"
	if !floor.IsZero() {
		at = floor.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("pogo events list: window starts before retained history at %s "+
		"(window start %s); older events were discarded by log rotation, so the %d "+
		"event(s) above are a LOWER BOUND, not a count",
		at, since.UTC().Format(time.RFC3339), found)
}
