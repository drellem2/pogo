//go:build darwin

package sleep

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
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
