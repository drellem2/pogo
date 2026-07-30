// Package auditwatch detects audits that merged and were never answered.
//
// # It is a DETECTOR, not a gate
//
// Nothing in this package refuses anything. There is no caller at dispatch, no
// refusal string, and no path by which it can stop a merge. That is deliberate
// and is the whole shape of the thing:
//
//   - A GATE on this event would have to decide whether an audit's findings
//     WARRANT a successor. That is a judgement, not mechanically decidable, and
//     a gate that guessed it would block merges on a coin flip.
//   - What IS mechanically decidable is that NOTHING HAPPENED AT ALL — no
//     successor ticket, no recorded verdict, nothing referencing the audit
//     anywhere in the store. This measures only that.
//
// The failure mode being covered is silence, and a detector is the right shape
// for something whose failure mode is silence. Do not grow it into a refusal.
//
// # The residue it covers
//
// The dispatch-pairing gate (internal/agent/dispatchpairing.go) enforces that a
// paired audit EXISTS at dispatch. It states its own limit: it checks that a
// pair exists, never that it is any good, and an audit filed to satisfy it and
// then shelved unread satisfies it. That residue was covered by one agent
// remembering to read each audit as it merged — on 2026-07-30 that produced
// three successor tickets filed by hand, every one of which would otherwise
// have existed only in a commit message, where nothing acts on it.
//
// This needs nobody to remember. It fires on exactly the case the gate cannot
// see, from the store alone.
//
// # It also converts a filing race into a fallback
//
// Two duties bound to two events seconds apart — a dispatcher's successor duty
// at AUDIT-MERGE, a product owner's pre-file duty at FILE — and on 2026-07-30
// both fired and produced duplicate tickets. The agreed line is that the
// product owner files the repair and the dispatcher does not; this observes the
// silence if the owner does not. Neither party has to be fast.
//
// WHAT THAT LINE COSTS, AND WHAT THIS DOES NOT COVER: the non-filer is often the
// second reader and may hold findings the owner's ticket lacks (this happened —
// four findings, filed separately). This sees whether A successor exists, never
// whether it carries everything the audit found. Reading the audit and mailing
// what you find stays human and stays explicit.
//
// # Both limits, restated where the code is
//
//  1. A recorded clean verdict is an artifact someone can produce cheaply.
//     Tagging an unread audit clean silences this exactly as filing an unread
//     audit satisfies the pairing gate. See config.AuditSuccessorConfig.
//  2. It detects after the fact rather than preventing. By the time it fires the
//     audit has merged and the window has elapsed. It buys that the omission is
//     findable afterwards by someone who was not watching.
package auditwatch

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// SilentAudit is one merged audit that the window has closed on with nothing
// referencing it and no verdict recorded on it.
type SilentAudit struct {
	ID string `json:"id"`
	// Title is the audit's first heading, so a report can say WHICH audit went
	// unanswered without the reader opening the store.
	Title string `json:"title,omitempty"`
	Repo  string `json:"repo,omitempty"`
	// CoveringRepo is the configured [audit_successor] repos entry that put this
	// item in scope — named so a reader can see what selected it rather than
	// inferring the rule from the finding.
	CoveringRepo string `json:"covering_repo,omitempty"`
	// MergedAt is the completion instant, from the item's result.json mtime.
	MergedAt time.Time `json:"merged_at"`
	// Silence is how long it has been unanswered — now minus MergedAt, always
	// longer than the window.
	Silence time.Duration `json:"silence"`
}

// Report is the outcome of one scan. Every merged audit examined lands in
// exactly one of the counters or in Silent, so the numbers add up and a reader
// can tell "nothing to report" from "nothing checked".
type Report struct {
	// Enabled is false when the deployment configured no repos or no audit tag.
	// A disabled detector reports zeroes, which must never be read as a clean
	// store — the whole failure mode here is silence, so an unconfigured
	// detector has to be visibly unconfigured.
	Enabled bool          `json:"enabled"`
	Window  time.Duration `json:"window"`
	// Merged is how many covered audits were examined.
	Merged int `json:"merged"`
	// Answered is how many had at least one successor in the store.
	Answered int `json:"answered"`
	// CleanVerdict is how many recorded a clean verdict on themselves instead.
	// Counted separately from Answered because it is the weaker of the two
	// artifacts — see limit 1 — and folding it in would hide how much of a
	// clean report rests on it.
	CleanVerdict int `json:"clean_verdict"`
	// Waiting is how many merged too recently for the window to have closed.
	// Not a verdict either way, and reported so a small Silent count is not
	// mistaken for a small population.
	Waiting int `json:"waiting"`
	// Undated is how many are in done/ with no recorded completion time, so
	// their silence cannot be aged at all. This is the detector's own blind
	// spot and it is counted rather than folded into a verdict: an item with no
	// result.json is neither answered nor known-silent.
	Undated int `json:"undated"`
	// Silent are the failures, oldest silence first.
	Silent []SilentAudit `json:"silent,omitempty"`
}

