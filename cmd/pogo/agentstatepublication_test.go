package main

// Tests for the "agent-state repo publication" doctor row (mg-015c).

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/homevcs"
)

func subjects(labels ...string) []homevcs.Subject {
	var out []homevcs.Subject
	for _, l := range labels {
		out = append(out, homevcs.Subject{Label: l, Dir: "/dir/" + l})
	}
	return out
}

// TestAgentStatePublicationLine_PublicFails is the ticket. Daniel's ruling —
// "pogo-config absolutely should not be public" — is a standing constraint on
// the state, and a constraint reported at the same volume as a tidiness note is
// one nobody can script against. This row's exit code is the difference.
func TestAgentStatePublicationLine_PublicFails(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME"),
		Repos: []homevcs.RepoPublication{{
			Toplevel: "/Users/x/.pogo", Remote: "https://github.com/drellem2/pogo-config.git",
			Name: "drellem2/pogo-config", Holds: []string{"$POGO_HOME"},
			Visibility: homevcs.VisibilityPublic,
		}},
	})
	if status != "fail" {
		t.Fatalf("status = %q, want fail for a PUBLIC agent-state repo; detail = %q", status, detail)
	}
	for _, want := range []string{"SECURITY", "PUBLIC", "world-readable", "drellem2/pogo-config", "--visibility private"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q\ndetail = %q", want, detail)
		}
	}
	// The remedy must be pasteable: `gh repo edit` takes owner/name, not a
	// clone URL, so echoing the remote verbatim would hand the reader a
	// command that does not run.
	if strings.Contains(detail, "pogo-config.git --visibility") {
		t.Errorf("the remedy names the clone URL where `gh repo edit` wants owner/name\ndetail = %q", detail)
	}
}

// TestAgentStatePublicationLine_PublicOutranksAndLeads. The row carries every
// subject repo, and both properties matter: a clean repo must not pull the
// status down from fail, and the exposure must head the line, because the front
// of a checklist detail is the part that gets skimmed and forwarded.
func TestAgentStatePublicationLine_PublicOutranksAndLeads(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME", "agent memory"),
		Repos: []homevcs.RepoPublication{
			// AuditPublication sorts most-exposed first; the renderer must
			// preserve that order rather than re-deriving one.
			{Toplevel: "/mem", Remote: "https://github.com/drellem2/pogo-agent-memory.git",
				Name: "drellem2/pogo-agent-memory", Holds: []string{"agent memory"},
				Visibility: homevcs.VisibilityPublic},
			{Toplevel: "/Users/x/.pogo", Remote: "https://github.com/drellem2/pogo-config.git",
				Name: "drellem2/pogo-config", Holds: []string{"$POGO_HOME"},
				Visibility: homevcs.VisibilityPrivate},
		},
	})
	if status != "fail" {
		t.Fatalf("status = %q, want fail; a private repo must not mask a public one", status)
	}
	pub := strings.Index(detail, "pogo-agent-memory")
	priv := strings.Index(detail, "pogo-config")
	if pub < 0 || priv < 0 || pub > priv {
		t.Errorf("detail = %q, want the PUBLIC repo named before the private one", detail)
	}
}

// TestAgentStatePublicationLine_AllPrivateIsAPassThatNamesWhatItChecked. Both
// live subjects are private, so this row will spend its life green — which is
// exactly why the green must say what it looked at. A row that speaks only on
// exposure is indistinguishable from a row that stopped running.
func TestAgentStatePublicationLine_AllPrivateIsAPassThatNamesWhatItChecked(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME", "agent memory"),
		Repos: []homevcs.RepoPublication{
			{Toplevel: "/Users/x/.pogo", Remote: "https://github.com/drellem2/pogo-config.git",
				Name: "drellem2/pogo-config", Holds: []string{"$POGO_HOME"},
				Visibility: homevcs.VisibilityPrivate},
			{Toplevel: "/mem", Remote: "https://github.com/drellem2/pogo-agent-memory.git",
				Name: "drellem2/pogo-agent-memory", Holds: []string{"agent memory"},
				Visibility: homevcs.VisibilityPrivate},
		},
	})
	if status != "pass" {
		t.Fatalf("status = %q, want pass when every subject repo is private; detail = %q", status, detail)
	}
	for _, want := range []string{"checked 2 agent-state directories", "2 repositories", "drellem2/pogo-config", "drellem2/pogo-agent-memory", "is PRIVATE"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q\ndetail = %q", want, detail)
		}
	}
}

// TestAgentStatePublicationLine_UnestablishedIsNotPass. The instrument class
// this ticket was filed against is the one that goes quiet when it cannot see.
// An unauthenticated, offline or rate-limited `gh` must be loud, and must not be
// spelled as PRIVATE.
func TestAgentStatePublicationLine_UnestablishedIsNotPass(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME"),
		Repos: []homevcs.RepoPublication{{
			Toplevel: "/Users/x/.pogo", Remote: "https://github.com/drellem2/pogo-config.git",
			Name: "drellem2/pogo-config", Holds: []string{"$POGO_HOME"},
			Unknown: "gh is not on PATH, so nothing asked whether drellem2/pogo-config is published",
		}},
	})
	if status != "warn" {
		t.Fatalf("status = %q, want warn when the publication state was not established; detail = %q", status, detail)
	}
	for _, want := range []string{"NOT ESTABLISHED", "NOT as private", "gh is not on PATH", "gh repo view"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q\ndetail = %q", want, detail)
		}
	}
	if strings.Contains(detail, "is PRIVATE") {
		t.Errorf("detail = %q claims PRIVATE for a state nothing established", detail)
	}
}

