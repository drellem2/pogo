package sourcewatch

// Discovery: which jobs are CONSUMERS, and which directory is the SOURCE.
//
// Nothing in a plist says "this variable is my input". The audit therefore has
// to decide, from structure alone, which of a job's environment variables names
// the data it consumes — and it has to decide conservatively, because a
// detector that reports on POGO_HOME or STATE_DIR alongside MAIL_DIR is one
// whose findings get skimmed past.
//
// TWO ADMISSION RULES, either of which is sufficient. Both are structural, so
// neither mentions mail, `human`, `daniel`, or any path that today's cutover
// will change:
//
//  1. A DIVERGENT BINDING. Two jobs run the SAME program and declare the SAME
//     variable with DIFFERENT directories. Two instances of one program that
//     differ in a directory is that directory being a per-instance data
//     binding — which is precisely what com.pogo.notify and com.pogo.deadman
//     are, both running poll-mail.sh and disagreeing only about MAIL_DIR.
//
//  2. A SIBLING FAMILY. The directory is one of several like-shaped
//     directories: same parent-of-parent, same basename, as in
//     <store>/<stream>/new. A box that is one of a family of boxes has, by
//     construction, somewhere else the same data could be arriving — which is
//     what makes the comparison meaningful. This rule is what keeps the audit
//     working when a cohort collapses to ONE consumer: if the deadman were
//     retired tomorrow, com.pogo.notify would still be compared against its
//     sibling boxes rather than falling silent along with the peer that used to
//     convict it.
//
// Rule 2 is structural and therefore blind to meaning, so one further screen
// sits in front of both rules: a variable that names the process's ENVIRONMENT
// rather than its input is never a source (standardProcessEnv). That screen is
// a list of standard variable NAMES, so it does not decay when the boxes move.
//
// WHAT THE RULES EXCLUDE, checked against this machine's real plists on
// 2026-08-09 rather than reasoned about: PATH (holds a list separator), HOME
// (named environment, and in any case its grandparent is the filesystem root),
// POGO_HOME=$HOME (grandparent is the filesystem root — rule 2 declines to glob
// /*/daniel, and rule 1 does not apply across different programs),
// POGO_HOME=~/.pogo and POGO_DEPLOY_SRC and POGO_PLUGIN_PATH (no sibling of the
// same basename, no same-program peer), STATE_DIR (declared by one job only, no
// siblings), and every non-path value — POLL_INTERVAL, MIN_AGE_SECONDS,
// TITLE_PREFIX, HEARTBEAT_JOB. What survives is MAIL_DIR on the two poll-mail.sh
// jobs, by both rules independently.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/service"
)

// record is one plist as read, before any admission decision.
type record struct {
	label     string
	program   string
	env       map[string]string
	plistPath string
}

// Discover reads every plist in dir and returns the consumer/source bindings
// the admission rules admit.
//
// An unreadable directory is an error, never an empty slice: this package's
// founding bug is a quiet reading that means "I could not look".
func Discover(dir string) ([]Consumer, error) {
	if dir == "" {
		return nil, os.ErrNotExist
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var records []record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".plist") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			// One unreadable plist is not a reason to abandon the sweep, and it
			// is also not a consumer we can judge. It contributes nothing.
			continue
		}
		if r, ok := parseRecord(data, filepath.Join(dir, e.Name())); ok {
			records = append(records, r)
		}
	}

	var out []Consumer
	for _, r := range records {
		keys := make([]string, 0, len(r.env))
		for k := range r.env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := r.env[k]
			if !isCandidateDir(v) {
				continue
			}
			if !admit(r, k, v, records) {
				continue
			}
			out = append(out, Consumer{
				Label:     r.label,
				Program:   r.program,
				SourceKey: k,
				Source:    filepath.Clean(v),
				PlistPath: r.plistPath,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].SourceKey < out[j].SourceKey
	})
	return out, nil
}

// parseRecord pulls the three things admission needs out of one plist.
func parseRecord(data []byte, path string) (record, bool) {
	doc, err := service.DecodePlistDict(data)
	if err != nil {
		return record{}, false
	}
	r := record{plistPath: path, env: map[string]string{}}
	if s, ok := doc["Label"].(string); ok {
		r.label = strings.TrimSpace(s)
	}
	if r.label == "" {
		r.label = strings.TrimSuffix(filepath.Base(path), ".plist")
	}
	if args, ok := doc["ProgramArguments"].([]any); ok && len(args) > 0 {
		if s, ok := args[0].(string); ok {
			r.program = strings.TrimSpace(s)
		}
	}
	if r.program == "" {
		if s, ok := doc["Program"].(string); ok {
			r.program = strings.TrimSpace(s)
		}
	}
	if envs, ok := doc["EnvironmentVariables"].(map[string]any); ok {
		for k, v := range envs {
			if s, ok := v.(string); ok {
				r.env[k] = strings.TrimSpace(s)
			}
		}
	}
	return r, true
}

