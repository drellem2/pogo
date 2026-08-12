package refinery

import (
	"fmt"
	"strings"
	"syscall"
)

// A quality gate that fails because the HOST ran out of a resource is not a
// verdict on the branch, and until mg-b41f the refinery had no way to say so.
//
// # What happened
//
// On 2026-08-12 the boot volume reached 255 MiB free. ./build.sh failed in the
// gate and the refinery recorded (mr-d9u5n9atjv1ohvj2fbv0):
//
//	Status:  failed
//	Error:   quality gate: ./build.sh failed [cmd/pogo, cmd/pogod, internal/agent,
//	         +46 more package(s), failing tests include
//	         TestStallWatchGate_BootDirections, TestUpgradeBoot_AutoStartsCoordinatorAsMayor]
//	Attempts: 1 (failed once, no retry)
//	retried: NO — the build gate ran on this tree and returned a verdict —
//	         re-running establishes the same fact
//
// Twenty-five lines of the gate's own output said something else:
//
//	link: mapping output file failed: no space left on device
//	open .../importcfg: no space left on device
//	compile: writing output: write $WORK/b469/_pkg_.a: no space left on device
//
// `go clean -modcache` freed 7.3G, the identical branch was resubmitted, and the
// gate ran normally (mr-d9u5omatjv1ohvj2fbvg, merged). THE VERDICT WAS NOT
// REPRODUCIBLE, which is precisely what the no-retry reasoning assumed it was.
//
// # The two halves of the defect, and which one is larger
//
// The no-retry rule — "the gate ran on this tree and returned a verdict,
// re-running establishes the same fact" — is CORRECT for a deterministic gate
// failure and FALSE for an environmental one, because re-running after the
// resource is restored establishes a DIFFERENT fact. Nothing here loosens that
// rule for the class it was written for; this adds a class it could not express.
//
// The larger cost is the accusation. The summary line named fifty packages and
// two tests, so the obvious reading is "these tests broke" — and the evidence
// that it was the host sat one level down in the raw output, which is exactly
// where the reader the summary exists to serve does not go. A notice that points
// away from the cause is worse than no notice.
//
// # Why this is allowed to read the gate's output, when the network table is not
//
// failureclass.go draws a deliberate boundary: gate output is arbitrary text and
// must not be matched against the network table, because a genuine assertion
// failure that happened to print "connection refused" would then be retried
// forever. That boundary is about RETRY, and this class does not cross it — a
// host-resource failure is NOT retried (below), so the runaway it exists to
// prevent cannot occur here. What this changes is who is accused, not how many
// times the gate runs.
//
// # Why it is not retried automatically
//
// A full disk is not a DHCP lease. It does not come back on its own, so an
// automatic retry spends a whole gate run — minutes, on the single merge slot
// every other request queues behind — to re-derive the same fact, and it spends
// them while the host is still in the state that fails every gate on the box.
// The failure mail already carries the class in its SUBJECT and the triage note
// in its body (cmd/pogod/main.go), and it already goes to both the author and
// the coordinator; that is the "mark it retryable and mail the coordinator" half
// of the choice mg-b41f left open, and the mail path for it already existed.

// hostResourceSignal is one measured wording that means the HOST ran out of a
// resource while the gate ran. resource names what ran out, in the word the
// report will use.
type hostResourceSignal struct {
	pattern  string
	resource string
}

// hostResourceSignals are matched case-insensitively against the gate's own
// combined output.
//
// THE TABLE IS SHORT ON PURPOSE AND EVERY ENTRY HAS ACTUALLY OCCURRED HERE. A
// speculative table is what makes text-matching on arbitrary gate output
// dangerous: each pattern that has never been seen is a fresh chance to take a
// real defect away from its author and hand it to the host. The neighbouring
// conditions mg-b41f names were therefore counted against this fleet's own
// corpus before being left out, on 2026-08-12:
//
//	no space left on device   26 hits / 2 merge requests   <- INCLUDED
//	exit status 137            0 hits                      <- left out
//	out of memory              0 hits                      <- left out
//	cannot allocate memory     0 hits                      <- left out
//	disk quota exceeded        0 hits                      <- left out
//	input/output error         0 hits                      <- left out
//
// counted over the 100 merge requests retained in ~/.pogo/refinery-state.json,
// and separately over ~/.pogo/events.log and two generations of pogod.log.
//
// `too many open files` needs its own sentence, because it is the one that DOES
// occur on this host — 7 hits in events.log on 2026-05-20 — and it is still not
// here. Every one of them was pogod's own scheduler hitting its fd limit
// (mg-d205), not a gate. Adding it would mean matching gate output for a wording
// that has never appeared in gate output, which is the speculative entry this
// comment exists to refuse.
var hostResourceSignals = []hostResourceSignal{
	{"no space left on device", "disk space"},
}

