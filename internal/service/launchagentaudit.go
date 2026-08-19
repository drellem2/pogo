package service

// The activation audit (mg-fc99).
//
// WHAT IT IS FOR. mg-8f7e shipped TWO artifacts on TWO separate install paths —
// the runner script `pogo-deploy.sh` and the `com.pogo.deploy` plist — and only
// the script was installed. `deployHours` said {3,4,5} and the runner documented
// 04:00 and 05:00 as retries; the plist on the box had `Hour = 3` alone, with an
// mtime predating both. So the retry half of the fix was INERT for five days
// while the ticket read as closed, and nothing on the machine disagreed.
//
// WHY NOTHING NOTICED, and the constraint that shape puts on this file: from
// inside the runner, a retry fire that never happens is INDISTINGUISHABLE from a
// night that needed no retry. There is no log line for a fire that did not
// occur. An absence cannot be detected by reading the output of the thing that
// was supposed to be triggered — the only witness is the installed plist itself,
// compared against what the shipped code would render. That comparison is the
// whole of this file, and it is why the audit reads ~/Library/LaunchAgents
// directly rather than any log, stamp, or event.
//
// WHY IT IS A REGISTRY AND NOT A CHECK FOR `Hour = 3`. The generalisable defect
// is not the deploy plist: it is that a ticket may ship artifacts with SEPARATE
// activation paths, where "merged" witnesses one of them and nothing witnesses
// the other. A literal assertion about hour 3 would itself rot the moment
// somebody re-rendered the plist, and would say nothing about com.pogo.recovery
// or com.pogo.daemon or the fourth job nobody has written yet. So the audit
// ranges over every launchd job this package installs, and its question is the
// general one: does the copy on disk match what THIS build would write?
//
// WHY BYTE EQUALITY IS THE DRIFT PREDICATE. Because it is the installer's own.
// InstallDeploy, InstallRecovery and installLaunchd each read the existing file
// and rewrite it when `string(existing) != rendered`. Reusing that predicate
// means "stale" here means exactly "re-running the installer would change this
// file" — no second, weaker notion of up-to-date that could disagree with the
// thing it is auditing.
//
// WHAT THAT MAKES THE VERDICT RELATIVE TO. The renderers bind host state —
// POGO_HOME, the resolved pogod path, launchdPath() — so the question this audit
// answers is precisely "would re-running the installer FROM THIS ENVIRONMENT
// change the file", not "is the file objectively correct". That is deliberate:
// it is the same environment-sensitivity the installer has, so the two can never
// disagree. It also means a `pogo doctor --check` run under an unusual POGO_HOME
// reports drift the installer would genuinely write, which is a true statement
// about that environment and a confusing one to read out of context. The remedy
// string names the command, not a hand edit, for exactly this reason.
//
// WHY THE SCHEDULE IS DECODED ON TOP OF THAT. Byte drift alone does not
// distinguish a plist whose log path moved from one whose fires are missing.
// The second kind leaves a job that is installed, loaded, listed by launchctl,
// and silently does a fraction of what the code believes it does — which is the
// failure that produced this ticket. So the audit decodes StartCalendarInterval
// on both sides and reports schedule drift as its own fact.

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Statuses reported per managed job. Four, not two, because the two ways this
// audit can fail to have an opinion must never render as the way it has one and
// it is good: a detector that stopped running looks exactly like a detector with
// nothing to report.
const (
	LaunchAgentOK      = "ok"      // installed, and byte-identical to what this build renders
	LaunchAgentStale   = "stale"   // installed, and re-running the installer would change it
	LaunchAgentAbsent  = "absent"  // no plist on disk — nothing to compare, NOT a clean bill
	LaunchAgentUnknown = "unknown" // could not render or could not read — NOT CHECKED
)

