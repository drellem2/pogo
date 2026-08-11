package agent

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// DispatchGate answers, at the moment of dispatch, whether a work item is gated
// away from automatic execution — whether handing it to a worker is allowed at
// all.
//
// This is the mg-4798 ruling, built. The rule itself (an item is dispatchable
// unless its assignee names a non-dispatchable executor) already existed and was
// already enforced in Go — but only in internal/stallwatch, which decides what to
// WATCH. The path that actually DISPATCHES carried the rule as a sentence in a
// prompt template instead: `internal/agent/prompts/mayor.md` told the coordinator
// that a `--assignee=human` item "won't be dispatched", 170 lines away from the
// listing advice it needed to be read with. So `pogo agent spawn-polecat` would
// cheerfully put a polecat on a human-gated item, and the only thing standing in
// the way was whether an agent had read and retained a paragraph.
//
// mg-4798 rejected fixing that in the prose and rejected fixing it in `mg list`
// (the CLI is convenience, not gatekeeper — ARCHITECTURE.md M2; and status and
// assignee are orthogonal by construction). It ruled the gate belongs in the
// executable path at the dispatch point, reusing the predicate rather than
// restating it. Hence: one predicate in config.IsDispatchGated, two enforcement
// sites that cannot drift apart.
//
// An interface so the handler is testable without a macguffin store, mirroring
// ClaimReleaser.
type DispatchGate interface {
	// DispatchGated reports whether workItemID is gated, and what gated it, so
	// the refusal can name it — "refused: assigned to human" is actionable, a
	// bare refusal is a bug report.
	//
	// It deliberately has no error return. See MGDispatchGate.DispatchGated for
	// why an unreadable store must not be an error here.
	DispatchGated(workItemID string) (Gating, bool)
}

// Gating names WHICH gate refused an item. Exactly one field is set on a
// refusal; both are empty when nothing gated.
//
// Two fields rather than one string because the two gates have different ways
// out and a refusal that cannot say which one fired sends the reader to the
// wrong field. They are answered by one gate object and one read of the item,
// though — the mg-4798 ruling is that a dispatch rule belongs in the executable
// path at the dispatch point, and a SECOND gate object reading the same file to
// answer the same question ("may this be executed at all") is how two rules
// begin to drift apart.
type Gating struct {
	// Assignee is the gating `assignee:` value — a sentinel from the configured
	// vocabulary, or the `blocked:<agent>` shape (config.IsDispatchGated).
	Assignee string
	// Stage is the gating state-carrier `stage:` value from the item's body
	// (config.IsStageGated). mg-69b1: the gh-issue playbook parks a carrier at
	// `stage: gated` for a human GO/NO-GO and sets no assignee, so before this
	// field the workflow's own gate stopped nothing.
	Stage string
	// CarrierUnreadable gates an item whose carrier block the parser could not
	// reach (mg-27d4). It is not a third KIND of gate so much as the honest
	// answer to the stage question: the item declares a stage somewhere the
	// leading-block scan cannot read, so this gate does not know whether it is
	// `gated`, and an item whose gate cannot be read is precisely the item not to
	// dispatch.
	//
	// It carries no value, because there is none to carry. Reporting a stage read
	// out of the middle of a body is the body-wide search workitem's parser
	// deliberately refuses — a body that DISCUSSES a stage would gate on it.
	CarrierUnreadable bool
}

// MGDispatchGate is the production DispatchGate: it reads the work item out of
// the macguffin store and tests its assignee against the configured gate
// vocabulary.
type MGDispatchGate struct {
	// Root overrides the macguffin store location. Empty resolves via
	// macguffinStoreRoot, which under a test binary is a throwaway temp store
	// rather than the live one.
	Root string
	// Gates is the non-dispatchable assignee vocabulary. Empty falls back to
	// config.DefaultNonDispatchableAssignees, so an unwired daemon still gates
	// "human" and "parked" — the default is the enforcing one, because a gate
	// that needs wiring to work is a gate that is off wherever someone forgot.
	Gates []string
}

