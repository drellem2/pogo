package crewreset

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/alerts"
)

// drellem2/pogo#262 slice 4: the pointer is crew_reset.pointer, an alert of
// its own. Muted, the pointer is withheld and the notice mail still goes;
// routed, the pointer is typed at the route's agent.
func TestPointerIsMutableAndRoutable(t *testing.T) {
	t.Cleanup(func() { alerts.SetMutes(nil); alerts.SetRoutes(nil) })
	now := t0
	pop := []Present{{Name: "pm-pogo", StartedAt: now.Add(-5 * time.Hour)}}
	var nudged []string
	nudge := func(name, text string) error {
		nudged = append(nudged, name+": "+text)
		return nil
	}

	alerts.SetMutes(map[alerts.Name]string{alerts.CrewResetPointer: `[alerts."crew_reset.pointer"] mute = true`})
	f := &fixture{pop: pop}
	got := f.watcher(Options{Nudge: nudge}).Tick(now)
	if len(nudged) != 0 {
		t.Fatalf("muted pointer typed: %v", nudged)
	}
	if len(got) != 1 || got[0].NudgeErr != nil || f.mailsTo("pm-pogo") != 1 {
		t.Fatalf("muted pointer: notices %+v, mails to pm-pogo %d — want the notice mailed and no error", got, f.mailsTo("pm-pogo"))
	}

	alerts.SetMutes(nil)
	alerts.SetRoutes(map[alerts.Name]alerts.Route{alerts.CrewResetPointer: {To: "ops", Source: "test"}})
	f2 := &fixture{pop: pop}
	f2.watcher(Options{Nudge: nudge}).Tick(now)
	if len(nudged) != 1 || !strings.HasPrefix(nudged[0], "ops: ") {
		t.Fatalf("routed pointer: nudges %v, want one at ops", nudged)
	}
	if f2.mailsTo("pm-pogo") != 1 {
		t.Errorf("routing the pointer moved the notice: mails to pm-pogo = %d", f2.mailsTo("pm-pogo"))
	}
}
