package driftwatch

// revision.go adds the POSITIVE staleness detector (mg-5bd2) beside the mirror
// drift check.
//
// THE PROBLEM IT EXISTS FOR. Every alarm we had about the daemon being current
// was indexed to the nightly deploy job's own exit code. That answers a proxy
// question:
//
//	asked:              did last night's deploy job exit zero?
//	what we care about: IS THE DAEMON CURRENT?
//
// The proxy goes dark exactly when the job stops running. On 2026-08-01..08-04
// the job never fired — no `pogo-deploy: start` line at all — so there was no
// exit code, so there was no alarm, and four silent nights looked identical to
// four healthy ones because health is also "no alarm". By 08-07 the running
// pogod was 85 commits behind main on a binary built from a 2026-07-30 commit.
//
// It is blind to two modes by construction:
//
//	1. the run never firing;
//	2. a run exiting 0 without the new binary reaching the daemon.
//
// WHAT THIS CHECKS INSTEAD. It states the thing we care about positively and
// does not route through the job at all: how old is the commit the running
// process was built from? That number moves only when a new binary is actually
// running, so it is true under all three deploy failure modes — job failed
// loudly, job never fired, job exited 0 without installing. All three produce
// the same observable (the running revision's commit date stops advancing),
// which is the point of not asking about the job.
//
// WHERE THE READING COMES FROM. The running revision and its commit timestamp
// are read IN-PROCESS from debug.ReadBuildInfo — the same `vcs.revision` /
// `vcs.time` pair GET /version reports. Deliberately not an HTTP call to
// localhost:10000: a loopback probe can be answered by a process that is not
// this daemon (mg-e314 documented exactly that impersonation, and doctor now
// carries a row saying it cannot prevent it), and a self-report read out of our
// own binary cannot be. It also means the check needs no repo, no network and
// no config to be armed — the one thing the mg-5701 lineage keeps getting wrong
// is shipping a detector that is inert until somebody remembers to configure it.
//
// NOTE `vcs.time` IS THE COMMIT'S TIME, NOT THE BUILD'S. That is what we want
// and it is also why neither uptime nor a recent restart can substitute for it:
// on this box pogod's start_time was 2026-08-04 while its binary was built from
// a 2026-07-30 commit. A bounce re-launched the same stale binary. Only the
// revision says which code is running.
//
// COMMITS-BEHIND IS CONTEXT, NEVER A GATE. When a repo is configured the notice
// also carries `git rev-list --count <rev>..origin/main`. It is reported and
// never used to suppress: origin/main is a remote-tracking ref that only a
// fetch refreshes, so a suppressor keyed on "behind == 0" would read a stale ref
// and go quiet — reintroducing, one layer down, the exact go-dark failure this
// detector was written to remove. The cost of that choice is a false positive if
// main goes quiet for longer than the threshold while the daemon is genuinely
// current. That trade is deliberate and it is the right way round: a false
// positive is a mail with "0 commits behind origin/main" in it, a false negative
// is another four silent nights.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// DefaultStaleAfter is N: how old the running revision's COMMIT may be before
// the daemon is reported stale.
//
// Seven days, because:
//
//   - The deploy is nightly. A healthy pipeline puts the daemon on a commit
//     from last night, so seven days is seven consecutive missed deploys —
//     an order of magnitude past any single bad night, a weekend, or one flaky
//     run, which is what keeps it from crying wolf.
//   - It would have fired on 2026-08-06 for the incident that prompted it (the
//     running binary's commit is dated 2026-07-30), a day before a human
//     noticed by hand, and against the state as measured on 2026-08-07 the gap
//     is 8 days — so this is not a threshold picked to be safely un-fireable.
//   - It tolerates the only realistic false-positive shape, a quiet main. Seven
//     days with no commit does not happen in this repo (85 landed in the eight
//     days of the incident window), and if it ever did the notice says so on
//     its own face by reporting the commits-behind count.
const DefaultStaleAfter = 7 * 24 * time.Hour

