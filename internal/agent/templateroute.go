package agent

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/workitem"
)

// The `type` → worker-template routing map, and the CLOSED gate that enforces
// it at the dispatch point.
//
// This is the mg-4798 ruling one rule over from where dispatchgate.go applied
// it, and the argument is the same argument. The rule — a work item's `type`
// field picks which worker template it is dispatched with — existed only as a
// markdown table in internal/agent/prompts/mayor.md, pinned by a prompt test.
// Nothing in Go knew it. So the guarantee that a `type: design` item reached the
// architect rather than the builder was that an agent had read and retained a
// three-row table 170 lines from the command it modifies.
//
// A rule belongs in the executable path at the point of decision. Hence this
// file: ONE map, consulted by handleSpawnPolecat, serving both callers that
// dispatch — the coordinator dispatching by hand today, and an automated slinger
// dispatching on a cron tomorrow (mg-e65e). Neither carries its own copy; a
// second implementation is exactly the drift mg-4798 named.
//
// # Why the map is CLOSED, which is the load-bearing half
//
// An unmapped type produces NO template and refuses the dispatch. It does not
// fall back to the build worker. That is the opposite of what the prose said
// ("anything else (default `task`) — omit --template") and it is deliberate,
// because the two misroutes are not symmetric:
//
//   - A design item sent to a build worker implements something nobody decided,
//     opens a PR, and the refinery merges it. The design question gets answered
//     by whatever happened to get built. SILENT, AND IT LANDS CODE.
//   - A build item sent to an architect returns a recommendation and costs one
//     cycle. LOUD AND HARMLESS.
//
// Defaulting to build converts the loud failure into the silent one. A default
// is a guess, and this map guesses about nothing: `scoping`, `audit`, `bug`, and
// bare `task` are all unmapped, and all of them refuse.
//
// Note the direction is the reverse of MGDispatchGate.DispatchGated, which fails
// OPEN, and the difference is not an inconsistency. That gate answers "may this
// item be dispatched at all"; guessing wrong there refuses legitimate work and
// one unreadable store halts the fleet. This gate answers "which worker does it
// get"; guessing wrong here silently lands the wrong kind of work. Refusing
// costs a named 409 that says what to pass instead.
//
// # The human override
//
// An explicit --template ALWAYS wins and is never routed. A person (or the
// coordinator acting by hand) can dispatch anything at any template, including
// the build worker for an unmapped type. What cannot happen is an automated
// caller that supplies no --template silently receiving the build worker. The
// override is the escape hatch; the refusal is the default path.
//
// The gh-issue stage templates (polecat-triage / polecat-build-pr /
// polecat-review) route on the `workflow: gh-issue` body marker and a stage, not
// on `type`, and are dispatched with an explicit --template. They come through
// the override path and are unaffected by this map.
//
// # Growing the map
//
// It grows by a code change plus a test, not by config. "Closed" is the point:
// a vocabulary an operator can extend at runtime is one an operator can extend
// by accident, and every entry here is a claim that a whole template's worth of
// instructions is the right thing to hand that kind of work.

// workerTemplateForType is THE type→template routing map. It is closed: a type
// absent from it has no template, and dispatch is refused rather than defaulted.
//
// Keys are lower-case; TemplateForType normalizes before looking up.
var workerTemplateForType = map[string]string{
	"design": "polecat-architect",
	"qa":     "polecat-qa",
}

// BuildWorkerTemplate is the general-purpose build worker's template name.
//
// It is NOT a default and must not be used as one — it is here so the refusal
// message can name the flag that gets you it, and so the "spawn me a builder"
// literal has one home rather than being retyped at each call site. Nothing in
// the routing path falls back to it.
const BuildWorkerTemplate = "polecat"

// TemplateForType returns the worker template mapped to a work item's `type`,
// and whether the type is mapped at all. An unmapped or empty type returns
// ("", false) — no template, which the caller must treat as a refusal.
//
// Matching is case-insensitive and whitespace-trimmed, mirroring
// config.IsDispatchGated, so a "Design" or " design " frontmatter value routes.
func TemplateForType(itemType string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(itemType))
	if t == "" {
		return "", false
	}
	tmpl, ok := workerTemplateForType[t]
	return tmpl, ok
}

// MappedTypes returns the routed types in sorted order, for refusal messages
// and help text. Callers get the live map, so a message can never name a
// vocabulary the router does not actually implement.
func MappedTypes() []string {
	types := make([]string, 0, len(workerTemplateForType))
	for t := range workerTemplateForType {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// mappedRoutes renders the map as "design→polecat-architect, qa→polecat-qa" for
// a refusal message.
func mappedRoutes() string {
	types := MappedTypes()
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, t+"→"+workerTemplateForType[t])
	}
	return strings.Join(parts, ", ")
}

