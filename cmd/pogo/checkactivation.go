package main

// `pogo check-activation` — the SCHEDULABLE half of the launchd activation
// audit (mg-b9e7).
//
// WHAT THIS ADDS TO THE DOCTOR ROW, WHICH ALREADY DOES THE COMPARISON. Nothing,
// arithmetically: it calls service.AuditLaunchAgents() and renders its verdict
// through the same launchAgentActivationLine() the `launchd activation` row uses,
// so the two surfaces cannot disagree about whether the box is clean. What it
// adds is the three things a row inside `pogo doctor --check` structurally
// cannot have:
//
//	AN EXIT CODE.  doctor deliberately never fails on this (see
//	               launchagentdrift.go, "WHY IT WARNS AND NEVER FAILS"), which is
//	               correct for a checklist and useless to a caller that wants to
//	               act. This command exits 1 on drift, so a schedule or a deploy
//	               step can gate on it.
//	A CALLER.      The row only exists when a person runs doctor. mg-b201's drift
//	               sat for seven days between such runs. scripts/pogo-self-deploy
//	               now calls this every night — see report_activation there.
//	A LOUD ABSENCE. See below; this is the whole reason it is a TOP-LEVEL command.
//
// WHY TOP-LEVEL AND NOT `pogo service check-activation`, WHICH IS WHERE IT
// BELONGS BY SUBJECT. Because of gap 2 of mg-b9e7: the detector for "merged but
// not installed" ships in the same binary it detects for, so an old `pogo` does
// not have it — and what an old `pogo` DOES when asked is the whole question.
// Measured on this box, 2026-08-13, against the `pogo` on PATH:
//
//	pogo service check-activation   -> exit 0, prints `pogo service` help
//	pogo service bogus-sub          -> exit 0, prints `pogo service` help
//	pogo bogus-top                  -> exit 1, "unknown command"
//
// `service` has no Run of its own, so cobra answers any unknown subcommand of it
// by printing help and SUCCEEDING. A scheduled caller reading exit status would
// have scored an old binary as a clean box — which is mg-b9e7's defect,
// reproduced by its own remedy, one layer down. Top-level, the absence is a
// nonzero exit.
//
// Nonzero is not yet enough, because "unknown command" (1) and "drifted" (1) are
// the same integer. So every verdict this command prints leads with
// activationMarker, and the deploy-side caller REQUIRES that marker before it
// will read the exit status as a verdict at all. An old binary produces a
// nonzero exit with no marker, and that combination is reported as its own
// finding — "this build predates the check" — rather than as drift.
//
// WHY IT PRINTS THE BUILD IT COMPARES FROM. Gap 3 of mg-b9e7: the plists are Go
// templates with this build's constants bound in, so "matches this build" is a
// claim about an expectation, not about main. Running an old `pogo` to fix drift
// reinstalls the OLD schedule and prints success — that happened on 2026-08-07.
// A drift report that does not say which build it drifted FROM sends its reader
// to do exactly that, so the build stamp is on the report and not only in
// `pogo version`.
//
// REPORTS ONLY, DELIBERATELY, AND THIS IS THE PART mg-b9e7 LEAVES OPEN. It never
// installs, never bootstraps, never kickstarts. Reconciling is `pogo service
// install*`, which bounces the daemon or rewrites a nightly's schedule; making
// that automatic is a blast-radius decision that mg-b201 declined to take inside
// a drift ticket and that this ticket declines to take inside a detection
// change. What is closed here is that the drift is no longer silent. What is not
// closed is that a human still has to act on it — that decision is mg-de0c,
// filed rather than left in this sentence, because a deferred half announced in
// a comment and never filed is a half that gets dropped.
//
// WHAT THIS COMMAND CANNOT SEE, said here because a detector's denominator is
// the thing that rots: it audits the jobs internal/service renders (the registry
// in launchagentaudit.go) and it reports, but does not audit, the pogo jobs
// loaded outside that registry. com.pogo.revisionprobe is deliberately outside
// it (mg-a03d). And it compares BYTES ON DISK against this build's rendering —
// a plist launchd rejected or only partly parsed is a perfectly good-looking
// file, so `launchctl print` remains the second read, exactly as
// scripts/launchd/README.md says.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/service"
	"github.com/drellem2/pogo/internal/version"
)

