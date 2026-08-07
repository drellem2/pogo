package service

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/server"
)

// This file owns the one thing installLaunchd borrows from the whole fleet and
// has to give back: orchestration.
//
// The install's step 1 stops fleet-wide dispatch so a crew agent's `mg`/`pogo`
// command cannot respawn a non-launchd pogod mid-handoff (mg-9cdc). That reason
// is sound and is not weakened here. What was missing was the return leg: the
// only thing that used to undo step 1 was step 7 — a NEW pogod booting, which
// comes up in ModeFull. Five fallible steps sit between them, and on every one
// of those paths the restart that would have restored full mode is precisely
// the thing that just failed. The failure mode disabled its own recovery, and
// left the box with orchestration stopped, the old pogod still alive, dispatch
// dark fleet-wide, and nothing logged (mg-6515).
//
// That state is survivable only if it is loud. On 2026-08-07 the fleet sat dark
// for 10h39m with a coordinator, a stall-watch, an ack-watch and a deaf-watch
// all running: every one reported healthy or reported into a mailbox nobody
// could read, because the agent that reads mail was one of the agents dispatch
// was dark for. So the restore here is a defer that runs on every exit path
// rather than a check bolted onto the five returns we happen to know about, and
// a restore that cannot run or does not take is shouted at stderr and carried
// into the failure mail.

// orchestrator is the fleet-wide dispatch control the installer borrows. It is
// an interface so the mg-6515 positive control can force each failing step
// against a fake pogod and read back the mode it was left in — the real one
// lives behind :10000 and cannot be driven into those states from a test.
type orchestrator interface {
	// Alive reports whether a pogod is answering. False means there is
	// nothing to stop and, on the way out, nothing to restore through.
	Alive() bool
	// Stop transitions the daemon to index-only mode.
	Stop() error
	// Start transitions back to full mode and reports what actually came
	// back. The report is not decoration: a transition can succeed while
	// restoring no agents at all (gh #108), and it is the only thing that
	// distinguishes the two.
	Start() (server.StartReport, error)
}

// liveOrchestrator drives the pogod on :10000.
type liveOrchestrator struct{}

func (liveOrchestrator) Alive() bool                        { return client.HealthCheck() == nil }
func (liveOrchestrator) Stop() error                        { return client.StopOrchestration() }
func (liveOrchestrator) Start() (server.StartReport, error) { return client.StartOrchestration() }

// quiesceCrew tells the running pogod to stop orchestration (agents +
// refinery) so crew agents can't auto-respawn pogod via RunWithHealthCheck
// during the launchd handoff. Without this step, a crew agent's `mg`/`pogo`
// command issued between `pogo server stop` and `launchctl load` will
// trigger client.StartServer(), which spawns a non-launchd pogod that wins
// the :10000 bind and silently knocks launchd's pogod out (the deterministic
// race observed on mg-9cdc, 2026-04-28). No-op if pogod isn't running.
//
// The bool it returns is what obliges the caller to restore: it is true when a
// stop was ATTEMPTED against a live pogod, not when one was confirmed. A Stop
// that reports an error may still have taken — an error is frequently the
// response arriving late or not at all, not the crew staying up. Restoring
// after a stop that did not take is a no-op (StartOrchestration returns
// AlreadyFull); skipping the restore after a stop that did take is the
// ten-hour outage. The asymmetry decides the default.
func quiesceCrew(orch orchestrator) (quiesced bool) {
	if !orch.Alive() {
		return false
	}
	fmt.Println("Quiescing crew (stopping orchestration)...")
	if err := orch.Stop(); err != nil {
		fmt.Printf("  warning: %v (continuing anyway)\n", err)
	}
	return true
}

// orchestrationRestore records what the installer did about the fleet-wide
// dispatch its step 1 stopped. It is printed and mailed rather than dropped,
// because a silent failed restore is mg-6515 one layer down.
type orchestrationRestore struct {
	// Attempted is true when the install had stopped orchestration and so
	// owed the fleet a restore — whether or not the restore succeeded.
	Attempted bool
	// OK is true only when orchestration is known to be back in full mode.
	OK bool
	// Detail is the human-readable account, always populated.
	Detail string
}

// String renders the restore for the install's stdout/stderr and for the
// failure mail. The prefix is the part an operator greps for.
func (r orchestrationRestore) String() string {
	switch {
	case !r.Attempted && r.Detail == "":
		// The zero value: the install failed before it reached the step
		// that stops orchestration, so nothing was ever taken.
		return "Orchestration: untouched — the install failed before it stopped anything"
	case !r.Attempted:
		return "Orchestration: " + r.Detail
	case r.OK:
		return "Orchestration RESTORED: " + r.Detail
	default:
		return "Orchestration NOT RESTORED: " + r.Detail
	}
}

