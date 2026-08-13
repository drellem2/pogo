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

// --- The reviewer's pointer branch (mg-1af2) ---------------------------------

// reviewerRepo builds the shape that made this detector fire on every gh-issue
// reviewer: a builder branch with pushed work, and a reviewer branch pointing at
// the same head because reviewing means checking that branch out.
func reviewerRepo(t *testing.T) string {
	t.Helper()
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-paaf6", "workitem.go",
		"feat(workitem): a review ticket DECLARES the build item it reviews (mg-aaf6)")
	gitRun(t, repo, "branch", "polecat-p1c60", "polecat-paaf6")
	return repo
}

// TestReleasingAReviewerMailsNobody is the acceptance test for mg-1af2.
//
// On 2026-08-12 releasing review polecat p1c60 mailed the coordinator that it
// had "left pushed work behind on polecat-p1c60", with a remedy of
// `pogo refinery submit polecat-p1c60 --author=mg-1c60`. `git rev-parse` printed
// the same sha for polecat-p1c60 and polecat-paaf6: all four commits were the
// builder's, already reviewed, and submitted under mg-aaf6 two minutes later.
// Following the printed remedy would have submitted them a SECOND time under the
// reviewer's authorship. The only thing that caught it was a human noticing the
// commit subjects named another item.
func TestReleasingAReviewerMailsNobody(t *testing.T) {
	logPath := useTempEventLog(t)
	logs := captureLog(t)
	mail := captureStrandedMail(t)
	repo := reviewerRepo(t)

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("p1c60", "mg-1c60")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	if sent := mail(); len(sent) != 0 {
		subject, _ := sent[0].Message()
		t.Fatalf("releasing a REVIEW polecat sent %d mail(s); the branch is a pointer at the "+
			"builder's head and its remedy would double-submit the builder's work under the "+
			"reviewer's authorship. Subject was: %q", len(sent), subject)
	}
	if ev := findEvent(readEventLines(t, logPath), "work_item_stranded_push", "cat-p1c60"); ev != nil {
		t.Fatalf("a reviewer's pointer branch emitted work_item_stranded_push: %v", ev)
	}

	// Silent is not the same as suppressed-and-unobservable. A check that can
	// only ever remove an alert has to leave a trace, or "correctly identified as
	// a pointer" and "the detector stopped working" are the same absence.
	ev := findEvent(readEventLines(t, logPath), "work_item_push_carried", "cat-p1c60")
	if ev == nil {
		t.Fatal("the suppression left no work_item_push_carried event: nothing downstream can " +
			"tell this from the detector having silently died")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["carrier"].(string); got != "polecat-paaf6" {
		t.Errorf("event details.carrier = %q, want polecat-paaf6", got)
	}
	if got, _ := details["owner_item"].(string); got != "mg-aaf6" {
		t.Errorf("event details.owner_item = %q, want mg-aaf6", got)
	}
	if out := logs(); !strings.Contains(out, "NOT stranded") {
		t.Errorf("the suppression was silent in the log; got: %s", out)
	}
}

// TestReleasingTheBuilderStillMailsEvenThoughAReviewerPointsAtIt. The half that
// must not move: the builder's work IS stranded, and a reviewer having checked
// its branch out is not a reason to go quiet. mg-9a19 is why this detector
// exists.
func TestReleasingTheBuilderStillMailsEvenThoughAReviewerPointsAtIt(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := reviewerRepo(t)

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})

	a := livePolecat("paaf6", "mg-aaf6")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("the BUILDER's stranded branch sent %d mails, want 1 — the mg-1af2 fix must not "+
			"buy quiet on reviewers at the price of the case this detector was built for", len(sent))
	}
	subject, body := sent[0].Message()
	if !strings.Contains(subject, "do NOT dispatch") {
		t.Errorf("subject lost the prohibition: %q", subject)
	}
	if !strings.Contains(body, "pogo refinery submit polecat-paaf6") {
		t.Errorf("body lost the remedy:\n%s", body)
	}
}

