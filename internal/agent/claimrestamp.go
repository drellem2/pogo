package agent

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The claim-pid re-stamp: restoring a HARD started-signal after mg-7254
// (mg-7d6d, on pm-pogo's ruling of 2026-07-30).
//
// THE HOLE THIS FILLS. mg-feb3's auto-renudge gated on a hard signal — the work
// item left available/, i.e. the polecat ran its own `mg claim`. mg-7254 moved
// the claim to pogod at spawn, before the process exists, so that signal is true
// from the first instant of every --id dispatch and proves nothing about the
// agent. mg-7254 did not let that ship silently: it fell those dispatches back
// to the ready-composer signal (mg-c33e) and filed the residue.
//
// The residue is specific. The ready-composer fallback catches a harness whose
// composer NEVER RENDERED. It does not catch the failure mg-feb3 was built for:
// in the mg-ce61 paste-buffer wedge the composer does render, WaitForReady
// latches promptReadySeen, and the kickoff piles in the kernel input buffer as
// one paste block whose CR never re-tokenizes. So sawPromptReady() is already
// true when the watcher runs, the agent reads as "started", and a polecat wedged
// in exactly the way the recovery net exists for draws no auto_renudge at all.
//
// THE SIGNAL. pogod claims with its own pid (`mg claim --pid <pogod>`), and a
// macguffin claim file is named <id>.md.<pid>. So the polecat's first protocol
// action can be to RE-STAMP that claim to its own pid, and "started" becomes:
//
//	the claim pid is no longer pogod's own
//
// That is a positive observation of the polecat's own act, not the absence of
// one, which is what makes it hard where the fallback is a heuristic. pm-pogo
// ruled for it on the failure mode rather than the elegance: a spurious renudge
// is cheap, a missed wedge is what mg-feb3 exists to prevent.
//
// It also keeps mg-7254's guarantee by construction rather than by care. A
// re-stamp is a rename WITHIN claimed/; the item is never in available/ at any
// point, so there is no window for a second dispatch. That property is the whole
// reason `mg unclaim` + `mg claim` is not an acceptable implementation of it.
//
// WHY THIS IS CAPABILITY-GATED, AND WHY THE GATE IS A PROBE. macguffin has no
// command that re-stamps a held claim — `mg claim` requires the item to be
// available. That is filed additively as macguffin mg-bb43 (`mg reclaim <id>
// --pid`). Until it lands, no polecat can re-stamp, so a watcher gating on the
// re-stamp would report EVERY healthy polecat unstarted and spray three CRs and
// three auto_renudge events per dispatch — which would not be a cheap spurious
// renudge, it would be an event stream that is 100% false positives and a
// detector nobody reads.
//
// So the signal engages only where the mechanism exists, and the gate is an
// OBSERVATION (`mg reclaim --help` exits 0) rather than a constant someone must
// remember to flip. One probe drives both halves together — the started-signal
// and the prompt step that satisfies it — because activating either alone is a
// defect: the signal without the step renudges everything, the step without the
// signal is a command no one reads. EnableClaimRestampSignal is the single entry
// point that makes half-on unreachable; cmd/pogod calls it once at startup.

// ClaimRestampVerifier reports whether the claim on workItemID has been
// re-stamped away from pogod's own pid — the polecat's own first protocol action,
// and therefore a HARD started-signal (see the file doc).
//
// It has StartVerifier's shape and semantics on purpose: `started` is the same
// question, and a non-nil error is inconclusive, which the watcher must treat as
// "do not renudge" rather than as "unstarted". Distinct type because the two are
// wired independently and answer about different observables — conflating them
// is how mg-7254 nearly disabled the recovery net.
type ClaimRestampVerifier func(workItemID string) (restamped bool, err error)

// SetClaimRestampVerifier installs the claim-pid re-stamp query used as the hard
// started-signal for dispatches pogod claimed at spawn.
//
// nil — the default — means the re-stamp mechanism is unavailable, and
// start-verification keeps the WEAKER ready-composer fallback for those
// dispatches. nil is the correct state whenever macguffin cannot re-stamp a held
// claim (macguffin mg-bb43); installing a verifier while no polecat can satisfy
// it renudges every healthy agent.
//
// PREFER EnableClaimRestampSignal, which cannot install this half alone. This
// setter exists for tests that need to drive one half in isolation — including the
// positive control that proves the wedge is undetected without it.
func (r *Registry) SetClaimRestampVerifier(v ClaimRestampVerifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimRestampVerifier = v
}

