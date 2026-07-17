package agent

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// The orphaned-polecat surface: what resolves an AgentUnknown (mg-0b77).
//
// mg-13a3 gave pogod a second witness and stopped the lie: a registry-absent
// polecat whose persisted (pid, start_time) still matches a live process now
// resolves UNKNOWN, not GONE, so we no longer reap a living agent. That fixed
// the killing. It did not deal with the survivor.
//
// UNKNOWN was designed as a TRANSIENT. Its own comment says "we have no
// evidence either way" — a *yet*. For crew it resolves: the agent boots,
// registers, becomes ALIVE. For a polecat that outlived a pogod restart it
// NEVER resolves, because the registry is in-memory with no adopt path
// (mg-46a4, mg-61a0): absence never heals. And UNKNOWN is consumed exactly
// once in the whole tree — scheduler.GCStaleMailChecks tests `== AgentGone` —
// so every non-GONE state means only "keep the schedule". Nothing else looks.
//
// So the survivor sits in UNKNOWN forever: ALIVE, UNREACHABLE, holding a
// worktree and a claim, with a mail-check firing into a void. That is a
// resource leak plus permanent noise, and today nothing whatsoever acts on it.
//
// WHAT RESOLVES IT: A HUMAN. The machine cannot, and both mechanical
// candidates are wrong for reasons that are not close:
//
//   - ADOPT is impossible, and would relocate the lie. A polecat is driven
//     through a PTY whose MASTER fd lived in the address space of the pogod
//     that died; the master closed with it and the slave hung up. No syscall
//     binds a new master to an orphaned slave — the control channel is not
//     misplaced, it is destroyed. We could forge a *registry entry*, but an
//     entry asserts Alive() and feeds nudge and PolecatCount: it would claim
//     controllability we do not have. That is precisely the conflation this
//     ticket is about, moved into the registry where more code trusts it.
//     (RecordPolecatWitness's doc anticipates "a future adopt path"; the
//     witness would indeed be its input, but the witness is not the missing
//     piece. The PTY is, and it is gone.)
//
//   - KILL makes registry-absence authoritative for DESTRUCTION — the mirror
//     image of mg-de08/mg-8677, which is the one move this ticket forbids.
//     The survivor holds a worktree that may carry uncommitted work and a
//     claim that may be mid-flight; a survivor is also still able to commit,
//     push, and submit. "I cannot see it" has never licensed reaping here and
//     does not start licensing it because the verb changed.
//
//   - DOING NOTHING is today's behaviour, and it is the leak.
//
// What is left is to make the survivor OBSERVABLE, and let a human decide. The
// machine's honest role is to report a fact it can establish (this process is
// alive and we cannot control it) and to stop other subsystems claiming that
// fact away.
//
// WHY THIS IS NOT "A LOUDER LOG LINE". de08's corollary says an expected agent
// must stay noisy rather than go quiet, because noise is visible. It is not:
// the survivor's scheduler_fire_failed noise goes to events.log, and nobody
// reads events.log unless they are already suspicious — a louder err line into
// an unread log is the same silence with more characters. The ruling applied
// here is that LOUD MUST MEAN OBSERVABLE FROM OUTSIDE THE THING THAT FAILED.
// That is not invented for this ticket: sentineldrift.go already defines it
// operationally for exactly this reason ("pogo#76 was invisible across the
// WHOLE fleet because the only signal was a per-spawn log line — and nobody
// reads our logs"), and its answer is a durable event PLUS a mail to the
// coordinator, deduplicated per episode. This file follows that precedent
// rather than inventing a second notion of loud.
//
// The alert repeats on a cooldown rather than firing once per pogod lifetime.
// That is the point: the leak is permanent and only a human can end it, so the
// signal must persist until they do. It self-terminates — when the survivor is
// dealt with (killed, or allowed to finish), it stops being witnessed-alive
// and the mail stops. Noise that ends when the fault ends is the fault
// reporting itself, not spam.

// orphanAlertCooldown is how long a given survivor stays quiet after it has
// been reported. A survivor is PERMANENT — absence never heals — so unlike a
// transient this will re-fire indefinitely until a human resolves it. An hour
// matches sentineldrift's cooldown and is the interval at which "there is a
// leaked agent on this box" is worth restating.
const orphanAlertCooldown = time.Hour

