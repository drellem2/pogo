package refusalwatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireMG resolves the real macguffin binary or skips. The skip is the
// declared residual limit of every mg-driven test in this repo: on a box with no
// `mg` this proves nothing, and says so rather than passing.
func requireMG(t *testing.T) string {
	t.Helper()
	bin, err := MGBinary()
	if err != nil {
		t.Skipf("no `mg` binary: %v — this test drives the real CLI and proves nothing without it", err)
	}
	return bin
}

// newStore builds a throwaway macguffin store with NOTHING in it.
func newStore(t *testing.T, bin string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "macguffin")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustMG(t, bin, root, "init")
	return root
}

func mustMG(t *testing.T, bin, root string, args ...string) string {
	t.Helper()
	out, err := runMG(bin, root, args...)
	if err != nil {
		t.Fatalf("`mg %s` on the throwaway store: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func alarmFixture() Alarm { return probeAlarm(time.Date(2026, 9, 7, 11, 41, 11, 0, time.UTC)) }

// ------------------------------------------------------------- the receipt

// The property the whole package rests on: `mg mail send` exiting 0 is NOT a
// delivery. This constructs a sender that exits 0, prints a well-formed msg_id,
// and writes nothing — the shape of a send into a store that is read-only, full,
// or simply not the one the notifier polls.
func TestAnExitZeroSendThatWroteNothingIsNOTConfirmed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "macguffin")
	if err := os.MkdirAll(filepath.Join(root, "mail", "human", "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	liar := fakeMG(t, `{"msg_id":"1788000000000000000.1.1","to":"human"}`, 0)

	rec, err := MailSink{Root: root, Bin: liar, To: "human"}.Deliver(alarmFixture())
	if err == nil {
		t.Fatal("Deliver returned nil for a send that put no file in the maildir")
	}
	if rec.Confirmed {
		t.Fatal("the receipt is CONFIRMED for a message that does not exist; this is the whole defect — " +
			"an alarm that reports itself sent and reaches nobody")
	}
	if !strings.Contains(rec.Err, "nothing is at") {
		t.Errorf("Err = %q, want it to name the missing artefact", rec.Err)
	}
}

// The matched control: the same code against the real binary and a real store
// must confirm. Without this arm the test above passes for a MailSink that can
// never confirm anything.
func TestTheSameCodeConfirmsAgainstARealStore(t *testing.T) {
	bin := requireMG(t)
	root := newStore(t, bin)
	mustMG(t, bin, root, "mail", "register", "human")

	rec, err := MailSink{Root: root, Bin: bin, To: "human"}.Deliver(alarmFixture())
	if err != nil || !rec.Confirmed {
		t.Fatalf("Deliver against a real store: confirmed=%v err=%v (%s)", rec.Confirmed, err, rec.Err)
	}
	if _, statErr := os.Stat(rec.Ref); statErr != nil {
		t.Fatalf("the confirmed receipt points at %s, which does not exist: %v", rec.Ref, statErr)
	}
}

// mg refuses a recipient nobody registered (mg-d639), and that refusal must
// surface as NOT delivered rather than as a delivery to a phantom box.
func TestAnUnregisteredRecipientIsNotDelivered(t *testing.T) {
	bin := requireMG(t)
	root := newStore(t, bin)

	rec, err := MailSink{Root: root, Bin: bin, To: "nobody-registered-this"}.Deliver(alarmFixture())
	if err == nil || rec.Confirmed {
		t.Fatalf("a send to an unregistered mailbox reported confirmed=%v err=%v", rec.Confirmed, err)
	}
}

// A send that prints no parseable msg_id has no artefact to confirm against, and
// must not be taken on trust.
func TestASendWithNoMsgIDIsNotConfirmed(t *testing.T) {
	root := t.TempDir()
	quiet := fakeMG(t, "Delivered: -> human", 0)
	rec, err := MailSink{Root: root, Bin: quiet, To: "human"}.Deliver(alarmFixture())
	if err == nil || rec.Confirmed {
		t.Fatalf("confirmed=%v err=%v for a send that printed no msg_id", rec.Confirmed, err)
	}
	if !strings.Contains(rec.Err, "no msg_id") {
		t.Errorf("Err = %q, want it to name the missing id", rec.Err)
	}
}

func TestAFailingSendIsNotConfirmed(t *testing.T) {
	broken := fakeMG(t, "no_such_mailbox", 3)
	rec, err := MailSink{Root: t.TempDir(), Bin: broken, To: "human"}.Deliver(alarmFixture())
	if err == nil || rec.Confirmed {
		t.Fatalf("confirmed=%v err=%v for a send that exited 3", rec.Confirmed, err)
	}
}

func TestAMissingMGBinaryIsNotConfirmed(t *testing.T) {
	rec, err := MailSink{Root: t.TempDir(), Bin: filepath.Join(t.TempDir(), "not-a-binary"), To: "human"}.
		Deliver(alarmFixture())
	if err == nil || rec.Confirmed {
		t.Fatalf("confirmed=%v err=%v with no usable binary", rec.Confirmed, err)
	}
}

// ------------------------------------------------------------- the ledger

func TestTheLedgerAppendsAndReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms", "refusal-streak.log")
	a := alarmFixture()
	rec, err := LedgerSink{Path: path}.Deliver(a)
	if err != nil || !rec.Confirmed {
		t.Fatalf("confirmed=%v err=%v (%s)", rec.Confirmed, err, rec.Err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	for _, want := range []string{"2026-09-07T11:41:11Z", "mayor,pm-pogo", "FLEET STOPPED"} {
		if !strings.Contains(line, want) {
			t.Errorf("the ledger line %q is missing %q", line, want)
		}
	}
	// A second alarm appends rather than truncating: the ledger's whole value is
	// that a past alarm is still there when somebody comes looking.
	if _, err := (LedgerSink{Path: path}).Deliver(a); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(path)
	if strings.Count(string(b2), "FLEET STOPPED") != 2 {
		t.Errorf("the ledger holds %d alarms after two deliveries, want 2", strings.Count(string(b2), "FLEET STOPPED"))
	}
}

func TestTheLedgerDoesNotConfirmAPathItCannotCreate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := LedgerSink{Path: filepath.Join(file, "cannot", "exist.log")}.Deliver(alarmFixture())
	if err == nil || rec.Confirmed {
		t.Fatalf("confirmed=%v err=%v for a path under a regular file", rec.Confirmed, err)
	}
}

// --------------------------------------------------------------- Deliver

func TestDeliverTriesEVERYSinkAndDoesNotShortCircuit(t *testing.T) {
	first := &fakeSink{name: "first", confirm: true}
	second := &fakeSink{name: "second", confirm: true}
	receipts, err := Deliver([]Sink{first, second}, alarmFixture())
	if err != nil {
		t.Fatal(err)
	}
	if first.count() != 1 || second.count() != 1 {
		t.Fatalf("sinks tried %d/%d, want 1/1 — a chain that stops at the first success has one channel and a "+
			"spare that is never exercised", first.count(), second.count())
	}
	if got := Confirmed(receipts); len(got) != 2 {
		t.Errorf("Confirmed = %v, want both", got)
	}
}

func TestDeliverReturnsErrNoRecipientWhenNothingConfirms(t *testing.T) {
	_, err := Deliver([]Sink{&fakeSink{name: "a"}, &fakeSink{name: "b"}}, alarmFixture())
	if !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("err = %v, want ErrNoRecipient", err)
	}
	// The reasons travel with it: an operator must be able to see WHY both
	// channels declined without re-running anything.
	if !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "b:") {
		t.Errorf("err = %q, want both sinks' reasons in it", err)
	}
}

// One confirmed sink is a delivery even when the other failed. Two channels
// exist so that one can fail.
func TestOneConfirmedSinkIsADelivery(t *testing.T) {
	receipts, err := Deliver([]Sink{&fakeSink{name: "dead"}, &fakeSink{name: "live", confirm: true}}, alarmFixture())
	if err != nil {
		t.Fatalf("err = %v with one confirmed sink", err)
	}
	if got := Confirmed(receipts); len(got) != 1 || got[0] != "live" {
		t.Errorf("Confirmed = %v, want [live]", got)
	}
}

// An empty sink list is the routed_to=nobody state, not a no-op.
func TestNoSinksIsErrNoRecipient(t *testing.T) {
	if _, err := Deliver(nil, alarmFixture()); !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("err = %v with no sinks configured, want ErrNoRecipient", err)
	}
}