func (r *Registry) getClaimRestampVerifier() ClaimRestampVerifier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.claimRestampVerifier
}

// claimRestampCommand is the process-wide re-stamp command, MINUS its work-item
// argument — "mg reclaim". Empty (the default) means the mechanism is unavailable
// and templates omit the step entirely.
//
// WITHOUT THE ARGUMENT ON PURPOSE. The obvious shape is to store the whole command
// with a "{{.Id}}" in it, and it silently does not work: text/template does not
// re-expand a value it substitutes, so the polecat's prompt would tell it to run
// the literal `mg reclaim {{.Id}}`. withDefaults appends the real id instead — see
// TemplateVars.ClaimRestampCmd.
//
// Process-wide rather than per-request for the same reason coordinatorName and
// workerName are: it is a property of the deployment (which mg is on PATH), not
// of one dispatch, and threading it through every call site is how a caller
// forgets it. Set once during startup, before any polecat is spawned.
var claimRestampCommand string

// SetClaimRestampCommand sets the claim re-stamp command emitted into polecat
// prompts, WITHOUT its work-item argument ("mg reclaim" — the id is appended per
// dispatch). An empty string (the default) omits the step.
//
// PREFER EnableClaimRestampSignal, which cannot install this half alone. A prompt
// step with no verifier watching it is a command nothing reads; a verifier with no
// prompt step renudges every healthy polecat three times.
func SetClaimRestampCommand(cmd string) {
	claimRestampCommand = cmd
}

// ClaimRestampCommand returns the process-wide claim re-stamp command without its
// work-item argument, or "" if the mechanism is unavailable.
func ClaimRestampCommand() string {
	return claimRestampCommand
}

// claimRestampCmdFor returns the re-stamp command a dispatch on workItemID should
// run, or "" when there is nothing to render.
//
// An empty workItemID yields "" — there is no claim to re-stamp, and the step must
// not appear in a prompt that cannot act on it. That matches the started-signal
// exactly: Registry.startedSignal's claim-pid arm also requires a work item and
// falls back to the ready-composer signal without one. The prompt and the watcher
// agree about which dispatches this mechanism covers, which is the property the
// whole capability gate is protecting.
func claimRestampCmdFor(workItemID string) string {
	if claimRestampCommand == "" || workItemID == "" {
		return ""
	}
	return claimRestampCommand + " " + workItemID
}

// MGReclaimSubcommand is the additive macguffin subcommand the claim-pid
// re-stamp needs, filed as macguffin mg-bb43. Named here as a constant because
// it appears in three places that must not drift: the capability probe, the
// command written into polecat prompts, and the operator log line that explains
// why the hard signal is off.
const MGReclaimSubcommand = "reclaim"

// MGSupportsClaimRestamp probes whether the mg binary can re-stamp a held
// claim's pid, by asking the subcommand for its help.
//
// `--help` rather than a version comparison or a `mg schema` parse: it is the
// canonical "does this CLI have this verb" question, it costs one process, and
// it cannot be satisfied by a macguffin that grew the command under a different
// name. cobra exits non-zero with "unknown command" when the verb is absent, so
// the discriminator is the exit status and nothing is matched against prose.
//
// A false answer is a working deployment, not a fault — every macguffin
// predating mg-bb43 answers false — so the caller logs it as a degraded signal
// rather than an error.
func MGSupportsClaimRestamp(bin string) bool {
	if bin == "" {
		bin = "mg"
	}
	// CombinedOutput rather than Run: cobra writes the unknown-command error to
	// stderr, and letting that reach pogod's log on every startup would report a
	// routine capability answer as a failure.
	_, err := exec.Command(bin, MGReclaimSubcommand, "--help").CombinedOutput()
	return err == nil
}

