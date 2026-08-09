package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The exit code IS the interface. A schedule, a doctor row or a CI step reads
// this command's integer and nothing else, and the four values mean four
// different things a caller must respond to differently:
//
//	0  no dropped verdicts in the scanned population
//	1  at least one dropped verdict — chase it
//	2  usage — the question was asked wrong, nothing was measured
//	3  INSTRUMENT FAILURE — the question was fine, the detector could not look
//
// Collapsing 3 into 0 is the failure this whole lineage keeps rediscovering, and
// collapsing it into 1 sends somebody hunting a finding that was never made. So
// these are exercised end to end against a built binary rather than asserted
// about the constants.

var (
	buildOnce sync.Once
	pogoPath  string
	buildErr  error
)

func checkVerdictsBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "checkverdicts-bin-")
		if err != nil {
			buildErr = err
			return
		}
		pogoPath = filepath.Join(dir, "pogo")
		out, err := exec.Command("go", "build", "-o", pogoPath, ".").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatalf("build pogo: %v", buildErr)
	}
	return pogoPath
}

// verdictStore builds a minimal macguffin store: one landed item, one filer,
// and a mailbox. Whether the verdict was mailed is the caller's choice, which is
// what makes the 0 and 1 arms a matched pair rather than two separate fixtures.
func verdictStore(t *testing.T, verdictMailed bool) string {
	t.Helper()
	root := t.TempDir()
	mustMkdir := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	done := mustMkdir("work", "done")
	box := mustMkdir("mail", "filer-a", "cur")

	item := "---\nid: mg-a1b2\ncreator: filer-a\ntype: task\n---\n\n# a landed item\n"
	if err := os.WriteFile(filepath.Join(done, "mg-a1b2.md"), []byte(item), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(done, "mg-a1b2.result.json"),
		[]byte(`{"branch":"polecat-wa1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	event := `{"actor":"daniel","item_id":"mg-a1b2","ts":"2026-08-01T10:00:00Z","type":"work.done"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "events.jsonl"), []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	if verdictMailed {
		msg := "From: wa1\nSubject: mg-a1b2 VERDICT\nDate: 2026-08-01T09:00:00Z\n\nbody\n"
		if err := os.WriteFile(filepath.Join(box, "1.msg"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runCheckVerdicts(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(checkVerdictsBinary(t), append([]string{"check-verdicts"}, args...)...)
	// MG_ROOT is pinned to a path that does not exist so a --root the test
	// forgot to pass cannot silently reach the developer's live store and turn
	// this suite's result into a property of one machine's ~/.macguffin.
	cmd.Env = append(os.Environ(), "MG_ROOT="+filepath.Join(t.TempDir(), "no-such-store"))
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run check-verdicts %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// TestExitCodesSeparateTheFourAnswers.
func TestExitCodesSeparateTheFourAnswers(t *testing.T) {
	clean := verdictStore(t, true)
	dirty := verdictStore(t, false)

	t.Run("0 when the verdict was delivered", func(t *testing.T) {
		out, code := runCheckVerdicts(t, "--root", clean, "--filer", "filer-a")
		if code != 0 {
			t.Errorf("exit %d on a store with no dropped verdicts, want 0:\n%s", code, out)
		}
		if !strings.Contains(out, "DELIVERED            : 1") {
			t.Errorf("the clean run does not report the delivery:\n%s", out)
		}
	})

	t.Run("1 when a verdict was dropped", func(t *testing.T) {
		out, code := runCheckVerdicts(t, "--root", dirty, "--filer", "filer-a")
		if code != 1 {
			t.Errorf("exit %d on a store with one dropped verdict, want 1:\n%s", code, out)
		}
		if !strings.Contains(out, "mg-a1b2") {
			t.Errorf("the finding does not name the item:\n%s", out)
		}
		if !strings.Contains(out, "REPORTED, NOT REPAIRED") {
			t.Errorf("the report does not say it took no action; this family's whole contract is "+
				"that it never acts:\n%s", out)
		}
	})

	t.Run("2 on a usage error", func(t *testing.T) {
		// A --since that is not a timestamp would be compared as a string, sort
		// above every stamp in the store, exclude everything, and exit 0. A
		// silent wrong answer is the one outcome this must not produce.
		out, code := runCheckVerdicts(t, "--root", dirty, "--since", "yesterday")
		if code != exitVerdictUsage {
			t.Errorf("exit %d for --since yesterday, want %d:\n%s", code, exitVerdictUsage, out)
		}
		if _, code := runCheckVerdicts(t, "--root", dirty, "--no-such-flag"); code != exitVerdictUsage {
			t.Errorf("exit %d for an unknown flag, want %d", code, exitVerdictUsage)
		}
		if _, code := runCheckVerdicts(t, "--root", dirty, "a-positional-arg"); code != exitVerdictUsage {
			t.Errorf("exit %d for a positional argument, want %d", code, exitVerdictUsage)
		}
	})

	t.Run("3 when the run measured nothing", func(t *testing.T) {
		// The landing half removed: every item reads as never landed, so a
		// careless detector would report "0 dropped" and exit 0 over a store it
		// could not read.
		blind := verdictStore(t, false)
		if err := os.Remove(filepath.Join(blind, "events.jsonl")); err != nil {
			t.Fatal(err)
		}
		out, code := runCheckVerdicts(t, "--root", blind, "--filer", "filer-a")
		if code != exitInstrumentFailure {
			t.Errorf("exit %d with the landing half gone, want %d — a blind run must not exit clean:\n%s",
				code, exitInstrumentFailure, out)
		}
		if !strings.HasPrefix(out, "INSTRUMENT FAILURE") {
			t.Errorf("the blind run's output does not LEAD with the banner:\n%s", out)
		}

		// And a store that does not exist at all.
		if _, code := runCheckVerdicts(t, "--root", filepath.Join(t.TempDir(), "gone")); code != exitInstrumentFailure {
			t.Errorf("exit %d for an unresolvable store, want %d", code, exitInstrumentFailure)
		}
	})
}

// TestAScopedEmptyPopulationIsNotBlind. `--filer somebody-who-never-filed` is an
// answer to the question asked and exits 0 — but it must SAY it judged nothing,
// or it renders identically to a clean fleet.
func TestAScopedEmptyPopulationIsNotBlind(t *testing.T) {
	root := verdictStore(t, false)
	out, code := runCheckVerdicts(t, "--root", root, "--filer", "nobody-who-ever-filed")
	if code != 0 {
		t.Errorf("exit %d for a scoped scan that matched nothing, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "JUDGED NOTHING") {
		t.Errorf("a scoped scan that matched nothing does not say so:\n%s", out)
	}
}

// TestTheProbeIsReachableFromTheCLI. The Go test in internal/verdictwatch is
// what makes the merge gate exercise the constructive probe; this flag is what
// makes it answerable on a deployed box with no Go toolchain, by the operator
// looking at a green census and wondering whether to believe it.
func TestTheProbeIsReachableFromTheCLI(t *testing.T) {
	if _, err := exec.LookPath("mg"); err != nil {
		t.Skip("mg is not on PATH; the constructive probe drives the real macguffin CLI")
	}
	out, code := runCheckVerdicts(t, "--probe")
	if code != 0 {
		t.Fatalf("`check-verdicts --probe` exited %d:\n%s", code, out)
	}
	for _, want := range []string{"known-bad", "known-good", "DROPPED", "DELIVERED"} {
		if !strings.Contains(out, want) {
			t.Errorf("the probe's report does not mention %q:\n%s", want, out)
		}
	}
}

// TestTheHelpStatesTheContractItsCallersReadFor. The family's contract lives in
// its help text — "never acts", "never files anything", "never fixes" — and a
// reader deciding whether to schedule this needs the exit codes and the
// report-only boundary without opening the source.
func TestTheHelpStatesTheContractItsCallersReadFor(t *testing.T) {
	out, code := runCheckVerdicts(t, "--help")
	if code != 0 {
		t.Fatalf("--help exited %d:\n%s", code, out)
	}
	for _, want := range []string{
		"REPORTS ONLY", // the boundary, stated
		"never files the missing verdict",
		"INSTRUMENT FAILURE",        // the third answer
		"ORDERED BY WHEN IT LANDED", // the ordering is the recovery affordance
		"Exit status: 0 no dropped verdicts, 1 at least one, 2 usage error, 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the help text does not carry %q:\n%s", want, out)
		}
	}

	// The ONE-LINE summary is where every sibling's contract lives — "never
	// acts", "never files anything", "never closes anything", "never fixes" —
	// and it is what a reader scanning `pogo --help` for something to schedule
	// actually reads. Asserted against THAT listing rather than against the long
	// help, because they are different strings and only one of them travels.
	cmd := exec.Command(checkVerdictsBinary(t), "--help")
	cmd.Env = append(os.Environ(), "MG_ROOT="+filepath.Join(t.TempDir(), "no-such-store"))
	listing, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pogo --help: %v\n%s", err, listing)
	}
	var line string
	for _, l := range strings.Split(string(listing), "\n") {
		if strings.Contains(l, "check-verdicts") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("check-verdicts is not listed in `pogo --help`:\n%s", listing)
	}
	if !strings.Contains(line, "never files anything") {
		t.Errorf("the one-line summary does not carry the family's report-only contract: %q", line)
	}
}
