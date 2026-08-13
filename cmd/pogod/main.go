////////////////////////////////////////////////////////////////////////////////
////////// This will eventually be the code that is in `pogod`        //////////
////////////////////////////////////////////////////////////////////////////////

package main

import _ "net/http/pprof"
import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightlyone/lockfile"
	"golang.org/x/net/netutil"

	"github.com/drellem2/pogo/internal/absentwatch"
	"github.com/drellem2/pogo/internal/ackwatch"
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/credexpiry"
	"github.com/drellem2/pogo/internal/deafwatch"
	"github.com/drellem2/pogo/internal/driftwatch"
	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/firstturn"
	"github.com/drellem2/pogo/internal/ghintake"
	"github.com/drellem2/pogo/internal/ghteardown"
	"github.com/drellem2/pogo/internal/ghtoken"
	"github.com/drellem2/pogo/internal/gitceiling"
	"github.com/drellem2/pogo/internal/health"
	"github.com/drellem2/pogo/internal/heartbeat"
	"github.com/drellem2/pogo/internal/pathenv"
	"github.com/drellem2/pogo/internal/platform/sleep"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/providers"
	"github.com/drellem2/pogo/internal/reaper"
	"github.com/drellem2/pogo/internal/reconcile"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/reviewdecl"
	"github.com/drellem2/pogo/internal/scheduler"
	"github.com/drellem2/pogo/internal/search"
	"github.com/drellem2/pogo/internal/server"
	"github.com/drellem2/pogo/internal/service"
	"github.com/drellem2/pogo/internal/staleness"
	"github.com/drellem2/pogo/internal/stallwatch"
	"github.com/drellem2/pogo/internal/synthwatch"
	"github.com/drellem2/pogo/internal/turnlog"
	"github.com/drellem2/pogo/internal/turnwatch"
	"github.com/drellem2/pogo/internal/wedgewatch"
	"github.com/drellem2/pogo/internal/workitem"

	pogoPlugin "github.com/drellem2/pogo/pkg/plugin"
)

var agentRegistry *agent.Registry

// conditions annunciates the pogod conditions enumerated as rows A2..A15 in
// docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md — the
// ones with an actor who could act and, until mg-342d, no channel to reach them.
//
// It is a package-level var for the same reason agentRegistry is: the decision
// points are spread from main's first statement (log rotation) to the heartbeat
// tick (own-heartbeat write) to the git GC timer, and threading an annunciator
// through all of them would mean changing every signature on the way. Every
// method is nil-receiver-safe, so a decision point reached before main arms it —
// or in a test that does not care — is a no-op rather than a panic.
var conditions *conditionAnnunciator

var mergeQueue *refinery.Refinery
var sched *scheduler.Scheduler
var srv *server.Server
var startTime time.Time

var bindFlag = flag.String("bind", "", "address to bind the server to (default: 127.0.0.1)")
var portFlag = flag.Int("port", 0, "port to listen on (default: 10000)")

// maxHTTPConns caps concurrent HTTP connections so a client leak can't
// exhaust daemon file descriptors. Generous for a localhost daemon whose
// normal load is a handful of CLI and agent clients.
const maxHTTPConns = 256

// registryLiveness implements scheduler.AgentLiveness against the agent
// registry so the scheduler can garbage-collect mail-check-* schedules whose
// target agent is gone (gh drellem2/macguffin #15). A schedule addresses an
// agent by its event identity (cat-/crew-<name>) or, for some crew schedules,
// its bare name, so we match on both.
//
// It answers on THREE sources, consulted in a STRICT ORDER (mg-8677, mg-13a3):
//
//  1. the registry — evidence about an actual process. A registered agent that
//     is running, or one held for an imminent respawn, is ALIVE. A registered
//     agent that has terminally exited with no respawn coming is GONE.
//  2. the persisted process witness (agent.AgentWitness) — evidence about an
//     actual process that OUTLIVES the pogod that observed it. Consulted ONLY
//     when step 1 yielded no evidence at all, i.e. the registry holds no entry.
//  3. the desired state on disk (agent.DesiredStateFor) — an auto_start, not
//     parked crew prompt. Consulted ONLY when steps 1 and 2 both yielded no
//     evidence at all.
//
// The order is the whole design, and every step of it was learned from a bug.
// Steps 1 and 2 are both EVIDENCE (something looked at a process); step 3 is
// EXPECTATION (something read a config file about what ought to be running).
// Evidence beats expectation, always; that is the mg-8677 rule and adding a
// second evidence source does not weaken it — the witness is consulted after
// the registry and before the desired state precisely because it is the same
// KIND of thing as the registry, and a strictly better thing than a prompt.
//
// The registry alone is NOT sufficient, and believing it was is what caused
// mg-de08. A freshly-started pogod has an EMPTY registry: its scheduler loads
// the fleet's persisted mail-check-* from disk and starts ticking BEFORE
// AutoStartAgents() spawns the crew, so a registry-only answer reports every
// crew agent gone and reaps the entire fleet's mail loop seconds before the
// crew boot into a world where their schedules no longer exist. The old
// RestartOnCrash guard could not save them: it reads a flag off a registry
// entry that does not exist yet. So an ABSENT entry means UNKNOWN, never GONE.
//
// But the desired state must not be allowed to answer a question the registry
// already answered, and letting it was mg-8677: a registered, terminally-exited,
// restart_on_crash=false agent fell through to DesiredStateFor and came back
// EXPECTED on the strength of auto_start=true, keeping its mail-check alive
// forever and accumulating unbounded scheduler_fire_failed noise. The registry
// LOOKED and found a corpse; auto_start does not resurrect it.
//
// And the desired state cannot answer for an agent that never had one, which
// was mg-13a3. A polecat has no prompt and no auto_start, so before the
// witness existed BOTH sources came back absent for every polecat that
// survived a pogod restart, and the default arm called that death:
//
//	registry: no entry        (absence)
//	desired state: not wanted (absence)
//	=> GONE => reap the mail-check
//
// Two absences are not evidence of anything. The comment that shipped with
// mg-8677 asserted otherwise — it called "neither registered nor in the
// desired state" a form of positive evidence, which defined an absence into a
// presence and licensed exactly the reap mg-de08 forbade. mg-61a0 then
// reproduced the consequence: a live polecat, unregistered after a restart,
// lost its mail-check from memory and disk and went permanently dark. The
// registry is in-memory with no adopt path, so absence never heals on its own.
// The witness is what heals it — a polecat's own (pid, start_time), persisted,
// so a successor pogod has something to LOOK at.
//
// GONE therefore means positive evidence of death, with no disjunct that is
// merely an absence wearing the word "evidence":
//
//   - a corpse in the registry (step 1: we watched it exit), or
//   - an agent with no SURVIVING evidence of life and no desired state:
//     nothing on this machine claims this agent should be running, and nothing
//     can see a process for it. Either it was never witnessed, or its witness
//     names a process that is provably not ours — the pid answers nothing, or
//     it answers but started at a different time, so the pid was recycled and
//     our process is long dead (step 2).
//
// A dead witness is therefore a necessary half of that second disjunct and not
// a sufficient one on its own (mg-f9e8). It retires a PROCESS's claim to life;
// it does not answer whether the agent should be running, and for a polecat the
// distinction is invisible because a polecat is never expected. Step 3 decides,
// and for everything that existed before crew were witnessed it decides the same
// way it always did.
//
// Across a pogod restart the unregistered population splits on EVIDENCE, not
// on the shape of its config: everything this fleet started and that is still
// running is witnessed (UNKNOWN → keep), auto_start crew are additionally
// EXPECTED → keep, and an agent whose witness was dropped at exit or fails the
// identity match is GONE → reap.
//
// That split used to be spelled "crew are auto_start, live polecats are
// witnessed", which quietly excluded a whole supported population: an
// auto_start = false crew agent is not a polecat, so it was never witnessed,
// and not expected, so it was reaped WHILE ALIVE on the strength of two
// absences — the prohibition above, applied to the one population mg-de08's fix
// did not reach. The agent then goes deaf with no exit, so neither an auto_start
// respawn nor the suppression page fires. `diagnose` will name it a DEAF
// SURVIVOR (internal/agent/api.go) if someone types that agent's name, but
// nothing announces it: deafwatch iterates the REGISTRY
// (Registry.MailLoopReport, via deafwatch.RegistrySource), and this population
// is registry-absent by construction — that absence is the first of the two the
// classifier reasoned from. A detector's existence is not its coverage; the
// question is which set it iterates. The absent-while-alive state itself is not
// a hypothesis — docs/investigations/registry-absent-while-alive-2026-07-17.md
// reproduced it end-to-end on this host and watched the sweep delete a live
// agent's mail-check from memory and disk with nothing logged.
// Since mg-f9e8 every agent pogod starts is witnessed, crew included, so the
// population that used to arrive here with nothing to show is carrying evidence.
// What did NOT change is the reap of an agent pogod never started: it is still
// unwitnessed, still not expected, and still reaped, which is orphan-nudge
// prevention and is pinned by the `lurker` case in mailcheck_gc_restart_test.go.
type registryLiveness struct{ reg *agent.Registry }

func (l registryLiveness) AgentState(scheduleAgent string) scheduler.AgentState {
	if l.reg != nil {
		for _, a := range l.reg.List() {
			if a.Name == scheduleAgent || a.EventAgent() == scheduleAgent {
				if a.Alive() || a.RestartOnCrash {
					return scheduler.AgentAlive
				}
				// A registered, terminally-exited, no-respawn agent. RETURN
				// HERE — do not fall through to the desired state. This is the
				// precedence rule, and it is not a micro-optimisation to be
				// simplified away by hoisting the DesiredStateFor call out:
				//
				//   Consult desired state ONLY when the registry yields NO
				//   evidence. Evidence beats expectation, always. Never let
				//   auto_start override a corpse.
				//
				// This IS the positive evidence of death that mg-de08 requires
				// before a reap: the registry looked, and found a body. Asking
				// the prompt "but is it supposed to be running?" after that can
				// only produce a wrong answer — an auto_start=true +
				// restart_on_crash=false agent would be called EXPECTED and its
				// mail-check would fire at a corpse forever (mg-8677).
				//
				// Reaping is right here, and does not violate mg-de08's "stop
				// deleting the alarm" corollary: that holds only where the
				// schedule is the ONLY signal. It is not — the agent already
				// diagnoses "exited" (internal/agent/api.go), which is more
				// precise than fire-failure noise. The schedule adds no
				// information here, only unbounded noise.
				//
				// The eager onExit reap (RemoveMailChecksForAgent) usually gets
				// here first, and that is NOT a reason for this sweep to defer
				// to it: this sweep is the BACKSTOP for what onExit cannot do,
				// including an eager reap that failed to persist and rolled
				// back. A backstop that delegates to the thing it backstops is
				// not a backstop.
				//
				// Humans and agents both reason from desired state by default —
				// which is exactly why the code must not.
				return scheduler.AgentGone
			}
		}
	}
	// The registry holds no entry for this agent: no evidence either way, NOT
	// evidence of death (mg-de08). Before asking what SHOULD be running, ask
	// whether we have any surviving evidence about what IS — a polecat's
	// persisted (pid, start_time) outlives the pogod that recorded it, so a
	// restarted pogod can still look at the process itself (mg-13a3).
	switch v := agent.AgentWitness(scheduleAgent); v {
	case agent.WitnessAlive:
		// OUR process — matched on pid AND start time — is running. The
		// registry has simply forgotten it (in-memory, no adopt path across a
		// restart). This is the case that used to reap a live polecat.
		//
		// UNKNOWN, not ALIVE: the honest claim is "this process is running",
		// which is not the same as "this agent is healthy and reachable". Both
		// keep the schedule, and UNKNOWN keeps us from asserting more than we
		// checked.
		return scheduler.AgentUnknown
	case agent.WitnessDead:
		// Positive evidence that the process we recorded is gone: the pid holds
		// nothing, or it holds a process that started at a different time and is
		// therefore not ours.
		//
		// FALL THROUGH TO THE DESIRED STATE rather than returning GONE here, and
		// the difference only became visible when crew joined this store
		// (mg-f9e8). For a polecat nothing changes: a polecat has no prompt, so
		// the desired state says "not expected" and the answer below is still
		// GONE — mg-8677's recycled pid is still reaped, by the same two steps in
		// the same order. But an auto_start = true CREW agent can now hold a dead
		// witness, and the state it describes is ordinary: this fleet restarts
		// nightly, pogod's death takes the crew down with it, and the successor's
		// witness for each of them is a corpse until AutoStartAgents respawns it.
		// Returning GONE on that would reap the entire crew's mail loop whenever
		// the auto-start sweep is late or fails for one agent — mg-de08 exactly,
		// re-entered through the fix for mg-f9e8. The GC gate usually hides it
		// (it waits for the sweep plus a settle window), but "usually hidden" is
		// what mg-de08 was too.
		//
		// The rule this keeps is the one mg-de08 states, not a weaker one. A dead
		// witness is evidence about a PROCESS, and it retires that process's
		// claim to life — it is not evidence that the agent should not be running.
		// The registry arm above is the one that answers "we watched it exit and
		// nothing is bringing it back", and it still returns without asking.
		log.Printf("scheduler: %s's witness names a process that is gone; asking the desired state "+
			"whether it should be running before reaping its mail-check", scheduleAgent)
	case agent.WitnessUnreadable:
		// A witness exists and something is alive on its pid, but we could not
		// confirm the process is ours. We must not call an unmeasurable thing
		// dead.
		log.Printf("scheduler: cannot confirm the process behind %s's witness is ours; NOT reaping its mail-check", scheduleAgent)
		return scheduler.AgentUnknown
	case agent.WitnessNoRecord:
		// No witness at all: no pogod on this box has a record of starting this
		// agent and still having it running. Either its witness was dropped when
		// this or an earlier pogod watched it exit, or nothing ever started it —
		// a crew prompt that is only ever run by hand, or an agent that does not
		// exist. Fall through to the desired state.
		//
		// It used to read "crew are never witnessed; their auto_start is their
		// second witness", and that sentence was load-bearing and only true for
		// auto_start = true (mg-f9e8). The prompt-side witness IS auto_start, so
		// an auto_start = false crew agent had no process witness (not a polecat)
		// and no desired-state witness (not expected), and this arm handed the
		// pair of absences to the default below, which called them death — while
		// the agent was running. Crew are witnessed now, so a LIVE one that this
		// fleet started never reaches here.
	}

	// Neither the registry nor the witness has anything to say. Only now may
	// the desired state speak — ask whether this agent is supposed to be
	// running before reaping anything.
	expected, err := agent.DesiredStateFor(scheduleAgent)
	switch {
	case err != nil:
		// A prompt exists for this agent but we could not read it. We know it
		// was configured and know nothing else — the one thing we must not do
		// is call that death.
		log.Printf("scheduler: cannot classify %s against the desired state (%v); NOT reaping its mail-check", scheduleAgent, err)
		return scheduler.AgentUnknown
	case expected:
		return scheduler.AgentExpected
	default:
		return scheduler.AgentGone
	}
}

// schedulePauser implements agent.SchedulePauser against the scheduler so
// park can remove an agent's schedules (recording them in the park file for
// restore) and wake can re-add them (mg-41e1). Entries travel as raw JSON
// because the agent package cannot import the scheduler package (the
// scheduler already imports agent).
type schedulePauser struct{ sched *scheduler.Scheduler }

func (p schedulePauser) PauseForAgent(aliases ...string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, alias := range aliases {
		for _, e := range p.sched.List(alias) {
			if _, err := p.sched.Remove(e.Agent, e.ID); err != nil {
				return out, err
			}
			data, err := json.Marshal(e)
			if err != nil {
				return out, err
			}
			out = append(out, data)
		}
	}
	return out, nil
}

func (p schedulePauser) RestoreForAgent(entries []json.RawMessage) (int, error) {
	restored := 0
	var firstErr error
	now := time.Now()
	for _, raw := range entries {
		var e scheduler.Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Recompute the next fire for recurring entries — the recorded
		// NextFire likely came due during the park and must not replay as a
		// missed fire. One-shots keep their fire time on purpose: a gate-lift
		// reminder that came due while parked should fire once on wake.
		if !e.OneShot {
			e.NextFire = time.Time{}
		}
		if _, err := p.sched.Add(e, now); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		restored++
	}
	return restored, firstErr
}

// mailCheckRegistrar implements agent.MailCheckRegistrar against the scheduler
// so spawn-polecat can auto-register a polecat's mail-check loop at spawn time
// (mg-e633). The entry is addressed to the polecat's bare registry name — the
// identity PogodDeliverer.Get resolves for PTY nudge delivery and the reap path
// (RemoveMailChecksForAgent) matches on exit — with a mail-check-<id> schedule
// id so the scheduler's stale-entry sweep leaves it alone (mg-8e5d). Replay
// policy "once" and nudge delivery mirror the crew-agent mail-check convention.
type mailCheckRegistrar struct {
	sched *scheduler.Scheduler
	// escalate, when set, nudges the mayor that a live polecat was left with no
	// mail-check reachability channel after verify+retry both failed. nil
	// disables escalation (tests). Called ONLY on the persistent post-retry
	// path — never for the benign startup nil-registrar (mg-6fe0).
	escalate func(agentName, scheduleID string)
}

// RegisterMailCheck adds the polecat's mail-check schedule, then VERIFIES it
// actually persisted and retries ONCE if not. A mail-check loop is a polecat's
// primary reachability channel, so "best-effort" is the wrong contract:
// Scheduler.Add's persist is a disk write that can transiently fail, and a
// silent drop leaves a live worker unreachable. The verify+retry recovers that
// transient persist-IO suspect; on a persistent failure it escalates to the
// mayor (a live polecat going dark) and returns the error so the agent layer
// records schedule_register_failed telemetry. It CANNOT recover a nil
// registrar — that path never reaches here, it is handled a layer up (mg-6fe0).
func (m mailCheckRegistrar) RegisterMailCheck(agentName, workItemID, cron, message string) error {
	if m.sched == nil {
		return nil
	}
	scheduleID := scheduler.MailCheckIDPrefix + workItemID
	entry := scheduler.Entry{
		Agent:        agentName,
		ID:           scheduleID,
		Kind:         scheduler.KindMailCheck,
		Cron:         cron,
		ReplayPolicy: scheduler.ReplayOnce,
		Delivery:     scheduler.DeliveryNudge,
		Message:      message,
	}

	err := m.addAndVerify(entry, agentName, scheduleID)
	if err == nil {
		return nil
	}
	// Retry once — recovers a transient persist-IO failure (Add rolls its own
	// memory state back on a persist error, so the retry re-adds cleanly).
	if err = m.addAndVerify(entry, agentName, scheduleID); err == nil {
		return nil
	}

	// Persistent after retry: a live polecat with no reachability channel.
	// Escalate to the mayor so a human/coordinator can intervene.
	if m.escalate != nil {
		m.escalate(agentName, scheduleID)
	}
	return err
}

// addAndVerify performs one Add followed by a Get to confirm the entry is
// actually present afterward (Add reports persist errors, but a defensive Get
// also catches a lost write / concurrent reap). Returns nil only when the entry
// is verified present.
func (m mailCheckRegistrar) addAndVerify(entry scheduler.Entry, agentName, scheduleID string) error {
	if _, err := m.sched.Add(entry, time.Now()); err != nil {
		return err
	}
	if _, ok := m.sched.Get(agentName, scheduleID); !ok {
		return fmt.Errorf("mail-check schedule %s for %s absent after Add", scheduleID, agentName)
	}
	return nil
}

// mgMailboxRegistrar implements agent.MailboxRegistrar against the `mg` CLI so
// spawn-polecat provisions a polecat's mailboxes at spawn time (mg-7dc1).
//
// It is wired unconditionally, NOT inside the scheduler-loaded branch that gates
// mailCheckRegistrar. The two are independent: the mail-check loop needs a
// scheduler, whereas addressability needs only macguffin. A daemon whose
// scheduler failed to load still spawns polecats that people mail by hand, and
// on that daemon a mailbox is the only reachability they have left.
type mgMailboxRegistrar struct{}

func (mgMailboxRegistrar) RegisterMailbox(name string) error {
	return client.RegisterMGMailbox(name)
}

// scheduleRegisterFailureReporter implements agent.ScheduleRegisterFailureReporter
// by writing schedule_register_failed telemetry to the scheduler's own-root
// events.log (logPath). It is wired EVEN WHEN scheduler.New fails — its whole
// reason to exist is to make the startup nil-registrar drop loud — so it carries
// the resolved own-root path directly rather than a *Scheduler (which may not
// exist). Event-only: escalation to the mayor is the registrar adapter's job on
// the persistent post-retry path, not this reporter's (mg-6fe0).
type scheduleRegisterFailureReporter struct{ logPath string }

func (r scheduleRegisterFailureReporter) ReportScheduleRegisterFailed(agentName, scheduleKey, reason string) {
	scheduler.EmitScheduleRegisterFailedTo(r.logPath, agentName, scheduler.MailCheckIDPrefix+scheduleKey, reason)
}

// schedulerStallWindows implements agent.StallScheduleProvider against the
// scheduler so diagnose can tell a cron-driven agent's by-design between-cron
// idle from a genuine wedge (mg-5b23). For each recurring cron schedule
// addressed to the agent it reports the schedule's last/next firing and the
// interval between firings; one-shot and unparseable schedules contribute
// nothing.
type schedulerStallWindows struct{ sched *scheduler.Scheduler }

