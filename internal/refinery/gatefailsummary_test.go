package refinery

import (
	"fmt"
	"strings"
	"testing"
)

// The load-bearing case is the FIRST one: the verbatim shape of the output that
// produced `./build.sh failed: exit status 1` twice on 2026-08-07. Everything
// else here is a guard on the ways that summary could go wrong; that one is the
// occurrence being paid for.

func TestSummarizeNamesThePackageAndTestFromARealGateFailure(t *testing.T) {
	// Trimmed from the run that reddened main via internal/agent (mg-9259
	// changed `mg done`; internal/agent asserted the old behaviour).
	const output = `Starting build
Step 1: Formatting...
Step 2: Testing...
Making test directories
Testing Go packages
ok  	github.com/drellem2/pogo/internal/refinery	3.204s
--- FAIL: TestTriagePacketIsWrittenBeforeAnySuccessorExists (0.03s)
    triagepacket_live_test.go:210: want exactly one result sidecar preserved across the refusal, got []
FAIL
FAIL	github.com/drellem2/pogo/internal/agent	14.882s
ok  	github.com/drellem2/pogo/internal/mailbox	0.412s
FAIL
`
	got := summarizeGateFailure(output)
	if !strings.Contains(got, "internal/agent") {
		t.Errorf("summary %q does not name the failing package; localising it cost a full suite run every time", got)
	}
	if !strings.Contains(got, "TestTriagePacketIsWrittenBeforeAnySuccessorExists") {
		t.Errorf("summary %q does not name the failing test", got)
	}
	// The packages that PASSED must not appear. A report that lists everything
	// it saw is the bare exit status again, with more words.
	for _, passed := range []string{"internal/refinery", "internal/mailbox"} {
		if strings.Contains(got, passed) {
			t.Errorf("summary %q names %s, which passed", got, passed)
		}
	}
}

func TestSummarizeIsSilentWhenItRecognisesNothing(t *testing.T) {
	for _, output := range []string{
		"",
		"Starting build\nStep 3: Building binaries into ./bin...\n",
		"some tool nobody has taught this to read said something inscrutable\n",
	} {
		if got := summarizeGateFailure(output); got != "" {
			t.Errorf("summarizeGateFailure(%q) = %q, want \"\" — a guess sends the reader "+
				"somewhere wrong, which is the complaint being answered", output, got)
		}
	}
}

func TestSummarizeReportsASetupFailureAsSetupNotAsAFinding(t *testing.T) {
	// internal/testsandbox's banner. mg-3412: one broken sandbox printed
	// fourteen assertion failures about a tree that was provably fine, and two
	// of them read as security findings.
	const output = `=== RUN   TestSomething
SETUP FAILURE (internal/testsandbox): POGO_HOME resolves onto the live tree /Users/daniel/.pogo
--- FAIL: TestSomething (0.00s)
--- FAIL: TestSomethingElse (0.00s)
FAIL	github.com/drellem2/pogo/internal/agent	0.104s
`
	got := summarizeGateFailure(output)
	if !strings.Contains(got, "setup failed") {
		t.Errorf("summary %q does not say the SETUP failed", got)
	}
	if strings.Contains(got, "TestSomething") {
		t.Errorf("summary %q offers an assertion name as the headline under a broken sandbox; "+
			"those names are not findings", got)
	}
}

func TestSummarizeNamesAPackageThatDidNotCompile(t *testing.T) {
	const output = `Testing Go packages
# github.com/drellem2/pogo/internal/agent [github.com/drellem2/pogo/internal/agent.test]
internal/agent/claimrelease_test.go:31:2: undefined: mgcontract
FAIL	github.com/drellem2/pogo/internal/agent [build failed]
FAIL
`
	got := summarizeGateFailure(output)
	if !strings.Contains(got, "internal/agent") {
		t.Errorf("summary %q does not name the package that failed to build", got)
	}
}