// CalendarFire is one StartCalendarInterval entry.
//
// Absent fields are -1 rather than 0, because launchd treats an omitted key as a
// wildcard and 0 is a legitimate value for every one of them: a fire with no
// Hour key runs every hour, and a fire with `Hour = 0` runs at midnight. Folding
// those together would make a plist that fires 24 times a day compare equal to
// one that fires at midnight.
type CalendarFire struct {
	Minute  int
	Hour    int
	Day     int
	Weekday int
	Month   int
}

func (f CalendarFire) String() string {
	var hm string
	switch {
	case f.Hour >= 0 && f.Minute >= 0:
		hm = fmt.Sprintf("%02d:%02d", f.Hour, f.Minute)
	case f.Hour >= 0:
		hm = fmt.Sprintf("%02d:** (every minute)", f.Hour)
	case f.Minute >= 0:
		hm = fmt.Sprintf("**:%02d (every hour)", f.Minute)
	default:
		hm = "every minute"
	}
	var extra []string
	if f.Weekday >= 0 {
		extra = append(extra, fmt.Sprintf("weekday=%d", f.Weekday))
	}
	if f.Day >= 0 {
		extra = append(extra, fmt.Sprintf("day=%d", f.Day))
	}
	if f.Month >= 0 {
		extra = append(extra, fmt.Sprintf("month=%d", f.Month))
	}
	if len(extra) > 0 {
		return hm + " [" + strings.Join(extra, ", ") + "]"
	}
	return hm
}

// LaunchSchedule is everything in a plist that decides WHEN the job runs.
//
// StartInterval is carried alongside the calendar fires and not folded into
// them: swapping a wall-clock schedule for an interval is a silent way to turn a
// nightly into a job that fires N seconds after every load, and the two forms
// have no common representation that would show the swap.
type LaunchSchedule struct {
	Calendar  []CalendarFire
	Interval  int // StartInterval, seconds; 0 when the key is absent
	RunAtLoad bool
	Decoded   bool // false when the plist could not be parsed at all
}

// String renders a schedule the way an operator reading a checklist wants it.
func (s LaunchSchedule) String() string {
	if !s.Decoded {
		return "unreadable"
	}
	parts := make([]string, 0, len(s.Calendar)+1)
	for _, f := range s.Calendar {
		parts = append(parts, f.String())
	}
	if s.Interval > 0 {
		parts = append(parts, fmt.Sprintf("every %ds after load", s.Interval))
	}
	if len(parts) == 0 {
		return "no schedule"
	}
	return strings.Join(parts, ", ")
}

// Equal compares two schedules for launchd-equivalence. Fire ORDER is not part
// of it: launchd holds StartCalendarInterval as a set and fires each entry on
// its own, so two plists that list the same fires in different orders schedule
// identically and must not be reported as drift.
func (s LaunchSchedule) Equal(other LaunchSchedule) bool {
	if s.Decoded != other.Decoded || s.Interval != other.Interval || s.RunAtLoad != other.RunAtLoad {
		return false
	}
	if len(s.Calendar) != len(other.Calendar) {
		return false
	}
	a, b := sortedFires(s.Calendar), sortedFires(other.Calendar)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedFires(in []CalendarFire) []CalendarFire {
	out := append([]CalendarFire(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Month != b.Month:
			return a.Month < b.Month
		case a.Day != b.Day:
			return a.Day < b.Day
		case a.Weekday != b.Weekday:
			return a.Weekday < b.Weekday
		case a.Hour != b.Hour:
			return a.Hour < b.Hour
		default:
			return a.Minute < b.Minute
		}
	})
	return out
}

// LaunchAgentAudit is one managed job's verdict.
type LaunchAgentAudit struct {
	Label         string
	Path          string
	Status        string
	ScheduleDrift bool // the installed plist fires at different times than this build expects
	Expected      LaunchSchedule
	Installed     LaunchSchedule
	Remedy        string // the command that re-renders and reloads this job
	Detail        string
}