// activationMarker leads every verdict line this command prints.
//
// It exists so a CALLER can tell "this binary ran the check and answered" from
// "this binary has no such command". Both are nonzero exits and the exit codes
// collide; only the marker separates them. Callers must match it as a literal
// prefix of the first line.
const activationMarker = "activation:"

// The three verdicts. UNKNOWN is a real answer and is never a pass — it is what
// this command says when it could not compare, which on this subject is the
// state that has historically been mistaken for clean.
const (
	activationActivated = "ACTIVATED"
	activationDrifted   = "DRIFTED"
	activationUnknown   = "UNKNOWN"
)

// activationJob is one managed launchd job's row.
type activationJob struct {
	Label         string `json:"label"`
	Path          string `json:"path"`
	State         string `json:"state"`
	ScheduleDrift bool   `json:"schedule_drift"`
	Remedy        string `json:"remedy,omitempty"`
	Detail        string `json:"detail"`
}

// activationReport is the whole answer, in one value, so the human text and the
// JSON are two renderings of one thing rather than two computations.
type activationReport struct {
	// Marker is activationMarker, in the JSON too: a caller reading --json
	// needs the same "did this binary answer at all" evidence as one reading
	// the text.
	Marker string `json:"marker"`
	// Verdict is ACTIVATED, DRIFTED or UNKNOWN.
	Verdict string `json:"verdict"`
	// Build is what this binary is, spelled the way `pogo version` spells it.
	// It is the expectation every row below was compared against.
	Build string `json:"build"`
	// Headline is the one-sentence form of Verdict, for the first line.
	Headline string `json:"headline"`
	// Examined / Drifted / Absent / Unreadable are the population, split.
	Examined   int `json:"examined"`
	Drifted    int `json:"drifted"`
	Absent     int `json:"absent"`
	Unreadable int `json:"unreadable"`
	// Jobs is every managed job, including the clean ones. A report that lists
	// only findings cannot be told from one that did not run.
	Jobs []activationJob `json:"jobs"`
	// Scope is the audit's denominator, rendered by the same helper the doctor
	// row uses.
	Scope string `json:"scope"`
	// Summary is launchAgentActivationLine's detail verbatim — the doctor row's
	// own sentence, so a reader comparing the two surfaces sees one string.
	Summary string `json:"summary"`
	// DoctorStatus is what the doctor row would render for this same audit
	// ("pass" or "warn"). Carried so a divergence between the two surfaces is
	// visible in the output rather than only in a test.
	DoctorStatus string `json:"doctor_status"`
}

// ExitCode maps the verdict onto this repo's three-way exit convention (see
// internal/cli): 0 established clean, 1 established bad, 3 could not establish.
//
// DRIFTED outranks UNKNOWN when both hold. Drift is the actionable finding with
// a named remedy; an uncomparable job is a thing to say, and burying a real
// drift under it is how mg-8f7e stayed invisible for five days.
func (r activationReport) ExitCode() int {
	switch r.Verdict {
	case activationActivated:
		return cli.ExitSuccess
	case activationDrifted:
		return cli.ExitError
	default:
		return cli.ExitUnknown
	}
}

