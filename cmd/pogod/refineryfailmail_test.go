package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
)

// The mail reaches a coordinator who is not at a terminal, so it carries the
// same evidence `pogo refinery show` does (mg-e5c2).

func TestSubjectClassIsVisibleWithoutOpeningTheMail(t *testing.T) {
	infra := &refinery.MergeRequest{Status: refinery.StatusFailed, FailureClass: refinery.ClassInfrastructure}
	defect := &refinery.MergeRequest{Status: refinery.StatusFailed, FailureClass: refinery.ClassDefect}
	if refineryFailureClassLabel(infra) == refineryFailureClassLabel(defect) {
		t.Fatal("both classes give the same subject label — thirty-one identical MERGE FAILED subjects is what invited thirty-one dispatches on 2026-08-05")
	}
	if got := refineryFailureClassLabel(&refinery.MergeRequest{Status: refinery.StatusFailed}); got != string(refinery.ClassUnclassified) {
		t.Errorf("an unclassified failure labels as %q, want a stable subject shape", got)
	}
}

func TestAttemptSummaryDistinguishesOnceFromSeveral(t *testing.T) {
	once := refineryAttemptSummary(&refinery.MergeRequest{AttemptCount: 1})
	many := refineryAttemptSummary(&refinery.MergeRequest{AttemptCount: 5})
	if !strings.Contains(once, "failed ONCE and was not retried") {
		t.Errorf("single-attempt summary = %q", once)
	}
	if !strings.Contains(many, "failed after 5 attempts") {
		t.Errorf("multi-attempt summary = %q", many)
	}
	if once == many {
		t.Error("the mail cannot distinguish 'failed once' from 'failed after 5' — the exact gap mg-e5c2 names")
	}
}

func TestAttemptSummaryCarriesWhyThereWasNoRetry(t *testing.T) {
	got := refineryAttemptSummary(&refinery.MergeRequest{
		AttemptCount:     1,
		NotRetriedReason: "not retryable: the test gate ran on this tree and returned a verdict",
	})
	if !strings.Contains(got, "No further retry: not retryable: the test gate ran") {
		t.Errorf("summary drops the reason: %q", got)
	}
}

// TestMailCarriesBothTransportsVerbatim is the regression for the reading error
// that cost several hours: 20 ssh failures and 11 https failures in the same
// bursts, and only the https half named the cause.
func TestMailCarriesBothTransportsVerbatim(t *testing.T) {
	mr := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 2, FailureClass: refinery.ClassInfrastructure,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "ssh", Retried: true, BackoffSeconds: 2,
				Command: "git fetch origin", RawError: "ssh: connect to host github.com port 22: Undefined error: 0"},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Transport: "https",
				Command: "git fetch origin", NotRetriedReason: "not retryable: the network retry budget is spent",
				RawError: "fatal: unable to access 'https://github.com/drellem2/pogo/': Could not resolve host: github.com"},
		},
	}
	body := refineryAttemptDetail(mr)
	for _, want := range []string{
		"transport=ssh", "transport=https",
		"Undefined error: 0", "Could not resolve host: github.com",
		"git fetch origin",
		"retried: yes, after 2s of backoff",
		"retried: NO — not retryable: the network retry budget is spent",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "normalised") && !strings.Contains(body, "never a normalised") {
		t.Error("the body claims to be a normalised summary")
	}
}

func TestNoAttemptDetailWhenThereAreNoAttempts(t *testing.T) {
	if got := refineryAttemptDetail(&refinery.MergeRequest{}); got != "" {
		t.Errorf("empty attempt detail rendered %q", got)
	}
}

// TestTriageNoteTellsACoordinatorWhatNotToDo: the note is the actionable half
// of the class, and the infrastructure one has to say "do not dispatch".
func TestTriageNoteTellsACoordinatorWhatNotToDo(t *testing.T) {
	if !strings.Contains(refinery.ClassInfrastructure.TriageNote(), "do NOT dispatch") {
		t.Errorf("infrastructure note: %q", refinery.ClassInfrastructure.TriageNote())
	}
	if !strings.Contains(refinery.ClassDefect.TriageNote(), "fix is warranted") {
		t.Errorf("defect note: %q", refinery.ClassDefect.TriageNote())
	}
}

