package agent

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/workitem"
)

// The `declares-remainder` warning, injected into the rendered prompt by the
// DAEMON at dispatch.
//
// # The control that kept failing (mg-a367)
//
// An item tagged `declares-remainder` says its own output is a RECOMMENDATION,
// so at the moment it completes the thing it recommends is undone by
// construction. `mg done` refuses such an item until a successor is named. A
// worker that merges without filing one is stopped within seconds of the merge
// — pogod stops it there by design — so the refused close lands on nobody, the
// item returns to available/, and priority-wake and stall-watch advertise
// FINISHED work as unclaimed. Only the coordinator can clear it.
//
// The control for that was one line in the coordinator's operating
// instructions: when dispatching such an item, say so in the body, because the
// body is the snapshot the worker reads and the only channel that reaches it in
// time. That control is written down, in the right place, addressed to the right
// actor — and it failed three times, most instructively on mg-9d4e, which is the
// ticket ABOUT this failure, dispatched under a repeated stall-watch notice
// about that same item. A control that degrades under load is not much of a
// control for a coordinator whose load is the thing being managed. Standing
// exposure when this was filed: 21 open items carrying the tag, each one
// forgotten line from repeating it.
//
// So the warning is emitted HERE, by the process that already reads the work
// item at spawn, rather than composed by whoever is dispatching. That is the
// whole point of the change: it must not depend on anyone remembering.
//
// # Two properties this file exists to preserve
//
//   - INJECTED, NOT COMPOSED. Nothing a dispatcher does or forgets changes
//     whether the block appears. It keys on the tag, read from the store.
//
//   - ADDITIVE. It is PREPENDED to the rendered prompt and touches nothing else.
//     The dispatcher's brief frequently carries load-bearing rescue
//     instructions; this must not replace or reorder a word of it. It goes above
//     the template output rather than into TemplateVars.Body for the same
//     reason it is not a `{{if .DeclaresRemainder}}` block in the templates: a
//     template is an on-disk, user-editable file, so making the warning
//     conditional on a template carrying it would rebuild the defect one layer
//     down — a control that is present only where somebody remembered to put it.
//
// # What it deliberately does NOT do
//
// It does not refuse the dispatch. The tag means "this work has a known
// remainder", not "this is unsafe to start".
//
// It does not suppress itself when the item already carries a `successor:<id>`
// tag, and that is a decision rather than an omission. A pre-existing successor
// tag DOES satisfy `mg done` (measured 2026-08-19 against a scratch store: an
// item tagged `declares-remainder,successor:mg-1985` closed cleanly), so a
// suppression would be right most of the time — but only most. The same
// measurement showed a tag naming an id that no longer exists is still REFUSED
// ("names successor mg-dead, which no longer exists"), and this process cannot
// tell a rotted link from a live one: internal/workitem does not read the
// archive, so an archived-but-valid successor looks identical to a deleted one.
// The failure directions are not symmetric — a redundant paragraph costs a
// worker ten seconds, and a missing one costs the coordinator an item stuck in
// available/ — so the block fires on the declaration alone.

// DeclaresRemainderTag is the macguffin tag that makes `mg done` refuse an item
// naming no successor. One literal, here, because the reader of this file and
// the reader of a refusal message must be looking at the same string.
const DeclaresRemainderTag = "declares-remainder"

// RemainderDeclarer answers whether a work item declares a remainder, at the
// moment of dispatch.
//
// A separate interface from DispatchGate and WorkItemTyper for the reason
// WorkItemTyper is separate from DispatchGate: the three ask different questions
// of the same file, they fail in different directions, and widening a
// load-bearing gate's interface to carry an unrelated verdict is how two rules
// begin to drift apart. This one is not a gate at all — it refuses nothing.
type RemainderDeclarer interface {
	// DeclaresRemainder reports whether workItemID carries the declaration. A
	// missing id, an item that cannot be found, and an unreadable store all
	// report false: see MGRemainderDeclarer.DeclaresRemainder for the direction.
	DeclaresRemainder(workItemID string) bool
}

// MGRemainderDeclarer is the production RemainderDeclarer: it reads the work
// item out of the macguffin store and looks for the tag.
type MGRemainderDeclarer struct {
	// Root overrides the macguffin store location. Empty resolves via
	// macguffinStoreRoot, which under a test binary is a throwaway temp store
	// rather than the live one.
	Root string
}

