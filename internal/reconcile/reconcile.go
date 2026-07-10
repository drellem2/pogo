// Package reconcile implements pogo's host reconcile step and drift check
// (mg-be0c).
//
// The defect this package exists to fix produced four incidents in a single
// day, none with a worker at fault: a fix merged correctly into git, the code
// was correct, and the running host stayed on the old behavior — because pogo
// generates correct artifacts and had no step that reconciled them onto the
// host, and no check that noticed when the host had drifted. Instance 2 (a
// stale recovery plist) hid for six weeks because nothing compared the loaded
// job to what the generator would produce.
//
// The mechanism here is three things, learned in blood:
//
//   - COPIES, NOT SYMLINKS. A host artifact is a copy of a generator/repo
//     source, never a symlink into a checkout. A symlink from ~/.pogo/…/bin
//     into ~/dev/… would make an uncommitted local edit instantly live in
//     production — no merge, no review — which inverts the repo/host boundary
//     this whole mechanism defends and makes a dev checkout the running system.
//     Copies keep the boundary; the cost is that copies can drift, and drift is
//     detectable (that is what CheckDrift is for).
//
//   - ATOMIC REPLACE, NEVER IN-PLACE REWRITE. bash reads a script by byte
//     offset; rewriting the file under a live interpreter can resume it at a
//     shifted offset and execute garbage. AtomicReplace writes a temp file in
//     the target's directory and rename(2)s it over the target, so the running
//     interpreter keeps its original inode until it is replaced wholesale.
//
//   - RESTART THE PROCESS, NEVER JUST THE FILE. Writing bytes changes nothing
//     for a long-lived bash `while` loop: bash parses the whole loop once and
//     never re-reads the file, so a hardened poller can run its pre-hardening
//     code for its entire life. And on this host launchd dispatches no
//     nondemand spawns (mg-50e0) — KeepAlive/RunAtLoad start nothing — so the
//     restart cannot be delegated to launchd. Reconcile issues an explicit
//     `launchctl kickstart` after it replaces the bytes. The kickstart is
//     load-bearing, not a courtesy.
//
// CheckDrift is the real deliverable: detection is what actually failed. It
// REPORTS, it never auto-reconciles (an auto-fix loop fighting a genuinely
// broken artifact is the same unbounded-reaper failure shape), and it compares
// the RUNNING REALITY, not just the on-disk file — a plist whose bytes match
// the generator but whose LOADED job still execs the old path is still drifted,
// and a poller whose file was updated after the process started is running old
// code even though the path is identical. Both are "the file is not the
// process."
//
// Complementary to the tier-1 reaper (mg-d18b), not overlapping: the reaper
// kickstarts a job whose HEARTBEAT is stale (dead/wedged process); reconcile
// restarts a job after its FILE changed (alive process running old code). A
// fresh heartbeat proves the process is doing work, not that it runs the
// current code. Neither covers the other's case.
package reconcile

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// procStartSkew is the tolerance the stale-process check allows between a
// target's mtime and the running process's start time before it calls the
// process stale. It exists because `ps lstart` reports a process start time
// truncated to whole seconds while a file's mtime carries sub-second precision:
// a file written 0.1s BEFORE a process that started at the same whole second
// would otherwise read as "written after the process started" and produce a
// false positive. The real signal this check exists for is a process that
// predates its file by a meaningful margin (pa's pollers ran 41 minutes of
// pre-patch code), so a couple seconds of slack costs nothing and removes the
// same-second race.
const procStartSkew = 2 * time.Second

// Mirror describes one host-side artifact that is a COPY of a generator/repo
// source — never a symlink into it. See the package doc for why copies.
type Mirror struct {
	// Name is a short label used in reports, e.g. "watchdog".
	Name string
	// Source is the truth: the repo/generator file whose bytes are canonical.
	Source string
	// Target is the copy that launchd (or anything else) actually execs.
	Target string
	// Label is the launchd label to kickstart after Target changes. Empty
	// means the artifact is not a running job and only the file is reconciled.
	Label string
}

// KickFunc kickstarts launchd job `label` and returns the pid launchd assigns
// afterward. service.KickstartJob is the production implementation; tests
// inject a fake so the package stays launchctl-free and unit-testable.
type KickFunc func(label string) (int, error)

