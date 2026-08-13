package main

import (
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// EventPromptRefresh is the durable record of act 3 of prompt activation.
//
// The four acts of getting a merged prompt fix in front of a running agent are:
//
//	act 1  merge the prompt fix               the refinery
//	act 2  ship the new binary                scripts/pogo-self-deploy, 03:00 local
//	act 3  install the prompts onto disk      THIS — pogod's boot, see main.go
//	act 4  restart the agent so it re-reads    the same boot for auto_start = true;
//	                                          for auto_start = false the deploy's
//	                                          lost-mail-check alert names the agent
//	                                          and the command
//
// mg-b6bd was filed on the belief that act 3 had NO owner, because
// `grep -c 'prompt install' scripts/pogo-self-deploy` is 0 and always has been.
// That grep is accurate and the conclusion drawn from it is wrong: the deploy
// never shells out to the CLI, it kickstarts pogod, and pogod's boot calls
// agent.InstallPrompts in-process before it auto-starts anybody (40b60c1,
// 2026-04-27). Act 3 is automatic, and its cadence is every daemon restart.
//
// What was genuinely missing is the half this file adds: a RECORD. The boot
// logged one counts-only line to pogod.log —
//
//	pogod: refreshed prompts (installed=0 updated=9 skipped=0 conflicts=0)
//
// — which cannot answer the only question a reader ever has, "does agent X's
// live prompt carry commit Y". It names no file, names no revision, and lands
// in pogod.log, which is pogod's inherited stderr and is on no agent's schedule
// (the same property that let a declined sync fire every boot for seven days,
// mg-c3f0). With no readable record, an install that runs every single night is
// indistinguishable from one that runs at random — which is exactly how mg-b6bd
// read it, and the reading was reasonable given the evidence available.
//
// So the fix is not to add an installer. It is to make the installer that
// already runs say what it did, somewhere durable, in terms that answer the
// question: which agents, from which revision, when.
const EventPromptRefresh = "prompt_refresh"

// promptRefreshUnknownRevision is what goes on the record when the running
// binary carries no VCS stamp (a `go build` from a dirty or non-git tree).
//
// It is a named sentinel rather than an empty string because those are
// different facts and a reader must not have to guess which one they are
// holding: "" reads as "the field was not written", which would make an old
// pogod's silence look identical to a new pogod's honest "I do not know".
const promptRefreshUnknownRevision = "unknown"

// promptRefreshEvent renders one boot's prompt refresh into the event that goes
// to events.log.
//
// It is emitted on EVERY boot that attempts a refresh — including the boot
// where every prompt was already current and nothing was written. That is the
// deliberate difference between this record and the log lines below, which stay
// quiet on a no-op to avoid spamming pogod.log on every restart:
//
//	the LOG answers "is there something I need to do?"     — silence is correct
//	the RECORD answers "what state are the prompts in?"    — silence is useless
//
// An all-skipped boot is not an absence of information. It is the positive
// observation "all 9 prompts were current at revision R as of time T", and it
// is the single most common answer to "did agent X get the fix" — so it is the
// one that must not be the answer the record cannot give.
//
// refreshErr non-nil means InstallPrompts failed outright and nothing was
// installed; res is ignored in that case.
func promptRefreshEvent(res *agent.InstallResult, rev string, refreshErr error, now time.Time) events.Event {
	details := map[string]any{
		"revision": promptRefreshRevision(rev),
	}
	if refreshErr != nil {
		details["ok"] = false
		details["error"] = refreshErr.Error()
		return events.Event{
			Timestamp: now.UTC().Format(time.RFC3339Nano),
			EventType: EventPromptRefresh,
			Agent:     "pogod",
			Details:   details,
		}
	}

	details["ok"] = true
	var installed, updated, skipped []string
	var conflicts []string
	if res != nil {
		installed, updated, skipped = res.Installed, res.Updated, res.Skipped
		for _, c := range res.Conflicts {
			conflicts = append(conflicts, c.Path)
		}
	}
	// Every name, never a top-N. The whole defect being repaired is a record
	// that reported a count and withheld the identities; a record that reports
	// SOME identities and silently drops the rest is the same defect with a
	// smaller blast radius, and it reads as completeness while not being it.
	// Nine prompts today; a fleet large enough for this to matter has bigger
	// problems than a long JSON array.
	details["installed"] = nonNilNames(installed)
	details["updated"] = nonNilNames(updated)
	details["skipped"] = nonNilNames(skipped)
	details["conflicts"] = nonNilNames(conflicts)
	// changed is what a reader asking "did anything move tonight" wants, and
	// deriving it from two arrays is the sort of arithmetic a shell reader gets
	// wrong. Conflicts are NOT changed — a conflict is precisely the file the
	// install declined to touch.
	details["changed"] = len(installed) + len(updated)
	return events.Event{
		Timestamp: now.UTC().Format(time.RFC3339Nano),
		EventType: EventPromptRefresh,
		Agent:     "pogod",
		Details:   details,
	}
}

// promptRefreshRevision normalises the build stamp for the record. See
// promptRefreshUnknownRevision for why absent is a value and not a gap.
func promptRefreshRevision(rev string) string {
	if strings.TrimSpace(rev) == "" {
		return promptRefreshUnknownRevision
	}
	return rev
}

// nonNilNames guarantees a JSON array rather than a JSON null for an empty
// list. A consumer that reads `.details.updated | length` must not have to
// special-case null, and a shell reader grepping for `"updated":[` must not
// have the key change shape depending on the outcome.
func nonNilNames(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// shortRev trims a full 40-char SHA for the human-facing log lines. The event
// keeps the full revision; the log gets the prefix an operator actually types.
func shortRev(rev string) string {
	rev = promptRefreshRevision(rev)
	if rev == promptRefreshUnknownRevision || len(rev) <= 12 {
		return rev
	}
	return rev[:12]
}
