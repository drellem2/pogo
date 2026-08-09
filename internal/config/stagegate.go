package config

import "strings"

// GatedStage is the one state-carrier `stage:` value that gates dispatch: the
// workflow is parked at a HUMAN DECISION and only that decision advances it.
//
// # Why the stage line has to gate at all
//
// The gh-issue playbook in internal/agent/prompts/mayor.md carries workflow
// state as the leading lines of a work item's body:
//
//	workflow: gh-issue
//	stage: triage | gated | build | review | merge
//	gh: <owner>/<repo>#<n>
//
// Before mg-69b1 that line was read by exactly one reader — the coordinator that
// wrote it. Dispatch gated on `assignee` (IsDispatchGated) and nothing else, and
// the playbook's transition into the gate set the stage without setting an
// assignee. So a carrier at `stage: gated` was gated in the WORKFLOW and
// ungated in the DISPATCHER, and the two disagreed silently.
//
// That was observed live on 2026-08-09, not reasoned from the text: three
// gh-issue carriers sat at `stage: gated` awaiting a GO/NO-GO, all reading
// `status=available, assignee=[]`, and the priority wake offered two of them to
// the coordinator as "ready and unclaimed — claim or dispatch now".
//
// The exposure is latent in the worst possible way. While the triage worker
// runs, ITS CLAIM gates the item and nothing is visible; the window opens the
// instant that worker is stopped, which is precisely when the item enters the
// gate. And the gate is by design the longest-lived state in the workflow
// ("silence = HOLD ... however long that takes"), so the window is unbounded and
// it is the one state the workflow expects to sit in.
//
// The harm is reporter-facing, which is what makes this more than bookkeeping.
// A re-dispatched triage worker's first instructed act is to post an
// acknowledgement comment on the GitHub issue — so a second ack lands on a
// stranger's open issue, and triage re-runs on something already at the human
// gate, by following the shipped playbook exactly.
//
// # Why a stage and not another assignee sentinel
//
// The stopgap ("the playbook ALSO sets --assignee=human, and clears it on GO")
// works, and it is one more instruction a coordinator must not forget — in a
// ticket that exists because an instruction was forgettable, plus a second one
// to un-forget on the way out. Deriving the gate from the line the workflow
// already sets means the gate cannot be half-applied: there is one thing to
// write and it means one thing to both readers.
const GatedStage = "gated"

// IsStageGated reports whether a state-carrier `stage:` value gates an item away
// from AUTOMATIC dispatch. Matching is case-insensitive and whitespace-trimmed,
// mirroring IsDispatchGated, so a hand-edited `Stage: Gated` still gates.
//
// # Only `gated` gates, and the other stages are not oversights
//
// mg-69b1 asked explicitly whether the exposure extends to the other stages.
// Each was checked and each is deliberately dispatchable:
//
//   - `triage` — triage has not happened yet. A re-dispatch here is the RECOVERY
//     the state is for: if the triage worker died, another one should run. (The
//     duplicate-ack risk belongs to the worker, which can look for its own ack
//     before posting; it is not a reason to refuse the work.)
//   - `build`, `merge` — the item is claimed by the worker doing that work, and
//     if that worker dies, re-dispatching the build IS the intended repair.
//     Nothing external has been promised that a second run would duplicate.
//   - `review` — the review ticket must be dispatched WHILE it reads
//     `stage: review`; gating on the stage would refuse the one dispatch the
//     stage exists to receive. Its hold ("not until the PR exists") is a
//     different condition with a different instrument — see the review ticket's
//     `--assignee=blocked:<coordinator>` self-gate in mayor.md transition 3,
//     which is IsDispatchGated's job, not this one.
//
// So the vocabulary is one word, and it stays one word until some workflow
// invents a second stage that means "a human must decide before this moves".
//
// # Not configurable, unlike the assignee vocabulary
//
// non_dispatchable_assignees is a deployment's to redefine because "who may not
// execute work here" is a local policy question. A stage is not: it is a
// position in a workflow that a prompt defines, and a deployment that does not
// run that workflow has no `stage:` lines to gate. A config key would be a knob
// whose only setting is the one it ships with.
//
// # Failure direction
//
// The predicate itself is total, but its caller's is not: MGDispatchGate reads
// the stage from a work item it positively parsed off disk, and everything else
// — no id, no store, no carrier block, an unparseable body — reads as "no
// stage" and lets the spawn proceed. That matches the assignee gate's documented
// asymmetry, and for the same reason: failing closed on a missing store refuses
// every legitimate spawn on the day one macguffin path goes bad.
func IsStageGated(stage string) bool {
	return strings.EqualFold(strings.TrimSpace(stage), GatedStage)
}