// OrphanedPolecat is a polecat that is provably alive and provably beyond this
// pogod's control: its process matches a persisted witness, and no registry
// entry addresses it. Both halves are load-bearing. Witness-alive alone is an
// ordinary running polecat; registry-absent alone is an ordinary dead one.
type OrphanedPolecat struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	StartTime  time.Time `json:"start_time"`
	WorkItemID string    `json:"work_item_id,omitempty"`
}

// OrphanedPolecats returns every polecat this pogod can SEE is alive but
// cannot REACH — the population mg-13a3 made visible and that nothing yet
// acts on.
//
// The error is not decorative and must not be swallowed into an empty slice.
// An unreadable witness store means we do not know who is out there, which is
// a different fact from "nobody is out there" — the same distinction mg-76e5
// enforced one layer up (mail_check_count yields EMPTY, never 0). A caller
// that renders this error as zero survivors has rebuilt the defect.
func (r *Registry) OrphanedPolecats() ([]OrphanedPolecat, error) {
	alive, err := WitnessedAlivePolecats()
	if err != nil {
		return nil, err
	}
	if len(alive) == 0 {
		return nil, nil
	}

	// Every identity the registry currently addresses. A registry entry may be
	// matched by bare name or by event identity (cat-<name>), and witness
	// records are keyed by the bare Agent.Name — so normalise both sides
	// rather than assuming one spelling.
	known := map[string]struct{}{}
	if r != nil {
		for _, a := range r.List() {
			known[a.Name] = struct{}{}
			known[a.EventAgent()] = struct{}{}
		}
	}

	var out []OrphanedPolecat
	for _, w := range alive {
		if _, ok := known[w.Name]; ok {
			continue
		}
		if _, ok := known["cat-"+w.Name]; ok {
			continue
		}
		out = append(out, OrphanedPolecat{
			Name:       w.Name,
			PID:        w.PID,
			StartTime:  w.StartTime,
			WorkItemID: w.WorkItemID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// orphanReporter dedupes the survivor alert so a permanent leak produces a
// periodic signal rather than one per heartbeat tick. Keyed by polecat name.
type orphanReporter struct {
	mu        sync.Mutex
	cooldown  time.Duration
	now       func() time.Time
	alert     func(OrphanedPolecat)
	lastAlert map[string]time.Time
}

func newOrphanReporter() *orphanReporter {
	return &orphanReporter{
		cooldown:  orphanAlertCooldown,
		now:       time.Now,
		alert:     defaultOrphanAlert,
		lastAlert: map[string]time.Time{},
	}
}

// report fires the alert for each survivor outside its cooldown, and returns
// how many it fired. Alerts are invoked outside the lock: they do I/O (an event
// append and a mail send).
func (o *orphanReporter) report(orphans []OrphanedPolecat) int {
	o.mu.Lock()
	now := o.now()
	var fire []OrphanedPolecat
	for _, p := range orphans {
		last, seen := o.lastAlert[p.Name]
		if !seen || now.Sub(last) >= o.cooldown {
			o.lastAlert[p.Name] = now
			fire = append(fire, p)
		}
	}
	// Forget survivors that are no longer orphaned, so a name that comes back
	// later is reported afresh rather than silenced by a stale cooldown.
	live := make(map[string]struct{}, len(orphans))
	for _, p := range orphans {
		live[p.Name] = struct{}{}
	}
	for name := range o.lastAlert {
		if _, ok := live[name]; !ok {
			delete(o.lastAlert, name)
		}
	}
	o.mu.Unlock()

	for _, p := range fire {
		o.alert(p)
	}
	return len(fire)
}

// fleetOrphans is the process-global reporter. One pogod per host, so one
// instance is the whole fleet's view.
var fleetOrphans = newOrphanReporter()

// ReportOrphanedPolecats sweeps for survivors and surfaces any it finds. pogod
// calls this from its heartbeat tick. Returns the number of survivors found
// (not the number alerted — the cooldown suppresses repeats, and a caller
// asking "is anything leaked right now?" wants the population, not the noise).
//
// A witness read error is surfaced and reported as -1 rather than 0: we do not
// know, and this function will not be the one that says "none" when it means
// "cannot see".
func (r *Registry) ReportOrphanedPolecats() int {
	orphans, err := r.OrphanedPolecats()
	if err != nil {
		log.Printf("orphan: cannot enumerate surviving polecats (%v) — this is NOT a report of zero (mg-0b77)", err)
		return -1
	}
	fleetOrphans.report(orphans)
	return len(orphans)
}

// defaultOrphanAlert is the production alert sink: a durable event on the spine
// plus a mail to the coordinator, mirroring defaultDriftAlert. The event is for
// mg and the digest; the mail is the half that is observable from outside the
// subsystem that failed.
func defaultOrphanAlert(p OrphanedPolecat) {
	events.Emit(context.Background(), events.Event{
		EventType: "polecat_orphaned",
		Agent:     "pogod",
		Details: map[string]any{
			"polecat":      p.Name,
			"pid":          p.PID,
			"work_item_id": p.WorkItemID,
			"start_time":   p.StartTime.Format(time.RFC3339),
		},
	})
	mailOrphanAlert(p)
}

// mailOrphanAlert sends the LOUD half. Best-effort and deliberately so: if mg
// is not on PATH or the inbox does not exist, the polecat_orphaned event is
// already on the spine and a leaked agent must not take the daemon down with
// it. Mirrors mailDriftAlert's shell-out posture.
//
// The mail goes to the coordinator, not to `human`: resolving this needs
// someone who can look up the work item, decide whether the survivor's worktree
// holds anything worth keeping, and then unclaim/kill/let-it-finish. That is
// the mayor's job, and the mayor escalates to a human when it is not.
//
// WHY THE BODY SENDS THE READER TO `pogo agent witness` BEFORE KILLING
// (mg-da48). This mail's claim is true when written and decays immediately:
// pids are recycled, and the moment the survivor exits its pid is free for an
// unrelated process. Everything about the delivery channel widens that window
// — the alert repeats hourly until resolved, mail sits unread for an unbounded
// time, and pogod cannot recall a mail it already sent. So a bare `kill <pid>`
// in this body is an instruction that is only safe in the second it was
// written, delivered by a channel that guarantees it will be read later. That
// is stray-pkill-killed-the-fleet with the daemon's authority behind it, aimed
// by a decayed record.
//
// The cruel part is that the protection already exists and does not reach the
// one consumer told to run `kill`. witnessVerdict matches (pid, start_time), so
// the DETECTOR cannot be fooled by a recycled pid — it re-probes and resolves
// GONE. The reader gets a pid and a prose paragraph. The fix is not to weaken
// the alert (real orphans are an unsolved problem and this is their only
// signal — mg-0b77, mg-46a4, mg-61a0; the noise was the bug, not the alarm) but
// to hand the reader the same instrument the detector uses. `pogo agent
// witness` IS witnessVerdict reached over a process boundary — one definition
// of "our process is alive", not a second one written in prose — so the body
// names it and states the stale case as the DEFAULT reading, because by the
// time this is read that is the likelier one.
func mailOrphanAlert(p OrphanedPolecat) {
	coordinator := CoordinatorName()
	work := p.WorkItemID
	if work == "" {
		work = "(unknown — no work item recorded in its witness)"
	}

	// The one runnable line the reader is offered, gated on the witness so the
	// kill cannot outrun the fact that justifies it. Built once and used for
	// both resolutions below: two spellings of the same command are two things
	// to keep right, and the salvage path and the discard path differ in what
	// the reader does BEFORE running it, not in what they run.
	//
	// The unclaim is conditional because `work` is PROSE when the witness has no
	// work item — the literal string this alert put in front of the mayor on
	// 2026-07-17 was `kill 438 && mg unclaim (unknown — no work item recorded in
	// its witness)`, which is not a command, it is a shell syntax error wearing
	// one. Handing out an unrunnable line teaches the reader to edit the line
	// before running it, and the first thing an editing reader drops is the part
	// they did not understand — the grep. So: if there is nothing to unclaim,
	// say so in a comment and keep the line paste-clean.
	unclaim := fmt.Sprintf(" && mg unclaim %s", p.WorkItemID)
	if p.WorkItemID == "" {
		unclaim = "   # no work item in its witness — check `mg list` for an orphaned claim"
	}
	action := fmt.Sprintf("pogo agent witness --json | grep -q '%s' && kill %d%s",
		WitnessAliveGrep(p.Name, p.PID), p.PID, unclaim)
	subject := fmt.Sprintf("[orphaned-polecat] %s is alive on pid %d but unreachable", p.Name, p.PID)
	body := fmt.Sprintf(
		"A polecat outlived the pogod that spawned it and is now beyond this daemon's control.\n\n"+
			"Polecat:    %s\n"+
			"PID:        %d\n"+
			"Started:    %s\n"+
			"Work item:  %s\n\n"+
			"WHAT THIS MEANS. Its process is alive — matched on pid AND start time, so this is\n"+
			"our polecat and not a recycled pid (mg-13a3). But the registry is in-memory with no\n"+
			"adopt path, so this pogod has no entry for it and never will: absence never heals\n"+
			"(mg-46a4, mg-61a0). It holds a worktree and a claim, and its mail-check is firing\n"+
			"into a void — its PTY died with the pogod that owned it, so it cannot be nudged or\n"+
			"mailed. It is NOT being reaped (that would be mg-de08 again) and it is NOT drainable:\n"+
			"a redeploy drain will quiesce the registry to zero around it and leave it running.\n\n"+
			"WHY YOU AND NOT THE DAEMON. The daemon cannot resolve this. Re-attaching is not\n"+
			"possible (the PTY master died with its pogod; nothing can bind a new one to the\n"+
			"orphaned slave), and killing on the strength of a missing registry entry is the one\n"+
			"move that is definitely wrong. So it needs a decision that weighs what is in the\n"+
			"worktree, which is yours.\n\n"+
			"BEFORE YOU KILL ANYTHING, RE-CONFIRM IDENTITY. THIS IS NOT OPTIONAL.\n"+
			"This mail was true when it was WRITTEN, and it does not know when you are reading\n"+
			"it: it repeats about every %s until resolved, and mail waits. A pid is NOT an\n"+
			"identity — only (pid, start_time) is. If %s has exited since this was sent, pid %d\n"+
			"was freed and the kernel is free to hand it to an unrelated process, which\n"+
			"`kill %d` would then kill. Assume that has happened until you have checked:\n\n"+
			"      pogo agent witness --json\n\n"+
			"That re-probes (pid, start_time) NOW with the same verdict the detector used to\n"+
			"send this — it is the instrument, not a second opinion. Read its output:\n\n"+
			"  - %s is NOT in `alive` — THIS ALERT IS STALE. The polecat is already gone, there\n"+
			"    is nothing to resolve, and pid %d is no longer ours. Run none of the kills\n"+
			"    below. If this keeps arriving with the same pid, the store is lying and THAT\n"+
			"    is the bug — report it rather than killing anything.\n"+
			"  - %s IS in `alive` on pid %d — the record still matches a live process started\n"+
			"    %s. The survivor is real and the actions below are safe TO RUN NOW. Re-check\n"+
			"    if you put this down and come back to it.\n\n"+
			"TO RESOLVE, once the witness has confirmed it is still alive, pick one and act:\n"+
			"  - Let it finish, if it can still commit and submit unaided. It will exit on its own\n"+
			"    and this alert stops.\n"+
			"  - Salvage then stop it: check its worktree for uncommitted work, then\n"+
			"      %s\n"+
			"  - Stop it if the work is worthless or reproducible:\n"+
			"      %s\n\n"+
			"The grep is the re-check wired INTO the command so it cannot be skipped: if the\n"+
			"polecat is no longer witnessed alive, grep fails and the kill never runs. Do not\n"+
			"drop it for a bare `kill %d` — a pid pasted out of a mail of unknown age is how an\n"+
			"unrelated process dies (mg-da48).\n\n"+
			"This alert repeats about every %s until the process is gone, because nothing else\n"+
			"will tell you: the only other signal is scheduler_fire_failed in events.log, which\n"+
			"nobody reads unless already suspicious (mg-0b77).\n",
		p.Name, p.PID, p.StartTime.Format(time.RFC3339), work,
		orphanAlertCooldown, p.Name, p.PID, p.PID,
		p.Name, p.PID,
		p.Name, p.PID, p.StartTime.Format(time.RFC3339),
		action, action,
		p.PID, orphanAlertCooldown)

	cmd := exec.Command("mg", "mail", "send", coordinator,
		"--from", "pogod", "--subject", subject, "--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("orphan: cannot mail %s about orphaned polecat %s (%v): %s",
			coordinator, p.Name, err, string(out))
	}
}