// buildActivationReport is the whole classification as a pure function of the
// audit, so every verdict is reachable in a test on a box where the real answer
// is fixed. Same reasoning as auditLaunchAgent one package over: the audit's own
// correctness must not be something only the machine that has the bug can
// demonstrate.
func buildActivationReport(audits []service.LaunchAgentAudit, supported bool, scope service.LaunchAgentScope, build string) activationReport {
	status, detail := launchAgentActivationLine(audits, supported, scope)
	r := activationReport{
		Marker:       activationMarker,
		Build:        build,
		Examined:     len(audits),
		Summary:      detail,
		DoctorStatus: status,
		Scope:        launchAgentScopeNote(audits, scope),
	}

	for _, a := range audits {
		r.Jobs = append(r.Jobs, activationJob{
			Label:         a.Label,
			Path:          a.Path,
			State:         a.Status,
			ScheduleDrift: a.ScheduleDrift,
			Remedy:        a.Remedy,
			Detail:        a.Detail,
		})
		switch a.Status {
		case service.LaunchAgentStale:
			r.Drifted++
		case service.LaunchAgentAbsent:
			r.Absent++
		case service.LaunchAgentUnknown:
			r.Unreadable++
		}
	}

	switch {
	case !supported:
		r.Verdict = activationUnknown
		r.Headline = "no launchd on this platform, so no installed plist was compared against anything. This is NOT a report that any machine's plists are current"
	case len(audits) == 0:
		r.Verdict = activationUnknown
		r.Headline = "no managed launchd job was examined at all, so nothing here says an installed plist matches the code that ships it"
	case r.Drifted > 0:
		r.Verdict = activationDrifted
		r.Headline = fmt.Sprintf("%d of %d managed launchd job(s) disagree with the plist this build renders", r.Drifted, r.Examined)
	case r.Unreadable > 0:
		r.Verdict = activationUnknown
		r.Headline = fmt.Sprintf("%d of %d managed launchd job(s) could not be compared, so this run cannot say the box is current", r.Unreadable, r.Examined)
	case r.Absent > 0:
		// Absent is UNKNOWN, not ACTIVATED, and the difference matters here in
		// a way it does not in the doctor row. The row is read by a person who
		// also reads the sentence naming the absent job; a caller reads the
		// exit status alone, and "this job was never installed" is precisely
		// the state mg-b9e7 is about. Calling it 0 would hand a scheduled
		// caller a pass over a merged plist nobody ever activated — which is
		// the defect, scored as its own absence of evidence.
		r.Verdict = activationUnknown
		r.Headline = fmt.Sprintf("%d of %d managed launchd job(s) are NOT INSTALLED — this audit compares installed plists and says nothing about a job whose install never ran", r.Absent, r.Examined)
	case !scope.Observed:
		r.Verdict = activationUnknown
		r.Headline = "every examined plist matches this build, but the loaded jobs could not be enumerated, so the count above is this build's registry size and not a share of the pogo jobs on this box"
	case len(scope.Unexplained()) > 0:
		r.Verdict = activationUnknown
		r.Headline = fmt.Sprintf("every examined plist matches this build, but %d loaded pogo job(s) are outside this audit with NO recorded reason — a job that arrived by an install path nobody checked against the auditor", len(scope.Unexplained()))
	default:
		r.Verdict = activationActivated
		r.Headline = fmt.Sprintf("all %d managed launchd job(s) on this box match the plist this build renders", r.Examined)
	}
	return r
}

// activationStateLabel is the fixed-width tag each job row leads with. Written
// out rather than derived from the service constants so a column change here
// cannot silently rename a state a caller greps for.
func activationStateLabel(state string, scheduleDrift bool) string {
	switch state {
	case service.LaunchAgentOK:
		return "OK      "
	case service.LaunchAgentStale:
		if scheduleDrift {
			return "FIRES   " // the loud one: a job doing a fraction of what the code believes
		}
		return "DRIFT   "
	case service.LaunchAgentAbsent:
		return "ABSENT  "
	default:
		return "UNKNOWN "
	}
}

