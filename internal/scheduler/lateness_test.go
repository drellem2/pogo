package scheduler

import (
	"strings"
	"testing"
	"time"
)

// The mg-d4a7 lateness line. Its subject is the one fact a window-bound
// procedure needs and could not get from the footer alone: not the due time —
// that has been carried since mg-bcfa — but the instruction to compare it
// against the CURRENT clock rather than against `fired`.
//
// The measurement behind it: deploy-verify-architect's 2026-08-19 fire was due
// 03:33:00 and fired 03:33:10, and was consumed at 07:52:35. Ten seconds by
// every lateness measure in this repo; 4h19m by the only one that mattered.

// TestLatenessLine_RidesEveryFireShape guards the unconditional part. A
// per-shape or per-schedule opt-in is exactly the design that leaves the class
// open for whichever schedule nobody remembered to mark.
func TestLatenessLine_RidesEveryFireShape(t *testing.T) {
	due := time.Date(2026, 8, 19, 3, 33, 0, 0, time.UTC)
	at := due.Add(10 * time.Second)

	shapes := map[string]Entry{
		"message": {ID: "deploy-verify-architect", Agent: "architect",
			Cron: "33 3 * * *", Message: "DEPLOY VERIFY", NextFire: due},
		"one-shot":  {ID: "wakeup", Agent: "architect", OneShot: true, NextFire: due},
		"bare-cron": {ID: "poll", Agent: "architect", Cron: "33 3 * * *", NextFire: due},
	}

	for name, e := range shapes {
		body := buildBody(e, at)
		if !strings.Contains(body, "How late am I") {
			t.Errorf("%s fire carries no lateness line:\n%s", name, body)
		}
		if !strings.Contains(body, "due=2026-08-19T03:33:00Z") {
			t.Errorf("%s lateness line does not restate the due time:\n%s", name, body)
		}
	}
}

// TestLatenessLine_NamesTheWrongReference is the whole point of the line, so it
// is pinned rather than left to prose. `fired=` sits beside `due=` and reads
// like "when you got this"; on 2026-08-19 the two were 10 seconds apart and the
// run was 4h19m late. A line that said only "compare against now" would leave
// the misreading it exists to correct unnamed.
func TestLatenessLine_NamesTheWrongReference(t *testing.T) {
	due := time.Date(2026, 8, 19, 3, 33, 0, 0, time.UTC)
	e := Entry{ID: "deploy-verify-architect", Agent: "architect",
		Cron: "33 3 * * *", Message: "DEPLOY VERIFY", NextFire: due}

	body := buildBody(e, due.Add(10*time.Second))
	for _, want := range []string{
		"CURRENT clock",      // the right reference
		"NOT against fired=", // the wrong one, named
		"4h19m",              // the measurement that makes the warning credible
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lateness line is missing %q:\n%s", want, body)
		}
	}
}

// TestLatenessLine_DoesNotRefuse pins the design point mg-d4a7 turns on. Late is
// GRADED: a carried fire invalidates a specific, small part of a run, not the
// run. The mechanism informs; the procedure — which alone knows which of its
// reads are time-sensitive — decides. A body that told the recipient to stop, or
// that a late fire is invalid, would move that judgement to the one place that
// cannot make it.
func TestLatenessLine_DoesNotRefuse(t *testing.T) {
	due := time.Date(2026, 8, 19, 3, 33, 0, 0, time.UTC)
	e := Entry{ID: "deploy-verify-architect", Agent: "architect",
		Cron: "33 3 * * *", Message: "DEPLOY VERIFY", NextFire: due}

	body := buildBody(e, due.Add(4*time.Hour))
	if !strings.Contains(body, "graded") {
		t.Errorf("the line must say lateness is graded, not binary:\n%s", body)
	}
	if !strings.Contains(body, "answer the rest normally") {
		t.Errorf("the line must keep the still-valid part of the work in scope:\n%s", body)
	}
	for _, banned := range []string{"do not run", "abort", "refuse this fire", "skip this fire"} {
		if strings.Contains(strings.ToLower(body), banned) {
			t.Errorf("the mechanism must not refuse the work (%q):\n%s", banned, body)
		}
	}
}

// TestLatenessLine_KeepsTheAckCommandLast protects the line that agents
// demonstrably act on. The ack command is meant to be copied verbatim off the
// end of the fire; anything appended after it would put a paragraph between the
// instruction and the prompt.
func TestLatenessLine_KeepsTheAckCommandLast(t *testing.T) {
	due := time.Date(2026, 8, 19, 3, 33, 0, 0, time.UTC)
	e := Entry{ID: "deploy-verify-architect", Agent: "architect", Cron: "33 3 * * *",
		Message: "DEPLOY VERIFY", NextFire: due, PendingToken: "133d14bd"}

	body := buildBody(e, due.Add(10*time.Second))
	lines := strings.Split(body, "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "When this fire's work is done, run: pogo schedule ack ") {
		t.Errorf("ack command is no longer the final line, got %q\nfull body:\n%s", last, body)
	}
	if !strings.Contains(body, "ack=133d14bd") {
		t.Errorf("token dropped out of the footer:\n%s", body)
	}
}

// TestLatenessLine_UsesTheOriginalDueNotTheFireTime is the guard against the
// change that would quietly undo all of this: rendering the line off the fire
// time instead of the entry's NextFire. Tick fires an entry off NextFire and
// advances it only afterwards, so at Deliver time NextFire IS the original due
// — a carried fire delivered hours late still reports the time it came due, and
// a line built from `fireTime` would report "on time" for every fire ever sent.
func TestLatenessLine_UsesTheOriginalDueNotTheFireTime(t *testing.T) {
	due := time.Date(2026, 8, 19, 3, 33, 0, 0, time.UTC)
	late := due.Add(4*time.Hour + 19*time.Minute)
	e := Entry{ID: "deploy-verify-architect", Agent: "architect",
		Cron: "33 3 * * *", Message: "DEPLOY VERIFY", NextFire: due}

	body := buildBody(e, late)
	if !strings.Contains(body, "compare due=2026-08-19T03:33:00Z") {
		t.Errorf("lateness line must carry the ORIGINAL due, not the fire time:\n%s", body)
	}
	if strings.Contains(body, "compare due=2026-08-19T07:52:00Z") {
		t.Errorf("lateness line was built from the fire time; a late fire now reads on time:\n%s", body)
	}
}
