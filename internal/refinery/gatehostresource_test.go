package refinery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// incidentFullDisk is the head of the gate output the refinery actually
// recorded for mr-d9u5n9atjv1ohvj2fbv0 on 2026-08-12, copied verbatim from
// ~/.pogo/refinery-state.json. It is a fixture rather than a hand-written
// approximation because the whole complaint in mg-b41f is about what a real
// report said, and a synthesised one can be made to say whatever the test wants.
const incidentFullDisk = `(omitting gate ./test.sh: ./build.sh runs it, and running it twice per merge tests nothing new)
=== Running: ./build.sh ===
Starting build
fmt.sh (go fmt ./...)
Step 1: Formatting...
test.sh (its own per-step profile is nested inside this row)
Step 2: Testing...
Making test directories
Testing Go packages
?   	github.com/drellem2/pogo/cmd/lsp	[no test files]
building pogo CLI: exit status 1
# github.com/drellem2/pogo/cmd/pogo
/Users/daniel/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.darwin-arm64/pkg/tool/darwin_arm64/link: mapping output file failed: no space left on device
FAIL	github.com/drellem2/pogo/cmd/pogo	1.419s
github.com/drellem2/pogo/internal/agent.test: open /var/folders/4n/T/go-build2860400185/b457/importcfg: no space left on device
github.com/drellem2/pogo/internal/agent: open /var/folders/4n/T/go-build2860400185/b455/vet.cfg: no space left on device
# github.com/drellem2/pogo/internal/client [github.com/drellem2/pogo/internal/client.test]
compile: writing output: write $WORK/b469/_pkg_.a: no space left on device
--- FAIL: TestStallWatchGate_BootDirections (0.01s)
--- FAIL: TestUpgradeBoot_AutoStartsCoordinatorAsMayor (0.00s)
FAIL	github.com/drellem2/pogo/internal/agent	0.030s
FAIL	github.com/drellem2/pogo/internal/client [build failed]
FAILED: exit status 1
`

// TestFullDiskIsAHostConditionNotADefect is mg-b41f's headline. The recorded
// disposition was class=defect with "the build gate ran on this tree and
// returned a verdict — re-running establishes the same fact", and then the
// identical branch merged clean once 7.3G was freed. The sentence was false.
func TestFullDiskIsAHostConditionNotADefect(t *testing.T) {
	hre := newHostResourceError("./build.sh", incidentFullDisk, t.TempDir(), errors.New("exit status 1"))
	if hre == nil {
		t.Fatal("the incident's own gate output was not recognised as a host condition")
	}
	// "build" is the stage that made this a defect: the verdict-stage table is
	// consulted on the stage alone, and the stage is right.
	d := classifyFailure("build", "", hre)
	if d.Class != ClassHost {
		t.Fatalf("class = %s, want %s — a full boot volume is not a fact about the branch", d.Class, ClassHost)
	}
	if d.Retryable {
		t.Error("retryable = true — a full disk is not restored by waiting, so an automatic retry burns a gate slot")
	}
	if !strings.Contains(d.Reason, "DIFFERENT fact") {
		t.Errorf("the reason must deny the verdict rule it is an exception to, got: %q", d.Reason)
	}
	if countsAgainstAuthor(ClassHost) {
		t.Error("a full disk fails every gate on the box — counting it accumulates a verdict about whoever was queued")
	}
}

// TestGateVerdictStaysADefect is the control, and it guards the half of mg-b41f
// that says WHAT MUST NOT CHANGE. A genuinely red gate is still a defect and is
// still not retried; the new class is an addition, not a loosening.
func TestGateVerdictStaysADefect(t *testing.T) {
	red := "--- FAIL: TestThing (0.01s)\n    thing_test.go:9: got 1, want 2\nFAIL\tgithub.com/drellem2/pogo/internal/agent\t0.030s\n"
	if hre := newHostResourceError("./build.sh", red, t.TempDir(), errors.New("exit status 1")); hre != nil {
		t.Fatalf("a red test was read as a host condition: %v", hre)
	}
	for _, stage := range []string{"build", "test"} {
		d := classifyFailure(stage, red, errors.New("quality gate: ./build.sh failed: exit status 1"))
		if d.Class != ClassDefect {
			t.Errorf("stage %s: class = %s, want %s", stage, d.Class, ClassDefect)
		}
		if d.Retryable {
			t.Errorf("stage %s: a red gate became retryable — mg-b41f must not loosen the no-retry rule", stage)
		}
	}
}