// --- A closed item is not a re-dispatch risk (mg-1af2) -----------------------

// TestStrandedAlertDropsTheBoardParagraphForAClosedItem. In the 2026-08-12
// instance mg-1c60 was already `done`, so the notice's most emphatic paragraph —
// "the board shows the item as available and priority-wake will advertise it as
// unclaimed" — was false as well. Stating it anyway invites the reader to treat
// the whole notice as boilerplate, which is what an emphatic detector cannot
// afford. The finding itself still stands: a closed item with unmerged commits
// is a real and rarely-looked-for state.
func TestStrandedAlertDropsTheBoardParagraphForAClosedItem(t *testing.T) {
	base := StrandedAlert{
		Polecat: "9a19", WorkItemID: "mg-9a19", Repo: "/repo", Route: RouteRelease,
		Finding: strandedwork.Finding{
			Repo: "/repo", Branch: "polecat-9a19", Ref: "refs/remotes/origin/polecat-9a19",
			Pushed: true, Found: true, Target: "refs/remotes/origin/main",
			Disposition: strandedwork.DispositionResubmit,
			Unmerged:    []strandedwork.Commit{{SHA: "abc123abc123abc", Subject: "feat: x (mg-9a19)"}},
		},
	}

	open := base
	open.ItemStatus = "available"
	subject, body := open.Message()
	if !strings.Contains(body, "DO NOT DISPATCH A WORKER AT mg-9a19") {
		t.Errorf("an open item lost the do-not-dispatch paragraph:\n%s", body)
	}
	if !strings.Contains(subject, "do NOT dispatch") {
		t.Errorf("an open item lost the prohibition in the subject: %q", subject)
	}

	closed := base
	closed.ItemStatus = "done"
	subject, body = closed.Message()
	if strings.Contains(body, "priority-wake will advertise it as unclaimed") {
		t.Errorf("a done item was told the board shows it as available:\n%s", body)
	}
	if !strings.Contains(body, "NOT A RE-DISPATCH RISK") {
		t.Errorf("a done item's body does not say what IS wrong with it:\n%s", body)
	}
	if !strings.Contains(body, "never reached refs/remotes/origin/main") {
		t.Errorf("a done item's body does not name the target its branch never reached:\n%s", body)
	}
	if strings.Contains(subject, "do NOT dispatch") {
		t.Errorf("the subject still prohibits a dispatch that cannot happen: %q", subject)
	}
	if !strings.Contains(subject, "never merged") {
		t.Errorf("the subject does not carry what is actually wrong: %q", subject)
	}

	// The polarity that matters: an UNREADABLE status must leave the wording
	// exactly as it shipped, never quietly demote the alert.
	unknown := base
	if _, body := unknown.Message(); !strings.Contains(body, "DO NOT DISPATCH A WORKER AT mg-9a19") {
		t.Errorf("an unreadable status silently demoted the alert:\n%s", body)
	}
}

// TestWorkItemStatusProbeIsConsultedOnce. The probe is best-effort and its
// failure must not reach the caller — but when it answers, the answer has to
// arrive on the alert the sink receives, or the wording above can never fire in
// production.
func TestWorkItemStatusProbeIsConsultedOnce(t *testing.T) {
	mail := captureStrandedMail(t)
	var asked []string
	SetWorkItemStatusProbe(func(id string) string {
		asked = append(asked, id)
		return "done"
	})
	t.Cleanup(func() { SetWorkItemStatusProbe(nil) })

	sendStrandedAlert(StrandedAlert{Polecat: "9a19", WorkItemID: "mg-9a19", Route: RouteRelease})

	if len(asked) != 1 || asked[0] != "mg-9a19" {
		t.Errorf("probe calls = %v, want exactly [mg-9a19]", asked)
	}
	sent := mail()
	if len(sent) != 1 || sent[0].ItemStatus != "done" {
		t.Fatalf("the sink received %+v; the probed status did not reach the alert", sent)
	}
}

