package strandedmail

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/mgcontract"
)

// sandboxMgRoot points mg at a throwaway workspace via MG_ROOT so these tests
// touch no real mailbox. Skips when mg is not installed — this suite runs on
// machines without the fleet's tooling.
//
// The clauses are declared here rather than assumed. This package is where
// mg-d639 landed: a correct change in mg — an unknown recipient became a
// refusal instead of a phantom mailbox — turned pogo's main red through THIS
// file's fixtures, and then through five further sites the whole-gate run
// found. Both halves of that change are now named contract, so the next move
// arrives as a break in internal/mgcontract with mg-d639 on it, rather than as
// a fixture failure in a package whose own code is fine.
func sandboxMgRoot(t *testing.T) {
	t.Helper()
	mgcontract.Require(t,
		mgcontract.MailSendCreateRegistersANewMailbox,
		mgcontract.MailSendRefusesAnUnknownRecipient,
		mgcontract.MailListJSONReportsUnreadCounts,
	)
	t.Setenv("MG_ROOT", mailRoot(t))
}

func mgSend(t *testing.T, to, from, subject string) {
	t.Helper()
	// --create because this is a FIXTURE BUILDER, not a caller. mg-d639 made an
	// unknown recipient a refusal (no_such_mailbox, exit 3), and every name this
	// helper is given is unknown by construction: the sandbox's MG_ROOT is a
	// fresh empty tree, which is the whole point of the envelope in
	// testmain_test.go. The flag is the one mg documents for "the recipient
	// really is new"; it is not a way around the refusal, it is the case the
	// refusal was written to leave room for. Nothing in the package's PRODUCTION
	// path gains it — a typo'd recipient there must still be caught.
	out, err := exec.Command("mg", "mail", "send", to, "--create", "--from="+from, "--subject="+subject, "--body=fixture").CombinedOutput()
	if err != nil {
		t.Fatalf("mg mail send %s: %v\n%s", to, err, out)
	}
}

// TestAgainstRealMg drives the whole sweep through the REAL mg binary, because
// the parts most able to fail silently are the ones a fake cannot exercise: the
// NDJSON field names. If mg ever renames "unread", Detect reads 0 for every
// mailbox and this check goes permanently, cheerfully quiet — which is this
// bug's exact shape, reproduced inside the thing built to catch it. A mock
// keyed on our own struct tags could never notice.
//
// Both directions are constructed against the same binary and the same
// workspace: a genuinely stranded message must be found, and a mailbox that is
// merely empty must not be.
func TestAgainstRealMg(t *testing.T) {
	sandboxMgRoot(t)

	// The mismatch case: mail addressed to the work-item-derived box `aa96`
	// while agent `waa96` (mail-check keyed mail-check-mg-aa96) reads `waa96`.
	mgSend(t, "aa96", "mayor", "CORRECTION: retracting the causal claim in your brief")
	// A correctly-delivered message, so the fixture also contains a mailbox
	// that is being read — the sweep must not flag it.
	mgSend(t, "wfc99", "pm-pogo", "re: your consult")

	boxes, err := EnumerateMailboxes()
	if err != nil {
		t.Fatalf("EnumerateMailboxes: %v", err)
	}
	if len(boxes) == 0 {
		t.Fatal("mg enumerated no mailboxes after two deliveries — the NDJSON contract has changed")
	}
	var sawUnread bool
	for _, b := range boxes {
		if b.Unread > 0 {
			sawUnread = true
		}
	}
	if !sawUnread {
		t.Fatalf("no mailbox reported unread>0 after two deliveries; mg's field names likely changed: %+v", boxes)
	}

	checks := []MailCheck{
		{Agent: "waa96", ScheduleID: "mail-check-mg-aa96", Polled: []string{"waa96"}}, // mismatched: `aa96` is abandoned
		{Agent: "wfc99", ScheduleID: "mail-check-mg-fc99", Polled: []string{"wfc99"}}, // healthy: `fc99` never existed
	}
	rep := Detect(checks, boxes, ListMessages)

	if len(rep.Findings) != 1 {
		t.Fatalf("findings = %d, want exactly 1 (the stranded box only): %+v", len(rep.Findings), rep.Findings)
	}
	f := rep.Findings[0]
	if f.Mailbox != "aa96" {
		t.Errorf("flagged mailbox = %q, want %q", f.Mailbox, "aa96")
	}
	if f.Agent != "waa96" || f.Polls != "waa96" {
		t.Errorf("finding = %+v, want agent=waa96 polls=waa96", f)
	}
	if len(f.Messages) != 1 || f.Messages[0].From != "mayor" || !strings.Contains(f.Messages[0].Subject, "CORRECTION") {
		t.Fatalf("messages = %+v, want the mayor's correction with sender and subject intact", f.Messages)
	}

	// The recovery command the report prints must be one mg actually accepts.
	// Run the printed string itself, split into argv — so what is tested is
	// what a reader would paste, not a reconstruction of it. This is the
	// assertion that caught both defects in that line: `mg mail read` rejects
	// the bare --json id, and refuses a cross-box read without --force.
	cmdline := ReadCommand(f.Mailbox, f.Messages[0].ID)
	argv := strings.Fields(cmdline)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("the report's own recovery command failed: %s: %v\n%s", cmdline, err, out)
	}
	if !strings.Contains(string(out), "CORRECTION") {
		t.Errorf("%s did not return the stranded message:\n%s", cmdline, out)
	}
}

// TestAgainstRealMgStaysQuietWhenNothingIsStranded is the negative control, run
// against the same binary. Every mail-check is correctly pointed and every
// mailbox is read by the agent it belongs to; the sweep must produce nothing.
// Without this, TestAgainstRealMg would pass just as happily against a Detect
// that flagged every mailbox it saw.
func TestAgainstRealMgStaysQuietWhenNothingIsStranded(t *testing.T) {
	sandboxMgRoot(t)

	mgSend(t, "waa96", "mayor", "your dispatch brief")
	mgSend(t, "wfc99", "pm-pogo", "re: your consult")

	boxes, err := EnumerateMailboxes()
	if err != nil {
		t.Fatalf("EnumerateMailboxes: %v", err)
	}
	checks := []MailCheck{
		{Agent: "waa96", ScheduleID: "mail-check-mg-aa96", Polled: []string{"waa96"}},
		{Agent: "wfc99", ScheduleID: "mail-check-mg-fc99", Polled: []string{"wfc99"}},
	}
	rep := Detect(checks, boxes, ListMessages)
	if rep.Actionable() {
		t.Fatalf("sweep flagged a fleet whose mail is all correctly delivered and read: %+v", rep.Findings)
	}
	if rep.Checked != 2 {
		t.Errorf("checked = %d, want 2 — a clean answer from an empty judgement is the failure mode here", rep.Checked)
	}
}

// TestEnumerateMailboxesSurfacesAFailedMg keeps a broken mg from reading as an
// empty fleet. `mg mail list --json` failing and every mailbox being clean must
// not produce the same value.
func TestEnumerateMailboxesSurfacesAFailedMg(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, arg ...string) *exec.Cmd { return exec.Command("/nonexistent/mg", arg...) }
	if _, err := EnumerateMailboxes(); err == nil {
		t.Fatal("EnumerateMailboxes returned nil error when mg could not be run; a fleet we could not read is not a clean fleet")
	}
}