// managedLaunchAgent binds a label to the three things the audit needs: where
// the installed copy lives, what this build would write there, and what an
// operator runs to reconcile the two.
//
// Adding another launchd job to this package means adding a row here. That is
// the point: the next two-artifact ticket gets audited because its job is in the
// registry, not because somebody remembered to write a check for it. It has held
// once already — com.pogo.reclaim (mg-b7c3) ships a plist AND a runner script on
// two install paths, exactly mg-8f7e's shape, and it is audited below because
// this comment was read rather than because anyone rederived the argument.
//
// A JOB OUTSIDE THIS PACKAGE IS DELIBERATELY NOT IN THIS REGISTRY (mg-a03d).
// com.pogo.revisionprobe arms scripts/revision-probe.sh, and it is rendered by
// scripts/install-revision-probe.sh from the tracked plist rather than by this
// package, for the reason the probe itself is a shell script: everything in here
// ships in the `pogo` BINARY, which the redeploy installs, and the probe's whole
// job is to be armed on a box where the deploy has been failing for a week. A
// row here would need a Go copy of the plist to render — which is the mirror
// mg-b201 was filed for — and would make the auditor for the deploy witness
// arrive by the deploy.
//
// com.pogo.fleetliveness is out for the same reason one level out (mg-f867). It
// arms scripts/fleet-liveness-probe.sh, the witness that the FLEET is still
// completing turns, and it must be armable on a box whose fleet has been dead
// for five days — the condition it exists to report. A Go-rendered row would
// make its auditor arrive by the `pogo` binary, i.e. by a working deploy and an
// agent turn.
//
// This comment is the registry's promise kept rather than broken: the omission
// is a decision with a reason, not a job somebody forgot. Auditing the probe's
// job on the merge-activated path is filed as its own item, because a deferred
// half announced in a comment and never filed is a half that gets dropped.
// REMEDY IS A STRING TO PRINT, NEVER A COMMAND TO RUN (mg-de0c). It is the one
// field here that looks executable, and nothing in this repo executes it. That
// is a decision with the blast radius measured per job
// (docs/design/launchd-reconcile-decision.md), not an omission waiting to be
// tidied up: `pogo service install` bounces the daemon, `install-recovery`
// bootstraps a RunAtLoad job that kickstarts pogod when the recovery queue is
// non-empty, and `install-deploy` boots out the job an automated caller would
// most likely be running inside — measured to kill that process at the bootout,
// before InstallDeploy's own bootstrap line.
//
// The reason it cannot become executable by a small edit is upstream of all
// three: the drift predicate is byte equality (see the file header), which
// cannot classify WHAT drifted. On the reference box com.pogo.daemon's whole
// diff is POGO_HOME=$HOME -> $HOME/.pogo — a state-root move, reported in the
// same words as a moved log path. Choosing an installer off a boolean means
// choosing it without reading the difference it is about to apply.
type managedLaunchAgent struct {
	Label  string
	Path   func() string
	Render func() (string, error)
	Remedy string
}

func managedLaunchAgents() []managedLaunchAgent {
	return []managedLaunchAgent{
		{
			Label:  launchdLabel,
			Path:   launchdPlistPath,
			Render: func() (string, error) { s, _, err := renderLaunchdPlist(); return s, err },
			Remedy: "pogo service install",
		},
		{
			Label:  recoveryLabel,
			Path:   recoveryPlistPath,
			Render: func() (string, error) { s, _, err := renderRecoveryPlist(); return s, err },
			Remedy: "pogo service install-recovery",
		},
		{
			Label:  deployLabel,
			Path:   deployPlistPath,
			Render: func() (string, error) { s, _, err := renderDeployPlist(); return s, err },
			Remedy: "pogo service install-deploy",
		},
		{
			Label:  reclaimLabel,
			Path:   reclaimPlistPath,
			Render: func() (string, error) { s, _, err := renderReclaimPlist(); return s, err },
			Remedy: "pogo service install-reclaim",
		},
	}
}

