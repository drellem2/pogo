package midsessionwedge

// Production wiring: the reading comes from the live agent registry, and the
// exonerating probe from the polecat's own worktree metadata.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// ErrNoRegistry is returned when the watcher is asked to read a fleet it has no
// registry for. It is an ERROR rather than an empty snapshot for the reason
// every detector in this tree repeats: "nothing to judge" and "could not judge"
// must not be the same reading.
var ErrNoRegistry = errors.New("midsessionwedge: no agent registry")

// RegistrySource reads every live agent's mid-session state from the registry.
//
// The digest is taken over the agent's FULL retained ring
// (agent.OutputRingBytes), not a smaller window. A short window is more easily
// held identical by a burst that scrolls the interesting bytes past it, and the
// whole judgement here rests on "identical" meaning identical.
func RegistrySource(reg *agent.Registry) SourceFunc {
	return func(now time.Time) ([]Reading, error) {
		if reg == nil {
			return nil, ErrNoRegistry
		}
		agents := reg.List()
		out := make([]Reading, 0, len(agents))
		for _, a := range agents {
			if a == nil {
				continue
			}
			r := Reading{
				Name:       a.Name,
				Kind:       string(a.Type),
				HasReceipt: a.HasReceiptSignal(),
			}
			if b := a.RecentOutput(agent.OutputRingBytes); len(b) > 0 {
				sum := sha256.Sum256(b)
				r.Digest = hex.EncodeToString(sum[:])
			}
			if r.HasReceipt {
				// A read error leaves Submits at zero, which would read as "no
				// submit since the delivery" — the incriminating direction — so
				// it clears HasReceipt instead and the agent is declined.
				n, err := agent.CountSubmits(a.ReceiptFile())
				if err != nil {
					r.HasReceipt = false
				} else {
					r.Submits = n
				}
			}
			if q := a.QueuedNudge(); q != nil {
				r.Owed = true
				r.OwedAt = q.At
				r.OwedSubmits = q.Submits
			}
			out = append(out, r)
		}
		return out, nil
	}
}

// RegistryRecover delivers a bare submit terminator to the named agent.
//
// Agent.Nudge, not NudgeWithMode: the confirm path would escalate on its own —
// message, bare return, message again — and there is no message here to
// escalate with. The one payload this detector is willing to send is the empty
// one, because it submits whatever is loaded and cannot duplicate anything.
func RegistryRecover(reg *agent.Registry) RecoverFunc {
	return func(name string) error {
		if reg == nil {
			return ErrNoRegistry
		}
		a := reg.Get(name)
		if a == nil {
			return errors.New("midsessionwedge: agent " + name + " is not in the registry")
		}
		return a.Nudge("")
	}
}

// RegistrySubmits re-reads one agent's receipt count.
func RegistrySubmits(reg *agent.Registry) SubmitsFunc {
	return func(name string) (int, error) {
		if reg == nil {
			return 0, ErrNoRegistry
		}
		a := reg.Get(name)
		if a == nil {
			return 0, errors.New("midsessionwedge: agent " + name + " is not in the registry")
		}
		return agent.CountSubmits(a.ReceiptFile())
	}
}

// gitMetaFiles are the per-worktree files whose mtimes move when the agent does
// anything git-shaped: a commit rewrites HEAD and appends logs/HEAD, `git add`
// and even a status refresh rewrite index, a rebase or merge writes the rest.
var gitMetaFiles = []string{"HEAD", "index", "ORIG_HEAD", "logs/HEAD", "COMMIT_EDITMSG", "MERGE_MSG"}