// WorkItemTyper answers what `type` marker a work item carries, at the moment of
// dispatch.
//
// A separate interface from DispatchGate rather than a method bolted onto it:
// the two ask different questions of the same file ("may this be dispatched at
// all" vs "what worker does it get"), they fail in opposite directions, and
// mg-ebb0's gate is load-bearing enough that widening its interface to carry an
// unrelated verdict is not worth the coupling.
type WorkItemTyper interface {
	// WorkItemType returns the item's `type` and whether the item was read at
	// all. A found item with no `type` field returns ("", true) — present but
	// untyped, which is unmapped and therefore refused, same as an unknown type.
	WorkItemType(workItemID string) (itemType string, found bool)
}

// MGWorkItemTyper is the production WorkItemTyper: it reads the work item out of
// the macguffin store and reports its `type` frontmatter field.
type MGWorkItemTyper struct {
	// Root overrides the macguffin store location. Empty resolves via
	// macguffinStoreRoot, which under a test binary is a throwaway temp store
	// rather than the live one.
	Root string
}

// WorkItemType implements WorkItemTyper.
//
// Every failure to read reports found=false, which the caller turns into a
// refusal — the closed direction. An id that was never supplied, an item absent
// from the store, and an unreadable store all mean "this dispatcher cannot say
// what kind of work this is", and dispatching a worker anyway is precisely the
// guess the map exists to prevent.
func (m MGWorkItemTyper) WorkItemType(workItemID string) (string, bool) {
	if workItemID == "" {
		return "", false
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return "", false
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), workItemID)
	if err != nil {
		log.Printf("template router: could not read work item %s from %s: %v — "+
			"REFUSING to pick a worker template for it; pass --template explicitly "+
			"to dispatch anyway", workItemID, root, err)
		return "", false
	}
	if !found {
		return "", false
	}
	return item.Type, true
}

// SetWorkItemTyper installs the type reader consulted when a dispatch supplies
// no --template. Passing nil restores the default, MGWorkItemTyper{} — which is
// functional, not a no-op, for the same reason SetDispatchGate's default is.
func (r *Registry) SetWorkItemTyper(t WorkItemTyper) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workItemTyper = t
}

func (r *Registry) getWorkItemTyper() WorkItemTyper {
	r.mu.RLock()
	t := r.workItemTyper
	r.mu.RUnlock()
	if t == nil {
		return MGWorkItemTyper{}
	}
	return t
}

// resolveDispatchTemplate decides which worker template a spawn request gets.
// It returns (template, "") when a template is determined, and ("", refusal)
// when the closed map produces none — in which case the caller must refuse the
// spawn rather than substitute anything.
//
// Precedence is exactly two rules:
//
//  1. An explicit template on the request wins, always, unrouted. This is the
//     hand-dispatch override.
//  2. Otherwise the work item's `type` is read and routed through the closed
//     map. No match, no item, no id ⇒ no template.
func (r *Registry) resolveDispatchTemplate(spawnReq SpawnPolecatAPIRequest) (string, string) {
	if tmpl := strings.TrimSpace(spawnReq.Template); tmpl != "" {
		return tmpl, ""
	}

	how := fmt.Sprintf("Routed types: %s. Pass --template=%s for the general-purpose build worker, "+
		"or --template=<name> for any other template", mappedRoutes(), BuildWorkerTemplate)

	if spawnReq.Id == "" {
		return "", "no --template was given and no --id was given, so there is no work item " +
			"`type` to route on; refusing to guess a worker template. " + how
	}

	itemType, found := r.getWorkItemTyper().WorkItemType(spawnReq.Id)
	if !found {
		return "", fmt.Sprintf("no --template was given and work item %s could not be read, "+
			"so there is no `type` to route on; refusing to guess a worker template. %s",
			spawnReq.Id, how)
	}

	tmpl, ok := TemplateForType(itemType)
	if !ok {
		named := fmt.Sprintf("type %q", itemType)
		if strings.TrimSpace(itemType) == "" {
			named = "no `type` field"
		}
		return "", fmt.Sprintf("work item %s has %s, which is not in the worker-template routing map, "+
			"so no template is selected and this dispatch is refused. This is deliberate: an unrouted "+
			"type does NOT fall back to the build worker, because a design item silently built and "+
			"merged is worse than a dispatch that stops and says so. %s",
			spawnReq.Id, named, how)
	}
	return tmpl, ""
}
