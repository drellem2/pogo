package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func TestEarlyExitNoticeNamesTheOrphanedItem(t *testing.T) {
	e := agent.EarlyExit{ExitCode: 1, After: 2800 * time.Millisecond, Window: time.Minute, LastOutput: "❯ No, exit\n"}
	subject, body := earlyExitNotice("f394", "mg-f394", e)

	for _, want := range []string{"f394", "2.8s", "mg-f394"} {
		if !strings.Contains(subject, want) {
			t.Errorf("subject %q does not name %q", subject, want)
		}
	}
	for _, want := range []string{"exit status 1", "still reads claimed", "Treat the dispatch as failed", "#177", "No, exit"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not say %q:\n%s", want, body)
		}
	}

	// A harness that DID reach its composer and then died is not the trust
	// dialog, and the notice must not point there.
	e.ComposerSeen = true
	if _, body := earlyExitNotice("f394", "mg-f394", e); strings.Contains(body, "#177") {
		t.Errorf("notice blames the trust dialog for a harness that reached its composer:\n%s", body)
	}
}

func TestNotifyEarlyExitMailsTheCoordinator(t *testing.T) {
	var to, from string
	mail := func(t, f, _, _ string) error { to, from = t, f; return nil }
	notifyEarlyExit("f394", "mg-f394", "mayor", agent.EarlyExit{ExitCode: 1, After: time.Second, Window: time.Minute}, mail)
	if to != "mayor" || from != "pogod" {
		t.Errorf("mailed to=%q from=%q, want mayor from pogod", to, from)
	}

	// A failing transport must not panic the exit handler it runs in.
	notifyEarlyExit("f394", "mg-f394", "mayor", agent.EarlyExit{}, func(_, _, _, _ string) error {
		return errors.New("mg down")
	})
	// No coordinator configured: nothing to mail, and no call.
	called := false
	notifyEarlyExit("f394", "mg-f394", "", agent.EarlyExit{}, func(_, _, _, _ string) error { called = true; return nil })
	if called {
		t.Error("mailed with no coordinator configured")
	}
}
