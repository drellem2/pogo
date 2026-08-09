// Package sourcewatch reports a consumer that is reading from a source nothing
// is writing to, while something IS being written somewhere comparable.
//
// # The defect
//
// mg-65d2 re-pointed com.pogo.notify — the job whose name is "Daniel gets
// notified" — from ~/.macguffin/mail/human/new to ~/.macguffin/mail/daniel/new,
// as step 4 of a staged cutover behind a relay that is not activated yet. The
// fleet still writes `human`. So the primary notifier is loaded, healthy,
// polling on schedule, reporting no errors, and watching a directory nothing
// writes to; every notification Daniel receives is carried by the fail-open
// deadman behind it.
//
// The routing is not the defect — that intermediate state is DESIGNED, and
// finishing or reverting the cutover is a decision tracked elsewhere (mg-8158).
// The defect is that no instrument on this machine can see the state at all:
//
//	launchctl list      -> com.pogo.notify loaded and healthy.   TRUE.
//	the job's own log   -> polling on schedule, no errors.       TRUE.
//	Daniel              -> receives nothing from that job.
//
// Everything goes dark and every check stays green. That ran for at least 40
// hours before three agents independently went looking at one quiet log.
//
// # Why this is stated generally, and not as a check on today's two boxes
//
// "Alarm if notify watches daniel/ while agents write human/" would be a control
// that DECAYS the moment the cutover completes — and completing the cutover is
// the expected outcome. The general defect is not about mail:
//
//	A CONSUMER RE-POINTED AWAY FROM THE LIVE DATA KEEPS REPORTING HEALTH.
//
// So the predicate here is the general one — compare a consumer's CONFIGURED
// SOURCE against where data is ACTUALLY ARRIVING — which catches the next
// re-point automatically, whoever performs it and in whichever direction,
// without anyone updating this file.
//
// # What it says about com.pogo.notify today, measured rather than assumed
//
// Run against the live plists on 2026-08-09 at 19:22Z it reports com.pogo.notify
// STARVED at a 30-minute window and LIVE at the six-hour default, because
// ~/.macguffin/mail/daniel/new has in fact received seven messages during the
// day: mayor, pm-pogo and pa address `daniel` directly, alongside the
// verification sends this ticket's own investigation produced. The 40-hour
// silence that produced the ticket is real and predates that traffic.
//
// The honest statement is therefore that this package does NOT convict
// com.pogo.notify at its default window right now, and should not — the box it
// reads is receiving. That is the detector working rather than failing: it
// reports the machine as it is, not as the ticket found it, which is the whole
// difference between an instrument and an assertion. An instrument tuned until
// it agreed with the ticket would be the third thing on this box measuring its
// own premises.
//
// # The test this package had to pass against itself
//
// architect derived it from five separate false-greens on this box in one day:
//
//	What would this instrument report if the thing it NAMES stopped entirely?
//	If the answer is green, it is measuring its own execution.
//
// A "poller health" check would have passed casual review and reproduced the
// exact defect inside the fix for it. Applied here, the thing this package names
// is "data arriving into sources that consumers read". If ALL of it stopped —
// the fleet dies, nothing is written anywhere — then every source has zero
// arrivals, and the naive predicate ("someone has zero while someone else has
// more than zero") finds nothing and reads as clean. That is the failure mode,
// inside the fix, one level down.
//
// Hence StatusUndetermined, and hence it is not a pass. When no comparable
// source has ANY activity in the window, this package reports that it could not
// distinguish a mis-pointed consumer from a quiet machine, and says so in those
// words. The same applies when discovery finds no consumers at all: zero
// consumers examined is NOT CHECKED, never a clean bill.
//
// # Report-only
//
// It reads plists and stats directories. It never edits a plist, re-points a
// consumer, or bounces a job. Re-pointing com.pogo.notify back at `human` was
// proposed three times on 2026-08-09 by three routes and retracted every time —
// it reverts a verified step of a staged cutover, and the most persuasive
// version framed it as restoring redundancy. That decision is not a detector's
// to make.
package sourcewatch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultWindow is how far back "actually arriving" reaches.
//
// Chosen against the two costs it trades off, both of which were measured on
// the live store rather than reasoned about.
//
// TOO SHORT convicts a low-rate consumer between its arrivals. At 30 minutes on
// 2026-08-09, 18 of 1364 mailboxes had received; a consumer on any of the other
// 1346 would have been called starved for the crime of a quiet half hour, and a
// detector that cries wolf is one somebody removes from the checklist.
//
// TOO LONG lets a source that died an hour ago keep showing this morning's
// traffic, so a consumer whose feed has just been cut reads as live for the
// remainder of the window.
//
// Six hours is longer than the gaps this fleet's traffic actually has while it
// is alive, and short enough that a re-point is visible the same working day.
// Note that the window delays only the SECOND kind of finding: a consumer
// pointed at a box that has never had traffic is starved from the first sample,
// because its peers' traffic is what convicts it, not the passage of time.
const DefaultWindow = 6 * time.Hour

