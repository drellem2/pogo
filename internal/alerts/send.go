package alerts

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
)

// MailFunc is the shape every mail transport in pogo has: the one watchers
// take as an injected Options field, and the one Transport implements.
type MailFunc = func(to, from, subject, body string) error

// Mail is THE send seam. Every alert mail goes through here, named.
//
// via is the transport the caller already holds — Transport in production, a
// recorder in a unit test, or a caller's own delivery (refusalwatch confirms
// its mail landed in the maildir). It is called exactly as the caller would
// have called it: there is no nil default, because a nil transport in a unit
// test silently becoming the real `mg` would mail a live inbox.
//
// Slice 1 changes no behaviour: the message, recipient and sender are passed to
// via unchanged and via's error is returned unchanged. Muting and routing
// (later slices of drellem2/pogo#262) are applied here, which is why a send
// that skips this function is a build failure (TestNoSendBypassesTheRegistry).
func Mail(name Name, via MailFunc, to, from, subject, body string) error {
	check(name)
	return via(to, from, subject, body)
}

// MailOutput is Mail for the callers that log `mg`'s own output on failure:
// it runs `mg mail send <to> --from <from> --subject <subject> --body <body>`
// and returns the command's combined output and its error, exactly as those
// callers' own exec.Command did before the seam existed.
func MailOutput(name Name, to, from, subject, body string) ([]byte, error) {
	check(name)
	cmd := execCommand("mg", "mail", "send", to,
		"--from", from, "--subject", subject, "--body", body)
	return cmd.CombinedOutput()
}

// Transport sends one message with `mg mail send`. It is the production
// transport (formerly client.SendMGMail) and is what pogod injects wherever a
// watcher takes a MailFunc. Calling it directly is a bypass; pass it to Mail.
func Transport(to, from, subject, body string) error {
	cmd := execCommand("mg", "mail", "send", to, "--from="+from, "--subject="+subject, "--body="+body)
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg mail send failed: %s (%w)", string(out), err)
	}
	return nil
}

// execCommand is a variable for testability.
var execCommand = exec.Command

var unknownLogged sync.Map

// check reports a name the registry does not declare. It never blocks a send:
// the guard test is what keeps undeclared names out, and dropping mail over a
// bookkeeping error would be a behaviour change.
func check(name Name) {
	if _, ok := byName[name]; ok {
		return
	}
	if _, seen := unknownLogged.LoadOrStore(name, true); !seen {
		log.Printf("alerts: %q is not a declared alert; sending it anyway", name)
	}
}
