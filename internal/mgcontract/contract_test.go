package mgcontract

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheDeclaredMgContractStillHolds is the ONE place a behavioural change in
// the `mg` binary is allowed to turn pogo's gate red.
//
// Before this existed, such a change arrived as an assertion failure in an
// unrelated package — internal/strandedmail for mg-d639, internal/agent for
// mg-9259 — and cost, measured on 2026-08-07:
//
//   - a full-suite run per occurrence to localise, because the gate reported a
//     bare `./build.sh failed: exit status 1`;
//   - five killed branches, each needing an individual exoneration, because the
//     failure names whichever branch is unlucky enough to be in the gate;
//   - three agents attributing the second outage to the first mg change,
//     because the SHAPE was identical.
//
// What it fixes is attribution, not the red. pogo genuinely holds a stale
// expectation when a clause breaks and somebody has to rule on it; this makes
// the ruling a glance instead of a hunt.
//
// The failure message is deliberately long. It is the only artifact a reader
// gets, and everything in it — the mg item that created the behaviour pogo
// expects, the pogo tests resting on it, and the instruction not to flip the
// probe — is what the 2026-08-07 occurrences had to be reconstructed by hand.
func TestTheDeclaredMgContractStillHolds(t *testing.T) {
	if !Installed() {
		t.Skip("mg is not on PATH; the contract is with a binary this machine does not have")
	}
	version := mgVersion()
	for _, res := range VerifyAll() {
		if res.Holds() {
			continue
		}
		since := res.Clause.Since
		if since == "" {
			since = "(no mg work item recorded; it predates the ones pogo can name)"
		}
		t.Errorf(`
mg contract clause %q NO LONGER HOLDS.

  observed: %v

  installed mg:  %s
  pogo expects:  the behaviour established by %s
  pogo needs it: %s

  the pogo tests that rest on it (they will SKIP, not fail — their premise is gone):
      %s

  WHAT TO DO. Decide which side is wrong, then act on that side. Either mg
  regressed and this is a report to file against it, or mg moved deliberately
  and pogo's expectation is the stale one — in which case fix the dependent
  tests and re-declare this clause with its new Since.

  WHAT NOT TO DO. Do not edit the probe to agree with what mg now does. A clause
  rewritten to match the dependency has stopped testing anything, and every pogo
  test behind it is then resting on a behaviour nobody checked.

  See internal/mgcontract and docs/design/mg-contract.md.`,
			res.Clause.Name, res.Err, version, since, res.Clause.Why,
			strings.Join(res.Clause.Dependents, "\n      "))
	}
}

// TestEveryClauseIsDeclaredOnce guards the registry itself. A duplicate name
// would make Lookup return the first of two probes and silently retire the
// other, which is the same class of quiet loss the package exists to end.
func TestEveryClauseIsDeclaredOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Clauses() {
		if c.Name == "" {
			t.Error("a clause is declared with no name")
			continue
		}
		if seen[c.Name] {
			t.Errorf("clause %q is declared twice; Lookup would return one probe and silently drop the other", c.Name)
		}
		seen[c.Name] = true
		if c.probe == nil {
			t.Errorf("clause %q declares no probe, so it asserts nothing", c.Name)
		}
		if strings.TrimSpace(c.Why) == "" {
			t.Errorf("clause %q says nothing about what breaks in pogo without it; that sentence IS the failure report", c.Name)
		}
		if len(c.Dependents) == 0 {
			t.Errorf("clause %q names no dependents, so a break here says nothing about what else to re-read", c.Name)
		}
	}
}

// TestRequireSkipsRatherThanFailsWhenAClauseIsBroken records the routing
// decision, because it is the part that is easy to reverse by accident and hard
// to notice: a Require that FAILED would reproduce the exact defect this package
// was filed for — N red assertions in N packages for one change in a sibling
// repo.
//
// Driven through a real sub-test rather than a fake TB, so what is observed is
// the outcome `go test` reports and not the calls Require happened to make.
func TestRequireSkipsRatherThanFailsWhenAClauseIsBroken(t *testing.T) {
	const broken = "contract-probe/deliberately-undeclared"
	var skipped, failed bool
	t.Run("dependent", func(t *testing.T) {
		defer func() {
			skipped, failed = t.Skipped(), t.Failed()
		}()
		Require(t, broken)
		t.Error("Require returned on a clause that cannot hold; the calling test carried on past a dead premise")
	})
	if failed {
		t.Error("Require FAILED the dependent test. That is the pre-mg-216c behaviour: " +
			"one change in mg becomes one red assertion per dependent package, in packages " +
			"whose own code is fine. The red belongs to TestTheDeclaredMgContractStillHolds.")
	}
	if !skipped {
		t.Error("Require neither skipped nor failed the dependent test, so it ran on past a premise that does not hold")
	}
}

// TestVerifyProbesEachClauseOnce pins the caching, which is what makes Require
// affordable to call from every dependent test: the suite pays for a clause once
// per process, not once per call site.
func TestVerifyProbesEachClauseOnce(t *testing.T) {
	if !Installed() {
		t.Skip("mg is not on PATH")
	}
	const name = "contract-probe/counted"
	var runs int
	mu.Lock()
	delete(results, name)
	mu.Unlock()
	clauses = append(clauses, Clause{
		Name:       name,
		Why:        "a fixture for the caching check",
		Dependents: []string{"internal/mgcontract (this test)"},
		probe:      func(*store) error { runs++; return nil },
	})
	t.Cleanup(func() {
		clauses = clauses[:len(clauses)-1]
		mu.Lock()
		delete(results, name)
		mu.Unlock()
	})

	for i := 0; i < 3; i++ {
		if res := Verify(name); !res.Holds() {
			t.Fatalf("Verify(%q) = %v, want it to hold", name, res.Err)
		}
	}
	if runs != 1 {
		t.Errorf("the probe ran %d times across three Verify calls, want 1", runs)
	}
}

// TestAnUndeclaredClauseIsAnError closes the other direction: a Require naming a
// clause nobody declared must not read as "the contract holds".
func TestAnUndeclaredClauseIsAnError(t *testing.T) {
	res := Verify("contract-probe/nothing-declares-this")
	if res.Holds() {
		t.Fatal("Verify reported an undeclared clause as holding")
	}
	if !strings.Contains(res.Err.Error(), "no clause named") {
		t.Errorf("the error for an undeclared clause is %v; it should say the clause is not declared", res.Err)
	}
}

// mgVersion is best-effort: a version string sharpens the failure report but
// nothing rests on it, so an mg that cannot print one is not itself a finding.
func mgVersion() string {
	out, err := exec.Command("mg", "version").CombinedOutput()
	if err != nil {
		return "(mg version: unavailable)"
	}
	return strings.TrimSpace(string(out))
}
