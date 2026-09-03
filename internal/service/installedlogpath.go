package service

import (
	"fmt"
	"os"
	"runtime"
)

// InstalledLogPath reports where the INSTALLED launchd job redirects pogod's
// stderr — read out of the plist on disk, not derived from the template.
//
// WHY IT IS READ AND NOT DERIVED (mg-a19a, and mg-7537 before it). PogodLogPath
// returns the path this build's template WOULD install. A plist written by an
// older build, or edited by hand, can name a different file — and the file a
// diagnostician is about to grep is the one the loaded job names, not the one
// this binary would have chosen. mayor.md already tells readers to derive the
// path from the installed plist for exactly this reason; this is that
// derivation in code, so the daemon's own log-liveness check judges the same
// path a human following the documented procedure would grep.
//
// StandardErrorPath is preferred over StandardOutPath because the check that
// consumes this compares against a process's fd 2. The template points both at
// one file, so they agree today; when they do not, fd 2 is the one that
// answers the question being asked.
//
// Returns ok=false when there is no installed job, when the plist cannot be
// read or parsed, or when it declares no redirect at all — every one of which
// is a reading that could not be taken rather than a path of "". A caller must
// not substitute a default for a false ok: doing so is how a check starts
// judging a file nobody writes to.
func InstalledLogPath() (string, bool) {
	if runtime.GOOS != "darwin" {
		// systemd's generated unit sets no StandardOutput, so there is no log
		// FILE to name — output goes to the journal. Reporting a path here
		// would invent one.
		return "", false
	}
	installed, plistPath := Status()
	if !installed {
		return "", false
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return "", false
	}
	dict, derr := decodePlistDict(data)
	if derr != nil {
		return "", false
	}
	for _, key := range []string{"StandardErrorPath", "StandardOutPath"} {
		if s, ok := dict[key].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// PogodLogPathForReport renders the installed path when there is one and the
// template's path otherwise, alongside which of the two it is. Callers that
// print a path to a human owe them that distinction: "what the job names" and
// "what this build would install" are different claims and have already been
// confused on this box.
func PogodLogPathForReport() string {
	if p, ok := InstalledLogPath(); ok {
		return p
	}
	return fmt.Sprintf("%s (no installed job names a log; this is the default this build would install)", PogodLogPath())
}