// Per-consumer verdicts. Four, not two, because the two ways this audit can
// fail to have an opinion must never render as the way it has one and it is
// good — the same reasoning that gave internal/service four LaunchAgent
// statuses, and the same reasoning that produced this package.
const (
	// StatusLive: the configured source has had activity in the window.
	StatusLive = "live"
	// StatusStarved: the configured source has had NO activity in the window
	// while a comparable source HAS. This is the finding.
	StatusStarved = "starved"
	// StatusMissing: the configured source is not a directory on this machine.
	// A consumer polling a path that does not exist is not starved, it is
	// mis-configured, and the two want different remedies.
	StatusMissing = "missing"
	// StatusUndetermined: nothing arrived ANYWHERE comparable in the window, or
	// there was nothing to compare against. NOT a pass — see the package
	// comment. This is the branch that keeps the fix from measuring its own
	// execution.
	StatusUndetermined = "undetermined"
)

// Consumer is one job that reads from a configured directory.
//
// It is discovered from a plist rather than declared in a registry here. A
// registry would have to be edited by whoever adds the next consumer, and "the
// person who re-points a consumer also remembers to update its watchdog" is the
// assumption this whole package exists because nobody can hold.
type Consumer struct {
	// Label is the launchd label — what an operator types after `launchctl`.
	Label string `json:"label"`
	// Program is ProgramArguments[0]: what the job actually runs. Two jobs
	// running the same program are peers by construction, which is one of the
	// two ways a source is admitted for comparison (see admit).
	Program string `json:"program"`
	// SourceKey is the environment variable that names the source, e.g.
	// "MAIL_DIR". Carried so the report can say WHICH knob is pointed wrong.
	SourceKey string `json:"source_key"`
	// Source is the directory that variable names.
	Source string `json:"source"`
	// PlistPath is where the binding was read from — the file to look at.
	PlistPath string `json:"plist_path"`
}

// Activity is one directory's evidence of recent writing.
type Activity struct {
	Dir string `json:"dir"`
	// Count is entries whose mtime falls inside the window.
	Count int `json:"count"`
	// Last is the most recent evidence of a write: the newer of the directory's
	// own mtime and its newest entry's.
	//
	// The directory's own mtime is included because it is the only signal that
	// survives DRAINAGE. A maildir `new/` that a working consumer empties as
	// fast as it fills holds no recent entries and is not, for that reason,
	// dead — the directory mtime moves on both create and unlink, so it reports
	// traffic THROUGH the box rather than backlog IN it. Without this, a
	// healthy drained source and an abandoned one are the same reading.
	Last time.Time `json:"last"`
	// Exists is whether the path is a directory at all.
	Exists bool `json:"exists"`
	// Err records a directory that could not be read. A source this package
	// could not look at has NOT been found quiet.
	Err error `json:"-"`
}

// Active reports whether this directory shows writing inside the window.
func (a Activity) Active(now time.Time, window time.Duration) bool {
	if !a.Exists || a.Err != nil {
		return false
	}
	return a.Count > 0 || (!a.Last.IsZero() && now.Sub(a.Last) <= window)
}

func (a Activity) String() string {
	switch {
	case a.Err != nil:
		return fmt.Sprintf("%s (unreadable: %v)", a.Dir, a.Err)
	case !a.Exists:
		return a.Dir + " (not a directory)"
	case a.Count > 0:
		return fmt.Sprintf("%s (%d in window, last %s)", a.Dir, a.Count, a.Last.Format(time.RFC3339))
	case a.Last.IsZero():
		return a.Dir + " (no activity ever recorded)"
	default:
		return fmt.Sprintf("%s (last %s)", a.Dir, a.Last.Format(time.RFC3339))
	}
}

// maxNamedPeers caps how many live peers a finding names.
//
// Measured, not guessed. On this machine on 2026-08-09 the sibling family of
// ~/.macguffin/mail/daniel/new is every agent mailbox in the store — 1000-odd
// directories — and a finding that enumerated the live ones rendered a 400KB
// doctor row. Three named boxes, most recent first, is enough to make the
// comparison checkable by hand; LivePeerCount carries the rest of the fact.
const maxNamedPeers = 3