// RegistryWorktree builds the POSITIVE-ONLY exonerating probe over each agent's
// own worktree, as the registry records it — Agent.WorktreeDir, an observation,
// rather than a path reconstructed from the agent's name, which is a claim that
// can rot.
//
// WHAT IT SEES, AND WHY THAT IS ENOUGH. It stats the linked worktree's git
// metadata (see gitMetaFiles) and the worktree root itself. It does NOT walk the
// tree, so an agent quietly editing a file six directories down moves nothing
// here. That is acceptable ONLY because this signal is used in one direction:
// movement clears the alarm, and stillness contributes nothing. Reading it the
// other way — treating a still worktree as evidence of a wedge — is the trap the
// mayor named on 2026-09-08 when this signal settled a live case: a fresh commit
// proves recent life, and THE ABSENCE OF ONE PROVES NOTHING AT ALL, because an
// agent thinking hard between commits is byte-identical to an agent wedged
// between commits and commits are rare events.
//
// ok=false means there is no worktree to read at all — a crew agent, or a
// polecat spawned with --no-worktree — and carries no judgement either way.
//
// TWO THINGS WERE MEASURED HERE, on this box, 2026-09-08, against a polecat
// that was working continuously the whole time:
//
//   - Its worktree metadata had not moved in FIFTY MINUTES. That is the mayor's
//     limit as a direct observation rather than an argument: a live, busy,
//     provably-healthy agent is indistinguishable from a wedged one by this
//     probe alone, because commits are rare events. Anything that reads
//     stillness here as evidence is reading the instrument backwards.
//   - Repeated `git status`, `git diff` and `git log` over that window did NOT
//     advance the index mtime, so the obvious accidental-exoneration path — a
//     read-only command refreshing the index and clearing the alarm for every
//     polecat, silently — did not occur. That is one box on one day, which is
//     why the exoneration event names the path anyway.
func RegistryWorktree(reg *agent.Registry) WorktreeFunc {
	return func(name string, since time.Time) (Movement, bool) {
		if reg == nil {
			return Movement{}, false
		}
		a := reg.Get(name)
		if a == nil || a.WorktreeDir == "" {
			return Movement{}, false
		}
		newest, which, ok := worktreeNewest(a.WorktreeDir)
		if !ok {
			return Movement{}, false
		}
		return Movement{Moved: !newest.Before(since), At: newest, Path: which}, true
	}
}

// worktreeMoved reports the newest mtime among a worktree's git metadata and
// its root directory, and whether that is at or after `since`.
func worktreeMoved(dir string, since time.Time) (bool, time.Time, bool) {
	newest, _, ok := worktreeNewest(dir)
	if !ok {
		return false, time.Time{}, false
	}
	return !newest.Before(since), newest, true
}

// worktreeNewest returns the newest mtime and WHICH path carried it.
//
// The name is not decoration. This probe can only ever exonerate, so a path that
// moves for a reason unrelated to the agent's progress — a background `git
// status` refreshing the index is the obvious candidate — would suppress every
// finding for every polecat, silently and forever. Naming the path in the
// exoneration event turns that from an instrument that cannot fail into one
// whose failure is visible in the log as the same filename every time.
func worktreeNewest(dir string) (time.Time, string, bool) {
	if dir == "" {
		return time.Time{}, "", false
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return time.Time{}, "", false
	}
	newest, which := st.ModTime(), "."
	if gd := gitDir(dir); gd != "" {
		for _, f := range gitMetaFiles {
			fi, err := os.Stat(filepath.Join(gd, f))
			if err != nil {
				continue
			}
			if fi.ModTime().After(newest) {
				newest, which = fi.ModTime(), f
			}
		}
	}
	return newest, which, true
}

// gitDir resolves a worktree's git directory. In a LINKED worktree — which is
// what every polecat has — `.git` is a file holding `gitdir: <path>`, not a
// directory, so following it is not optional: statting `<worktree>/.git/HEAD`
// finds nothing and would report every polecat as never having moved.
func gitDir(worktree string) string {
	p := filepath.Join(worktree, ".git")
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if st.IsDir() {
		return p
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "gitdir:"); ok {
			g := strings.TrimSpace(rest)
			if g == "" {
				return ""
			}
			if !filepath.IsAbs(g) {
				g = filepath.Join(worktree, g)
			}
			return g
		}
	}
	return ""
}