// restoreOrchestration puts back what quiesceCrew took. It is called from a
// defer on every failing exit path of the install sequence.
//
// The three outcomes are deliberately distinct, because they need different
// things from a human:
//
//   - nothing was taken → nothing to say beyond that.
//   - taken, pogod answers → start orchestration and report what came back.
//     A transition that restores zero agents is not success and does not read
//     as success here (gh #108).
//   - taken, nothing answers → there is no API left to restore through. This
//     is the worst cell and the one that cannot be fixed from in here, so it
//     is shouted with the command a human has to run.
func restoreOrchestration(orch orchestrator, quiesced bool) orchestrationRestore {
	return restoreOrchestrationTo(os.Stderr, orch, quiesced)
}

// restoreOrchestrationTo is restoreOrchestration with the loud channel
// parameterized, so a test can read back what a failed restore actually said
// instead of taking "it printed something" on trust.
func restoreOrchestrationTo(loud io.Writer, orch orchestrator, quiesced bool) orchestrationRestore {
	if !quiesced {
		return orchestrationRestore{Detail: "left alone — this install never stopped it"}
	}
	if !orch.Alive() {
		return shout(loud, orchestrationRestore{
			Attempted: true,
			Detail: fmt.Sprintf("this install stopped orchestration and nothing answers on %s, so there is no daemon to restore it through. "+
				"The fleet is down until someone runs `pogo server start` or a successful `pogo service install`.", pogodPort),
		})
	}
	report, err := orch.Start()
	if err != nil {
		return shout(loud, orchestrationRestore{
			Attempted: true,
			Detail: fmt.Sprintf("this install stopped orchestration and starting it again failed: %v. "+
				"pogod is answering on %s but dispatch is dark fleet-wide — run `pogo server start` and check the mode.", err, pogodPort),
		})
	}
	summary := summarizeStartReport(report)
	if report.Mode != "" && report.Mode != config.ModeFull.String() {
		return shout(loud, orchestrationRestore{
			Attempted: true,
			Detail: fmt.Sprintf("start-orchestration returned without error but the daemon reports mode %q, not %q (%s). Dispatch may still be dark — check `pogo server status`.",
				report.Mode, config.ModeFull.String(), summary),
		})
	}
	restore := orchestrationRestore{Attempted: true, OK: true, Detail: summary}
	fmt.Println(restore.String())
	return restore
}

// shout writes a failed restore to the loud channel (stderr in production). The
// install's own error goes back to whoever invoked it; this line is for whoever
// is reading the terminal or the log, because the residual state — dispatch
// dark, pogod possibly still up — looks like nothing at all from the outside.
// That is the whole reason 2026-08-07 ran for ten hours.
func shout(loud io.Writer, r orchestrationRestore) orchestrationRestore {
	fmt.Fprintf(loud, "\n!!! %s\n\n", r.String())
	return r
}

// summarizeStartReport names the fleet, not just the mode. "restarted" on its
// own is what gh #108 printed while restoring nothing.
func summarizeStartReport(r server.StartReport) string {
	if r.AlreadyFull {
		return "daemon was already in full mode; nothing had to be restarted"
	}
	parts := []string{fmt.Sprintf("mode=%s", r.Mode)}
	if r.RefineryRestarted {
		parts = append(parts, "refinery restarted")
	} else {
		parts = append(parts, "refinery NOT restarted")
	}
	if len(r.AgentsStarted) > 0 {
		parts = append(parts, fmt.Sprintf("crew started (%d): %s", len(r.AgentsStarted), strings.Join(r.AgentsStarted, ", ")))
	} else {
		parts = append(parts, "no crew agents started")
	}
	if len(r.AgentsAlreadyRunning) > 0 {
		parts = append(parts, fmt.Sprintf("already running (%d): %s", len(r.AgentsAlreadyRunning), strings.Join(r.AgentsAlreadyRunning, ", ")))
	}
	if len(r.AgentsParked) > 0 {
		parts = append(parts, fmt.Sprintf("parked (%d): %s", len(r.AgentsParked), strings.Join(r.AgentsParked, ", ")))
	}
	for _, f := range r.AgentsFailed {
		parts = append(parts, fmt.Sprintf("FAILED to start %s: %s", f.Name, f.Error))
	}
	if r.AgentStartSkipped != "" {
		parts = append(parts, "auto-start sweep skipped: "+r.AgentStartSkipped)
	}
	return strings.Join(parts, "; ")
}