// Text is the human rendering. The first line is the marker line and is the only
// line a caller is promised.
func (r activationReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s — %s\n", activationMarker, r.Verdict, r.Headline)
	fmt.Fprintf(&b, "  build: %s\n", r.Build)
	b.WriteString("         Every verdict below is against THIS build's rendering of the plists. A `pogo`\n")
	b.WriteString("         older than a merged plist change reports its own older plist as the expectation,\n")
	b.WriteString("         and reinstalling from it restores the drift while printing success.\n")
	for _, j := range r.Jobs {
		fmt.Fprintf(&b, "  %s %s — %s\n", activationStateLabel(j.State, j.ScheduleDrift), j.Label, j.Detail)
	}
	fmt.Fprintf(&b, "  scope: %s\n", r.Scope)
	b.WriteString("  REPORTS ONLY: nothing here installs, bootstraps or kickstarts anything. Reconciling is\n")
	b.WriteString("  a `pogo service install*` run by a human — and it must be run from a build that\n")
	b.WriteString("  contains the plist change, or it reinstalls the schedule it was meant to replace.\n")
	return b.String()
}

func newCheckActivationCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "check-activation",
		Short: "Report managed launchd plists that disagree with the plist this build renders (never reconciles)",
		Long: `Compare every managed launchd job's INSTALLED plist against the plist this
build would write, and exit on the answer.

This is the same comparison as the ` + "`launchd activation`" + ` row in
` + "`pogo doctor --check`" + `, rendered through the same code so the two cannot
disagree. It exists separately because that row has no exit code, no caller, and
no way to be noticed when it is missing:

  EXIT CODE   doctor never fails on this on purpose — reconciling is a
              machine-local action with a blast radius, and whoever scripts
              doctor's exit status did not ask to be blocked on it. A caller that
              wants to ACT needs a status, so this command has one.
  A CALLER    the doctor row only exists when a person runs doctor. Between two
              such runs drift is silent; it was silent for seven days in the case
              mg-b201 fixed. scripts/pogo-self-deploy runs this command every
              night, from the binary the deploy just installed.
  ABSENCE     this command is TOP-LEVEL rather than under ` + "`pogo service`" + `
              because ` + "`pogo service <unknown>`" + ` exits 0 and prints help,
              while ` + "`pogo <unknown>`" + ` exits 1. A binary too old to carry
              this check must not answer a scheduled caller with a success.

EXIT CODES — three, because "could not compare" is a real answer and is not a
pass:

  0  ACTIVATED  every managed job's installed plist matches this build, the
                loaded set was enumerated, and every pogo job outside the audit
                has a recorded reason.
  1  DRIFTED    at least one installed plist differs from this build's. The
                remedy is named per job. Schedule drift is called out as FIRES,
                because a plist that differs in its fire times is a job doing a
                fraction of what the code believes it does, and every fire it
                lacks is INERT — no log line, no failure, nothing downstream that
                can observe the absence.
  3  UNKNOWN    the comparison could not be completed or could not be trusted:
                a plist that is NOT INSTALLED AT ALL, one that could not be read
                or rendered, a loaded set that could not be enumerated, or a
                loaded pogo job nobody has recorded an exclusion reason for.

WHAT IT COMPARES FROM, AND WHY THAT IS PRINTED. The plists are Go templates with
this build's constants bound in, so every verdict is relative to this binary. On
2026-08-07 the ` + "`pogo`" + ` on PATH was built before a schedule change had
merged: running its installer would have REINSTALLED the old schedule and
reported success. The build stamp is therefore part of the report, not only of
` + "`pogo version`" + `.

REPORTS ONLY. It never installs, never bootstraps, never kickstarts, and it is
not a reconciler. Making reconciliation automatic is a decision about blast
radius — ` + "`pogo service install`" + ` bounces the daemon and
` + "`install-deploy`" + ` rewrites a nightly's schedule — that mg-b9e7
deliberately does not take here.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			audits := service.AuditLaunchAgents()
			rep := buildActivationReport(
				audits,
				service.LaunchAgentsSupported(),
				service.ScopeLaunchAgents(audits),
				version.Get().Describe("pogo"),
			)
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				fmt.Print(rep.Text())
			}
			os.Exit(rep.ExitCode())
		},
	}
}
