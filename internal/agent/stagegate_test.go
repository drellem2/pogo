package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// The workflow gate (mg-69b1). These tests are the same shape as
// TestSpawnPolecatRefusedForGatedAssignee and for the same reason: the defect
// was a guard that existed only in prose, so the control that matters is the RED
// one — dispatch actually refused on a known-bad input, not merely allowed on a
// good one.
//
// The known-bad input is not invented. It is the body of a live gh-issue carrier
// as the shipped playbook writes it at the human gate: a triage ticket parked at
// `stage: gated`, `status=available`, and NO ASSIGNEE, because the playbook's
// transition into the gate set the stage and nothing else. Three of those sat in
// the store on 2026-08-09 and the priority wake offered two of them up as "ready
// and unclaimed".

// gateStoreCarrier builds a store holding one gh-issue carrier with the given
// stage and assignee, written the way the playbook writes it: the carrier block
// is BODY, under the title heading, not frontmatter.
func gateStoreCarrier(t *testing.T, id, stage, assignee string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: " + id + "\ntype: task\nassignee: " + assignee +
		"\npriority: high\ntags: [gh-issue, declares-remainder]\n---\n" +
		"# triage: pogod log rotation is a silent no-op (drellem2/pogo#104)\n" +
		"workflow: gh-issue\n" +
		"stage: " + stage + "\n" +
		"gh: drellem2/pogo#104\n\n" +
		"Triage this GitHub issue: investigate the codebase and produce a packet.\n"
	if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSpawnPolecatRefusedAtTheHumanGate(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{
		Root: gateStoreCarrier(t, "mg-e789", config.GatedStage, ""),
	})

	rr := spawnPolecatFor(t, reg, "mg-e789")
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn on a carrier at `stage: gated` with no assignee: status = %d, want 409 — "+
			"this is the mg-69b1 defect: gated in the workflow, ungated in the dispatcher", rr.Code)
	}

	body := rr.Body.String()
	// The refusal has to name the item, the stage that gated it, and the way
	// out. A re-dispatch here posts a SECOND acknowledgement comment on a
	// stranger's open GitHub issue, so the reader needs to know it is not
	// looking at a transient failure to retry.
	for _, want := range []string{"mg-e789", "stage: gated", "HUMAN DECISION GATE", "re-triage"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal missing %q, got: %q", want, body)
		}
	}

	// Nothing left behind: the gate sits above every side effect (mg-ef80).
	if a := reg.Get("cat-gate"); a != nil {
		t.Error("a refused dispatch registered an agent anyway")
	}
}

// The negative half. It does not assert the spawn SUCCEEDS — with no template on
// disk it will not — only that the stage gate is not what stopped it. Without
// this, a gate that refused every carrier would pass the test above while
// breaking the whole gh-issue track.
//
// Every stage but `gated` is here by name, because "which stages gate" is the
// question mg-69b1 asked and each answer is a decision, not an omission: see
// config.IsStageGated for why triage, build, review and merge must all dispatch.
func TestSpawnPolecatAllowedAtEveryOtherStage(t *testing.T) {
	for _, stage := range []string{"triage", "build", "review", "merge"} {
		t.Run(stage, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{
				Root: gateStoreCarrier(t, "mg-open", stage, ""),
			})

			if rr := spawnPolecatFor(t, reg, "mg-open"); rr.Code == http.StatusConflict {
				t.Errorf("stage %q was refused; the gh-issue track dispatches at this stage: %s",
					stage, rr.Body.String())
			}
		})
	}
}

// A gated carrier that ALSO carries a gating assignee refuses with the
// assignee's message. Both are true, and the assignee was set by hand — it
// states an intent the stage cannot and its way out is different ("reassign or
// clear"), so flattening the two would send the reader to the wrong field.
//
// This is the live population as it stands: mg-e789 / mg-a3f0 / mg-b055 were
// hand-gated to `human` when the defect was found, so the first items this code
// meets in production carry both.
func TestSpawnPolecatGatedCarrierWithAssigneeNamesTheAssignee(t *testing.T) {
	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{
		Root: gateStoreCarrier(t, "mg-b055", config.GatedStage, "human"),
	})

	rr := spawnPolecatFor(t, reg, "mg-b055")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, `assigned to "human"`) {
		t.Errorf("a doubly-gated item must refuse with the assignee message, got: %q", body)
	}
}

// TestMayorTeachesTheEnforcedStageGate pins the prose half. The code refuses;
// the prompt is what stops the coordinator writing a workflow around a refusal
// it did not expect, and what tells it the ONE thing it now has to keep
// accurate. mg-4798's lesson applies to the pairing as much as to the gate: a
// rule that lives only in the executable path is enforced but unexplained, and
// the reader's next move after a 409 is to invent a workaround.
func TestMayorTeachesTheEnforcedStageGate(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		// The gate, named where the carrier block is defined.
		"`stage: gated` is a dispatch gate, and pogod enforces it (mg-69b1).",
		// That it is `gated` ALONE — the question mg-69b1 asked, answered in
		// the file so nobody re-derives it from a refusal.
		"`triage`, `build`, `review` and `merge` all dispatch normally",
		// The one operation the enforcement adds, at the one branch that needs
		// it: re-triage moves the stage back.
		"A fresh triage round needs the stage moved back to `triage` first",
		// Ordering at the gate: edit the stage BEFORE stopping the worker,
		// because the claim is what holds the ticket until then.
		"before* you stop the triage {{.Worker}}",

		// The review ticket's own hold (mg-69b1's "check while you are in
		// there"). `stage: review` cannot gate it — that is the stage it must be
		// dispatched in — so the hold is an assignee, in the settled
		// `blocked:<agent>` vocabulary, and transition 4 clears it.
		"--assignee=blocked:{{.Coordinator}} \\",
		`mg edit <review ticket id> --assignee=""`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing gh-issue gate guidance %q", want)
		}
	}

	// The regression: the review ticket filed with nothing holding it but the
	// coordinator's intention. "gated by hand" was the honest description of a
	// hold that stopped nothing.
	if strings.Contains(body, "Dispatch ordering is gated by hand instead") {
		t.Error("mayor.md: the review ticket is back to being held by hand only (mg-69b1)")
	}
}

// The gate reads a DECLARATION, not a mention. An ordinary ticket whose body
// discusses the gate — every ticket about this defect, starting with mg-69b1
// itself — must stay dispatchable, or the first casualty of the fix is the work
// to improve it.
func TestSpawnPolecatNotGatedByProseAboutTheGate(t *testing.T) {
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: mg-69b1\ntype: task\nassignee: \"\"\npriority: high\n---\n" +
		"# A gh-issue carrier at stage: gated is still dispatchable\n" +
		"OBSERVED LIVE 2026-08-09. Three gh-issue carriers sat at `stage: gated`\n" +
		"awaiting a GO/NO-GO. The playbook's transition 2 says only:\n\n" +
		"  \"When the triage packet arrives, set `stage: gated` and send Daniel\n" +
		"   the triage + recommendation summary.\"\n"
	if err := os.WriteFile(filepath.Join(avail, "mg-69b1.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := newDrainTestRegistry(t)
	reg.SetDispatchGate(MGDispatchGate{Root: root})
	if rr := spawnPolecatFor(t, reg, "mg-69b1"); rr.Code == http.StatusConflict {
		t.Errorf("a ticket that only QUOTES the carrier line was gated by it: %s", rr.Body.String())
	}
}
