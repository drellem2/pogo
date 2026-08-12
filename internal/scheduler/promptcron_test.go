package scheduler

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// The rule this file enforces: a schedule that must not be dropped should not
// share a minute with one that can be.
//
// When two of an agent's fires land in the same wake cycle, pogod suppresses
// the second (`wake_silence_once`) and it falls back to mail. Which one loses
// is not chosen — it is whichever arrived second. So the cost of a collision
// is asymmetric: a schedule that fires many times an hour absorbs being
// dropped (the next fire is minutes behind), while a once-a-day schedule
// loses a whole cycle, and for a PM's evening sweep that cycle is the daily
// digest — its one scheduled output.
//
// The shipped prompts are where this gets decided for every install, and
// pm-template.md prescribed the collision outright: sweeps at `0 9` / `0 17`
// against a `*/10` mail-check collided twice a day on every PM on every box
// (mg-956b). Offsetting the sweep minute removes the collision rather than
// making the fallback more reliable; this test is what stops the minute being
// "tidied" back to zero.

// promptCron is one `--cron "<expr>" --id <id>` registration found in a prompt.
type promptCron struct {
	file string
	id   string
	expr string
	cron CronSchedule
}

var (
	promptCronRe = regexp.MustCompile(`--cron\s+"([^"]+)"`)
	promptIDRe   = regexp.MustCompile(`--id\s+(\S+)`)
)

// collectPromptCrons parses every schedule registration the shipped prompts
// instruct an agent to make, keyed by the file that prescribes it. Same file
// ~= same agent, and wake suppression is per-agent, so a file is the unit
// within which two schedules can collide.
func collectPromptCrons(t *testing.T) map[string][]promptCron {
	t.Helper()
	out := map[string][]promptCron{}
	err := fs.WalkDir(agent.DefaultPromptsFS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, readErr := fs.ReadFile(agent.DefaultPromptsFS(), path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(data), "\n") {
			m := promptCronRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			expr := m[1]
			// The id is what a reader (and `pogo schedule list`) sees; a
			// registration without one is its own bug, so name it rather
			// than skipping the line.
			id := "<no --id on this line>"
			if idm := promptIDRe.FindStringSubmatch(line); idm != nil {
				id = idm[1]
			}
			c, parseErr := ParseCron(expr)
			if parseErr != nil {
				t.Errorf("%s: prompt prescribes an unparseable cron %q (id %s): %v", path, expr, id, parseErr)
				continue
			}
			out[path] = append(out[path], promptCron{file: path, id: id, expr: expr, cron: c})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk prompts: %v", err)
	}
	return out
}

// fireTimes returns every instant the schedule fires in (start, end].
func fireTimes(c CronSchedule, start, end time.Time) []time.Time {
	var out []time.Time
	for t := c.Next(start); !t.IsZero() && !t.After(end); t = c.Next(t) {
		out = append(out, t)
	}
	return out
}

// TestPromptSchedulesDoNotCollide asserts that no prompt instructs an agent to
// register a schedule that cannot absorb a dropped fire (one that fires at
// most once a day) at an instant when one of that same agent's other
// schedules also fires.
func TestPromptSchedulesDoNotCollide(t *testing.T) {
	byFile := collectPromptCrons(t)

	// A window long enough to cover day-of-week restrictions. Fixed start so
	// the test does not depend on when it runs.
	start := time.Date(2026, 5, 3, 0, 0, 0, 0, time.Local) // a Sunday
	end := start.AddDate(0, 0, 7)

	// Vacuity guard: this test is worthless if the extraction quietly finds
	// nothing. The prompts registered 11 schedules across 9 files when this
	// was written; assert a floor rather than an exact count so adding a
	// prompt does not fail the build for the wrong reason.
	total := 0
	for _, cs := range byFile {
		total += len(cs)
	}
	if total < 10 {
		t.Fatalf("found only %d cron registrations across %d prompt files — the extraction is broken, not the prompts", total, len(byFile))
	}
	// pm-template is the file this rule was written for; if its three
	// registrations stop being found, everything below passes vacuously.
	if got := len(byFile["pm/pm-template.md"]); got != 3 {
		t.Errorf("pm/pm-template.md: found %d cron registrations, want 3 (mail-check + two sweeps)", got)
	}

	for file, crons := range byFile {
		fires := make([]map[time.Time]bool, len(crons))
		perDay := make([]float64, len(crons))
		for i, c := range crons {
			ft := fireTimes(c.cron, start, end)
			set := make(map[time.Time]bool, len(ft))
			for _, f := range ft {
				set[f] = true
			}
			fires[i] = set
			perDay[i] = float64(len(ft)) / 7
		}
		for i := range crons {
			for j := i + 1; j < len(crons); j++ {
				// Only a schedule that fires at most once a day is unable to
				// absorb a suppressed fire. Two frequent schedules colliding
				// is noise; this one is data loss.
				if perDay[i] > 1 && perDay[j] > 1 {
					continue
				}
				var shared []time.Time
				for f := range fires[i] {
					if fires[j][f] {
						shared = append(shared, f)
					}
				}
				if len(shared) == 0 {
					continue
				}
				t.Errorf("%s: %s (%s, %.1f fires/day) and %s (%s, %.1f fires/day) fire in the same wake cycle %d times a week "+
					"(first: %s) — pogod suppresses whichever arrives second, and the once-a-day one cannot absorb that. "+
					"Offset the daily schedule's minute off the recurring one's boundary (mg-956b).",
					file, crons[i].id, crons[i].expr, perDay[i], crons[j].id, crons[j].expr, perDay[j],
					len(shared), shared[0].Format(time.RFC3339))
			}
		}
	}
}

// TestPMSweepMinutesAreOffBoundary pins the specific fix from mg-956b: the PM
// sweeps must not sit on a multiple of 10, which is where the `*/10`
// mail-check fires. TestPromptSchedulesDoNotCollide would also catch a
// straight revert, but this one names the number so the failure message says
// what to change.
func TestPMSweepMinutesAreOffBoundary(t *testing.T) {
	crons := collectPromptCrons(t)["pm/pm-template.md"]
	if len(crons) == 0 {
		t.Fatal("pm/pm-template.md: no cron registrations found")
	}
	for _, c := range crons {
		if !strings.HasPrefix(c.id, "sweep-") {
			continue
		}
		minute := strings.Fields(c.expr)[0]
		if strings.Contains(minute, "*") || strings.Contains(minute, ",") || strings.Contains(minute, "-") {
			t.Errorf("pm-template.md: sweep %s has minute field %q — a sweep fires once a day at one minute", c.id, minute)
			continue
		}
		m, err := strconv.Atoi(minute)
		if err != nil {
			t.Errorf("pm-template.md: sweep %s minute %q: %v", c.id, minute, err)
			continue
		}
		if m%10 == 0 {
			t.Errorf("pm-template.md: sweep %s fires at minute %d, a `*/10` mail-check boundary — the two collide in one wake cycle "+
				"every day and the sweep is the one that cannot absorb the drop (mg-956b)", c.id, m)
		}
	}
}