// TestFullDiskSummaryNamesTheDiskNotTheTests is the SECOND cost mg-b41f names,
// and the one it calls larger: the summary line accused the branch by name.
func TestFullDiskSummaryNamesTheDiskNotTheTests(t *testing.T) {
	got := summarizeGateFailure(incidentFullDisk)
	if !strings.Contains(got, "HOST") || !strings.Contains(got, "no space left on device") {
		t.Fatalf("summary = %q — it must name the disk", got)
	}
	// The recorded error named these two by name, which is what sent the reader
	// to read the tests instead of the host.
	for _, accusation := range []string{"TestStallWatchGate_BootDirections", "TestUpgradeBoot_AutoStartsCoordinatorAsMayor", "internal/agent"} {
		if strings.Contains(got, accusation) {
			t.Errorf("summary still names %q: %q", accusation, got)
		}
	}
	// And the error the gate runner actually returns carries the same denial in
	// its FIRST line, which is the part that travels into mail subjects and
	// `pogo refinery show`.
	hre := newHostResourceError("./build.sh", incidentFullDisk, t.TempDir(), errors.New("exit status 1"))
	first := strings.SplitN(hre.Error(), "\n", 2)[0]
	if !strings.Contains(first, "NOT ON THIS BRANCH") {
		t.Errorf("first line does not deny the verdict: %q", first)
	}
	if !strings.Contains(hre.Error(), "exit status 1") {
		t.Error("the gate's own error was dropped rather than nested")
	}
}

// TestHostResourceIsDecidedBeforeTheOutputIsCapped is this fix checked for the
// defect it repairs — a report that points away from the cause.
//
// The copy of the gate output persisted on the merge request is capped to 8 KiB
// with the middle elided. An incident whose ENOSPC lines all fell in that middle
// would read back as an ordinary build failure, so classifying from the stored
// record would reintroduce exactly the misattribution this file exists to
// remove. The class must be decided upstream of the cap.
func TestHostResourceIsDecidedBeforeTheOutputIsCapped(t *testing.T) {
	// A run whose only ENOSPC evidence sits in the middle: filler, the wording,
	// then more filler.
	filler := strings.Repeat("ok  \tgithub.com/drellem2/pogo/internal/filler\t0.01s\n", 200)
	full := filler + "compile: writing output: write $WORK/b1/_pkg_.a: no space left on device\n" + filler

	if _, _, _, ok := outputReportsHostResourceExhaustion(full); !ok {
		t.Fatal("the full output does not report the condition — fixture is wrong")
	}
	capped := capGateOutputTo(full, 2048)
	if !gateOutputWasCapped(capped) {
		t.Fatal("fixture did not exceed the cap")
	}
	if _, _, _, ok := outputReportsHostResourceExhaustion(capped); ok {
		t.Skip("this fixture's evidence survived the cap; the ordering requirement is unchanged")
	}
	// The point of the test: the capped record cannot answer, so the answer has
	// to have been taken before capping. runQualityGates is where that happens.
	if hre := newHostResourceError("./build.sh", full, t.TempDir(), errors.New("exit status 1")); hre == nil {
		t.Fatal("classification from the FULL output failed — the pre-cap reading is the only one that works")
	}
}

