package promptedit

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
// POGO_HOME is the one that matters here, and it matters twice.
//
// This package's whole subject is the tree under ~/.pogo/agents. Every test
// passes an explicit root, so nothing here READS the live corpus by accident —
// but agent.PromptDir() resolves from POGO_HOME, and one line of a future test
// calling it with no argument would silently point the sweep at the operator's
// real prompts. The verdicts would then depend on which prompts happened to be
// edited that hour, which is precisely the kind of result this detector family
// exists to distrust.
//
// The second reason is sharper. This package MAILS. The seam is injected in
// every test, and a substitution missed in one of them would shell out to the
// real `mg` and tell a live crew agent its prompt has been hand-edited — a
// manufactured fleet alarm from a `go test` run, and one that would be believed
// because it arrives in the same mailbox from the same sender as the real
// thing.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("promptedit")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestPromptDirIsSandboxed is the positive control for the isolation above.
// Without it the envelope is an unverified claim: dropping the TestMain would
// leave every other test in this package green while a default-rooted sweep
// went back to reading the operator's live ~/.pogo/agents.
func TestPromptDirIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	got := agent.PromptDir()
	if got == "" {
		t.Fatal("agent.PromptDir() = \"\" under the sandbox; the envelope is not pinning POGO_HOME")
	}
	if !sandbox.Contains(got) {
		t.Errorf("agent.PromptDir() = %s, want a path under the sandbox root %s; a default-rooted "+
			"sweep would read the live prompt corpus", got, sandbox.Root)
	}

	// The sandboxed tree is EMPTY, and a sweep of it must say so rather than
	// erroring: a machine with no installed corpus has no installed file to
	// have been edited. This also proves the enumeration tolerates every
	// shipped directory being absent at once.
	shipped, err := LoadShipped(shippedFixtureFS())
	if err != nil {
		t.Fatalf("LoadShipped: %v", err)
	}
	rep, err := Scan(got, shipped, "mayor")
	if err != nil {
		t.Fatalf("a sweep of an empty prompt tree must not fail: %v", err)
	}
	if rep.Total() != 0 || len(rep.Findings) != 0 {
		t.Errorf("a sweep of the empty sandbox found %d file(s) and %d finding(s) — it is reading "+
			"something outside the envelope:\n%s", rep.Total(), len(rep.Findings), rep.Render())
	}
}