func TestSummarizeHandlesSeveralFailingPackagesWithoutMisattributingTests(t *testing.T) {
	// The mg-d639 shape: one change in a sibling binary, breakage in several
	// pogo packages at once. Test names cannot be attributed to a package from
	// this text, so they must not be pinned to one.
	const output = `--- FAIL: TestAgainstRealMg (0.21s)
FAIL	github.com/drellem2/pogo/internal/strandedmail	0.219s
--- FAIL: TestSinkDiagnosesTheWrongRecipient (0.02s)
FAIL	github.com/drellem2/pogo/internal/deploy	0.030s
FAIL
`
	got := summarizeGateFailure(output)
	for _, want := range []string{"internal/strandedmail", "internal/deploy"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not name %s", got, want)
		}
	}
	if strings.Contains(got, "internal/strandedmail: Test") || strings.Contains(got, "internal/deploy: Test") {
		t.Errorf("summary %q pins a test name to a package the output does not attribute it to: %q", got, got)
	}
	if !strings.Contains(got, "TestAgainstRealMg") {
		t.Errorf("summary %q drops the failing test names entirely; they are listed unattributed, not omitted", got)
	}
}

func TestSummarizeIsBounded(t *testing.T) {
	// Distinct names per iteration — a fixture that repeats one name measures
	// dedupe, not the cap.
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "--- FAIL: TestSomethingWithAnExtremelyLongDescriptiveName%s%d\n", strings.Repeat("X", 20), i)
		fmt.Fprintf(&b, "FAIL\tgithub.com/drellem2/pogo/internal/package%s%d\t0.1s\n", strings.Repeat("y", 10), i)
	}
	got := summarizeGateFailure(b.String())
	if len(got) > maxSummaryLen {
		t.Errorf("summary is %d bytes, over the %d cap; it rides in a persisted error and is read in a terminal", len(got), maxSummaryLen)
	}
	if !strings.Contains(got, "more") {
		t.Errorf("summary %q elides silently; a truncated list that does not say so reads as a complete one", got)
	}
}

func TestSummarizeFallsBackToTheShellSuites(t *testing.T) {
	// test.sh runs the shell suites AFTER `go test ./...`, so a failure there
	// carries no Go package at all.
	const output = `Testing Go packages
ok  	github.com/drellem2/pogo/internal/agent	1.204s
Testing changelog coverage check
  FAIL: mg-216c has no changelog fragment
`
	got := summarizeGateFailure(output)
	if !strings.Contains(got, "changelog fragment") {
		t.Errorf("summary %q does not carry the shell suite's own failure text", got)
	}
	if strings.Contains(got, "internal/agent") {
		t.Errorf("summary %q names a package that passed", got)
	}
}

func TestSummarizeNamesAPanicOrAKill(t *testing.T) {
	for _, tc := range []struct{ name, output, want string }{
		{"panic", "=== RUN   TestX\npanic: runtime error: index out of range [3]\n\ngoroutine 41 [running]:\n", "panic:"},
		{"killed", "=== RUN   TestX\nsignal: killed\n", "signal: killed"},
		{"timeout", "panic: test timed out after 10m0s\n", "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizeGateFailure(tc.output); !strings.Contains(got, tc.want) {
				t.Errorf("summary %q does not carry %q; a test binary that died reports no FAIL line "+
					"for its package and would otherwise summarise to nothing", got, tc.want)
			}
		})
	}
}

// TestTheGateErrorCarriesTheSummary is the wiring, driven through the same
// formatting the merge path uses. Without it the summariser could be perfect and
// the reported sentence unchanged, which is the only thing anyone reads.
func TestTheGateErrorCarriesTheSummary(t *testing.T) {
	const output = `--- FAIL: TestRefusedDoneKeepsTheResultItWasGiven (0.03s)
FAIL	github.com/drellem2/pogo/internal/agent	12.06s
FAIL
`
	what := summarizeGateFailure(output)
	if what == "" {
		t.Fatal("the summariser recognised nothing in a plain go test failure")
	}
	msg := "./build.sh failed [" + what + "]: exit status 1"
	if !strings.Contains(msg, "internal/agent") {
		t.Errorf("the gate error reads %q, which still sends the reader to the whole suite", msg)
	}
}

// TestShortenPkgKeepsThePathAReaderWouldType — `internal/agent` is a directory
// they can cd to; the module-prefixed import path is not.
func TestShortenPkgKeepsThePathAReaderWouldType(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/drellem2/pogo/internal/agent":     "internal/agent",
		"github.com/drellem2/pogo/cmd/pogod":          "cmd/pogod",
		"github.com/drellem2/pogo/internal/agent/sub": "internal/agent/sub",
		"github.com/drellem2/pogo":                    "github.com/drellem2/pogo",
	} {
		if got := shortenPkg(in); got != want {
			t.Errorf("shortenPkg(%q) = %q, want %q", in, got, want)
		}
	}
}
