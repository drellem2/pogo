// Package proctable reads the host's process table — pid, parent, process
// group, and cumulative CPU time per process — and says how precise the CPU
// column it just returned actually is.
//
// # Why the resolution is part of the answer
//
// Two callers difference this table across a short window to answer "is this
// subtree doing work": internal/hostload for the fleet's share of the host, and
// the refinery's gate watch for one gate's subtree. Both compute
// CPU-seconds-per-wall-second, and both are only as good as the granularity of
// the CPU column they differenced.
//
// That granularity is not the same everywhere, and the difference is not
// cosmetic:
//
//   - BSD `ps` on darwin prints TIME as `MM:SS.ss` — hundredths, 10ms.
//   - procps `ps` on Linux prints TIME as `[DD-]HH:MM:SS` — WHOLE SECONDS.
//     There is no sub-second format option; `cputimes` is integer seconds too.
//
// So the identical code that measures a spinning subtree at 1.00 cores over a
// 400ms window on darwin measures it at 0.00 on Linux, because 400ms of CPU
// rounds to zero whole seconds at both ends of the window. That is exactly what
// happened: mg-0c51's and mg-1b8c's tests were written and verified on darwin
// and went red on every CI run from 11:17 on 2026-07-30, asserting against
// cores=0.00 in a Linux runner (mg-79e3). Nothing was broken; the instrument
// could not read the scale.
//
// This package fixes the instrument rather than the scale. On Linux it reads
// /proc/<pid>/stat directly, whose utime+stime are in USER_HZ ticks — 10ms,
// the same order as darwin's — so a short window resolves work on both. Where
// no better source exists it still returns rows, but it reports the coarse
// Resolution alongside, so a caller can say "this window is too short for this
// host" instead of reporting a fabricated zero.
//
// # Supported environments
//
//	linux, /proc readable   linux-procfs   10ms   short windows resolve
//	darwin                  darwin-ps      10ms   short windows resolve
//	linux, no /proc         linux-ps        1s    needs a multi-second window
//	other unix              <goos>-ps       1s    needs a multi-second window
//
// A caller that must measure over a window shorter than Source.MinWindow has
// no measurement, and must say so — see Source.CannotResolve.
package proctable

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Row is one process as the table reports it.
type Row struct {
	PID  int
	PPID int
	PGID int
	// CPU is cumulative CPU time (user + system) consumed by the process
	// since it started, truncated to the source's Resolution.
	CPU time.Duration
}

// Source names where rows come from and how precise their CPU column is.
type Source struct {
	// Name identifies the reader, e.g. "linux-procfs". It is printed in test
	// output and in gate records so that a run states the environment it
	// measured in — this whole class of bug is invisible until it does.
	Name string
	// Resolution is the quantum of the CPU column: every Row.CPU is a whole
	// multiple of it, so any difference of two readings carries up to one
	// Resolution of error per process.
	Resolution time.Duration
}

// minWindowTicks is how many Resolution ticks a window must span before a rate
// taken over it means anything. Five bounds the quantisation error on a subtree
// burning one full core at 20% — enough to separate "about a core" from
// "nothing", which is the discrimination both callers exist to make.
//
// It is deliberately NOT enough to resolve the 0.02-core idle threshold; that
// would need ~50 ticks. A window between MinWindow and that can say "this is
// work" or "this is not a core's worth", and nothing finer.
const minWindowTicks = 5

// MinWindow is the shortest window over which this source can separate a
// subtree burning a full core from an idle one.
func (s Source) MinWindow() time.Duration { return minWindowTicks * s.Resolution }

// CanResolve reports whether a rate taken over window is meaningful.
func (s Source) CanResolve(window time.Duration) bool { return window >= s.MinWindow() }

// CannotResolve returns the reason a window is too short for this source, or
// "" when it is long enough. It returns a REASON rather than a bool because
// the caller's job is to report "no measurement, and why" — a bool would let
// it degrade back into the zero it cannot distinguish from idle.
func (s Source) CannotResolve(window time.Duration) string {
	if s.CanResolve(window) {
		return ""
	}
	return fmt.Sprintf("%s resolves CPU time to %s, so a %s window cannot separate work from none "+
		"(needs %s)", s.Name, s.Resolution, window, s.MinWindow())
}

// String renders the source and its precision together, because neither is
// useful without the other.
func (s Source) String() string {
	return fmt.Sprintf("%s (%s CPU-time resolution, usable from a %s window)",
		s.Name, s.Resolution, s.MinWindow())
}

// Source names.
const (
	linuxProcfs = "linux-procfs"
	psSuffix    = "-ps"
)

const (
	// procfsTick is the unit /proc/<pid>/stat reports CPU time in. Linux fixes
	// USER_HZ at 100 for procfs output regardless of the kernel's internal
	// CONFIG_HZ, so this is 10ms wherever the procfs source is selected.
	procfsTick = 10 * time.Millisecond
	// darwinPSTick is BSD ps's TIME precision: `MM:SS.ss`.
	darwinPSTick = 10 * time.Millisecond
	// psSecondTick is procps' TIME precision: whole seconds, no finer format
	// available.
	psSecondTick = time.Second
)

// procfsRoot is where the Linux process table lives. A variable so the reader
// can be exercised against a fixture on a machine that has no /proc.
var procfsRoot = "/proc"

// current is resolved once: the answer cannot change while the process runs,
// and every caller must agree on it or one of them will difference a table it
// has the wrong precision for.
var current = sync.OnceValue(detect)