// isCandidateDir rejects the values that cannot be a source directory at all.
//
// A value that does not exist yet is still a candidate — a consumer polling a
// path that is not there is one of the findings (StatusMissing), and screening
// it out here would hide it. A value that exists and is a FILE is not: a config
// path is not a stream.
func isCandidateDir(v string) bool {
	if v == "" || !filepath.IsAbs(v) {
		return false
	}
	if strings.ContainsRune(v, os.PathListSeparator) {
		return false // a search path, not a directory
	}
	info, err := os.Stat(v)
	if err == nil && !info.IsDir() {
		return false
	}
	return true
}

// standardProcessEnv are variables that describe the process's ENVIRONMENT
// rather than the data it consumes. A job's HOME is not its input, whatever
// shape the directory happens to have.
//
// This is a list of standard variable NAMES, not of paths, which is what keeps
// it from being the decaying literal this ticket forbids: HOME will still be
// HOME after the cutover completes, and after the next one. It was added
// because the sibling rule below is structural and therefore blind — a
// TestDiscover.../001 temp directory has siblings named 001 too, and on that
// evidence the audit admitted a deploy job's HOME as a data source. The rule
// caught it before this shipped; the fix is to say what those names are.
var standardProcessEnv = map[string]bool{
	"PATH": true, "HOME": true, "PWD": true, "OLDPWD": true, "SHELL": true,
	"USER": true, "LOGNAME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
	"XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true,
}

// admit applies the two rules. See the file comment for why they are these two.
func admit(r record, key, value string, all []record) bool {
	if standardProcessEnv[key] {
		return false
	}
	return hasDivergentBinding(r, key, value, all) || len(siblingDirs(value)) > 0
}

// hasDivergentBinding is rule 1: another instance of the same program declares
// the same variable, pointed somewhere else.
func hasDivergentBinding(r record, key, value string, all []record) bool {
	if r.program == "" {
		return false
	}
	for _, other := range all {
		if other.plistPath == r.plistPath || other.program != r.program {
			continue
		}
		if v, ok := other.env[key]; ok && filepath.Clean(v) != filepath.Clean(value) && isCandidateDir(v) {
			return true
		}
	}
	return false
}

// siblingDirs is rule 2: the like-shaped directories beside this one —
// <grandparent>/*/<basename>, excluding the directory itself.
//
// A grandparent at the filesystem root is declined. /*/daniel is not a family
// of boxes, it is every user account on the machine, and admitting HOME as a
// data source would bury the findings that matter under the ones that do not.
//
// ONE READDIR OF THE GRANDPARENT, then a stat per candidate — never an open of
// a sibling. This used to be a filepath.Glob of <grand>/*/<basename>, which
// reads as the sentence above but does not behave like it: Go's glob does not
// shortcut a meta-free final component, so for every entry of <grand> it does
// os.Open + Readdirnames(-1) and only then matches names. It read ~200
// directories in full to test for the presence of one name in each.
//
// That is not merely wasteful. On macOS ~/Desktop, ~/Documents and ~/Downloads
// are TCC-gated: stat succeeds, open BLOCKS on a consent prompt, and in a
// headless agent nobody answers it. STATE_DIR=~/.pogo/reminders-deadman has
// grandparent ~, so `pogo doctor --check` — the command this repo documents as
// the first-line health check, and the first thing the doctor crew prompt runs
// — wedged in open(2) forever and printed zero bytes. os.Stat on the JOINED
// path resolves a name through a directory without opening it, so a gated
// sibling returns an error instead of never returning at all.
func siblingDirs(value string) []string {
	dir := filepath.Clean(value)
	parent := filepath.Dir(dir)
	grand := filepath.Dir(parent)
	if grand == parent || grand == string(filepath.Separator) || grand == "." {
		return nil
	}
	entries, err := os.ReadDir(grand)
	if err != nil {
		return nil
	}
	base := filepath.Base(dir)
	var out []string
	for _, e := range entries {
		m := filepath.Join(grand, e.Name(), base)
		if m == dir {
			continue
		}
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// peersFor is the comparison population for one consumer: the sibling boxes
// beside its source, plus any other consumer's source bound to the same
// variable name.
//
// The second half matters when the sources are NOT siblings — a consumer
// re-pointed at a directory somewhere else entirely still has to be compared
// against the box its peer is reading, or the re-point is invisible in exactly
// the way this package is built to prevent.
func peersFor(c Consumer, all []Consumer) []string {
	seen := map[string]bool{c.Source: true}
	var out []string
	add := func(d string) {
		d = filepath.Clean(d)
		if seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, s := range siblingDirs(c.Source) {
		add(s)
	}
	for _, other := range all {
		if other.SourceKey == c.SourceKey {
			add(other.Source)
		}
	}
	sort.Strings(out)
	return out
}
