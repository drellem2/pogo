//go:build darwin

package sleep

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// wakePredicate matches kernel/airportd "Wake reason:" entries in the
// unified log. The trailing colon is load-bearing: it distinguishes a real
// wake event from the unified log's own echoes of `log stream` invocations,
// whose argv (containing our predicate text) would otherwise feedback-loop.
const wakePredicate = `eventMessage CONTAINS[c] "Wake reason:"`

// Watch streams the macOS unified log for kernel wake-reason events and
// invokes hook each time the system wakes from sleep. It returns once the
// `log stream` subprocess is started; the reader runs in a goroutine until
// ctx is canceled (which terminates the subprocess via exec.CommandContext).
//
// The sleep-resilience design (docs/sleep-resilience-design.md §5) lists two
// implementation routes: an IOKit IOPMSleepWakeMessageType notifier via
// cgo, or polling `pmset -g log`. We use neither directly:
//
//   - cgo is incompatible with the goreleaser build (CGO_ENABLED=0).
//   - On macOS Sequoia / Sonoma, `pmset -g log` no longer records explicit
//     "Wake from Sleep" lines; the kernel's wake reason now lands in the
//     unified logging system.
//
// `log stream` is the modern, non-cgo equivalent of both: it streams kernel
// power events as they happen, satisfying the spec's <1s latency goal
// without polling overhead and without forcing CGO_ENABLED=1.
//
// Returns an error if the `log` binary is unavailable or the subprocess
// cannot be started. Callers should fall back to the portable heartbeat
// detector in that case (graceful degrade — the only thing lost is wake
// latency, not correctness).
func Watch(ctx context.Context, hook func()) error {
	if hook == nil {
		return errors.New("platform/sleep: hook must not be nil")
	}
	if _, err := exec.LookPath("log"); err != nil {
		return fmt.Errorf("platform/sleep darwin: `log` binary unavailable: %w", err)
	}
	// Clear any watcher stranded by an earlier pogod before adding our own.
	// See reapOrphanedWatchers for why they survive and why this is the only
	// remedy darwin offers.
	if n := reapOrphanedWatchers(); n > 0 {
		log.Printf("platform/sleep darwin: reaped %d orphaned `log stream` watcher(s) stranded by an earlier pogod", n)
	}
	cmd := exec.CommandContext(ctx, "log", "stream", "--predicate", wakePredicate)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("platform/sleep darwin: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform/sleep darwin: start log stream: %w", err)
	}
	go runStream(ctx, cmd, stdout, hook)
	return nil
}

func runStream(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, hook func()) {
	defer func() {
		// Drain stdout and reap the subprocess so we don't leak a zombie or
		// a half-open pipe.
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
	}()
	sc := bufio.NewScanner(stdout)
	// log stream lines can be long (full process path + message). 1 MiB is
	// generous; the default 64 KiB has been observed to truncate.
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if !isUserWakeLine(sc.Text()) {
			continue
		}
		hook()
	}
	if ctx.Err() == nil {
		// Subprocess died unexpectedly. The heartbeat detector will keep
		// catching wakes within an Interval; we lose only the latency
		// improvement until pogod restarts.
		log.Printf("platform/sleep darwin: log stream ended (%v); falling back to heartbeat-only wake detection", sc.Err())
	}
}

