package workitem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The state carrier block (mg-69b1). `stage:` stopped being decoration the day
// it became a dispatch gate, so these tests pin what counts as a DECLARATION —
// and, more importantly, what does not.
//
// The negative cases carry the weight. A body-wide search for "stage: gated"
// would pass every positive case here and still be wrong, because bodies discuss
// stages: the ticket that made the stage load-bearing quotes the transition it
// is complaining about, indented, inside a paragraph. See
// TestCarrierBlockIgnoresProseThatQuotesIt.

func parseBody(t *testing.T, body string) WorkItem {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mg-test.md")
	content := "---\nid: mg-test\ntype: task\nassignee: \"\"\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := parseWorkItem(path, "available")
	if err != nil {
		t.Fatalf("parseWorkItem: %v", err)
	}
	return item
}

func TestCarrierBlockParsed(t *testing.T) {
	tests := []struct {
		name              string
		body              string
		wantWorkflow      string
		wantStage         string
		wantTitleContains string
	}{
		{
			name: "the shipped shape, straight off a live gh-issue carrier",
			body: `# triage: pogod log rotation is a silent no-op (drellem2/pogo#104)
workflow: gh-issue
stage: gated
gh: drellem2/pogo#104

Triage this GitHub issue: investigate the codebase and produce a packet.
`,
			wantWorkflow:      "gh-issue",
			wantStage:         "gated",
			wantTitleContains: "log rotation",
		},
		{
			name: "a blank line under the heading still parses",
			body: `# build: something

workflow: gh-issue
stage: build
gh: drellem2/pogo#106
`,
			wantWorkflow: "gh-issue",
			wantStage:    "build",
		},
		{
			name: "hand-edited capitalisation and spacing",
			body: `# triage: something
Workflow:   gh-issue
Stage:  Gated
`,
			wantWorkflow: "gh-issue",
			wantStage:    "Gated",
		},
		{
			name: "a stage with no workflow line still declares a stage",
			body: `# triage: something
stage: gated
`,
			wantStage: "gated",
		},
		{
			name:      "no carrier block at all — the ordinary item",
			body:      "# Add user authentication\n\nDo the thing.\n",
			wantStage: "",
		},
		{
			name: "a prose line ends the block before the stage line is reached",
			body: `# triage: something
Reported: the daemon wedged overnight.
stage: gated
`,
			wantStage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := parseBody(t, tt.body)
			if item.Workflow != tt.wantWorkflow {
				t.Errorf("Workflow = %q, want %q", item.Workflow, tt.wantWorkflow)
			}
			if item.Stage != tt.wantStage {
				t.Errorf("Stage = %q, want %q", item.Stage, tt.wantStage)
			}
			if tt.wantTitleContains != "" && !strings.Contains(item.Title, tt.wantTitleContains) {
				t.Errorf("Title = %q, want it to contain %q", item.Title, tt.wantTitleContains)
			}
		})
	}
}

// TestCarrierBlockIgnoresProseThatQuotesIt is the red arm of this change, and it
// is not hypothetical: the body below is mg-69b1's own, the ticket that made
// `stage: gated` gate dispatch. It quotes the playbook line it is complaining
// about. A parser that searched the body for the string would have gated the bug
// report about the gate — an item nobody could then dispatch a fix from.
func TestCarrierBlockIgnoresProseThatQuotesIt(t *testing.T) {
	item := parseBody(t, "# A gh-issue carrier at stage: gated is still dispatchable\n"+
		`OBSERVED LIVE 2026-08-09, not reasoned from the text. Three gh-issue carriers
sat at `+"`stage: gated`"+` awaiting a GO/NO-GO.

THE DEFECT. The GH-Issue Workflow playbook's transition 2 says only:

  "When the triage packet arrives, set `+"`stage: gated`"+` and send Daniel the triage +
   recommendation summary."
`)

	if item.Stage != "" {
		t.Errorf("Stage = %q from a body that only QUOTES the carrier line; "+
			"a prose mention is not a declaration and must not gate", item.Stage)
	}
	if item.Workflow != "" {
		t.Errorf("Workflow = %q, want empty", item.Workflow)
	}
}

// The scan must not read the whole file looking for a block that is not there.
// listFrom parses every item in done/, which grows without bound, and done items
// carry the largest bodies in the store (a retired triage ticket holds its whole
// packet).
func TestCarrierBlockScanIsBounded(t *testing.T) {
	body := "# a body that is nothing but carrier-shaped lines\n"
	for i := 0; i < carrierBlockScanLimit*4; i++ {
		body += "filler: value\n"
	}
	body += "stage: gated\n"

	if got := parseBody(t, body).Stage; got != "" {
		t.Errorf("Stage = %q; a stage line past the %d-line scan limit must not be read",
			got, carrierBlockScanLimit)
	}
}
