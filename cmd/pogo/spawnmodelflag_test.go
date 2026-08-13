package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSpawnPolecatHelp_ModelFlag pins the CLI surface of mg-e7f5: `--model`
// exists, is distinguishable from `--provider`, and its help text carries the
// one thing a reader must know before using it.
//
// The help text is where the warning has to live, not only in the source
// comments. The reasoning for pogo pinning no model survived for five weeks only
// as comments inside ~/.config/pogo/config.toml — a file nobody implementing this
// feature had any reason to open — which is how the constraint nearly got lost.
// A flag whose help says "pin a model here" without saying what happens when that
// model runs out is the same failure with a shorter fuse.
func TestSpawnPolecatHelp_ModelFlag(t *testing.T) {
	out, err := exec.Command(pogoBin, "agent", "spawn-polecat", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v\n%s", err, out)
	}
	help := string(out)

	if !strings.Contains(help, "--model") {
		t.Fatalf("spawn-polecat must offer --model, got:\n%s", help)
	}
	// --provider must still be there and must still say it picks the HARNESS.
	// The two flags are different axes, and a reader who conflates them will
	// reach for --provider when they wanted --model (which is the state of the
	// world this ticket was filed about).
	if !strings.Contains(help, "--provider") || !strings.Contains(help, "Harness provider") {
		t.Errorf("--provider must remain, described as the harness selector, got:\n%s", help)
	}

	// The hazard, in the help text: omitting the flag pins nothing, and a pinned
	// model that runs out WEDGES rather than degrading.
	for _, want := range []string{
		"Omit to pass NO model argument",
		"pins no default",
		"WEDGES",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("--model help must say %q — it is the only place a person choosing "+
				"to pin a model will be standing; got:\n%s", want, help)
		}
	}
}

// There is deliberately no CLI-level test that actually DISPATCHES with a bad
// --model. `pogo agent spawn-polecat` talks to whatever pogod the CLI resolves,
// and a sandbox does not isolate that — so such a test would issue a real spawn
// against the live fleet the moment the guard it exists to check regressed. The
// refusal is covered hermetically instead, at the handler, in
// internal/agent/modeldispatch_test.go (TestDispatchRefusesUnusableModel), which
// asserts the 400, the reason, and that nothing is left behind.