// TestDiskClauseSaysWhenTheReadingDisagrees pins the honesty of the second
// instrument. The class comes from the text; the measurement is reported beside
// it and must say so when the two do not line up, rather than being quietly
// dropped or quietly treated as confirmation.
func TestDiskClauseSaysWhenTheReadingDisagrees(t *testing.T) {
	base := hostResourceError{Gate: "./build.sh", Resource: "disk space", Signal: "no space left on device", Occurrences: 3}

	agree := base
	agree.Disk = diskSpace{Path: "/vol", Free: 255 << 20, Total: 460 << 30, Measured: true}
	if !strings.Contains(agree.diskClause(), "AGREES") {
		t.Errorf("a 255 MiB reading must be reported as agreeing: %q", agree.diskClause())
	}

	disagree := base
	disagree.Disk = diskSpace{Path: "/vol", Free: 300 << 30, Total: 460 << 30, Measured: true}
	clause := disagree.diskClause()
	if !strings.Contains(clause, "does NOT agree") {
		t.Errorf("a 300 GiB reading must be reported as disagreeing: %q", clause)
	}
	if !strings.Contains(clause, "test's own output") {
		t.Error("the disagreement must offer the reading that the wording came from a test, not from the kernel")
	}

	// The disagreement must reach the FIRST LINE, which is the part that travels
	// into a mail subject and a `pogo refinery show` glance. A headline that
	// asserts a host condition while the report's own second instrument doubts
	// it points the reader away from the cause — this fix committing, in the
	// opposite direction, the defect it exists to remove.
	if head := strings.SplitN(disagree.Error(), "\n", 2)[0]; !strings.Contains(head, "DOES NOT CORROBORATE") {
		t.Errorf("the disagreement did not reach the headline: %q", head)
	}
	if head := strings.SplitN(agree.Error(), "\n", 2)[0]; strings.Contains(head, "DOES NOT CORROBORATE") {
		t.Errorf("a corroborated reading hedged its own headline: %q", head)
	}

	// An unmeasured host acquires neither an excuse nor an accusation — the same
	// rule an unsampled contention reading follows.
	unmeasured := base
	unmeasured.Disk = diskSpace{Path: "/vol"}
	if unmeasured.diskClause() != "" {
		t.Errorf("an unmeasured host rendered a reading: %q", unmeasured.diskClause())
	}
	if strings.Contains(unmeasured.Error(), "Disk:") {
		t.Error("the report claims a disk reading it never took")
	}
}

// TestHostResourceBeatsIndeterminate: a gate that cannot write can also hang
// until it is killed, so both classifications can be live at once. "The host ran
// out of disk" names something; "the run was cut short" does not. Neither blames
// the branch, so preferring the specific one costs the author nothing.
func TestHostResourceBeatsIndeterminate(t *testing.T) {
	timeout := &gateTimeoutError{Gate: "./build.sh", Timeout: time.Hour, Elapsed: time.Hour}
	hre := newHostResourceError("./build.sh", incidentFullDisk, t.TempDir(), timeout)
	if hre == nil {
		t.Fatal("no host-resource error built")
	}
	var reached *gateTimeoutError
	if !errors.As(error(hre), &reached) {
		t.Error("the gate's own timeout error is no longer reachable through errors.As — Unwrap is broken")
	}
	if d := classifyFailure("test", "", hre); d.Class != ClassHost {
		t.Errorf("class = %s, want %s", d.Class, ClassHost)
	}
}

// TestHostResourceSignalsAreOnlyMeasuredOnes is the ratchet against the failure
// mode that would make this change worse than the defect it fixes.
//
// The signal table reads ARBITRARY gate output. Every pattern in it that has
// never actually occurred is a fresh chance to take a real defect away from its
// author and hand it to the host. mg-b41f says so explicitly — "only after
// checking each actually occurs here — do not add a speculative list" — and the
// counts behind these exclusions are recorded in gatehostresource.go.
func TestHostResourceSignalsAreOnlyMeasuredOnes(t *testing.T) {
	for _, unmeasured := range []string{
		"exit status 137",        // 0 hits across the retained corpus
		"out of memory",          // 0 hits
		"cannot allocate memory", // 0 hits
		"disk quota exceeded",    // 0 hits
		// Occurs on this host (7 hits, events.log 2026-05-20) but only in
		// pogod's own scheduler (mg-d205), never in gate output.
		"too many open files",
	} {
		for _, sig := range hostResourceSignals {
			if strings.Contains(sig.pattern, unmeasured) {
				t.Errorf("signal %q was added without a measurement behind it; record the count in "+
					"gatehostresource.go, or take it out", sig.pattern)
			}
		}
	}
	if len(hostResourceSignals) == 0 {
		t.Fatal("the table is empty — the measured ENOSPC signal was removed")
	}
}

