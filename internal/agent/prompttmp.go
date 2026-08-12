package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Expanded-prompt temp files: the directory pogod writes a polecat's rendered
// prompt into, the removal that runs when that polecat is gone, and the sweep
// that reclaims what a dead pogod could not.
//
// # The leak
//
// ExpandTemplateToFile RETURNS A PATH, so the file has to outlive the call, and
// until mg-5197 nothing ever removed it on the success path. One file per prompt
// expansion — i.e. per polecat spawn — forever. Measured 2026-08-12 on this box:
// 16,011 files and 75 MB in a single $TMPDIR entry, the oldest 8 days old.
//
// WHAT THE COUNT IS ACTUALLY MADE OF, because it is not what the ticket assumed.
// 15,275 of those 16,011 are under 100 bytes and hold "body\n" or "# polecat\n
// body wi-err": they are TEST fixtures, written by the suites that drive
// handleSpawnPolecat, not by the fleet. Only 396 are over 5 KB, the size a real
// expanded prompt has — about 50 real spawns a day against roughly 1,900 test
// ones. So the production leak this file's subject is, is ~2.5% of the entries;
// the rest is the same defect reached through the same handler by `go test`.
// Registry.Remove closes both, because every suite that stops or removes an
// agent funnels through it — but the arithmetic is worth stating rather than
// letting a future reader infer that 16,011 files a week were polecat spawns.
//
// None of that is what makes it worth fixing. It is UNBOUNDED and it is on the
// SPAWN path, so it grows with fleet activity and nothing resets it — small
// against the 61 GB mg-de3c reclaimed, and that is a fact about today's fleet
// size, not about the shape.
//
// # Why this is not internal/testtmp, and what it does borrow
//
// mg-de3c's sweep keys on OWNERSHIP rather than age, for a reason that applies
// here in full: this box runs several polecats concurrently, and an age rule
// that guesses wrong deletes a LIVE spawn's prompt — which pogod re-reads on a
// restart_on_crash respawn, and which the harness holds a path into for the life
// of the session. That RULE generalises. The MECHANISM does not:
//
//   - testtmp encodes the CREATING process's pid in the name and sweeps on that
//     pid's liveness. It can, because there the creator, the owner and the only
//     user are one process, and the directory's lifetime is exactly that
//     process's lifetime.
//   - Here the creator is pogod — long-lived, and the wrong answer in both
//     directions: keyed on pogod's pid nothing is ever reaped while it runs, and
//     everything is reaped the moment it restarts, including the files of the
//     polecats that outlived it. The owner is the POLECAT, whose pid does not
//     exist yet at the moment the file is written, because the path is an
//     argument to the process that will have it.
//
// So the key is the polecat's NAME, and the liveness question is answered by
// LivePolecatSet — the registry unioned with the persisted polecat witness. That
// is not a third bespoke cleanup: it is the same do-not-touch set gitgc.Sweep
// already gates worktree removal on, which is the identical decision ("may I
// delete this dead polecat's stuff") against the identical hazard ("it is not
// dead, it outlived the pogod that spawned it" — mg-0130). A file whose owner
// cannot be established is KEPT, and an unreadable witness store cancels the
// sweep rather than shrinking the live set, exactly as it cancels gitgc's.
//
// # Where age still appears, and in which direction
//
// Twice, and never as the thing that authorises deleting an owned file:
//
//   - SpawnGrace KEEPS a file younger than the grace whose owner is not (yet) in
//     the live set. The file is created before the worktree, the claim and the
//     process, so a concurrent spawn's sweep can see it before its owner is
//     registered. Age is used only to refuse.
//   - LegacyStaleAfter is the ONLY branch where age authorises removal, and only
//     for a name that encodes no owner — a file written by a pogod that predates
//     this fix (all 16,011 of them), or by something else entirely. It is a week
//     because such a file may belong to a polecat that is still running under the
//     older binary, and a polecat does not run for a week. Same shape and same
//     reasoning as testtmp.StaleAfter. Note what that means for the reclaim: the
//     existing backlog is 0–8 days old, so the first sweeps take none of it and
//     it drains over the following week. Stopping the writing is this ticket;
//     reclaiming what already leaked is deliberately not (mg-de3c said the same).
//
// # Two mechanisms, and which one does the work
//
// Registry.Remove deletes a spawn's prompt when the agent leaves the fleet. That
// is the mechanism: it is exact, it needs no liveness inference at all, and it
// covers every normal exit — Stop's two teardown branches and pogod's exit
// callback all funnel through it, while respawn deliberately does not.
//
// SweepExpandedPrompts is residue only. It exists for the single path no
// in-process callback can cover — pogod dies while polecats are running, so
// Remove never runs for them — which is the same gap gitgc's startup sweep is
// for (mg-30d5 D3). So it is called ONCE, from cmd/pogod at startup, and from
// nowhere else.
//
// THAT PLACEMENT IS A SAFETY PROPERTY, NOT A PREFERENCE. An empty live set means
// "no polecat is alive" and "I am reading the wrong witness store" identically,
// and $TMPDIR is machine-wide while the witness store follows POGO_HOME. A test
// binary — internal/testsandbox pins POGO_HOME and does not pin TMPDIR — has an
// empty witness and the REAL prompt directory, so a sweep reachable from library
// code would have the test suite deleting the running fleet's prompts. Which is
// this remedy exhibiting the class of defect it exists to fix, by a new route.
// Keeping the call in the daemon's startup path is what makes that unreachable
// rather than merely unlikely; the grace below is the second line, not the first.

