package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// The polecat witness: a second source of truth about whether a polecat is
// alive, persisted so it survives the death of the pogod that spawned it.
//
// WHY THIS EXISTS (mg-13a3). The scheduler's mail-check GC asks
// registryLiveness (cmd/pogod) whether a schedule's agent is still around.
// The registry is in-memory and has no adopt/reattach path, so a restarted
// pogod's registry is EMPTY — permanently, for every agent that survived the
// restart. Absence never heals. For crew that is harmless: their prompt's
// auto_start is an independent second witness, so the desired state answers
// EXPECTED and their mail-check lives (mg-de08). Polecats have no prompt and
// no auto_start, so BOTH sources came back absent and the classifier's
// default arm concluded death:
//
//	registry: no entry        (absence)
//	desired state: not wanted (absence)
//	=> GONE => reap the mail-check
//
// Two absences are not evidence. mg-61a0 reproduced the consequence
// end-to-end: a live polecat (pid 32471), unregistered after a pogod restart,
// was classified GONE and had its mail-check deleted from memory and disk —
// permanently dark, unreachable by the mayor, with no signal that anything
// was wrong.
//
// What has saved us so far is an accident, not a design: pogod installs no
// signal handler, so its death closes the PTY master and the setsid'd polecat
// takes SIGHUP and dies at its default disposition — meaning a live polecat
// and a running pogod almost always coexist, and the empty-registry case
// stays rare. That accident is load-bearing on the SIGHUP disposition of a
// third-party harness binary we do not control (claude, codex, cursor, pi).
// mg-61a0 pinned it with a test (TestPolecatDoesNotOutlivePogod), but a test
// detects; it cannot prevent. The day a provider traps SIGHUP for graceful
// shutdown, polecats outlive pogod and go dark. This file is what makes that
// day survivable: the polecat's own pid becomes the witness the registry
// lacks, so the classifier has evidence to consult instead of a second
// absence.
//
//	Registry-absent + OUR process alive = UNKNOWN, never GONE.
//
// This is the same shape as mg-76e5 one layer up (mail_check_count yields
// EMPTY, never 0, because unreachable and zero are different facts). Absent
// and dead are different facts.
//
// WHY (pid, start_time) AND NOT pid ALONE. A bare pid is a false witness, and
// trusting one would re-enter mg-8677 through the fix for mg-61a0: PIDs are
// reused. A dead polecat whose pid gets recycled by an unrelated process
// reads as alive => UNKNOWN => its schedule is kept forever, firing at a
// corpse and accumulating unbounded scheduler_fire_failed noise — precisely
// the bug mg-8677 fixed. A bare kill(pid, 0) answers "is SOME process alive",
// never "is OUR process alive". So we persist the kernel's start time for the
// pid and match BOTH: a pid whose start time disagrees is not our polecat and
// resolves GONE, not UNKNOWN.
//
// The store never yields UNKNOWN on the strength of a pid alone. If we cannot
// establish process identity we say so (WitnessUnreadable) rather than
// guessing in either direction.

// witnessStateVersion is the on-disk schema version for the witness file.
// A file written by a NEWER pogod is refused rather than overwritten, matching
// internal/scheduler's store.
const witnessStateVersion = 1

// witnessFileName is the witness store's basename under config.PogoHome().
const witnessFileName = "polecat-witness.json"

// witnessRecord is one polecat's persisted proof of life.
//
// PID and StartTime are a PAIR and are only ever meaningful together — see the
// package comment. StartTime is the KERNEL's start time for the process as
// reported by `ps -o lstart=`, NOT the wall-clock time.Now() at which pogod
// happened to construct the Agent. That distinction is the whole point of the
// field: it must be readable again, from the kernel, by a pogod that never
// spawned this process and has no memory of it. A value we made up ourselves
// could not be re-derived and so could not be matched against.
type witnessRecord struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	StartTime  time.Time `json:"start_time"`
	WorkItemID string    `json:"work_item_id,omitempty"`
}