// DispatchGated implements DispatchGate.
//
// THE FAILURE DIRECTION IS DELIBERATE AND IT IS OPEN. An item that cannot be
// found, an id that was never supplied, an unreadable store — all report "not
// gated" and let the spawn proceed. Only a work item positively read from disk
// whose assignee positively matches the gate vocabulary is refused.
//
// That is the opposite of the asymmetry in stall-watch's twin, and for a reason
// worth stating rather than inheriting. Stall-watch fails toward WATCHING because
// its cost of guessing wrong is a spurious nudge — loud and self-correcting. This
// gate's cost of guessing wrong is a refused dispatch: it would break every
// legitimate spawn whose item is not in the store, and `--id` is optional by
// design (a spawn without one merely forfeits start-verification, mg-2437), so
// failing closed would refuse every id-less spawn outright. A gate that fails
// closed on a missing store also means one bad path in macguffin halts the entire
// fleet.
//
// What that costs is worth being precise about, because "fails open" is the kind
// of property that gets discovered rather than read. This gate stops a
// coordinator from dispatching a gated item it read out of available/ — the
// actual mg-4798 failure, where the item is present and the assignee is right
// there in its frontmatter. It does NOT stop a caller who supplies no id, or a
// wrong one. It is a guard on the dispatch decision, not proof that no worker can
// ever reach gated work.
func (m MGDispatchGate) DispatchGated(workItemID string) (Gating, bool) {
	if workItemID == "" {
		return Gating{}, false
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return Gating{}, false
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), workItemID)
	if err != nil {
		// Loud but not fatal: the spawn proceeds (see the fail-open rationale
		// above), and the log records that the gate could not answer, so a store
		// that has become unreadable is visible as something other than silence.
		log.Printf("dispatch gate: could not read work item %s from %s: %v — "+
			"dispatching WITHOUT the assignee gate; if this item is assigned to a "+
			"non-dispatchable executive (%v) the spawn was not refused",
			workItemID, root, err, m.gates())
		return Gating{}, false
	}
	if !found {
		return Gating{}, false
	}
	if config.IsDispatchGated(item.Assignee, m.Gates) {
		return Gating{Assignee: item.Assignee}, true
	}
	// The workflow's own gate (mg-69b1). An item whose body carrier declares
	// `stage: gated` is parked at a human decision, and it gates whatever its
	// assignee says — including the ordinary empty one, which is exactly the case
	// that was dispatchable. Checked AFTER the assignee so a gated item that also
	// carries a sentinel still refuses with the assignee's message: that value was
	// set by hand and states an intent the stage cannot, and its way out differs.
	if config.IsStageGated(item.Stage) {
		return Gating{Stage: item.Stage}, true
	}
	// The gate we could not read (mg-27d4). Checked AFTER the readable stage, so
	// an item that declares a real leading `stage: gated` AND has a stray
	// declaration further down still refuses with the specific message.
	//
	// This is the fail-CLOSED direction, and it is a deliberate exception to the
	// asymmetry documented on config.IsStageGated. That asymmetry is about the
	// STORE — no id, no store, an unopenable file — where failing closed would
	// refuse every legitimate spawn on the day one path goes bad. This is not
	// that: the item was found, opened and parsed, and what it says is that its
	// own gate declaration is somewhere this parser will not read. The blast
	// radius is one item, and it is an item nobody can currently reason about.
	if item.CarrierUnreadable {
		log.Printf("dispatch gate: work item %s has carrier-shaped `workflow:`/`stage:` lines "+
			"the leading-block parser cannot reach — REFUSING the spawn, because an item whose "+
			"gate cannot be read may be at `stage: gated`", workItemID)
		return Gating{CarrierUnreadable: true}, true
	}
	// Not gated — the spawn proceeds. But if the item DECLARES a block in the
	// only channel that existed before the `blocked:<agent>` shape (mg-6fb0), say
	// so on the way past. This is the harm moment: a polecat is about to be put
	// on work whose author wrote down that it is waiting on someone. It does not
	// refuse, because a tag is not a gate and making it one would split the gate
	// across two channels — the thing mg-6fb0 explicitly declined to do.
	if tag, ok := config.BlockIntentMismatch(item.Assignee, item.TagList(), m.Gates); ok {
		suggest := config.SuggestBlockedAssignee(tag)
		if suggest == "" {
			suggest = "--depends, if it is waiting on another work item"
		} else {
			suggest = "--assignee=" + suggest
		}
		log.Printf("dispatch gate: work item %s is tagged %q but its assignee %q does not gate "+
			"dispatch — DISPATCHING ANYWAY (a tag is not a gate); if it is genuinely blocked, %s",
			workItemID, tag, item.Assignee, suggest)
	}
	return Gating{}, false
}

