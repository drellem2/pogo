package driftwatch

// nofire.go is the RUNNER for the did-not-run detector (mg-2416). The judging
// lives in internal/staleness/nofire.go; this file is the half that makes it
// actually happen without anybody remembering to ask.
//
// WHAT IT DETECTS THAT NOTHING ELSE COULD. Of the eight nights 2026-07-29..
// 08-07, four (08-01..08-04) were not deploy failures — the job NEVER STARTED.
// No `pogo-deploy: start` line, no exit code, no attempt stamp, no mail. Every
// detector we owned was downstream of the job running, so all four nights were
// indistinguishable from healthy ones.
//
// HOW THIS DIFFERS FROM THE TWO CHECKS ALREADY IN THIS PACKAGE. They are not
// redundant with it and it is not redundant with them:
//
//	mirror drift (driftwatch.go)  asks whether a host artifact matches its repo
//	                              source. Says nothing about whether the nightly
//	                              fired.
//	revision staleness (revision.go)  asks how old the code pogod is running is.
//	                              It WOULD eventually have caught this outage —
//	                              but at seven days, and only as a consequence:
//	                              it reports that the daemon went stale, not that
//	                              the job stopped firing, and it cannot fire at
//	                              all on a night the deploy was a no-op because
//	                              nothing had landed.
//	no-fire (this file)           asks whether the JOB RAN, on the morning after
//	                              the FIRST silent night, and names the nights.
//
// The threshold difference is the point: four consecutive silent nights should
// not need a week and a stale-code symptom to surface.
//
// IT NEVER REQUIRES THE JOB TO HAVE RUN. That is the entire constraint. It
// consults no exit code, no stamp, no rc, and no launchd spawn counter — see
// internal/staleness/nofire.go for why `launchctl print`'s `runs` field is
// actively misleading here (measured 0 on this box after seven fires, because
// re-installing the plist resets it). It reads the log's start lines against
// the schedule, and the absence of a line for a night that was due IS the
// signal.
//
// IT DOES NOT DIAGNOSE WHY, AND THAT IS DELIBERATE. Powered off, wedged by the
// nondemand-spawn pending state (mg-50e0), LaunchAgent unloaded, plist removed —
// from the log these are indistinguishable, and they all have the same
// consequence. mg-2416's body proposed the wedge as the mechanism; measuring it
// (docs/investigations/deploy-nofire-nights-were-a-power-off-2026-08-07.md)
// showed the host was POWERED OFF from 07-31 21:18 to 08-04 11:40, which
// contains all four windows. A detector built around the hypothesised cause
// would have been built around the wrong one and would still have had to fire on
// this. So it reports the absence and names the candidates without asserting
// one.
//
// ARMING. It arms itself on any host where the nightly deploy LaunchAgent is
// INSTALLED, and needs no config key to do it — the mg-5701 lineage's recurring
// defect is shipping a detector that stays inert until somebody remembers to
// configure it, and this whole ticket exists because a gap went unwatched. On a
// host with no deploy agent (a dev box, a sandbox) there is no nightly to watch,
// so it disarms and SAYS SO once: a blind detector and a healthy host otherwise
// produce identical silence, which is the failure being removed, not a shape to
// reproduce.

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/staleness"
)

// deployAgentLabel is the nightly's launchd label, named in the notice so the
// reader has the exact string to paste into `launchctl print`. It mirrors
// internal/service's own deployLabel; it is repeated rather than imported
// because internal/service imports enough of the world that a detector should
// not take the dependency to obtain one string.
const deployAgentLabel = "com.pogo.deploy"

// DefaultNoFireRenotify is the base gap of the no-fire notice backoff.
//
// Twelve hours, half the revision check's base, because the conditions differ
// in what a reader can do about them. A stale revision is a standing state that
// one redeploy clears; a silent night is a repeating event whose next instance
// is TONIGHT, and a notice that lands before the next scheduled fire is the one
// that can still save a fifth night.
const DefaultNoFireRenotify = 12 * time.Hour

