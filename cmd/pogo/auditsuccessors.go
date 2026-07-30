package main

// The "audit successors" line in `pogo doctor --check` (mg-28b7).
//
// WHY IT REPORTS HERE. The scope note on the ticket was "prefer somewhere a
// person acts on rather than a maildir that already carries hundreds of unread
// notices". `pogo doctor --check` is a checklist someone reads on purpose, it
// is what the doctor crew agent runs first, and it already carries the other
// store-level detector (the stale-claim count). A mail notice would have been
// cheaper to write and would have landed in the pile this detector exists to
// avoid becoming.
//
// WHY IT WARNS AND NEVER FAILS. `fail` sets doctor's exit code, and a nonzero
// `pogo doctor --check` is a signal that something on this host is broken. An
// unanswered audit is not a broken host — it is work someone owes. Escalating it
// to a failed health check would put an unrelated person's ticket in the path of
// anyone scripting against doctor's exit status, which is the "detector grows
// into a gate" failure the ticket names, arriving through the exit code instead
// of through a refusal.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/auditwatch"
	"github.com/drellem2/pogo/internal/config"
)

// auditSuccessorCheckName is the doctor checklist row this renders on.
const auditSuccessorCheckName = "audit successors"

// auditSuccessorLine renders one doctor check row from a scan.
//
// It returns the status ("pass" or "warn") and the detail. Four states, and the
// distinctions between them are the point of the whole exercise:
//
//	NOT CONFIGURED   the detector is inert. Says so, out loud, on every run.
//	NOT CHECKED      the store could not be read. Also says so.
//	NOTHING TO SAY   every merged audit was answered, with the population.
//	SILENT AUDITS    the failures, named, oldest first.
//
// The first two are `pass` because nothing is wrong with the host, but they must
// never render as the third. A detector whose failure mode is silence cannot
// report its own silence as a clean bill of health — that is the exact shape of
// the defect it was built to catch, one level up.
func auditSuccessorLine(rep auditwatch.Report, scanErr error, cfg config.AuditSuccessorConfig, now time.Time) (status, detail string) {
	if scanErr != nil {
		return "pass", "NOT CHECKED: the macguffin store could not be read: " + scanErr.Error() +
			". This is not a report that every merged audit was answered"
	}
	if !rep.Enabled {
		return "pass", "not configured — [audit_successor] names " + missingConfigPart(cfg) +
			", so no merged audit is checked for a successor. This is not a report that every merged audit was answered"
	}

	window := formatWindow(rep.Window)
	population := fmt.Sprintf("%d merged audit(s) examined: %d answered by a successor, %d by a recorded clean verdict, %d still inside the %s window, %d with no recorded completion time",
		rep.Merged, rep.Answered, rep.CleanVerdict, rep.Waiting, window, rep.Undated)

	if len(rep.Silent) == 0 {
		return "pass", "no merged audit has gone unanswered past " + window + " — " + population
	}

	names := make([]string, 0, len(rep.Silent))
	for _, s := range rep.Silent {
		// .UTC() before formatting, because the trailing Z in the layout is a
		// LITERAL and not a zone specifier. Without the conversion this printed
		// the host's local clock and labelled it Z — an hour off under BST, and
		// wrong in a way a reader cross-checking against `created:` frontmatter
		// (which mg writes in real UTC) would have to discover for themselves.
		names = append(names, fmt.Sprintf("%s (silent %s, merged %s)",
			s.ID, formatWindow(s.Silence.Truncate(time.Minute)), s.MergedAt.UTC().Format("2006-01-02 15:04Z")))
	}
	return "warn", fmt.Sprintf(
		"%d merged audit(s) answered by NOTHING after %s: %s. Read each one and file a repair ticket referencing it (`mg new --depends=<audit-id> --tags=<audit-id>-followup ...`), or record a clean verdict by tagging the audit %s. This is a DETECTOR, not a gate: it blocks nothing, and a clean verdict is an artifact you can produce without reading the audit — it silences this line either way. %s",
		len(rep.Silent), window, strings.Join(names, ", "), cleanVerdictAdvice(cfg), population)
}

// missingConfigPart names which half of the configuration is absent, because
// "not configured" with two required keys sends a reader to the docs to find out
// which one they left out.
func missingConfigPart(cfg config.AuditSuccessorConfig) string {
	switch {
	case len(cfg.Repos) == 0 && len(cfg.AuditTags) == 0:
		return "no repos and no audit_tags"
	case len(cfg.Repos) == 0:
		return "no repos"
	default:
		return "no audit_tags"
	}
}

// cleanVerdictAdvice renders the clean-verdict half of the remedy. A deployment
// that configured no clean-verdict tag has no such option, and the line says so
// rather than naming a tag that would do nothing.
func cleanVerdictAdvice(cfg config.AuditSuccessorConfig) string {
	tags := make([]string, 0, len(cfg.CleanVerdictTags))
	for _, t := range cfg.CleanVerdictTags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return "(no clean_verdict_tags are configured, so a successor ticket is the only way to answer an audit here)"
	}
	sort.Strings(tags)
	return "`" + strings.Join(tags, "` or `") + "`"
}

// formatWindow renders a duration the way a person reading a checklist wants it:
// "4h0m0s" is what Go prints and is not what anyone says out loud.
func formatWindow(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}
