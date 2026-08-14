package refinery

import (
	"fmt"
	"regexp"
	"strings"
)

// A failed quality gate used to be reported as `./build.sh failed: exit status 1`.
// That sentence names neither the package nor the cause, and it is the sentence
// that travels: it is the MR's RawError, it is what `pogo refinery show` prints,
// and it is what a polecat is told when its branch is in the gate.
//
// Measured on 2026-08-07, when one stale expectation about a sibling binary took
// down every pogo merge twice in ninety minutes: each occurrence cost a full
// suite run to localise, five branches were blamed one at a time for a failure
// none of them caused, and three agents attributed the second outage to the
// first one's cause because the two reports were textually identical (mg-216c).
//
// The gate's own output was sitting there the whole time. This reads the failing
// names out of it and puts them in the error, so the first line of the report
// says WHERE. It does not diagnose and it must not try to: it reports names it
// found, and finding none it says nothing rather than guessing.

var (
	// `--- FAIL: TestName (0.03s)`, at any indent — subtests are indented.
	goTestFailRe = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	// `FAIL\tgithub.com/owner/repo/internal/agent\t0.030s` and the
	// `[build failed]` / `[setup failed]` variants.
	//
	// The separators are [ \t]+ rather than \s+ on purpose: \s matches a
	// newline, so `\s+` let a match START on the bare `FAIL` summary line and
	// SWALLOW the real result line below it — the one case this whole file
	// exists for came back empty.
	goPkgFailRe = regexp.MustCompile(`(?m)^(FAIL|ok)[ \t]+(\S+)[ \t]+(.*)$`)
	// `# github.com/owner/repo/internal/agent [github.com/...test]` — the header
	// the compiler prints above a build error, which is the only place the
	// package appears when nothing ran.
	goCompilePkgRe = regexp.MustCompile(`(?m)^# (\S+)`)
	// The shell suites in test.sh report `  FAIL: <what>`.
	shellFailRe = regexp.MustCompile(`(?m)^\s*FAIL: (.+)$`)
)

const (
	// Bounds on the summary. It rides in an error string that is persisted on
	// the MR and read in a terminal; past a handful of names it stops being a
	// glance, which is the entire value being bought.
	maxSummaryPackages = 4
	maxSummaryTests    = 3
	maxSummaryLen      = 240
)