// Verdict is one consumer judged against the sources it is comparable to.
type Verdict struct {
	Consumer Consumer `json:"consumer"`
	Status   string   `json:"status"`
	Own      Activity `json:"own"`
	// PeerCount is the whole comparison population; LivePeerCount is how much
	// of it received data in the window. Both are counts rather than slices
	// because on a real store the population is in the thousands, and a report
	// nobody can read is a report nobody reads.
	PeerCount     int `json:"peer_count"`
	LivePeerCount int `json:"live_peer_count"`
	// LivePeers is the most recently written members of that live set, capped
	// at maxNamedPeers — the evidence, so a finding names what convicted the
	// consumer instead of asserting it.
	LivePeers []Activity `json:"live_peers,omitempty"`
	Detail    string     `json:"detail"`
}

// Report is one sweep.
type Report struct {
	Now      time.Time     `json:"now"`
	Window   time.Duration `json:"window"`
	Verdicts []Verdict     `json:"verdicts"`
	// Scanned is how many plists discovery examined. Zero with a nil Err means
	// there were no plists to read, which is a fact about the machine and still
	// not a clean bill.
	Scanned int `json:"scanned"`
	// Err is set when discovery itself could not run. "I could not look" and
	// "nothing is wrong" are the two readings this package exists to keep apart,
	// so they get separate fields rather than a shared empty slice.
	Err error `json:"-"`
}

// Findings is the starved set — the consumers reading from a source nothing
// writes to while something comparable is being written.
func (r Report) Findings() []Verdict {
	var out []Verdict
	for _, v := range r.Verdicts {
		if v.Status == StatusStarved {
			out = append(out, v)
		}
	}
	return out
}

// Count returns how many verdicts carry the given status.
func (r Report) Count(status string) int {
	n := 0
	for _, v := range r.Verdicts {
		if v.Status == status {
			n++
		}
	}
	return n
}

// SampleFunc yields one directory's activity. Production binds sampleDir;
// tests substitute a fixture so a verdict's phrasing can be exercised without
// building a filesystem that has the fault.
type SampleFunc func(dir string) Activity

// PeerFunc yields the directories a consumer's source is comparable to. It is a
// seam for the same reason: the peer rule is filesystem-shaped (see peersFor),
// and the wording of a finding must be testable without one.
type PeerFunc func(c Consumer) []string

// Evaluate judges every discovered consumer.
//
// It is a pure function of its inputs — no filesystem, no clock — so every
// branch including the ones this machine cannot currently produce is reachable
// in a test. The audit's own correctness must not be something only the host
// that has the bug can demonstrate.
func Evaluate(consumers []Consumer, sample SampleFunc, peers PeerFunc, now time.Time, window time.Duration) Report {
	rep := Report{Now: now, Window: window, Scanned: len(consumers)}
	if window <= 0 {
		window = DefaultWindow
		rep.Window = window
	}

	// Sample each distinct directory once. Two consumers pointed at the same
	// box is a legitimate configuration (it is what completing the cutover
	// produces), and re-reading the directory for each would let them disagree.
	cache := map[string]Activity{}
	get := func(dir string) Activity {
		if a, ok := cache[dir]; ok {
			return a
		}
		a := sample(dir)
		cache[dir] = a
		return a
	}

	for _, c := range consumers {
		v := Verdict{Consumer: c, Own: get(c.Source)}
		var alive []Activity
		for _, p := range peers(c) {
			if p == c.Source {
				continue
			}
			v.PeerCount++
			if a := get(p); a.Active(now, window) {
				v.LivePeerCount++
				alive = append(alive, a)
			}
		}
		// Most recent first: the boxes that were written while this consumer's
		// was not are the ones that make the finding checkable by hand.
		sort.Slice(alive, func(i, j int) bool { return alive[i].Last.After(alive[j].Last) })
		if len(alive) > maxNamedPeers {
			alive = alive[:maxNamedPeers]
		}
		v.LivePeers = alive
		rep.Verdicts = append(rep.Verdicts, judge(v, now, window))
	}
	return rep
}