type witnessOnDisk struct {
	Version  int             `json:"version"`
	Polecats []witnessRecord `json:"polecats"`
}

// WitnessAliveGrep returns the fragment of `pogo agent witness --json` output
// that is present exactly when (name, pid) is witnessed ALIVE right now.
//
// WHY A HELPER AND NOT A LITERAL IN THE CALLER (mg-da48). The orphan alert mail
// tells its reader to re-confirm identity before killing, and wires the check
// into the command it hands out so it cannot be skipped:
//
//	pogo agent witness --json | grep -q '<this>' && kill <pid> && mg unclaim <item>
//
// That instruction is worth exactly as much as the pattern is right. A pattern
// that never matches turns the mail into advice that always refuses — annoying
// but safe. A pattern that matches too MUCH is the dangerous one, and it is
// easy to write by accident: grepping the name alone passes for a DIFFERENT
// incarnation of the same polecat (names are reused; RecordPolecatWitness
// explicitly replaces a record by name), so the kill would land on a live
// successor's pid. Both halves of the identity, or the check is theatre.
//
// It is exported so cmd/pogo can pin it against the real command's real output
// in a test. The pattern is a claim about a serialization that lives in another
// package; the only thing that makes it a fact rather than a hope is a test
// that marshals the actual report and greps it. See TestWitnessAliveGrepMatches
// (cmd/pogo). Field order here is not incidental — it is the contract, and the
// test is what keeps it one.
func WitnessAliveGrep(name string, pid int) string {
	return fmt.Sprintf(`"name":"%s","pid":%d`, name, pid)
}

// witnessMu serialises read-modify-write cycles on the witness file. The file
// is small and written only at polecat spawn/exit, so a single package mutex
// is sufficient and keeps the store independent of any Registry instance —
// the classifier reads it from a pogod that never spawned these agents.
var witnessMu sync.Mutex

// witnessPathOverride lets one test point the store at its own temp file
// without mutating POGO_HOME for the whole process. Empty does NOT mean "use
// PogoHome()" — see WitnessPath. It means "no test asked for a specific path",
// which under `go test` still resolves to a sandbox, never to the live store.
var witnessPathOverride string

// procStartFn is the process start-time probe, indirected so tests can force
// the unreadable-identity branch. Production always uses procStart.
var procStartFn = procStart

// testWitnessOnce/testWitnessDir memoise the per-process sandbox that
// testDefaultWitnessPath hands out. One directory per test binary: tests that
// need isolation from EACH OTHER call sandboxWitness, which is a different
// question from isolation from the LIVE fleet, and only the latter is decided
// here.
var (
	testWitnessOnce sync.Once
	testWitnessDir  string
)

// WitnessPath returns the absolute path of the polecat witness file.
//
// WHY THE TEST BRANCH IS NOT OPT-IN (mg-da48). This store is written at polecat
// spawn, from Spawn, via noteWitnessStart — and `go test ./internal/agent/` is
// full of tests that spawn agents while testing something else entirely. Those
// tests wrote PHANTOM records into the LIVE store: real test-process pids under
// fixture names ("ready-test", "cadence", "no-sentinel-profile"), which pogod's
// orphan detector then read back as leaked polecats and mailed the mayor an
// authoritative `kill <pid>` for. Measured on the live fleet three times in ten
// minutes on 2026-07-17; the pids were dead and recyclable by the time anyone
// read the mail.
//
// The guard for this already existed — witnessPathOverride, and witness_test.go
// calls sandboxWitness sixteen times. It did not help, and the reason is the
// whole lesson: witness_test.go remembers because the witness is its SUBJECT.
// nudge_test.go and attach_regression_test.go spawn agents INCIDENTALLY, and
// from where they stand they are testing nudges and attach — they have no
// reason to know this store exists, and so had zero sandboxWitness calls
// between them. An opt-in guard is only ever remembered by the tests that
// least need it. The same shape as mg-a558, and the same pollutant that hit
// events.log in trust-the-record-not-the-statistic.
//
// So the DEFAULT is the sandbox and there is nothing to remember: under a test
// binary the live store is not reachable from this function at all. A new test
// file that spawns an agent and does nothing special cannot touch it — which is
// the acceptance bar, because "does nothing special" is precisely what the two
// polluting files did.
//
// This branch is decided by testing.Testing(), i.e. by whether OUR binary is a
// test binary. A test that boots the real pogod as a SUBPROCESS is unaffected:
// that child is not a test binary and resolves POGO_HOME as production does,
// which is correct — such tests already sandbox POGO_HOME for the child, and it
// is not this function's business to second-guess a real daemon's state dir.
func WitnessPath() string {
	if witnessPathOverride != "" {
		return witnessPathOverride
	}
	if testing.Testing() {
		return testDefaultWitnessPath()
	}
	return filepath.Join(config.PogoHome(), witnessFileName)
}