func (p schedulerStallWindows) CronWindowsForAgent(agentIdentity string) []agent.CronWindow {
	if p.sched == nil {
		return nil
	}
	now := time.Now()
	// A schedule may address an agent by its event identity (crew-/cat-<name>)
	// or, for some crew schedules, its bare name — mirror registryLiveness and
	// match either. List filters on exact Agent, so query both forms.
	aliases := []string{agentIdentity}
	if bare := strings.TrimPrefix(strings.TrimPrefix(agentIdentity, "crew-"), "cat-"); bare != agentIdentity {
		aliases = append(aliases, bare)
	}
	var windows []agent.CronWindow
	for _, alias := range aliases {
		for _, e := range p.sched.List(alias) {
			if e.OneShot {
				continue
			}
			interval := e.CronInterval(now)
			if interval <= 0 {
				continue
			}
			windows = append(windows, agent.CronWindow{
				LastFire: e.LastFire,
				NextFire: e.NextFire,
				Interval: interval,
			})
		}
	}
	return windows
}

// schedulerMailChecks implements agent.MailCheckProvider against the scheduler
// so diagnose can report an EXPECTED agent that has no mail delivery path
// (mg-de08). It is the inverse consumer of schedulerStallWindows: that one
// reads schedules to explain away idle, this one reads them to condemn an
// agent nothing can wake.
type schedulerMailChecks struct{ sched *scheduler.Scheduler }

func (p schedulerMailChecks) HasMailCheck(agentIdentity string) bool {
	if p.sched == nil {
		return false
	}
	// Match the same alias forms as schedulerStallWindows and registryLiveness:
	// a mail-check may be registered under the event identity or the bare name.
	aliases := []string{agentIdentity}
	if bare := strings.TrimPrefix(strings.TrimPrefix(agentIdentity, "crew-"), "cat-"); bare != agentIdentity {
		aliases = append(aliases, bare)
	}
	for _, alias := range aliases {
		for _, e := range p.sched.List(alias) {
			// Structural, not lexical (mg-fa53): a mail-check is identified by its
			// Kind, so this stays correct even if the id-naming convention ever
			// drifts. List returns entries loaded through applyDefaults, so legacy
			// no-kind entries already have KindMailCheck inferred.
			if e.Kind == scheduler.KindMailCheck {
				return true
			}
		}
	}
	return false
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /health")
	// health.LivenessBody, not a literal: doctor's loopback-resolution check
	// tells pogod apart from an impersonator by this exact string, and a
	// second copy here is a copy that can drift while the check keeps
	// reporting pass (drellem2/pogo#110).
	fmt.Fprint(w, health.LivenessBody)
}

// versionInfo is the JSON body of GET /version. It reports the RUNNING
// process's build identity — the axis bin/pogo-self-deploy needs for three-way
// drift detection (running vs installed binary vs main HEAD) per mg-6afa. The
// running revision must be self-reported: reading `go version -m ~/go/bin/pogod`
// gives the INSTALLED binary's revision, which diverges from the running one
// the instant `go install` rewrites that file underneath a live daemon.
type versionInfo struct {
	Revision  string `json:"revision"`   // vcs.revision embedded at build
	Time      string `json:"time"`       // vcs.time
	Modified  bool   `json:"modified"`   // vcs.modified (dirty tree at build)
	GoVersion string `json:"go_version"` // toolchain that built it
	StartTime string `json:"start_time"` // RFC3339 process start
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	info := versionInfo{StartTime: startTime.Format(time.RFC3339)}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Revision = s.Value
			case "vcs.time":
				info.Time = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func healthFull(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	// Pogod health
	mode := "full"
	if srv != nil {
		mode = srv.Mode().String()
	}
	pogodHealth := health.Pogod{
		Status: "ok",
		Uptime: time.Since(startTime).Truncate(time.Second).String(),
		Mode:   mode,
	}

	// Agents health
	var agentsHealth health.Agents
	if agentRegistry != nil {
		agents := agentRegistry.List()
		agentsHealth.Total = len(agents)
		agentsHealth.Details = make([]health.AgentDetail, len(agents))
		for i, a := range agents {
			info := agent.ExportInfo(a)
			detail := health.AgentDetail{
				Name:     info.Name,
				Status:   string(info.Status),
				Restarts: info.RestartCount,
				Uptime:   info.Uptime,
				ExitCode: info.ExitCode,
			}
			agentsHealth.Details[i] = detail
			switch info.Status {
			case "running":
				agentsHealth.Running++
			default:
				agentsHealth.Exited++
			}
		}
	}

	// Refinery health
	var refineryHealth health.Refinery
	if mergeQueue != nil {
		st := mergeQueue.GetStatus()
		refineryHealth.Enabled = st.Enabled
		refineryHealth.Running = st.Running
		refineryHealth.QueueLength = st.QueueLen
		refineryHealth.HistoryLength = st.HistoryLen
		refineryHealth.PollInterval = st.PollInterval
		// The slot-holder travels with the pending count, never without it
		// (mg-48d8): the count alone renders a busy refinery and a stopped one
		// identically, and that is the reading six agents acted on.
		refineryHealth.Processing = st.Processing
		refineryHealth.ProcessingSince = st.ProcessingSince

		// Count recent failures from history
		for _, mr := range mergeQueue.History() {
			if mr.Status == refinery.StatusFailed {
				refineryHealth.RecentFailures++
			}
		}
	}

	resp := health.FullResponse{
		Pogod:    pogodHealth,
		Agents:   agentsHealth,
		Refinery: refineryHealth,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func homePage(w http.ResponseWriter, r *http.Request) {
	// Only match the exact root path. In Go 1.22+ ServeMux, the "/{$}"
	// pattern restricts this to "/", but if registered as "/" (catch-all),
	// we must check manually to avoid swallowing unmatched routes with a
	// confusing 200 response instead of a proper 404.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(w, "greetings from pogo daemon")
}

func allProjects(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects")
	switch r.Method {
	case "GET", "":
		json.NewEncoder(w).Encode(project.Projects())
	case "DELETE":
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "path is required", http.StatusBadRequest)
			return
		}
		if project.Remove(req.Path) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"removed": true,
				"path":    req.Path,
			})
		} else {
			http.Error(w, "project not found", http.StatusNotFound)
		}
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

// clean normalizes an incoming visit path. It must not append a trailing
// separator: the path may name a file, and lstat("/repo/file.go/") fails
// with ENOTDIR (mg-88cc). project.Visit appends the separator to directory
// paths where it needs one.
func clean(path string) string {
	return filepath.Clean(path)
}

