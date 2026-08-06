package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/health"
)

// TestHealthRefineryLineShowsTheSlotHolder is the population evidence from
// mg-48d8 turned into an assertion.
//
// On 2026-08-05 six polecats independently mailed the mayor within one hour
// reporting the refinery as stalled — "11 queued / 0 processing", "head of
// queue never picked up", "no merge since 18:45". An MR held the slot the whole
// time. Every one of them measured correctly and inferred wrongly, because
// every count they could see was of the QUEUE and the queue excludes the
// request being merged.
//
// Six competent readers reaching the same false conclusion from the same
// instrument in one evening is not six mistakes. So the test is that the two
// arrangements cannot render the same.
func TestHealthRefineryLineShowsTheSlotHolder(t *testing.T) {
	now := time.Now()

	busy := formatHealthRefinery("running", health.Refinery{
		Enabled: true, Running: true,
		QueueLength: 11, HistoryLength: 40,
		Processing:      "mr-d9png12tjv1h244d8420",
		ProcessingSince: now.Add(-29 * time.Minute),
	}, now)

	// Byte-identical except that nothing holds the slot. This is the genuinely
	// alarming arrangement, and before the in-flight field existed it produced
	// the same line as the healthy one above.
	stalled := formatHealthRefinery("running", health.Refinery{
		Enabled: true, Running: true,
		QueueLength: 11, HistoryLength: 40,
	}, now)

	t.Logf("BUSY:\n%s\nSTALLED:\n%s", busy, stalled)

	if busy == stalled {
		t.Fatal("a refinery merging steadily and one holding 11 requests with nothing in flight " +
			"rendered identically — that is the line six agents read as a fleet-wide stall")
	}
	if !strings.Contains(busy, "mr-d9png12tjv1h244d8420") {
		t.Errorf("the busy line must name the request holding the slot, got: %s", busy)
	}
	if !strings.Contains(busy, "29m0s") {
		t.Errorf("the busy line must say how long the slot has been held — a slot-holder with no "+
			"age cannot be told from one that is itself wedged, got: %s", busy)
	}
	if strings.Contains(busy, "NOTHING IN FLIGHT") {
		t.Errorf("a busy refinery must not carry the stalled banner, got: %s", busy)
	}
	if !strings.Contains(stalled, "NOTHING IN FLIGHT") {
		t.Errorf("pending requests with nothing being merged must be said outright, not left to be "+
			"inferred from a count, got: %s", stalled)
	}
	// The pending count keeps its own name. It was labelled `queue=11`, which
	// reads as "the whole pipeline" and is why the slot-holder's absence from
	// it went unnoticed.
	if !strings.Contains(busy, "pending=11") {
		t.Errorf("the pending count must be labelled as pending, got: %s", busy)
	}
}

// TestHealthRefineryLineStatesAnEmptyPipeline keeps the in-flight field from
// meaning three things. An idle refinery has nothing in flight and nothing
// waiting, and must not borrow the alarm belonging to the stalled case.
func TestHealthRefineryLineStatesAnEmptyPipeline(t *testing.T) {
	now := time.Now()
	idle := formatHealthRefinery("running", health.Refinery{Enabled: true, Running: true}, now)
	if !strings.Contains(idle, "in flight: none") {
		t.Errorf("an idle refinery must still state that nothing is in flight — an omitted field "+
			"would read as \"not measured\", got: %s", idle)
	}
	if strings.Contains(idle, "NOTHING IN FLIGHT") {
		t.Errorf("an empty pipeline is not a stall and must not be flagged as one, got: %s", idle)
	}
}