// TestMailSaysHowLongItActuallyWaited is the mg-c3b7 requirement, and the
// distinction it exists to make: a reader must be able to tell "the network was
// down for longer than anyone could wait" from "we did not really wait".
//
// The 2026-08-10 mail could not. It reported the attempt count and the budget's
// own wording, and the fact that the whole retry campaign had lasted 52 seconds
// — against an outage since measured at 15m26s — was only in the refinery log.
func TestMailSaysHowLongItActuallyWaited(t *testing.T) {
	// The incident's own shape: 5 network attempts, 52s of backoff, spent.
	spent := &refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 5, FailureClass: refinery.ClassInfrastructure,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 2},
			{Attempt: 2, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 5},
			{Attempt: 3, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 15},
			{Attempt: 4, Stage: "fetch", Class: refinery.ClassInfrastructure, Retried: true, BackoffSeconds: 30},
			{Attempt: 5, Stage: "fetch", Class: refinery.ClassInfrastructure, NotRetriedReason: "not retryable: the network retry budget is spent"},
		},
	}
	got := refineryAttemptSummary(spent)
	if !strings.Contains(got, "Waited: 52s") {
		t.Errorf("the mail does not state the time actually slept, so a 52-second wait against a 15-minute outage reads the same as a patient one:\n%s", got)
	}
	if !strings.Contains(got, "4 retried attempt(s)") {
		t.Errorf("the mail does not say how many attempts the wait was spread over:\n%s", got)
	}

	// A failure that was never retried must not borrow the wording of one that
	// waited. Reporting "0s" as though it were a measured patience is the same
	// ambiguity in the other direction.
	none := refineryAttemptSummary(&refinery.MergeRequest{
		Status: refinery.StatusFailed, AttemptCount: 1,
		Attempts: []refinery.AttemptFailure{
			{Attempt: 1, Stage: "test", Class: refinery.ClassDefect, NotRetriedReason: "not retryable: the test gate ran on this tree and returned a verdict"},
		},
	})
	if !strings.Contains(none, "nothing here was retried") {
		t.Errorf("an unretried failure does not say so:\n%s", none)
	}
	if got == none {
		t.Error("a 52-second wait and no wait at all produce the same sentence")
	}
}

// --- Addressing: who the notice actually reaches (mg-1fcc) ---

// fakeAgentLookup is the registry slice resolveRefineryFailureAddressees uses.
// Keyed by registry name; GetByWorkItemOrName mirrors the real one's semantics
// (direct name hit first, then a WorkItemID scan).
type fakeAgentLookup struct{ agents map[string]*agent.Agent }

func (f fakeAgentLookup) Get(name string) *agent.Agent { return f.agents[name] }

func (f fakeAgentLookup) GetByWorkItemOrName(id string) *agent.Agent {
	if id == "" {
		return nil
	}
	if a := f.agents[id]; a != nil {
		return a
	}
	for _, a := range f.agents {
		if a.WorkItemID == id {
			return a
		}
	}
	return nil
}

// The observed defect, verbatim: MR mr-...naa0 on branch polecat-c32e3 authored
// as mg-32e3, with c32e3 running. The notice went to mg-32e3 only, and sat
// unread while the one actor that could resubmit had an empty inbox.
func TestFailureNoticeIsAddressedToTheAgentNotOnlyTheWorkItem(t *testing.T) {
	reg := fakeAgentLookup{agents: map[string]*agent.Agent{
		"c32e3": {Name: "c32e3", Type: agent.TypePolecat, WorkItemID: "mg-32e3"},
	}}
	got := resolveRefineryFailureAddressees(reg, &refinery.MergeRequest{
		ID: "mr-d9sljcatjv1sgaptnaa0", Branch: "polecat-c32e3", Author: "mg-32e3",
	})

	if !got.AgentResolved() || got.Agent != "c32e3" {
		t.Fatalf("the branch's owner was not resolved: Agent=%q reason=%q", got.Agent, got.Reason)
	}
	if !contains(got.Mailboxes, "c32e3") {
		t.Errorf("the agent that owns polecat-c32e3 is not a recipient: %v — this is the whole defect", got.Mailboxes)
	}
	// The work-item box may be a deliberate audit trail, and the ticket
	// sanctions sending to both. What it must not be is the ONLY recipient.
	if !contains(got.Mailboxes, "mg-32e3") {
		t.Errorf("the work-item audit mailbox was dropped: %v", got.Mailboxes)
	}
	if got.Mailboxes[0] != "c32e3" {
		t.Errorf("the actor that can resubmit is not addressed first: %v", got.Mailboxes)
	}
}

