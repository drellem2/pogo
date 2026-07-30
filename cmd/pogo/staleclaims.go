package main

// The claimed-work-item count behind `pogo doctor --check`'s macguffin line
// (mg-b13b).
//
// The count used to be the line count of the RENDERED listing:
//
//	items := strings.TrimSpace(string(mgOut))          // mg list --status=claimed
//	count := len(strings.Split(items, "\n"))
//
// That output is prose, not data. An empty store prints one sentence —
//
//	No claimed work items.
//
// — which is one non-empty line, so every store with nothing to report was
// reported as "1 claimed work item(s) — check for stale claims". The defect was
// invisible from inside the number: against a store with five genuinely claimed
// items the count was five, correct, so it never read as an off-by-one. It was
// a sentence being counted as an item, and only the empty store had one.
//
// A detector that fires on healthy input is worse than no detector, because it
// teaches its readers to skip the line — and this is the line a real stale
// claim surfaces on. An item held by a dead pid is invisible to dispatch and
// shows up nowhere else in the checklist.
//
// So the count comes from `--json` now: NDJSON, one object per item, and no
// bytes at all for an empty store. There is no sentence to miscount, and the
// count is a decode rather than a line split, so any notice or banner mg grows
// later is a parse error here — visible — rather than a silent +1.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// mgListClaimedArgs is the invocation that yields the machine-readable listing.
var mgListClaimedArgs = []string{"list", "--status=claimed", "--json"}

// countClaimedWorkItems counts the work items in the NDJSON stdout of
// `mg list --status=claimed --json`. No bytes means no claimed items.
//
// An item is a JSON object carrying an id. The id is required rather than
// assumed, so that mg's error envelope — {"error":{…}} — can never be counted
// as a claimed work item if it ever reaches this stream.
func countClaimedWorkItems(stdout []byte) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	n := 0
	for {
		var item struct {
			ID string `json:"id"`
		}
		err := dec.Decode(&item)
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, fmt.Errorf("parsing mg list output: %w", err)
		}
		if item.ID != "" {
			n++
		}
	}
}

// claimedWorkItemCount runs mg and returns the number of claimed work items.
//
// The error is mg's own — an uninitialised MG_ROOT is the ordinary cause — and
// the caller decides what to say about it. It is not folded into a zero: "the
// store could not be read" and "the store is clean" are different facts, and
// reporting the first as the second is the same class of defect as the count
// this file exists to fix.
func claimedWorkItemCount() (int, error) {
	// Output(), not CombinedOutput(): stderr merged into the stream would be a
	// parse failure at best and a phantom item at worst.
	out, err := exec.Command("mg", mgListClaimedArgs...).Output()
	if err != nil {
		return 0, fmt.Errorf("mg list --status=claimed: %s", mgErrorDetail(err))
	}
	return countClaimedWorkItems(out)
}

// mgErrorDetail renders a failed mg invocation as one readable line. Under
// --json mg writes its error envelope to stderr, so the bare "exit status 1"
// that exec reports carries none of the reason; the message inside the
// envelope is the whole content.
func mgErrorDetail(err error) string {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err.Error()
	}
	stderr := bytes.TrimSpace(ee.Stderr)
	if len(stderr) == 0 {
		return err.Error()
	}
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(stderr, &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	line, _, _ := strings.Cut(string(stderr), "\n")
	return line
}
