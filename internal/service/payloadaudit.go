package service

// The PAYLOAD half of the launchd activation audit (mg-30f8).
//
// WHAT WAS MISSING, AND WHY IT WAS MISSING IN EXACTLY THIS SHAPE. launchagentaudit.go
// compares every managed job's INSTALLED PLIST against the plist this build renders.
// It says nothing about the file that plist POINTS AT. Three of the four managed jobs
// run a shell script that `install-*` COPIES into ~/.pogo/bin, and a copy is not
// refreshed by a merge — so the plist can be byte-perfect, the job can fire on time,
// and the program it executes can be three weeks old.
//
// That is not hypothetical. Measured on the reference box 2026-09-08, with
// `com.pogo.deploy`'s plist reporting `ok` from the audit next door:
//
//	~/.pogo/bin/pogo-deploy.sh    Aug 19   1191 lines differ from scripts/launchd/pogo-deploy.sh
//	~/.pogo/bin/net-control.sh    Aug 19    352 lines differ from scripts/lib/net-control.sh
//	~/.pogo/bin/pogo-recovery.sh  Jul 10     99 lines differ from scripts/launchd/pogo-recovery.sh
//
// The deployed runner still EXECUTED the blind `pgrep -P` child walk that mg-19e4
// replaced with a `ps` walk three weeks earlier, and the deployed net-control still
// carried the completed-connect(2) verdict mg-a932 fixed. Both were reported fixed by
// every survey that read the repo, because in the repo they are.
//
// THE ASYMMETRY IS THE ROOT CAUSE, not the staleness. A plist is RENDERED from a Go
// template with this build's constants bound in, so the expectation travels inside the
// binary and the comparison can be made on any box, from any directory. A payload
// script is COPIED from the filesystem, so the expectation is a path — and a path that
// does not resolve is how a subject stops being audited without anyone deciding it
// should be. Hence the source path being named in every verdict below, and hence
// "could not find the source" being UNKNOWN rather than a quiet skip.
//
// BYTE EQUALITY IS THE PREDICATE; THE TICKET LIST IS ONLY THE DESCRIPTION. Same
// predicate as the plist audit, for the same reason (mg-de0c: byte equality cannot
// classify WHAT drifted, and choosing an installer off a boolean is choosing it
// without reading the difference). What is added here is a DESCRIPTION of the gap —
// the mg-XXXX ids named in the source and absent from the installed copy — because
// "1191 lines differ" sends a reader to a diff and "mg-19e4, mg-a854, mg-5c4a are not
// in the running copy" sends them to the fixes.
//
// That list is a LOWER BOUND and is labelled as one everywhere it renders. A fix that
// left no id comment in the file is invisible to it: mg-769a's signal-stage message is
// exactly such a fix one file over, and counting it as absent-because-unnamed is how
// this ticket came to assert it was undeployed when it never lived in that file at
// all. The list never sets the verdict; the bytes do.
//
// AND THE COUNT IS NOT THE INSTRUMENT EITHER. `grep -c 'pgrep -P'` returns 2 for the
// fixed source and 1 for the broken installed copy, because mg-19e4's fix is a
// PROHIBITION and a prohibition fix ADDS occurrences of the string it prohibits. Set
// difference over ids is position-free and does not have that failure mode; a
// frequency comparison over the prohibited text does, and reverses.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The payload states. Deliberately the same four words as LaunchAgent* next door plus
// one, so a reader who has learned one vocabulary has learned both — and so that
// "could not compare" reads as NOT CHECKED on both surfaces rather than as clean on
// one of them.
const (
	PayloadOK      = "ok"      // installed, and byte-identical to the source this build would install
	PayloadStale   = "stale"   // installed, and re-running the installer would change it
	PayloadAbsent  = "absent"  // no file at the install path, and no plist pointing at it either
	PayloadOrphan  = "orphan"  // the plist IS installed and the program it names does not exist
	PayloadUnknown = "unknown" // could not locate the source, or could not read one side — NOT CHECKED
)

