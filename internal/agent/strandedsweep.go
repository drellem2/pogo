package agent

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// The restart sweep: the one population the release gate can never reach
// (mg-be37).
//
// WHY A SWEEP AT ALL, GIVEN THE GATE WORKS. reportStrandedWorkOnRelease has
// exactly one caller — releasePolecatClaim — and that function needs an
// *Agent OUT OF THE REGISTRY. It has two doors and between them they cover more
// than the ticket assumed: Registry.Stop (a graceful `pogo agent stop`, the
// predeploy quiesce) and ReleaseClaimAfterExit (the reaper, which catches a
// polecat that crashed, was OOM-killed, or took a SIGKILL). Hard exits are NOT
// the hole; architect settled that by source read on 2026-08-10, and the
// restart_on_crash no-release exclusion does not apply here because
// RestartOnCrashDefault is true only for crew.
//
// THE HOLE IS A POLECAT THAT OUTLIVES A POGOD RESTART, and it is structural
// rather than intermittent. The registry is in-memory with no adopt path — the
// comment at cmd/pogod/main.go says so in terms, "absence never heals on its
// own" — so a successor pogod has no *Agent for a surviving polecat and neither
// door is EVER reachable for it again. Not at a later crash, not at a later
// graceful stop. It is un-instrumented for the whole remainder of its life. And
// this fleet restarts pogod nightly, so the exposure is every polecat running
// across the bounce.
//
// SO THE TRIGGER IS THE RESTART, NOT A CLOCK. A timer-driven sweep of every
// polecat branch was explicitly ruled out, twice, and correctly: it re-derives
// with `git cherry` a question the gate already answers 5-for-5, and on this
// repo it produces 59 rows of which the filer would defend 2. Startup is where
// the uncovered population is CREATED, and it is the moment when that population
// is exactly enumerable — see UnadoptablePolecats.
//
// WHY THE WITNESS STORE IS THE RIGHT POPULATION, AND "ABSENT FROM THE REGISTRY"
// ALONE IS NOT. At startup the registry is empty by construction, so a literal
// reading of "branches whose agent is absent from the registry" selects every
// polecat branch in the repo — 634 of them here, which is the forbidden sweep
// wearing a different trigger. The witness store is the narrow version of the
// same idea: noteWitnessStart writes a record when a polecat spawns and
// noteWitnessExit DELETES it when the registry sees it exit, so a record that
// survives into a new pogod's boot is precisely a polecat whose exit was never
// witnessed by the daemon that owned it. That is the same set architect
// described, established from durable state instead of inferred from an empty
// map.
//
// REPORT-ONLY. It never submits and never closes. A wrong auto-submit lands
// unreviewed work, and here the risk is sharper than usual: a swept polecat may
// still be RUNNING (that is the orphan case), and submitting under a working
// agent takes a branch away mid-flight.

// UnadoptablePolecat is a polecat this pogod can never instrument: a durable
// witness record with no registry entry to attach it to.
type UnadoptablePolecat struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	StartTime  time.Time `json:"start_time"`
	WorkItemID string    `json:"work_item_id,omitempty"`
	SourceRepo string    `json:"source_repo,omitempty"`
	// Alive is true when the process still matches the witness. It does not
	// change whether the branch is checked — a live orphan's pushed work is just
	// as invisible — but it changes the advice in the mail.
	Alive bool `json:"alive"`
}

