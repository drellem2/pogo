package gitgc

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
)

// TicketState is the lifecycle classification of a macguffin work item,
// reduced to what GC needs: only a concluded ticket makes its polecat
// branch a deletion candidate.
//
// Every not-concluded status is a SEPARATE state even though GC treats them
// identically, because this type is also what the operator report prints. It
// used to collapse available/claimed/pending into one state whose String() was
// "in-flight", and that word is a claim about the world — that somebody is
// working on the item — which the status alone never established. A reader of
// `pogo gc --list-preserved` took three trees labelled "in-flight" to be
// protected by active work; all three items were `available`, blocked, with no
// process running (mg-11fa). The reduction GC needs is Concluded(); the report
// needs the word mg actually said, so the word is kept and the reduction is a
// method.
type TicketState int

const (
	// TicketUnknown means the work item could not be resolved — no such
	// ticket, or `mg` was unavailable. GC keeps unknown branches: it never
	// deletes what it cannot positively classify.
	TicketUnknown TicketState = iota
	// TicketAvailable means the item exists, is not concluded, and NOBODY
	// holds it. Its branch is kept. This is not "in flight": an available
	// item is one dispatch away from being concluded, so a tree resting on
	// it is resting on the absence of a decision, not on a decision.
	TicketAvailable
	// TicketClaimed means an agent holds the item. Its branch is kept. Note
	// that a claim outlives the process that made it, so this state does not
	// establish that anything is running either — `pogo agent list` does.
	TicketClaimed
	// TicketPending means the item is queued behind something. Kept.
	TicketPending
	// TicketShelved means the item was deliberately set aside. Kept — and
	// reported as shelved rather than as unknown, which is what it used to
	// render as by falling through to the default. "Shelved" is a decision
	// somebody made; "unknown" says the lookup failed. 205 of this machine's
	// work items are shelved, and every one of them said "unknown".
	TicketShelved
	// TicketDone means the work item is marked done. Its branch is
	// deletable once merged into the target branch.
	TicketDone
	// TicketArchived means the work item has been archived — work
	// concluded. Its branch is deletable regardless of merge state.
	TicketArchived
)

// Concluded reports whether the work item's lifecycle has ended, making
// its branch a deletion candidate (a done ticket is still subject to the
// merge gate; an archived ticket is not).
//
// This is the ONLY reduction GC's deletion decisions are allowed to make, and
// splitting the not-concluded statuses above left it bit-for-bit unchanged:
// everything that is not Done or Archived is kept, exactly as before.
func (s TicketState) Concluded() bool {
	return s == TicketDone || s == TicketArchived
}

// String renders the state as the status word mg reported, so a report that
// prints it asserts nothing mg did not say.
func (s TicketState) String() string {
	switch s {
	case TicketAvailable:
		return "available"
	case TicketClaimed:
		return "claimed"
	case TicketPending:
		return "pending"
	case TicketShelved:
		return "shelved"
	case TicketDone:
		return "done"
	case TicketArchived:
		return "archived"
	default:
		return "unknown"
	}
}

// stateFromStatus maps an mg status string to a TicketState. An unrecognised
// status stays TicketUnknown and is therefore kept: a status this function has
// not been taught is not evidence that work concluded.
func stateFromStatus(status string) TicketState {
	switch status {
	case "done":
		return TicketDone
	case "archived":
		return TicketArchived
	case "available":
		return TicketAvailable
	case "claimed":
		return TicketClaimed
	case "pending":
		return TicketPending
	case "shelved":
		return TicketShelved
	default:
		return TicketUnknown
	}
}

// TicketIndex maps macguffin work-item IDs (e.g. "mg-30d5") to state.
type TicketIndex map[string]TicketState

// LoadTicketIndex builds a TicketIndex by invoking `mg list --all --json`,
// which emits one JSON object per work item across every status including
// archived and shelved.
func LoadTicketIndex() (TicketIndex, error) {
	out, err := exec.Command("mg", "list", "--all", "--json").Output()
	if err != nil {
		return nil, err
	}
	return parseTicketIndex(out), nil
}

// parseTicketIndex parses the NDJSON emitted by `mg list --json`.
func parseTicketIndex(ndjson []byte) TicketIndex {
	idx := TicketIndex{}
	for _, line := range strings.Split(string(ndjson), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		if item.ID != "" {
			idx[item.ID] = stateFromStatus(item.Status)
		}
	}
	return idx
}

