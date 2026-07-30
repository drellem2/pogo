package hostload

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// readLoadAvg1 returns the host's 1-minute load average, or 0 if it cannot be
// read.
//
// This value is reported as context and is never decided on — see the package
// comment for the measurement that disqualified it. It is read at all because
// it is what let a coordinator notice the original problem: it is a sufficient
// instrument for "something is wrong here" and an insufficient one for "do not
// dispatch". Both of those are true at once, and the difference is the reason
// this function is separate from everything that computes cores.
func readLoadAvg1() float64 {
	// Linux.
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				return v
			}
		}
	}
	// Darwin and the BSDs: `{ 1.23 4.56 7.89 }`.
	if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		s := strings.Trim(strings.TrimSpace(string(out)), "{} ")
		if f := strings.Fields(s); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				return v
			}
		}
	}
	return 0
}
