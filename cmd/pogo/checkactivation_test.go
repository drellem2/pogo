package main

// Tests for `pogo check-activation` (mg-b9e7) — the schedulable half of the
// launchd activation audit.
//
// Three properties are load-bearing and each has its own block below:
//
//  1. UNKNOWN IS NEVER A PASS. Every state where the comparison could not be
//     completed — not installed, unreadable, an unenumerable loaded set, a
//     loaded pogo job nobody ruled on — exits 3, not 0. A caller that reads only
//     the exit status is the reason this command exists; handing it a 0 over a
//     plist nobody ever installed would be mg-b9e7's defect scored as its own
//     absence of evidence.
//  2. THE MARKER IS ON EVERY VERDICT. An old binary and a drifted box both exit
//     nonzero, and the exit codes collide at 1. Only the marker separates "this
//     binary answered" from "this binary has no such command".
//  3. THE COMMAND IS TOP-LEVEL. `pogo service <unknown>` SUCCEEDS — that is a
//     structural property of cobra, asserted here against cobra itself rather
//     than remembered from a shell session — so filing this check under
//     `service` would let an old binary answer a scheduled caller with a 0.

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/service"
)

// cleanScope is a scope with nothing outstanding: observed, and every loaded job
// either audited or excluded with a reason.
func cleanScope(labels ...string) service.LaunchAgentScope {
	return service.LaunchAgentScope{Observed: true, Loaded: labels, Audited: labels}
}

func okAudit(label string) service.LaunchAgentAudit {
	return service.LaunchAgentAudit{
		Label: label, Path: "/tmp/" + label + ".plist",
		Status: service.LaunchAgentOK, Detail: "installed plist matches this build",
	}
}

const testBuild = "pogo 0.10.0 (abc1234, branch=main, source=ldflags)"

func report(audits []service.LaunchAgentAudit, scope service.LaunchAgentScope) activationReport {
	return buildActivationReport(audits, true, scope, testBuild)
}

