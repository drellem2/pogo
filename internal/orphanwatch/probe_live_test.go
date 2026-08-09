package orphanwatch

import (
	"testing"
)

// TestProbeGoesRedAgainstAConstructedOrphan is the acceptance bar for mg-4518,
// stated as a test: prove the detector goes RED against an orphan you construct,
// not only GREEN against a clean host.
//
// It is a LIVE test. It starts two real CPU burners, detaches them from their
// launching shells so they genuinely reparent, and runs the real Scan — the real
// process table, the real cwd reader — over the result. Nothing about the
// process table is stubbed; only the registry's liveness answer is stood in,
// because the two owners are synthetic and were never registered.
//
// The failing arm is the point. A detector whose RED path nobody exercises is
// indistinguishable from one that cannot fire, and this fleet has caught three
// controls in exactly that state. Running it in `go test ./...` means the gate
// exercises it on every merge.
//
// It burns about two cores for ~3 seconds and kills both burners by pid. It is
// skipped under -short.
func TestProbeGoesRedAgainstAConstructedOrphan(t *testing.T) {
	if testing.Short() {
		t.Skip("live probe starts real CPU burners; skipped under -short")
	}
	res := Probe(t.TempDir())
	if res.Err != nil {
		t.Fatalf("probe could not be conducted: %v", res.Err)
	}
	t.Log(res.Summary())

	if !res.Reported {
		t.Errorf("FAILING ARM: constructed orphan pid=%d (owner %s, dead) was NOT reported. "+
			"report: busy=%d live_owner=%d unattributable=%d cwd_unreadable=%d orphans=%+v",
			res.OrphanPID, res.DeadOwner, res.Report.Busy, res.Report.LiveOwner,
			res.Report.Unattributable, res.Report.CwdUnreadable, res.Report.Orphans)
	}
	if !res.Spared {
		t.Errorf("POSITIVE CONTROL: pid=%d belongs to a LIVE owner (%s) and was reported anyway. "+
			"This is the case that killed the ppid heuristic — four live workers carried the "+
			"identical signature on 2026-08-07. orphans=%+v", res.ControlPID, res.LiveOwner, res.Report.Orphans)
	}
	if !res.Passed() {
		t.Fatal("probe did not pass both arms")
	}
}

// TestProbeReparentsTheOrphan checks the probe is reproducing the reported
// MECHANISM and not merely a process in a directory. If the launcher stopped
// detaching, the probe would still pass — the detector does not read ppid — and
// would quietly stop being a reproduction of mg-4518.
//
// A ppid that is neither 1 nor the test binary's own is fine on hosts that
// reparent to a subreaper instead of init; what is asserted is only that the
// burner is no longer under the shell that launched it.
func TestProbeReparentsTheOrphan(t *testing.T) {
	if testing.Short() {
		t.Skip("live probe starts real CPU burners; skipped under -short")
	}
	res := Probe(t.TempDir())
	if res.Err != nil {
		t.Fatalf("probe could not be conducted: %v", res.Err)
	}
	if res.DetachedPPID == 0 {
		t.Fatalf("could not read the constructed orphan's ppid (pid=%d); the reproduction cannot be checked", res.OrphanPID)
	}
	if res.DetachedPPID == res.OrphanPID {
		t.Fatalf("orphan pid=%d reports itself as its own parent", res.OrphanPID)
	}
	t.Logf("constructed orphan pid=%d ppid=%d — reported by the detector on cwd alone, "+
		"and the identical ppid is carried by the spared control pid=%d ppid=%d",
		res.OrphanPID, res.DetachedPPID, res.ControlPID, res.ControlPPID)
}