// Current returns the process-table source in force on this host.
func Current() Source { return current() }

func detect() Source {
	switch runtime.GOOS {
	case "linux":
		if _, err := os.Stat(filepath.Join(procfsRoot, "self", "stat")); err == nil {
			return Source{Name: linuxProcfs, Resolution: procfsTick}
		}
		return Source{Name: "linux" + psSuffix, Resolution: psSecondTick}
	case "darwin":
		return Source{Name: "darwin" + psSuffix, Resolution: darwinPSTick}
	default:
		// Unknown ps dialect: assume the coarse one. Claiming more precision
		// than a source has is the failure this package exists to prevent, so
		// the unknown case errs toward "say you cannot measure".
		return Source{Name: runtime.GOOS + psSuffix, Resolution: psSecondTick}
	}
}

// Read snapshots every process on the host.
func Read() ([]Row, error) {
	if Current().Name == linuxProcfs {
		return readProcfs(procfsRoot)
	}
	return readPS()
}

// readProcfs walks /proc. A process that exits between the directory listing
// and the read of its stat file is skipped rather than failing the snapshot:
// one departed process must not cost the whole measurement.
func readProcfs(root string) ([]Row, error) {
	dir, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("proctable: open %s: %w", root, err)
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return nil, fmt.Errorf("proctable: read %s: %w", root, err)
	}
	rows := make([]Row, 0, len(names))
	for _, name := range names {
		if pid, err := strconv.Atoi(name); err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, name, "stat"))
		if err != nil {
			continue
		}
		if row, ok := parseProcStat(string(b)); ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("proctable: %s held no readable process entries", root)
	}
	return rows, nil
}

// parseProcStat reads one /proc/<pid>/stat line.
//
// The command name is field 2 and is wrapped in parentheses that it may itself
// contain — `(sh -c (x))` is a legal comm — so the fields after it are located
// from the LAST ')' rather than by splitting the whole line. Everything after
// that paren starts at field 3.
func parseProcStat(s string) (Row, bool) {
	open := strings.IndexByte(s, '(')
	closing := strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return Row{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil || pid <= 0 {
		return Row{}, false
	}
	f := strings.Fields(s[closing+1:])
	// f[0] is field 3 (state), so field N sits at f[N-3]: ppid 4, pgrp 5,
	// utime 14, stime 15.
	const utimeIdx, stimeIdx = 14 - 3, 15 - 3
	if len(f) <= stimeIdx {
		return Row{}, false
	}
	ppid, err1 := strconv.Atoi(f[4-3])
	pgid, err2 := strconv.Atoi(f[5-3])
	utime, err3 := strconv.ParseInt(f[utimeIdx], 10, 64)
	stime, err4 := strconv.ParseInt(f[stimeIdx], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return Row{}, false
	}
	if utime < 0 || stime < 0 {
		return Row{}, false
	}
	return Row{
		PID:  pid,
		PPID: ppid,
		PGID: pgid,
		// utime+stime only: cutime/cstime are reaped children's time, charged
		// to the parent at wait(), which would make a rate spike at reaping
		// rather than when the work happened.
		CPU: time.Duration(utime+stime) * procfsTick,
	}, true
}

// ReadTimeout bounds a process-table read. Exported because a caller that
// joins a sampler goroutine has to bound its own wait by it. These readings
// are taken on hosts
// that may be under heavy contention — which is exactly when someone is
// looking — and an unbounded exec would let a sampler hang. A hung sampler
// must degrade to "no measurement", which callers report honestly.
const ReadTimeout = 10 * time.Second

// execPS runs ps. A variable so a test can exercise the failure path without
// arranging for a real ps to fail.
var execPS = func(ctx context.Context) ([]byte, error) {
	// -A is the POSIX spelling of "every process" and is accepted by both the
	// BSD ps on darwin and procps on Linux; the trailing "=" on each field
	// suppresses the header so the output is pure data.
	return exec.CommandContext(ctx, "ps", "-Ao", "pid=,ppid=,pgid=,time=").Output()
}

func readPS() ([]Row, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ReadTimeout)
	defer cancel()
	out, err := execPS(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ps did not return within %s", ReadTimeout)
		}
		return nil, fmt.Errorf("ps: %w", err)
	}
	return ParsePS(string(out)), nil
}

// ParsePS turns `ps -Ao pid=,ppid=,pgid=,time=` output into rows, skipping
// anything it cannot read rather than failing the whole sample: one malformed
// line must not cost the measurement.
func ParsePS(out string) []Row {
	var rows []Row
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		cpu, ok := ParseCPUTime(f[3])
		if err1 != nil || err2 != nil || err3 != nil || !ok {
			continue
		}
		rows = append(rows, Row{PID: pid, PPID: ppid, PGID: pgid, CPU: cpu})
	}
	return rows
}

// ParseCPUTime reads ps's cumulative TIME column: [[DD-]HH:]MM:SS[.ff].
func ParseCPUTime(s string) (time.Duration, bool) {
	days := 0
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days = d
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hours, mins int
	if len(parts) == 3 {
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, false
		}
		hours = h
	}
	m, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		return 0, false
	}
	mins = m
	secs, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, false
	}
	total := float64(days)*86400 + float64(hours)*3600 + float64(mins)*60 + secs
	if total < 0 {
		return 0, false
	}
	return time.Duration(total * float64(time.Second)), true
}
