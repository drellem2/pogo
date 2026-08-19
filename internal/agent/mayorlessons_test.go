package agent

import (
	"strings"
	"testing"
)

// The mg-bb99 guards: two lessons that had survived only as HAND EDITS to this
// box's installed `~/.pogo/agents/mayor.md`, present in no embed and therefore
// on no other deployment.
//
// The cost was not only that the other boxes lacked them. A local edit is what
// makes pogod DECLINE a shipped prompt update and divert it to `mayor.md.dist`
// — which is the mg-3ebe / mg-c3f0 failure, a coordinator running 13-day-stale
// guidance and finding out by accident. So the two paragraphs blocked every
// future change to this file for as long as they stayed local, and each was
// re-lost the moment somebody reinstalled the prompt.
//
// Both assertions below pin PLACEMENT, not just presence, because placement is
// what each lesson is about: one has to reach the dispatch body, the other has
// to reach the completed-worker sweep. A paragraph that survives an edit by
// being relocated to the bottom of the file passes a `strings.Contains` and
// teaches nobody anything at the moment they need it.

// mustMayorPrompt returns the embedded coordinator prompt.
func mustMayorPrompt(t *testing.T) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	return string(data)
}

// A `declares-remainder` item that merges without a successor leaves a FINISHED
// item sitting `available`, drawing stall-watch notices, and the worker is
// reaped at merge so only the coordinator can clear it. The dispatch body is a
// snapshot — it is the only channel that reaches the worker in time, which is
// why this warning has to sit in the spawn step and nowhere else. Cost twice on
// 2026-08-13 (mg-6e4f, mg-4020).
func TestMayorPromptWarnsAboutDeclaresRemainderAtDispatch(t *testing.T) {
	s := mustMayorPrompt(t)

	const warning = "If the item is tagged `declares-remainder`, say so in the body you pass."
	warn := strings.Index(s, warning)
	if warn < 0 {
		t.Fatalf("mayor.md: the dispatch-time `declares-remainder` warning is missing — a worker "+
			"dispatched onto such an item learns of the tag only when `mg done` refuses it, and by "+
			"then its branch has merged (mg-6e4f, mg-4020); wanted %q", warning)
	}

	// Placement: inside the spawn step, before the already-working check. The
	// section bounds are what make this a placement assertion rather than a
	// second presence one.
	spawn := strings.Index(s, "### 2. Spawn {{.Worker}}s for ready work")
	health := strings.Index(s, "### 3. Check agent health")
	if spawn < 0 || health < 0 {
		t.Fatalf("mayor.md: could not bound the spawn step (spawn=%d health=%d)", spawn, health)
	}
	if warn < spawn || warn > health {
		t.Errorf("mayor.md: the `declares-remainder` warning is outside the spawn step "+
			"(at %d, step runs %d..%d) — the dispatch body is the only channel that reaches "+
			"the worker in time, so a warning the coordinator reads elsewhere is too late", warn, spawn, health)
	}
	if anchor := strings.Index(s, "Before spawning, check that no {{.Worker}} is already working"); anchor >= 0 && warn > anchor {
		t.Errorf("mayor.md: the `declares-remainder` warning sits after the already-working check "+
			"(warning=%d anchor=%d); it belongs with the body the coordinator is composing", warn, anchor)
	}

	// The two incidents are the evidence. Without them the paragraph reads as
	// a preference and gets edited out as verbosity.
	for _, ref := range []string{"mg-6e4f", "mg-4020"} {
		if !strings.Contains(s[spawn:health], ref) {
			t.Errorf("mayor.md: the spawn step does not cite %s — the warning loses the incident "+
				"that earned it", ref)
		}
	}
}

// pogod mails the filer itself on the merge and self-close routes, so the
// coordinator's obligation here is exactly the residue: the paths where the
// template forbids the worker to close its own item, of which triage is the
// clearest (drellem2/pogo#144, mg-1d9e). That carve-out is real and was
// re-verified for mg-bb99 — the self-close notice comes off the done-reaper,
// which only inspects LIVE workers (agent.Registry.PolecatActivityAt skips
// !alive()), so a close the coordinator performs after stopping the worker
// reaches nobody. See TestACoordinatorCloseWithNoLiveWorkerTellsNobody in
// cmd/pogod, which pins the daemon half of that claim.
func TestMayorPromptTellsTheFilerTheItemLanded(t *testing.T) {
	s := mustMayorPrompt(t)

	const duty = "If the item's filer is not you, tell them it landed."
	tell := strings.Index(s, duty)
	if tell < 0 {
		t.Fatalf("mayor.md: the filer-notification duty is missing — on the paths pogod does not "+
			"cover (triage most clearly), the agent that commissioned the work is never told it "+
			"landed (drellem2/pogo#144, mg-1d9e); wanted %q", duty)
	}

	// Placement: in the completed-worker cleanup list, after the archive step
	// and before the backstop paragraph. That list is what the coordinator
	// actually walks when a merge lands.
	// Anchored on the list ITEM, not on the command it contains. This bound
	// used to read `strings.Index(s, "mg archive --days=0")`, which was the
	// archive step's only distinctive string until mg-c2e1 replaced that
	// prescription with the per-id form. The test kept passing afterwards —
	// but only because the prose that FORBIDS the mass form still contains the
	// literal, several paragraphs further down. The bound had quietly stopped
	// meaning "the start of the cleanup list" and started meaning "wherever the
	// prohibition happens to sit", which is a silent pass waiting for the next
	// edit to that paragraph.
	archive := strings.Index(s, "1. Archive the work item")
	backstop := strings.Index(s, "You are the **backstop** for {{.Worker}}s")
	if archive < 0 || backstop < 0 {
		t.Fatalf("mayor.md: could not bound the completed-worker list (archive=%d backstop=%d)",
			archive, backstop)
	}
	if tell < archive || tell > backstop {
		t.Errorf("mayor.md: the filer-notification duty is outside the completed-worker cleanup "+
			"list (at %d, list runs %d..%d) — it is a step in that sweep, not general advice", tell, archive, backstop)
	}

	item := s[archive:backstop]
	// `mg show` is how the creator is found; without it the duty names no
	// recipient and cannot be carried out.
	if !strings.Contains(item, "mg show") {
		t.Error("mayor.md: the filer-notification duty does not say how to find the creator (`mg show <id>`)")
	}
	// The carve-out is the whole point: without it the coordinator duplicates
	// pogod's own mail on every merge.
	if !strings.Contains(item, "merge and self-close") {
		t.Error("mayor.md: the duty does not name the routes pogod already covers — without the " +
			"carve-out the coordinator re-mails every filer pogod has already mailed")
	}
	if !strings.Contains(item, "live") {
		t.Error("mayor.md: the duty does not say WHY the coordinator's own close is uncovered — " +
			"the self-close notice only inspects live workers, and a reader who does not know that " +
			"cannot tell which of their closes need a mail")
	}
	for _, ref := range []string{"drellem2/pogo#144", "mg-1d9e"} {
		if !strings.Contains(item, ref) {
			t.Errorf("mayor.md: the filer-notification duty does not cite %s", ref)
		}
	}
}