// testDefaultWitnessPath returns this test binary's private witness path.
//
// It never returns a path under PogoHome, and it has no error return, because
// there is no failure here that could justify handing back the live store: a
// test that cannot get a temp dir must fail to write ANYWHERE rather than
// succeed at writing a phantom polecat into the fleet's state. The fallback is
// therefore another unwritable-at-worst temp path, not config.PogoHome().
func testDefaultWitnessPath() string {
	testWitnessOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pogo-test-witness-*")
		if err != nil {
			// Still not the live store. A bad path here fails the test that
			// needed it; the live store's phantom records outlive everything.
			dir = filepath.Join(os.TempDir(), fmt.Sprintf("pogo-test-witness-%d", os.Getpid()))
		}
		testWitnessDir = dir
	})
	return filepath.Join(testWitnessDir, witnessFileName)
}

// procStart reads process pid's start time via `ps -o lstart=`, which prints a
// full local timestamp like "Wed Jul 10 15:50:52 2026". ok=false when the
// process is gone or the field cannot be parsed.
//
// Deliberately the same instrument on both sides of the match: the value we
// persist at spawn and the value we probe at classification time are produced
// by this same call, so they are directly comparable without a fudge factor.
// internal/reconcile has a twin of this helper (hostdeps.go) which compares an
// lstart reading against a file mtime and therefore needs a tolerance
// (procStartSkew) to absorb lstart's whole-second truncation. We need no such
// tolerance precisely because we never compare lstart to anything but lstart.
func procStart(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}, false
	}
	return parsePsLstart(string(out))
}