// DefaultStaleRenotify is the base gap of the notice backoff — see
// DefaultStaleMaxNotices for the whole bounding argument.
const DefaultStaleRenotify = 24 * time.Hour

// DefaultStaleMaxNotices bounds how many times ONE stale revision may mail.
//
// This matters more than it looks. The condition holds for DAYS while the
// coarse sampler runs every 15 minutes, so an unbounded detector would mail
// ~96 times a day, and the five deploy REDs that preceded this all reached
// Daniel and were all filtered — a sixth alarm on the same channel is only an
// improvement if it cannot become background noise.
//
// So: the gap doubles after each notice. With the 24h base that puts notices at
// detection, +1d, +3d and +7d — four in a week — and then this revision is DONE
// mailing. The ratchet resets when the running revision changes, which for an
// in-process build stamp means a restart, and a restart that did not fix the
// staleness has earned a fresh notice.
//
// What is deliberately NOT bounded is the event log: `revision_stale` is
// emitted on every stale sample. Mail is the scarce channel and gets a cap; the
// pull-side record is cheap and stays complete, so "pogod stopped mailing" never
// has to be read as "the condition cleared".
const DefaultStaleMaxNotices = 4

// SelfRevision is the running process's own build identity — the same
// vcs.revision / vcs.time pair GET /version reports.
type SelfRevision struct {
	// Revision is the full vcs.revision SHA. Empty means the binary carries no
	// vcs stamp.
	Revision string
	// CommitTime is vcs.time: the time of the COMMIT the binary was built from,
	// not the time of the build and emphatically not the time of the process
	// start.
	CommitTime time.Time
	// Modified is vcs.modified — the working tree was dirty at build time, so
	// the running code is not exactly Revision.
	Modified bool
}

// Stamped reports whether the build carries enough vcs information to date.
func (s SelfRevision) Stamped() bool {
	return s.Revision != "" && !s.CommitTime.IsZero()
}

// Short is the abbreviated revision used in subjects and log lines.
func (s SelfRevision) Short() string {
	if len(s.Revision) > 8 {
		return s.Revision[:8]
	}
	return s.Revision
}

// RevisionFunc reports the running process's build identity. Production uses
// BuildRevision; tests substitute a fake so a known-bad state can be replayed
// deterministically.
type RevisionFunc func() SelfRevision

// Behind is the commits-behind context for one revision. CONTEXT ONLY — the
// watcher never lets any field of it suppress a notice (see the package comment
// at the top of this file).
type Behind struct {
	// Count is how many commits origin/main is ahead of the revision. Only
	// meaningful when Known.
	Count int
	// Known reports that Count was established.
	Known bool
	// Foreign reports that the revision is not in the configured repo AT ALL —
	// the running binary was not built from this repository. It is a distinct
	// answer from "the count is unavailable", and worth saying out loud rather
	// than folding into a shrug, because it happens for real on this box: a
	// polecat worktree lives under ~/.pogo, which is itself a git repo, so
	// `go build` inside one walks up and stamps ~/.pogo's HEAD instead of
	// pogo's. A binary carrying that stamp is dated against an unrelated
	// project's history, which is exactly the sort of thing a notice must name
	// instead of quietly reporting as an age.
	Foreign bool
}

// BehindFunc reports the commits-behind context for rev.
type BehindFunc func(rev string) Behind

// BuildRevision reads the running binary's vcs stamp. It is the production
// RevisionFunc.
func BuildRevision() SelfRevision {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return SelfRevision{}
	}
	var s SelfRevision
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			s.Revision = setting.Value
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, setting.Value); err == nil {
				s.CommitTime = t.UTC()
			}
		case "vcs.modified":
			s.Modified = setting.Value == "true"
		}
	}
	return s
}