// The narrow case this ticket exists for, in c32e3's words: "an author who polls
// finds out at failure time; an author who has finished polling (or was stopped)
// finds out never." Nothing is registered for the branch, so there is no reader.
func TestAStoppedOwnerIsReportedRatherThanSilentlyDeliveredTo(t *testing.T) {
	got := resolveRefineryFailureAddressees(fakeAgentLookup{}, &refinery.MergeRequest{
		ID: "mr-x", Branch: "polecat-cdb58", Author: "mg-db58",
	})

	if got.AgentResolved() {
		t.Fatalf("claims a live agent %q with an empty registry", got.Agent)
	}
	if got.Reason == "" {
		t.Error("an unresolvable owner records no reason — the event would say nothing")
	}
	if !strings.Contains(got.Reason, "cdb58") || !strings.Contains(got.Reason, "polecat-cdb58") {
		t.Errorf("the reason does not name the owner or the branch, which is all the event has: %q", got.Reason)
	}
	// The Maildir outlives the process, so still write there — a rescuer or a
	// successor reads the agent's box, not the ticket's.
	if !contains(got.Mailboxes, "cdb58") {
		t.Errorf("the stopped owner's own mailbox was dropped: %v", got.Mailboxes)
	}
	if !contains(got.Mailboxes, "mg-db58") {
		t.Errorf("the work-item mailbox was dropped: %v", got.Mailboxes)
	}
}

// A crew agent or a human resubmitting somebody else's stranded branch (the
// mg-be37 flow): the branch names a dead polecat, the author names a live crew
// agent. Both are addressed, and the live one counts as resolved — it is the
// actor that can act.
func TestACrewAuthorResolvesEvenWhenTheBranchOwnerIsGone(t *testing.T) {
	reg := fakeAgentLookup{agents: map[string]*agent.Agent{
		"mayor": {Name: "mayor", Type: agent.TypeCrew},
	}}
	got := resolveRefineryFailureAddressees(reg, &refinery.MergeRequest{
		ID: "mr-y", Branch: "polecat-c32e3", Author: "mayor",
	})

	if got.Agent != "mayor" {
		t.Fatalf("the live author was not resolved: Agent=%q reason=%q", got.Agent, got.Reason)
	}
	if !contains(got.Mailboxes, "mayor") || !contains(got.Mailboxes, "c32e3") {
		t.Errorf("recipients = %v, want both the submitting agent and the branch's owner", got.Mailboxes)
	}
}

// A polecat registered under its bare name with no WorkItemID recorded, on a
// branch that carries no polecat prefix: the author lookup is the fallback, and
// it must still find it.
func TestANonPolecatBranchFallsBackToTheAuthorLookup(t *testing.T) {
	reg := fakeAgentLookup{agents: map[string]*agent.Agent{
		"pm-pogo": {Name: "pm-pogo", Type: agent.TypeCrew},
	}}
	got := resolveRefineryFailureAddressees(reg, &refinery.MergeRequest{
		ID: "mr-z", Branch: "roadmap-refresh", Author: "pm-pogo",
	})

	if got.Agent != "pm-pogo" {
		t.Fatalf("Agent=%q, want the author resolved off a non-polecat branch", got.Agent)
	}
	if got.BranchOwner != "" {
		t.Errorf("BranchOwner=%q for a branch with no polecat prefix", got.BranchOwner)
	}
	if len(got.Mailboxes) != 1 || got.Mailboxes[0] != "pm-pogo" {
		t.Errorf("recipients = %v, want exactly the one real mailbox (no duplicate)", got.Mailboxes)
	}
}

// The agent name and the author string can be the SAME box (crew agents author
// under their own name). Sending twice would double every notice.
func TestRecipientsAreDeduplicated(t *testing.T) {
	reg := fakeAgentLookup{agents: map[string]*agent.Agent{
		"c1fcc": {Name: "c1fcc", Type: agent.TypePolecat, WorkItemID: "mg-1fcc"},
	}}
	got := resolveRefineryFailureAddressees(reg, &refinery.MergeRequest{
		Branch: "polecat-c1fcc", Author: "c1fcc",
	})
	if len(got.Mailboxes) != 1 {
		t.Errorf("recipients = %v, want one — the agent, the branch owner and the author are the same box", got.Mailboxes)
	}
}

