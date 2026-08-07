package service

import (
	"strings"
	"testing"
)

// TestDeployLogPathIsTheFileThePlistWritesTo pins the one agreement the
// did-not-run detector (mg-2416) rests on: the path it READS must be the path
// launchd WRITES.
//
// If they diverge, the detector opens a file that is empty or absent — and an
// empty deploy log is indistinguishable from a deploy that never fired, so a
// path typo would not break the alarm, it would make it fire constantly on a
// perfectly healthy host, which gets it filtered and then ignored. The reverse
// typo is worse: a detector pointed at a file some other process keeps fresh
// would go permanently quiet.
//
// This repo has already shipped one file under two paths and had a grep return
// empty against the wrong one (mg-f766), so the agreement is asserted rather
// than assumed.
func TestDeployLogPathIsTheFileThePlistWritesTo(t *testing.T) {
	plist, data, err := renderDeployPlist()
	if err != nil {
		t.Fatalf("renderDeployPlist: %v", err)
	}

	want := DeployLogPath()
	if want == "" {
		t.Fatal("DeployLogPath() is empty")
	}

	// Both redirections, because stdout and stderr are pointed separately and a
	// detector reading only one of them would miss half the runner's output.
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if !strings.Contains(plist, "<key>"+key+"</key>\n    <string>"+want+"</string>") {
			t.Errorf("plist %s does not point at DeployLogPath() = %s\n\nrendered:\n%s", key, want, plist)
		}
	}

	// And the derivation itself, so a future refactor that keeps the strings
	// matching by coincidence still has to keep them matching by construction.
	if got := data.LogDir + "/" + data.LogName; got != want {
		t.Errorf("plist template data renders %s, DeployLogPath() = %s", got, want)
	}
}