// GitBehind builds a BehindFunc that counts how far origin/main is ahead of rev
// in repo (a leading ~ is expanded). It shells out read-only (`git rev-list
// --count`) and never fetches: refreshing a remote-tracking ref is a mutation
// this report-only runner has no business performing, and the count is context,
// so a stale answer is fine and a missing one is fine too.
func GitBehind(repo string) BehindFunc {
	repo = expandHome(repo, os.Getenv("HOME"))
	return func(rev string) Behind {
		if repo == "" || rev == "" {
			return Behind{}
		}
		// Ask first whether the revision is even in this repo, so a binary built
		// from somewhere else is reported as such instead of as an unavailable
		// count. A repo we cannot read at all fails this too, and falls through
		// to the plain unknown below rather than being called foreign.
		if err := exec.Command("git", "-C", repo, "cat-file", "-e", rev+"^{commit}").Run(); err != nil {
			if exec.Command("git", "-C", repo, "rev-parse", "--git-dir").Run() == nil {
				return Behind{Foreign: true}
			}
			return Behind{}
		}
		out, err := exec.Command("git", "-C", repo, "rev-list", "--count", rev+"..origin/main").Output()
		if err != nil {
			return Behind{}
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || n < 0 {
			return Behind{}
		}
		return Behind{Count: n, Known: true}
	}
}

// Staleness is one revision sample: what is running, how old it is, and whether
// that crosses the threshold.
type Staleness struct {
	SelfRevision
	// Now is the sample time the age was computed against.
	Now time.Time
	// Age is Now - CommitTime.
	Age time.Duration
	// Threshold is N.
	Threshold time.Duration
	// Stale is the alarm predicate: Age > Threshold.
	Stale bool
	// Behind is how many commits origin/main is ahead, or -1 when unknown.
	// Context only.
	Behind int
	// Foreign reports that the running revision is not in the configured repo at
	// all. Context only — it never gates the alarm either, but it changes what
	// the notice says the age MEANS.
	Foreign bool
}

// AgeDays renders the age in whole days, the unit the threshold is expressed in
// and the unit that belongs in a subject line.
func (s Staleness) AgeDays() int {
	return int(s.Age / (24 * time.Hour))
}

// evaluate turns a build stamp into a Staleness. Split out from the watcher so
// the predicate can be tested against replayed real-world readings without a
// clock, a mailer, or an interval.
func evaluate(rev SelfRevision, behind BehindFunc, now time.Time, threshold time.Duration) Staleness {
	s := Staleness{
		SelfRevision: rev,
		Now:          now,
		Threshold:    threshold,
		Behind:       -1,
	}
	if !rev.Stamped() {
		return s
	}
	s.Age = now.Sub(rev.CommitTime)
	s.Stale = s.Age > threshold
	if behind != nil {
		b := behind(rev.Revision)
		s.Foreign = b.Foreign
		if b.Known {
			s.Behind = b.Count
		}
	}
	return s
}

// staleSubject is the line that travels. It carries the age, the revision, the
// commit date and — when known — the commits-behind count, because the subject
// is what gets skimmed and forwarded, and because a subject whose numbers GROW
// cannot be matched by the constant-subject filter rule that swallowed the five
// `pogo-deploy` REDs before it.
func staleSubject(s Staleness) string {
	subject := fmt.Sprintf("pogod is running %d-day-old code — revision %s (%s)",
		s.AgeDays(), s.Short(), s.CommitTime.Format("2006-01-02"))
	switch {
	case s.Foreign:
		// Louder than a commits-behind number, and it belongs in the subject: it
		// means the age above is measured against some other project's history,
		// so the reader must not take the day count at face value.
		subject += ", NOT a commit in the pogo repo"
	case s.Behind >= 0:
		subject += fmt.Sprintf(", %d commits behind main", s.Behind)
	}
	return subject
}

// staleBody explains what was measured, why the deploy job's own alarms cannot
// see it, and what the reader is expected to do — including the notice budget,
// so "this went quiet" is never mistaken for "this cleared".
func staleBody(s Staleness, notice, maxNotices int, repo string) string {
	var b strings.Builder

	fmt.Fprintf(&b,
		"The running pogod was built from a commit that is %s old. This is a POSITIVE staleness check: it reads the running process's own build stamp and does not consult the deploy job at all.\n\n",
		formatAge(s.Age))

	b.WriteString("MEASURED\n")
	fmt.Fprintf(&b, "  running revision   %s\n", s.Revision)
	fmt.Fprintf(&b, "  commit date        %s\n", s.CommitTime.Format(time.RFC3339))
	fmt.Fprintf(&b, "  age                %s (threshold %s)\n", formatAge(s.Age), formatAge(s.Threshold))
	switch {
	case s.Foreign:
		fmt.Fprintf(&b, "  behind origin/main N/A — revision %s IS NOT IN %s\n", s.Short(), repo)
		fmt.Fprintf(&b, "                     The running binary was not built from that repository, so the age above is\n"+
			"                     measured against SOME OTHER project's history and is not a statement about pogo.\n"+
			"                     A known cause on this box: ~/.pogo is itself a git repo, so `go build` run inside a\n"+
			"                     polecat worktree (~/.pogo/polecats/*) walks up and stamps ~/.pogo's HEAD.\n")
	case s.Behind >= 0:
		fmt.Fprintf(&b, "  behind origin/main %d commits (context only — read from %s, only as fresh as the last fetch)\n", s.Behind, repo)
	default:
		b.WriteString("  behind origin/main unknown (no [drift_watch] self_repo configured — the age above does not depend on it)\n")
	}
	if s.Modified {
		b.WriteString("  build tree         DIRTY at build time — the running code is not exactly this commit\n")
	}
	b.WriteString("\n")

	b.WriteString(
		"WHY THE DEPLOY ALARMS DID NOT TELL YOU\n" +
			"Every other 'is the daemon current' alarm is indexed to the nightly deploy job's exit code, so it can only report a run that HAPPENED. A night the job never fires produces no exit code and therefore no alarm, and a run that exits 0 without the new binary reaching the daemon reports success. Both look exactly like health. This check reads the running binary instead, so it fires on all three: the job failing loudly, the job never firing, and the job exiting 0 without installing.\n\n")

	b.WriteString(
		"WHAT TO DO\n" +
			"  curl -s localhost:10000/version | jq -r .revision   # confirm independently\n" +
			"  # then redeploy and RESTART pogod — a restart alone re-launches the same binary.\n" +
			"Note that neither uptime nor a recent restart answers this: pogod can be freshly started and still be running week-old code.\n\n")

	b.WriteString(
		"THIS IS REPORT-ONLY. pogod did not redeploy itself and has no seam through which it could. A reconcile loop fighting a genuinely-broken build artifact is the unbounded-reaper shape (mg-345b).\n\n")

	fmt.Fprintf(&b,
		"NOTICE %d OF %d for this revision. The gap doubles after each one, so they land at detection, +1d, +3d and +7d, and then stop; the ratchet resets only when the running revision changes. After notice %d this condition will keep being recorded as a `revision_stale` event on every sample but will send no more mail — silence from here is not the condition clearing.\n",
		notice, maxNotices, maxNotices)

	return b.String()
}

// formatAge renders a duration in days and hours, which is the resolution this
// check operates at. Go's default duration string would render eight days as
// "192h0m0s".
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	switch {
	case days == 0:
		return fmt.Sprintf("%dh", hours)
	case hours == 0:
		return fmt.Sprintf("%dd", days)
	default:
		return fmt.Sprintf("%dd%dh", days, hours)
	}
}

