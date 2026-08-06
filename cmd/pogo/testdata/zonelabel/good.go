package zonelabel

import (
	"fmt"
	"time"
)

type record struct {
	MergedAt time.Time
	NextFire time.Time
}

// The two correct in-tree patterns, reproduced so the control also proves the
// check does not flag what it is meant to endorse. A check that fires on
// everything is as useless as one that fires on nothing.
//
//   - auditsuccessors.go — a literal Z, and the .UTC() that makes it true
//   - main.go, `schedule list` — .Local() with a layout carrying a real offset
func correct(r record) string {
	return fmt.Sprintf("merged=%s next=%s",
		r.MergedAt.UTC().Format("2006-01-02 15:04Z"),
		r.NextFire.Local().Format(time.RFC3339))
}

// nonTimeFormat is somebody else's Format method taking a string that is not a
// time layout. The check must leave it alone.
type stanza struct{}

func (stanza) Format(style string) string { return style }

func notATimeLayout() string { return stanza{}.Format("wide") }