// TestAgentStatePublicationLine_UndecidedSubjectIsNotPass. A subject directory
// this run could not resolve is the same unearned all-clear one level up.
func TestAgentStatePublicationLine_UndecidedSubjectIsNotPass(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects:  subjects("$POGO_HOME"),
		Undecided: []string{"$POGO_HOME (/Users/x/.pogo): git is not on PATH"},
	})
	if status != "warn" {
		t.Errorf("status = %q, want warn for a subject nothing could resolve; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "NOT ESTABLISHED") {
		t.Errorf("detail = %q, want the disclaimer", detail)
	}
}

// TestAgentStatePublicationLine_MissingVerdictIsNotPass guards the seam between
// the audit and this renderer. A repo carrying a remote, no verdict and no
// reason is a defect in the check — falling through to the clean branch is how a
// detector stops detecting with no test going red.
func TestAgentStatePublicationLine_MissingVerdictIsNotPass(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME"),
		Repos: []homevcs.RepoPublication{{
			Toplevel: "/Users/x/.pogo", Remote: "https://github.com/drellem2/pogo-config.git",
			Name: "drellem2/pogo-config", Holds: []string{"$POGO_HOME"},
		}},
	})
	if status == "pass" {
		t.Fatalf("status = pass for a remote with no publication verdict; detail = %q", detail)
	}
	if !strings.Contains(detail, "NO publication verdict") {
		t.Errorf("detail = %q, want the missing verdict named as a defect in this check", detail)
	}
}

// TestAgentStatePublicationLine_NoSubjectsIsNotPass. A caller that enumerated
// nothing has a bug, and the row must not read as an all-clear on its behalf.
func TestAgentStatePublicationLine_NoSubjectsIsNotPass(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{})
	if status != "warn" {
		t.Errorf("status = %q, want warn when nothing was enumerated; detail = %q", status, detail)
	}
}

// TestAgentStatePublicationLine_NonPrivateButNotPublicIsNamed. GitHub's INTERNAL
// is neither world-readable nor private. Bucketing it into either would be a
// claim the measurement does not support.
func TestAgentStatePublicationLine_NonPrivateButNotPublicIsNamed(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME"),
		Repos: []homevcs.RepoPublication{{
			Toplevel: "/Users/x/.pogo", Remote: "https://github.com/acme/pogo-config.git",
			Name: "acme/pogo-config", Holds: []string{"$POGO_HOME"},
			Visibility: homevcs.VisibilityInternal,
		}},
	})
	if status != "warn" {
		t.Errorf("status = %q, want warn for INTERNAL; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "INTERNAL") || !strings.Contains(detail, "not PRIVATE") {
		t.Errorf("detail = %q, want the state named verbatim", detail)
	}
}

// TestAgentStatePublicationLine_LocalOnlyRepoIsAClearPass: a subject versioned
// by a local-only repo publishes nothing, which is a decided answer. Reporting
// it as unchecked would make every developer's box a standing warning and train
// readers past the row.
func TestAgentStatePublicationLine_LocalOnlyRepoIsAClearPass(t *testing.T) {
	status, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME"),
		Repos: []homevcs.RepoPublication{{
			Toplevel: "/Users/x/.pogo", Holds: []string{"$POGO_HOME"},
		}},
	})
	if status != "pass" {
		t.Errorf("status = %q, want pass for a repo with no origin; detail = %q", status, detail)
	}
	if !strings.Contains(detail, "no origin remote") {
		t.Errorf("detail = %q, want it to say why publication is not at issue", detail)
	}
}

// TestAgentStatePublicationLine_HoldsAreCapped keeps the dozen per-agent memory
// dirs that all resolve to $POGO_HOME's work tree from turning one row into a
// wall.
func TestAgentStatePublicationLine_HoldsAreCapped(t *testing.T) {
	holds := []string{"$POGO_HOME"}
	for i := 0; i < agentStateMaxHolds+3; i++ {
		holds = append(holds, "agent memory "+string(rune('a'+i)))
	}
	_, detail := agentStatePublicationLine(homevcs.PublicationReport{
		Subjects: subjects("$POGO_HOME"),
		Repos: []homevcs.RepoPublication{{
			Toplevel: "/Users/x/.pogo", Remote: "https://github.com/drellem2/pogo-config.git",
			Name: "drellem2/pogo-config", Holds: holds, Visibility: homevcs.VisibilityPrivate,
		}},
	})
	if !strings.Contains(detail, "(+4 more)") {
		t.Errorf("detail = %q, want the elided remainder counted", detail)
	}
}

// TestDoctorCheck_AgentStatePublicationRowAlwaysRenders runs the real binary. A
// row that exists only in a unit test is a row that can be dropped from the
// checklist without a single failure — and this one guards a standing ruling, so
// its presence is the whole product.
func TestDoctorCheck_AgentStatePublicationRowAlwaysRenders(t *testing.T) {
	home := t.TempDir()
	line, ok := doctorChecks(t, []string{"POGO_HOME=" + home})[agentStatePublicationCheckName]
	if !ok {
		t.Fatalf("no %q row in doctor --check; the detector must be visible even when it finds nothing", agentStatePublicationCheckName)
	}
	if !strings.Contains(line, "checked") {
		t.Errorf("row = %q, want it to name the population it examined", line)
	}
}