func file(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /file")
	switch r.Method {
	case "POST":
		decoder := json.NewDecoder(r.Body)
		var req project.VisitRequest
		decodeErr := decoder.Decode(&req)
		if decodeErr != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		req.Path = clean(req.Path)
		response, err := project.Visit(req)
		if err != nil {
			http.Error(w, err.Message, err.Code)
			return
		}
		json.NewEncoder(w).Encode(response)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugin(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugin")
	switch r.Method {
	case "GET":
		encodedPath := r.URL.Query().Get("path")
		path, err := url.QueryUnescape(encodedPath)
		if err != nil {
			fmt.Printf("Error urldecoding path variable: %v\n", err)
			return
		}
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		resp := (*plugin).Info()
		json.NewEncoder(w).Encode(resp)
		return
	case "POST":
		var reqObj pogoPlugin.DataObject
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&reqObj)
		if err != nil {
			fmt.Printf("Request could not be parsed.")
			http.Error(w, "", http.StatusBadRequest)
			return
		}

		path := reqObj.Plugin
		plugin := driver.GetPlugin(path)
		if plugin == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		respString := (*plugin).Execute(reqObj.Value)
		var respObj = pogoPlugin.DataObject{Value: respString}
		json.NewEncoder(w).Encode(respObj)
		return
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func plugins(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /plugins")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(driver.GetPluginPaths())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func projectById(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /projects/{projectId}")
	switch r.Method {
	case "GET":
		projectIdStr := r.PathValue("projectId")
		// If projectIdStr blank we look at the queryParameter 'path'
		if projectIdStr == "file" {
			projectPathStr := r.URL.Query().Get("path")
			// url decode projectIdStr
			path, err := url.QueryUnescape(projectPathStr)
			log.Printf("Path: %s\n", path)
			if err != nil {
				log.Printf("Error urldecoding projectIdStr: %v\n", err)
				http.Error(w, "", http.StatusBadRequest)
				return
			}
			proj := project.GetProjectByPath(path)
			if proj == nil {
				http.Error(w, "", http.StatusNotFound)
				return
			}
			resp := project.GetProject(proj.Id)
			json.NewEncoder(w).Encode(resp)
			return
		}
		projectId, err := strconv.Atoi(projectIdStr)
		if err != nil {
			log.Printf("Error converting projectId to int: %v\n", err)
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		resp := project.GetProject(projectId)
		if resp == nil {
			http.Error(w, "", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func status(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Visited /status")
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(project.GetProjectStatuses())
	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func registerHandlers() {
	http.HandleFunc("/", homePage)
	http.HandleFunc("/file", file)
	http.HandleFunc("/projects/{projectId}", projectById)
	http.HandleFunc("/projects", allProjects)
	http.HandleFunc("/plugin", plugin)
	http.HandleFunc("/plugins", plugins)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/health/full", healthFull)
	http.HandleFunc("/version", versionHandler)
	http.HandleFunc("/status", status)
	http.HandleFunc("/workitems", workitem.HandleWorkItems)

	// Agent and refinery endpoints behind orchestration guard.
	// When the server is in index-only mode, these return 503.
	orchestrated := http.NewServeMux()
	agentRegistry.RegisterHandlers(orchestrated)
	if mergeQueue != nil {
		// Use a closure so handlers always resolve the current mergeQueue.
		// SetRefineryStarter swaps the package-level pointer on orchestration
		// restart; binding handlers to the original instance leaves
		// /refinery/queue serving stale data from the dead refinery (#9).
		refinery.RegisterHandlersFunc(orchestrated, func() *refinery.Refinery {
			return mergeQueue
		})
	} else {
		// Refinery is disabled via config — register stub handlers so
		// /refinery/* endpoints return a clear "disabled" error instead
		// of a confusing 404.
		refinery.RegisterDisabledHandlers(orchestrated)
	}
	if sched != nil {
		// Scheduler is part of the orchestration substrate — registering or
		// removing schedules requires pogod to be in the same mode that runs
		// the heartbeat tick.
		sched.RegisterHandlers(orchestrated)
	}
	if srv != nil {
		http.Handle("/agents/", srv.RequireOrchestration(orchestrated))
		http.Handle("/agents", srv.RequireOrchestration(orchestrated))
		http.Handle("/refinery/", srv.RequireOrchestration(orchestrated))
		http.Handle("/scheduler/", srv.RequireOrchestration(orchestrated))

		// Server mode endpoints (not guarded — always available)
		srv.RegisterHandlers(http.DefaultServeMux)
	} else {
		// No server coordinator — register directly
		http.Handle("/agents/", orchestrated)
		http.Handle("/agents", orchestrated)
		http.Handle("/refinery/", orchestrated)
		http.Handle("/scheduler/", orchestrated)
	}
}

// resolveAgentProvider maps a config provider id to its agent.Provider
// descriptor via the providers registry. An unknown id logs a warning and
// falls back to Claude so a stale or mistyped config never wedges daemon
// startup.
//
// The fallback is right and the silence was not — enumeration row A7 (mg-342d).
// An unknown provider id is not a crash, which is exactly what makes it
// expensive: the fleet comes up and runs on a harness nobody asked for, and every
// behavioural difference that causes gets debugged as a prompt or model problem.
// `notify` is the addressee that can act on it (empty disables annunciation, for
// call sites that run before a coordinator name exists).
func resolveAgentProvider(id, notify string) *agent.Provider {
	p, ok := providers.Resolve(id)
	if !ok {
		log.Printf("WARNING: unknown agent provider %q in config; falling back to %q",
			id, p.ID)
		conditions.Raise(conditionUnknownProvider(notify, id, p.ID, "config"), time.Now())
	} else {
		conditions.Clear(rowA7ProviderPrefix+id, time.Now())
	}
	return p
}

// stallWatchArmed reports whether pogod should arm the passive stall watcher on
// this boot. It requires both the watcher being enabled AND a config file being
// present (cfg.Source set). The cfg.Source gate mirrors the prompt-refresh /
// crew-auto-start gate in main(): an unconfigured daemon never auto-starts a
// coordinator, so arming the watcher would nudge a coordinator (default
// "mayor") this process never launched — spurious nudges and durable-mail
// noise on every isolated/CI/sandbox daemon (gh drellem2/pogo #75). Only watch a
// coordinator the daemon would actually start.
func stallWatchArmed(cfg *config.Config) bool {
	return cfg.StallWatch.Enabled && cfg.Source != ""
}

// stallFallbackDamper is the damping term on the stall-watch mail fallback
// (mg-61ce). It counts, per recipient, how many consecutive fallbacks pogod has
// pushed into that recipient's inbox since the last time it had EVIDENCE the
// recipient could receive a message in person — a successful PTY delivery.
// Past a cap, further fallbacks are withheld.
//
// Why a damping term was needed at all. The fallback (mg-79dc) is triggered by
// exactly one condition: the recipient is too busy to go idle. It responds by
// adding work to that recipient's inbox. So the remedy load rises with the load
// it is responding to, and nothing anywhere in the loop pushed back. Measured on
// this box over the last 20000 events: 1814 stall fires took the mail road, and
// the mayor's maildir holds 766 stall-watch messages — 742 of them the
// "(undelivered to terminal)" fallback, the single largest subject line in a
// 5978-message mailbox by a factor of nine.
//
// The unread_mail category closes the loop outright. Its notice says "your inbox
// is too full" and is delivered AS one more message in that inbox: 530 of the
// fallbacks are that category, and 179 such messages are sitting in the mayor's
// mailbox right now. That is gain >= 1 with no damping — the remedy re-arms its
// own trigger, and no amount of draining outruns it.
//
// What this counter measures, precisely: consecutive fallbacks since the last
// PTY success. It is NOT a measure of unread depth, and deliberately so. The
// mayor's inbox is the one on this box where real traffic outweighs noise, so
// damping on total unread would let a busy-but-healthy coordinator's legitimate
// mail silence the watcher. Keying on stall-watch's own undamped contribution
// makes the term proportional to what stall-watch is responsible for.
//
// Why a PTY success is the right reset signal. It is direct evidence the agent
// went idle — the precise condition that means it is between turns and able to
// drain. No filesystem probe is needed, which matters: a probe that reads the
// recipient's maildir would do work proportional to the backlog, at exactly the
// moment the backlog is largest, and so would reproduce inside the remedy the
// very defect the remedy exists to remove. This counter is O(1) per fire and
// allocates nothing.
//
// What is deliberately NOT damped: the offline road (DeliveryMail, recipient not
// running). That road has the same flooding shape and 303 of the measured fires
// took it, but it has no reset signal — an offline agent never produces a PTY
// success — so a cap there would latch permanently the first time a coordinator
// went down. It needs a different mechanism and is out of scope here.
//
// Applying the finding to the remedy. A suppressor is exactly the kind of
// artifact that can carry the defect it removes — "load rises with load" — so
// each channel it touches was checked for the same shape:
//
//   - The loud log line is emitted once per saturation run, not once per fire
//     (see announce). Per-fire it would be the identical flood in a different
//     channel, which is the failure mode wearing a disguise.
//   - No new events are emitted; the suppressed fire reuses the
//     stall_watch_fired event that would have been written anyway, adding one
//     field.
//   - The counter itself is O(1) per fire with no I/O, so its cost does not
//     track the backlog. That ruled out the otherwise-attractive design of
//     probing the recipient's maildir for undrained notices — a read whose cost
//     grows with the backlog, performed at the moment the backlog is largest.
//   - The map is pruned on the offline road, so it does not grow without bound
//     across a daemon's lifetime as unique polecat names come and go.
//
// And the sharpest version of the question: when the damper IS suppressing, who
// finds out, given that the channel it suppresses is the one that would say so?
// Nothing that matters travels by mail here. The transition is a log line, and
// every subsequent fire stamps nudge_suppressed_consecutive into events.log — a
// value that keeps climbing is a coordinator that has not gone idle once across
// that whole run, which is a louder and more specific signal than the flood it
// replaced. Both roads are outside the loop, which is what makes them usable.
//
// Reconciling this with mg-79dc's first-attempt doctrine ("the cooldown is a
// rate limiter, not a retry queue; delivery must succeed on the FIRST attempt").
// That doctrine is about notices reaching NOBODY. Suppression here happens only
// when a capful of identical-channel notices is already sitting in front of the
// recipient undrained, so the marginal notice reaches nobody either way — it
// joins a pile that is not being read. And nothing is lost on the far side:
// stall-watch re-derives every condition from scratch each tick and never
// queues, so the moment the recipient becomes reachable the CURRENT state fires,
// not a stale replay. Suppression defers a notice until it can be received; it
// does not discard a fact.
type stallFallbackDamper struct {
	mu sync.Mutex
	// cap is the number of consecutive fallbacks allowed per recipient before
	// suppression begins. Negative means no damping at all.
	cap int
	// consecutive maps recipient -> fallbacks since that recipient's last PTY
	// delivery. Entries are deleted on reset rather than zeroed, so the map
	// tracks only currently-saturated recipients.
	consecutive map[string]int
	// announced records the recipients already logged as suppressed, so the
	// loud line fires once at the transition instead of on every fire — a
	// per-fire log line would itself be the flood-under-load shape this damper
	// exists to remove.
	announced map[string]bool
}

func newStallFallbackDamper(limit int) *stallFallbackDamper {
	if limit == 0 {
		limit = config.DefaultStallMailFallbackBacklogCap
	}
	return &stallFallbackDamper{
		cap:         limit,
		consecutive: make(map[string]int),
		announced:   make(map[string]bool),
	}
}

// admit records one more fallback for recipient and reports whether the mail may
// be sent. It returns the post-increment consecutive count either way, so a
// suppressed fire can record how deep the suppression runs.
func (d *stallFallbackDamper) admit(recipient string) (n int, allow bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cap < 0 {
		return 0, true
	}
	d.consecutive[recipient]++
	n = d.consecutive[recipient]
	return n, n <= d.cap
}

// reset clears a recipient's fallback run. Called on a successful PTY delivery:
// the agent went idle, so it is reachable and able to drain, and the next
// fallback starts a fresh run.
func (d *stallFallbackDamper) reset(recipient string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.consecutive, recipient)
	delete(d.announced, recipient)
}

// announce reports whether this is the first suppressed fire for recipient in
// the current run, so the caller logs the transition exactly once.
func (d *stallFallbackDamper) announce(recipient string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.announced[recipient] {
		return false
	}
	d.announced[recipient] = true
	return true
}

// newStallNudger builds the stall watcher's delivery function. It tries the
// agent's PTY in wait-idle mode when the agent is running, and falls back to
// durable macguffin mail in BOTH cases where the PTY cannot carry the message:
// the agent is not running, or the PTY nudge failed.
//
// The wait-idle mode is the load-bearing choice for gh drellem2/pogo #61: it
// blocks until the agent's PTY goes quiet before writing, so a BUSY agent is
// never interrupted mid-turn (the write lands at the next turn boundary) and an
// idle agent is woken at once. The priority wake reuses this exact nudger — it
// does not introduce a second, more aggressive delivery path — so it inherits
// the never-interrupt-a-busy-agent guarantee.
//
// The fallback-on-FAILURE half is mg-79dc, and it exists because wait-idle has
// a structural blind spot: it can only deliver to an agent that goes idle, and
// a working agent never goes idle. On 2026-07-17, 18 of 47 stall fires (~38%)
// died with `still producing output after 30s ... context deadline exceeded`,
// including both work-item fires. The channel failed EXACTLY when the mayor was
// busy — which is precisely when a dispatch stall is most likely and the notice
// most needed. A watcher whose reporting channel goes dark under the condition
// it watches for is not a lossy watcher; it is a blind one.
//
// The failures were not near-misses that a longer timeout would catch: the
// recorded "last PTY write" values were 2ms, 8ms, 26ms — the mayor was writing
// continuously, not almost-quiet. No timeout survives a genuinely busy agent,
// which is why lengthening it is the wrong fix (it would only trade a visible
// failure for a slower one).
//
// Mail is the right shape for this signal: `mg mail` does not require an idle
// recipient, and the mayor drains its inbox on its own cadence. Note the fallback
// lands in a channel stall-watch ITSELF watches (categoryUnreadMail), so an
// ignored fallback escalates rather than vanishing.
//
// This does NOT weaken the #61 never-interrupt guarantee — we still never write
// to a busy PTY. The guarantee was "do not interrupt a busy agent", not "do not
// inform it". Nor does it double-deliver: mail is sent only when the PTY nudge
// returned an error, i.e. only when nothing was written.
//
// The shape mirrors newMailCheckReachabilityEscalator below, which already got
// this right — try the PTY, fall back to mail on failure.
//
// ptyTimeout is the wait-idle budget. Production passes
// agent.DefaultNudgeTimeout (see newStallNudger); tests inject a short one so
// the busy-agent fallback path can be proven in milliseconds rather than by
// sleeping out a 30s deadline. The timeout's LENGTH is deliberately not what
// the fallback depends on — see mg-79dc: no length saves a busy agent, which is
// exactly why the fallback exists and why lengthening it was ruled out.
// The `damper` argument is mg-61ce's damping term on the fallback road; see
// stallFallbackDamper for what it counts and why. A nil damper means no damping,
// which is the pre-mg-61ce behaviour.
func newStallNudgerWithTimeoutAndDamper(reg *agent.Registry, mail func(to, from, subject, body string) error, ptyTimeout time.Duration, damper *stallFallbackDamper) stallwatch.Nudger {
	return func(agentName string, notice stallwatch.Notice) (stallwatch.Delivery, error) {
		message := notice.Message
		if reg != nil {
			a := reg.Get(agentName)
			if a != nil && a.Status == agent.StatusRunning {
				// NudgeWake, not NudgeWithMode: a stall fire is the canonical
				// WAKE — an automated nudge whose only purpose is to rouse an
				// agent that is not doing anything — so it is subject to the
				// wake-cycle policy (internal/agent/wakepolicy.go). A suppressed
				// wake returns an error and takes the mail road below, exactly
				// like a busy PTY: the policy suppresses the terminal write, not
				// the notice.
				err := a.NudgeWake(message, agent.NudgeWaitIdle, ptyTimeout, "")
				if err == nil {
					// The agent went idle: it is reachable in person and able
					// to drain. That is the damper's reset signal (mg-61ce).
					if damper != nil {
						damper.reset(agentName)
					}
					return stallwatch.Delivery{Channel: stallwatch.DeliveryPTY}, nil
				}
				// The PTY could not take it — busy, or wedged redrawing. Before
				// taking the mail road, ask the damping term whether this
				// recipient can still absorb a fallback (mg-61ce). Past the cap
				// it cannot: a capful of stall-watch notices is already sitting
				// in its inbox undrained, and adding another is the move that
				// makes the remedy load rise with the load it responds to.
				if damper != nil {
					n, allow := damper.admit(agentName)
					if !allow {
						// Loud once per run, not once per fire — a per-fire log
						// line under saturation would be the same flood in a
						// different channel. The per-fire record lives in the
						// stall_watch_fired event's nudge_suppressed_consecutive.
						if damper.announce(agentName) {
							log.Printf("pogod: STALL FALLBACK SUPPRESSED for %s — %d consecutive mail fallbacks "+
								"with no successful PTY delivery in between (cap %d); further stall notices are "+
								"WITHHELD until %s goes idle once. The last PTY refusal was: %v",
								agentName, n, damper.cap, agentName, err)
						}
						return stallwatch.Delivery{
							Channel:               stallwatch.DeliverySuppressed,
							FallbackReason:        err.Error(),
							SuppressedConsecutive: n,
						}, nil
					}
				}
				// Tell the recipient why it arrived here: a stall
				// notice in the inbox rather than on the terminal means the
				// terminal was busy, and that context is itself diagnostic.
				body := fmt.Sprintf(
					"%s\n\n---\nThis notice could not be delivered to your terminal (%v), "+
						"so it was sent as mail instead. It may therefore be older than it looks — "+
						"re-check the current state before acting on it.",
					message, err)
				if mailErr := mail(agentName, "stall-watch", stallSubject(notice)+" (undelivered to terminal)", body); mailErr != nil {
					// Both channels are down. This is the genuine hard failure:
					// nothing carried the message. Log loudly — a stall notice
					// that reaches nobody is the failure this watcher exists to
					// prevent, and it must not be inferable only from a JSON
					// field in a log nobody reads.
					log.Printf("pogod: STALL NUDGE UNDELIVERED to %s — pty nudge failed (%v) "+
						"AND mail fallback failed (%v); the stall notice reached NOBODY", agentName, err, mailErr)
					return stallwatch.Delivery{}, fmt.Errorf(
						"pty nudge failed (%v) and mail fallback failed: %w", err, mailErr)
				}
				return stallwatch.Delivery{
					Channel:        stallwatch.DeliveryMailFallback,
					FallbackReason: err.Error(),
				}, nil
			}
		}
		// The offline road: the recipient is not running (or the registry does
		// not know it). Undamped — see stallFallbackDamper for why a cap with no
		// reset signal would latch permanently — but the recipient's fallback
		// run IS cleared here, for two reasons.
		//
		// Correctness: a coordinator that died and came back is a new process
		// with a new PTY, and a run accumulated against the old one says nothing
		// about the new one. Carrying it over would suppress the first notices
		// to a freshly restarted, perfectly reachable agent.
		//
		// Housekeeping: this is what bounds the damper's map in a daemon that
		// runs for months. Recipients are not a fixed set — `blocked:<agent>`
		// notices (mg-3844) address polecats, and polecat names are unique per
		// spawn — so without a prune the map would grow one entry per polecat
		// that ever drew a suppressed reminder and never a PTY success. Every
		// such agent eventually stops running and takes this road.
		if damper != nil {
			damper.reset(agentName)
		}
		if err := mail(agentName, "stall-watch", stallSubject(notice), message); err != nil {
			return stallwatch.Delivery{}, err
		}
		return stallwatch.Delivery{Channel: stallwatch.DeliveryMail}, nil
	}
}

// stallSubjectFallback is the subject a notice gets when it carries none. It is
// the string EVERY stall-watch mail used to carry, kept for exactly one reason:
// a caller that composes no subject must still produce mail, and a notice with
// an empty subject line is worse than an unhelpful one. Seeing it in a maildir
// means a fire reached the delivery site without going through
// stallwatch.subject — a bug in the watcher, not in delivery.
const stallSubjectFallback = "stall-watch: work piling up"

// stallSubject reads the notice's own subject, falling back when it is empty.
//
// The fallback is a total function on purpose. mg-b6f8's defect was that this
// site COULD NOT do better — the facts were not in scope here, so one constant
// was the only thing it could write. Now the facts arrive with the notice, and
// the only remaining way to emit the old undistinguishable subject is for the
// watcher to have composed nothing.
func stallSubject(n stallwatch.Notice) string {
	if s := strings.TrimSpace(n.Subject); s != "" {
		return s
	}
	return stallSubjectFallback
}

// newStallNudgerWithTimeout is the test-facing constructor: an injected PTY
// budget at the production damping cap. Tests that need to exercise the damper
// itself build one explicitly and call newStallNudgerWithTimeoutAndDamper.
func newStallNudgerWithTimeout(reg *agent.Registry, mail func(to, from, subject, body string) error, ptyTimeout time.Duration) stallwatch.Nudger {
	return newStallNudgerWithTimeoutAndDamper(reg, mail, ptyTimeout,
		newStallFallbackDamper(config.DefaultStallMailFallbackBacklogCap))
}

// newStallNudger is the production constructor: the standard wait-idle budget at
// the configured damping cap.
//
// On the budget: mg-61ce asked whether the 30s wait-idle deadline is still the
// right number for the current fleet size, since it was chosen against a smaller
// one. It is, and the measurement says lengthening it is not merely unhelpful
// but pointless. Across 1702 recorded fallbacks on this box, the gap since the
// coordinator's last PTY write AT THE MOMENT the 30s deadline expired had a
// median of 218ms and a p99 of 941ms; only 10 of 1702 (0.6%) had reached even
// one second, against a 2s idle threshold. The coordinator is not almost-quiet
// at the deadline — it is writing continuously — so a 60s or 300s budget buys
// nothing and holds the heartbeat longer for it. The outcome of every one of
// those fires was determined in the first two seconds. That is the same
// conclusion mg-79dc reached from 18 samples, now confirmed at ~100x the n.
func newStallNudger(reg *agent.Registry, mail func(to, from, subject, body string) error, fallbackCap int) stallwatch.Nudger {
	return newStallNudgerWithTimeoutAndDamper(reg, mail, agent.DefaultNudgeTimeout,
		newStallFallbackDamper(fallbackCap))
}

// newMailCheckReachabilityEscalator builds the mayor-nudge fired when a
// polecat's mail-check schedule could not be registered even after
// verify+retry (mg-6fe0). A live polecat with no mail-check loop has no
// proactive reachability channel — it will miss reviewer findings and
// re-review requests that drive the modify<->review loop — so this is a
// coordination alert, not a cosmetic one. Delivery mirrors newStallNudger:
// wait-idle PTY nudge when the mayor is running (never interrupts a busy turn),
// durable macguffin mail otherwise so the signal survives an offline mayor.
func newMailCheckReachabilityEscalator(reg *agent.Registry, coordinator string) func(agentName, scheduleID string) {
	return func(agentName, scheduleID string) {
		msg := fmt.Sprintf(
			"reachability alert: polecat %s could not register its mail-check schedule %s after verify+retry — "+
				"it has NO proactive mail channel and may miss reviewer findings / re-review requests. "+
				"Re-register it (`pogo schedule %s --cron \"*/10 * * * *\" --id %s ...`) or restart it.",
			agentName, scheduleID, agentName, scheduleID)
		if reg != nil {
			if a := reg.Get(coordinator); a != nil && a.Status == agent.StatusRunning {
				if err := a.NudgeWithMode(msg, agent.NudgeWaitIdle, agent.DefaultNudgeTimeout); err == nil {
					return
				}
			}
		}
		if err := client.SendMGMail(coordinator, "pogod", "polecat reachability alert", msg); err != nil {
			log.Printf("pogod: mail-check reachability escalation to %s failed: %v", coordinator, err)
		}
	}
}

// newStartVerifier builds the post-spawn start-verification query for the
// auto-renudge watcher (mg-feb3). It reports a polecat as "started" once its mg
// work item has left the available/ queue — the item's presence in available/
// is the HARD unstarted-signal the watcher gates its bare-CR renudge on. workRoot
// is the macguffin work directory (~/.macguffin/work); it scans only available/,
// so the check is cheap and never walks the unbounded done/ tree. A read error
// propagates so the watcher treats it as inconclusive rather than renudging a
// possibly-working agent.
func newStartVerifier(workRoot string) agent.StartVerifier {
	return func(workItemID string) (bool, error) {
		items, err := workitem.ListFrom(workRoot, "available")
		if err != nil {
			return false, err
		}
		for _, it := range items {
			if it.ID == workItemID {
				return false, nil // still available → not yet claimed → unstarted
			}
		}
		return true, nil // left the available queue → claimed → started
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `pogod — the pogo daemon.

Supervises agents as UNIX processes: the mayor (the coordinator), polecats
(disposable worker agents), and the refinery (the merge queue). Work items
and mail live in mg/macguffin (the task-store CLI).

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()
	startTime = time.Now()

	// Rotate the launchd-managed log before anything writes to it, then mark
	// the run boundary. launchd appends across restarts (so prior-run crash
	// evidence survives), and this startup rotation keeps the file bounded
	// while guaranteeing the previous run's tail is in pogod.log or
	// pogod.log.1 when a post-mortem needs it (mg-6d02). No-op unless
	// stderr actually is pogod.log — dev runs and pipe-captured spawns are
	// untouched.
	rotated, logPath, rotErr := service.RotatePogodLogIfNeeded()
	if rotErr != nil {
		log.Printf("pogod: log rotation failed (continuing): %v", rotErr)
	}
	if rotated {
		log.Printf("pogod: rotated %s (previous run's log is %s.1)", logPath, logPath)
	}
	log.Printf("pogod: starting (pid=%d)", os.Getpid())

	// Repair PATH before spawning anything. Under launchd/systemd pogod inherits
	// a minimal or empty PATH, which breaks bare-name subprocess lookups such as
	// the scheduler/refinery `mg mail send` fallback (mg-905f). Do this first so
	// every child spawned below resolves mg/gh/git without absolute paths.
	if err := pathenv.Ensure(); err != nil {
		fmt.Printf("Warning: could not augment PATH: %v\n", err)
	}

	// Repair GH_TOKEN for the same reason and in the same breath (mg-03ea).
	// pathenv fixes children that cannot be FOUND under launchd's minimal env;
	// this fixes children that are found, run, and cannot AUTHENTICATE. Without
	// it every `gh` call pogod makes exits with "populate the GH_TOKEN
	// environment variable", which is why the gh-issue teardown detector below
	// reported every carrier as indeterminate on every run. Logged
	// existence-only: the value never reaches the log.
	log.Printf("pogod: %s", ghtoken.Ensure())

	// Bound every git repository lookup at POGO_HOME, before anything shells out
	// to git or spawns an agent. Every repo pogod manages (polecats/*,
	// refinery/worktrees/*, agents/*) is nested INSIDE ~/.pogo, so a lookup
	// aimed at one that has lost its .git walks up and silently succeeds on the
	// fleet's own config repo. Setting the ceiling on this process covers
	// pogod's own git calls and every child it spawns — including git run by an
	// agent's harness — because children inherit this environment (mg-ca7d).
	//
	// Fatal, not a warning: unset, this variable's absence is indistinguishable
	// from a walk that never needed bounding, so a daemon that merely logged and
	// carried on would run the whole fleet unguarded with the reassurance of a
	// startup line nobody reads. The same reasoning as mg-8f09 — an absent guard
	// is UNKNOWN, never clean.
	if err := gitceiling.Ensure(); err != nil {
		fmt.Printf("Cannot bound git repository lookups at %s: %v\n", config.PogoHome(), err)
		os.Exit(1)
	}

	// Ensure the state dir exists before anything writes into it — a fresh
	// or isolated POGO_HOME (mg-3dc3) starts empty and the lockfile create
	// below fails on a missing parent dir.
	if err := os.MkdirAll(config.PogoHome(), 0755); err != nil {
		fmt.Printf("Cannot create state dir %s: %v", config.PogoHome(), err)
		os.Exit(1)
	}

	// Acquire lockfile. The path is derived from POGO_HOME (see
	// config.LockfilePath), NOT os.TempDir(): $TMPDIR differs between the
	// launchd domain and a shell/agent, so a TempDir-based lock did not
	// prevent a second pogod from starting and racing the live daemon for
	// :10000, hanging up the agent fleet via SIGHUP (#22).
	lock, err := lockfile.New(config.LockfilePath())
	if err != nil {
		fmt.Printf("Cannot create lock. reason: %v", err)
		os.Exit(1)
	}

	if err = lock.TryLock(); err != nil {
		// Only one pogod may own a POGO_HOME at a time. Name the PID that
		// currently holds the lock so the operator can find the live daemon.
		holder := "an unknown pid"
		if p, gerr := lock.GetOwner(); gerr == nil {
			holder = fmt.Sprintf("pid %d", p.Pid)
		}
		// Shared refinery/queue counts across host + containerized clients are
		// by-design shared-POGO_HOME state, not two live daemons (which cannot
		// coexist — this path hard-exits). See docs/CONFIGURATION.md (mg-f227).
		fmt.Printf("Cannot acquire pogod lock %s: held by %s (reason: %v).\n"+
			"A single pogod owns each POGO_HOME; its refinery/queue state is "+
			"shared by design across every client on this POGO_HOME (mg-f227).\n",
			config.LockfilePath(), holder, err)
		os.Exit(1)
	}

	defer func() {
		if err := lock.Unlock(); err != nil {
			fmt.Printf("Cannot unlock %q, reason: %v", lock, err)
		}
	}()

	// Initialize agent registry. The socket dir hangs off PogoHome, not
	// os.TempDir(): $TMPDIR is per-user, so two daemons on distinct POGO_HOME
	// roots with identically-named agents used to share one socket file and
	// fight the mg-d216 supervisor over it forever (mg-8532).
	socketDir, insidePogoHome := config.AgentSocketDir()
	if insidePogoHome {
		// NewRegistry creates socketDir with mode 0700, and MkdirAll stamps that
		// mode on every parent it creates on the way down — including the agents/
		// dir the sockets share with the prompt files, which has always been
		// 0755. Create that parent first so a fresh POGO_HOME ends up with the
		// same layout as an existing one. socketDir itself still lands at 0700,
		// which is what an attach socket wants.
		if err := os.MkdirAll(agent.PromptDir(), 0755); err != nil {
			fmt.Printf("Cannot create agent state dir %s: %v\n", agent.PromptDir(), err)
			os.Exit(1)
		}
	} else {
		log.Printf("pogod: POGO_HOME %s is too deep to hold unix sockets (sun_path limit); "+
			"agent attach sockets live in %s instead — still unique to this POGO_HOME",
			config.PogoHome(), socketDir)
		// The fallback dir sits in a shared temp root, so it needs two things
		// NewRegistry does not do: the nest parent vetted before we create
		// anything inside it, and a record of which POGO_HOME owns this leaf so
		// the leaves of roots that no longer exist can be reaped. Test binaries
		// are the only thing that reaches this branch in practice, and each one
		// used to strand a directory here forever — 3,883 of one $TMPDIR's
		// 37,083 entries (mg-a997).
		if err := agent.PrepareFallbackSocketDir(socketDir, config.PogoHome()); err != nil {
			fmt.Printf("Cannot prepare agent socket dir %s: %v\n", socketDir, err)
			os.Exit(1)
		}
	}
	var initErr error
	agentRegistry, initErr = agent.NewRegistry(socketDir)
	if initErr != nil {
		fmt.Printf("Cannot create agent registry: %v\n", initErr)
		os.Exit(1)
	}
	// NO `defer agentRegistry.StopAll(...)` here, deliberately (mg-6b66).
	//
	// There used to be one. It could never run, on any path, and it lied to
	// every reader about how this fleet dies:
	//
	//   - SIGTERM — the routine stop (`pogo server stop` signals it directly,
	//     see internal/client.stopDaemon; so do launchd and the nightly
	//     restart) — has no handler here, so it kills at the default
	//     disposition and skips deferred functions entirely.
	//   - The only other way out is the bottom of main(): log.Fatal(Serve(...)),
	//     and log.Fatal is os.Exit(1), which also skips defers.
	//   - SIGKILL, panic and host crash skip them too.
	//
	// So agents are NOT stopped on the way out. What actually kills them is the
	// PTY hangup: pogod owns each agent's PTY master, its death force-closes
	// that fd, the terminal is revoked, and the agent — a session leader with
	// that PTY as its controlling terminal (gh #22) — takes SIGHUP and dies at
	// the default disposition. That accident is load-bearing: pogod's GC sweep
	// reaps the mail-check of any polecat missing from the in-memory registry,
	// which is only safe because a polecat cannot outlive pogod. It is pinned by
	// TestPolecatDoesNotOutlivePogod (internal/agent/polecat_pty_hangup_test.go,
	// mg-61a0) and documented in docs/investigations/.
	//
	// Restoring the defer would change nothing — re-read the exit paths above
	// before you try. A SIGTERM handler WOULD make graceful shutdown real, but
	// it is a behaviour change, not a cleanup: StopAll stops agents serially at
	// up to 5s each while stopDaemon gives pogod a 5s deadline in total, so a
	// naive handler makes `pogo server stop` miss its own deadline on any real
	// fleet. It also cannot cover SIGKILL/panic/host crash — the paths that
	// actually strand agents. If you want it, size the budget to that deadline
	// and keep the hangup documented; it stays load-bearing either way.

	// Load config early so we can use it for agent command setup
	cfg := config.Load()

	// Pin the frozen legacy role names BEFORE anything reads a role name off
	// cfg (mg-bc47). The guard used to run much further down, next to the
	// prompt refresh — correct logic, too late: config.Load fills an empty
	// [agents] coordinator/worker with the LIVE Default* consts, so the first
	// boot of a build that flipped those defaults (mg-ce47) resolved the NEW
	// names and acted on them — auto-started a coordinator named "ringmaster",
	// armed the stall watcher on it, addressed refinery mail to its mailbox —
	// in the same second it wrote "mayor" into config.toml. It self-healed on
	// the next restart, leaving boot 1's coordinator a stray agent with an
	// orphaned mailbox. Pinning here and re-loading means boot 1 already sees
	// the pinned names.
	cfg, rolePinErr := pinAndResolveRoles(cfg)

	// Second, config-aware PATH repair pass: [agents] extra_path lets a
	// deployment point pogod at harness runtimes the automatic probe in
	// pathenv.Ensure misses (gh #25). Runs before any agent spawns.
	if err := pathenv.EnsureExtra(cfg.Agents.ExtraPath); err != nil {
		fmt.Printf("Warning: could not apply [agents] extra_path: %v\n", err)
	}

	// Apply index-scope limits from config (mg-d205).
	search.SearchService.SetMaxFilesPerTree(cfg.MaxFilesPerTree)
	project.SetIndexRoots(cfg.IndexRoots)

	// Configure agent command templates and the harness providers.
	agentRegistry.SetCommandConfig(&cfg.Agents)

	// Arm the dispatch gate with the configured non-dispatchable vocabulary, so
	// `pogo agent spawn-polecat` refuses a work item gated to a human, parked, or
	// blocked on a named agent (mg-4798, mg-6fb0 — the `blocked:<agent>` shape is
	// structural and gates whatever this vocabulary says). The vocabulary is read
	// from [stallwatch] because that is where
	// the key already lives and both consumers must agree on it — one list, one
	// predicate (config.IsDispatchGated), two enforcement points. A daemon that
	// never reaches this line still gates the defaults; this only applies an
	// operator's `non_dispatchable_assignees` override.
	//
	// Deliberately NOT behind stallWatchArmed: the gate is not a detector and has
	// nothing to do with whether a coordinator is being watched. Tying it to that
	// would silently disable it on every sandbox and unconfigured daemon, which
	// is the class of gap mg-da48 and mg-6c4b were both about.
	agentRegistry.SetDispatchGate(agent.MGDispatchGate{
		Gates: cfg.StallWatch.NonDispatchableAssignees,
	})

	// The pairing half of the same chokepoint (mg-0e24): refuse to dispatch an
	// item whose repository requires a paired item that was never filed. Unlike
	// the gate above, this one carries NO shipped policy — cfg.DispatchPairing is
	// empty unless a deployment names repos under [dispatch_pairing], and an
	// empty repo list makes the gate inert. Wired unconditionally anyway, so
	// whether it enforces is a question about this host's config.toml and not
	// about which branch of pogod's startup ran.
	agentRegistry.SetDispatchPairingGate(agent.MGDispatchPairingGate{
		Cfg: cfg.DispatchPairing,
	})
	if len(cfg.DispatchPairing.Repos) > 0 {
		log.Printf("dispatch pairing armed: items in %v owe a paired item tagged %v before dispatch "+
			"(require_tags=%v waiver_tags=%v)",
			cfg.DispatchPairing.Repos, cfg.DispatchPairing.PairTags,
			cfg.DispatchPairing.RequireTags, cfg.DispatchPairing.WaiverTags)
	}

	// The per-repo cap (mg-3977), at the same chokepoint. Unlike the pairing
	// gate above this one DOES carry shipped policy — config.Load defaults it to
	// three workers per repo with one slot held for the refinery — because what
	// it prevents is a property of running a shared test suite concurrently and
	// not one deployment's program. Wired unconditionally so whether it enforces
	// is a question about [dispatch] in config.toml, never about which branch of
	// startup ran.
	agentRegistry.SetDispatchCap(cfg.DispatchCap)
	if cfg.DispatchCap.Armed() {
		log.Printf("dispatch cap armed: at most %d worker(s) per repo, %d slot(s) reserved for the "+
			"refinery while it has work there", cfg.DispatchCap.MaxPolecatsPerRepo,
			cfg.DispatchCap.RefineryReserve)
	} else {
		log.Printf("dispatch cap DISARMED ([dispatch] max_polecats_per_repo = 0): " +
			"nothing limits how many workers enter one repo")
	}

	// The refinery half of that cap. The queue is reached through a THUNK, not
	// captured by value: an orchestration restart replaces *mergeQueue
	// (SetRefineryStarter, below), and a closure over the old pointer would
	// reserve against a refinery nobody is using any more.
	agentRegistry.SetRefineryActivity(refineryRepoActivity(func() *refinery.Refinery { return mergeQueue }))

	// And the routing half, beside the gate it sits next to (mg-9a04): a spawn
	// with no --template routes on the work item's `type` through the closed map
	// in internal/agent/templateroute.go, refusing rather than defaulting when
	// the type is unrouted. Explicit, even though MGWorkItemTyper{} is already
	// the zero-value default, so the two halves of the dispatch decision are
	// visible in one place rather than one being wired and one being implicit.
	agentRegistry.SetWorkItemTyper(agent.MGWorkItemTyper{})

	// Role names were resolved by pinAndResolveRoles above, before any consumer
	// could read one. cfg here is the post-pin config.
	coordinator := cfg.Agents.CoordinatorName()

	// Where a watcher escalation goes once the fleet has demonstrably not
	// cleared the finding — the box a PERSON reads. Resolved once, here, and
	// passed to all four escalating watchers below, because the four package
	// defaults ("human" in each) are four copies of one deployment fact and a
	// deployment that relays `human` has to move all four together or not at
	// all. See config.DefaultEscalationBox for the loop this breaks: with a
	// representative agent owning `human`, an escalation reading "the
	// representative is deaf" would otherwise be delivered into the inbox of
	// the agent it is reporting as unable to read its inbox (mg-65d2).
	escalationBox := cfg.Agents.EscalationBoxName()

	// Arm the condition annunciator (mg-342d) — rows A2..A15 of
	// docs/investigations/pogod-log-conditions-with-no-reader-2026-07-30.md, the
	// conditions mg-c3f0 found to have an actor who could act and no channel to
	// reach them. It has to be built HERE, at the first line where a coordinator
	// name exists, because the addressee is the whole mechanism: earlier than
	// this there is nobody to mail, and everything below this line is a decision
	// point that used to end in log.Printf and nothing else.
	//
	// The two conditions that occur BEFORE this line are captured rather than
	// dropped, and raised immediately below (log rotation at the very top of
	// main, and the role-default pin inside pinAndResolveRoles). Capturing an
	// early condition and annunciating it at the first moment there is a reader
	// is a different thing from tailing a log for it: the value is still the one
	// the emitter held, unparsed.
	conditions = newConditionAnnunciator(
		conditionNoticesPath(),
		client.SendMGMail,
		func(name, message string) error {
			if agentRegistry == nil {
				return fmt.Errorf("no agent registry")
			}
			a := agentRegistry.Get(name)
			if a == nil {
				return fmt.Errorf("agent %s is not running", name)
			}
			return a.Nudge(message)
		},
	)

	// A14 — log rotation. Deferred from main's first statement (see rotErr
	// above), because rotation runs before config is even loaded.
	if rotErr != nil {
		conditions.Raise(conditionLogRotationFailed(coordinator, rotErr.Error()), time.Now())
	} else {
		conditions.Clear(rowA14LogRotation, time.Now())
	}

	// A10 — role-default pin. Deferred from pinAndResolveRoles, which cannot
	// annunciate its own failure: resolving the addressee is one of its jobs.
	if rolePinErr != nil {
		conditions.Raise(conditionRolePinFailed(coordinator, rolePinErr.Error()), time.Now())
	} else {
		conditions.Clear(rowA10RolePin, time.Now())
	}

	// Register every known harness provider into the registry, then set the
	// global default. Before mg-b31b a single provider was resolved here, once,
	// at startup; now the registry resolves a provider per spawn from the
	// precedence chain (--provider flag > provider: frontmatter > per-type
	// config > global default). That is what lets one Codex polecat run
	// alongside a Claude fleet with no pogod restart.
	//
	// Each provider carries its own lifecycle hooks (applied per-spawn off the
	// agent's resolved provider, not a registry global):
	//   - PostSpawnHook auto-accepts the harness's workspace/trust dialog.
	//   - SessionHook is the lifetime modal-dismissal watcher (mg-4421) that
	//     scans tee'd PTY output for the rating dialog and rate-limit-options
	//     modal and dismisses each via its menu keystroke. It survives
	//     schedule-substrate failures by living inside pogod's per-agent PTY
	//     goroutine — see mg-ef6b §7 / mg-5a3d §4.
	for _, p := range providers.All() {
		agentRegistry.RegisterProvider(p)
	}
	agentRegistry.SetDefaultProvider(cfg.Agents.Provider)

	// Install the wake-cycle policy's limit-episode query (mg-8184). This is the
	// composition root doing the wiring on purpose: internal/agent ASKS
	// internal/claude at the moment it is about to wake an agent, and
	// internal/claude never learns that a nudge path exists. Pull, not push —
	// which is what keeps the fleet usage-limit coordinator report-only while
	// its state still suppresses a useless wake into a wedged harness.
	agent.SetLimitEpisodeQuery(claude.UsageLimitEpisodeOpen)

	// Wire the post-spawn start-verification watcher (mg-feb3): after the initial
	// nudge, pogod checks whether a polecat actually claimed its work item and, if
	// a concurrent-spawn init-stall swallowed the kickoff, re-delivers a bare
	// submit terminator. The macguffin work root mirrors the stall watcher's
	// default (~/.macguffin/work).
	if home, err := os.UserHomeDir(); err == nil {
		agentRegistry.SetStartVerifier(newStartVerifier(filepath.Join(home, ".macguffin", "work")))
	}

	// Restore the HARD started-signal for dispatches pogod claims at spawn
	// (mg-7d6d). The verifier above cannot serve them: pogod's own claim satisfies
	// it from the first instant, so it falls back to the ready-composer signal,
	// which misses the mg-ce61 paste-buffer wedge the recovery net exists for. The
	// polecat's first protocol action re-stamps the claim to its own pid, and this
	// verifier watches that pid.
	//
	// The signal needs an additive macguffin subcommand that may not be installed
	// (macguffin mg-bb43), so this probes for it and engages only if it is there,
	// wiring the verifier and the polecat prompt step together — see
	// EnableClaimRestampSignal on why neither half is reachable alone. The empty
	// bin resolves to "mg" on PATH, the same default MGWorkItemClaimer uses, so the
	// probe cannot test a different binary from the one that took the claim.
	agentRegistry.EnableClaimRestampSignal("", os.Getpid())

	// Validate the command binary for each agent type exists on PATH. Each type
	// can select a different provider via [agents.<type>] provider, so resolve
	// per type. An empty configured command means "use the provider's default
	// template". Dedupe so an identical command is only checked once.
	checkedCmds := map[string]bool{}
	for _, agentType := range []string{"crew", "polecat"} {
		typeProvider := resolveAgentProvider(cfg.Agents.AgentProvider(agentType), coordinator)
		typeCmd := cfg.Agents.AgentCommand(agentType)
		if typeCmd == "" {
			typeCmd = typeProvider.CommandTemplate
		}
		if !checkedCmds[typeCmd] {
			agent.ValidateCommandBinary(typeCmd)
			checkedCmds[typeCmd] = true
		}
	}

	// Set up agent lifecycle callbacks. Restart vs. cleanup is now driven by
	// the agent's RestartOnCrash flag (resolved from prompt frontmatter, with
	// type-based defaults: crew=true, polecat=false). This preserves the
	// historical behavior — crew agents are still restarted, polecats are
	// still cleaned up — while letting users opt out per-agent.
	// Bounded backstop for --defer-done polecats (gh #81): when such a polecat
	// merges, OnMerged skips the auto-done/auto-stop and arms this instead, so
	// the polecat can finish its own post-merge flow. If it never ends its
	// lifecycle, the backstop reaps + escalates it — the OnExit hook below
	// disarms it on a clean exit. Escalation mails the mayor.
	deferBackstop := newDeferredBackstop(deferDoneBackstopTimeout, agentRegistry, func(mr *refinery.MergeRequest) {
		subject := fmt.Sprintf("DEFER-DONE BACKSTOP FIRED: polecat %s lingered post-merge", mr.Author)
		body := fmt.Sprintf("A --defer-done polecat merged but did not complete its lifecycle within %s.\n"+
			"pogod reaped the lingering process to free its slot (gh #34/#35).\n\n"+
			"Work item: %s\nBranch: %s\nMR: %s\n\n"+
			"The polecat never called `mg done` — verify the work item state and re-dispatch if its post-merge flow (PR creation, verify, mail) did not finish.",
			deferDoneBackstopTimeout, mr.Author, mr.Branch, mr.ID)
		if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
			log.Printf("refinery: failed to mail coordinator defer-done backstop escalation: %v", err)
		}
	})
	// Let the backstop tell "finished its flow but is still alive waiting to be
	// stopped" (the normal end state for a PR-flow polecat) from "stalled and
	// never completed". Both get reaped; only the latter escalates (mg-7746).
	deferBackstop.workItemDone = client.MGWorkItemDone
	// The other failure ending: the deferred polecat DIES between its merge and
	// its `mg done`. Its claim is released by settleExit (a self-exit never goes
	// through Registry.Stop, so nothing else would), and the mayor is told —
	// the branch is merged but the PR it owed is not open (mg-c8d5).
	deferBackstop.escalateDeath = func(mr *refinery.MergeRequest, a *agent.Agent, released bool, releaseErr error) {
		id, branch, mrID := a.WorkItemID, "", ""
		if mr != nil {
			if mr.Author != "" {
				id = mr.Author
			}
			branch, mrID = mr.Branch, mr.ID
		}
		subject := fmt.Sprintf("DEFERRED POLECAT DIED POST-MERGE: %s never reached mg done", id)
		claimLine := fmt.Sprintf("The claim was released; %s is back in available/ and can be re-dispatched.", id)
		if releaseErr != nil {
			claimLine = fmt.Sprintf("RELEASING THE CLAIM FAILED (%v) — %s is STRANDED in claimed/ under a dead pid, "+
				"where dispatch will not see it and stall-watch does not look. Release it by hand: `mg unclaim %s`.",
				releaseErr, id, id)
		}
		body := fmt.Sprintf("A deferred (PR-flow or --defer-done) polecat's process ended while it still held the claim on its work item.\n"+
			"That means it died AFTER its branch merged and BEFORE `mg done` — the merge landed, the pull request it owed did not.\n\n"+
			"Work item: %s\nAgent: %s\nBranch: %s\nMR: %s\n\n"+
			"%s\n\n"+
			"Check whether the PR was opened before re-dispatching; the branch is already merged into its target.",
			id, a.Name, branch, mrID, claimLine)
		if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
			log.Printf("refinery: failed to mail coordinator deferred-death escalation: %v", err)
		}
	}

	// Build the synthetic-failure-turn detector (mg-8cdb, from mg-18d0's
	// finding). It reads each running agent's harness session transcript and
	// pages `human` when an agent is answering every nudge LOCALLY and failing
	// it — an expired credential, a rate limit, a spend cap. That class leaves
	// the agent alive, responsive, and productive of nothing, so no exit-driven
	// or idle-driven check in this daemon can see it: on 2026-07-22 six agents
	// burned 143 nudges each for 23h30m while scheduler_fire_delivered logged
	// 647 healthy deliveries.
	//
	// It PAGES and SUPPRESSES RESTARTS; it never remediates. No member of the
	// class is fixable by restarting, and restarting costs a live session's
	// whole context plus the transcript the diagnosis depends on.
	//
	// It is armed unconditionally — unlike the stall watcher it nudges nobody
	// and starts nothing, so an unconfigured or sandbox daemon is safe. Where a
	// harness declares no transcript path (every provider but Claude today) the
	// scan returns StateUnavailable and every consumer behaves exactly as it did
	// before this existed.
	synthWatcher := synthwatch.New(synthwatch.Options{
		Home:    homeDir(),
		Targets: func() []synthwatch.Target { return synthTargets(agentRegistry) },
		Globs:   providers.SessionTranscriptGlobs,
		Mail:    client.SendMGMail,
	})
	// diagnose reports the verdict, and ShouldRespawnAgent consults it before
	// any restart_on_crash respawn.
	agentRegistry.SetTranscriptScanner(synthScanner{w: synthWatcher})
	log.Printf("pogod: synthetic-failure-turn detector enabled (interval=%s, page-only — restarts are SUPPRESSED, never issued)",
		synthwatch.DefaultInterval)

	agentRegistry.SetOnExit(func(a *agent.Agent, err error) {
		// Settle any defer-done backstop for this polecat: its process has
		// ended, so the slot is free and there is nothing left to reap (gh #81)
		// — but if it ended while still holding its work-item claim, it died
		// between its merge and `mg done`, and the claim is released and the
		// mayor told (mg-c8d5). A no-op for the vast majority of agents, which
		// are not deferred.
		deferBackstop.cancel(a)

		// Consult the synthetic-failure-turn detector BEFORE respawning
		// (mg-8cdb). An agent whose transcript shows it failing every turn
		// locally is not recoverable by restarting: the replacement session
		// inherits the same dead credential or exhausted limit, while the
		// restart discards the session's context and overwrites the transcript
		// the diagnosis rests on. mg-18d0 costed the ungated version of this
		// path at ~66 restarts over 23.5h, recovering nothing. The detector has
		// already paged `human`; there is nothing for this daemon to do but
		// stand down and say so loudly.
		respawn, suppressedBy := agentRegistry.ShouldRespawnAgent(a)
		if !respawn && a.ShouldRespawn() {
			log.Printf("agent %s (%s) exited while failing every turn (%s); SUPPRESSING respawn — "+
				"a restart cannot fix this and destroys the session's context (mg-18d0). A human has been paged.",
				a.Name, a.Type, suppressedBy.Reason)
			synthWatcher.SuppressRestart(a.Name, a.EventAgent())
		}
		if respawn {
			// Restart-on-crash agents: respawn after a short backoff so a
			// fast crash loop doesn't peg the daemon. The agent stays in
			// the registry and its worktree (if any) is preserved.
			log.Printf("agent %s (%s) exited unexpectedly, scheduling restart", a.Name, a.Type)
			// Capture the registry generation HERE, at scheduling time, not
			// inside the goroutine. The goroutine sleeps 2s before firing
			// while StopAll returns synchronously, so this respawn can land
			// after a stop-orchestration has already completed — and, if a
			// start-orchestration follows inside that window, after the
			// shutdown latch has been cleared again. Passing the generation
			// makes this respawn belong to the fleet it was scheduled in: any
			// stop or start in between refuses it, however late it fires.
			gen := agentRegistry.Generation()
			go func() {
				time.Sleep(2 * time.Second)
				_, rerr := agentRegistry.RespawnFromGeneration(a.Name, gen)
				// A refusal from the shutdown latch or the generation check is
				// the guard working, not a restart failure — see
				// noteRespawnOutcome for why alarming on it made A6 unreadable
				// (mg-0208).
				noteRespawnOutcome(conditions, coordinator, a.Name, rerr, time.Now())
			}()
		} else {
			if a.RestartOnCrash && !a.ShouldRespawn() {
				// restart_on_crash is set but the agent is parked — the park
				// flag (written before the park stop) suppresses the respawn
				// and routes the exit through the cleanup path (mg-41e1).
				// (The other suppressor, the synthetic-failure-turn detector,
				// logs its own line above; a.ShouldRespawn() is still true in
				// that case, so this one does not double-report it as parked.)
				log.Printf("agent %s (%s) exited while parked; suppressing respawn", a.Name, a.Type)
			}
			// No-restart agents: clean up worktree (if any) and remove from
			// the registry. Polecats hit this path by default; a crew agent
			// with restart_on_crash=false in its prompt frontmatter also
			// lands here.
			//
			// This callback fires from waitAndHandle on EVERY process exit
			// — normal completion, crash, and force-stop alike (pogo agent
			// stop SIGTERMs then SIGKILLs; cmd.Wait returns either way) —
			// so the worktree cleanup below runs on abnormal exits, not
			// only clean ones. The single exit path no in-process callback
			// can cover, pogod dying mid-polecat, is the job of the gitgc
			// startup sweep. See mg-30d5 D3.
			log.Printf("agent %s (%s) exited, cleaning up", a.Name, a.Type)
			// A worktree holding uncommitted work is PRESERVED, not reaped
			// (mg-ee02). See cleanupAgentWorktree for why preservation and
			// not refusal, and for the notification that keeps a preserved
			// tree from accumulating unnoticed.
			//
			// WorkItemID is passed because a preserved tree is the one form of
			// stranded work no pushed-commit guard can see, and without the id
			// the notice cannot name the item that is now unsafe to dispatch at
			// (mg-32e3). It was available here all along.
			cleanupAgentWorktree(exitedAgent{
				Name:        a.Name,
				EventAgent:  a.EventAgent(),
				WorkItemID:  a.WorkItemID,
				SourceRepo:  a.SourceRepo,
				WorktreeDir: a.WorktreeDir,
			}, coordinator, client.SendMGMail)
			a.Cleanup()
			// Removes the spawn's expanded prompt file along with the registry
			// entry — this is the branch on which the owner is not coming back,
			// and the respawn arm above deliberately keeps it (mg-5197).
			agentRegistry.Remove(a.Name)
			// Eagerly reap this agent's mail-check loop so it stops firing the
			// moment the agent is gone, rather than on the next Tick sweep
			// (gh drellem2/macguffin #15). Match on both the bare name and the
			// cat-/crew- event identity a schedule may be addressed by.
			if sched != nil {
				if n := sched.RemoveMailChecksForAgent(time.Now(), a.Name, a.EventAgent()); n > 0 {
					log.Printf("agent %s: reaped %d stale mail-check schedule(s)", a.Name, n)
				}
			}
		}
	})

	// Start the heartbeat detector. It compares wall vs monotonic time on
	// each tick and emits a system_wake event if they diverge by more than
	// the configured threshold — catches host sleep, VM pause, NTP step.
	// See docs/sleep-resilience-design.md §1.
	hb := heartbeat.New()
	if cfg.Heartbeat.Interval > 0 {
		hb.Interval = cfg.Heartbeat.Interval
	}
	if cfg.Heartbeat.JumpThreshold > 0 {
		hb.Threshold = cfg.Heartbeat.JumpThreshold
	}

	// The mail-check reap's startup grace (mg-de08). Built here because the
	// scheduler below installs it and the auto-start sweep further down opens
	// it; it stays closed until then.
	gcGate := newStartupGCGate(startupGCSettle)

	// Provision every spawned polecat's mailboxes (mg-7dc1). Wired OUTSIDE the
	// scheduler block below on purpose — see mgMailboxRegistrar: addressability
	// does not depend on a scheduler, and a daemon whose scheduler failed to
	// load is the one where hand-sent mail is the only channel left.
	agentRegistry.SetMailboxRegistrar(mgMailboxRegistrar{})

	// Start the scheduler. Schedules in ~/.pogo/schedules.json drive a
	// Tick() call from the heartbeat loop — wall-clock jumps are absorbed
	// for free because the scheduler stores absolute fire times and the
	// same goroutine handles both system_wake detection and the sweep.
	schedPath, err := scheduler.DefaultPath()
	if err != nil {
		log.Printf("pogod: scheduler disabled (cannot resolve home dir): %v", err)
		// A2, second site (mg-342d). Same consequence as a load failure — no
		// schedule fires for anyone — so it gets the same severity and its own
		// suppression key.
		conditions.Raise(conditionSchedulerNoHome(coordinator, err.Error()), time.Now())
	} else {
		conditions.Clear(rowA2SchedulerNoHome, time.Now())
		// Wire the schedule-register failure reporter FIRST, independent of
		// whether the scheduler below actually loads. If scheduler.New fails, the
		// mail-check registrar is never installed and every polecat spawn takes
		// the nil-registrar path — the startup suspect this telemetry exists to
		// surface (mg-6fe0). The reporter targets the scheduler's own-root
		// events.log, resolvable from schedPath even without a live *Scheduler.
		agentRegistry.SetScheduleRegisterFailureReporter(
			scheduleRegisterFailureReporter{logPath: scheduler.EventLogPath(schedPath)})

		deliverer := &scheduler.PogodDeliverer{
			Registry: agentRegistry,
			Mail:     client.SendMGMail,
			// Same own-root events.log the scheduler's own fire events go to
			// (mg-e06d), so a coalesced mailbox copy is readable next to the
			// scheduler_fire_* record of the fire that produced it (mg-af83).
			LogPath: scheduler.EventLogPath(schedPath),
		}
		s, err := scheduler.New(schedPath, deliverer)
		if err != nil {
			log.Printf("pogod: scheduler load failed (%s): %v", schedPath, err)
			// A2 — the enumeration's own highest severity (mg-342d). No
			// mail-check schedule fires for ANYONE, so the fleet loses its
			// proactive channel wholesale and the two watchers that exist to
			// detect fleet deafness are disabled by the same fault.
			//
			// This is the one condition that asks for a WAKE as well as a mail,
			// and the reason is the reason mg-c3f0 stopped here: mail is
			// deliverable (client.SendMGMail shells out to `mg`, no scheduler
			// involved) but the coordinator's mail-check loop is itself a
			// schedule, so it will never be prompted to look. The nudge rides
			// the heartbeat, which drives the scheduler rather than depending on
			// it — see conditionWaker for why that makes this detector not dead
			// by its own fault, and for the narrower failure class (a scheduler
			// that loaded and then silently stopped) that this does NOT cover.
			conditions.Raise(conditionSchedulerLoadFailed(coordinator, schedPath, err.Error()), time.Now())
		} else {
			conditions.Clear(rowA2SchedulerLoad, time.Now())
			// Install the liveness checker so Tick garbage-collects
			// mail-check-* schedules whose target agent is gone (gh
			// drellem2/macguffin #15). Backed by the agent registry AND the
			// desired state on disk: an unregistered crew agent is EXPECTED,
			// not gone (mg-de08).
			s.SetLiveness(registryLiveness{reg: agentRegistry})
			// Hold that reap until the first auto-start sweep has completed and
			// settled. The invariant above is only as good as the data it reads,
			// and at this point in startup the registry is empty and the crew
			// have not been spawned yet (mg-de08).
			s.SetGCGate(gcGate.open)
			sched = s
			// Make diagnose cron-aware: a crew agent driven by a recurring cron
			// is idle by design between firings and must not be flagged as
			// stalled within one cron interval of its last firing (mg-5b23).
			agentRegistry.SetStallScheduleProvider(schedulerStallWindows{sched: s})
			// The inverse check (mg-de08): an agent pogod is expected to be
			// running with NO mail-check schedule can be mailed but never
			// woken, and until now diagnosed perfectly healthy. Same
			// desired-state predicate as the reap above — one source of truth,
			// two directions.
			agentRegistry.SetMailCheckProvider(schedulerMailChecks{sched: s})
			// Let park/wake pause and restore an agent's schedules (mg-41e1).
			agentRegistry.SetSchedulePauser(schedulePauser{sched: s})
			// Auto-register a polecat's mail-check loop at spawn so review
			// loops round-trip without manual schedule registration (mg-e633).
			// On a persistent registration failure (verify+retry both failed),
			// escalate to the mayor: a live polecat with no reachability channel
			// is a coordination problem, not a cosmetic one (mg-6fe0).
			agentRegistry.SetMailCheckRegistrar(mailCheckRegistrar{
				sched:    s,
				escalate: newMailCheckReachabilityEscalator(agentRegistry, coordinator),
			})
			log.Printf("pogod: scheduler loaded from %s", schedPath)
		}
	}

	// Build the stall watcher (gh drellem2/macguffin #12): the pogod-side third
	// leg of the wedge-response triad. It rides the heartbeat loop and nudges
	// the mayor when work piles up behaviorally — available items left
	// unclaimed past a threshold, or unread mail accumulating — even while the
	// mayor's process looks healthy. Running here, in pogod's
	// guaranteed-independent heartbeat, is the whole point: a watcher inside the
	// mayor's own loop can't catch that loop silently skipping its check-work /
	// check-mail steps. See internal/stallwatch and docs/design/stall-watch-design.md.
	//
	// Gate arming on cfg.Source, exactly as prompt refresh and crew auto-start
	// are below (see the cfg.Source == "" branch): an unconfigured daemon never
	// auto-starts a coordinator, so arming the watcher would nudge a coordinator
	// (default "mayor") that this process never launched — spurious nudges
	// and durable-mail noise on every isolated/CI/sandbox daemon (gh drellem2/pogo
	// #75). Only watch a coordinator the daemon would actually start.
	var stallWatcher *stallwatch.Watcher
	if cfg.StallWatch.Enabled && cfg.Source == "" {
		log.Printf("pogod: no config file at %s; stall watcher not armed (no auto-started coordinator to nudge)", config.ConfigFilePath())
	}
	if stallWatchArmed(cfg) {
		stallWatcher = stallwatch.New(cfg.StallWatch, stallwatch.Options{
			// The cap is mg-61ce's damping term on the mail fallback: past it,
			// a coordinator that has not gone idle once stops being sent more
			// notices about being too busy to be sent notices.
			Nudge: newStallNudger(agentRegistry, client.SendMGMail, cfg.StallWatch.MailFallbackBacklogCap),
			// Let a priority wake short-circuit the ~30s heartbeat poll for a
			// prompt follow-up sweep (gh #61). hb.Nudge coalesces, so this can't
			// storm the loop; the priority cooldown bounds it to one extra tick
			// per wake.
			FastPoll: hb.Nudge,
			// Let both dispatch notices read the per-repo worker cap before
			// naming a remedy (mg-dd77). Without it a saturated fleet draws a
			// recurring "claim or dispatch them" that the spawn point refuses
			// on arrival, on the same channel that carries genuinely
			// actionable dispatch news.
			Capacity: newStallCapacity(agentRegistry),
			// Let every available/ check ask whether a live worker is already
			// on an item before calling it neglected (mg-1a8a). Without it a
			// claim that failed open at spawn leaves the item in available/,
			// where the priority wake urges a coordinator to dispatch work a
			// polecat is already doing.
			Workers: newStallWorkers(agentRegistry),
		})
		log.Printf("pogod: stall watcher enabled (agent=%s item_age=%s mail_age=%s max_mail=%d cooldown=%s fallback_cap=%d priority_wake=%t wake_delay=%s wake_cooldown=%s fast_priorities=%s non_dispatchable=%s indefinite_hold=%t hold_age=%s hold_cooldown=%s)",
			cfg.StallWatch.Agent, cfg.StallWatch.UnclaimedItemAgeThreshold,
			cfg.StallWatch.UnreadMailAgeThreshold, cfg.StallWatch.MaxUnreadMailCount,
			cfg.StallWatch.NudgeCooldown,
			// Printed because a NEGATIVE value here silently disables the
			// damping (mg-61ce), and a disabled damper is indistinguishable
			// from a working one until the flood arrives.
			cfg.StallWatch.MailFallbackBacklogCap,
			cfg.StallWatch.PriorityWakeEnabled,
			cfg.StallWatch.HighPriorityWakeDelay, cfg.StallWatch.HighPriorityWakeCooldown,
			strings.Join(cfg.StallWatch.FastPriorities, ","),
			// The `blocked:<agent>` shape (mg-6fb0) gates alongside the
			// vocabulary and is not IN it, so printing the list alone would
			// understate what this daemon enforces — an operator reading the
			// startup line to find out what gates would read a false answer.
			strings.Join(cfg.StallWatch.NonDispatchableAssignees, ",")+
				","+config.BlockedAssigneePrefix+"<agent>",
			// Printed because this reader's own finding applies to itself: an
			// indefinite hold is invisible when nothing reads it, and a reader
			// that is off is invisible in exactly the same way. Its events fire
			// only when something is held, so "no indefinite_hold events" and
			// "the report is disarmed" are otherwise the same observation
			// (mg-f398). This line separates them at startup.
			cfg.StallWatch.IndefiniteHoldReportEnabled,
			cfg.StallWatch.IndefiniteHoldAgeThreshold,
			cfg.StallWatch.IndefiniteHoldReportCooldown)
	}

	// Build the drift-check runner (mg-345b): the DETECTION backstop that rides
	// the heartbeat and, on a COARSE interval, runs the check-drift detector
	// (internal/reconcile.CheckDrift) over the [reconcile] mirrors and mails
	// `human` when a host artifact has drifted from its repo source. This is the
	// runner mg-5701's detector never had — "a detector you have to remember to
	// ask." It is REPORT-ONLY (never reconciles) and rides the heartbeat, NOT a
	// launchd timer, because the nondemand-spawn wedge (mg-50e0) would leave a
	// launchd timer silently never firing. See internal/driftwatch and mg-75f9.
	//
	// The same runner also carries the REVISION-STALENESS check (mg-5bd2): a
	// POSITIVE answer to "is the daemon current?" that does not route through the
	// nightly deploy job. Every previous alarm on that question was indexed to
	// the job's own exit code, so a night the job never fired produced no exit
	// code and therefore no alarm — four such nights passed in a row and pogod
	// ended up 85 commits behind main. This check reads the running process's own
	// vcs stamp instead, so it fires whether the job failed loudly, never fired,
	// or exited 0 without installing. See internal/driftwatch/revision.go.
	//
	// The same runner ALSO carries the DID-NOT-RUN check (mg-2416), which is a
	// different question from either of the two above and is why neither of them
	// covered this. Four of the eight nights in that outage (08-01..08-04) were
	// not failures — the job never started, so there was no `pogo-deploy: start`
	// line, no exit code and no stamp, and every alarm we owned was downstream of
	// the job running. The staleness check would eventually have caught the
	// consequence, at seven days; this catches the cause, on the morning after
	// the first silent night, by reading the deploy log's start lines against the
	// schedule. It never consults the run's outcome — that is the whole point —
	// and notably never reads launchd's `runs` counter, which re-installing the
	// plist resets to zero and which therefore reads 0 both for a job that just
	// ran and one that never has. See internal/driftwatch/nofire.go.
	//
	// The mirror check is armed only when there are [reconcile] mirrors — no
	// mirrors means no artifacts to watch, so an unconfigured/sandbox daemon is a
	// silent no-op on that half. The staleness check is armed UNCONDITIONALLY: it
	// needs no repo, no network and no config, and gating it on configuration
	// somebody has to remember to write is precisely how the mg-5701 lineage
	// keeps shipping detectors that never run. An unstamped binary (any test
	// build) disarms itself and says so once. The no-fire check is armed on any
	// host where the nightly LaunchAgent is INSTALLED — also no config key, for
	// the same reason — and disarms loudly where it is not. The Enabled flag is
	// the off switch for all three.
	var driftWatcher *driftwatch.Watcher
	if cfg.DriftWatch.Enabled {
		mirrors := make([]reconcile.Mirror, 0, len(cfg.Reconcile.Mirrors))
		for _, m := range cfg.Reconcile.Mirrors {
			mirrors = append(mirrors, reconcile.Mirror{Name: m.Name, Source: m.Source, Target: m.Target, Label: m.Label})
		}
		opts := driftwatch.Options{
			Mirrors: mirrors,
			// The FACTORY, not a pre-built Deps: HostDeps carries a per-sample
			// launchctl cache, so each sample must get a fresh one or drift would
			// freeze after the first check (see driftwatch.Options.NewDeps).
			NewDeps:    reconcile.HostDeps,
			Mail:       client.SendMGMail,
			Revision:   driftwatch.BuildRevision,
			StaleAfter: cfg.DriftWatch.SelfStaleAfter,
		}
		// The did-not-run check. The log path comes from internal/service, the
		// same derivation the installed plist's StandardOutPath is bound from, so
		// the detector and the installer cannot disagree about which file holds
		// the record — a detector reading the wrong path finds an empty file, and
		// an empty deploy log is indistinguishable from a deploy that never fired.
		deployHours, deployMinute := service.DeploySchedule()
		opts.DeployLogPath = service.DeployLogPath()
		opts.DeployLog = driftwatch.FileDeployLog(opts.DeployLogPath)
		opts.DeployInstalled = service.DeployStatus
		opts.Schedule = staleness.DeploySchedule{
			Hours:  deployHours,
			Minute: deployMinute,
			Grace:  staleness.DefaultGrace,
		}
		// Optional and context-only: the commits-behind number in the notice. It
		// never gates the alarm, so an unset or unfetched repo costs one line of
		// context and never the verdict.
		if repo := cfg.DriftWatch.SelfRepo; repo != "" {
			opts.Behind = driftwatch.GitBehind(repo)
			opts.BehindRepo = repo
		}
		driftWatcher = driftwatch.New(cfg.DriftWatch, opts)

		staleAfter := cfg.DriftWatch.SelfStaleAfter
		if staleAfter <= 0 {
			staleAfter = driftwatch.DefaultStaleAfter
		}
		// Print the running revision and its COMMIT date at startup. An operator
		// reading this line can answer "is this daemon current?" without waiting
		// for an alarm — and if the stamp is missing, the line says the check is
		// blind rather than leaving that to be inferred from its silence.
		rev := driftwatch.BuildRevision()
		revDesc := "UNSTAMPED (staleness check blind)"
		if rev.Stamped() {
			revDesc = fmt.Sprintf("%s committed %s", rev.Short(), rev.CommitTime.Format(time.RFC3339))
		}
		// The no-fire half gets its own clause, and says which of the two states
		// it is in. A detector that logs nothing when it is unarmed is the exact
		// shape this ticket exists to remove.
		noFireDesc := fmt.Sprintf("watching %s", opts.DeployLogPath)
		if installed, _ := service.DeployStatus(); !installed {
			noFireDesc = "NOT ARMED (" + "no nightly deploy LaunchAgent installed on this host)"
		}
		log.Printf("pogod: drift-check runner enabled (mirrors=%d interval=%s, report-only); revision-staleness: running %s, stale_after=%s; deploy-no-fire: %s",
			len(mirrors), cfg.DriftWatch.Interval, revDesc, staleAfter, noFireDesc)
	}

	// Build the credential-expiry warner (mg-7024): the standing check that
	// PREDICTS the next fleet-wide auth outage instead of detecting it a day
	// late. The OAuth refresh grant has a fixed 30-day life that use does not
	// extend, so its expiry sits on local disk as a plain integer
	// (`refreshTokenExpiresAt` in the `Claude Code-credentials` keychain item).
	// Both prior outages went unnoticed until the fleet had been dead ~24h;
	// this mails `human` at T-7d/-72h/-24h/-2h so a person can run `/login` at
	// a moment of their choosing. See internal/credexpiry and mg-ed45.
	//
	// pogod is the right host for two reasons. It rides the heartbeat rather
	// than a launchd timer (the nondemand-spawn wedge, mg-50e0, would leave a
	// timer silently never firing), and it SURVIVES the condition it predicts —
	// pogod holds no Claude credential of its own, so it keeps ticking through
	// an auth outage that kills every agent. That second constraint is weaker
	// here than for a reactive pager, since this warner does its work while
	// everything is still healthy, but the heartbeat is free so there is no
	// reason to take the weaker option.
	//
	// Always constructed when enabled: unlike drift-watch there is no config to
	// gate on. It probes for the credential itself and self-disarms LOUDLY (one
	// log line + a cred_expiry_disarmed event) on a host that has none, so a
	// sandbox or a non-macOS box is quiet without ever claiming the credential
	// is healthy. REPORT-ONLY, and necessarily — only a human can run `/login`.
	var credWatcher *credexpiry.Watcher
	if cfg.CredExpiry.Enabled {
		credWatcher = credexpiry.New(credexpiry.Options{
			Enabled:       true,
			Mail:          client.SendMGMail,
			Interval:      cfg.CredExpiry.Interval,
			BlindRenotify: cfg.CredExpiry.BlindRenotify,
		})
		log.Printf("pogod: credential-expiry warner enabled (interval=%s, warns at 7d/72h/24h/2h, report-only)",
			cfg.CredExpiry.Interval)
	}

	// Build the gh-issue teardown detector (mg-6e57): the standing check that
	// every gh-issue carrier at status=done has actually had its GitHub issue
	// closed. mg-07ba reached `done, stage: merge` with the work genuinely
	// finished and drellem2/pogo#89 sat OPEN for four days, because a carrier
	// that completed its teardown and one that skipped it are indistinguishable
	// from the outside. REPORT-ONLY: it mails and never closes or comments —
	// that stays human-gated.
	//
	// It reports to a FLEET mailbox (`pm-pogo` by default, mg-b586), because a
	// teardown miss is a workflow failure the fleet chases, not a decision for a
	// human; `human` is copied only once a finding has gone unresolved past
	// escalate_after, when "the fleet is not handling this" is itself the news.
	//
	// Armed only when `gh` is actually available. Without it EVERY lookup is
	// indeterminate, and the runner would faithfully report an environment gap
	// as a wall of findings — noise that would get the detector muted before the
	// run that matters. A missing gh is a precondition, not a finding.
	var teardownWatcher *ghteardown.Watcher
	if cfg.GHTeardown.Enabled {
		if _, err := exec.LookPath("gh"); err != nil {
			log.Printf("pogod: gh-issue teardown detector NOT armed — `gh` not on PATH (%v); "+
				"done carriers will not be checked against their issues", err)
			// A13 (mg-342d). The ONE row that does not go to the coordinator:
			// this subsystem already has a deliberately-chosen mailbox for its
			// findings ([gh_teardown] notify_to, mg-b586), and its not-armed
			// condition belongs to the same reader as those findings.
			//
			// A missing `gh` is a precondition rather than a finding — which is
			// exactly why the watcher refuses to arm — but it is a precondition
			// that can BREAK, and under launchd it has: PATH in the daemon's
			// environment is not PATH in a shell, so `gh` working when you type
			// it proves nothing. The transition is what is worth mailing.
			// Falls back to the coordinator if notify_to was explicitly blanked:
			// an unroutable notice is louder than a lost one, but a routable one
			// is better than both.
			teardownTo := cfg.GHTeardown.NotifyTo
			if teardownTo == "" {
				teardownTo = coordinator
			}
			conditions.Raise(conditionTeardownNotArmed(teardownTo, err.Error()), time.Now())
		} else {
			conditions.Clear(rowA13TeardownNotArmed, time.Now())
			src := ghteardown.MGSource{}
			teardownWatcher = ghteardown.New(ghteardown.Options{
				Enabled:       true,
				Source:        src.Carriers,
				Mail:          client.SendMGMail,
				Interval:      cfg.GHTeardown.Interval,
				RenotifyAfter: cfg.GHTeardown.RenotifyAfter,
				NotifyTo:      cfg.GHTeardown.NotifyTo,
				EscalateAfter: cfg.GHTeardown.EscalateAfter,
				EscalateTo:    escalationBox,
			})
			log.Printf("pogod: gh-issue teardown detector enabled (interval=%s renotify=%s notify_to=%s escalate_after=%s escalate_to=%s, report-only)",
				cfg.GHTeardown.Interval, cfg.GHTeardown.RenotifyAfter,
				cfg.GHTeardown.NotifyTo, cfg.GHTeardown.EscalateAfter, escalationBox)
		}
	}

	// Build the gh-issue INTAKE detector (mg-039b): the reconciliation nobody
	// computed. The sibling above audits the workflow's LAST step; this one audits
	// its FIRST — for every OPEN issue on a watched repo, does a work item exist
	// carrying that issue's `gh:` marker?
	//
	// It exists because a delivered `[gh]` mail can be dropped with nothing
	// noticing. drellem2/pogo#99 generated two delivered mails on 2026-07-29 and
	// went ~10 hours with no carrier; its paired issue #100 was carried normally,
	// so a pair filed to be considered together was split and the untracked half
	// was invisible to every listing the fleet runs. It surfaced only because a PM
	// ran an open-issue sweep by hand, early, on a hunch. The coordinator prompt
	// already prescribes the discipline that would have prevented it — prescribing
	// it was not sufficient, because there was no detector, only an instruction.
	//
	// Reports to the COORDINATOR rather than the PM (unlike the teardown detector):
	// the remedy is to file a carrier and dispatch triage, and that is the only
	// agent that does either. `human` is copied only once one issue has gone
	// uncarried past escalate_after, when "the coordinator is not handling this" has
	// itself become the news — which is also the answer to what happens if the
	// coordinator is down. REPORT-ONLY: it mails and never files or comments.
	//
	// Armed only when `gh` is actually available. Without it EVERY repo lookup
	// fails, and the runner would faithfully report an environment gap as a wall of
	// unreadable repos — noise that would get the detector muted before the run
	// that matters. A missing gh is a precondition, not a finding.
	var intakeWatcher *ghintake.Watcher
	if cfg.GHIntake.Enabled {
		if _, err := exec.LookPath("gh"); err != nil {
			log.Printf("pogod: gh-issue intake detector NOT armed — `gh` not on PATH (%v); "+
				"open issues will not be reconciled against carriers", err)
			// A13's second consequence — see conditionIntakeNotArmed. Routed to this
			// subsystem's own mailbox for the same reason A13 is, falling back to the
			// coordinator if notify_to was explicitly blanked.
			intakeTo := cfg.GHIntake.NotifyTo
			if intakeTo == "" {
				intakeTo = coordinator
			}
			conditions.Raise(conditionIntakeNotArmed(intakeTo, err.Error()), time.Now())
		} else {
			conditions.Clear(rowA13IntakeNotArmed, time.Now())
			// The watch list is resolved ONCE at build time rather than per sample:
			// it comes from the poller's state directory, and a repo added there
			// mid-run is picked up on the next pogod restart. Resolving it per sample
			// would mean a directory read on every tick for a list that changes
			// perhaps twice a year.
			stateDir := filepath.Join(config.PogoHome(), ghintake.PollerStateDirName)
			repos, repoSrc := ghintake.ResolveRepos(cfg.GHIntake.Repos, stateDir)
			src := ghintake.MGSource{}
			intakeWatcher = ghintake.New(ghintake.Options{
				Enabled: true,
				Source: func() (ghintake.Inventory, error) {
					return ghintake.Collect(repos, ghintake.GHOpenIssues, src.Carriers, src.Statuses())
				},
				Mail:          client.SendMGMail,
				Interval:      cfg.GHIntake.Interval,
				Grace:         cfg.GHIntake.Grace,
				RenotifyAfter: cfg.GHIntake.RenotifyAfter,
				NotifyTo:      cfg.GHIntake.NotifyTo,
				EscalateAfter: cfg.GHIntake.EscalateAfter,
				EscalateTo:    escalationBox,
			})
			log.Printf("pogod: gh-issue intake detector enabled (interval=%s grace=%s renotify=%s notify_to=%s escalate_after=%s escalate_to=%s repos=%v from %s, report-only)",
				cfg.GHIntake.Interval, cfg.GHIntake.Grace, cfg.GHIntake.RenotifyAfter,
				cfg.GHIntake.NotifyTo, cfg.GHIntake.EscalateAfter, escalationBox, repos, repoSrc)
		}
	}

	// Build the REVIEW-DECLARATION detector (mg-253e): the sweep that reports a
	// review ticket carrying no usable `reviews:` line, and therefore a builder
	// the mg-aaf6 exemption can never protect.
	//
	// It is the residual mg-aaf6 named in its own PR rather than papered over.
	// That guard removes every piece of state somebody must remember to clear —
	// but the declaration itself is still written by a coordinator following an
	// instruction, and an unfollowed instruction emits nothing. The guard does
	// not fire, the builder is reaped mid-review as it was before, and no
	// artifact says a declaration was expected and missing.
	//
	// It runs here rather than only as a CLI because of what the sibling next
	// door cost: verdictwatch was a correct, audited detector that NOTHING RAN.
	// Shipping a check for "the coordinator did not do the thing it was told to
	// do" and then relying on the coordinator to remember to run it would
	// reproduce the very defect one level up.
	//
	// No arming precondition, unlike its two gh-issue siblings above: the scan is
	// a local filesystem walk, so there is no external tool whose absence would
	// turn an environment gap into a wall of findings. REPORT-ONLY, and here that
	// is load-bearing rather than conventional — a detector that repaired the
	// thing it measures could never again be trusted to measure it, so there is
	// no seam in internal/reviewdecl through which a work item could be written.
	var reviewDeclWatcher *reviewdecl.Watcher
	if cfg.ReviewDecl.Enabled {
		src := reviewdecl.Source{}
		reviewDeclWatcher = reviewdecl.New(reviewdecl.Options{
			Enabled:       true,
			Source:        src.Items,
			Mail:          client.SendMGMail,
			Interval:      cfg.ReviewDecl.Interval,
			RenotifyAfter: cfg.ReviewDecl.RenotifyAfter,
			NotifyTo:      cfg.ReviewDecl.NotifyTo,
			Statuses:      src.Statuses(),
		})
		log.Printf("pogod: review-declaration detector enabled (interval=%s renotify=%s notify_to=%s boundary=%s statuses=%v, report-only)",
			cfg.ReviewDecl.Interval, cfg.ReviewDecl.RenotifyAfter, cfg.ReviewDecl.NotifyTo,
			reviewdecl.ConventionLandedAt.Format(time.RFC3339), src.Statuses())
	} else {
		// Logged rather than left silent. A detector for a silently-absent guard
		// that is itself silently switched off would be the same defect one more
		// level up, and `[review_decl] enabled = false` is otherwise indistinguishable
		// from a build that never had the detector in it.
		log.Printf("pogod: review-declaration detector DISABLED by config — review tickets filed " +
			"without a `reviews:` line will not be reported (mg-253e)")
	}

	// Build the scheduler-completion deficit detector (mg-1935): the READER the
	// ack counters never had. mg-a754 gave every fire a completion signal and
	// `pogo schedule list` even renders `⚠ N unacked`, but nothing consumed it —
	// so a crew agent sat at 36% completion for its entire run and the only path
	// to noticing was a human comparing rows in that table.
	//
	// It compares each schedule to its PEERS (same kind, same cadence,
	// comparable fire count) rather than to its own history, because the agent
	// that motivated this was always broken and had no regression to see. See
	// internal/ackwatch.
	//
	// Armed only when the scheduler loaded — without it there is nothing to
	// sample. StartedAt suppresses the report for one settle window after this
	// process starts, on the same footing as a system_wake: a restart is when
	// agents re-register their mail-checks and zero their counters, and mg-42ac
	// made that nightly. REPORT-ONLY.
	var ackWatcher *ackwatch.Watcher
	if cfg.AckWatch.Enabled && sched != nil {
		schedulerLog := scheduler.EventLogPath(schedPath)
		ackWatcher = ackwatch.New(ackwatch.Options{
			Enabled: true,
			Source: func(now time.Time) (ackwatch.Snapshot, error) {
				at, reason := ackwatch.LastDisruption(schedulerLog, now)
				// The windowed fire traffic is what the ABSOLUTE (blackout) arm
				// judges, and it comes from events rather than the counters
				// because a re-registration zeroes those and the nightly redeploy
				// guarantees one (mg-e2a4). RecentFires never errors — an
				// unreadable window arrives as Recent.Err and is reported as a
				// blind arm, so it cannot be mistaken for a calm one.
				recent := ackwatch.RecentFires(schedulerLog, now, ackwatch.DefaultBlackoutWindow)
				// The liveness gate. "Fires delivered, nothing completed" is also
				// what an EMPTY fleet looks like — every night on this box between
				// midnight and 09:30 — so the blackout arm judges running agents
				// only, and reports itself blind rather than guessing when this
				// set is unavailable.
				// Start times matter as much as status: an agent is judged only
				// once it has been up for the whole window, or the window reaches
				// back into a period when it did not exist while the scheduler was
				// delivering to its schedule the whole time.
				runningSince := map[string]time.Time{}
				if agentRegistry != nil {
					for _, a := range agentRegistry.List() {
						if a.Status == agent.StatusRunning {
							runningSince[a.Name] = a.StartTime
						}
					}
				}
				return ackwatch.Snapshot{
					Now:              now,
					Samples:          ackwatch.SampleEntries(sched.List(""), now),
					LastDisruption:   at,
					DisruptionReason: reason,
					Recent:           &recent,
					RunningSince:     runningSince,
				}, nil
			},
			Mail:                  client.SendMGMail,
			Interval:              cfg.AckWatch.Interval,
			RenotifyAfter:         cfg.AckWatch.RenotifyAfter,
			BlackoutRenotifyAfter: cfg.AckWatch.BlackoutRenotify,
			NotifyTo:              cfg.AckWatch.NotifyTo,
			EscalateAfter:         cfg.AckWatch.EscalateAfter,
			EscalateTo:            escalationBox,
			StartedAt:             time.Now(),
		})
		log.Printf("pogod: ack-watch enabled (interval=%s renotify=%s blackout_renotify=%s notify_to=%s escalate_to=%s escalate_after=%s, report-only)",
			cfg.AckWatch.Interval, cfg.AckWatch.RenotifyAfter, cfg.AckWatch.BlackoutRenotify,
			cfg.AckWatch.NotifyTo, escalationBox, cfg.AckWatch.EscalateAfter)
		conditions.Clear(rowA3AckWatchNotArmed, time.Now())
	} else if cfg.AckWatch.Enabled {
		log.Printf("pogod: ack-watch NOT armed — the scheduler did not load, so there are no completion counters to read")
		// A3 (mg-342d). The watcher that exists to notice "fires are being
		// DELIVERED but not COMPLETED" — the exact shape of the 23h30m outage of
		// 2026-07-22 — silently off, disabled by the failure it would have
		// caught. See conditionAckWatchNotArmed for why A3's other listed site
		// (`deaf-watch NOT armed`) is NOT wired: that branch is unreachable in
		// production, and the deaf-watch degradation that actually happens under
		// A2 is already on the event spine as deaf_watch_error.
		conditions.Raise(conditionAckWatchNotArmed(coordinator), time.Now())
	}

	// Build the missing-mail-loop ANNOUNCER (mg-032b). `pogo agent diagnose`
	// has judged this correctly since mg-de08 and completely since mg-738f, and
	// until now it was the only consumer: a subcommand that takes the agent's
	// NAME as an argument, which is the one thing an operator cannot know when
	// the fault is that an agent silently stopped answering. This runner applies
	// the SAME judgement (agent.Registry.MailLoopReport -> diagnose's own
	// mailLoopFor) across the whole registry and mails, so the fault is
	// observable from OUTSIDE the agent that failed.
	//
	// It is armed independently of the scheduler variable because its source
	// asks the REGISTRY, not the scheduler — but the registry can only answer
	// once SetMailCheckProvider has been called, which happens on the scheduler
	// path above. Without it MailLoopReport returns an error, which the watcher
	// records as deaf_watch_error rather than as a clean fleet. REPORT-ONLY.
	var deafWatcher *deafwatch.Watcher
	if cfg.DeafWatch.Enabled && agentRegistry != nil {
		deafWatcher = deafwatch.New(deafwatch.Options{
			Enabled:       true,
			Source:        deafwatch.RegistrySource(agentRegistry),
			Mail:          client.SendMGMail,
			Interval:      cfg.DeafWatch.Interval,
			HoldDown:      cfg.DeafWatch.HoldDown,
			RenotifyAfter: cfg.DeafWatch.RenotifyAfter,
			NotifyTo:      cfg.DeafWatch.NotifyTo,
			EscalateAfter: cfg.DeafWatch.EscalateAfter,
			EscalateTo:    escalationBox,
		})
		log.Printf("pogod: deaf-watch enabled (interval=%s hold_down=%s renotify=%s notify_to=%s escalate_after=%s escalate_to=%s, report-only)",
			cfg.DeafWatch.Interval, cfg.DeafWatch.HoldDown, cfg.DeafWatch.RenotifyAfter,
			cfg.DeafWatch.NotifyTo, cfg.DeafWatch.EscalateAfter, escalationBox)
	} else if cfg.DeafWatch.Enabled {
		log.Printf("pogod: deaf-watch NOT armed — the agent registry did not load, so there is nothing to judge")
	}

	// Build the ABSENT-AGENT announcer (mg-7d20). Every other standing detector
	// iterates the REGISTRY, so an agent that was stopped is not a row with a bad
	// value in it — it is no row at all, and no reader can tell "down" from
	// "never configured here". `crew-doctor` was stopped on 2026-08-10 during an
	// auth-incident cleanup and stayed down 2 days 21 hours: the stall-watch
	// reads a sweep log a stopped agent does not write, ackwatch had no schedule
	// left to under-complete (pogod reaped the mail-check at stop, reason=
	// agent_gone), deaf-watch's population is the registry it had left, and
	// `pogo agent list` cannot show a member that is not running. The one surface
	// that would have reported it — `pogo doctor --check` — is read on a cadence
	// by doctor and by nobody else, so the instrument for doctor's absence WAS
	// doctor.
	//
	// This runner's population is the CONFIGURED crew/mayor set
	// (agent.Registry.RosterReport), with presence as a property of each member
	// rather than the precondition for being looked at. It is deliberately
	// patient with an on-demand agent — being off is that class's ordinary state
	// — and impatient with a supervised one, because a detector that cries wolf
	// is the reason nobody builds the one that would work.
	//
	// Armed on the registry alone: the prompt tree is read at sample time, and an
	// unreadable one reports as absent_watch_error rather than as a complete
	// roster. REPORT-ONLY.
	var absentWatcher *absentwatch.Watcher
	if cfg.AbsentWatch.Enabled && agentRegistry != nil {
		absentWatcher = absentwatch.New(absentwatch.Options{
			Enabled:       true,
			Source:        absentwatch.RegistrySource(agentRegistry),
			Mail:          client.SendMGMail,
			Interval:      cfg.AbsentWatch.Interval,
			HoldDown:      cfg.AbsentWatch.HoldDown,
			DormantAfter:  cfg.AbsentWatch.DormantAfter,
			RenotifyAfter: cfg.AbsentWatch.RenotifyAfter,
			NotifyTo:      cfg.AbsentWatch.NotifyTo,
			EscalateAfter: cfg.AbsentWatch.EscalateAfter,
			EscalateTo:    escalationBox,
		})
		log.Printf("pogod: absent-watch enabled (interval=%s hold_down=%s dormant_after=%s renotify=%s notify_to=%s escalate_after=%s escalate_to=%s, report-only)",
			cfg.AbsentWatch.Interval, cfg.AbsentWatch.HoldDown, cfg.AbsentWatch.DormantAfter,
			cfg.AbsentWatch.RenotifyAfter, cfg.AbsentWatch.NotifyTo, cfg.AbsentWatch.EscalateAfter, escalationBox)
	} else if cfg.AbsentWatch.Enabled {
		const reason = "the agent registry did not load, so there is nothing to compare the configured set against"
		log.Printf("pogod: absent-watch NOT armed — %s", reason)
		// And on the EVENT SPINE, not only on stderr — the same reasoning as the
		// first-turn floor below, and here it is the detector's own defect
		// arriving one level up. This runner exists because an instrument that
		// reads green over a missing thing is worse than no instrument. A runner
		// that was never armed emits nothing, finds nothing, and is
		// indistinguishable from one that is running over a complete roster; and
		// pogod logs to inherited stderr, which is how four months of pogod.log
		// held zero lines for events that were in the running binary the whole
		// time. Silence about the detector is the same failure as silence about
		// the agent.
		events.Emit(context.Background(), events.Event{
			EventType: absentwatch.EventError,
			Agent:     "pogod",
			Details: map[string]any{
				"error": reason,
				"phase": "arm",
				"why":   "absent-watch could not be armed at startup; no absence will be reported until pogod is restarted with an agent registry",
			},
		})
	}

	// Build the FIRST-COMPLETED-TURN floor (mg-3cbb): the runner that alarms
	// when pogod has spawned a crew agent and that agent has never finished a
	// single scheduled fire since.
	//
	// A SPAWN IS NOT A SUCCESS. On 2026-08-11 this daemon logged `autostart:
	// started X (pid=N)` five times at 03:01 local, re-registered every
	// mail-check schedule, and passed its own post-check ("5 mail-check
	// schedule(s) present") ninety seconds later — over a fleet that then
	// completed zero turns for seventeen hours. Everything pogod asserted was
	// true. None of it was evidence of an agent being alive.
	//
	// This does NOT replace ackwatch's blackout arm above, which fired 33
	// consecutive times through that outage and was right every time. That arm
	// judges a completion RATIO over a trailing window and so cannot speak about
	// an agent until it has been up for the whole window — 3h02m after the
	// bounce, measured. This one speaks at 45 minutes, on the same event log.
	// was-alive-then-went-dark and was-never-alive are different failures with
	// different earliest-detectable moments, and each arm owns one.
	//
	// Armed on the registry AND the scheduler log: without the first there is no
	// population, without the second no evidence. Both absences report as BLIND
	// rather than as a clean fleet. REPORT-ONLY.
	var firstTurnWatcher *firstturn.Watcher
	if cfg.FirstTurn.Enabled && agentRegistry != nil && sched != nil {
		schedulerLog := scheduler.EventLogPath(schedPath)
		firstTurnWatcher = firstturn.New(firstturn.Options{
			Enabled: true,
			Source: func(now time.Time) firstturn.Snapshot {
				crew, scanned, ok := firstturn.CrewAgents(agentRegistry)
				if !ok {
					return firstturn.Snapshot{Now: now, Err: "agent registry unavailable"}
				}
				// The evidence read is anchored at the OLDEST spawn in the
				// population rather than at a fixed window, so it costs a short
				// scan on a healthy fleet and grows only while an outage does —
				// which is the one time the extra reach is what makes the claim
				// provable.
				since := firstturn.EarliestStart(crew, now, firstturn.DefaultLookback)
				ev, readErr := firstturn.ReadEvidence(schedulerLog, since, now)
				return firstturn.Attach(crew, ev, now, scanned, firstturn.DefaultLookback, readErr)
			},
			Mail:     client.SendMGMail,
			Interval: cfg.FirstTurn.Interval,
			Params:   firstturn.Params{Grace: cfg.FirstTurn.Grace},
			NotifyTo: cfg.FirstTurn.NotifyTo,
			// The fleet-wide case goes here, immediately and structurally. The
			// mayor is inside every fleet outage in this system's history
			// (mg-e2a4), so a notice that only reaches it is not a weaker alert
			// — it is no alert.
			EscalateTo: escalationBox,
			StartedAt:  time.Now(),
		})
		log.Printf("pogod: first-turn floor enabled (interval=%s grace=%s notify_to=%s escalate_to=%s, report-only)",
			cfg.FirstTurn.Interval, cfg.FirstTurn.Grace, cfg.FirstTurn.NotifyTo, escalationBox)
	} else if cfg.FirstTurn.Enabled {
		reason := firstTurnUnarmedReason(agentRegistry != nil, sched != nil)
		log.Printf("pogod: first-turn floor NOT armed — %s", reason)
		// And on the EVENT SPINE, not only on stderr. pogod logs to inherited
		// stderr, which is how four months of pogod.log held zero lines for
		// events that were in the running binary the whole time (mg-3cbb's own
		// lineage). A floor that is silently not running is indistinguishable
		// from a floor that is running and finding nothing — which is the exact
		// failure this whole package exists to end, one level up.
		events.Emit(context.Background(), events.Event{
			EventType: firstturn.EventBlind,
			Agent:     "pogod",
			Details: map[string]any{
				"reason":  reason,
				"scanned": 0,
				"why":     "the first-turn floor could not be armed at startup; it will judge nothing until pogod is restarted with both dependencies present",
			},
		})
	}

	// Build the WEDGED-AGENT detector (mg-fc8d). On 2026-08-04 twelve polecats
	// and the doctor crew agent sat at a Claude Code login prompt for thirteen
	// hours; on 2026-08-05 it recurred for seven. Roughly twenty agent-hours of
	// nothing, and every liveness instrument in this daemon read healthy for the
	// whole of both windows — because the agents were ANIMATING. Claude Code
	// redraws a spinner while parked at a prompt, so `last-activity` (PTY
	// writes) said "just now" forever, the process was alive so status said
	// running, and CPU was near zero, which is also what a legitimately blocked
	// agent looks like.
	//
	// This runner reads what none of those instruments read: the CONTENT of each
	// agent's PTY, and the agent's own declared work counter beside its process
	// uptime. See internal/wedgewatch for why the cross-check gates on that
	// counter being frozen rather than on the raw ratio, and why a 401 shortly
	// after a connectivity failure is ONE signature rather than a revoked
	// credential.
	//
	// REPORT-ONLY, and unusually strictly: no mail, because mg-fc8d item (3) —
	// escalating a fleet-level wedge OUTSIDE the wedged party — is an
	// alerting-policy decision reserved to Daniel and is deliberately not built.
	// The findings go to the event log and to this daemon's log; wiring a
	// recipient is a later, separate change.
	// The TURN-COMPLETION reader (mg-a270). Every crew agent appends one line
	// per completed turn to ~/.pogo/agents/turnlog/<name>.log — an artifact
	// nothing but a finished turn produces — and this reads them against the
	// registry's population.
	//
	// IT LIVES HERE FOR ONE REASON, and it is the whole point of the amendment
	// that added it: every fleet-wide scheduled check on this machine is
	// coordinator-owned, so a detector that routes through the coordinator
	// cannot report the coordinator being down. That circularity, not a
	// mistuned threshold, is why a 22-hour fleet outage read green on
	// 2026-08-10/11. pogod is the only participant that is not a crew agent.
	// A nightly crew reader was offered and refused: its ~24h worst-case
	// latency is the same order as the outages it would detect.
	//
	// The routing rule is in turnwatch.recipients — a finding about the
	// coordinator goes to the escalation box, never to the coordinator.
	// REPORT-ONLY: it mails and emits, with no seam to nudge or restart.
	var turnWatcher *turnwatch.Watcher
	if agentRegistry != nil {
		turnWatcher = turnwatch.New(turnwatch.Options{
			Enabled: true,
			Scan: func(now time.Time) (turnlog.Report, error) {
				return turnlog.Scan(turnlog.Options{
					Now: now,
					Population: func() ([]turnlog.Present, error) {
						// Crew only. Polecat prompts carry no turn-completion
						// clause — their work is evidenced by the claim
						// re-stamp, the branch and the merge — so including
						// them would produce a permanent red that means
						// nothing, which is how a detector becomes ignorable.
						var out []turnlog.Present
						for _, a := range agentRegistry.List() {
							if a.Type != agent.TypeCrew {
								continue
							}
							if a.Status != agent.StatusRunning && a.Status != agent.StatusRestarting {
								continue
							}
							out = append(out, turnlog.Present{
								Name: a.Name, Type: string(a.Type), StartedAt: a.StartTime,
							})
						}
						return out, nil
					},
				})
			},
			Mail:        client.SendMGMail,
			Coordinator: coordinator,
			HumanBox:    escalationBox,
			StartedAt:   time.Now(),
		})
		log.Printf("pogod: turn-watch enabled (interval=%s max_age=%s grace=%s hold_down=%s; "+
			"coordinator findings route to %s, never to %s — report-only)",
			turnwatch.DefaultInterval, turnlog.DefaultMaxAge, turnwatch.DefaultGrace,
			turnwatch.DefaultHoldDown, escalationBox, coordinator)
	} else {
		// Said out loud, because a detector that is silently off is this
		// package's own subject matter one level up. Without the registry there
		// is no population, and turnlog.Scan refuses to substitute the file
		// listing for it — so the honest state is "not armed", not "clean".
		log.Printf("pogod: turn-watch NOT armed — no agent registry, so there is no population to " +
			"read turn-completion artifacts against. Nothing is watching whether crew agents complete turns")
	}

	var wedgeWatcher *wedgewatch.Watcher
	if cfg.WedgeWatch.Enabled && agentRegistry != nil {
		wedgeWatcher = wedgewatch.New(wedgewatch.Options{
			Enabled: true,
			Source: wedgewatch.RegistrySource(agentRegistry, wedgewatch.SystemCredential,
				wedgewatch.SystemHost, wedgewatch.SystemEvents),
			Interval:      cfg.WedgeWatch.Interval,
			RenotifyAfter: cfg.WedgeWatch.RenotifyAfter,
			Thresholds: wedgewatch.Thresholds{
				MarkerHoldDown:    cfg.WedgeWatch.MarkerHoldDown,
				FreezeHoldDown:    cfg.WedgeWatch.FreezeHoldDown,
				MinUptime:         cfg.WedgeWatch.MinUptime,
				Ratio:             cfg.WedgeWatch.Ratio,
				CoincidenceWindow: cfg.WedgeWatch.CoincidenceWindow,
			},
		})
		log.Printf("pogod: wedge-watch enabled (interval=%s marker_hold_down=%s freeze_hold_down=%s "+
			"min_uptime=%s ratio=%.0fx coincidence_window=%s, report-only, NOT routed — mg-fc8d item 3 is unruled)",
			cfg.WedgeWatch.Interval, cfg.WedgeWatch.MarkerHoldDown, cfg.WedgeWatch.FreezeHoldDown,
			cfg.WedgeWatch.MinUptime, cfg.WedgeWatch.Ratio, cfg.WedgeWatch.CoincidenceWindow)
	} else if cfg.WedgeWatch.Enabled {
		log.Printf("pogod: wedge-watch NOT armed — the agent registry did not load, so there are no PTYs to read")
	}

	// The done-item polecat reaper (mg-56d1): completion frees a slot, however
	// completion happened. The merge hook above covers polecats whose deliverable
	// is a branch; this covers the triage / audit / investigation polecats that
	// produce no merge, call `mg done` themselves, and until now held a slot
	// until a coordinator noticed. See cmd/pogod/donereap.go for why the
	// condition is item-done AND idle, and why it polls rather than hooks.
	//
	// The only ACTING detector on this tick. Its action is bounded to stopping a
	// polecat whose work is provably concluded.
	// Tell the agent that COMMISSIONED a work item when it completes (mg-f120).
	// Built here, once, and shared by the two observers of a close — the merge
	// reap below and the done-item reaper — so that one completion seen from two
	// angles produces one mail. See internal/filernotify for the decision rules
	// and for why the dedup key is the item plus its result sidecar.
	filerNotify := newFilerNotifier(coordinator, agentRegistry)
	log.Printf("pogod: work-item completion notifier armed — the item's Creator is mailed at close, so a commissioning agent "+
		"no longer depends on the worker volunteering a report (mg-f120); coordinator=%s", coordinator)

	var doneReap *doneReaper
	if cfg.DoneReap.Enabled && agentRegistry != nil {
		doneReap = newDoneReaper(agentRegistry, client.MGWorkItemDone, client.MGWorkItemReviews, cfg.DoneReap.IdleGrace)
		doneReap.SetFilerNotifier(filerNotify)
		log.Printf("pogod: done-item polecat reaper enabled (idle_grace=%s) — a polecat whose item reaches done is stopped once it goes quiet, merge or no merge (mg-56d1); "+
			"a builder is exempt while a live polecat's item declares `reviews:` its work item (mg-aaf6)",
			cfg.DoneReap.IdleGrace)
	} else if cfg.DoneReap.Enabled {
		log.Printf("pogod: done-item polecat reaper NOT armed — the agent registry did not load, so there is nothing to reap")
	}

	// The restart obligation on a stopped fleet (mg-5af1). Armed at the stop by
	// internal/server, fired here — deliberately in pogod rather than in
	// whatever stopped the fleet, because the whole defect is that the stopper
	// cannot be trusted to reach its own step 2.
	//
	// It resolves `srv` late, through a closure: this OnTick is built several
	// hundred lines BEFORE server.New runs, so a resumer holding the pointer
	// would hold nil for the life of the process.
	var orchResume *orchResumer
	if cfg.OrchestrationResume.Enabled {
		orchResume = newOrchResumer(func() orchResumeServer {
			if srv == nil {
				return nil
			}
			return srv
		}, conditions, coordinator, cfg.OrchestrationResume)
		log.Printf("pogod: orchestration resume deadline armed (grace=%s) — a fleet left stopped past its deadline is restarted and the coordinator is mailed (mg-5af1)",
			cfg.OrchestrationResume.Grace)
	} else {
		log.Printf("pogod: orchestration resume deadline DISABLED by config — a fleet stopped by a procedure that dies stays stopped until a human notices (this is the 2026-08-08 configuration)")
	}

	// Drive both heartbeat-piggybacked subsystems from a single OnTick. The
	// scheduler runs inline (it stores absolute fire times, so a clock jump is
	// absorbed in the same goroutine). The stall watcher runs in a goroutine so
	// a wait-idle nudge — which can block up to DefaultNudgeTimeout — never
	// delays the next tick or the scheduler sweep; its per-category cooldown and
	// internal mutex keep overlapping checks safe.
	// pogodHeartbeatPath is pogod's OWN heartbeat file. The tier-1 reaper can
	// supervise every com.pogo.* job EXCEPT pogod itself (a child agent cannot
	// reap its parent, and launchd will not — mg-50e0). Publishing pogod's
	// heartbeat here, on every heartbeat tick, gives an external human-held
	// check (the digest, or bridget once threading is on) a way to DETECT a
	// dead pogod. This is detection, not recovery: the known single point of
	// failure this tier explicitly leaves open. See docs/design/reaper-design.md.
	pogodHeartbeatPath := filepath.Join(config.PogoHome(), "health", "pogod.heartbeat")
	hb.OnTick = func(now time.Time) {
		if err := reaper.WriteHeartbeat(pogodHeartbeatPath); err != nil {
			log.Printf("pogod: failed to write own heartbeat %s: %v", pogodHeartbeatPath, err)
			// A11 (mg-342d). This one is rate-limit-critical: the tick is every
			// ~30s, so without the annunciator's per-condition floor this would
			// mail ~2900 times a day. The floor is why it is safe to wire at all.
			conditions.Raise(conditionHeartbeatWriteFailed(coordinator, pogodHeartbeatPath, err.Error()), now)
			conditions.flush()
		} else {
			conditions.Clear(rowA11HeartbeatWrite, now)
			conditions.flush()
		}
		// Deliver any queued PTY wake (A2). Queued rather than sent inline
		// because the wake-needing conditions are detected during startup,
		// before crew auto-start — at the moment A2 is known there is no
		// coordinator process to nudge yet. This tick is also the proof that the
		// wake channel does not depend on the failed subsystem: the heartbeat
		// drives the scheduler (`if sched != nil` below), not the reverse.
		conditions.retryWakes(now)
		if sched != nil {
			sched.Tick(context.Background(), now)
		}
		if stallWatcher != nil {
			go stallWatcher.Check(now)
		}
		// The drift-check runner rides the same tick but throttles itself to a
		// COARSE interval (its own lastRun gate), so it samples at most once per
		// DriftWatch.Interval no matter how often this fires. In a goroutine
		// because CheckDrift shells out to launchctl/ps and the mail send shells
		// out to `mg mail send` — neither must delay the next tick. Report-only.
		if driftWatcher != nil {
			go driftWatcher.Check(now)
		}
		// The gh-issue teardown detector rides the same tick and throttles
		// itself to a COARSE interval. In a goroutine because it shells out to
		// `mg` once per carrier and to `gh` over the network — neither must
		// delay the next tick. Report-only.
		if teardownWatcher != nil {
			go teardownWatcher.Check(now)
		}
		// The gh-issue INTAKE detector rides the same tick on its own coarse
		// interval. In a goroutine because it scans every work item in the store
		// via `mg show` and lists issues over the network — neither must delay the
		// next tick. Report-only: it mails, and it has no seam through which it
		// could file a work item or comment on an issue.
		if intakeWatcher != nil {
			go intakeWatcher.Check(now)
		}
		// The review-declaration detector rides the same tick on its own coarse
		// interval. In a goroutine because it walks four status directories and
		// shells out to `mg mail send` on a finding — neither must delay the next
		// tick. Report-only: it mails, and it has no seam through which it could
		// write the `reviews:` line whose absence it reports.
		if reviewDeclWatcher != nil {
			go reviewDeclWatcher.Check(now)
		}
		// The completion-deficit detector rides the same tick and throttles
		// itself to a COARSE interval. In a goroutine because a finding shells
		// out to `mg mail send`, which must never delay a tick. Report-only: it
		// mails, and it has no seam through which it could nudge or restart the
		// agent it names.
		if ackWatcher != nil {
			go ackWatcher.Check(now)
		}
		// The missing-mail-loop announcer rides the same tick and throttles
		// itself to a coarse interval. In a goroutine because an announcement
		// shells out to `mg mail send`, which must never delay a tick.
		// Report-only: it mails, and it has no seam through which it could
		// register a schedule, nudge, or restart the agent it names.
		if deafWatcher != nil {
			go deafWatcher.Check(now)
		}
		// The absent-agent announcer rides the same tick and throttles itself to
		// a coarse interval. In a goroutine because an announcement shells out
		// to `mg mail send`, which must never delay a tick. Report-only: it
		// mails, and it has no seam through which it could START the agent it
		// names — the reason an agent left is the part worth knowing.
		//
		// It sits BESIDE deafWatcher rather than inside it on purpose (mg-7d20).
		// The two are disjoint by population as well as by fault: deafwatch
		// iterates the REGISTRY and asks "can this running agent be woken",
		// absentwatch iterates the CONFIGURED SET and asks "is this agent here
		// at all". An agent that has left the registry is outside deafwatch's
		// population by construction, which is exactly how `crew-doctor` stayed
		// down for 2d21h with every instrument green.
		if absentWatcher != nil {
			go absentWatcher.Check(now)
		}
		// The first-completed-turn floor rides the same tick and throttles
		// itself to a coarse interval. In a goroutine because it scans the
		// scheduler event log and shells out to `mg mail send` on a finding —
		// neither must delay a tick. Report-only: it mails, and it has no seam
		// through which it could restart or respawn the agent it names.
		if firstTurnWatcher != nil {
			go firstTurnWatcher.Check(now)
		}
		// The turn-completion reader rides the same tick and throttles itself
		// to a coarse interval. In a goroutine because a finding shells out to
		// `mg mail send`, which must never delay a tick. Report-only.
		//
		// It sits BESIDE firstTurnWatcher rather than inside it on purpose
		// (mg-a270 / mg-3cbb). The two answer different questions on different
		// evidence: that one asks "has this agent completed a turn since it was
		// spawned" from scheduler acks with a 45-minute grace, this one asks
		// "has it completed one in the last N" from the agent-written turnlog
		// with a 3-hour floor. Neither threshold serves the other question, and
		// keeping the evidence sources separate means an agent that stops
		// writing turnlog lines while still acking stays visible, and the
		// converse.
		if turnWatcher != nil {
			go turnWatcher.Check(now)
		}
		// The wedged-agent detector rides the same tick and throttles itself to
		// a coarse interval. In a goroutine because it scans every agent's PTY
		// ring and, when an auth symptom appears, shells out to `security` for
		// the credential's expiry — which can block on a keychain
		// authorization prompt and must never delay a tick. Report-only, and
		// with no mail seam at all: see the wiring above for why.
		if wedgeWatcher != nil {
			go func(now time.Time) {
				wedgeWatcher.Check(now)
				logWedgeFindings(wedgeWatcher, now)
			}(now)
		}
		// The synthetic-failure-turn detector rides the same tick and throttles
		// itself to synthwatch.DefaultInterval. In a goroutine because it reads
		// transcript files off disk and shells out to `mg mail send` on a hit —
		// neither must delay the next tick. Page-only.
		go synthWatcher.Check(now)
		// The credential-expiry warner rides the same tick and throttles itself
		// to a COARSE interval. In a goroutine because it shells out to
		// `security` (which can block on a keychain authorization prompt, hence
		// its own internal timeout) and to `mg mail send` — neither must delay
		// the next tick. Report-only: it warns, it never re-mints.
		if credWatcher != nil {
			go credWatcher.Check(context.Background(), now)
		}
		// Surface polecats that outlived an earlier pogod and are now alive but
		// unreachable — UNKNOWN forever, holding a worktree and a claim, with
		// nothing else in the tree looking at them (mg-0b77). Only a human can
		// resolve one, so this reports rather than acts. In a goroutine because
		// the alert shells out to `mg mail send`, which must never delay a tick.
		if agentRegistry != nil {
			go agentRegistry.ReportOrphanedPolecats()
		}
		// Stop polecats whose work item has concluded and which have gone quiet
		// (mg-56d1). In a goroutine because it shells out to `mg show` once per
		// live polecat and then to Stop, which blocks on SIGTERM — neither must
		// delay the next tick. It self-serialises, so overlapping ticks cannot
		// issue a duplicate Stop. Deliberately NOT throttled to a coarser
		// interval than the tick: the whole cost of this defect is measured in
		// slot-seconds, and the grace window already bounds how eagerly it acts.
		if doneReap != nil {
			go doneReap.Check(now)
		}
		// Restore a fleet whose stopper never came back (mg-5af1). In a
		// goroutine because the restore runs the crew auto-start sweep and the
		// alarm shells out to `mg mail send` — neither must delay the next
		// tick. It self-throttles and cannot act while the mode is full, so an
		// overlapping tick cannot issue a duplicate restart.
		if orchResume != nil {
			go orchResume.Check(now)
		}
	}

	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	go hb.Run(hbCtx)

	// Start the polecat git garbage collector: a startup sweep plus a
	// periodic ticker that deletes stale polecat-* branches and reclaims
	// leaked worktrees once their work items have concluded. mg-30d5.
	startGitGC(hbCtx, agentRegistry, cfg.GitGC, coordinator)

	// Reclaim the per-spawn prompt files of polecats a previous pogod died
	// holding. The same gap as the git GC's startup sweep, at the same moment,
	// and deliberately not on its ticker or behind its flag. mg-5197.
	sweepExpandedPromptsAtStartup(agentRegistry)

	// Start the tier-1 heartbeat reaper: a goroutine (NOT a LaunchAgent — the
	// wedge in mg-50e0 means we cannot rely on being spawned) that kickstarts
	// declared launchd jobs whose heartbeat state file has gone stale. Liveness
	// is heartbeat freshness, never process existence. mg-d18b.
	startReaper(hbCtx, cfg.Reaper)

	// Optional platform-specific wake notifier — reduces wake-event latency
	// from up-to-Interval (~30s) down to <1s by short-circuiting the
	// heartbeat tick when the OS reports a wake. Strict performance
	// optimization: hb alone is correct; an error here is logged and we
	// continue. See internal/platform/sleep and
	// docs/sleep-resilience-design.md §5.
	if err := sleep.Watch(hbCtx, hb.Nudge); err != nil {
		log.Printf("pogod: platform sleep shim unavailable: %v (heartbeat-only wake detection still active)", err)
	}

	// Start plugins
	driver.Init()

	defer driver.Kill()
	defer project.SaveProjects()

	// Load project list from disk (fast, no indexing)
	project.Init()

	// Prune stale registry entries — nonexistent paths and ephemeral
	// worktrees — before any indexing runs (mg-d205).
	project.PruneRegistry()

	// Start refinery merge queue loop
	refineCfg := refinery.DefaultConfig()
	if cfg.Refinery.Enabled {
		if cfg.Refinery.PollInterval > 0 {
			refineCfg.PollInterval = cfg.Refinery.PollInterval
		}
		if cfg.Refinery.MaxConcurrentMerges > 0 {
			refineCfg.MaxConcurrentMerges = cfg.Refinery.MaxConcurrentMerges
		}
		var refErr error
		mergeQueue, refErr = refinery.New(refineCfg)
		if refErr != nil {
			fmt.Printf("Warning: refinery failed to start: %v\n", refErr)
		} else {
			mergeQueue.SetOnMerged(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: merged %s (branch=%s, author=%s)", mr.ID, mr.Branch, mr.Author)

				// Ask the WORK ITEM whether merging completes it before
				// acting as though it does (mg-d86e). Resolved here, once,
				// rather than inside the reap: the mail below has to say the
				// same thing the reap decided, and two probes could disagree.
				// Synchronous — it is one local `mg show` — and a no-op for
				// merges the reap path ignores anyway.
				postMerge := resolvePostMergeWork(agentRegistry, mr, client.MGWorkItemDeclaresPostMergeWork)

				// Event-driven polecat stop (gh #35): mark the work item
				// done and stop the merged polecat now instead of waiting
				// for the mayor's next coordination cycle. Run async — the
				// stop can block up to its SIGTERM timeout and this
				// callback fires on the refinery loop.
				go reapMergedPolecat(agentRegistry, mr, client.CloseMGWorkItemAtMerge, postMerge, deferBackstop, filerNotify)

				// Mail the coordinator so it can archive the work item and
				// handle QA. The mayor's reap loop stays as a backstop for
				// polecats the event-driven stop above misses (e.g. a merge
				// resolved while pogod was down).
				// A failed post-merge step is loud in the SUBJECT, not buried in
				// the body. The merge succeeded, so a subject-scanning reader
				// would otherwise file this next to every other MERGED mail
				// while the release it describes is half-finished (mg-6879).
				subject := fmt.Sprintf("MERGED: %s (branch=%s)", mr.ID, mr.Branch)
				if mr.PostMergeError != "" {
					subject = fmt.Sprintf("MERGED but POST-MERGE STEP FAILED: %s (branch=%s)", mr.ID, mr.Branch)
				}
				body := fmt.Sprintf("Merge request %s succeeded.\nBranch: %s\nTarget: %s\nAuthor: %s", mr.ID, mr.Branch, mr.TargetRef, mr.Author)
				// Name the commit. Every question about a merge downstream of
				// this mail ("was the tag on the right SHA", "what shipped")
				// starts here, and the SHA used to be unreachable (mg-6879).
				if mr.MergedSHA != "" {
					body += fmt.Sprintf("\nMerged SHA: %s", mr.MergedSHA)
				}
				// Say plainly which kind of merge this was. The mayor has to
				// tell "merged to the default branch, done" from "merged to an
				// integration branch, PR still pending" and was previously left
				// to infer it (mg-7746).
				if mr.PRFlow {
					body += fmt.Sprintf("\nPR flow: YES — %s is an integration branch, not the repo default."+
						"\nThis merge is NOT completion: the author still has to open the PR to the default branch."+
						"\nThe work item stays claimed and the polecat keeps running; it calls `mg done` itself once the PR is open.",
						mr.TargetRef)
				}
				// Same obligation for an item that declared its own remainder
				// (mg-d86e). Without this line the mail reads as unqualified
				// success on exactly the tickets whose work has not happened
				// yet — which is how two releases were reported complete with
				// no tag, and why the mail is not evidence on its own.
				if postMerge.Declared {
					body += fmt.Sprintf("\nCompletion: NOT THIS MERGE — %s"+
						"\nThe work item stays claimed and the polecat keeps running; it calls `mg done` itself once its remaining work finishes."+
						"\nDo NOT archive this item on the strength of this mail: check its acceptance criteria.",
						postMerge.Reason)
				}
				// Surface deploy failures to the mayor so the runtime gap (merged
				// commit but stale binary) gets remediated. The merge has already
				// landed — only the post-merge deploy hook failed.
				if mr.DeployError != "" {
					body += fmt.Sprintf("\nDeploy: FAILED — %s", mr.DeployError)
				}
				// The post-merge step the refinery performed on the author's
				// behalf (mg-6879). Reported in both directions: the success
				// line is the record that the deliverable exists, which is what
				// makes "merged" trustworthy for a release cut, and the failure
				// line is an explicit ask because nothing else will chase it —
				// the work item is deliberately NOT done, so the 15-minute
				// backstop is the only other actor and it reports a stalled
				// polecat rather than a missing tag.
				if mr.PostMergeTag != "" && mr.PostMergeError == "" {
					body += fmt.Sprintf("\nPost-merge tag: %s pushed to origin at %s (performed by the refinery, not the polecat).", mr.PostMergeTag, mr.MergedSHA)
				}
				if mr.PostMergeError != "" {
					body += fmt.Sprintf("\n\nPOST-MERGE STEP FAILED — %s"+
						"\nThe merge LANDED and is not unwound; its follow-on step did not run to completion."+
						"\nThe work item is deliberately NOT marked done and the polecat is still running."+
						"\nACTION NEEDED: fix the step by hand (for a tag: confirm which commit should carry it, then create and push it), or re-dispatch."+
						"\nDo NOT archive this item — `merged` here does not mean the deliverable exists.",
						mr.PostMergeError)
				}
				if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
					log.Printf("refinery: failed to mail coordinator: %v", err)
				}
			})
			mergeQueue.SetOnFailed(func(mr *refinery.MergeRequest) {
				log.Printf("refinery: failed %s (branch=%s, author=%s, status=%s, attempts=%d, error=%s, consecutive_author_failures=%d)",
					mr.ID, mr.Branch, mr.Author, mr.StatusLabel(), mr.AttemptCount, mr.Error, mr.FailureCount)

				// The CLASS goes in the SUBJECT, not only in the body (mg-e5c2).
				// A subject line is the part of a mail that travels: it is what
				// shows in a list, and on 2026-08-05 thirty-one identical
				// "MERGE FAILED" subjects were what invited thirty-one dispatches
				// for defects that did not exist.
				subject := fmt.Sprintf("MERGE FAILED (%s): %s (branch=%s)",
					strings.ToUpper(refineryFailureClassLabel(mr)), mr.ID, mr.Branch)
				body := fmt.Sprintf("Merge request %s failed.\nBranch: %s\nAuthor: %s\nStatus: %s\n%s\nAttempts: %s\nError: %s\nGate output: %s\nConsecutive failures by this author: %d\n%s",
					mr.ID, mr.Branch, mr.Author, mr.StatusLabel(), mr.FailureClass.TriageNote(),
					refineryAttemptSummary(mr), mr.Error, mr.GateOutput, mr.FailureCount,
					refineryAttemptDetail(mr))

				// Mail the author so they can fix and resubmit — and address the
				// AGENT that owns the branch, not only the work-item id
				// (mg-1fcc). `mr.Author` is "mg-32e3"; the running polecat reads
				// "c32e3". Those are different mailboxes, so the notice used to
				// land unread in a box its addressee does not poll. Polling the
				// refinery is the working channel and nothing was ever stranded
				// by this, but a polecat that was stopped or is past its polling
				// phase has no channel at all — which is the gap this closes.
				route := routeRefineryFailMail(mr, func(author string) string {
					if a := agentRegistry.GetByWorkItemOrName(author); a != nil {
						return a.Name
					}
					return ""
				})
				var undelivered []string
				for _, to := range route.Recipients {
					if err := client.SendMGMail(to, "refinery", subject, body); err != nil {
						log.Printf("refinery: failed to mail %s: %v", to, err)
						undelivered = append(undelivered, to)
					}
				}
				// A notice that reached no agent-owned mailbox is recorded rather
				// than left to look like a successful delivery.
				if ev, ok := refineryFailRouteEvent(mr, route, undelivered); ok {
					log.Printf("refinery: MERGE FAILED notice for %s did not route cleanly (agent_notified=%v): %v",
						mr.ID, ev.Details["agent_notified"], ev.Details["reason"])
					events.Emit(context.Background(), ev)
				}

				// Mail the coordinator so they can re-dispatch if the author exited.
				if err := client.SendMGMail(coordinator, "refinery", subject, body); err != nil {
					log.Printf("refinery: failed to mail coordinator: %v", err)
				}

				// Escalation: if the failure threshold has been reached, send
				// a separate alert to the mayor so it can intervene (e.g. stop
				// the polecat, reassign the work item).
				if mr.ThresholdReached {
					escSubject := fmt.Sprintf("FAILURE THRESHOLD REACHED: %s (%d consecutive failures)", mr.Author, mr.FailureCount)
					escBody := fmt.Sprintf("Author %s has failed %d consecutive merge attempts.\nLatest MR: %s\nBranch: %s\nError: %s\nConsider stopping the polecat or reassigning the work item.",
						mr.Author, mr.FailureCount, mr.ID, mr.Branch, mr.Error)
					if err := client.SendMGMail(coordinator, "refinery", escSubject, escBody); err != nil {
						log.Printf("refinery: failed to mail coordinator escalation: %v", err)
					}
				}

				// Auto-reopen the work item so it moves back to claimed/ for retry.
				// This keeps the item assigned to the original polecat.
				// Polecats use their work item ID as the author field.
				if mr.Author != "" {
					if err := client.ReopenMGWorkItem(mr.Author); err != nil {
						if errors.Is(err, client.ErrMGWorkItemNotDone) {
							// The item never left claimed/ — a live polecat still
							// owns it, which is the state the reopen wanted. All 18
							// of these in one 50,603-line log were this outcome
							// (mg-5d3f); reporting it as a failure taught readers
							// to skip error lines.
							log.Printf("refinery: work item %s already claimed (in progress), no reopen needed", mr.Author)
						} else {
							log.Printf("refinery: failed to reopen work item %s: %v", mr.Author, err)
						}
					} else {
						log.Printf("refinery: reopened work item %s after merge failure", mr.Author)
					}
				}
			})
			go mergeQueue.Start(context.Background())
			defer mergeQueue.Stop()
		}
	}

	// Initialize server coordinator
	srv = server.New(agentRegistry, mergeQueue)
	// The deadline every stop is measured against (mg-5af1). Set here rather
	// than defaulted inside the server so the value an operator reads in
	// config.toml is the value the daemon applies. A DISABLED resumer also
	// disarms the deadline, so `/server/mode` does not advertise a resume_due
	// that nothing will act on.
	if cfg.OrchestrationResume.Enabled {
		srv.SetResumeGrace(cfg.OrchestrationResume.Grace)
	} else {
		srv.SetResumeGrace(0)
	}
	// The agent-side analogue of SetRefineryStarter below: a return to full
	// mode re-runs the auto-start sweep. Before gh #108 there was no such
	// hook, so `pogo server start` against an index-only daemon flipped the
	// mode, brought the refinery back, and left the fleet empty.
	//
	// Since mg-060c it also runs when the daemon is ALREADY in full mode, which
	// is what makes `pogo server start` restore a crashed mayor rather than
	// print "already running" at a daemon with no coordinator.
	//
	// The two gates are the same ones the boot path applies further down — a
	// config file must exist, and [agents] autostart must be on. A daemon that
	// deliberately spawns no fleet must not gain a side door into doing so
	// through the CLI.
	srv.SetAgentStarter(agentStarterFor(
		func() bool { return cfg.Source != "" },
		func() bool { return cfg.Agents.AutoStart },
		agentRegistry.AutoStartAgents,
	))
	if mergeQueue != nil {
		onMerged := mergeQueue.OnMergedFunc()
		onFailed := mergeQueue.OnFailedFunc()
		srv.SetRefineryStarter(func() (*refinery.Refinery, error) {
			// refineCfg carries StatePath, so the fresh instance loads the
			// state the outgoing one flushed in Stop() — an orchestration
			// restart no longer empties the merge queue.
			newRef, err := refinery.New(refineCfg)
			if err != nil {
				return nil, err
			}
			if onMerged != nil {
				newRef.SetOnMerged(onMerged)
			}
			if onFailed != nil {
				newRef.SetOnFailed(onFailed)
			}
			mergeQueue = newRef
			go mergeQueue.Start(context.Background())
			return newRef, nil
		})
	}

	// Register HTTP handlers
	registerHandlers()

	// Start the HTTP listener BEFORE background indexing so the server
	// is immediately responsive to API calls (especially agent management).
	if *bindFlag != "" {
		cfg.Bind = *bindFlag
	}
	if *portFlag != 0 {
		cfg.Port = *portFlag
	}
	addr := cfg.ListenAddr()
	ln, listenErr := net.Listen("tcp", addr)
	if listenErr != nil {
		log.Fatalf("pogod: failed to listen on %s: %v", addr, listenErr)
	}
	fmt.Printf("pogod listening on %s\n", addr)

	// Now start background work: indexing and repo scanning.
	// The server is already accepting connections above.
	go func() {
		project.IndexAll()
		log.Printf("pogod: background project indexing complete")
	}()

	// Start the timer-driven incremental indexer: every index_interval it
	// scans index_roots for new repos and re-walks the registered projects
	// that are due. Per-project exponential backoff skips projects whose
	// content hasn't changed (up to 16× the base interval); a detected change
	// or a `pogo visit` resets a project to base cadence (mg-1236). The walk
	// itself is incremental — unchanged files cost only an Lstat. This
	// replaces the event-based filesystem watcher. See
	// docs/design/indexing-strategy.md and mg-5b0d.
	project.StartPeriodicIndexer(hbCtx, cfg.IndexInterval)

	// Prompt refresh and crew auto-start are gated on a config file existing.
	// A pogod with no config file is an unconfigured or deliberately isolated
	// instance (tests, CI, POGO_HOME sandboxes) — installing default prompts
	// and spawning a mayor from them would put an unrequested agent fleet on
	// the machine, and before mg-3dc3 an isolated daemon did exactly that,
	// racing the real crew. Orchestration is opt-in via config.toml.
	if cfg.Source == "" {
		log.Printf("pogod: no config file at %s; skipping prompt refresh and crew auto-start", config.ConfigFilePath())
	} else {
		// Refresh installed prompts from the embedded source before auto-starting
		// any agents. When a new pogo binary ships prompt updates, the live files
		// under $POGO_HOME/agents/ stay stale until something runs InstallPrompts —
		// previously only `pogo install` and `pogo agent prompt install`. Doing it
		// here means a daemon restart is enough to propagate updates, and the PMs
		// auto-started below pick up the latest prompts on the same boot. Hash
		// stamps make this a no-op when nothing changed.
		//
		// THIS IS ACT 3 of prompt activation, and it is the only thing that
		// performs it on this box. It is automatic and its cadence is every
		// daemon restart — which the nightly deploy guarantees at 03:00 local.
		// mg-b6bd read `grep -c 'prompt install' scripts/pogo-self-deploy` -> 0
		// and concluded nothing installs prompts; the grep is right and the
		// conclusion is wrong, because the deploy kickstarts pogod rather than
		// shelling out to the CLI. See promptrefreshrecord.go for the full
		// four-act map and for what mg-b6bd was actually missing: a record.
		promptRefreshRev := driftwatch.BuildRevision().Revision
		if installRes, err := agent.InstallPrompts(agent.InstallOpts{}); err != nil {
			log.Printf("pogod: prompt refresh failed: %v", err)
			// A4 (mg-342d), and strictly worse than the A1 that started this:
			// A1 is one prompt declined for a REASON (local edits pogod must not
			// overwrite), A4 is every prompt staying stale for no reason at all —
			// and before this it got less annunciation than A1 did.
			conditions.Raise(conditionPromptRefreshFailed(coordinator, err.Error()), time.Now())
			// On the record too. A failed act 3 leaves every prompt at its
			// previous content with no on-disk trace that anything was even
			// attempted — the one outcome where a reader most needs the record
			// to say "no, tonight it did not happen".
			events.Emit(hbCtx, promptRefreshEvent(nil, promptRefreshRev, err, time.Now()))
		} else {
			conditions.Clear(rowA4PromptRefresh, time.Now())
			for _, line := range promptRefreshLogLines(installRes, promptRefreshRev) {
				log.Print(line)
			}
			// The durable half, emitted unconditionally — including the
			// all-skipped boot the log lines above deliberately stay quiet for.
			// pogod.log is inherited stderr and is on no agent's schedule;
			// events.log is the file that is still there tomorrow and that
			// `pogo events list --type=prompt_refresh` can be pointed at.
			events.Emit(hbCtx, promptRefreshEvent(installRes, promptRefreshRev, nil, time.Now()))
			// The log lines above are correct and were never read: pogod.log is
			// on no agent's schedule, so a declined sync fired every boot for
			// seven days while the mayor ran stale guidance (mg-c3f0). Mail the
			// agent whose prompt was declined, here at the decision point,
			// while we still hold the conflict set in-process — and note that
			// this runs BEFORE the auto-start sweep below, so the notice is
			// already in that agent's maildir when it starts.
			notifyPromptSyncConflicts(installRes, coordinator, agent.PromptDir(),
				promptSyncNoticesPath(), time.Now(), client.SendMGMail)
		}

		// The role-default pin used to live here, between prompt refresh and
		// auto-start. It now runs in pinAndResolveRoles(), immediately after
		// config.Load() — the prompts refreshed just above are synthesized with
		// the process-wide role names, and the sweep below auto-starts an agent
		// named after the coordinator, so both must see the PINNED names, not
		// the freshly-flipped Default* consts (mg-bc47).

		// Auto-start crew agents whose prompt frontmatter declares auto_start = true.
		// This replaces the manual `pogo agent start mayor` step on a fresh boot
		// and is idempotent — agents already registered (e.g. across pogod
		// restart-while-running) are skipped. [agents] autostart = false (or
		// POGO_AGENT_AUTOSTART=false) turns the whole sweep off for daemons
		// that are configured but must not spawn a fleet — sandboxes and
		// tests (mg-9a1c). Prompt refresh above still runs: it only writes
		// files, it doesn't start anything.
		if !cfg.Agents.AutoStart {
			log.Printf("pogod: crew auto-start disabled ([agents] autostart = false); not starting any agents")
		} else {
			for _, res := range agentRegistry.AutoStartAgents() {
				switch res.Status {
				case agent.AutoStartStatusStarted:
					log.Printf("pogod: auto-started %s", res.Name)
					conditions.Clear(rowA5AutoStartPrefix+res.Name, time.Now())
				case agent.AutoStartStatusSkippedRunning:
					log.Printf("pogod: %s already running, skipping auto-start", res.Name)
					conditions.Clear(rowA5AutoStartPrefix+res.Name, time.Now())
				case agent.AutoStartStatusFailed:
					log.Printf("pogod: auto-start of %s failed: %s", res.Name, res.Error)
					// A5 (mg-342d), the row the enumeration flagged as genuinely
					// hard. When the agent that failed to start is NOT the
					// coordinator, this is solved: the coordinator is mailed and
					// can start it. When it IS the coordinator, the actor and the
					// casualty are the same process and there is no in-fleet
					// reader by construction — the mail still goes to its
					// maildir, where it is read on the first mail-check after the
					// coordinator next starts, and the notice says plainly that
					// nobody read it at the time. See conditionAutoStartFailed
					// for why the answer is NOT to fall back to `human`.
					conditions.Raise(conditionAutoStartFailed(
						coordinator, res.Name, res.Error, res.Name == coordinator), time.Now())
				}
			}
		}
	}

	// Open the mail-check reap's startup grace (after the settle window). This
	// sits OUTSIDE the branches above on purpose: every startup path must reach
	// it, including the two that deliberately start nothing (no config file,
	// [agents] autostart = false). Those daemons still supervise polecats, and
	// a gate that never opens would never reap a dead polecat's mail-check
	// loop — trading mg-de08 for the orphan-nudge accumulation the reap exists
	// to prevent (mg-de08 PART B).
	gcGate.markAutoStartComplete(time.Now())

	// Sweep for pushed work stranded by a polecat that outlived a PREVIOUS pogod
	// (mg-be37). This is the one population the release gate cannot reach: it
	// reports from releasePolecatClaim, which needs a registry entry, and the
	// registry is in-memory with no adopt path (see the comment above), so a
	// polecat that was running when this daemon's predecessor died is
	// un-instrumented for the whole rest of its life — a later graceful stop
	// included.
	//
	// HERE, AND NOT ON THE HEARTBEAT. The trigger is the restart because the
	// restart is what CREATES the uncovered set; a clock would only be guessing
	// at when that happened, and re-mailing the same branches every tick. This
	// call sits outside the auto-start branches for the same reason
	// markAutoStartComplete does: every startup path must reach it, including
	// the two that deliberately start nothing. A daemon that supervises polecats
	// without starting crew still inherits its predecessor's survivors.
	//
	// In a goroutine because it fetches and inspects git repositories and then
	// shells out to `mg mail send` — boot must not wait on the network, and a
	// mid-outage restart is exactly when this is slowest and when it matters
	// most.
	if agentRegistry != nil {
		go agentRegistry.ReportStrandedWorkAcrossRestart()
	}

	// Close out the boot's annunciation: persist the transition store and put the
	// counts on the log and the event spine (mg-342d).
	//
	// This runs on EVERY boot, including the clean ones, and that is the point.
	// It is the answer to "how would you know the annunciator itself had stopped
	// working" — a daemon that boots and emits no pogod_condition_summary is a
	// daemon where this mechanism is not running at all, which is a different and
	// checkable shape from a daemon with no conditions to report. A notifier that
	// can only be observed when it fires is a notifier whose silence means
	// nothing.
	conditions.report()

	// Serve HTTP (blocks until shutdown). Explicit server instead of bare
	// http.Serve so a slow or hung client can't pin a goroutine forever,
	// with a connection cap for backpressure (gh #38). Localhost-only
	// today, so the values are generous; WriteTimeout must cover the
	// slowest handler (/agents/spawn-polecat does a git worktree add plus
	// agent startup).
	httpServer := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       1 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	log.Fatal(httpServer.Serve(netutil.LimitListener(ln, maxHTTPConns)))
}

// promptRefreshLogLines renders the boot-time report for a prompt refresh into
// the lines pogod should log. It exists so a DECLINED sync is VISIBLE: a
// conflict is where InstallPrompts preserved a user-edited canonical and
// diverted the shipped update to a <name>.md.dist sidecar, meaning the
// propagation that pogod's boot is supposed to guarantee did NOT happen for
// that file.
//
// Two failure modes this repairs (both were silent before mg-f86c):
//
//  1. A boot with conflicts AND installs/updates used to print a reassuring
//     "refreshed prompts (installed=.. updated=.. skipped=..)" that structurally
//     could not name the file it declined. Conflicts now appears in the count.
//  2. A CONFLICT-ONLY boot (no installs, no updates) used to satisfy neither
//     arm of the old `else if len(Updated)>0 || len(Installed)>0` and logged
//     NOTHING AT ALL — total silence for the outcome that most needs saying.
//     Conflicts now drives the condition, so this boot logs loudly.
//
// Each conflict gets its own loud line naming the file and the remedy, because
// the reader's next question is always "which one, and what do I do".
//
// A third failure mode, repaired by mg-b6bd: the summary named a COUNT and no
// identities. "updated=9" is true, is what this box logged at 03:01 on the
// night doctor ran all night on a prompt fixed at 03:44, and cannot be used to
// answer "is doctor's live prompt current" by anyone who is not already sitting
// in front of the file. The names now go on the line, and so does the revision
// they came from — see promptrefreshrecord.go for the durable half.
func promptRefreshLogLines(res *agent.InstallResult, rev string) []string {
	// Nothing propagated and nothing declined: stay quiet (hash stamps make the
	// common boot a no-op). Conflicts count here so a conflict-only boot speaks.
	//
	// Silence here is NOT silence overall: promptRefreshEvent still records the
	// no-op boot, because "everything was already current at revision R" is an
	// answer and the log is the wrong place to repeat it nightly.
	if res == nil || (len(res.Installed) == 0 && len(res.Updated) == 0 && len(res.Conflicts) == 0) {
		return nil
	}
	lines := []string{
		fmt.Sprintf("pogod: refreshed prompts from %s (installed=%d updated=%d skipped=%d conflicts=%d)",
			shortRev(rev), len(res.Installed), len(res.Updated), len(res.Skipped), len(res.Conflicts)),
	}
	// Named, in full, on their own lines. Whichever of these is non-empty is
	// the set of agents whose next start reads something different from their
	// last one, which is the only actionable thing this whole report contains.
	if len(res.Installed) > 0 {
		lines = append(lines, "pogod: prompts INSTALLED (new): "+strings.Join(res.Installed, ", "))
	}
	if len(res.Updated) > 0 {
		lines = append(lines, "pogod: prompts UPDATED (restart these agents to pick them up): "+strings.Join(res.Updated, ", "))
	}
	for _, c := range res.Conflicts {
		lines = append(lines, fmt.Sprintf(
			"pogod: ⚠ prompt sync DECLINED for %s — user-edited canonical preserved; shipped update diverted to %s. Reconcile %s with %s (see docs/prompt-customization.md).",
			c.Path, c.DistPath, c.Path, c.DistPath))
	}
	return lines
}