// judge is the predicate, and the order of its branches is the design.
func judge(v Verdict, now time.Time, window time.Duration) Verdict {
	c := v.Consumer
	switch {
	case v.Own.Err != nil:
		v.Status = StatusUndetermined
		v.Detail = fmt.Sprintf("NOT CHECKED: %s reads %s=%s, which could not be read (%v) — this is not a report that the source is quiet",
			c.Label, c.SourceKey, c.Source, v.Own.Err)
		return v

	case !v.Own.Exists:
		v.Status = StatusMissing
		v.Detail = fmt.Sprintf("%s reads %s=%s, which is not a directory on this machine — the job polls it anyway and reports no error, so nothing downstream can observe the absence (%s)",
			c.Label, c.SourceKey, c.Source, c.PlistPath)
		return v

	case v.Own.Active(now, window):
		v.Status = StatusLive
		v.Detail = fmt.Sprintf("%s reads %s=%s, which had %s",
			c.Label, c.SourceKey, c.Source, activityPhrase(v.Own, now, window))
		return v
	}

	// From here the configured source is quiet. Whether that is a FINDING
	// depends entirely on whether anything comparable is not — and if nothing
	// comparable is live, this package has no opinion and must say so rather
	// than fall through to a pass.
	if v.LivePeerCount == 0 {
		v.Status = StatusUndetermined
		v.Detail = fmt.Sprintf("NOT CHECKED: %s reads %s=%s, which had no arrivals in the last %s — and neither did any of the %d comparable source(s), so a consumer pointed at a dead box and a machine where nothing is being written anywhere are the same reading here. This is not a pass",
			c.Label, c.SourceKey, c.Source, window, v.PeerCount)
		return v
	}

	v.Status = StatusStarved
	v.Detail = fmt.Sprintf("%s reads %s=%s and NOTHING HAS ARRIVED THERE in the last %s, while %s. The job is loaded, polling, and reporting no error; every instrument that watches the job itself will go on reading green. Re-point it or retire it — %s",
		c.Label, c.SourceKey, c.Source, window,
		arrivedElsewhere(v), c.PlistPath)
	return v
}

// arrivedElsewhere names the evidence without printing the whole store.
func arrivedElsewhere(v Verdict) string {
	names := make([]string, 0, len(v.LivePeers))
	for _, p := range v.LivePeers {
		names = append(names, p.String())
	}
	if v.LivePeerCount == 1 && len(names) == 1 {
		return "data is arriving at " + names[0]
	}
	s := fmt.Sprintf("%d of %d comparable sources are receiving", v.LivePeerCount, v.PeerCount)
	if len(names) == 0 {
		return s
	}
	if v.LivePeerCount > len(names) {
		return fmt.Sprintf("%s, most recently %s (and %d more)", s, strings.Join(names, "; "), v.LivePeerCount-len(names))
	}
	return fmt.Sprintf("%s: %s", s, strings.Join(names, "; "))
}

func activityPhrase(a Activity, now time.Time, window time.Duration) string {
	if a.Count > 0 {
		return fmt.Sprintf("%d arrival(s) in the last %s", a.Count, window)
	}
	return fmt.Sprintf("no backlog but was written %s ago (a source a consumer is draining reads empty, which is not the same as dead)", now.Sub(a.Last).Round(time.Minute))
}

// sampleDir is the live SampleFunc.
func sampleDir(dir string, now time.Time, window time.Duration) Activity {
	a := Activity{Dir: dir}
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return a
	case err != nil:
		a.Err = err
		return a
	case !info.IsDir():
		return a
	}
	a.Exists = true
	a.Last = info.ModTime()

	cutoff := now.Add(-window)
	// A directory whose own mtime predates the window has had no entry created
	// or unlinked since — which is what an arrival IS — so there is nothing to
	// count and the listing is skipped.
	//
	// This is a cost decision with a measurement behind it, not a
	// micro-optimisation. The comparison population for a mailbox on this
	// machine is every mailbox in the store: 1000-odd directories, one of them
	// holding 1249 files, walked on every `pogo doctor --check`. A detector
	// expensive enough to be worth removing from the checklist is a detector
	// that will be.
	if a.Last.Before(cutoff) {
		return a
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		a.Err = err
		return a
	}
	for _, e := range entries {
		// Dotfiles are not arrivals. .DS_Store gets its mtime bumped by a
		// Finder window, and a source that looks live because somebody browsed
		// it is exactly the false green this package exists to remove.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(a.Last) {
			a.Last = fi.ModTime()
		}
		if fi.ModTime().After(cutoff) {
			a.Count++
		}
	}
	return a
}

// Audit is the live sweep: discover consumers from installed plists, then judge
// them.
//
// A discovery error is carried on the Report rather than returned, so a caller
// that renders the report cannot accidentally render "no findings" for a sweep
// that never happened.
func Audit(launchAgentsDir string, now time.Time, window time.Duration) Report {
	if window <= 0 {
		window = DefaultWindow
	}
	consumers, err := Discover(launchAgentsDir)
	if err != nil {
		return Report{Now: now, Window: window, Err: err}
	}
	sample := func(dir string) Activity { return sampleDir(dir, now, window) }
	peers := func(c Consumer) []string { return peersFor(c, consumers) }
	return Evaluate(consumers, sample, peers, now, window)
}

// DefaultLaunchAgentsDir is where launchd keeps per-user jobs.
func DefaultLaunchAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}
