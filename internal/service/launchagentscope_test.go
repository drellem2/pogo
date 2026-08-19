package service

// Tests for the activation audit's own denominator (mg-7a20).

import (
	"strings"
	"testing"
)

// TestParseLaunchctlList is the observed half of the denominator, on the exact
// bytes `launchctl list` emits — header line, "-" in either numeric column for a
// loaded-but-not-running job, and every non-pogo job on the box mixed in.
func TestParseLaunchctlList(t *testing.T) {
	out := strings.Join([]string{
		"PID\tStatus\tLabel",
		"5458\t0\tcom.pogo.pa-heyfeed",
		"-\t0\tcom.pogo.deploy",
		"51243\t-9\tcom.pogo.daemon",
		"401\t0\tcom.apple.SafariHistoryServiceAgent",
		"-\t0\tcom.pogo.recovery",
		"",
	}, "\n")

	got := parseLaunchctlList(out)
	want := []string{"com.pogo.pa-heyfeed", "com.pogo.deploy", "com.pogo.daemon", "com.pogo.recovery"}
	if len(got) != len(want) {
		t.Fatalf("parseLaunchctlList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseLaunchctlList = %v, want %v", got, want)
		}
	}
}

// TestParseLaunchctlListIgnoresShortLines. A job whose label alone survives on a
// line — a truncated read, a blank, the header of some future launchctl — must
// not become a phantom pogo job, because a phantom is reported as an unexplained
// exclusion and unexplained exclusions are the thing an operator is asked to act
// on.
func TestParseLaunchctlListIgnoresShortLines(t *testing.T) {
	if got := parseLaunchctlList("com.pogo.daemon\n\n   \ngarbage\n"); len(got) != 0 {
		t.Errorf("parseLaunchctlList = %v, want nothing from lines that are not three columns", got)
	}
}

// TestScopeSplitsExplainedFromUnexplained is the finding this ticket names: of
// the ten jobs outside the registry on the box it was measured on, three carried
// a recorded reason and seven carried none, and the row said nothing about
// either. The split is the output.
func TestScopeSplitsExplainedFromUnexplained(t *testing.T) {
	audits := []LaunchAgentAudit{{Label: launchdLabel}, {Label: recoveryLabel}, {Label: deployLabel}}
	loaded := []string{
		launchdLabel, recoveryLabel, deployLabel,
		"com.pogo.notify", "com.pogo.revisionprobe", "com.pogo.fleetliveness", // recorded reasons
		"com.pogo.some-new-thing", // nobody has ruled on this one
	}

	s := scopeLaunchAgents(audits, loaded)

	if !s.Observed {
		t.Fatal("Observed = false for a successful enumeration")
	}
	if len(s.Audited) != 3 {
		t.Errorf("Audited = %v, want the three registry jobs", s.Audited)
	}
	if len(s.Excluded) != 4 {
		t.Fatalf("Excluded = %+v, want the four loaded jobs outside the registry", s.Excluded)
	}
	un := s.Unexplained()
	if len(un) != 1 || un[0].Label != "com.pogo.some-new-thing" {
		t.Errorf("Unexplained = %+v, want exactly the job with no recorded reason", un)
	}
}

// TestScopeReasonsAreNotJustRestatements. A reason has to say why bringing the
// job into the registry is not a small edit, or it is the sentence "it is not in
// the registry" wearing a different hat — which would let ten silent exclusions
// become ten explained ones without anything having been decided.
func TestScopeReasonsAreNotJustRestatements(t *testing.T) {
	for label, reason := range launchAgentExclusionReasons() {
		if len(reason) < 40 {
			t.Errorf("%s: reason %q is too short to be a decision", label, reason)
		}
		if !strings.Contains(reason, "installed by") && !strings.Contains(reason, "rendered by") {
			t.Errorf("%s: reason %q does not say who installs the job — the reason this audit cannot render an expected copy is that another install path owns it", label, reason)
		}
	}
}

// TestScopeCountsARegistryJobOnlyWhenItIsLoaded. Audited is registry ∩ loaded,
// not the registry: a job the registry examines and finds NOT INSTALLED must not
// count toward coverage of the box, or the covered number reports jobs that are
// not there.
func TestScopeCountsARegistryJobOnlyWhenItIsLoaded(t *testing.T) {
	s := scopeLaunchAgents(
		[]LaunchAgentAudit{{Label: launchdLabel}, {Label: deployLabel}},
		[]string{launchdLabel, "com.pogo.notify"},
	)
	if len(s.Audited) != 1 || s.Audited[0] != launchdLabel {
		t.Errorf("Audited = %v, want only the registry job that is actually loaded", s.Audited)
	}
	if len(s.Excluded) != 1 || s.Excluded[0].Label != "com.pogo.notify" {
		t.Errorf("Excluded = %+v, want the loaded job outside the registry and nothing else", s.Excluded)
	}
}

// TestScopeOnAnEmptyBoxIsObservedNotUnknown. Zero loaded pogo jobs is a real
// observation and must not render as a failed one; the reverse — a failed read
// rendering as zero-outside — is the defect this file exists for and is pinned in
// the doctor row's tests.
func TestScopeOnAnEmptyBoxIsObservedNotUnknown(t *testing.T) {
	s := scopeLaunchAgents(nil, nil)
	if !s.Observed || len(s.Excluded) != 0 {
		t.Errorf("scope = %+v, want an observed empty box", s)
	}
}

// TestRegistryJobsHaveNoExclusionReason. An entry for a job that IS audited is
// unreachable, and an unreachable entry is how a reader concludes the job is
// excluded when it is not.
func TestRegistryJobsHaveNoExclusionReason(t *testing.T) {
	reasons := launchAgentExclusionReasons()
	for _, a := range managedLaunchAgents() {
		if r, ok := reasons[a.Label]; ok {
			t.Errorf("%s is in the registry AND has an exclusion reason (%q); one of the two is wrong", a.Label, r)
		}
	}
}