func TestActivationVerdicts(t *testing.T) {
	drift := service.LaunchAgentAudit{
		Label: "com.pogo.deploy", Path: "/tmp/d.plist", Status: service.LaunchAgentStale,
		ScheduleDrift: true, Remedy: "pogo service install-deploy",
		Detail: "installed plist FIRES AT DIFFERENT TIMES than this build expects",
	}
	absent := service.LaunchAgentAudit{
		Label: "com.pogo.reclaim", Path: "/tmp/r.plist", Status: service.LaunchAgentAbsent,
		Remedy: "pogo service install-reclaim", Detail: "not installed: no plist at /tmp/r.plist",
	}
	unreadable := service.LaunchAgentAudit{
		Label: "com.pogo.recovery", Path: "/tmp/v.plist", Status: service.LaunchAgentUnknown,
		Detail: "NOT CHECKED: could not be read",
	}

	cases := []struct {
		name      string
		audits    []service.LaunchAgentAudit
		supported bool
		scope     service.LaunchAgentScope
		want      string
		wantExit  int
	}{
		{
			name:      "every plist matches and the scope is fully accounted for",
			audits:    []service.LaunchAgentAudit{okAudit("com.pogo.daemon"), okAudit("com.pogo.deploy")},
			supported: true,
			scope:     cleanScope("com.pogo.daemon", "com.pogo.deploy"),
			want:      activationActivated, wantExit: cli.ExitSuccess,
		},
		{
			name:      "a stale plist is DRIFTED",
			audits:    []service.LaunchAgentAudit{okAudit("com.pogo.daemon"), drift},
			supported: true,
			scope:     cleanScope("com.pogo.daemon", "com.pogo.deploy"),
			want:      activationDrifted, wantExit: cli.ExitError,
		},
		{
			// The one place this command deliberately parts company with the
			// doctor row, which renders absent-only as a pass. A person reading
			// the row also reads the sentence naming the job; a caller reads the
			// status alone.
			name:      "a plist that was never installed is UNKNOWN, never a pass",
			audits:    []service.LaunchAgentAudit{okAudit("com.pogo.daemon"), absent},
			supported: true,
			scope:     cleanScope("com.pogo.daemon"),
			want:      activationUnknown, wantExit: cli.ExitUnknown,
		},
		{
			name:      "a plist that could not be read is UNKNOWN",
			audits:    []service.LaunchAgentAudit{okAudit("com.pogo.daemon"), unreadable},
			supported: true,
			scope:     cleanScope("com.pogo.daemon", "com.pogo.recovery"),
			want:      activationUnknown, wantExit: cli.ExitUnknown,
		},
		{
			name:      "an unenumerable loaded set is UNKNOWN even when every examined plist matches",
			audits:    []service.LaunchAgentAudit{okAudit("com.pogo.daemon")},
			supported: true,
			scope:     service.LaunchAgentScope{ObserveNote: "launchctl list failed"},
			want:      activationUnknown, wantExit: cli.ExitUnknown,
		},
		{
			name:      "a loaded pogo job with no recorded exclusion reason is UNKNOWN",
			audits:    []service.LaunchAgentAudit{okAudit("com.pogo.daemon")},
			supported: true,
			scope: service.LaunchAgentScope{
				Observed: true,
				Loaded:   []string{"com.pogo.daemon", "com.pogo.mystery"},
				Audited:  []string{"com.pogo.daemon"},
				Excluded: []service.LaunchAgentExclusion{{Label: "com.pogo.mystery"}},
			},
			want: activationUnknown, wantExit: cli.ExitUnknown,
		},
		{
			name:      "no launchd on this platform is UNKNOWN, never a pass",
			audits:    nil,
			supported: false,
			scope:     service.LaunchAgentScope{ObserveNote: "no launchd on this platform"},
			want:      activationUnknown, wantExit: cli.ExitUnknown,
		},
		{
			name:      "nothing examined at all is UNKNOWN",
			audits:    nil,
			supported: true,
			scope:     cleanScope(),
			want:      activationUnknown, wantExit: cli.ExitUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := buildActivationReport(tc.audits, tc.supported, tc.scope, testBuild)
			if r.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q (headline: %s)", r.Verdict, tc.want, r.Headline)
			}
			if got := r.ExitCode(); got != tc.wantExit {
				t.Errorf("exit = %d, want %d", got, tc.wantExit)
			}
		})
	}
}

// Drift outranks every UNKNOWN cause when both hold. A drift buried under "one
// job could not be read" is mg-8f7e's five invisible days, reproduced by the
// reporter.
func TestActivationDriftOutranksUnknown(t *testing.T) {
	drift := service.LaunchAgentAudit{Label: "com.pogo.deploy", Status: service.LaunchAgentStale, ScheduleDrift: true, Detail: "fires differently"}
	absent := service.LaunchAgentAudit{Label: "com.pogo.reclaim", Status: service.LaunchAgentAbsent, Detail: "not installed"}
	unreadable := service.LaunchAgentAudit{Label: "com.pogo.recovery", Status: service.LaunchAgentUnknown, Detail: "unreadable"}

	r := report([]service.LaunchAgentAudit{absent, unreadable, drift}, service.LaunchAgentScope{ObserveNote: "unenumerable"})
	if r.Verdict != activationDrifted {
		t.Fatalf("verdict = %q, want %q: an actionable drift must not be demoted by an uncomparable neighbour", r.Verdict, activationDrifted)
	}
	if r.ExitCode() != cli.ExitError {
		t.Errorf("exit = %d, want %d", r.ExitCode(), cli.ExitError)
	}
	if r.Drifted != 1 || r.Absent != 1 || r.Unreadable != 1 {
		t.Errorf("population = drifted %d / absent %d / unreadable %d, want 1/1/1 — the losing states must still be COUNTED, not swallowed by the leading verdict",
			r.Drifted, r.Absent, r.Unreadable)
	}
}