// gates returns the effective gate vocabulary, for messages.
func (m MGDispatchGate) gates() []string {
	if len(m.Gates) == 0 {
		return config.DefaultNonDispatchableAssignees
	}
	return m.Gates
}

// SetDispatchGate installs the gate consulted before a polecat is dispatched.
// Passing nil restores the default, which is MGDispatchGate{} — functional, not
// a no-op, for the same reason SetClaimReleaser's default is: a guard that only
// engages once someone remembers to wire it is a guard that is absent in every
// deployment where they didn't.
func (r *Registry) SetDispatchGate(g DispatchGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchGate = g
}

func (r *Registry) getDispatchGate() DispatchGate {
	r.mu.RLock()
	g := r.dispatchGate
	r.mu.RUnlock()
	if g == nil {
		return MGDispatchGate{}
	}
	return g
}

// dispatchGateRefusal returns the refusal message for a gated work item, or ""
// when dispatch is allowed. The message names the item, the assignee that gated
// it, and the two ways out, because the reader is usually an agent that must
// decide what to do next without a human in the loop.
func (r *Registry) dispatchGateRefusal(workItemID string) string {
	g, gated := r.getDispatchGate().DispatchGated(workItemID)
	if !gated {
		return ""
	}
	// The workflow gate (mg-69b1). Its way out is neither "reassign" nor "ask
	// the blocker" — the item is waiting on a DECISION, and the decision's own
	// procedure files whatever gets dispatched next. So the message points at
	// the stage line rather than at the assignee field, and names the one case
	// where moving the stage BACK is the right move: re-triage, which is
	// `stage: triage` by definition.
	if g.Stage != "" {
		return fmt.Sprintf("work item %s carries `stage: %s` in the state carrier block leading "+
			"its body: it is parked at a HUMAN DECISION GATE and is gated away from automatic "+
			"dispatch whatever its assignee says. No worker advances it — the gate decision does, "+
			"and that decision files what gets dispatched next. If you are re-running triage, set "+
			"the stage back to `triage` first (that is what re-triage means); if the decision has "+
			"been made, move the stage on",
			workItemID, g.Stage)
	}
	// The unreadable carrier (mg-27d4). The way out is neither a decision nor a
	// reassignment — it is an EDIT, and it is a one-line move, so the message
	// says exactly where the block goes rather than describing the parse rule.
	// The reader is usually an agent that has to fix this without a human.
	if g.CarrierUnreadable {
		return fmt.Sprintf("work item %s declares `workflow:`/`stage:` lines that the carrier-block "+
			"parser cannot reach: the block must be the FIRST non-blank content under the `# ` title "+
			"heading, and this item has something else there — a lead-in sentence, a PR link, or the "+
			"block written above the heading instead of below it. It is gated because the parser "+
			"cannot tell whether it says `stage: gated`, and an item whose gate cannot be read is not "+
			"dispatched. Fix it by moving the block directly under the title heading (`mg edit %s "+
			"--body=...`, preserving the rest of the body); the refusal clears as soon as the block "+
			"leads the body",
			workItemID, workItemID)
	}
	assignee := g.Assignee
	// The `blocked:<agent>` shape gets its own sentence (mg-6fb0). It gates for a
	// reason the sentinel vocabulary cannot state — the item is waiting on a NAMED
	// agent — and a refusal that flattened it to "non_dispatchable_assignees" would
	// throw away the one thing the author took the trouble to record. The way out
	// is different too: an item blocked on mayor is not "done by hand or
	// unparked", it is unblocked by mayor.
	if who, ok := config.BlockedOn(assignee); ok {
		if who == "" {
			return fmt.Sprintf("work item %s is assigned to %q — the `blocked:<agent>` shape, which "+
				"gates it away from automatic dispatch, but it names no agent. Rewrite it as "+
				"blocked:<agent> so the item says who it is waiting on, or clear the assignee to "+
				"dispatch a worker onto it", workItemID, assignee)
		}
		return fmt.Sprintf("work item %s is assigned to %q: it is BLOCKED ON %s, not merely owned by "+
			"them, and the `blocked:` shape gates it away from automatic dispatch. Ask %s to unblock "+
			"it, then reassign or clear the assignee to dispatch a worker onto it",
			workItemID, assignee, who, who)
	}
	return fmt.Sprintf("work item %s is assigned to %q, which is gated away from automatic "+
		"dispatch (non_dispatchable_assignees); it must be done by hand or unparked. "+
		"Reassign it or clear its assignee to dispatch a worker onto it",
		workItemID, assignee)
}