// parsePsLstart parses a `ps -o lstart=` timestamp in the local zone.
// Split out from procStart so it is testable without spawning a process.
func parsePsLstart(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// e.g. "Wed Jul 10 15:50:52 2026" (day-of-month may be space-padded).
	if t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", s, time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// loadWitness reads the witness file. A missing or empty file is not an error:
// it means no polecat has been witnessed, which is a legitimate state (a fresh
// machine, or a fleet with no polecats running).
//
// Caller must hold witnessMu.
func loadWitness() ([]witnessRecord, error) {
	data, err := os.ReadFile(WitnessPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var disk witnessOnDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("witness: parse %s: %w", WitnessPath(), err)
	}
	// Version 0 means a hand-written file with no version key; treat it as
	// current so it round-trips. A version from the future is refused: a newer
	// pogod may carry fields we would silently drop on our next write.
	if disk.Version > witnessStateVersion {
		return nil, fmt.Errorf("witness: %s is version %d, this pogod understands %d — refusing to overwrite",
			WitnessPath(), disk.Version, witnessStateVersion)
	}
	return disk.Polecats, nil
}

// saveWitness atomically replaces the witness file. Mirrors the write sequence
// in internal/scheduler/store.go: temp file in the same directory, fsync,
// rename — so a pogod that dies mid-write leaves the previous witness intact
// rather than a truncated file. A torn witness file would read as "no record"
// and reap live polecats, which is the exact failure this store exists to
// prevent, so the atomicity here is load-bearing rather than hygiene.
//
// Caller must hold witnessMu.
func saveWitness(recs []witnessRecord) error {
	path := WitnessPath()
	if recs == nil {
		recs = []witnessRecord{}
	}
	data, err := json.MarshalIndent(witnessOnDisk{Version: witnessStateVersion, Polecats: recs}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// noteWitnessStart records a live polecat's (pid, start_time) so a future
// pogod can probe it. Called from Spawn, mirroring noteCoordinatorStart.
//
// Only polecats are witnessed. Crew already have a second witness — auto_start
// in their prompt — and giving them a redundant one here would put two sources
// in a position to disagree about the same agent for no gain.
//
// Best-effort in one direction only. If we cannot read the process's start
// time we write NOTHING and log loudly, rather than writing a pid-only record.
// A pid without an identity is exactly the false witness this store exists to
// avoid: it could not distinguish our polecat from whatever process later
// inherits that pid, and it would answer UNKNOWN forever at a corpse. Refusing
// to write leaves the classifier's behaviour exactly as it was before this
// store existed — no better, but no more wrong.
func noteWitnessStart(a *Agent) {
	if a == nil || a.Type != TypePolecat {
		return
	}
	if err := RecordPolecatWitness(a.Name, a.PID, a.WorkItemID); err != nil {
		log.Printf("witness: %v — a future pogod may have no evidence polecat %s was alive (mg-13a3)", err, a.Name)
	}
}

// RecordPolecatWitness probes pid's start time and persists the pair as the
// named polecat's witness. Spawn is the production caller (via
// noteWitnessStart); it is exported because seeding this store is a legitimate
// operation for anything that comes to own a running polecat it did not spawn
// — a future adopt/reattach path would use exactly this, and tests use it to
// build the same state production builds rather than a hand-rolled imitation.
//
// Returns an error WITHOUT writing if the start time cannot be read. See
// noteWitnessStart: a pid-only record is a false witness, and no record at all
// is strictly better than one we cannot trust.
func RecordPolecatWitness(name string, pid int, workItemID string) error {
	start, ok := procStartFn(pid)
	if !ok {
		return fmt.Errorf("witness: cannot read start time for polecat %s (pid=%d) — not recording; "+
			"a pid without an identity cannot be distinguished from a recycled pid", name, pid)
	}

	witnessMu.Lock()
	defer witnessMu.Unlock()
	recs, err := loadWitness()
	if err != nil {
		return fmt.Errorf("witness: cannot load %s: %w — not recording polecat %s", WitnessPath(), err, name)
	}
	// Replace any stale record for this name: a name can be reused by a later
	// polecat, and the newest spawn is the one a probe should find.
	out := make([]witnessRecord, 0, len(recs)+1)
	for _, r := range recs {
		if r.Name != name {
			out = append(out, r)
		}
	}
	out = append(out, witnessRecord{
		Name:       name,
		PID:        pid,
		StartTime:  start,
		WorkItemID: workItemID,
	})
	if err := saveWitness(out); err != nil {
		return fmt.Errorf("witness: cannot persist polecat %s (pid=%d): %w", name, pid, err)
	}
	return nil
}

// noteWitnessExit drops a polecat's witness once its process has exited.
// Called from waitAndHandle, mirroring noteCoordinatorExit.
//
// The removal is itself the positive evidence: this pogod watched the process
// die (cmd.Wait returned), so the record is not merely stale, it is known
// false. Leaving it behind would strand a record whose pid is free to be
// recycled — the witness would then be arguing for a polecat that we know is
// dead. Dropping it returns the agent to "no record", which classifies GONE.
func noteWitnessExit(a *Agent) {
	if a == nil || a.Type != TypePolecat {
		return
	}
	witnessMu.Lock()
	defer witnessMu.Unlock()
	recs, err := loadWitness()
	if err != nil {
		log.Printf("witness: cannot load %s (%v) — leaving polecat %s's witness in place", WitnessPath(), err, a.Name)
		return
	}
	out := make([]witnessRecord, 0, len(recs))
	for _, r := range recs {
		if r.Name != a.Name {
			out = append(out, r)
		}
	}
	if len(out) == len(recs) {
		return // nothing to do; don't rewrite the file
	}
	if err := saveWitness(out); err != nil {
		log.Printf("witness: cannot drop polecat %s's witness: %v", a.Name, err)
	}
}

// WitnessVerdict is what the persisted witness can say about an agent.
type WitnessVerdict int

const (
	// WitnessNoRecord means no polecat witness exists for this agent. NOT
	// evidence of life or death — the caller learns nothing here and must ask
	// someone else. Crew always land here (they are never witnessed), as does
	// any polecat whose witness was dropped at exit or never written.
	WitnessNoRecord WitnessVerdict = iota

	// WitnessAlive means a witness exists and OUR process — matched on pid AND
	// start time — is running. This is evidence of life, and it is the whole
	// point of the store: it is what a restarted pogod has instead of a second
	// absence.
	WitnessAlive

	// WitnessDead means a witness exists and our process is provably not
	// running: either the pid answers no signal at all, or it answers but its
	// start time disagrees with the one we recorded, which means the pid was
	// recycled by an unrelated process and our polecat is long gone. This is
	// positive evidence of death and it is safe to reap on.
	WitnessDead

	// WitnessUnreadable means a witness exists and its pid is answering
	// signals, but we could not read the process's start time to confirm the
	// process is OURS. We know something is alive; we do not know that it is
	// our polecat, and the difference is the entire subject of this file. The
	// honest answer is "cannot tell" — never a reap.
	WitnessUnreadable
)

func (v WitnessVerdict) String() string {
	switch v {
	case WitnessAlive:
		return "alive"
	case WitnessDead:
		return "dead"
	case WitnessUnreadable:
		return "unreadable"
	default:
		return "no-record"
	}
}

// PolecatWitness probes the persisted witness for the given schedule agent and
// reports what the process itself says. The agent may be addressed by its bare
// name or by its event identity (cat-<name>), matching how schedules address
// agents elsewhere.
//
// This is a READ of state that outlives the process that wrote it, which is
// the only reason it can answer a question the in-memory registry cannot.
//
// RESOLUTION CAVEAT, stated rather than glossed: `ps lstart` reports whole
// seconds, so this cannot distinguish our polecat from a recycled pid whose
// new process started within the SAME second. That is not a real exposure —
// pids are allocated sequentially and a reuse requires the system to churn the
// entire pid space first, which does not happen inside one second — but it is
// the limit of the instrument and it belongs on the record. If pids ever
// became reusable that fast, this probe would need a finer identity source
// than lstart, not a wider tolerance.
func PolecatWitness(scheduleAgent string) WitnessVerdict {
	witnessMu.Lock()
	recs, err := loadWitness()
	witnessMu.Unlock()
	if err != nil {
		// We know a witness file exists but cannot read it. That is not
		// evidence of death, and treating it as such would reap a live fleet
		// on a parse error.
		log.Printf("witness: cannot read %s (%v) — treating %s as unreadable, NOT reaping", WitnessPath(), err, scheduleAgent)
		return WitnessUnreadable
	}
	for _, r := range recs {
		if r.Name != scheduleAgent && "cat-"+r.Name != scheduleAgent {
			continue
		}
		return witnessVerdict(r)
	}
	return WitnessNoRecord
}

// witnessVerdict probes ONE record's process and reports what it says.
//
// This is the single place the (pid, start_time) identity match is made.
// PolecatWitness (one agent, "should I reap its mail-check?") and
// WitnessedAlivePolecats (every agent, "who is alive that the registry cannot
// see?") both route through here, and that is deliberate: they are two
// questions about the same fact, and if they ever disagreed about what "our
// process is alive" means, the drain and the reaper would be reasoning about
// different fleets. The verdict has exactly one definition.
func witnessVerdict(r witnessRecord) WitnessVerdict {
	if !pidAlive(r.PID) {
		// Nothing holds this pid. Our polecat is dead and no recycling has
		// happened yet.
		return WitnessDead
	}
	start, ok := procStartFn(r.PID)
	if !ok {
		// The pid answers signal 0 but we cannot establish whose it is.
		log.Printf("witness: polecat %s pid=%d is alive but its start time is unreadable — "+
			"cannot confirm the process is ours; NOT reaping (mg-13a3)", r.Name, r.PID)
		return WitnessUnreadable
	}
	if !start.Equal(r.StartTime) {
		// Something is alive on this pid, but it started at a different
		// time, so it is NOT the process we recorded. The pid was
		// recycled; our polecat died. Reaping here is what keeps a
		// recycled pid from holding a dead polecat's schedule open
		// forever (mg-8677).
		log.Printf("witness: polecat %s pid=%d is alive but started %s, not %s — pid was recycled by an "+
			"unrelated process; our polecat is GONE (mg-13a3)", r.Name, r.PID, start, r.StartTime)
		return WitnessDead
	}
	return WitnessAlive
}

// WitnessedAlivePolecats returns every witnessed polecat whose process is
// provably OURS and running — the positive population the witness store exists
// to make visible. Order is by name so callers and their tests see a stable
// list.
//
// A read error yields nil, NOT an empty list with a nil error: "I cannot read
// the witness file" and "no polecats are alive" are different facts, and this
// store's entire subject is not conflating them. Callers get the error and must
// decide; none of them may render an unreadable store as zero.
func WitnessedAlivePolecats() ([]witnessRecord, error) {
	witnessMu.Lock()
	recs, err := loadWitness()
	witnessMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("witness: cannot read %s: %w", WitnessPath(), err)
	}
	var out []witnessRecord
	for _, r := range recs {
		if witnessVerdict(r) == WitnessAlive {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// WitnessStoreExists reports whether the witness file is on disk at all.
//
// WHY THIS IS NOT THE SAME QUESTION AS "are any polecats alive?" (mg-65b2).
// loadWitness maps a missing file to (nil, nil) — "no polecat has been
// witnessed" — and for the reaper that is right: no record means no evidence to
// reap on, and it declines. But a caller that must decide whether the fleet is
// IDLE cannot accept that mapping, because it collapses the two states this
// package exists to keep apart:
//
//	file present, "polecats":[]  — pogod looked and there are none. A ZERO.
//	file absent                  — nobody ever wrote one here. AN ABSENCE.
//
// saveWitness never removes the file (recs == nil is written as an empty
// array), so on any box whose pogod postdates mg-13a3 an idle fleet leaves a
// present-and-empty file. Absence therefore means something else entirely: a
// pogod that predates the witness, a wiped POGO_HOME, or a store we are not
// looking at. None of those are evidence that nothing is running.
//
// The drain gate is the caller that needs this (drain_wait, via `pogo agent
// witness`): consulting a witness that was never written and reading its
// emptiness as "idle" would rebuild the very fail-open it was reached for —
// concluding "drained" from a SINGLE absence, one layer up (mg-13a3's thesis).
//
// A stat error other than not-exist is returned, never folded into false: "I
// could not look" is not "it is not there", which is this file's whole subject.
func WitnessStoreExists() (bool, error) {
	witnessMu.Lock()
	defer witnessMu.Unlock()
	if _, err := os.Stat(WitnessPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("witness: cannot stat %s: %w", WitnessPath(), err)
	}
	return true, nil
}