// hostResourceLowWater is the free-space reading below which the measurement is
// reported as AGREEING with the wording that was matched.
//
// It is a REPORTING threshold and it decides nothing: no class, no retry, and no
// blame turns on it. The classification is made from the text alone (see
// diskClause), so moving this number changes one sentence in a report and
// nothing else.
//
// The value comes from two measurements rather than a feel. The incident failed
// with 255 MiB free on the volume. And one clean run of this repo's own gate —
// GOTMPDIR pointed at an empty directory, its size sampled every 2s while
// ./build.sh ran to exit 0 — peaked at 250.5 MiB of scratch. That 250.5 MiB is a
// FLOOR on what a run needs, not a requirement: it counts $WORK only, and not the
// GOCACHE writes the same run makes to a different directory on the same volume.
// A gigabyte is comfortably above the largest thing measured and comfortably
// below what a healthy boot volume on this fleet reports.
const hostResourceLowWater = 1 << 30 // 1 GiB

// diskSpace is one reading of a filesystem's free space, with Measured saying
// whether the reading happened at all.
type diskSpace struct {
	Path     string
	Free     uint64
	Total    uint64
	Measured bool
}

// measureDiskSpace reads free space on the filesystem holding path.
//
// A failed statfs is reported as NO measurement rather than as zero bytes free.
// Zero free would be the strongest possible corroboration of the wording, and
// manufacturing it out of a failed syscall would be the report inventing its own
// evidence — the same rule gateTimeoutError applies to an unsampled contention
// reading, for the same reason.
func measureDiskSpace(path string) diskSpace {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return diskSpace{Path: path}
	}
	// Bsize is uint32 on darwin and int64 on linux; both convert.
	bs := uint64(st.Bsize)
	return diskSpace{
		Path:     path,
		Free:     st.Bavail * bs,
		Total:    st.Blocks * bs,
		Measured: true,
	}
}

// hostResourceError reports a gate that failed because the host ran out of a
// resource. It carries the wording that was matched, how often it appeared, the
// first line verbatim, and an independent reading of the filesystem — so the
// classification can be audited rather than taken on trust.
type hostResourceError struct {
	Gate        string
	Resource    string
	Signal      string
	Occurrences int
	// Sample is the first line that carried the wording, verbatim.
	Sample string
	Disk   diskSpace
	// Err is the gate's own error, kept so errors.As still reaches whatever it
	// wrapped (a timeout, an exit status).
	Err error
}

func (e *hostResourceError) Unwrap() error { return e.Err }

func (e *hostResourceError) Error() string {
	b := &strings.Builder{}
	// The denial leads, for the same reason gateTimeoutError's does: the part of
	// a failure that travels is its first line, and a caveat further down does
	// not reach the reader who forwards the headline.
	//
	// Which is also why the disagreement, when there is one, is named UP HERE
	// and not only in the clause that measures it. A report whose headline
	// asserts a host condition while its own second instrument doubts it would
	// be pointing the reader away from the cause — the exact defect this file
	// exists to remove, committed in the opposite direction.
	fmt.Fprintf(b, "gate %q FAILED ON A HOST CONDITION, NOT ON THIS BRANCH%s: the host ran out of %s while it ran "+
		"(%q, %s in the gate's output). THIS IS NOT A VERDICT ON THE BRANCH — whatever packages and tests the "+
		"output names failed because the toolchain could not write, not because this change broke them.\n",
		e.Gate, e.headlineCaveat(), e.Resource, e.Signal, plural(e.Occurrences, "occurrence"))
	if e.Sample != "" {
		fmt.Fprintf(b, "First occurrence, verbatim: %s\n", e.Sample)
	}
	b.WriteString(e.diskClause())
	b.WriteString("Free the resource, then resubmit this branch UNCHANGED. It was NOT retried automatically: " +
		"a full disk is not restored by waiting, so another attempt would spend a whole gate run to re-derive " +
		"the same fact while the host is still in the state that fails every gate on it.")
	if e.Err != nil {
		fmt.Fprintf(b, "\nThe gate's own error, kept verbatim: %v", e.Err)
	}
	return b.String()
}