// Every verdict's first line is the marker line, and its second field is the
// verdict word. This is the contract scripts/pogo-self-deploy's
// classify_activation parses; without it an old binary's nonzero exit is
// indistinguishable from a drifted box's.
func TestActivationMarkerLeadsEveryVerdict(t *testing.T) {
	cases := map[string]activationReport{
		activationActivated: report([]service.LaunchAgentAudit{okAudit("com.pogo.daemon")}, cleanScope("com.pogo.daemon")),
		activationDrifted: report(
			[]service.LaunchAgentAudit{{Label: "com.pogo.deploy", Status: service.LaunchAgentStale, Detail: "differs"}},
			cleanScope("com.pogo.deploy")),
		activationUnknown: report(nil, cleanScope()),
	}
	for want, r := range cases {
		first := strings.SplitN(r.Text(), "\n", 2)[0]
		if !strings.HasPrefix(first, activationMarker+" ") {
			t.Errorf("%s: first line %q does not lead with %q", want, first, activationMarker)
		}
		fields := strings.Fields(first)
		if len(fields) < 2 || fields[1] != want {
			t.Errorf("%s: first line %q does not carry the verdict as its second field", want, first)
		}
		if r.Marker != activationMarker {
			t.Errorf("%s: JSON marker = %q, want %q — a --json caller needs the same evidence the text caller gets", want, r.Marker, activationMarker)
		}
	}
}

// The report must say which build it compared against. A drift report that does
// not is what sent 2026-08-07's reader to reinstall from a binary predating the
// schedule change — a no-op that printed success.
func TestActivationReportNamesTheBuildItComparedFrom(t *testing.T) {
	r := report([]service.LaunchAgentAudit{{Label: "com.pogo.deploy", Status: service.LaunchAgentStale, ScheduleDrift: true, Detail: "differs"}}, cleanScope("com.pogo.deploy"))
	if r.Build != testBuild {
		t.Errorf("Build = %q, want %q", r.Build, testBuild)
	}
	txt := r.Text()
	if !strings.Contains(txt, testBuild) {
		t.Errorf("the build stamp is absent from the human report:\n%s", txt)
	}
	// The stamp alone is a string nobody reads as a warning; the report has to
	// say what an older build's verdict would be worth.
	if !strings.Contains(txt, "older than a merged plist change") {
		t.Errorf("the report prints a build stamp without saying what a stale one costs:\n%s", txt)
	}
}

// Schedule drift is labelled apart from every other kind. A plist differing in a
// log path is stale; one differing in its FIRES is a job doing a fraction of
// what the code believes, with no log line for the part it skipped.
func TestActivationLabelsScheduleDriftApart(t *testing.T) {
	if got := activationStateLabel(service.LaunchAgentStale, true); !strings.HasPrefix(got, "FIRES") {
		t.Errorf("schedule drift labelled %q, want a FIRES label", got)
	}
	if got := activationStateLabel(service.LaunchAgentStale, false); !strings.HasPrefix(got, "DRIFT") {
		t.Errorf("non-schedule drift labelled %q, want DRIFT", got)
	}
	if got := activationStateLabel(service.LaunchAgentAbsent, false); !strings.HasPrefix(got, "ABSENT") {
		t.Errorf("absent labelled %q", got)
	}
	if got := activationStateLabel(service.LaunchAgentOK, false); !strings.HasPrefix(got, "OK") {
		t.Errorf("ok labelled %q", got)
	}
}

// Clean rows render too. A report listing only findings cannot be told from one
// that did not run — the same reason the doctor row renders when clean.
func TestActivationListsCleanJobsToo(t *testing.T) {
	r := report([]service.LaunchAgentAudit{okAudit("com.pogo.daemon"), {Label: "com.pogo.deploy", Status: service.LaunchAgentStale, Detail: "differs"}}, cleanScope("com.pogo.daemon", "com.pogo.deploy"))
	txt := r.Text()
	if !strings.Contains(txt, "com.pogo.daemon") {
		t.Errorf("the clean job is missing from the report:\n%s", txt)
	}
	if len(r.Jobs) != 2 {
		t.Errorf("Jobs = %d, want 2 (findings AND passes)", len(r.Jobs))
	}
}