// DefaultNoFireMaxNotices bounds how many notices ONE unchanged run of silent
// nights may mail.
//
// Four, matching the revision check, and for the same reason: the five
// `pogo-deploy` REDs that preceded this incident all reached Daniel and were all
// filtered, so a new alarm on the same channel is only an improvement if it
// cannot become background noise. With the 12h base the notices land at
// detection, +12h, +36h and +84h.
//
// The budget is keyed to the SIGNATURE of the outage (see noFireSignature), so
// another silent night is a different condition and gets its own fresh notice.
// A run of nights that keeps growing therefore keeps mailing, once per new
// night — which is the one thing that must not be throttled away, since each
// new night is new information.
//
// As with revision staleness, the EVENT is not capped. Mail is the scarce
// channel; the pull-side record stays complete, so "pogod stopped mailing" never
// has to be read as "the nightly started firing again".
const DefaultNoFireMaxNotices = 4

// DeployLogFunc reads the deploy log. Returns the text, whether the file
// exists, and any read error. pogod injects a reader for
// service.DeployLogPath(); tests inject a literal.
//
// found=false and err != nil are kept apart on purpose. A log that does not
// exist is a finding about the DEPLOY (it has never written a line). A log that
// exists and cannot be read is a finding about THIS CHECK (it is blind), and
// the two must not produce the same alarm.
type DeployLogFunc func() (text string, found bool, err error)

// DeployInstalledFunc reports whether the nightly deploy LaunchAgent is
// installed on this host, and where its plist is. pogod injects
// service.DeployStatus. It is the arming gate: no nightly, nothing to watch.
type DeployInstalledFunc func() (installed bool, path string)

// noticeRatchet is the doubling notice budget shared by the checks in this
// package: a notice now, then at +gap, +2*gap, +4*gap, and then silence until
// the condition's KEY changes, at which point the budget starts over.
//
// Extracted so the two report-only checks cannot drift apart in how they bound
// their mail. Its zero value is ready to use.
type noticeRatchet struct {
	mu    sync.Mutex
	key   string
	count int
	last  time.Time
}

// due advances the ratchet and reports which notice this sample is and whether
// it should mail. gap is the base; max caps the count, and a negative max means
// unbounded (tests only).
func (r *noticeRatchet) due(key string, now time.Time, gap time.Duration, max int) (notice int, mail bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if key != r.key {
		r.key = key
		r.count = 0
		r.last = time.Time{}
	}
	if r.count == 0 {
		r.count = 1
		r.last = now
		return 1, true
	}
	if max >= 0 && r.count >= max {
		return r.count, false
	}
	if now.Sub(r.last) < gap*(1<<(r.count-1)) {
		return r.count, false
	}
	r.count++
	r.last = now
	return r.count, true
}

// FileDeployLog builds the production DeployLogFunc for a path.
func FileDeployLog(path string) DeployLogFunc {
	return func() (string, bool, error) {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return "", false, nil
		}
		if err != nil {
			return "", true, err
		}
		return string(b), true, nil
	}
}

// noFireSignature identifies one distinct outage, so the notice budget is spent
// per-outage rather than per-condition.
//
// It is the newest missed night plus the count. Another silent night changes
// both, which resets the budget and mails again — deliberate: a fifth
// consecutive silent night is new information and must not be swallowed by a
// budget spent on the first four. A deploy that starts firing again clears the
// condition entirely and nothing is sent.
func noFireSignature(r staleness.NoFireReport) string {
	if !r.LogFound {
		return "log-missing"
	}
	if len(r.Missed) == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d", r.Missed[0], r.MissedTotal)
}