// Deps holds the impure lookups CheckDrift and Reconcile need to inspect the
// RUNNING reality. Every field is injectable so the drift logic is unit
// testable without launchctl or a real process table; the CLI wires the real
// implementations (launchctl print / ps / stat). A nil field means "that
// signal is unavailable" and the corresponding check is skipped rather than
// treated as drift — an absent signal must never masquerade as a clean host.
type Deps struct {
	// LoadedProgram returns the program path the loaded launchd job for label
	// actually execs, parsed from `launchctl print`. ok=false when the job or
	// its program is unknown (not loaded, or launchctl unavailable).
	LoadedProgram func(label string) (path string, ok bool)
	// RunningPID returns the pid launchd currently has assigned for label.
	// ok=false when the job has no live process.
	RunningPID func(label string) (pid int, ok bool)
	// ProcStart returns the wall-clock start time of process pid. ok=false when
	// the process is gone or its start time cannot be read.
	ProcStart func(pid int) (start time.Time, ok bool)
	// FileMtime returns path's modification time. ok=false when path is missing.
	FileMtime func(path string) (mtime time.Time, ok bool)
}

// Result records what Reconcile did to one mirror.
type Result struct {
	Name        string
	Changed     bool // the Target bytes were replaced
	Kickstarted bool // a restart was issued
	NewPID      int  // the pid launchd assigned after the kickstart (best effort)
	Reason      string
	Err         error
}

// AtomicReplace makes target hold exactly data, using a same-directory temp
// file plus rename(2) — never an in-place rewrite (see the package doc). It is
// idempotent: when target already holds exactly data it does nothing and
// returns changed=false, so a re-run neither rewrites bytes nor disturbs a
// running interpreter's inode.
func AtomicReplace(target string, data []byte, mode fs.FileMode) (changed bool, err error) {
	if existing, rerr := os.ReadFile(target); rerr == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// The temp file must share the target's directory so the rename is on one
	// filesystem and therefore atomic; a temp in $TMPDIR could land on a
	// different volume and degrade to a non-atomic copy.
	tmp, err := os.CreateTemp(dir, ".reconcile-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return false, fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return false, fmt.Errorf("rename %s -> %s: %w", tmpName, target, err)
	}
	return true, nil
}

// Reconcile makes m.Target a byte-for-byte copy of m.Source and, when a Label
// is set, kickstarts the job so the RUNNING process picks up the new bytes.
//
// It kickstarts when EITHER the bytes changed OR the running process is stale
// against the (now-correct) file. The second case is why reconcile is
// self-healing: a box whose file is already right but whose process started
// before the file was written (pa's pollers ran 41 minutes of pre-patch code)
// is healed by a re-run, even though AtomicReplace reports no byte change.
func Reconcile(m Mirror, kick KickFunc, deps Deps) Result {
	res := Result{Name: m.Name}
	data, err := os.ReadFile(m.Source)
	if err != nil {
		res.Err = fmt.Errorf("%s: read source %s: %w", m.Name, m.Source, err)
		return res
	}
	mode := fs.FileMode(0o755)
	if fi, serr := os.Stat(m.Source); serr == nil {
		mode = fi.Mode().Perm()
	}
	changed, err := AtomicReplace(m.Target, data, mode)
	if err != nil {
		res.Err = fmt.Errorf("%s: %w", m.Name, err)
		return res
	}
	res.Changed = changed

	if m.Label == "" || kick == nil {
		if changed {
			res.Reason = "file replaced (no launchd job)"
		}
		return res
	}

	// Decide whether a restart is warranted. Bytes changed always warrants one.
	// If they did not, the running process may still be stale (old path or a
	// pre-write start time); reuse the drift check so reconcile heals that too.
	reason := ""
	if changed {
		reason = "file replaced"
	} else if d := CheckDrift(m, deps); d.runningDrifted() {
		reason = "running process stale: " + d.runningReason()
	}
	if reason == "" {
		return res
	}
	pid, kerr := kick(m.Label)
	if kerr != nil {
		res.Err = fmt.Errorf("%s: kickstart %s: %w", m.Name, m.Label, kerr)
		return res
	}
	res.Kickstarted = true
	res.NewPID = pid
	res.Reason = reason
	return res
}

// Drift is the per-mirror drift report. An empty field means that dimension is
// clean; a non-empty field is a human-readable description of the divergence,
// naming the specific file or job. Clean() reports whether all dimensions agree.
type Drift struct {
	Name   string
	Target string
	Label  string
	// File drift: the on-disk copy no longer matches its source (the c51b
	// `cmp` case). "" when clean.
	FileDrift string
	// Path drift (running reality): the LOADED launchd job execs a different
	// program than Target — a plist whose bytes match but whose loaded job
	// points at the old path. This is exactly how the recovery plist hid for
	// six weeks. "" when clean.
	PathDrift string
	// Stale-process drift (running reality): the process launchd is running
	// started BEFORE Target was last written, so it parsed old bytes even
	// though the path is identical (pa's pollers). "" when clean.
	StaleProc string
}

// Clean reports whether the mirror has no drift on any dimension.
func (d Drift) Clean() bool {
	return d.FileDrift == "" && d.PathDrift == "" && d.StaleProc == ""
}

