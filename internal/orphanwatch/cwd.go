package orphanwatch

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// cwdTimeout bounds the external cwd read. lsof on a busy host is not fast, and
// a detector that hangs is a detector that stops reporting.
const cwdTimeout = 20 * time.Second

// ReadCwds returns the working directory of each requested pid.
//
// A pid MISSING from the result is a pid whose cwd could not be read — it
// exited between the sample and the read, or it belongs to another user and the
// kernel refused. That is not an empty string and must not be flattened into
// one: "no cwd" and "cwd is unattributable" are both non-verdicts here, but only
// the first is an instrument limit, and Scan counts them separately.
func ReadCwds(pids []int) map[int]string {
	if len(pids) == 0 {
		return map[int]string{}
	}
	if runtime.GOOS == "linux" {
		return readCwdsProcfs(pids)
	}
	return readCwdsLsof(pids)
}

// readCwdsProcfs reads /proc/<pid>/cwd, which is a symlink to the directory.
func readCwdsProcfs(pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	for _, pid := range pids {
		dir, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
		if err != nil || dir == "" {
			continue
		}
		out[pid] = dir
	}
	return out
}

// readCwdsLsof shells out to lsof, which is the only cwd source on darwin that
// does not require linking against libproc.
//
// One invocation for the whole batch: lsof takes tens to hundreds of
// milliseconds per call on a loaded host, and this runs after the CPU filter
// precisely so the batch is small.
func readCwdsLsof(pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	list := make([]string, 0, len(pids))
	for _, pid := range pids {
		list = append(list, strconv.Itoa(pid))
	}
	ctx, cancel := context.WithTimeout(context.Background(), cwdTimeout)
	defer cancel()
	// -a AND-combines the selectors; -d cwd restricts to the working-directory
	// "descriptor"; -Fpn asks for the machine-readable field format, which
	// emits `p<pid>`, `fcwd`, `n<path>` one per line.
	cmd := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-p", strings.Join(list, ","), "-Fpn")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out
	}
	// lsof exits non-zero when it could not open SOME of the pids, while still
	// printing the ones it could. The rows are the answer; the exit status is
	// not, so it is deliberately not consulted.
	if err := cmd.Start(); err != nil {
		return out
	}
	scan := bufio.NewScanner(stdout)
	cur := 0
	for scan.Scan() {
		line := scan.Text()
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(line[1:])
			if err != nil {
				cur = 0
				continue
			}
			cur = pid
		case 'n':
			if cur != 0 {
				out[cur] = line[1:]
			}
		}
	}
	_ = cmd.Wait()
	return out
}