// TestStrandedAlertCarriesTheSecondOpinionAboveTheRemedy (mg-5ec6). The content
// second opinion exists because `git cherry` over-reports: an ordinary clean
// refinery rebase can rewrite a commit's patch id — by replaying a hunk into
// moved context, or by dropping one the target already had — and the branch then
// reads as unmerged for the rest of its life. Until mg-5ec6 that instrument had
// one consumer, `pogo check-stranded`, and none of the three routes inside pogod
// carried it.
//
// It goes ABOVE the paste-ready submit line, because it is the one paragraph that
// changes how that line should be read. The SUBJECT is deliberately untouched:
// "do NOT dispatch" is true in both worlds — if the branch really did land, the
// work is on the target and a worker sent at the item re-derives it anyway.
func TestStrandedAlertCarriesTheSecondOpinionAboveTheRemedy(t *testing.T) {
	a := StrandedAlert{
		Polecat: "a3d4", WorkItemID: "mg-a3d4", Repo: "/repo", Route: RouteRelease,
		ItemStatus: "available",
		Finding: strandedwork.Finding{
			Repo: "/repo", Branch: "polecat-a3d4", Ref: "refs/remotes/origin/polecat-a3d4",
			Pushed: true, Found: true, Target: "refs/remotes/origin/main",
			Disposition: strandedwork.DispositionResubmit,
			Unmerged:    []strandedwork.Commit{{SHA: "c2f1854cea4f", Subject: "probe: price the bet (mg-a3d4)"}},
		},
		Presence:      strandedwork.Presence{Added: 2063, Present: 2008, Measured: true},
		SecondOpinion: "SECOND OPINION SAYS THIS MAY ALREADY HAVE LANDED UNDER A DIFFERENT SHA: 2008 of 2063 added line(s) already in the target (97%). It does not clear the row.",
	}

	subject, body := a.Message()
	if !strings.Contains(body, "MAY ALREADY HAVE LANDED") {
		t.Fatalf("the second opinion did not reach the mail body; the reader is told the branch is "+
			"unmerged with no way to see the 97%%:\n%s", body)
	}
	remedy := strings.Index(body, "WHAT TO DO")
	note := strings.Index(body, "SECOND OPINION")
	if note < 0 || remedy < 0 || note > remedy {
		t.Errorf("the second opinion (at %d) is below the remedy (at %d); the caveat has to be read "+
			"before the submit line, not after it", note, remedy)
	}
	if !strings.Contains(subject, "do NOT dispatch") {
		t.Errorf("the subject lost the prohibition because the second opinion hedged it: %q. "+
			"A branch that landed is a reason NOT to dispatch, not a reason to", subject)
	}
	// An alert with nothing to say adds no paragraph at all.
	bare := a
	bare.SecondOpinion = ""
	if _, body := bare.Message(); strings.Contains(body, "SECOND OPINION") {
		t.Errorf("an empty second opinion rendered a heading anyway:\n%s", body)
	}
}

// TestWrapAtKeepsLongTokensIntact. The second opinion arrives as one long line
// and is wrapped for the mail; a wrap that broke a sha or a branch name across
// two lines would make the one thing the reader has to copy uncopyable.
func TestWrapAtKeepsLongTokensIntact(t *testing.T) {
	long := "polecat-a3d4-with-an-unreasonably-long-name-nobody-would-ever-actually-use-but-still"
	got := wrapAt("the branch "+long+" is the one to look at", 40)
	if !strings.Contains(got, long) {
		t.Errorf("wrapAt broke a token longer than the width:\n%s", got)
	}
	for _, line := range strings.Split(wrapAt(strings.Repeat("word ", 40), 40), "\n") {
		if len(line) > 40 {
			t.Errorf("wrapAt produced a %d-column line: %q", len(line), line)
		}
	}
}