// MacguffinStoreRoot returns the macguffin store root pogod's dispatch-time claim
// was taken in — MGWorkItemClaimer's resolution exactly, override-free.
//
// Exported for the one caller that MUST agree with it: the claim-pid re-stamp
// verifier reads the pid off the very claim file ClaimForSpawn wrote, so a second
// resolution of "which store" would be free to disagree, and the way it would
// disagree is by reading ~/.macguffin while pogod claimed in $MG_ROOT. The signal
// would then report every polecat in a sandboxed daemon as started — silently, in
// the safe-looking direction. The agreement is pinned by a test rather than left
// as a thing to remember; this is the same coupling releaseSpawnClaim documents
// between the claimer and the releaser.
func MacguffinStoreRoot() string {
	return macguffinStoreRoot("")
}

// NewMGClaimRestampVerifier builds the production ClaimRestampVerifier: it reads
// the claim pid straight off the claim file's name in <root>/work/claimed/ and
// reports whether it differs from pogodPID.
//
// A FILESYSTEM READ, NOT A SUBPROCESS, and that is deliberate. The verifier runs
// on a 25s timer for every live polecat; shelling out to `mg show` per check
// would put a process spawn on the recovery path, which is the path that already
// only exists because the machine was too loaded to deliver a keystroke. The
// claim-file naming it depends on is not an inference either — claimHeld and
// workitem.FindFrom both already rest on it, and workitem.FindFrom carries the
// scar from learning it the hard way.
//
// The failure directions are chosen, not incidental:
//
//   - NO CLAIM FILE reads as STARTED. The claim pogod took is gone: the item is
//     done (a polecat that finished inside the start-verify window), or it was
//     released. Neither is "sitting under pogod's untouched claim", and reporting
//     unstarted would fire a stray CR at an agent that had just finished. The
//     watcher's whole discipline is that it delivers only while the agent is
//     PROVABLY unstarted.
//   - AN ABSENT claimed/ DIRECTORY is an ERROR, and it is deliberately NOT the
//     benign case above. pogod's own `mg claim` created that directory before this
//     verifier ever runs, so its absence means we are reading a different store
//     than the one the claim went into — the exact shape a MacguffinStoreRoot
//     disagreement takes. Read as "no claim file", that mistake would report every
//     polecat on the daemon as started and switch the detector off in silence,
//     which is precisely the failure mg-7d6d exists to end.
//   - UNREADABLE claimed/, or a claim file with no parsable pid suffix, is an
//     ERROR for the same reason. Inconclusive, so the watcher declines and stops —
//     a bare CR into a working agent is worse than leaving recovery to the
//     mail-check schedule. A pid-less name in particular must not be guessed at:
//     it means the store does not look the way this verifier assumes, and silently
//     reading that as either answer would make the mechanism unfalsifiable.
func NewMGClaimRestampVerifier(root string, pogodPID int) ClaimRestampVerifier {
	return func(workItemID string) (bool, error) {
		if workItemID == "" {
			return false, fmt.Errorf("claim-restamp check called with an empty work item id")
		}
		pid, held, err := claimPID(root, workItemID)
		if err != nil {
			return false, err
		}
		if !held {
			// Done, or released. Not under pogod's untouched claim either way.
			return true, nil
		}
		return pid != pogodPID, nil
	}
}

// claimPID returns the pid stamped on the claim file for id in <root>/work/claimed/.
// held is false when the directory exists but holds no claim for id, which is not
// an error (see NewMGClaimRestampVerifier). An absent directory, an unreadable
// one, and a claim file whose name carries no parsable .<pid> suffix are all
// errors — the caller must not read any of them as an answer.
func claimPID(root, id string) (pid int, held bool, err error) {
	dir := filepath.Join(root, "work", "claimed")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// os.IsNotExist is NOT folded into "no claim held" here. pogod's own
		// claim-at-spawn created this directory; if it is missing we are looking at
		// the wrong store, and calling that "started" would silently disable the
		// detector for every polecat on the daemon.
		return 0, false, fmt.Errorf("read claimed/ in %s (pogod's claim-at-spawn writes here, so "+
			"this failing means the claim-pid started-signal is reading the wrong store): %w",
			root, err)
	}
	prefix := id + ".md."
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		suffix := strings.TrimPrefix(e.Name(), prefix)
		pid, err := strconv.Atoi(suffix)
		if err != nil {
			return 0, false, fmt.Errorf("claim file %s in %s has no parsable pid suffix: %w",
				e.Name(), dir, err)
		}
		return pid, true, nil
	}
	// A bare <id>.md in claimed/ is a claim we cannot read a pid off. Report it
	// rather than treating the item as unclaimed: the started-signal would be
	// silently wrong, and this verifier's only value is being hard.
	for _, e := range entries {
		if !e.IsDir() && e.Name() == id+".md" {
			return 0, false, fmt.Errorf("claim file %s in %s carries no pid suffix, so the "+
				"claim pid cannot be read", e.Name(), dir)
		}
	}
	return 0, false, nil
}