// agentPrefixes are name fragments that pogo has, over its history,
// prepended to a work-item ID when forming a polecat or branch name.
// Stripping them recovers the underlying ID.
var agentPrefixes = []string{"cat-", "polecat-", "mg-", "pc-", "gt-"}

// retrySuffixes are decorations appended to a branch name for a
// re-dispatched or fixup polecat (e.g. polecat-3963-r, polecat-gt-30eb-fix).
var retrySuffixes = []string{"-retry", "-redo", "-new", "-fix", "-r3", "-r2", "-r", "-3", "-2"}

// hexToken matches a 4-hex-character macguffin work-item code.
var hexToken = regexp.MustCompile(`[0-9a-f]{4}`)

// candidateIDs derives the macguffin work-item IDs a polecat NAME might
// correspond to, most-specific first. The name is the polecat's registry
// name — equivalently a branch's `polecat-` suffix or a worktree directory's
// basename, which are two spellings of the same string.
//
// Pogo's polecat/branch naming has drifted across many releases —
// `polecat-<id>`, `polecat-mg-<id>`, `polecat-cat-mg-<id>`, single-letter
// `polecat-p<id>` / `polecat-r<id>`, and `-r`/`-fix` retry suffixes all occur
// in the wild — so several spellings are generated and the caller resolves the
// first that exists.
//
// Because every form a polecat name takes embeds its 4-hex ticket code,
// recovering that code is reliable; a name that yields no resolvable
// candidate is simply left classified TicketUnknown and therefore kept.
func candidateIDs(suffix string) []string {
	if suffix == "" {
		return nil
	}
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, e := range out {
			if e == s {
				return
			}
		}
		out = append(out, s)
	}

	add(suffix)
	add("mg-" + suffix)

	// Repeatedly strip leading agent-name prefixes: cat-mg-32a9 -> 32a9.
	core := suffix
	for {
		stripped := false
		for _, p := range agentPrefixes {
			if strings.HasPrefix(core, p) && len(core) > len(p) {
				core = core[len(p):]
				stripped = true
			}
		}
		if !stripped {
			break
		}
	}
	add("mg-" + core)
	add(core)

	// Strip a single trailing retry/fixup decoration: 30eb-fix -> 30eb.
	bare := core
	for _, s := range retrySuffixes {
		if strings.HasSuffix(bare, s) {
			bare = strings.TrimSuffix(bare, s)
			break
		}
	}
	add("mg-" + bare)

	// Last resort: the first 4-hex token in the core recovers the ticket
	// code from glued forms such as p06cb / r283e.
	if m := hexToken.FindString(core); m != "" {
		add("mg-" + m)
	}
	return out
}

// OwnerState resolves the work item behind a polecat NAME — a worktree
// directory's basename, or an orphan dir's name — and returns its ID and
// lifecycle state. If no candidate ID resolves against the index it returns
// ("", TicketUnknown), the safe answer that keeps the tree.
//
// This is the key the worktree phases classify on (mg-bdda). It answers "has
// the work this TREE was created for concluded", which is the question a
// directory's fate turns on; BranchState answers "has the work this REF holds
// concluded", which is the question a ref's fate turns on. They differ
// whenever a polecat checks out a foreign branch, and the two phases used to
// pick different ones for the same decision.
func (idx TicketIndex) OwnerState(name string) (id string, state TicketState) {
	for _, c := range candidateIDs(name) {
		if st, ok := idx[c]; ok {
			return c, st
		}
	}
	return "", TicketUnknown
}

// BranchState resolves the work item behind a polecat branch and returns
// its ID and lifecycle state. If no candidate ID resolves against the
// index it returns ("", TicketUnknown) — the safe, keep-the-branch answer.
//
// A polecat branch is `polecat-` + a polecat name, so this is OwnerState
// applied to the suffix; the two are one resolver deliberately, so a naming
// spelling that resolves for a branch cannot fail to resolve for the
// identically-named directory.
func (idx TicketIndex) BranchState(branch string) (id string, state TicketState) {
	return idx.OwnerState(BranchSuffix(branch))
}
