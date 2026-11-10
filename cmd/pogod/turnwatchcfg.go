package main

import (
	"fmt"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/turnlog"
	"github.com/drellem2/pogo/internal/turnwatch"
)

// turnWatchWindows is the pair of thresholds that decide how soon turn-watch
// escalates a crew agent (the coordinator above all) that stopped completing
// turns: a reading goes red at MaxAge since the last completed turn, and is
// announced once it has stayed red for HoldDown. With the defaults that is
// about 3.5h (pogo#296).
type turnWatchWindows struct {
	MaxAge   time.Duration
	HoldDown time.Duration
	// Rejected names each [turn_watch] key that was present but nonsense,
	// in words an operator can act on. The default is in force for each.
	Rejected []string
}

// resolveTurnWatchWindows turns the [turn_watch] section into the effective
// windows. An unset key takes the package default — turnlog.DefaultMaxAge and
// turnwatch.DefaultHoldDown, read from where they are defined rather than
// mirrored — so a config that does not mention the keys behaves exactly as
// pogod did before they existed.
//
// hold_down > max_age is deliberately NOT rejected: the two are sequential, not
// nested (red at max_age, announced hold_down later), so any positive pair is
// coherent.
func resolveTurnWatchWindows(c config.TurnWatchConfig) turnWatchWindows {
	w := turnWatchWindows{MaxAge: turnlog.DefaultMaxAge, HoldDown: turnwatch.DefaultHoldDown}
	if c.MaxAge > 0 {
		w.MaxAge = c.MaxAge
	}
	if c.HoldDown > 0 {
		w.HoldDown = c.HoldDown
	}
	if c.MaxAgeInvalid != "" {
		w.Rejected = append(w.Rejected, rejectedTurnWatchKey("max_age", c.MaxAgeInvalid, w.MaxAge))
	}
	if c.HoldDownInvalid != "" {
		w.Rejected = append(w.Rejected, rejectedTurnWatchKey("hold_down", c.HoldDownInvalid, w.HoldDown))
	}
	return w
}

func rejectedTurnWatchKey(key, raw string, inForce time.Duration) string {
	return fmt.Sprintf("pogod: CONFIG ERROR: [turn_watch] %s = %q rejected — it must be a positive Go duration "+
		"such as \"90m\" or \"1h\". Using the default %s instead; fix the key and restart pogod to apply it",
		key, raw, inForce)
}

// turnWatchOptions builds the watcher's options from the effective windows.
// It is the one place main.go and the tests both construct turn-watch, so the
// windows a test proves are the windows pogod runs with. root is the turnlog
// directory ("" = the real one).
func turnWatchOptions(enabled bool, win turnWatchWindows, root string,
	population func() ([]turnlog.Present, error), mail turnwatch.MailFunc,
	coordinator, humanBox string, startedAt time.Time) turnwatch.Options {
	return turnwatch.Options{
		Enabled: enabled,
		Scan: func(now time.Time) (turnlog.Report, error) {
			return turnlog.Scan(turnlog.Options{
				Now:        now,
				Root:       root,
				MaxAge:     win.MaxAge,
				Population: population,
			})
		},
		Mail:        mail,
		HoldDown:    win.HoldDown,
		Coordinator: coordinator,
		HumanBox:    humanBox,
		StartedAt:   startedAt,
	}
}