// sampleNoFire runs one did-not-run sample. Report-only: the only side effects
// are a mail and an event. It never kickstarts the job, never re-bootstraps the
// LaunchAgent, and has no seam through which it could — same ruling as the rest
// of this package (mg-345b), and doubly so here, where the suspected mechanism
// is a launchd wedge that a fighting loop would hammer rather than fix.
func (w *Watcher) sampleNoFire(now time.Time) {
	if w.deployInstalled != nil {
		installed, _ := w.deployInstalled()
		if !installed {
			// No nightly on this host, so there is nothing to be silent. Declare
			// it once rather than sit quiet: an unarmed detector and a healthy
			// host must not look the same.
			w.noFireDisarmedOnce.Do(func() {
				log.Printf("pogod: no-fire deploy check is NOT armed — the %s LaunchAgent is not installed on this host, "+
					"so there is no nightly deploy to watch.", deployAgentLabel)
				w.emit(events.Event{
					EventType: "deploy_nofire_disarmed",
					Agent:     "pogod",
					Details:   map[string]any{"reason": "nightly deploy LaunchAgent is not installed"},
				})
			})
			return
		}
	}

	text, found, err := w.deployLog()
	if err != nil {
		// The log exists and could not be read. That is blindness, not health,
		// and it gets its own once-per-process line for the same reason the
		// unstamped-binary case does. It is NOT mailed: a permission or IO fault
		// on the log is a local problem, and mailing it on every sample would
		// spend the channel that the real finding needs.
		w.noFireUnreadableOnce.Do(func() {
			log.Printf("pogod: no-fire deploy check is BLIND — %s exists but could not be read: %v",
				w.deployLogPath, err)
			w.emit(events.Event{
				EventType: "deploy_nofire_disarmed",
				Agent:     "pogod",
				Details:   map[string]any{"reason": "deploy log unreadable", "error": err.Error(), "path": w.deployLogPath},
			})
		})
		return
	}

	rep := staleness.CheckNoFire(w.deployLogPath, text, found, now, w.schedule)
	if rep.Clean() {
		return
	}

	sig := noFireSignature(rep)
	notice, mail := w.noFireRatchet.due(sig, now, w.noFireRenotify, w.noFireMaxNotices)

	details := map[string]any{
		"log_path":       rep.LogPath,
		"log_found":      rep.LogFound,
		"missed_total":   rep.MissedTotal,
		"missed":         rep.Missed,
		"fires":          rep.Fires,
		"last_due_night": rep.LastDueNight,
		"notice":         notice,
		"max_notices":    w.noFireMaxNotices,
		"mailed":         mail,
	}
	if rep.Horizon != "" {
		details["horizon"] = rep.Horizon
	}
	if rep.HorizonLimited {
		details["horizon_limited"] = true
	}
	if rep.Unparsed > 0 {
		details["unparsed_start_lines"] = rep.Unparsed
	}

	if mail {
		subject := noFireSubject(rep)
		if err := w.mail(mailTo, mailFrom, subject, noFireBody(rep, notice, w.noFireMaxNotices, w.schedule)); err != nil {
			details["mail_error"] = err.Error()
			log.Printf("pogod: no-fire notice (%s) could not be mailed: %v", rep.Summary(), err)
		} else {
			log.Printf("pogod: NO-FIRE — %s; notice %d/%d mailed to %s",
				rep.Summary(), notice, w.noFireMaxNotices, mailTo)
		}
	}

	// Emitted on EVERY unclean sample, mailed or not.
	w.emit(events.Event{EventType: "deploy_nofire", Agent: "pogod", Details: details})
}

// noFireSubject is the line that travels. It leads with the count of silent
// nights and carries the newest one, so a subject sitting in a filtered thread
// still changes when the outage grows — the constant-subject rule is what
// swallowed the five `pogo-deploy` REDs before this.
func noFireSubject(r staleness.NoFireReport) string {
	if !r.LogFound {
		return fmt.Sprintf("nightly deploy has NEVER run — no log at %s", r.LogPath)
	}
	if r.MissedTotal == 1 {
		return fmt.Sprintf("nightly deploy DID NOT RUN on %s — the job never started", r.Missed[0])
	}
	return fmt.Sprintf("nightly deploy DID NOT RUN on %d night(s), most recent %s — the job never started",
		r.MissedTotal, r.Missed[0])
}