// PromptTempDirName is the single directory this file owns inside $TMPDIR.
const PromptTempDirName = "pogo-prompts"

const (
	// promptFilePrefix and promptFileSuffix bracket every name written here.
	// The prefix is kept from the pre-mg-5197 "polecat-*.md" pattern so a human
	// grepping $TMPDIR sees the same thing they always did.
	promptFilePrefix = "polecat-"
	promptFileSuffix = ".md"
)

var (
	// PromptSpawnGrace is how young a file must be for the sweep to keep it
	// despite finding no live owner. It covers only the window between the
	// write and the owner's registration — worktree creation and the mg claim
	// live in that window, and both shell out.
	PromptSpawnGrace = time.Hour

	// PromptLegacyStaleAfter is how old a file whose name encodes NO owner must
	// be before the sweep removes it. See the direction-of-age note above.
	PromptLegacyStaleAfter = 7 * 24 * time.Hour
)

// PromptTempDir is the directory expanded prompts are written to.
//
// $TMPDIR is read on every call rather than cached, because unlike testtmp.Root
// this has no once-per-process contract to honour: pogod resolves it at spawn
// time and tests re-point TMPDIR between cases.
func PromptTempDir() string {
	return filepath.Join(os.TempDir(), PromptTempDirName)
}

// ensurePromptTempDir creates PromptTempDir, refusing to work through a symlink.
//
// Lstat first, and before the MkdirAll rather than instead of it. $TMPDIR is
// per-user and 0700 on darwin, but os.TempDir falls back to a world-writable
// /tmp when TMPDIR is unset — the case in CI — and there a pre-planted symlink
// at this name would have SweepExpandedPrompts deleting files of somebody else's
// choosing. MkdirAll follows the link and reports success, so the refusal has to
// be explicit. Carried verbatim from testtmp.Root, which is the half of that
// package's mechanism that DOES generalise unchanged.
func ensurePromptTempDir() (string, error) {
	dir := PromptTempDir()
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink; refusing to create or sweep through it", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// createExpandedPromptFile creates an empty prompt file owned by owner.
//
// The owner's name is written into the file name because it is the only handle
// the sweep has on who the file belongs to; a nameless file is precisely what
// leaked. owner must be a validated agent name — ValidateAgentName has already
// refused separators and control characters at the API boundary — and an empty
// one is rejected here rather than producing a name expandedPromptOwner cannot
// parse, which would silently demote a live polecat's file to the week-long
// legacy branch.
func createExpandedPromptFile(owner string) (*os.File, error) {
	if owner == "" {
		return nil, fmt.Errorf("expanded prompt needs an owner: an unowned file is one the sweep can only age out")
	}
	if err := ValidateAgentName(owner); err != nil {
		return nil, err
	}
	dir, err := ensurePromptTempDir()
	if err != nil {
		return nil, fmt.Errorf("create prompt temp dir: %w", err)
	}
	// "polecat-<owner>.*.md". The uniquifier is a separate dot-delimited field
	// so expandedPromptOwner can recover a name that itself contains dots by
	// splitting at the LAST one — agent names may contain them (ValidateAgentName
	// forbids only "." and ".." entire), so a first-dot split would truncate
	// "web.api" to "web" and hand its file to a polecat that does not exist.
	return os.CreateTemp(dir, promptFilePrefix+owner+".*"+promptFileSuffix)
}

// expandedPromptOwner recovers the polecat name encoded in a file name, or
// ok=false when the name is not one this file wrote.
//
// A false is not an error and not a licence to delete: pre-mg-5197 files match
// the prefix and suffix but carry no owner field, and they are what the legacy
// age branch exists for.
func expandedPromptOwner(name string) (string, bool) {
	if !strings.HasPrefix(name, promptFilePrefix) || !strings.HasSuffix(name, promptFileSuffix) {
		return "", false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, promptFilePrefix), promptFileSuffix)
	// Split at the LAST dot: everything before it is the owner, everything after
	// is os.CreateTemp's uniquifier. No assumption is made about the
	// uniquifier's alphabet.
	i := strings.LastIndex(stem, ".")
	if i <= 0 || i == len(stem)-1 {
		return "", false
	}
	return stem[:i], true
}