// ------------------------------------------------------- what a person reads

// The subject is the part that travels: a notification banner truncates, a mail
// list shows one line. Everything in it must be absolute.
func TestTheSubjectIsAbsoluteAndNamesTheScope(t *testing.T) {
	a := alarmFixture()
	subj := a.Subject()
	for _, want := range []string{"FLEET STOPPED", "3 consecutive failing turns", "entitlement", "mayor", "+1 more"} {
		if !strings.Contains(subj, want) {
			t.Errorf("Subject() = %q, want %q in it", subj, want)
		}
	}
	for _, forbidden := range []string{" ago", "currently", "just now"} {
		if strings.Contains(subj, forbidden) {
			t.Errorf("Subject() = %q contains the relative term %q, which is a lie the moment it is stored", subj, forbidden)
		}
	}
	body := a.Body()
	for _, want := range []string{"NOTHING WILL RESTART THESE", "presence is green", "pogo check-refusals"} {
		if !strings.Contains(body, want) {
			t.Errorf("Body() is missing %q; the reader is a woken human who must know what to do", want)
		}
	}
}

// ------------------------------------------------------------------ helpers

// fakeMG writes a stub binary that prints `out` and exits `code`. It is how the
// "send succeeded, nothing arrived" branch is CONSTRUCTED rather than asserted.
func fakeMG(t *testing.T, out string, code int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mg")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' " + shellQuote(out) + "\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh to build the stub sender: %v", err)
	}
	return p
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
