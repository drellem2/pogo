package refusalwatch

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// requireNotifier resolves the DEPLOYED poll-mail.sh.
//
// It cannot go through os.UserHomeDir(): this package's TestMain sandboxes HOME
// on purpose, so the runtime resolution NotifierScript() performs finds nothing
// here. The passwd entry is the real home regardless of $HOME, and the probe
// only ever READS the script — everything it writes lives under its own temp
// directory.
func requireNotifier(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("no passwd entry to resolve the deployed notifier from: %v", err)
	}
	for _, cand := range []string{
		filepath.Join(u.HomeDir, ".pogo", "pogo-reminders", "bin", "poll-mail.sh"),
		filepath.Join(u.HomeDir, "dev", "pogo-reminders", "bin", "poll-mail.sh"),
	} {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	t.Skipf("no deployed poll-mail.sh under %s — the last hop lives in another repo and this box has no copy of it; "+
		"this test proves nothing without one", u.HomeDir)
	return ""
}

// TestTheLastHopProbePasses is the arrow mg-6f3d did not measure: the alarm's
// bytes are in the maildir, and then WHAT. It drives the real notifier and
// requires it to raise the alarm on its own terms.
func TestTheLastHopProbePasses(t *testing.T) {
	requireMG(t)
	t.Setenv(NotifierScriptEnv, requireNotifier(t))

	res := ProbeLastHop()
	if res.InstrumentFailure() {
		t.Fatalf("the last-hop probe reported itself blind, which is neither a pass nor a failure:\n%s", res.Render())
	}
	if !res.Passed() {
		t.Fatalf("the last-hop probe did not pass:\n%s", res.Render())
	}
	var positive, control int
	for _, a := range res.Arms {
		if strings.HasPrefix(a.Name, "CONTROL:") {
			control++
		} else {
			positive++
		}
	}
	if positive == 0 || control == 0 {
		t.Fatalf("the probe ran %d positive and %d control arm(s); an arm that can only pass measures nothing",
			positive, control)
	}
}

// The blind branch is the state this probe spends on any box without the
// notifier, and it must be loud rather than green — the notifier lives in a
// different repo with no compile-time link to this one.
func TestTheLastHopProbeReportsBlindWithNoNotifier(t *testing.T) {
	orig := notifierLookup
	t.Cleanup(func() { notifierLookup = orig })
	notifierLookup = func() (string, error) { return "", errors.New("constructed: no notifier on this box") }

	res := ProbeLastHop()
	if !res.InstrumentFailure() {
		t.Fatal("the last-hop probe did not report blind with no notifier script")
	}
	if res.Passed() {
		t.Fatal("a blind last-hop probe reported Passed()")
	}
	if out := res.Render(); !strings.Contains(out, "INSTRUMENT FAILURE") {
		t.Errorf("Render() = %q, want it to say INSTRUMENT FAILURE", out)
	}
}

// NotifierScript must refuse a path that names nothing rather than silently
// falling back to a default — an override that quietly resolves elsewhere is how
// a probe ends up measuring a script nobody deployed.
func TestNotifierScriptRefusesAnOverrideThatNamesNothing(t *testing.T) {
	t.Setenv(NotifierScriptEnv, filepath.Join(t.TempDir(), "not-here.sh"))
	if got, err := NotifierScript(); err == nil {
		t.Fatalf("NotifierScript() = %q, want an error naming the bad override", got)
	} else if !strings.Contains(err.Error(), NotifierScriptEnv) {
		t.Errorf("error = %v, want it to name %s", err, NotifierScriptEnv)
	}
}