// summarizeGateFailure names what failed inside a gate's output.
//
// It returns "" when it recognises nothing, and the caller then reports exactly
// what it reported before. A summary that guessed would be worse than none: the
// whole complaint being answered is that readers were sent to the wrong place.
func summarizeGateFailure(output string) string {
	// A broken test SANDBOX is reported first and alone. mg-3412 is what the
	// alternative costs: one setup failure printed fourteen assertion failures
	// downstream of it, about a tree that was provably fine. If the envelope did
	// not stand up, the assertion names underneath it are not findings and must
	// not be offered as the headline.
	if line, ok := reportedSetupFailureLine(output); ok {
		return truncate("test setup failed, not the branch: "+line, maxSummaryLen)
	}

	// A gate that could not reach the NETWORK is reported before anything it
	// named, and INSTEAD of it — same shape and same reason as the host-resource
	// clause below (mg-67c9). runQualityGates builds a gateNetworkError and
	// returns before reaching this function, so this is a second lock on the same
	// door; it is here because answering `[internal/agent]` for output whose only
	// failure is `dial tcp: lookup proxy.golang.org: no such host` is wrong for
	// every caller, not only that one. That sentence is the exact headline the
	// 2026-08-14 merge died under.
	if sig, marker, sample, n, ok := outputReportsGateNetworkFailure(output); ok {
		return truncate(fmt.Sprintf("the GATE could not reach the network, not the branch (%q with %q x%d): %s",
			sig, marker, n, sample), maxSummaryLen)
	}

	// A HOST that ran out of a resource is reported before anything the gate
	// named, and INSTEAD of it. mg-b41f is what the alternative costs: fifty
	// packages and two tests named by this function, on a run where the
	// toolchain could not write a single file. Those names are not findings, and
	// naming them points the reader away from the cause.
	//
	// This is a second lock on the same door — runQualityGates builds a
	// hostResourceError and returns before it reaches this function, so the
	// sentence below does not normally travel. It is here because this
	// function's contract is "name what failed inside a gate's output", and
	// answering "internal/agent, +46 more" for output that says `no space left
	// on device` twenty-five times is wrong for every caller, not only that one.
	if sig, line, n, ok := outputReportsHostResourceExhaustion(output); ok {
		return truncate(fmt.Sprintf("the HOST ran out of %s, not the branch (%q x%d): %s",
			sig.resource, sig.pattern, n, line), maxSummaryLen)
	}

	failedPkgs := failingGoPackages(output)
	tests := dedupe(matchGroup(goTestFailRe, output))

	if len(failedPkgs) > 0 {
		return truncate(strings.Join(describePackages(failedPkgs, tests), ", "), maxSummaryLen)
	}

	// A package that did not build never reaches a FAIL line — `go build` and
	// `go vet` print the package header and stop. Without this the most common
	// non-test failure in the suite would report nothing at all.
	if pkgs := dedupe(matchGroup(goCompilePkgRe, output)); len(pkgs) > 0 {
		return truncate("build failed: "+strings.Join(shortenAll(pkgs, maxSummaryPackages), ", "), maxSummaryLen)
	}

	// Go named nothing; fall back to the shell suites, which test.sh runs after
	// `go test ./...` and which report their own failures by name.
	if shell := dedupe(matchGroup(shellFailRe, output)); len(shell) > 0 {
		head := shell
		suffix := ""
		if len(head) > maxSummaryTests {
			head, suffix = head[:maxSummaryTests], fmt.Sprintf(" (+%d more)", len(shell)-maxSummaryTests)
		}
		return truncate(strings.Join(head, "; ")+suffix, maxSummaryLen)
	}

	// A test binary that panicked or was killed prints neither a FAIL line for
	// its package nor a `--- FAIL:` for the case in flight, but it does say so.
	for _, marker := range []string{"panic: ", "signal: killed", "test timed out", "fatal error: "} {
		if idx := strings.Index(output, marker); idx >= 0 {
			return truncate(strings.TrimSpace(firstLineAt(output, idx)), maxSummaryLen)
		}
	}
	return ""
}

// reportedSetupFailureLine finds the line on which the gate BANNERED a setup
// failure, ignoring the lines on which a test merely TALKS about one.
//
// The distinction is not hypothetical (mg-67c9). On 2026-08-14
// mr-d9vah0atjv1vk5gh57c0 was reported as
//
//	quality gate: ./build.sh failed [test setup failed, not the branch:
//	  PASS: a sandbox HOME that is a symlink to the developer's home:
//	  prints the SETUP FAILURE banner]
//
// and that sentence was false in every part. Nothing had failed to set up: a
// substring scan of the whole output had landed inside a PASSING line of a
// positive control that exists to assert the banner is printed
// (scripts/pogo-sandbox_test.sh prints `PASS: $1`). The run's actual failure was
// `=== net-control.sh: 14 passed, 4 failed ===`, further down the same output,
// and the reader was pointed away from it.
//
// The symmetric case is worse and is covered by the same guard: that suite's
// FAILING branch prints `printed no 'SETUP FAILURE' banner`, so a control that
// caught a REAL regression — a sandbox that failed to banner — would have been
// summarised as "test setup failed, not the branch" and excused.
//
// So a harness's own verdict line can never be the banner. `PASS:`/`FAIL:`/
// `PROVED:` at the start of a line is this repo's shell-suite verdict vocabulary
// (scripts/pogo-sandbox_test.sh, and the `  FAIL: ` form shellFailRe already
// matches); a line that opens with one is that suite stating a result, and a
// result is not a banner whatever words it quotes. Everything the genuine
// banners emit — `=== SETUP FAILURE — the sandbox could not be established`,
// `SETUP FAILURE: <detail>`, and testsandbox.FailPrefix — starts with something
// else, so nothing that should fire stops firing.
func reportedSetupFailureLine(output string) (string, bool) {
	for _, raw := range strings.Split(output, "\n") {
		if !strings.Contains(raw, "SETUP FAILURE") {
			continue
		}
		line := strings.TrimSpace(raw)
		if isHarnessVerdictLine(line) {
			continue
		}
		return line, true
	}
	return "", false
}