// Scan reads the macguffin store under root and reports every covered audit
// that merged, outlived the window, and was answered by nothing.
//
// A store it cannot read is an ERROR, never an empty report. The difference
// between "no silent audits" and "I could not look" is the entire value of the
// thing, and a detector that reports the second as the first is worse than
// absent — it certifies a state it never observed. Callers must render the two
// differently.
//
// now is a parameter so a test can place the window wherever it needs it.
func Scan(root string, cfg config.AuditSuccessorConfig, now time.Time) (Report, error) {
	window := cfg.AuditWindow()
	rep := Report{Enabled: config.AuditSuccessorEnabled(cfg), Window: window}
	if !rep.Enabled || root == "" {
		return rep, nil
	}
	workRoot := filepath.Join(root, "work")

	// One walk, two uses: done/ supplies the audits, and all four statuses
	// supply the successor candidates.
	//
	// shelved/ and archive/ are excluded from BOTH, and the asymmetry matches
	// the pairing gate's (see pairCandidateStatuses). A shelved successor is a
	// dropped one, and counting it would let an audit be answered by abandoning
	// the answer. pending/ IS counted: a successor gated behind an unmet
	// `depends:` is filed, parked, and will run — that is an answer.
	items, err := workitem.ListAllFrom(workRoot, "available", "claimed", "done", "pending")
	if err != nil {
		return rep, err
	}

	for _, item := range items {
		if item.Status != "done" {
			continue
		}
		covering, isAudit := config.AuditItem(item.Repo, item.TagList(), cfg)
		if !isAudit {
			continue
		}
		rep.Merged++

		if config.AuditCleanVerdict(item.TagList(), cfg) {
			rep.CleanVerdict++
			continue
		}
		if hasSuccessor(items, item.ID) {
			rep.Answered++
			continue
		}
		// Answered by nothing. Now — and only now — does the clock matter.
		if item.CompletedAt.IsZero() {
			rep.Undated++
			continue
		}
		silence := now.Sub(item.CompletedAt)
		if silence < window {
			rep.Waiting++
			continue
		}
		rep.Silent = append(rep.Silent, SilentAudit{
			ID:           item.ID,
			Title:        item.Title,
			Repo:         item.Repo,
			CoveringRepo: covering,
			MergedAt:     item.CompletedAt,
			Silence:      silence,
		})
	}

	sort.Slice(rep.Silent, func(i, j int) bool {
		if !rep.Silent[i].MergedAt.Equal(rep.Silent[j].MergedAt) {
			return rep.Silent[i].MergedAt.Before(rep.Silent[j].MergedAt)
		}
		return rep.Silent[i].ID < rep.Silent[j].ID
	})
	return rep, nil
}

// hasSuccessor reports whether any item in the store references auditID.
//
// The audit's own id is taken from its frontmatter; an item with no `id:` can
// have no successor found for it by any means, so it is not searched for — that
// would match every item whose tags happen to contain the empty string.
func hasSuccessor(items []workitem.WorkItem, auditID string) bool {
	if auditID == "" {
		return false
	}
	for _, c := range items {
		if config.SucceedsItem(c.ID, c.TagList(), c.DependsList(), auditID) {
			return true
		}
	}
	return false
}

// DefaultRoot resolves the macguffin store root the same way the dispatch gates
// do: an explicit MG_ROOT wins, then $HOME/.macguffin. It returns "" when
// neither can be resolved, which Scan renders as a disabled scan rather than a
// guess.
//
// Under a test binary with no MG_ROOT it returns "" rather than the developer's
// live store. Scan only reads, so the blast radius of getting this wrong is a
// test that passes or fails on the contents of one machine's ~/.macguffin —
// which is exactly the kind of result this package exists to distrust. Tests
// pass an explicit root.
func DefaultRoot() string {
	if root := os.Getenv("MG_ROOT"); root != "" {
		return root
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin")
}