func TestOutputReportsHostResourceExhaustionCountsAndSamples(t *testing.T) {
	sig, sample, n, ok := outputReportsHostResourceExhaustion(incidentFullDisk)
	if !ok {
		t.Fatal("not detected")
	}
	if sig.resource != "disk space" {
		t.Errorf("resource = %q", sig.resource)
	}
	if n != 4 {
		t.Errorf("occurrences = %d, want 4 — the fixture carries four", n)
	}
	// The sample is the first line VERBATIM, so a reader sees what the kernel
	// answered rather than a normalised restatement of it.
	if !strings.Contains(sample, "mapping output file failed") {
		t.Errorf("sample = %q, want the first occurrence verbatim", sample)
	}
	// Case-insensitive: shell tools capitalise the same errno.
	if _, _, _, ok := outputReportsHostResourceExhaustion("cp: /x: No space left on device"); !ok {
		t.Error("capitalised wording was missed")
	}
	if _, _, _, ok := outputReportsHostResourceExhaustion("ok\tgithub.com/x/y\t0.1s\n"); ok {
		t.Error("a clean run was read as a host condition")
	}
}

func TestMeasureDiskSpaceReadsTheRealFilesystem(t *testing.T) {
	got := measureDiskSpace(t.TempDir())
	if !got.Measured {
		t.Fatal("statfs failed on a directory that exists")
	}
	if got.Total == 0 || got.Free > got.Total {
		t.Errorf("implausible reading: %d free of %d", got.Free, got.Total)
	}
	// A path that does not exist is NO measurement, never zero-free — zero free
	// would be the strongest possible corroboration invented out of an error.
	missing := measureDiskSpace(t.TempDir() + "/does/not/exist")
	if missing.Measured {
		t.Error("a failed statfs was reported as a measurement")
	}
}

// TestARealGateThatRunsOutOfDiskReachesTheClassifierAsHost is the positive
// control on the whole path, and it is the one that would have caught mg-b41f.
//
// Everything above tests a function in isolation; this runs a REAL gate through
// runQualityGates — the same call attemptMerge makes — and requires that what
// comes out the other end is a host condition rather than a verdict on the
// branch. The gate prints the wording the toolchain printed during the incident
// and exits non-zero, which is exactly the shape a full disk produces.
func TestARealGateThatRunsOutOfDiskReachesTheClassifierAsHost(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	// A script rather than an inline command, so the gate's NAME is "./gate.sh"
	// the way a real one is "./build.sh" — the headline quotes the command, and
	// an inline one would carry the fixture's own test names into it.
	script := "#!/bin/sh\n" +
		"echo 'compile: writing output: write $WORK/b1/_pkg_.a: no space left on device'\n" +
		"echo '--- FAIL: TestSomething (0.01s)'\n" +
		"printf 'FAIL\\tgithub.com/drellem2/pogo/internal/agent\\t0.03s\\n'\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(wtDir, "gate.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"./gate.sh\"]\n")

	mr := &MergeRequest{ID: "mr-enospc", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, gates, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("the gate must fail")
	}
	var hre *hostResourceError
	if !errors.As(err, &hre) {
		t.Fatalf("expected a hostResourceError from the real gate path, got %T: %v", err, err)
	}
	// gateStage is what attemptMerge passes to the classifier, so classify with
	// the stage the real caller would have produced — that stage is precisely
	// what used to force ClassDefect.
	if d := classifyFailure(gateStage(gates), "", err); d.Class != ClassHost {
		t.Errorf("end to end, a gate that ran out of disk classified as %s, want %s", d.Class, ClassHost)
	}
	// And the message a polecat receives does not name the test that "failed".
	msg := err.Error()
	if strings.Contains(strings.SplitN(msg, "\n", 2)[0], "TestSomething") {
		t.Errorf("the headline still accuses a test:\n%s", msg)
	}
	if !strings.Contains(msg, "NOT ON THIS BRANCH") {
		t.Errorf("the real failure message does not deny the verdict reading:\n%s", msg)
	}
	// The disk under a t.TempDir() is a real one with room on it, so this run
	// exercises the disagreement branch — the report must say so rather than
	// silently present a healthy reading as confirmation.
	if hre.Disk.Measured && hre.Disk.Free >= hostResourceLowWater && !strings.Contains(msg, "does NOT agree") {
		t.Errorf("a healthy disk reading was not reported as disagreeing:\n%s", msg)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{512, "512 B"},
		{255 << 20, "255.0 MiB"},
		{1 << 30, "1.0 GiB"},
		{460 << 30, "460.0 GiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