// harnessVerdictPrefixes are the shell suites' own result vocabulary. A line
// opening with one is a verdict, not evidence.
var harnessVerdictPrefixes = []string{"PASS:", "FAIL:", "PROVED:", "SKIP:"}

func isHarnessVerdictLine(trimmed string) bool {
	for _, p := range harnessVerdictPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// failingGoPackages returns the import paths `go test` reported FAIL for, in the
// order they appeared. The `ok` alternation in the pattern is deliberate: the
// regexp has to see the passing lines too, or a package path containing the word
// "FAIL" in a test's output could be mistaken for a result line.
func failingGoPackages(output string) []string {
	var failed []string
	for _, m := range goPkgFailRe.FindAllStringSubmatch(output, -1) {
		if m[1] == "ok" {
			continue
		}
		// `FAIL` on its own line — the summary `go test` prints for the run as a
		// whole — carries no package and must not be read as one.
		if !strings.Contains(m[2], "/") {
			continue
		}
		failed = append(failed, m[2])
	}
	return dedupe(failed)
}

// describePackages pairs each failing package with the failing test names, when
// the output offers them. `go test ./...` interleaves nothing — every `--- FAIL`
// for a package precedes that package's FAIL line — so the association is read
// off the order rather than guessed at.
func describePackages(pkgs, tests []string) []string {
	out := make([]string, 0, len(pkgs)+1)
	shown := pkgs
	var extra string
	if len(shown) > maxSummaryPackages {
		extra = fmt.Sprintf("+%d more package(s)", len(shown)-maxSummaryPackages)
		shown = shown[:maxSummaryPackages]
	}
	for _, pkg := range shown {
		if len(pkgs) == 1 && len(tests) > 0 {
			out = append(out, shortenPkg(pkg)+": "+joinTests(tests))
			continue
		}
		out = append(out, shortenPkg(pkg))
	}
	if extra != "" {
		out = append(out, extra)
	}
	// With several failing packages the test names cannot be attributed to one
	// of them from this text alone, so they are listed once, unattributed,
	// rather than pinned to a package that may not own them.
	if len(pkgs) > 1 && len(tests) > 0 {
		out = append(out, "failing tests include "+joinTests(tests))
	}
	return out
}

func joinTests(tests []string) string {
	if len(tests) <= maxSummaryTests {
		return strings.Join(tests, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(tests[:maxSummaryTests], ", "), len(tests)-maxSummaryTests)
}

// shortenPkg drops the module prefix: `github.com/drellem2/pogo/internal/agent`
// reads as `internal/agent`, which is what a reader types next.
func shortenPkg(pkg string) string {
	for _, marker := range []string{"/internal/", "/cmd/"} {
		if i := strings.Index(pkg, marker); i >= 0 {
			return pkg[i+1:]
		}
	}
	if i := strings.LastIndex(pkg, "/"); i >= 0 && strings.Count(pkg, "/") > 2 {
		return pkg[i+1:]
	}
	return pkg
}

func shortenAll(pkgs []string, max int) []string {
	if len(pkgs) > max {
		pkgs = pkgs[:max]
	}
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, shortenPkg(p))
	}
	return out
}

func matchGroup(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if v := strings.TrimSpace(m[1]); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func firstLineAt(s string, idx int) string {
	start := strings.LastIndexByte(s[:idx], '\n') + 1
	rest := s[start:]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		return rest[:end]
	}
	return rest
}
