package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mg-27d4's acceptance test, run through the real spawn handler: file an item
// with a prose line above a `stage: gated` block and prove it is gated anyway.
//
// It is the RED control and that is the whole point of it. Before this change
// the item below spawned a worker without complaint — the parser read its body,
// found the first line under the title was prose, stopped, and reported "no
// stage", which the gate read as "not gated". A guard that has only ever been
// watched on items whose block happens to lead the body has not been watched on
// the case it exists for.
//
// The body is not invented. It is mg-2997's shape, off the live store: a review
// carrier that opens with the PR link and puts the block underneath.

// gateStoreUnreadableCarrier builds a store holding one carrier whose block the
// parser cannot reach, in the layout given.
func gateStoreUnreadableCarrier(t *testing.T, id, body string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	front := "---\nid: " + id + "\ntype: task\nassignee: \"\"\npriority: high\ntags: [gh-issue]\n---\n"
	if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(front+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSpawnPolecatRefusedWhenTheCarrierCannotBeRead(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "a prose line above the block — the ticket's own acceptance case",
			body: "# review: something (drellem2/pogo#83)\n" +
				"PR TO REVIEW: https://github.com/drellem2/pogo/pull/84 (branch polecat-75d9).\n\n" +
				"workflow: gh-issue\nstage: gated\ngh: drellem2/pogo#83\n\n" +
				"Review the PR and mail findings to the build worker.\n",
		},
		{
			name: "the block written above the title heading — mg-779b's live shape",
			body: "workflow: gh-issue\nstage: gated\ngh: drellem2/pogo#100\n\n" +
				"# PARKED awaiting Daniel's GO/NO-GO on gh#100\n\n" +
				"Successor to the mg-2fcc triage.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{
				Root: gateStoreUnreadableCarrier(t, "mg-unrd", tt.body),
			})

			rr := spawnPolecatFor(t, reg, "mg-unrd")
			if rr.Code != http.StatusConflict {
				t.Fatalf("spawn on an item whose `stage: gated` the parser cannot reach: "+
					"status = %d, want 409 — an item whose gate cannot be READ must not be "+
					"dispatched, because it may be the one parked at a human decision", rr.Code)
			}
			// The refusal has to be actionable by the agent that reads it. The fix
			// is a one-line move, so the message must say where the block goes —
			// naming the item and telling the reader to fix the assignee (the other
			// gate's advice) would send them to the wrong field entirely.
			msg := rr.Body.String()
			for _, want := range []string{"mg-unrd", "title", "gate cannot be read"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal does not mention %q; a refusal that cannot say what to "+
						"fix is a bug report, not a guard.\ngot: %s", want, msg)
				}
			}
		})
	}
}

// The green control, and it is not a formality: the fail-closed direction is the
// one that strands work. An ordinary item, and a carrier whose block leads the
// body properly, must still dispatch — otherwise this change would have replaced
// a silent fail-open with a silent refusal of everything.
func TestSpawnPolecatStillAllowedWhenTheCarrierIsReadable(t *testing.T) {
	tests := []struct{ name, body string }{
		{
			name: "an ordinary item with no carrier block at all",
			body: "# Add user authentication\n\nDo the thing.\n",
		},
		{
			name: "a carrier whose block leads the body, at a dispatchable stage",
			body: "# build: something (drellem2/pogo#104)\nworkflow: gh-issue\nstage: build\n" +
				"gh: drellem2/pogo#104\n\nBuild it.\n",
		},
		{
			name: "a body that quotes the carrier convention in prose",
			body: "# A gh-issue carrier at stage: gated is still dispatchable\n" +
				"Three carriers sat at `stage: gated` awaiting a GO/NO-GO.\n\n" +
				"The playbook says: set `stage: gated` and send the summary.\n",
		},
		{
			name: "a body that shows the convention inside a fenced example",
			body: "# design: document the state carrier\nThe block looks like this:\n\n" +
				"```\nworkflow: gh-issue\nstage: gated\n```\n\nThat is the convention.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newDrainTestRegistry(t)
			reg.SetDispatchGate(MGDispatchGate{
				Root: gateStoreUnreadableCarrier(t, "mg-open", tt.body),
			})

			if rr := spawnPolecatFor(t, reg, "mg-open"); rr.Code == http.StatusConflict {
				t.Fatalf("dispatch refused on a readable item: %s", rr.Body.String())
			}
		})
	}
}