// PayloadScriptAudit is one installed script compared against its source.
type PayloadScriptAudit struct {
	// Label is the launchd job whose plist names this file. Two payloads can share
	// one label: com.pogo.deploy installs the runner AND the net-control library it
	// sources, and a report that named only the runner would have called the box
	// clean while half of what the nightly executes was stale.
	Label string
	// Name is the basename, which is what a person greps a log for.
	Name string
	// Path is where the installer puts it and where the plist points.
	Path string
	// Source is the file actually compared against, or "" when none was found. It is
	// carried into every rendering: "matches this build" over an unnamed source is
	// the claim this whole package exists to stop anyone making.
	Source string
	// SourceFallback is true when Source came from the nightly checkout rather than
	// from a tree beside this binary — see payloadSourceOrFallback. It changes what
	// the verdict is worth, so it is said out loud rather than inferred from a path.
	SourceFallback bool
	// Status is one of the Payload* constants above.
	Status string
	// InstalledMod is the mtime of the installed copy, zero when there is none. The
	// age is the one number that reads correctly with no diff to hand.
	InstalledMod time.Time
	// InstalledLines / SourceLines are the two sizes. Not a drift predicate — a
	// same-length edit drifts too — but the cheapest honest scale for the gap.
	InstalledLines int
	SourceLines    int
	// MissingIDs are the mg-XXXX work items named in the source and absent from the
	// installed copy: a LOWER BOUND on the fixes that are not running. Never empty
	// as evidence of anything — see the file header.
	MissingIDs []string
	// Remedy is the command a HUMAN runs. Advisory, exactly as on LaunchAgentAudit:
	// nothing in this repo executes it (mg-de0c).
	Remedy string
	Detail string
}

// managedPayloadScript binds an installed payload to the three things the audit needs:
// where the installed copy lives, where this build's copy of it lives, and what an
// operator runs to reconcile them.
//
// Adding a launchd job that ships a script means adding a row here as well as in
// managedLaunchAgents(). The two registries are separate because they answer different
// questions and a job can be clean in one and rotten in the other — which is precisely
// the state the reference box was in when this file was written, and the state a single
// merged registry would have averaged away.
//
// com.pogo.daemon has NO row and that is a decision, not an omission: its program is
// the pogod binary, and binary drift is already answered by pogo-self-deploy's
// three-way running/installed/main revision check, which reads a revision stamp rather
// than comparing bytes. A byte comparison against a 28MB build artifact would report
// drift on every rebuild of identical source.
type managedPayloadScript struct {
	Label      string
	Name       string
	Path       func() string
	Source     func() (string, error)
	PlistPath  func() string
	Remedy     string
	SourceNote string
}

func managedPayloadScripts() []managedPayloadScript {
	return []managedPayloadScript{
		{
			Label:      deployLabel,
			Name:       "pogo-deploy.sh",
			Path:       deployScriptInstallPath,
			Source:     findDeployScriptSource,
			PlistPath:  deployPlistPath,
			Remedy:     "pogo service install-deploy",
			SourceNote: "scripts/launchd/pogo-deploy.sh",
		},
		{
			Label:      deployLabel,
			Name:       "net-control.sh",
			Path:       netControlInstallPath,
			Source:     findNetControlSource,
			PlistPath:  deployPlistPath,
			Remedy:     "pogo service install-deploy",
			SourceNote: "scripts/lib/net-control.sh",
		},
		{
			Label:      recoveryLabel,
			Name:       "pogo-recovery.sh",
			Path:       recoveryScriptInstallPath,
			Source:     findRecoveryScriptSource,
			PlistPath:  recoveryPlistPath,
			Remedy:     "pogo service install-recovery",
			SourceNote: "scripts/launchd/pogo-recovery.sh",
		},
		{
			Label:      reclaimLabel,
			Name:       "pogo-reclaim.sh",
			Path:       reclaimScriptInstallPath,
			Source:     findReclaimScriptSource,
			PlistPath:  reclaimPlistPath,
			Remedy:     "pogo service install-reclaim",
			SourceNote: "scripts/launchd/pogo-reclaim.sh",
		},
	}
}

// AuditPayloadScripts compares every managed job's INSTALLED payload script against the
// copy this build would install.
//
// Returns nil on non-darwin, for the same reason AuditLaunchAgents does: an empty slice
// is indistinguishable from "checked, all clean", and on this subject that confusion is
// the defect. Callers must treat nil on a supported platform as NOT CHECKED.
func AuditPayloadScripts() []PayloadScriptAudit {
	if !LaunchAgentsSupported() {
		return nil
	}
	scripts := managedPayloadScripts()
	out := make([]PayloadScriptAudit, 0, len(scripts))
	for _, s := range scripts {
		src, fallback, srcErr := payloadSourceOrFallback(s)
		plistInstalled := false
		if _, err := os.Stat(s.PlistPath()); err == nil {
			plistInstalled = true
		}
		a := auditPayloadScript(s.Label, s.Name, s.Path(), src, srcErr, s.Remedy, s.SourceNote, plistInstalled)
		a.SourceFallback = fallback
		// Only where a comparison actually happened. An ABSENT or ORPHAN row is a
		// statement about the installed side, and naming which source it was not
		// compared against is noise on a verdict the source had no part in.
		if fallback && (a.Status == PayloadStale || a.Status == PayloadOK) {
			a.Detail += fmt.Sprintf(" (compared against the NIGHTLY CHECKOUT %s, not a tree beside this binary: %s. That checkout is synced at 03:00 and can be AHEAD of this build, so a drift it reports is real and a match it reports is only as current as the sync.)",
				deploySrcDir(), src)
		}
		out = append(out, a)
	}
	return out
}