// AuditLaunchAgents compares every managed launchd job's installed plist against
// the plist this build would write.
//
// Returns nil on non-darwin: there are no LaunchAgents there and an empty slice
// would be indistinguishable from "checked, all clean". Callers must treat a nil
// result on a supported platform as NOT CHECKED, never as a pass — see
// LaunchAgentsSupported.
func AuditLaunchAgents() []LaunchAgentAudit {
	if !LaunchAgentsSupported() {
		return nil
	}
	agents := managedLaunchAgents()
	out := make([]LaunchAgentAudit, 0, len(agents))
	for _, a := range agents {
		rendered, err := a.Render()
		out = append(out, auditLaunchAgent(a.Label, a.Path(), a.Remedy, rendered, err))
	}
	return out
}

// LaunchAgentsSupported reports whether this platform has the jobs at all.
func LaunchAgentsSupported() bool { return runtime.GOOS == "darwin" }

// auditLaunchAgent is the whole comparison, as a pure function of a path and the
// bytes this build would install there. Everything host-specific is resolved by
// the caller so this can be exercised against a temp dir on any platform — the
// audit's own correctness must not be something only the machine that has the
// bug can demonstrate.
func auditLaunchAgent(label, path, remedy, rendered string, renderErr error) LaunchAgentAudit {
	res := LaunchAgentAudit{Label: label, Path: path, Remedy: remedy}

	if renderErr != nil {
		res.Status = LaunchAgentUnknown
		res.Detail = fmt.Sprintf("NOT CHECKED: this build could not render the expected plist (%v), so the installed copy at %s was not compared against anything", renderErr, path)
		return res
	}
	res.Expected = parseLaunchSchedule([]byte(rendered))

	installed, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		res.Status = LaunchAgentAbsent
		res.Detail = fmt.Sprintf("not installed: no plist at %s. This audit compares an installed plist against the shipped code; it cannot tell a job deliberately left uninstalled from one whose install never ran (`%s` installs it)", path, remedy)
		return res
	case err != nil:
		res.Status = LaunchAgentUnknown
		res.Detail = fmt.Sprintf("NOT CHECKED: %s could not be read (%v)", path, err)
		return res
	}
	res.Installed = parseLaunchSchedule(installed)

	if bytes.Equal(installed, []byte(rendered)) {
		res.Status = LaunchAgentOK
		res.Detail = fmt.Sprintf("installed plist matches this build (%s)", res.Installed)
		return res
	}

	res.Status = LaunchAgentStale
	res.ScheduleDrift = !res.Expected.Equal(res.Installed)
	switch {
	case res.ScheduleDrift && !res.Installed.Decoded:
		res.Detail = fmt.Sprintf("installed plist at %s differs from this build AND could not be decoded, so its schedule is unknown; this build expects %s — run `%s`", path, res.Expected, remedy)
	case res.ScheduleDrift:
		res.Detail = fmt.Sprintf("installed plist at %s FIRES AT DIFFERENT TIMES than this build expects: installed %s, expected %s. Every expected fire the installed copy lacks is INERT — it produces no log line and no failure, so nothing downstream can observe its absence. Run `%s`", path, res.Installed, res.Expected, remedy)
	default:
		res.Detail = fmt.Sprintf("installed plist at %s differs from this build in keys other than its schedule (%s), so re-running the installer would rewrite it — run `%s`", path, res.Installed.scheduleNote(), remedy)
	}
	return res
}

// scheduleNote phrases the schedule for a job whose drift is NOT in its
// schedule. "schedule is unchanged: no schedule" is what the obvious wording
// produces for com.pogo.daemon and com.pogo.recovery, which are event-driven and
// have never had a wall-clock schedule to be unchanged.
func (s LaunchSchedule) scheduleNote() string {
	if s.Decoded && len(s.Calendar) == 0 && s.Interval == 0 {
		return "neither copy declares a wall-clock schedule"
	}
	return "schedule unchanged: " + s.String()
}