// RemoveExpandedPrompt removes an agent's prompt file if — and only if — it is
// one that createExpandedPromptFile wrote. It reports whether it removed
// anything.
//
// The guard is the whole point, and it is the way this remedy could exhibit the
// defect class it fixes. pogod's exit callback fires for every agent, and a CREW
// agent's PromptFile is a real, hand-maintained file at ~/.pogo/agents/crew/
// <name>.md. An unguarded os.Remove there would delete a crew persona on its
// first clean exit — trading an unbounded temp leak for silent destruction of
// checked-in configuration. So the path must sit directly in PromptTempDir and
// carry this file's naming; anything else is left alone and reported as
// untouched rather than as an error.
func RemoveExpandedPrompt(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if filepath.Dir(path) != PromptTempDir() {
		return false, nil
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, promptFilePrefix) || !strings.HasSuffix(base, promptFileSuffix) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SweepExpandedPrompts removes prompt files that no live polecat owns and
// returns how many it removed.
//
// live is the do-not-touch set keyed by polecat NAME — build it with
// LivePolecatSet so a polecat that outlived the pogod that spawned it is in it,
// and DO NOT call this at all if that call failed: an unreadable witness store
// is not an empty fleet, and sweeping against a live set known to be missing
// survivors is how a running polecat loses its prompt.
//
// Errors are swallowed per entry: a sweep that cannot delete something has lost
// nothing a caller can act on, and it must never be the reason a spawn fails.
//
// Exported so its behaviour can be tested directly against a fixture directory,
// which is the only way to observe the direction that matters — that a LIVE
// owner's file survives.
func SweepExpandedPrompts(live map[string]bool) int {
	dir := PromptTempDir()
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	now := time.Now()
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		owner, owned := expandedPromptOwner(name)
		if owned && live[owner] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// Cannot read the age, so the keep-guard cannot be applied. Keep.
			continue
		}
		age := now.Sub(info.ModTime())
		if owned {
			if age < PromptSpawnGrace {
				continue
			}
		} else {
			if !strings.HasPrefix(name, promptFilePrefix) || !strings.HasSuffix(name, promptFileSuffix) {
				// Not ours by any naming. This directory is pogo's, but a file
				// we did not write is not a file we may delete.
				continue
			}
			if age < PromptLegacyStaleAfter {
				continue
			}
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	return removed
}