// sampleRevision runs one revision-staleness sample. It never reconciles, never
// redeploys, and has no seam through which it could — the only side effects are
// a mail and an event.
func (w *Watcher) sampleRevision(now time.Time) {
	rev := w.revision()

	if !rev.Stamped() {
		// Nothing to date — and, the important half, nothing CLAIMED either.
		// Declare the blindness once so it is a stated silence rather than a
		// silence that reads as health, then stay quiet. Mailing here would
		// fire on every test binary and `go run`, neither of which carries a
		// vcs stamp.
		w.unstampedOnce.Do(func() {
			log.Printf("pogod: revision-staleness check is NOT armed — this binary carries no vcs stamp " +
				"(built without VCS info). pogod cannot tell how old the code it is running is.")
			w.emit(events.Event{
				EventType: "revision_stale_disarmed",
				Agent:     "pogod",
				Details:   map[string]any{"reason": "binary carries no vcs.revision/vcs.time stamp"},
			})
		})
		return
	}

	s := evaluate(rev, w.behind, now, w.staleAfter)
	if !s.Stale {
		return
	}

	notice, mail := w.staleNoticeDue(rev.Revision, now)

	details := map[string]any{
		"revision":    s.Revision,
		"commit_time": s.CommitTime.Format(time.RFC3339),
		"age_days":    s.AgeDays(),
		"age_hours":   int(s.Age.Hours()),
		"threshold":   formatAge(s.Threshold),
		"notice":      notice,
		"max_notices": w.maxNotices,
		"mailed":      mail,
	}
	if s.Behind >= 0 {
		details["behind_main"] = s.Behind
	}
	if s.Foreign {
		details["revision_foreign"] = true
	}
	if s.Modified {
		details["build_modified"] = true
	}

	if mail {
		subject := staleSubject(s)
		if err := w.mail(mailTo, mailFrom, subject, staleBody(s, notice, w.maxNotices, w.behindRepo)); err != nil {
			// Detected but not reportable. Record it: a staleness notice that
			// reaches nobody is the failure this detector exists to remove, and
			// it must not vanish just because the mail channel is down.
			details["mail_error"] = err.Error()
			log.Printf("pogod: revision-staleness notice (%s) could not be mailed: %v", s.Short(), err)
		} else {
			log.Printf("pogod: STALE revision %s (commit %s, %s old) — notice %d/%d mailed to %s",
				s.Short(), s.CommitTime.Format("2006-01-02"), formatAge(s.Age), notice, w.maxNotices, mailTo)
		}
	}

	// Emitted on EVERY stale sample, mailed or not. The mail budget is capped
	// (see DefaultStaleMaxNotices); this record is not, so exhausting the budget
	// never turns the condition invisible.
	w.emit(events.Event{EventType: "revision_stale", Agent: "pogod", Details: details})
}