// installSteps is the fallible middle of the launchd install: everything the
// sequence does after orchestration has been stopped and before the new pogod
// is verified. Each field is one step, so the mg-6515 positive control can fail
// exactly one of them at a time and read back the mode the box was left in.
type installSteps struct {
	unloadPrior func()       // step 2 — best-effort
	stopPogod   func()       // step 3 — best-effort
	drainPort   func() error // step 4
	writePlist  func() error // step 5
	loadPlist   func() error // step 5
	kickstart   func() error // step 5b
	verify      func() error // step 6

	// restore performs the step-1 rollback. Production leaves it nil, which
	// means restoreOrchestration. Only the mg-6515 positive control sets it —
	// to a no-op, which reproduces the pre-fix behaviour through the real
	// sequence and proves the mode assertion is able to go red.
	restore func(orchestrator, bool) orchestrationRestore
}

// runOrchestratedInstall executes the part of the launchd install that must not
// be interrupted: stop the crew, hand :10000 over to launchd, verify the new
// pogod answers. See mg-ae84 (architect, 2026-04-28T11:37Z) for why the order
// is what it is.
//
// The restore is a defer rather than a check at each return. Reordering so the
// quiesce happens last is not available — the quiesce has to precede the stop
// and the drain or it prevents nothing — so the sequence necessarily runs with
// dispatch down, and the only safe design is one where every way out of it,
// including ways nobody enumerated, goes through the restore. The 2026-08-07
// state was reached by a route neither reviewer had listed; a defer covers
// routes that have not been listed.
//
// It returns what the restore did, so a caller reporting the failure can carry
// it. A restore is not mentioned on the success path because there is nothing
// to restore: the new pogod boots in ModeFull (server.New), so the refinery and
// the agent registry are already up by the time verify returns.
func runOrchestratedInstall(orch orchestrator, steps installSteps) (restore orchestrationRestore, retErr error) {
	// Step 1: Quiesce crew. Tell the running pogod to drop crew agents so
	// they can't issue a `pogo`/`mg` command that auto-respawns a non-launchd
	// pogod via client.RunWithHealthCheck.
	quiesced := quiesceCrew(orch)

	defer func() {
		if retErr == nil {
			return
		}
		restoreFn := steps.restore
		if restoreFn == nil {
			restoreFn = restoreOrchestration
		}
		restore = restoreFn(orch, quiesced)
	}()

	// Step 2: Unload any prior plist. Best-effort — handles the
	// loaded-and-running, loaded-and-stopped, and loaded-with-stale-config
	// cases uniformly. Subsumes mg-6095 (idempotency against pre-loaded
	// plist).
	steps.unloadPrior()

	// Step 3: Stop any pogod still running (manual or formerly-launchd).
	steps.stopPogod()

	// Step 4: Wait for :10000 to drain. If a stranger holds the port past
	// the timeout, fail fast — loading the plist now would just produce
	// another silent launchd-pogod exit.
	if err := steps.drainPort(); err != nil {
		return restore, err
	}

	// Step 5: Write plist (if it changed) and load it.
	if err := steps.writePlist(); err != nil {
		return restore, err
	}
	if err := steps.loadPlist(); err != nil {
		return restore, err
	}

	// Step 5b: Force launchd to actually spawn pogod now. On modern macOS,
	// `launchctl load` of a RunAtLoad job leaves the job in
	// `pended nondemand spawn = speculative` state — launchd has the plist
	// registered but defers the initial fork-exec opportunistically and
	// often indefinitely, so `runs = 0` and `last exit code = (never
	// exited)` (mg-3963 repro state). kickstart forces the spawn
	// deterministically so verifyLaunchdRunning's healthcheck window has
	// a real process to wait for. Same kickstart pattern restartLaunchd
	// already relies on — without it, the post-load /health poll just
	// times out against a launchd job that never started.
	if err := steps.kickstart(); err != nil {
		return restore, err
	}

	// Step 6: Verify launchd-pogod is bound and answering on /health.
	if err := steps.verify(); err != nil {
		return restore, err
	}

	// Step 7: Crew agents auto-restart under the new pogod via
	// auto_start=true in their prompt frontmatter (mayor.md, pm-template.md).
	// pogod boots in ModeFull (server.New), so refinery + agent registry
	// are already running by the time verifyLaunchdRunning returns. This is
	// the restore on the success path — and, before mg-6515, the only one.
	return restore, nil
}