// headlineCaveat carries the disagreement into the first line, and is empty
// whenever the two instruments agree or only one of them spoke. It is
// deliberately short: the headline states the class, and the sentence that
// explains the disagreement is diskClause's job two lines below.
func (e *hostResourceError) headlineCaveat() string {
	if !e.Disk.Measured || e.Disk.Free < hostResourceLowWater {
		return ""
	}
	return " (BUT THE DISK READING BELOW DOES NOT CORROBORATE THIS — read it before acting)"
}

// diskClause reports the free space measured on the gate's own filesystem, and
// says out loud when that reading does NOT agree with the wording above.
//
// It is a second instrument, not a second opinion. The class is decided by the
// TEXT, because the text is the kernel's answer to a write the toolchain
// actually attempted; the reading is carried beside it — the same shape as the
// Host: line a contended timeout prints — so a reader can see the two agree, or
// see that they do not. Letting the reading veto the text would introduce a
// false negative in the one case this exists for: a gate's $WORK is deleted when
// it exits, and the run measured for hostResourceLowWater freed 250.5 MiB doing
// so, which on a volume that was 255 MiB from full is the whole margin.
func (e *hostResourceError) diskClause() string {
	if !e.Disk.Measured {
		// No measurement is not a measurement of plenty. Say nothing rather than
		// render an empty reading that a reader would take as evidence.
		return ""
	}
	if e.Disk.Free < hostResourceLowWater {
		return fmt.Sprintf("Disk: %s free of %s on %s, measured when the gate failed — which AGREES with the wording above.\n",
			humanBytes(e.Disk.Free), humanBytes(e.Disk.Total), e.Disk.Path)
	}
	return fmt.Sprintf("Disk: %s free of %s on %s, measured when the gate failed — which does NOT agree with the wording above. "+
		"Either the space was reclaimed between the failure and this measurement (a gate's own scratch directory is deleted "+
		"when it exits, and one run of this repo's gate was measured holding 250.5 MiB of it), or the wording came from a "+
		"test's own output rather than from the kernel. Read the gate output before acting on either reading.\n",
		humanBytes(e.Disk.Free), humanBytes(e.Disk.Total), e.Disk.Path)
}

// outputReportsHostResourceExhaustion is the single reading of the signal table,
// shared by newHostResourceError and by summarizeGateFailure so those two cannot
// end up holding different definitions of a full disk and contradicting each
// other inside one report — the same reason outputReportsConflict exists.
//
// It must be given the FULL gate output. The copy persisted on the merge request
// is capped to 8 KiB with its middle elided (gateoutputcap.go), and an incident
// whose ENOSPC lines fell entirely in that middle would read back as a clean
// build failure. Classification therefore happens in runQualityGates, before
// anything is capped; TestHostResourceIsDecidedBeforeTheOutputIsCapped pins it.
func outputReportsHostResourceExhaustion(output string) (sig hostResourceSignal, sample string, count int, ok bool) {
	hay := strings.ToLower(output)
	for _, s := range hostResourceSignals {
		n := strings.Count(hay, s.pattern)
		if n == 0 {
			continue
		}
		return s, strings.TrimSpace(firstLineAt(output, strings.Index(hay, s.pattern))), n, true
	}
	return hostResourceSignal{}, "", 0, false
}

// newHostResourceError builds the error for a gate that failed on a host
// resource, or returns nil when the output says nothing of the sort. wtDir is
// the gate's own working directory, which is what gets measured — the resource
// that matters is the one the gate was writing to.
func newHostResourceError(gate, output, wtDir string, err error) *hostResourceError {
	sig, sample, n, ok := outputReportsHostResourceExhaustion(output)
	if !ok {
		return nil
	}
	return &hostResourceError{
		Gate:        gate,
		Resource:    sig.resource,
		Signal:      sig.pattern,
		Occurrences: n,
		Sample:      truncate(sample, maxSummaryLen),
		Disk:        measureDiskSpace(wtDir),
		Err:         err,
	}
}

// humanBytes renders a byte count the way an operator reading a disk figure
// wants it. Binary units, because that is what df and Finder both report on the
// hosts this runs on.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