// reapOrphanedWatchers terminates `log stream` subprocesses stranded by an
// earlier pogod, returning how many it signalled.
//
// Why they exist at all — the production spawn path in Watch is correct, so
// this is not a bug in it. pogod installs no SIGTERM handler, so EVERY way it
// dies (the routine `pogo server stop`, launchd's restart, log.Fatal on a
// bind failure, SIGKILL, panic, host crash) skips deferred functions; see the
// exit-path inventory at cmd/pogod/main.go. The `defer hbCancel()` that
// exec.CommandContext depends on therefore never runs, and our child
// reparents to launchd.
//
// It ought to then die of SIGPIPE on its next write to the now-closed stdout
// pipe — which is exactly why pogod's other children do not accumulate. But
// wakePredicate matches almost nothing, so this child never writes, never
// takes SIGPIPE, and streams forever. Each survivor stays a live subscriber
// to the unified logging system that diagnosticd must service: 243 of them
// held one core at ~89% and the box at load 90-120 (mg-55de). Tests that boot
// a real pogod and SIGTERM it strand one apiece, which is why the leak
// accelerated under heavy test activity rather than tracking uptime.
//
// darwin has no PDEATHSIG, so a child cannot ask the kernel to kill it when
// its parent dies. Converging at startup is the remedy actually available:
// each pogod boot clears the corpse the previous one left, bounding the leak
// at one instead of letting it grow without limit. It also covers the
// SIGKILL/panic/crash paths that no shutdown handler could.
//
// Matching is deliberately narrow, and the narrowness is the safety property:
// PPID 1 (a live pogod's watcher has that pogod as its parent, so it is never
// a candidate) plus an exact argv match against our own predicate. Kills go
// one PID at a time via a signal to that PID. An unanchored `pkill -f` must
// never be used here — every pogo poller idles in `sleep N` under
// `set -euo pipefail`, and a broad pattern has taken this fleet's mail
// pollers and watchdog down before.
func reapOrphanedWatchers() int {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		// Cleanup is best-effort: never fail wake detection over it.
		return 0
	}
	reaped := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		pid, ppid, argv, ok := parsePSLine(sc.Text())
		if !ok || !isOrphanedWatcher(ppid, argv) {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			continue
		}
		reaped++
	}
	return reaped
}

// isOrphanedWatcher reports whether a `ps` row is one of our own wake
// watchers left without a parent. Both conditions are load-bearing: PPID 1
// excludes every watcher a running pogod still owns, and the exact argv match
// excludes everything else on the machine. Anything this returns true for is
// about to be signalled, so it must never widen into a substring match.
func isOrphanedWatcher(ppid int, argv string) bool {
	if ppid != 1 {
		return false
	}
	argv0, rest, found := strings.Cut(argv, " ")
	if !found {
		return false
	}
	// argv[0] is whatever the spawning pogod passed — "log" as Watch spawns
	// it today, but a fully-resolved /usr/bin/log is equally ours.
	if filepath.Base(argv0) != "log" {
		return false
	}
	return rest == "stream --predicate "+wakePredicate
}

// parsePSLine splits a `ps -axo pid=,ppid=,command=` row. The command field
// contains spaces (our predicate has three), so only the first two
// space-delimited fields may be split off; the remainder is argv verbatim.
func parsePSLine(line string) (pid, ppid int, argv string, ok bool) {
	pidStr, rest, found := strings.Cut(strings.TrimLeft(line, " "), " ")
	if !found {
		return 0, 0, "", false
	}
	ppidStr, argv, found := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if !found {
		return 0, 0, "", false
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 1 {
		return 0, 0, "", false
	}
	ppid, err = strconv.Atoi(ppidStr)
	if err != nil {
		return 0, 0, "", false
	}
	return pid, ppid, strings.TrimLeft(argv, " "), true
}

// isUserWakeLine returns true for syslog-formatted lines from `log stream`
// that report a real kernel/airportd wake-reason entry, and false for the
// stream's banner header or the unified log's echo of our own `log stream`
// invocation (feedback-loop guard).
func isUserWakeLine(line string) bool {
	if line == "" {
		return false
	}
	// Banner emitted at stream startup before any events.
	if strings.HasPrefix(line, "Timestamp ") {
		return false
	}
	// Self-reference: every `log` invocation is itself logged by the
	// unified log along with its argv, which contains our predicate text
	// and therefore matches the predicate. The `log` tool tags these
	// entries with " log: [com.apple.log:]".
	if strings.Contains(line, " log: [com.apple.log:]") {
		return false
	}
	// Defensive: only fire if the eventMessage actually contains the
	// wake-reason marker (case-insensitive). Guards against unrelated
	// entries that match the predicate by accident.
	return strings.Contains(strings.ToLower(line), "wake reason:")
}
