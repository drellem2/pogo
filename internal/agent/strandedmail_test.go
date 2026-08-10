package agent

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/strandedwork"
)

// captureStrandedMail installs a capturing mail sink for one test and returns a
// reader for what it received. It replaces the SINK, not `mg`: what is under
// test is whether the emit site addresses anybody at all, which was the whole
// defect (mg-be37) — the detector was correct and unread.
func captureStrandedMail(t *testing.T) func() []StrandedAlert {
	t.Helper()
	var got []StrandedAlert
	prev := strandedAlertMail
	strandedAlertMail = func(a StrandedAlert) { got = append(got, a) }
	t.Cleanup(func() { strandedAlertMail = prev })
	return func() []StrandedAlert { return got }
}

// TestReleasePolecatClaimMailsTheStrandedBranch is the acceptance test for this
// ticket.
//
// The state under test is the one that occurred five times on 2026-08-09: a
// polecat is released with pushed, unmerged work behind it. The detector already
// fired on all five and nobody saw any of them, because its only outputs were
// pogod's log and events.log — `work_item_stranded_push` had no consumer
// anywhere in the tree. The measured gap between detection and a human noticing
// was ~1h, 2.5h and ~3h. The event alone is therefore NOT sufficient evidence
// that this works, which is why this test asserts on the mail and not on the
// event the sibling test already covers.
func TestReleasePolecatClaimMailsTheStrandedBranch(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("9a19", "mg-9a19")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("releasing a polecat with pushed unmerged work sent %d mails, want 1 — the detector "+
			"fired 5/5 on 2026-08-09 and nothing read it; an unaddressed report is the defect (mg-be37)",
			len(sent))
	}
	m := sent[0]
	if m.WorkItemID != "mg-9a19" || m.Repo != repo || m.Route != RouteRelease {
		t.Errorf("alert = {item %q repo %q route %q}, want {mg-9a19 %q %q}",
			m.WorkItemID, m.Repo, m.Route, repo, RouteRelease)
	}
	if m.StillAlive {
		t.Error("a released polecat was reported as still alive; the release route cannot know that")
	}

	subject, body := m.Message()
	// The subject is the part that travels. A conditional in paragraph three
	// does not exist for a reader skimming a mailbox.
	if !strings.Contains(subject, "polecat-9a19") || !strings.Contains(subject, "do NOT dispatch") {
		t.Errorf("subject does not carry the branch AND the prohibition: %q", subject)
	}
	for _, want := range []string{
		"polecat-9a19",
		sha[:12],
		"pogo refinery submit polecat-9a19 --repo=" + repo,
		"--author=mg-9a19",
		"DO NOT DISPATCH A WORKER AT mg-9a19",
		"1 commit(s)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body is missing %q — it must carry branch, commit, unmerged count, the\n"+
				"paste-ready submit line and the do-not-dispatch warning.\nGot:\n%s", want, body)
		}
	}
}

// TestReleasePolecatClaimMailsNothingWhenNothingStranded. The other polarity,
// and the one that decides whether this is a detector or a nag. A mail on every
// stop is a mail nobody reads, which is the failure this ticket is about
// reproduced in its own remedy.
func TestReleasePolecatClaimMailsNothingWhenNothingStranded(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("9a19", "mg-9a19") // no branch was ever pushed
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("a polecat with no branch produced %d mail(s): %+v", len(sent), sent)
	}
}

// TestReleasePolecatClaimMailsNothingWhenItCannotCheck. UNJUDGED must not become
// a mail either — but it must stay in the log, which the sibling test asserts.
// Mailing on every unreadable repo would train the reader to filter this alert,
// and the alert is the only thing standing between an available item and a
// dispatch that destroys its work.
func TestReleasePolecatClaimMailsNothingWhenItCannotCheck(t *testing.T) {
	logs := captureLog(t)
	mail := captureStrandedMail(t)
	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("9a19", "mg-9a19")
	a.SourceRepo = t.TempDir() // not a git repository
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("an unanswerable check produced %d mail(s): %+v", len(sent), sent)
	}
	if out := logs(); !strings.Contains(out, "could NOT check") {
		t.Errorf("the unanswerable case went silent everywhere; got: %s", out)
	}
}

