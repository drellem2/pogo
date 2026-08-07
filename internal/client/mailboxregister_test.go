package client

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRegisterMGMailbox_BuildsRegisterNotCreate pins the SPELLING, which is the
// whole substance of mg-7dc1's fix.
//
// `mg mail register <name>` and `mg mail send <name> --create` both make a
// mailbox exist, and choosing between them is not a style question. --create
// belongs to a send: it says "deliver this whether or not the recipient was ever
// meant to exist", which is exactly the phantom-mailbox behaviour mg-d639
// removed — a typo'd recipient going back to reporting Delivered. Provisioning
// with `register` keeps a later no_such_mailbox meaningful.
//
// So this asserts the argv, not just that some mg call happened. A refactor that
// quietly swapped in --create would keep every other test in this repo green
// while restoring the defect under a new name, and this line is what stops it.
func TestRegisterMGMailbox_BuildsRegisterNotCreate(t *testing.T) {
	var got []string
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = old })

	if err := RegisterMGMailbox("p7dc1"); err != nil {
		t.Fatalf("RegisterMGMailbox: %v", err)
	}
	want := []string{"mg", "mail", "register", "p7dc1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("command = %v, want %v", got, want)
	}
	for _, arg := range got {
		if arg == "--create" {
			t.Fatal("provisioning used `--create`; that is the send-side flag, and putting it on the path that runs for every polecat re-creates phantom mailboxes under a new name (mg-d639)")
		}
		if arg == "send" {
			t.Fatal("provisioning issued `mg mail send`; registration must not deliver a message")
		}
	}
}

// TestRegisterMGMailbox_ReportsFailure verifies a failed registration surfaces
// as an error carrying mg's own output. The caller (spawn) is non-fatal, so this
// error string is the only description of the cause that reaches the event log —
// swallowing it would leave "mailbox_register_failed" with nothing to act on.
func TestRegisterMGMailbox_ReportsFailure(t *testing.T) {
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 'mg: permission denied' >&2; exit 1")
	}
	t.Cleanup(func() { execCommand = old })

	err := RegisterMGMailbox("p7dc1")
	if err == nil {
		t.Fatal("RegisterMGMailbox reported success on a failing mg")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error %q drops mg's own output; the cause is unrecoverable without it", err)
	}
}
