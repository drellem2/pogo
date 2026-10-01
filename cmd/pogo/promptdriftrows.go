package main

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// doctorRow is one rendered `pogo doctor` checklist row. Status is "pass",
// "warn" or "fail"; only "fail" turns `doctor --check` red.
type doctorRow struct {
	Status string
	Name   string
	Detail string
}

// promptDriftRows renders a non-empty agent.CheckPromptDrift result as doctor
// rows. It lives outside the doctor command so the STATUS of each row can be
// pinned by a test: the body-differs-from-stamp row must stay a warn, and a
// regression back to fail is otherwise invisible until a deliberately edited
// prompt leaves --check red with no remedy (mg-ae059, drellem2/pogo#125).
func promptDriftRows(drift []agent.PromptDrift) []doctorRow {
	var rows []doctorRow
	// Two states, two remedies. Install-fixable drift
	// (missing/unstamped/stale) is cured by re-running
	// install. An "edited" canonical is NOT: the installer
	// declines to clobber the local edit and only writes
	// <name>.dist, so advising install there would exit 0 and
	// change nothing — a false "I ran the fix" (mg-04ab).
	// Never fold the two into one remedy string.
	var installable, edited []agent.PromptDrift
	for _, d := range drift {
		if agent.DriftInstallFixable(d.Reason) {
			installable = append(installable, d)
		} else {
			edited = append(edited, d)
		}
	}
	if len(installable) > 0 {
		names := make([]string, 0, len(installable))
		for _, d := range installable {
			names = append(names, fmt.Sprintf("%s (%s)", d.Path, d.Reason))
		}
		// Name the owner and the cadence, not just the manual
		// remedy. Act 3 is NOT a step somebody has to remember:
		// pogod's boot runs InstallPrompts before it auto-starts
		// any crew, so the nightly deploy's kickstart installs
		// prompts every night. Drift showing up HERE therefore
		// means the binary embeds something newer than the last
		// restart propagated — a restart is the standing fix and
		// the CLI call is the way to not wait for one. mg-b6bd
		// was filed believing nothing installed prompts at all;
		// a remedy that names only the manual command is how a
		// reader arrives at that belief.
		rows = append(rows, doctorRow{"fail", "agent prompts up-to-date",
			fmt.Sprintf("%d prompt(s) drifted from embedded source: %s — run 'pogo agent prompt install', then restart affected agents. (pogod's boot installs prompts on every restart, so the nightly deploy clears this by itself; running it now is how you avoid waiting for that. `pogo events list --type=prompt_refresh` is the record of what each restart installed.)",
				len(installable), strings.Join(names, ", "))})
	}
	// An edited body under an UNCHANGED embed stamp is its own
	// row (drellem2/pogo#125): install skips such a file
	// outright and writes no .dist, so the reconcile-against-
	// .dist remedy above would send the reader after a sidecar
	// that will never appear.
	var reconcile, rewritten []string
	for _, d := range edited {
		if d.EmbedCurrent {
			rewritten = append(rewritten, d.Path)
		} else {
			reconcile = append(reconcile, fmt.Sprintf("%s (reconcile against %s.dist)", d.Path, d.Path))
		}
	}
	if len(reconcile) > 0 {
		rows = append(rows, doctorRow{"fail", "agent prompts up-to-date (local edits)",
			fmt.Sprintf("%d hand-edited prompt(s) diverged from the embedded source: %s — 'pogo agent prompt install' will NOT overwrite your edits; it writes the shipped copy to <name>.dist. Reconcile each canonical against its .dist sidecar (run install first if the .dist is absent), then restart affected agents",
				len(reconcile), strings.Join(reconcile, ", "))})
	}
	// A WARN, not a fail: a deliberate in-place edit is a
	// legitimate state ([prompt_edit] offers "keep the edit"),
	// and on an org-templated host every customized prompt is in
	// it. Failing here would leave --check red with no remedy
	// short of discarding the edit — install skips the file.
	// What #125 needs is that doctor stop calling it up-to-date.
	if len(rewritten) > 0 {
		rows = append(rows, doctorRow{"warn", "agent prompts up-to-date (body differs from stamp)",
			fmt.Sprintf("%d prompt(s) carry a stamp for this binary's embed but a body that is not it: %s — the file was rewritten in place after install (a hand-edit, or an org template's own copy). 'pogo agent prompt install' skips these and writes no .dist. If the body is intended, nothing is wrong and no command is needed; `pogo check-prompt-edits` reports each file. To restore the shipped text for ONE file, move that file aside and run 'pogo agent prompt install' — not --force, which overwrites every edited prompt at once",
				len(rewritten), strings.Join(rewritten, ", "))})
	}
	return rows
}
