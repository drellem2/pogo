package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/service"
)

// mg-a19a — pogod's log is not being written, and nothing said so for 44 hours.
//
// MEASURED 2026-09-03 20:53Z on this box. pogod pid 6610, up 57h45m, holding
// the lock, serving :10000, delivering scheduled fires and running a refinery
// gate. Its parent was Emacs, not launchd; its fd 2 was /dev/ttys007; it held
// no descriptor on ~/Library/Logs/pogo/pogod.log and had written zero lines
// there in its entire life. `launchctl print` reported runs = 4639 for
// com.pogo.daemon — 4,639 spawns, every one of which correctly refused the lock
// held by 6610 and exited 1, and the log's last 9,278 lines are those refusals.
//
// So the whole file was written by processes that lived milliseconds, and
// nothing on the box reported that. The ticket's third question was whether
// pogod should refuse to run when it cannot write its log, or at minimum
// surface it through an existing alarm. This is the second: refusing would
// break the legitimate case (`pogo server start` from a terminal, where output
// to a tty is the point), and a daemon that exits over its own logging turns a
// visibility fault into an outage.
//
// WHY THE ALARM IS NOT A LOG LINE — and this is not the usual version of that
// argument. Every other condition in this daemon logs correctly to a file
// nobody reads. THIS one would log correctly to a file that does not exist as a
// record at all: the notice that the log is not being written would be written
// to the log that is not being written. The annunciator mails the coordinator
// AND emits pogod_condition onto the durable event spine, and both of those
// survive the exact fault being reported. That is the only reason this detector
// is worth having.
//
// THE BOUND, stated because it is load-bearing: this fires at startup, from
// inside the daemon. The daemon that was detached for 57 hours on 2026-09-03
// was already running, so nothing here would have caught it in flight. What
// closes that half is `pogo service log`, which any diagnostician (or watcher)
// can run against a daemon that is already up. The two are complements, not
// alternatives.

const logDestinationConditionID = "pogod_log_not_written"

// logDestination is pogod's reading of its own fd 2 against the file its
// installed service definition names.
type logDestination struct {
	// JobLogPath is the path the INSTALLED plist names, read from disk rather
	// than derived from this build's template — a plist written by an older
	// build can name a different file, and the file that matters is the one a
	// reader following the documented procedure will grep.
	JobLogPath string
	// Writing is whether fd 2 and that file are the same inode.
	Writing bool
	// Stderr describes where fd 2 actually goes, for the notice.
	Stderr string
}

// observeOwnLogDestination compares this process's stderr against the installed
// job's log path. ok is false when there is nothing to compare — no installed
// job names a log (a fresh box, a sandbox, Linux, where the systemd unit sets
// no redirect and output goes to the journal). That is not a fault and must not
// annunciate: a condition that is always true on a whole class of host is a
// standing alarm with no transition, and the fastest way to get the channel
// muted.
//
// This uses os.SameFile — a device+inode comparison — and not a path
// comparison, so a rotated-away log (same descriptor, new name) and a symlinked
// log directory both read correctly. The daemon reading its OWN descriptor is
// strictly better evidence than anything an outside process can take, which is
// why the verdict is computed here rather than shelled out to lsof.
func observeOwnLogDestination() (logDestination, bool) {
	jobPath, ok := service.InstalledLogPath()
	if !ok {
		return logDestination{}, false
	}

	self, err := os.Stderr.Stat()
	if err != nil {
		// Unreadable own descriptor. Not a pass and not an alarm: report it as
		// undetermined so the caller neither clears nor raises.
		return logDestination{}, false
	}

	d := logDestination{JobLogPath: jobPath, Stderr: describeStderr(self)}
	if named, serr := os.Stat(jobPath); serr == nil && os.SameFile(self, named) {
		d.Writing = true
	}
	return d, true
}

// describeStderr renders fd 2 for a human. It deliberately does not claim a
// PATH: os.Stderr.Name() is "/dev/stderr" on every platform regardless of where
// the descriptor actually goes, and printing that would be a confident wrong
// answer inside a notice about confident wrong answers.
func describeStderr(fi os.FileInfo) string {
	switch m := fi.Mode(); {
	case m&os.ModeCharDevice != 0:
		return "a character device — a terminal or /dev/null. Nothing pogod writes is being kept."
	case m&os.ModeNamedPipe != 0:
		return "a pipe. Whatever is on the other end is where pogod's output went; if nothing is reading it, nowhere."
	case m&os.ModeSocket != 0:
		return "a socket."
	case m.IsRegular():
		return fmt.Sprintf("a regular file that is NOT the one named above (%d bytes at this reading). Its name can be recovered with `lsof -p <pid> -a -d 2 -F n`.", fi.Size())
	default:
		return "not a regular file (mode " + fi.Mode().String() + ")."
	}
}