// staleNoticeDue advances the notice ratchet and reports which notice this
// sample is, and whether it should mail.
//
// The backoff doubles from the base gap — notices land at 0, +renotify,
// +3*renotify, +7*renotify — and stops once maxNotices have gone out for this
// revision. A revision change resets the budget: a different binary is running,
// so it gets its own first notice, and that includes the case where a restart
// re-launched the SAME stale binary under a new process (the ratchet lives in
// memory, so a restart re-arms it — a restart that did not fix staleness has
// earned a notice).
//
// A negative maxNotices means unbounded, which only tests ask for.
func (w *Watcher) staleNoticeDue(revision string, now time.Time) (notice int, mail bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if revision != w.staleRev {
		w.staleRev = revision
		w.staleNotices = 0
		w.lastStaleNotice = time.Time{}
	}

	if w.staleNotices == 0 {
		w.staleNotices = 1
		w.lastStaleNotice = now
		return 1, true
	}
	if w.maxNotices >= 0 && w.staleNotices >= w.maxNotices {
		return w.staleNotices, false
	}
	// gap doubles: notice 1 -> +1x, notice 2 -> +2x, notice 3 -> +4x.
	gap := w.renotify * (1 << (w.staleNotices - 1))
	if now.Sub(w.lastStaleNotice) < gap {
		return w.staleNotices, false
	}
	w.staleNotices++
	w.lastStaleNotice = now
	return w.staleNotices, true
}

// expandHome resolves a leading ~ in a configured repo path.
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") && home != "" {
		return filepath.Join(home, p[2:])
	}
	return p
}