// UnadoptablePolecats returns every witnessed polecat with no registry entry.
//
// THE ERROR IS NOT DECORATIVE AND MUST NOT BECOME AN EMPTY SLICE. An unreadable
// witness store means we do not know who is out there, which is a different fact
// from "nobody is out there" — the same distinction OrphanedPolecats enforces
// next door, and the same polarity trap that runs through this whole ticket. A
// caller that renders this error as zero has rebuilt the defect.
func (r *Registry) UnadoptablePolecats() ([]UnadoptablePolecat, error) {
	recs, err := loadWitness()
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}

	// A registry entry may be addressed by bare name or by event identity
	// (cat-<name>), and witness records are keyed by the bare Agent.Name — so
	// normalise both sides rather than assuming one spelling.
	known := map[string]struct{}{}
	if r != nil {
		for _, a := range r.List() {
			known[a.Name] = struct{}{}
			known[a.EventAgent()] = struct{}{}
		}
	}

	var out []UnadoptablePolecat
	for _, w := range recs {
		// The store witnesses crew as well as polecats since mg-f9e8, and this
		// sweep means polecats literally: it checks gitgc.BranchPrefix+Name for
		// unmerged work. A crew agent has no such branch, so every crew survivor
		// of a restart would arrive here as an UNJUDGED or SKIPPED row — a sweep
		// reporting on a population it cannot ask a question about.
		if !w.isPolecat() {
			continue
		}
		if _, ok := known[w.Name]; ok {
			continue
		}
		if _, ok := known["cat-"+w.Name]; ok {
			continue
		}
		out = append(out, UnadoptablePolecat{
			Name:       w.Name,
			PID:        w.PID,
			StartTime:  w.StartTime,
			WorkItemID: w.WorkItemID,
			SourceRepo: w.SourceRepo,
			Alive:      witnessVerdict(w) == WitnessAlive,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// StrandedSweepReport is what one sweep established. Every field distinguishes
// an answer from a non-answer, which is the whole discipline this ticket is
// about.
type StrandedSweepReport struct {
	// Candidates is how many unadoptable polecats were considered.
	Candidates int `json:"candidates"`
	// Stranded is how many of them had pushed, unmerged work.
	Stranded int `json:"stranded"`
	// Clean is how many were checked and found to need nothing.
	Clean int `json:"clean"`
	// Carried is how many had commits the target lacks that ANOTHER branch
	// already carries and owns — a reviewer's branch pointing at the builder's
	// head, typically. Not stranded, and counted apart from Clean so the
	// suppression is measurable rather than invisible (mg-1af2).
	Carried int `json:"carried"`
	// Skipped is how many carried no source repo to check, so no question was
	// asked. A --no-worktree polecat, or a record written before the repo field
	// existed.
	Skipped int `json:"skipped"`
	// Unjudged is how many were asked and gave no answer — git failed, the
	// target would not resolve, the ref was unreadable.
	//
	// IT IS A COUNT AND NOT A LOG LINE, and that is this ticket's own defect
	// avoided inside its own remedy. The natural spelling of this predicate,
	// `git cherry <target> <branch> | grep -q '^+'`, answers LANDED WHENEVER GIT
	// FAILS: a failed git prints nothing, and no output is exactly how the
	// predicate spells clean. For a stranded-work sweep, clean is the PERMISSIVE
	// answer — it means this branch needs nothing — so a transient git or network
	// failure would silently convert a stranded branch into an all-clear row.
	// doctor measured ~40-minute connectivity waves on the night this was
	// written, so that is not hypothetical. Folding it into Clean hides
	// strandings; folding it into Stranded cries wolf on every blip. It is a
	// third outcome (mg-b6d1).
	Unjudged int `json:"unjudged"`
	// Err is set when the candidate population itself could not be read. Every
	// other count is meaningless when it is non-nil.
	Err error `json:"-"`
}

// Judged reports whether the sweep actually answered the question for every
// candidate it was able to ask about.
func (s StrandedSweepReport) Judged() bool { return s.Err == nil && s.Unjudged == 0 }

// ReportStrandedWorkAcrossRestart sweeps the unadoptable population and reports
// every stranded branch, on the event spine and by mail.
//
// pogod calls this ONCE per boot, from every startup path. Not on the heartbeat:
// the population changes only when a pogod dies, so a repeat on a tick would
// re-mail the same branches all day. Once per restart matches the rate at which
// the fault is produced.
//
// It re-mails on each SUBSEQUENT restart while a branch stays stranded, and that
// is deliberate rather than an oversight: the fault is permanent until somebody
// submits the branch, the alert self-terminates the moment they do, and noise
// that ends when the fault ends is the fault reporting itself. Same posture as
// the orphaned-polecat alert.
func (r *Registry) ReportStrandedWorkAcrossRestart() StrandedSweepReport {
	cands, err := r.UnadoptablePolecats()
	if err != nil {
		log.Printf("strandedwork: startup sweep could NOT enumerate unadoptable polecats (%v) — "+
			"this is NOT a report that none exist (mg-be37)", err)
		return StrandedSweepReport{Err: err}
	}
	rep := StrandedSweepReport{Candidates: len(cands)}
	if len(cands) == 0 {
		log.Printf("strandedwork: startup sweep found no polecat that outlived a previous pogod; " +
			"every polecat this daemon supervises is reachable by the release gate")
		rep.emitRan()
		return rep
	}

	// Refresh each repo at most once. The whole candidate set usually lives in
	// one or two repos, and Fetch is the slow part.
	fetched := map[string]struct{}{}
	for _, c := range cands {
		if c.SourceRepo == "" {
			rep.Skipped++
			log.Printf("strandedwork: startup sweep cannot check polecat %s (work item %q): its witness "+
				"records no source repo, so there is no branch to inspect — NOT a report that it is clean",
				c.Name, c.WorkItemID)
			continue
		}
		if _, done := fetched[c.SourceRepo]; !done {
			fetched[c.SourceRepo] = struct{}{}
			if _, ferr := strandedwork.Fetch(c.SourceRepo); ferr != nil {
				log.Printf("strandedwork: startup sweep could not refresh %s (%v) — checking against refs "+
					"this clone last saw", c.SourceRepo, ferr)
			}
		}
		branch := gitgc.BranchPrefix + c.Name
		f, ierr := strandedwork.Inspect(c.SourceRepo, branch, "")
		if ierr != nil {
			rep.Unjudged++
			log.Printf("strandedwork: startup sweep could NOT judge branch %s in %s: %v — UNJUDGED, "+
				"which is neither stranded nor clean (mg-be37)", branch, c.SourceRepo, ierr)
			continue
		}
		if f.Disposition == strandedwork.DispositionCarried {
			// A fifth outcome, for the same reason Unjudged is a third: folding
			// it into Clean would make the suppression unobservable, and this is
			// the count that says how often the mg-1af2 pointer case fires.
			rep.Carried++
			log.Printf("strandedwork: startup sweep found polecat %s's branch %s pointing at work "+
				"%s already carries and owns — NOT stranded (mg-1af2). %s",
				c.Name, f.Branch, f.Carrier, f.Summary())
			continue
		}
		if !f.Stranded() {
			rep.Clean++
			continue
		}
		rep.Stranded++
		reason := "polecat outlived a pogod restart, so the release gate could never fire for it"
		if c.Alive {
			reason = "polecat outlived a pogod restart and is STILL RUNNING, unreachable by this daemon"
		}
		log.Printf("strandedwork: STARTUP SWEEP found pushed work behind unadoptable polecat %s "+
			"(work item %s, %s). %s", c.Name, c.WorkItemID, reason, f.Summary())
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_stranded_push",
			Agent:      "cat-" + c.Name,
			WorkItemID: c.WorkItemID,
			Repo:       c.SourceRepo,
			Details: map[string]any{
				"branch":       f.Branch,
				"ref":          f.Ref,
				"pushed":       f.Pushed,
				"target":       f.Target,
				"disposition":  string(f.Disposition),
				"unmerged":     len(f.Unmerged),
				"reason":       reason,
				"summary":      f.Summary(),
				"route":        RouteRestartSweep,
				"polecat_pid":  c.PID,
				"still_alive":  c.Alive,
				"witnessed_at": c.StartTime.Format(time.RFC3339),
			},
		})
		sendStrandedAlert(StrandedAlert{
			Polecat:    c.Name,
			WorkItemID: c.WorkItemID,
			Repo:       c.SourceRepo,
			Reason:     reason,
			Route:      RouteRestartSweep,
			StillAlive: c.Alive,
			Finding:    f,
		})
	}

	log.Printf("strandedwork: startup sweep judged %d unadoptable polecat(s): %d stranded, %d clean, "+
		"%d carried by another branch, %d unjudged, %d with no repo to check",
		rep.Candidates, rep.Stranded, rep.Clean, rep.Carried, rep.Unjudged, rep.Skipped)
	rep.emitRan()
	return rep
}

// emitRan records that the sweep COMPLETED, on every boot including the ones
// with nothing to report.
//
// A detector that can only be observed when it fires is a detector whose silence
// means nothing — which is this ticket's subject, aimed here at its own remedy.
// Without this, "no polecat outlived the last restart" and "the sweep is not
// running at all" are the same absence, and the only other trace is a log.Printf
// into pogod's stderr, where the previous three months of this feature's output
// went unread. A boot with no work_item_stranded_sweep event is a boot where this
// mechanism did not run, which is a different and checkable shape.
//
// It is emitted only on the paths that reached a conclusion; the two error
// returns above log their own distinct failure, so a missing event there is
// accurate.
func (s StrandedSweepReport) emitRan() {
	events.Emit(context.Background(), events.Event{
		EventType: "work_item_stranded_sweep",
		Agent:     "pogod",
		Details: map[string]any{
			"candidates": s.Candidates,
			"stranded":   s.Stranded,
			"clean":      s.Clean,
			"carried":    s.Carried,
			"unjudged":   s.Unjudged,
			"skipped":    s.Skipped,
			"judged":     s.Judged(),
		},
	})
}
