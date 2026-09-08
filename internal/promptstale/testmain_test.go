package promptstale

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established before a
// single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root, read back out of the process,
// and refused if any of them resolves onto the developer's live tree.
//
// It matters here for the two reasons it matters in internal/promptedit, and one
// more that is specific to this package.
//
// This package MAILS. The seam is injected in every test, and a substitution
// missed in one of them would shell out to the real `mg` and tell a live crew
// agent its prompt is superseded — a manufactured fleet alarm from a `go test`
// run, arriving in the same mailbox from the same sender as the real thing.
//
// agent.PromptDir() resolves from POGO_HOME, so one line of a future test
// calling it with no argument would point the sweep at the operator's real
// prompts. On this machine that is not hypothetical: three of the nine shipped
// prompts are stale right now, so an unsandboxed test would be green or red
// depending on whether last night's deploy happened to succeed.
//
// And the REFERENCE side resolves from the environment too:
// staleness.DeployReferenceRepo reads POGO_DEPLOY_SRC and then
// <POGO_HOME>/deploy-src. Every test here passes an explicit repo, but an
// unpinned POGO_HOME would let a defaulting caller compare against the live
// deploy checkout.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("promptstale")
	sandbox = sb

	// testsandbox does not know about this one, and the doc above would be a
	// claim rather than a fact without it: POGO_DEPLOY_SRC takes priority over
	// <POGO_HOME>/deploy-src inside staleness.DeployReferenceRepo, so a
	// developer with it exported would have a pinned POGO_HOME and an
	// unsandboxed reference.
	os.Unsetenv("POGO_DEPLOY_SRC")

	code := m.Run()

	down()
	os.Exit(code)
}

// TestEnvelopeIsSandboxed is the positive control for the isolation above.
// Without it the envelope is an unverified claim: dropping the TestMain would
// leave every other test in this package green while a default-rooted sweep
// went back to reading the operator's live tree.
func TestEnvelopeIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	got := agent.PromptDir()
	if got == "" {
		t.Fatal("agent.PromptDir() = \"\" under the sandbox; the envelope is not pinning POGO_HOME")
	}
	if !sandbox.Contains(got) {
		t.Errorf("agent.PromptDir() = %s, want a path under the sandbox root %s; a default-rooted "+
			"sweep would read the live prompt corpus", got, sandbox.Root)
	}
}