// TestStrandedRecipientsAlwaysIncludeTheCoordinator. The coordinator is
// unconditional and FIRST. An alert whose addressee list can resolve to empty is
// this ticket's defect rebuilt one layer down, so no probe may gate it.
func TestStrandedRecipientsAlwaysIncludeTheCoordinator(t *testing.T) {
	got := strandedRecipients("/definitely/not/a/repo/with/a/pm-xyzzy-nobody-has")
	if len(got) == 0 || got[0] != CoordinatorName() {
		t.Fatalf("strandedRecipients = %v, want the coordinator (%q) first and unconditional",
			got, CoordinatorName())
	}
	for _, r := range got {
		if r == "" {
			t.Fatalf("strandedRecipients returned an empty recipient: %v", got)
		}
	}
}

// TestRepoPMCandidateIsDerivedFromTheRepoDirectory documents the guess that
// strandedRecipients probes before using. It is a candidate and never a
// recipient on its own — mg-f04b removed a literal `pm-pogo` from this tree
// because such names belong to one machine's fleet.
func TestRepoPMCandidateIsDerivedFromTheRepoDirectory(t *testing.T) {
	cases := map[string]string{
		"/Users/daniel/dev/pogo":                  "pm-pogo",
		"/Users/daniel/research/onethird_program": "pm-onethird_program",
		"/Users/daniel/dev/POGO/":                 "pm-pogo",
		"":                                        "",
		"/":                                       "",
	}
	for repo, want := range cases {
		if got := repoPMCandidate(repo); got != want {
			t.Errorf("repoPMCandidate(%q) = %q, want %q", repo, got, want)
		}
	}
}

// TestStrandedAlertMessageWarnsWhenTheWorkIsNotOnOrigin. "Stranded on origin" is
// recoverable at leisure; "stranded in a worktree git-gc is about to reap" is
// not, and the reader must be able to tell those apart from the mail alone.
func TestStrandedAlertMessageWarnsWhenTheWorkIsNotOnOrigin(t *testing.T) {
	local := StrandedAlert{
		Polecat: "9a19", WorkItemID: "mg-9a19", Repo: "/repo", Route: RouteRelease,
		Finding: strandedwork.Finding{
			Repo: "/repo", Branch: "polecat-9a19", Ref: "refs/heads/polecat-9a19",
			Pushed: false, Found: true, Target: "refs/remotes/origin/main",
			Disposition: strandedwork.DispositionResubmit,
			Unmerged:    []strandedwork.Commit{{SHA: "abc123abc123abc", Subject: "feat: x (mg-9a19)"}},
		},
	}
	_, body := local.Message()
	if !strings.Contains(body, "THE WORK IS NOT ON ORIGIN") {
		t.Errorf("a local-only branch did not say so:\n%s", body)
	}

	pushed := local
	pushed.Finding.Pushed = true
	pushed.Finding.Ref = "refs/remotes/origin/polecat-9a19"
	if _, body := pushed.Message(); strings.Contains(body, "THE WORK IS NOT ON ORIGIN") {
		t.Errorf("a pushed branch was described as local-only:\n%s", body)
	}
}

// TestStrandedAlertMessageCarriesThePreRegistrationRule. Resubmit advice
// followed against a pre-registration branch loses nothing; pre-registration
// advice skipped in favour of resubmit advice loses the control silently. So
// when both apply the mail must carry the stronger one.
func TestStrandedAlertMessageCarriesThePreRegistrationRule(t *testing.T) {
	pre := strandedwork.Commit{SHA: "0123456789abcdef", Subject: "pre-registration: predicted failures"}
	a := StrandedAlert{
		Polecat: "9a19", WorkItemID: "mg-9a19", Repo: "/repo", Route: RouteRelease,
		Finding: strandedwork.Finding{
			Repo: "/repo", Branch: "polecat-9a19", Ref: "refs/remotes/origin/polecat-9a19",
			Pushed: true, Found: true, Target: "refs/remotes/origin/main",
			Disposition:     strandedwork.DispositionPreRegistration,
			Unmerged:        []strandedwork.Commit{pre},
			PreRegistration: &pre,
		},
	}
	_, body := a.Message()
	if !strings.Contains(body, "PRE-REGISTRATION COMMIT") {
		t.Errorf("a pre-registration branch's mail did not name the control:\n%s", body)
	}
}