// EnableClaimRestampSignal probes the mg binary and, if it can re-stamp a held
// claim, turns on the HARD claim-pid started-signal for this daemon: BOTH the
// verifier the watcher gates on and the step polecat prompts carry to satisfy it.
// It reports whether the signal engaged, and logs the answer either way.
//
// ONE ENTRY POINT, BECAUSE HALF OF THIS MECHANISM IS A DEFECT. The verifier alone
// gates on a re-stamp no polecat is told to perform, so every healthy agent reads
// as unstarted and draws three CRs and three auto_renudge rows per dispatch — an
// event stream of pure false positives, which is worse than the gap it replaces
// because it also destroys the signal an operator reads. The prompt step alone is
// a command nothing observes. Neither is a state anyone would choose, so neither
// is reachable from here: a caller cannot wire one and forget the other.
//
// The gate is a probe rather than a config flag so that installing a macguffin
// with `mg reclaim` (macguffin mg-bb43) activates the signal on the next pogod
// restart, with nothing to remember and nothing to flip. pogodPID is the pid
// stamped on claims taken at spawn — pogod's own os.Getpid(), which is what
// MGWorkItemClaimer passes to `mg claim --pid`.
func (r *Registry) EnableClaimRestampSignal(bin string, pogodPID int) bool {
	supported := MGSupportsClaimRestamp(bin)

	// Both halves are ASSIGNED on both paths, never left as they were. An
	// unsupported probe that only skipped the installs would inherit whatever a
	// prior call had set — and the state it would inherit is one of the two broken
	// halves this function exists to make unreachable. "Enable" here establishes
	// the state; it does not add to it.
	var (
		verifier ClaimRestampVerifier
		cmd      string
	)
	if supported {
		// No work-item argument: withDefaults appends the dispatch's own id. --pid
		// is omitted because it defaults to the calling process — the polecat's own
		// shell, which is exactly the pid we want stamped.
		cmd = "mg " + MGReclaimSubcommand
		verifier = NewMGClaimRestampVerifier(MacguffinStoreRoot(), pogodPID)
	}
	r.SetClaimRestampVerifier(verifier)
	SetClaimRestampCommand(cmd)

	logClaimRestampCapability(supported, bin, cmd)
	return supported
}

// logClaimRestampCapability records, once at startup, which started-signal this
// daemon's polecats will be watched on.
//
// It is a log line and not merely a comment because the failure it guards is
// silence. mg-7254's gap went unnoticed only until someone thought to look; the
// lesson pm-pogo drew from it — "when a change retires a signal, the ticket names
// what depended on it" — applies to the running daemon too. An operator wondering
// why a wedged polecat drew no auto_renudge should find the answer in the first
// screen of pogod.log, not in a source comment.
func logClaimRestampCapability(supported bool, bin, cmd string) {
	if bin == "" {
		bin = "mg"
	}
	if supported {
		log.Printf("pogod: %s supports `%s %s`, so start-verification uses the HARD claim-pid "+
			"re-stamp signal (mg-7d6d): a polecat has started when the claim pid is no longer "+
			"pogod's. Polecat prompts carry the re-stamp step as `%s`",
			bin, bin, MGReclaimSubcommand, cmd)
		return
	}
	log.Printf("pogod: %s has no `%s` subcommand, so the HARD claim-pid re-stamp started-signal "+
		"is OFF (mg-7d6d) and dispatches pogod claimed at spawn fall back to the WEAKER "+
		"ready-composer signal: a composer that never renders draws a recovery CR, but a "+
		"paste-buffered kickoff (mg-ce61) will NOT. Install a macguffin with `%s %s` "+
		"(macguffin mg-bb43) and restart pogod to close that gap",
		bin, MGReclaimSubcommand, bin, MGReclaimSubcommand)
}