// The two surfaces must not disagree about whether the box is clean. They share
// launchAgentActivationLine, and this pins the one direction that must hold:
// whatever makes the doctor row WARN can never make this command ACTIVATED.
//
// The converse deliberately does not hold — an absent plist is a doctor `pass`
// and an UNKNOWN here — so this is stated as an implication rather than an
// equality, which is what keeps it true when either surface changes.
func TestActivationNeverPassesWhatTheDoctorRowWarnsAbout(t *testing.T) {
	scopes := []service.LaunchAgentScope{
		cleanScope("com.pogo.daemon"),
		{ObserveNote: "unenumerable"},
		{Observed: true, Loaded: []string{"com.pogo.daemon", "com.pogo.x"}, Audited: []string{"com.pogo.daemon"},
			Excluded: []service.LaunchAgentExclusion{{Label: "com.pogo.x"}}},
	}
	auditSets := [][]service.LaunchAgentAudit{
		{okAudit("com.pogo.daemon")},
		{{Label: "com.pogo.daemon", Status: service.LaunchAgentStale, Detail: "differs"}},
		{{Label: "com.pogo.daemon", Status: service.LaunchAgentAbsent, Detail: "absent"}},
		{{Label: "com.pogo.daemon", Status: service.LaunchAgentUnknown, Detail: "unreadable"}},
	}
	for _, sc := range scopes {
		for _, as := range auditSets {
			r := report(as, sc)
			if r.DoctorStatus == "warn" && r.Verdict == activationActivated {
				t.Errorf("doctor row warns but this command reports ACTIVATED\naudits: %+v\nscope: %+v", as, sc)
			}
		}
	}
}

// THE PLACEMENT GUARD, and the reason the command is not `pogo service
// check-activation`.
//
// cobra's default Args validator rejects an unknown argument only on a ROOT
// command that has subcommands; a child command with subcommands accepts
// anything and falls through to printing help, which SUCCEEDS. So an old binary
// asked for `pogo service check-activation` exits 0 — a scheduled caller reading
// exit status would score it as a clean box, which is exactly mg-b9e7's defect
// reproduced by its own remedy.
//
// Asserted against cobra here rather than remembered from a shell session,
// because it is cobra's behaviour that the design depends on and cobra is a
// dependency that moves.
func TestUnknownSubcommandOfAParentSucceedsButUnknownRootCommandDoesNot(t *testing.T) {
	newTree := func() (*cobra.Command, *cobra.Command) {
		root := &cobra.Command{Use: "pogo"}
		root.SetOut(os.NewFile(0, os.DevNull))
		root.SetErr(os.NewFile(0, os.DevNull))
		parent := &cobra.Command{Use: "service"}
		parent.AddCommand(&cobra.Command{Use: "install", Run: func(*cobra.Command, []string) {}})
		root.AddCommand(parent)
		root.AddCommand(&cobra.Command{Use: "check-activation", Run: func(*cobra.Command, []string) {}})
		return root, parent
	}

	root, _ := newTree()
	root.SetArgs([]string{"service", "bogus-sub"})
	if err := root.Execute(); err != nil {
		t.Fatalf("cobra rejected an unknown SUBcommand (%v) — if this is now an error, revisit checkactivation.go's placement argument; it may finally be safe under `service`", err)
	}

	root2, _ := newTree()
	root2.SetArgs([]string{"bogus-top"})
	if err := root2.Execute(); err == nil {
		t.Fatal("cobra accepted an unknown ROOT command — the top-level placement no longer makes an old binary's absence loud, and the marker in the output is the only thing left holding this up")
	}
}

// The wiring itself. buildActivationReport can be perfect while nothing ever
// registers the command, and registering it under `service` would silently undo
// the property the test above pins.
func TestCheckActivationIsRegisteredTopLevel(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "rootCmd.AddCommand(newCheckActivationCmd(") {
		t.Error("check-activation is not registered on rootCmd — the command exists and nothing can reach it")
	}
	if strings.Contains(s, "cmdService.AddCommand(newCheckActivationCmd(") {
		t.Error("check-activation is registered under `pogo service`, where an old binary answers an unknown subcommand with exit 0 — see checkactivation.go's placement argument")
	}
}