// noFireBody explains what was measured, why no existing alarm could see it,
// and what the reader is expected to check — including the notice budget, so
// silence from here is never read as the condition clearing.
func noFireBody(r staleness.NoFireReport, notice, maxNotices int, sched staleness.DeploySchedule) string {
	var b strings.Builder

	if !r.LogFound {
		fmt.Fprintf(&b,
			"There is NO deploy log at %s. The nightly deploy has not written a single line on this host — not a failure, an absence.\n\n",
			r.LogPath)
	} else {
		fmt.Fprintf(&b,
			"The nightly deploy DID NOT START on %d night(s) that were due. This is not a failed deploy: there is no exit code, because there was no run.\n\n",
			r.MissedTotal)
	}

	b.WriteString("SILENT NIGHTS\n")
	for _, night := range r.Missed {
		fmt.Fprintf(&b, "  %s  no `pogo-deploy: start` line at all\n", night)
	}
	if r.Truncated {
		fmt.Fprintf(&b, "  ... %d more (enumeration clipped; the count above is exact)\n", r.MissedTotal-len(r.Missed))
	}
	if len(r.Missed) == 0 {
		b.WriteString("  (none enumerated — see the log-absence above)\n")
	}
	b.WriteString("\n")

	b.WriteString("MEASURED\n")
	fmt.Fprintf(&b, "  record            %s\n", r.LogPath)
	fmt.Fprintf(&b, "  expectation       a fire every night at %s local (+ retries)\n", fireList(sched))
	fmt.Fprintf(&b, "  last due night    %s\n", r.LastDueNight)
	if r.LogFound {
		fmt.Fprintf(&b, "  real fires in log %d", r.Fires)
		if r.DryRuns > 0 {
			fmt.Fprintf(&b, " (+%d dry run(s), which deploy nothing and do not satisfy a night)", r.DryRuns)
		}
		b.WriteString("\n")
	}
	if r.Horizon != "" {
		fmt.Fprintf(&b, "  log covers from   %s", r.Horizon)
		if r.EarliestJudged != "" {
			fmt.Fprintf(&b, " (oldest night judged: %s)", r.EarliestJudged)
		}
		b.WriteString("\n")
	}
	if r.HorizonLimited {
		b.WriteString("  NOTE              nights before that are NOT judged either way — this log does not cover\n" +
			"                    them, and a rotated-away night must not be reported as a silent one.\n")
	}
	if r.Unparsed > 0 {
		fmt.Fprintf(&b, "  UNREADABLE        %d start line(s) carried a timestamp this check could not parse, so they\n"+
			"                    could not be credited to any night.\n", r.Unparsed)
	}
	b.WriteString("\n")

	b.WriteString(
		"WHY NOTHING ELSE TOLD YOU\n" +
			"Every other deploy alarm is downstream of the job RUNNING: the RED fires on a nonzero rc, and a job that never starts has no rc. It writes no log line, sets no stamp, and sends no mail, so a silent night is indistinguishable from a healthy one — health is also silence. On 2026-08-01..08-04 that happened four nights in a row and nothing noticed. This check reads the absence directly, against the schedule, so it needs no run to have happened.\n\n")

	b.WriteString(
		"WHY, IS NOT DIAGNOSED HERE\n" +
			"From the log, all of these look identical and all have the same consequence — the fleet runs yesterday's code:\n" +
			"  - the host was powered off through the window (launchd replays a missed StartCalendarInterval on WAKE, but not across a power cycle);\n" +
			"  - the job was pended by the nondemand-spawn wedge (mg-50e0);\n" +
			"  - the LaunchAgent was unloaded, or its plist removed.\n" +
			"The four nights of 2026-08-01..08-04 were the FIRST of those — measured, see docs/investigations/deploy-nofire-nights-were-a-power-off-2026-08-07.md — but this check deliberately does not assume a cause, because it has to fire on all three.\n\n")

	b.WriteString(
		"WHAT TO CHECK\n" +
			"  grep 'pogo-deploy: start' " + r.LogPath + "        # the record this read\n" +
			"  pmset -g log | head -1                              # was the host even up? a boot line is a power cycle\n" +
			"  last reboot                                         # corroborates the above\n" +
			"  launchctl print gui/$UID/" + deployAgentLabel + "  # is the job still loaded?\n" +
			"Do NOT read `runs` from that launchctl output as evidence: it counts spawns on the CURRENT bootstrap and re-installing the plist resets it to 0, so it reads 0 both for a job that just ran and for one that never has (measured 0 on this box after seven fires).\n\n")

	b.WriteString(
		"THIS IS REPORT-ONLY. pogod did not kickstart the job, re-bootstrap the LaunchAgent, or power anything on, and has no seam through which it could. Two of the three causes above are not fixable from in here at all, and for the third a loop kickstarting a wedged job would hammer it rather than fix it.\n\n")

	fmt.Fprintf(&b,
		"NOTICE %d OF %d for this run of silent nights. The gap doubles after each one and then stops. ANOTHER silent night is a different condition and gets its own fresh notice, so an outage that keeps growing keeps mailing; an unchanged one goes quiet after notice %d while still being recorded as a `deploy_nofire` event on every sample. Silence from here is not the nightly starting again.\n",
		notice, maxNotices, maxNotices)

	return b.String()
}

// fireList renders the schedule's fire times for the notice.
func fireList(sched staleness.DeploySchedule) string {
	if len(sched.Hours) == 0 {
		return "an unknown time"
	}
	parts := make([]string, 0, len(sched.Hours))
	for _, h := range sched.Hours {
		parts = append(parts, fmt.Sprintf("%02d:%02d", h, sched.Minute))
	}
	return strings.Join(parts, "/")
}
