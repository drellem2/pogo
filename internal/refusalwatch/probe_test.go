package refusalwatch

import (
	"errors"
	"strings"
	"testing"
)

// The probe is the runtime answer to "would anybody hear this?", and it is run
// here so that a future correct change which breaks the alarm channel turns the
// next pogo merge red BY NAME. internal/verdictwatch's probes died unobserved
// for two days behind a census that stayed green; that is the failure this test
// exists to make impossible for this channel.
func TestTheFleetDownProbePasses(t *testing.T) {
	requireMG(t)
	res := Probe()
	if res.InstrumentFailure() {
		t.Fatalf("the probe reported itself blind, which is neither a pass nor a failure:\n%s", res.Render())
	}
	if !res.Passed() {
		t.Fatalf("the fleet-down probe did not pass:\n%s", res.Render())
	}
	// The arms must include BOTH directions. A probe of only positive arms
	// cannot tell a working channel from a confirmation step that confirms
	// nothing.
	var positive, control int
	for _, a := range res.Arms {
		if strings.HasPrefix(a.Name, "CONTROL:") {
			control++
		} else {
			positive++
		}
	}
	if positive == 0 || control == 0 {
		t.Fatalf("the probe ran %d positive and %d control arm(s); a matched pair is the whole point",
			positive, control)
	}
}

// The blind branch is the probe's most important state, because it is the one it
// spends when it has silently stopped proving anything. Drive it on purpose.
func TestTheProbeReportsBlindRatherThanPassingWithNoMG(t *testing.T) {
	orig := mgLookup
	t.Cleanup(func() { mgLookup = orig })
	mgLookup = func() (string, error) { return "", errors.New("constructed: no mg on this box") }

	res := Probe()
	if !res.InstrumentFailure() {
		t.Fatal("the probe did not report blind with no mg binary")
	}
	if res.Passed() {
		t.Fatal("a blind probe reported Passed() — this is the state verdictwatch's probes spent two days in")
	}
	out := res.Render()
	if !strings.Contains(out, "INSTRUMENT FAILURE") {
		t.Errorf("Render() = %q, want it to say INSTRUMENT FAILURE", out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("Render() = %q, want it to say the answer is unknown rather than fine", out)
	}
}

// A failing arm must render as a failure that names what missed, because the
// operator running this on a deployed box has nothing else to go on.
func TestRenderNamesAFailingArm(t *testing.T) {
	res := ProbeResult{
		Store: "/tmp/x", MG: "/usr/bin/mg",
		Arms: []ProbeArm{
			{Name: "FLEET DOWN: the alarm is delivered", Want: "CONFIRMED delivered", Got: "NOT delivered", OK: false,
				Detail: "nothing is at /tmp/x/mail/human/new/1"},
		},
	}
	if res.Passed() {
		t.Fatal("Passed() with a failing arm")
	}
	out := res.Render()
	for _, want := range []string{"FAIL", "FLEET DOWN", "nothing is at", "routed to nobody"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() = %q, want %q in it", out, want)
		}
	}
}

// An empty arm list is not a pass. A probe that constructed nothing has measured
// nothing, and the shape of its output must not be the shape of success.
func TestAnEmptyProbeIsNotAPass(t *testing.T) {
	if (ProbeResult{}).Passed() {
		t.Fatal("a ProbeResult with no arms and no blind reason reported Passed()")
	}
}
