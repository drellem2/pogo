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
type TicketState int

const (
	// TicketUnknown means the work item could not be resolved — no such
	// ticket, or `mg` was unavailable. GC keeps unknown branches: it never
	// deletes what it cannot positively classify.
	TicketUnknown TicketState = iota
	// TicketInFlight means the work item is still active
	// (available / claimed / pending). Its branch is kept.
	TicketInFlight
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
func (s TicketState) Concluded() bool {
	return s == TicketDone || s == TicketArchived
}

func (s TicketState) String() string {
	switch s {
	case TicketInFlight:
		return "in-flight"
	case TicketDone:
		return "done"
	case TicketArchived:
		return "archived"
	default:
		return "unknown"
	}
}

// stateFromStatus maps an mg status string to a TicketState.
func stateFromStatus(status string) TicketState {
	switch status {
	case "done":
		return TicketDone
	case "archived":
		return TicketArchived
	case "available", "claimed", "pending":
		return TicketInFlight
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

// candidateIDs derives the macguffin work-item IDs a polecat branch might
// correspond to, most-specific first. Pogo's polecat/branch naming has
// drifted across many releases — `polecat-<id>`, `polecat-mg-<id>`,
// `polecat-cat-mg-<id>`, single-letter `polecat-p<id>` / `polecat-r<id>`,
// and `-r`/`-fix` retry suffixes all occur in the wild — so several
// spellings are generated and the caller resolves the first that exists.
//
// Because every form a polecat branch takes embeds its 4-hex ticket code,
// recovering that code is reliable; a branch that yields no resolvable
// candidate is simply left classified TicketUnknown and therefore kept.
func candidateIDs(branch string) []string {
	suffix := BranchSuffix(branch)
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

// BranchState resolves the work item behind a polecat branch and returns
// its ID and lifecycle state. If no candidate ID resolves against the
// index it returns ("", TicketUnknown) — the safe, keep-the-branch answer.
func (idx TicketIndex) BranchState(branch string) (id string, state TicketState) {
	for _, c := range candidateIDs(branch) {
		if st, ok := idx[c]; ok {
			return c, st
		}
	}
	return "", TicketUnknown
}
