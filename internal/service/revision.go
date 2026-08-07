package service

// revision.go carries the revision check onto the service package's two restart
// paths (mg-ed4a).
//
// THE GAP THIS CLOSES. Four paths restart or verify pogod, and only
// scripts/pogo-self-deploy's verify_running() asked whether the daemon that
// came back is the one it was supposed to be:
//
//	pogo-self-deploy verify_running()   polls /version against $MAIN
//	verifyLaunchdRunning()              launchctl list + /health — never /version
//	restartLaunchd()                    nothing
//	scripts/launchd/pogo-recovery.sh    the kickstart's exit code
//
// `launchctl list` says a job is registered. /health says something is
// listening. Neither says the RIGHT thing is listening, and a kickstart
// re-execs whatever is on disk — so "the daemon came back healthy" is exactly
// what a restart that reinstated a stale binary looks like. On 2026-08-07 this
// box had been in that state for eight days: alive, healthy, 92 commits behind,
// on a 2026-07-30 build.
//
// WHAT "EXPECTED" IS HERE. The revision stamped into the pogod binary that
// launchd is configured to exec — read from the plist, not from PATH, because
// the plist is what launchd actually runs. That expectation needs no repo, no
// network and no config, so unlike a main-HEAD comparison it is armed on every
// box the service package runs on.
//
// REPORT-ONLY, DELIBERATELY, AND THIS IS A DECISION NOT AN OVERSIGHT. Neither
// path's exit code changes: `pogo service install` still succeeds against a
// stale daemon and `pogo service restart` still succeeds when the restart
// re-launches the same binary. mg-ed4a asked for exactly that — installs
// currently succeed in that state and something may depend on it, so the
// instruction was to report first and decide about failing later. The observed
// property is now printed on every run; a caller that wants the check as a GATE
// has `pogo service verify-revision`, whose exit code does distinguish the three
// verdicts. Turning either path into a hard failure is a separate, explicit
// change.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/revcheck"
)

// restartVerifyTimeout bounds the post-restart poll. Shorter than the deploy
// script's 60s because these paths have already waited for /health before
// asking, so the daemon is up and the only question left is which binary it is
// — and a restart path that blocks for a further minute on a report-only check
// is a restart path people stop using.
const restartVerifyTimeout = 20 * time.Second

// plistProgramArgument matches the first <string> inside ProgramArguments.
// Deliberately narrow: this is a read of a file this package wrote from a
// template it owns, not a general plist parser.
var plistProgramArgument = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>\s*<string>([^<]*)</string>`)

// launchdProgramPath reports which pogod binary launchd is configured to exec.
//
// Read from the installed plist rather than from exec.LookPath, because those
// two can disagree — a plist written when pogod lived in one place keeps
// pointing there after a second copy lands earlier on PATH — and when they do,
// the PATH answer is the wrong expectation: launchd runs the plist's. Falls
// back to PATH when no plist is installed (the systemd side, and any pre-install
// call), which is the best available answer rather than a guess.
func launchdProgramPath() string {
	data, err := os.ReadFile(launchdPlistPath())
	if err == nil {
		if m := plistProgramArgument.FindSubmatch(data); m != nil {
			if p := strings.TrimSpace(string(m[1])); p != "" {
				return p
			}
		}
	}
	p, err := findPogod()
	if err != nil {
		return ""
	}
	return p
}

// expectedDaemonRevision is the revision a restart is SUPPOSED to put into the
// running process: the vcs stamp of the binary launchd execs. Returns a
// revcheck sentinel, never an error, so an unreadable binary lands in UNKNOWN
// instead of being dropped by a caller that ignored an error.
func expectedDaemonRevision() string {
	return revcheck.BinaryRevision(launchdProgramPath())
}

// verifyDaemonRevision runs the shared check against the live daemon and
// returns the three-valued result. It never fails a caller — the verdict is the
// return value, and what to do with it is the caller's decision.
func verifyDaemonRevision(timeout time.Duration) revcheck.Result {
	return revcheck.Wait(revcheck.Options{
		BaseURL:  config.Load().ServerURL(),
		Expected: expectedDaemonRevision(),
		Timeout:  timeout,
	})
}

// revisionReport renders the verdict for an operator reading a terminal or a
// log. AGREES gets one quiet line; anything else gets the block, because the
// non-AGREES cases are the ones that used to produce no output at all and the
// reader has to be told what the check could and could not see.
//
// `what` names the path that ran the check ("install", "restart") so a reader
// finding this in ~/Library/Logs knows which one spoke.
func revisionReport(what string, res revcheck.Result) string {
	if res.OK() {
		return fmt.Sprintf("  ✓ %s: pogod is running revision %s (matches %s)",
			what, revcheck.Short(res.Running), launchdProgramPath())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  ! %s: %s\n", what, res)
	switch res.Verdict {
	case revcheck.Differs:
		fmt.Fprintf(&b, "    The daemon came back healthy but is NOT running the binary launchd was told to exec.\n")
		fmt.Fprintf(&b, "    running   %s   (live, self-reported via GET /version)\n", res.Running)
		fmt.Fprintf(&b, "    expected  %s   (vcs stamp of %s)\n", res.Expected, launchdProgramPath())
		fmt.Fprintf(&b, "    A restart re-execs whatever is on disk, so this usually means the binary on disk\n")
		fmt.Fprintf(&b, "    is not the one you think it is, or a second pogod holds the port. Confirm with:\n")
		fmt.Fprintf(&b, "      curl -s %s/version | jq -r .revision\n", config.Load().ServerURL())
	case revcheck.Unknown:
		fmt.Fprintf(&b, "    This check did NOT measure whether the right binary is running. That is not the\n")
		fmt.Fprintf(&b, "    same as it being fine — it is the absence of a reading, reported as such.\n")
	}
	fmt.Fprintf(&b, "    This is REPORT-ONLY: %s did not fail because of it. For the check as a gate,\n", what)
	fmt.Fprintf(&b, "    run `pogo service verify-revision`, whose exit code distinguishes the verdicts.")
	return b.String()
}

// reportDaemonRevision runs the check and prints it. The whole point of the
// ticket is that no restart path may finish silently, so this is called for its
// output; the result is returned for callers that want to act on it.
func reportDaemonRevision(what string, timeout time.Duration) revcheck.Result {
	res := verifyDaemonRevision(timeout)
	fmt.Println(revisionReport(what, res))
	return res
}

// VerifyRevision is the check as a standalone question, exported for `pogo
// service verify-revision` and for the recovery agent that shells out to it.
//
// expected empty means "whatever launchd is configured to exec" — the same
// expectation the install and restart paths use. A caller with a different
// notion of what SHOULD be running (a deploy, which expects main's HEAD) passes
// it explicitly, which is the seam that lets one implementation serve every
// path without deciding for any of them.
//
// Unlike the two restart paths, this one's CALLER is free to fail on the
// verdict — that is why it exists separately.
func VerifyRevision(expected string, timeout time.Duration) revcheck.Result {
	if expected == "" {
		expected = expectedDaemonRevision()
	}
	return revcheck.Wait(revcheck.Options{
		BaseURL:  config.Load().ServerURL(),
		Expected: expected,
		Timeout:  timeout,
	})
}

// ExpectedDaemonRevision exports the expectation itself, so a caller can print
// which binary it was comparing against without re-deriving the plist read.
func ExpectedDaemonRevision() string { return expectedDaemonRevision() }

// LaunchdProgramPath exports which pogod binary launchd is configured to exec.
func LaunchdProgramPath() string { return launchdProgramPath() }