// DeclaresRemainder implements RemainderDeclarer.
//
// THE FAILURE DIRECTION IS QUIET, and unlike the dispatch gates that is not a
// trade-off between refusing and allowing — nothing is refused either way. What
// is lost when this cannot answer is a paragraph of prose, so an unreadable
// store, an absent item, or a spawn with no --id all produce no block and the
// dispatch proceeds exactly as it did before this file existed. The read failure
// is logged, because "the warning did not appear" and "the warning was not
// needed" are otherwise the same observation from outside.
func (m MGRemainderDeclarer) DeclaresRemainder(workItemID string) bool {
	if workItemID == "" {
		return false
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return false
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), workItemID)
	if err != nil {
		log.Printf("remainder declaration: could not read work item %s from %s: %v — "+
			"dispatching WITHOUT the `%s` warning; if this item declares a remainder, "+
			"its worker was not told that `mg done` will refuse without --successor",
			workItemID, root, err, DeclaresRemainderTag)
		return false
	}
	if !found {
		return false
	}
	for _, tag := range item.TagList() {
		if strings.EqualFold(strings.TrimSpace(tag), DeclaresRemainderTag) {
			return true
		}
	}
	return false
}

// SetRemainderDeclarer installs the reader consulted before a polecat's prompt
// is rendered. Passing nil restores the default, MGRemainderDeclarer{} — which
// is functional, not a no-op, for the same reason SetDispatchGate's default is:
// a control that engages only once someone wires it is absent in every
// deployment where they didn't, and "someone did not do the one step" is the
// exact failure this file exists to end.
func (r *Registry) SetRemainderDeclarer(d RemainderDeclarer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remainderDeclarer = d
}

func (r *Registry) getRemainderDeclarer() RemainderDeclarer {
	r.mu.RLock()
	d := r.remainderDeclarer
	r.mu.RUnlock()
	if d == nil {
		return MGRemainderDeclarer{}
	}
	return d
}

// declaresRemainderPrelude returns the block to prepend to the rendered prompt,
// or "" when the item declares no remainder (or could not be read).
//
// The text is fixed apart from the item id. It names the refusal the worker will
// hit, both ways out, and — because a worker that has already merged is usually
// seconds from being stopped — says that there is no second chance to notice.
func (r *Registry) declaresRemainderPrelude(workItemID string) string {
	if !r.getRemainderDeclarer().DeclaresRemainder(workItemID) {
		return ""
	}
	return remainderPreludeFor(workItemID)
}

// remainderPreludeFor renders the fixed block for one item id. Split out from
// the lookup so the text can be asserted without a store.
//
// WHAT IT TELLS THE WORKER IS NOT "PASS --successor", and the difference is the
// whole of mg-27c0. On the merge path the worker submits to the refinery and is
// stopped; POGOD performs the close and was never told the id. It resolves one
// by asking the store which item names the closing item as its `predecessor` —
// so the link has to be IN THE STORE before the branch merges, and `mg new`
// does not write it (measured 2026-08-19: a freshly filed item's `predecessor`
// field is `[]`). Telling a worker only to "file the successor" reproduces the
// failure that cost 5 of 5 declared items on the night of 2026-08-13: every one
// of them had a successor filed and none of them closed. So the block names the
// linking edit, and offers `mg done --successor` as the one-step form for a
// worker that does reach its own close.
func remainderPreludeFor(workItemID string) string {
	return fmt.Sprintf(`# ⚠ THIS WORK ITEM DECLARES A REMAINDER — its close WILL REFUSE without a successor

%[1]s is tagged `+"`"+DeclaresRemainderTag+"`"+`. That tag says the item's own output is a
RECOMMENDATION, so the moment it completes, the thing it recommends is undone by
construction — and `+"`mg done`"+` refuses it until something is named to carry that
forward:

    %[1]s declares a remainder (tag "`+DeclaresRemainderTag+`") and names no successor

**You will not get a second chance to notice.** On the merge path you do not
close this item — pogod does, seconds after your branch lands, and it stops you
about half a second later. A close you did not prepare for lands on nobody: the
item returns to `+"`available/`"+`, where the priority wake and the stall watch
advertise finished work as unclaimed, and only the coordinator can clear it.

**BEFORE YOU SUBMIT TO THE REFINERY, do one of these two things.**

**1. File the item that carries the remainder, and LINK IT BACK to this one.**
Both commands, not just the first — `+"`mg new`"+` does not write the link, and pogod
has no other way to find your successor: it asks the store which item names
%[1]s as its `+"`predecessor`"+` and closes only when exactly one does.

    mg new --repo=<the repo this item names> --title="..." --body-file=- <<'BODY'
    ...what is left, and why this item could not do it
    BODY
    mg edit <the new id> --add-tags=predecessor:%[1]s

It does not have to be a new item — if the remainder is already tracked
somewhere, link that id. If you do reach your own `+"`mg done`"+` (you outlived the
merge, or this item closes without one), `+"`mg done %[1]s --successor=<id>`"+` writes
the same link and closes in one step.

**2. If your work genuinely leaves NOTHING behind, say so explicitly.** Do not
invent a successor id to get a command through. Retract the declaration —
`+"`mg edit %[1]s --rm-tags="+DeclaresRemainderTag+"`"+` — and state in your verdict
why nothing is owed.

*This block was injected by pogod at dispatch because the work item carries the
tag. It is not part of the brief below, and it replaces nothing in it.*

---

`, workItemID)
}