// conditionLogNotWritten is the notice. Row carries the work item rather than an
// enumeration row: this condition does not come from
// docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md, and
// the Row field's job is to answer "why does this exist" from the event alone.
func conditionLogNotWritten(to string, d logDestination) pogodCondition {
	var b strings.Builder

	b.WriteString("The pogod that owns this POGO_HOME is not writing to the log its own service\n")
	b.WriteString("definition names. It is running normally in every other respect.\n\n")

	b.WriteString("DETAIL\n")
	fmt.Fprintf(&b, "  the installed job names : %s\n", d.JobLogPath)
	fmt.Fprintf(&b, "  pogod's fd 2 is         : %s\n", d.Stderr)
	fmt.Fprintf(&b, "  pogod pid               : %d\n\n", os.Getpid())

	b.WriteString("WHAT IT COSTS WHILE UNFIXED\n")
	b.WriteString("  Every diagnostic that greps that file is answering questions about this\n")
	b.WriteString("  daemon from a record this daemon did not write — with no indication of it.\n")
	b.WriteString("  That includes the refinery-log procedure in the mayor and polecat prompts\n")
	b.WriteString("  (`grep refinery: \"$log\" | grep <mr-id>`), which will return empty for every\n")
	b.WriteString("  MR, and an empty grep is indistinguishable from \"the refinery logged nothing\n")
	b.WriteString("  about this MR\" — the wrong conclusion to reach mid-diagnosis of a stuck merge.\n\n")
	b.WriteString("  A file in this state does not look empty. On 2026-09-03 an architect grepped\n")
	b.WriteString("  it for a fleet-stop window, found 51 `cause=modal_wedge` lines and 1,875\n")
	b.WriteString("  `ANIMATING BUT NOT WORKING` lines, and built a specific and mechanically\n")
	b.WriteString("  plausible root cause on them. Every one of those lines predated the window by\n")
	b.WriteString("  days; the real cause was an entitlement refusal (mg-6616). It took a second\n")
	b.WriteString("  investigator and a second source to withdraw the lead. The file alone gave no\n")
	b.WriteString("  signal that its answer was out of date.\n\n")

	b.WriteString("WHAT TO DO\n")
	b.WriteString("  1. `pogo service log` — the same comparison, runnable against a daemon that is\n")
	b.WriteString("     already up. Confirms this from outside and prints what to grep instead.\n")
	b.WriteString("  2. `pogo service supervision` — this state usually means the running pogod was\n")
	b.WriteString("     NOT started by launchd, in which case launchd is also supervising nothing\n")
	b.WriteString("     and a wedged daemon would never be restarted (mg-fa79). Different question,\n")
	b.WriteString("     same displacement, and it owes its own fix.\n")
	b.WriteString("  3. Until it is repaired, treat that file as covering only the period BEFORE\n")
	b.WriteString("     this daemon started. Any question whose window reaches past that gets \"the\n")
	b.WriteString("     record is absent\", never \"the record is negative\" — those are different\n")
	b.WriteString("     answers and the file renders them identically.\n")
	b.WriteString("  4. The repair is to let launchd own the daemon: stop the displacing pogod and\n")
	b.WriteString("     `launchctl kickstart -k gui/$(id -u)/com.pogo.daemon`. Do that deliberately\n")
	b.WriteString("     — it is a restart of the live fleet's daemon, not a diagnostic.\n\n")

	b.WriteString("WHY THIS IS MAIL AND NOT JUST A LOG LINE\n")
	b.WriteString("  Because a log line is the one channel this condition is guaranteed to break.\n")
	b.WriteString("  Every other pogod condition logs correctly to a file nobody reads; this one\n")
	b.WriteString("  would log correctly to a file that is not a record at all — the notice that\n")
	b.WriteString("  the log is not being written, written to the log that is not being written.\n")
	b.WriteString("  This notice is mailed AND emitted onto the durable event spine\n")
	b.WriteString("  (`pogo events --type pogod_condition`), both of which survive the fault being\n")
	b.WriteString("  reported. Measured cost of having neither: 44 hours, and one wrong root cause\n")
	b.WriteString("  in a live incident (mg-a19a, mg-6616).\n")

	return pogodCondition{
		ID:  logDestinationConditionID,
		Row: "mg-a19a",
		To:  to,
		Detail: fmt.Sprintf("installed job names %s; pogod pid %d fd 2 is %s",
			d.JobLogPath, os.Getpid(), d.Stderr),
		// Fingerprinted on the destination and not on the pid: a daemon
		// restarted into the same wrong destination is the same unfixed
		// condition, and re-mailing on every bounce is the firehose.
		Fingerprint: "dest:" + d.JobLogPath + "|" + d.Stderr,
		Subject: "[pogod] MY LOG IS NOT BEING WRITTEN — every diagnostic that greps " +
			"pogod.log is reading another process's file",
		Body: b.String(),
	}
}

// annunciateLogDestination takes the reading and raises, clears, or does
// neither. It is called at startup (the first moment an addressee exists) and
// again on every heartbeat tick — see the two call sites in main.go for why one
// of those is not enough.
//
// The three-way outcome is the point. An UNDETERMINED reading — no installed
// job to compare against — must neither raise nor clear: raising would put a
// standing alarm on every host that never installed the service, and clearing
// would let a plist going missing silently resolve a live condition.
func annunciateLogDestination(a *conditionAnnunciator, to string, now time.Time) {
	dest, determined := observeOwnLogDestination()
	if !determined {
		return
	}
	if dest.Writing {
		a.Clear(logDestinationConditionID, now)
		return
	}
	a.Raise(conditionLogNotWritten(to, dest), now)
}