// runningDrifted reports drift in the RUNNING reality (path or stale process),
// independent of on-disk file drift.
func (d Drift) runningDrifted() bool {
	return d.PathDrift != "" || d.StaleProc != ""
}

func (d Drift) runningReason() string {
	parts := []string{}
	if d.PathDrift != "" {
		parts = append(parts, d.PathDrift)
	}
	if d.StaleProc != "" {
		parts = append(parts, d.StaleProc)
	}
	return strings.Join(parts, "; ")
}

// Report renders a one-mirror drift block for humans. Empty when clean.
func (d Drift) Report() string {
	if d.Clean() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  DRIFT %s (%s)\n", d.Name, d.Target)
	if d.FileDrift != "" {
		fmt.Fprintf(&b, "        file:    %s\n", d.FileDrift)
	}
	if d.PathDrift != "" {
		fmt.Fprintf(&b, "        loaded:  %s\n", d.PathDrift)
	}
	if d.StaleProc != "" {
		fmt.Fprintf(&b, "        process: %s\n", d.StaleProc)
	}
	return b.String()
}

// CheckDrift compares one mirror's host reality against its source and reports
// divergence. It never mutates anything — reporting, not auto-reconciling, is
// deliberate (an auto-fix loop fighting a broken artifact is the unbounded
// reaper's failure shape). It checks three dimensions; see Drift.
func CheckDrift(m Mirror, deps Deps) Drift {
	d := Drift{Name: m.Name, Target: m.Target, Label: m.Label}

	// (1) File drift — the c51b `cmp`: does the copy still match the source?
	src, serr := os.ReadFile(m.Source)
	tgt, terr := os.ReadFile(m.Target)
	switch {
	case serr != nil:
		d.FileDrift = fmt.Sprintf("cannot read source %s: %v", m.Source, serr)
	case terr != nil && os.IsNotExist(terr):
		d.FileDrift = fmt.Sprintf("MISSING: %s is not deployed", m.Target)
	case terr != nil:
		d.FileDrift = fmt.Sprintf("cannot read target %s: %v", m.Target, terr)
	case !bytes.Equal(src, tgt):
		d.FileDrift = fmt.Sprintf("MODIFIED: %s differs from source %s", m.Target, m.Source)
	}

	if m.Label == "" {
		return d
	}

	// (2) Path drift — does the LOADED job exec Target, or something else?
	if deps.LoadedProgram != nil {
		if loaded, ok := deps.LoadedProgram(m.Label); ok && !samePath(loaded, m.Target) {
			d.PathDrift = fmt.Sprintf("job %s execs %s, expected %s", m.Label, loaded, m.Target)
		}
	}

	// (3) Stale process — did the running process start before Target's last
	// write? If so it parsed old bytes even at the correct path.
	if deps.RunningPID != nil && deps.ProcStart != nil && deps.FileMtime != nil {
		if pid, ok := deps.RunningPID(m.Label); ok {
			start, sok := deps.ProcStart(pid)
			mtime, mok := deps.FileMtime(m.Target)
			if sok && mok && mtime.After(start.Add(procStartSkew)) {
				d.StaleProc = fmt.Sprintf("pid %d (job %s) started %s, before %s was updated %s",
					pid, m.Label, start.Format(time.RFC3339), m.Target, mtime.Format(time.RFC3339))
			}
		}
	}

	return d
}

// samePath compares two filesystem paths for equality after cleaning. It does
// not resolve symlinks (host targets are copies by design, not links), so this
// is a lexical comparison of cleaned absolute-ish paths.
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// ParseLaunchctlProgram extracts the program path a loaded launchd job execs
// from `launchctl print` output. It prefers the `program = <path>` line and
// falls back to the first entry of the `arguments = { … }` block (argv[0]),
// which is what launchd shows for a job configured via ProgramArguments.
// ok=false when neither is present.
func ParseLaunchctlProgram(printOutput string) (string, bool) {
	lines := strings.Split(printOutput, "\n")
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "program = ") {
			p := strings.TrimSpace(strings.TrimPrefix(t, "program = "))
			if p != "" {
				return p, true
			}
		}
	}
	// Fall back to argv[0] inside the arguments block.
	inArgs := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if !inArgs {
			if strings.HasPrefix(t, "arguments = {") {
				inArgs = true
			}
			continue
		}
		if t == "}" {
			break
		}
		if t != "" {
			return t, true
		}
	}
	return "", false
}

// ParseLaunchctlPID extracts the running pid from `launchctl print` output
// (`pid = <n>`). ok=false when the job has no live pid.
func ParseLaunchctlPID(printOutput string) (int, bool) {
	for _, ln := range strings.Split(printOutput, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "pid = ") {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(t, "pid = ")), "%d", &pid); err == nil && pid > 0 {
				return pid, true
			}
		}
	}
	return 0, false
}
