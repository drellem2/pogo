package ineffect

import (
	"strings"
	"testing"
)

func sampleReport() Report {
	return Report{
		Commit:   "abcdef0123456789",
		Subject:  "a subject",
		When:     "2026-08-19T00:00:00Z",
		Reporter: "fedcba9876543210",
		Findings: []Finding{{
			Class: ClassCompiled,
			Paths: []string{"internal/agent/api.go"},
			Carriers: []CarrierVerdict{
				{Carrier: "running pogod", At: "http://127.0.0.1:10000/version", Observed: "111111111111", Verdict: Inert, Why: "111111111111 does not contain abcdef012345", Remedy: "restart pogod"},
				{Carrier: "installed pogo", At: "/bin/pogo", Observed: "222222222222", Verdict: Live, Why: "222222222222 contains abcdef012345"},
			},
			Note: "a compiled change needs both a rebuild and a restart",
		}},
		Overall: OverallHalfLive,
		Summary: "HALF-LIVE: 1 carrier(s) carry this commit and 1 do not (running pogod)",
	}
}

// Every row must carry BOTH the verdict word and the carrier name, and must
// name where it was measured. The failure being fixed is a correct global
// answer applied to the wrong artifact; a row that says only `INERT` invites
// exactly that.
func TestTextRowsNameVerdictCarrierAndSite(t *testing.T) {
	out := sampleReport().Text()
	for _, want := range []string{
		"INERT", "running pogod", "http://127.0.0.1:10000/version",
		"LIVE", "installed pogo", "/bin/pogo",
		"111111111111 does not contain",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Text() is missing %q:\n%s", want, out)
		}
	}
}

// The remedies collapse into ONE closing line. Repeating a remedy under each of
// five rows buries the fact that only two actions are owed, and a reader who
// skims the last line is the reader this is for.
func TestRemediesCollapseIntoOneOwedLine(t *testing.T) {
	out := sampleReport().Text()
	if strings.Count(out, "owed:") != 1 {
		t.Errorf("want exactly one `owed:` line:\n%s", out)
	}
	if !strings.Contains(out, "owed: restart pogod") {
		t.Errorf("the owed line does not carry the remedy:\n%s", out)
	}
}

// A LIVE carrier carries no remedy — an action printed beside a carrier that
// needs none is how a reader learns to skip the line.
func TestLiveRowsCarryNoRemedy(t *testing.T) {
	r := Report{
		Findings: []Finding{{Class: ClassCompiled, Paths: []string{"x.go"}, Carriers: []CarrierVerdict{
			{Carrier: "installed pogo", Verdict: Live, Why: "contains it", Remedy: "reinstall"},
		}}},
		Overall: OverallLive, Summary: "IN EFFECT",
	}
	if strings.Contains(r.Text(), "owed:") {
		t.Errorf("a report with only live carriers printed an owed line:\n%s", r.Text())
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[Overall]int{
		OverallLive:       0,
		OverallNoCarriers: 0,
		OverallInert:      1,
		OverallHalfLive:   1,
		OverallUnknown:    3,
	}
	for overall, want := range cases {
		if got := (Report{Overall: overall}).ExitCode(); got != want {
			t.Errorf("ExitCode(%q) = %d, want %d", overall, got, want)
		}
	}
}

// An unstamped reporter renders as a sentinel, never as a blank. A blank where
// a revision belongs reads as "nothing to see".
func TestUnstampedReporterIsSaidOutLoud(t *testing.T) {
	r := sampleReport()
	r.Reporter = ""
	if !strings.Contains(r.Text(), "<unstamped>") {
		t.Errorf("Text() hides an unstamped reporter:\n%s", r.Text())
	}
}

func TestErrorReportRendersTheReason(t *testing.T) {
	r := Report{Err: "boom", Summary: "cannot answer: boom", Overall: OverallUnknown}
	if !strings.Contains(r.Text(), "cannot answer: boom") {
		t.Errorf("Text() = %q, want the reason", r.Text())
	}
}

// The provenance caveat renders when it applies and stays quiet when it does
// not. A warning on every run — including the runs where the reporter's
// revision came from a build script that knew which repo it was building — is
// one readers learn to skip, and this one has to survive to the run where a
// linked-worktree build is printing the enclosing repo's HEAD.
func TestProvenanceCaveatRendersOnlyWhenItApplies(t *testing.T) {
	trusted := sampleReport()
	trusted.Reporter = "pogo 0.10.0 (abc1234, branch=main, source=ldflags)"
	if strings.Contains(trusted.Text(), "mg-8d0f") {
		t.Errorf("the caveat rendered against an ldflags-stamped reporter:\n%s", trusted.Text())
	}

	for _, reporter := range []string{
		"pogo 0.10.0 (d6d179f, branch=unknown, source=buildinfo)",
		"", // unstamped: the least trustworthy of all
	} {
		r := sampleReport()
		r.Reporter = reporter
		if !strings.Contains(r.Text(), "mg-8d0f") {
			t.Errorf("no caveat for reporter %q, whose revision does not name this repo by construction:\n%s", reporter, r.Text())
		}
	}
}