// payloadSourceOrFallback resolves the copy of a payload script to compare against,
// falling back to the nightly checkout ($POGO_DEPLOY_SRC, ~/.pogo/deploy-src) when the
// installer's own finder comes up empty.
//
// WHY THE AUDIT RESOLVES DIFFERENTLY FROM THE INSTALLER, given deploy.go argues that a
// divergence in how a file is FOUND is how one silently stops being shipped. Because the
// two have different requirements, and this one was MEASURED before it was written.
//
// The installer must install what THIS BUILD ships; a wider search there could ship a
// file that does not match the binary, which is mg-b9e7's trap. The audit must be able
// to ANSWER on a box where nothing is beside the binary — and without this fallback it
// could not. Measured 2026-09-08 on the reference box: run from a directory that is not
// a checkout, every payload row came back NOT CHECKED while three of them were in fact
// stale. `pogo-self-deploy`'s report_activation deliberately does not mail on UNKNOWN
// (a job left uninstalled would otherwise alert nightly forever), so the nightly would
// have found this drift and then silenced it — the detector reproducing the defect it
// was written to report, one layer down.
//
// The price is that deploy-src is a lagging snapshot of main, not this build's tree, so
// a comparison against it is worth something different. That is not hidden: the source
// path is on every row, SourceFallback is on the struct, and the sentence appended above
// says which direction the uncertainty runs.
func payloadSourceOrFallback(s managedPayloadScript) (path string, fallback bool, err error) {
	if p, e := s.Source(); e == nil {
		return p, false, nil
	} else {
		err = e
	}
	if s.SourceNote == "" {
		return "", false, err
	}
	cand := filepath.Join(deploySrcDir(), filepath.FromSlash(s.SourceNote))
	if _, e := os.Stat(cand); e == nil {
		if abs, e := filepath.Abs(cand); e == nil {
			return abs, true, nil
		}
		return cand, true, nil
	}
	return "", false, fmt.Errorf("%w; and not in the nightly checkout either (%s)", err, cand)
}