// parseLaunchSchedule pulls the scheduling keys out of a plist.
//
// A plist that will not parse yields Decoded=false rather than an error: the
// caller has already established byte drift or the lack of it, and an
// undecodable installed plist is a fact to report, not a reason to abandon the
// comparison that matters.
func parseLaunchSchedule(data []byte) LaunchSchedule {
	doc, err := decodePlistDict(data)
	if err != nil {
		return LaunchSchedule{}
	}
	s := LaunchSchedule{Decoded: true}
	if n, ok := doc["StartInterval"].(int); ok {
		s.Interval = n
	}
	if b, ok := doc["RunAtLoad"].(bool); ok {
		s.RunAtLoad = b
	}
	switch v := doc["StartCalendarInterval"].(type) {
	case map[string]any:
		s.Calendar = []CalendarFire{fireFromDict(v)}
	case []any:
		for _, e := range v {
			if d, ok := e.(map[string]any); ok {
				s.Calendar = append(s.Calendar, fireFromDict(d))
			}
		}
	}
	return s
}

func fireFromDict(d map[string]any) CalendarFire {
	f := CalendarFire{Minute: -1, Hour: -1, Day: -1, Weekday: -1, Month: -1}
	get := func(key string) int {
		if n, ok := d[key].(int); ok {
			return n
		}
		return -1
	}
	f.Minute, f.Hour, f.Day, f.Weekday, f.Month = get("Minute"), get("Hour"), get("Day"), get("Weekday"), get("Month")
	return f
}

// DecodePlistDict decodes an XML plist into a generic Go value tree.
//
// Exported for internal/sourcewatch, which reads plists this package does NOT
// manage — com.pogo.notify and com.pogo.deadman are installed by pogo-reminders,
// not by any installer here — and so cannot go through AuditLaunchAgents. It is
// the same decoder rather than a second one on purpose: a plist reader that
// disagreed with this one about what a key holds would make two detectors report
// different facts about the same file.
func DecodePlistDict(data []byte) (map[string]any, error) { return decodePlistDict(data) }

// decodePlistDict decodes an XML plist into a generic Go value tree.
//
// Written here rather than shelled out to PlistBuddy or plutil on purpose. Those
// exist only on the machine that has the problem, which would make every test of
// this audit unrunnable anywhere else — and a detector whose tests only run on
// the host it is meant to protect is one nobody can prove works before shipping
// it. Only the four value kinds a LaunchAgent uses are given types; everything
// else decodes to its text, which is all this audit reads.
func decodePlistDict(data []byte) (map[string]any, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, fmt.Errorf("no <plist> element")
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "plist" {
			continue
		}
		start, err := nextPlistValue(dec)
		if err != nil {
			return nil, err
		}
		v, err := decodePlistValue(dec, start)
		if err != nil {
			return nil, err
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("<plist> root is %T, want a <dict>", v)
		}
		return m, nil
	}
}

func nextPlistValue(dec *xml.Decoder) (xml.StartElement, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, nil
		case xml.EndElement:
			return xml.StartElement{}, fmt.Errorf("expected a value, found </%s>", t.Name.Local)
		}
	}
}

func decodePlistValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local != "key" {
					return nil, fmt.Errorf("expected <key> inside <dict>, found <%s>", t.Name.Local)
				}
				var key string
				if err := dec.DecodeElement(&key, &t); err != nil {
					return nil, err
				}
				vs, err := nextPlistValue(dec)
				if err != nil {
					return nil, err
				}
				v, err := decodePlistValue(dec, vs)
				if err != nil {
					return nil, err
				}
				m[strings.TrimSpace(key)] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		arr := []any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := decodePlistValue(dec, t)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			case xml.EndElement:
				return arr, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return start.Name.Local == "true", nil
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("<integer>%s</integer>: %w", s, err)
		}
		return n, nil
	default:
		var s string
		if err := dec.DecodeElement(&s, &start); err != nil {
			return nil, err
		}
		return s, nil
	}
}