// A daemon with no registry must not pretend it resolved somebody.
func TestNoRegistryIsUnresolvedNotSilentlyFine(t *testing.T) {
	got := resolveRefineryFailureAddressees(nil, &refinery.MergeRequest{
		Branch: "polecat-c1fcc", Author: "mg-1fcc",
	})
	if got.AgentResolved() {
		t.Fatalf("Agent=%q with a nil registry", got.Agent)
	}
	if !strings.Contains(got.Reason, "registry") {
		t.Errorf("reason = %q, want it to name the missing registry", got.Reason)
	}
	if !contains(got.Mailboxes, "c1fcc") || !contains(got.Mailboxes, "mg-1fcc") {
		t.Errorf("recipients = %v, want both boxes attempted anyway", got.Mailboxes)
	}
}

// The event is the whole point of the unresolved branch: without it, a notice
// delivered to two mailboxes nobody reads is indistinguishable from one that
// reached its author. This asserts the record exists AND carries what a reader
// needs — which branch, and whether the boxes even accepted the mail.
func TestUnaddressedNoticeLeavesAQueryableRecord(t *testing.T) {
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	mr := &refinery.MergeRequest{
		ID: "mr-d9sljcatjv1sgaptnaag", Branch: "polecat-cdb58", Author: "mg-db58",
		TargetRef: "main", RepoPath: "/Users/daniel/dev/pogo",
		Status: refinery.StatusFailed, FailureClass: refinery.ClassInfrastructure,
	}
	addr := resolveRefineryFailureAddressees(fakeAgentLookup{}, mr)
	emitRefineryFailureNoticeUnaddressed(mr, addr, map[string]string{
		"cdb58":   "mg mail send failed: no_such_mailbox",
		"mg-db58": "delivered",
	})

	raw, err := os.ReadFile(spine)
	if err != nil {
		t.Fatalf("no event was written: %v", err)
	}
	var ev events.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &ev); err != nil {
		t.Fatalf("event is not valid JSONL: %v\n%s", err, raw)
	}
	if ev.EventType != "refinery_failure_notice_unaddressed" {
		t.Errorf("event_type = %q", ev.EventType)
	}
	if ev.Agent != "refinery" {
		t.Errorf("agent = %q, want the refinery per the event-log convention", ev.Agent)
	}
	if ev.WorkItemID != "mg-db58" {
		t.Errorf("work_item_id = %q — a record that cannot name the item cannot be acted on", ev.WorkItemID)
	}
	for _, want := range []string{"polecat-cdb58", "cdb58", "mr-d9sljcatjv1sgaptnaag", "infrastructure"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the record does not carry %q:\n%s", want, raw)
		}
	}
	// The second half of the question: a refused send (no_such_mailbox, exit 3
	// since mg-d639) would otherwise be a log line nothing can query.
	if !strings.Contains(string(raw), "no_such_mailbox") {
		t.Errorf("the record does not say a recipient REFUSED the mail:\n%s", raw)
	}
}

// The remedy is an artifact of the same kind as the defect: an event addressed
// to a field nobody populates is the same failure one layer down. This is the
// enumeration row that checks the emitter degrades rather than panics when the
// merge request is the empty one a resolver could hand it.
func TestAddresseeResolutionSurvivesAnEmptyMergeRequest(t *testing.T) {
	got := resolveRefineryFailureAddressees(fakeAgentLookup{}, &refinery.MergeRequest{})
	if len(got.Mailboxes) != 0 {
		t.Errorf("recipients = %v for an empty MR, want none invented", got.Mailboxes)
	}
	if got.AgentResolved() || got.Reason == "" {
		t.Errorf("an empty MR resolves to Agent=%q reason=%q", got.Agent, got.Reason)
	}
	if nilMR := resolveRefineryFailureAddressees(fakeAgentLookup{}, nil); nilMR.AgentResolved() {
		t.Errorf("a nil merge request resolved an agent: %q", nilMR.Agent)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