// auditPayloadScript is the whole comparison as a function of two paths, so every state
// is reachable from a temp dir on any platform. Same discipline as auditLaunchAgent one
// file over: an audit whose own correctness can only be demonstrated on the machine that
// has the bug is an audit nobody can change safely.
//
// plistInstalled is passed in rather than derived so the ORPHAN state — a loaded job
// whose ProgramArguments name a file that is not there — is testable without a
// LaunchAgents directory.
func auditPayloadScript(label, name, path, source string, sourceErr error, remedy, sourceNote string, plistInstalled bool) PayloadScriptAudit {
	res := PayloadScriptAudit{Label: label, Name: name, Path: path, Source: source, Remedy: remedy}

	installed, instErr := os.ReadFile(path)
	if instErr == nil {
		res.InstalledLines = countLines(installed)
		if fi, err := os.Stat(path); err == nil {
			res.InstalledMod = fi.ModTime()
		}
	}

	// The source is resolved first because its absence disqualifies every other
	// verdict. A payload whose source could not be found has not been compared
	// against anything, and the one thing this file must never do is let that read
	// as a match.
	if sourceErr != nil || source == "" {
		res.Status = PayloadUnknown
		reason := "no source found"
		if sourceErr != nil {
			reason = sourceErr.Error()
		}
		res.Detail = fmt.Sprintf("NOT CHECKED: this build could not locate its copy of %s (%s), so %s was not compared against anything. Unlike a plist — which is rendered from a Go template and therefore comparable from any directory — a payload script is COPIED from the filesystem, so the expectation is a PATH, and a path that does not resolve is how a subject stops being audited with nobody deciding it should be. Run `%s` from a checkout, or set the matching POGO_*_SCRIPT override, to make this comparable",
			sourceNote, reason, path, remedy)
		return res
	}

	srcBytes, srcErr := os.ReadFile(source)
	if srcErr != nil {
		res.Status = PayloadUnknown
		res.Detail = fmt.Sprintf("NOT CHECKED: the source at %s could not be read (%v), so %s was not compared against anything", source, srcErr, path)
		return res
	}
	res.SourceLines = countLines(srcBytes)

	switch {
	case os.IsNotExist(instErr) && plistInstalled:
		res.Status = PayloadOrphan
		res.Detail = fmt.Sprintf("ORPHANED JOB: %s is installed and names %s as its program, and there is NO FILE THERE. launchd will fire the job on schedule and the exec will fail; the failure is not a pogo log line and nothing downstream observes it. Run `%s`",
			label, path, remedy)
		return res
	case os.IsNotExist(instErr):
		res.Status = PayloadAbsent
		res.Detail = fmt.Sprintf("not installed: no file at %s, and %s is not installed either, so this is consistent rather than broken. This audit compares an installed script against the code that ships it; it cannot tell a job deliberately left uninstalled from one whose install never ran (`%s` installs both)",
			path, label, remedy)
		return res
	case instErr != nil:
		res.Status = PayloadUnknown
		res.Detail = fmt.Sprintf("NOT CHECKED: %s could not be read (%v)", path, instErr)
		return res
	}

	if bytes.Equal(installed, srcBytes) {
		res.Status = PayloadOK
		res.Detail = fmt.Sprintf("installed copy at %s is byte-identical to %s (%d lines)", path, source, res.SourceLines)
		return res
	}

	res.Status = PayloadStale
	res.MissingIDs = missingWorkItemIDs(srcBytes, installed)
	res.Detail = fmt.Sprintf("THE FILE %s EXECUTES IS NOT THE FILE THIS BUILD SHIPS: %s (%d lines, installed %s) differs from %s (%d lines). A merge does not refresh a copied file, so every fix merged since that install is INERT on this box while every source-only survey reports it fixed. %s Run `%s`",
		label, path, res.InstalledLines, installedAgeNote(res.InstalledMod), source, res.SourceLines,
		missingIDsNote(res.MissingIDs), remedy)
	return res
}

// countLines is the cheapest honest scale for two files. Counts newline-terminated
// lines the way `wc -l` does, so a number here can be checked against a shell.
func countLines(b []byte) int { return bytes.Count(b, []byte{'\n'}) }

// installedAgeNote phrases the mtime, or says it is unknown rather than printing a zero
// time that reads as 1 January year 1.
func installedAgeNote(mod time.Time) string {
	if mod.IsZero() {
		return "install time unknown"
	}
	return mod.Format("2006-01-02")
}

// workItemID matches the mg-XXXX ids this repo stamps into the files a fix touches.
var workItemID = regexp.MustCompile(`mg-[0-9a-f]{4}`)

// missingWorkItemIDs is the set of work items named in the source and absent from the
// installed copy — a LOWER BOUND on the fixes that are not running.
//
// Set difference rather than frequency, and that is load-bearing. `grep -c 'pgrep -P'`
// over this exact pair returns 2 for the FIXED source and 1 for the BROKEN installed
// copy, because mg-19e4's fix is a prohibition and a prohibition fix ADDS occurrences
// of the string it prohibits. Anyone reaching for a count here gets the answer
// backwards; presence of an id does not have that property.
func missingWorkItemIDs(source, installed []byte) []string {
	have := map[string]bool{}
	for _, id := range workItemID.FindAllString(string(installed), -1) {
		have[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range workItemID.FindAllString(string(source), -1) {
		if have[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// missingIDsNote renders the id list with the caveat attached to it, never separately.
// The caveat is the part that travels badly: a bare list of five ids reads as "five
// fixes are missing", and the true claim is "at least these five, and a fix that left
// no id comment does not appear here at all".
func missingIDsNote(ids []string) string {
	if len(ids) == 0 {
		return "No work-item id named in the source is absent from the installed copy, which is NOT a report that the two agree — the bytes above already say they do not, and a fix that left no `mg-` comment is invisible to this list."
	}
	return fmt.Sprintf("AT LEAST %d work item(s) fixed upstream are not in the running copy: %s. That is a LOWER BOUND — a fix that left no `mg-` comment in the file does not appear here.",
		len(ids), strings.Join(ids, ", "))
}
