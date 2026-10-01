package scheduler

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// retiredLatenessClaim is the parenthetical the lateness line carried until
// drellem2/pogo#183. The 4h19m was the 2026-08-19 fire's latency, a literal —
// but "measured gap between sent and read" presented it as this fire's, on every
// fire, so a punctual fire read as 4h19m late. Nothing at send time can measure
// sent→read.
const retiredLatenessClaim = "measured gap between sent and read"

// collapseSpace folds every run of whitespace to one space, so a copy wrapped
// across lines (ARCHITECTURE.md's indented block) matches like a one-liner.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestLatenessLine_QuotedCopiesDoNotClaimAMeasurement pins the retired claim out
// of every place that QUOTES the lateness line to a reader: the shipped prompts
// (each of which shows an agent what a fire looks like), ARCHITECTURE.md, and
// `pogo schedule ack --help`. Those copies are what an agent learns the footer
// from, so a twelfth copy that reintroduced "measured gap" would teach the
// misreading #183 removed, whatever deliverer.go says.
//
// Positive control: the same walk must find the NEW wording in at least as many
// prompt files as quote the line — 8 when this was written, 2 since mg-aa74
// removed the scheduler-fire section from the six polecat templates (polecats
// no longer receive scheduler fires) — or a broken walk — wrong FS, renamed
// directory — would pass by finding nothing.
func TestLatenessLine_QuotedCopiesDoNotClaimAMeasurement(t *testing.T) {
	const current = "(on 2026-08-19 a fire sent 10s late was not read for 4h19m)"

	quoting := 0
	err := fs.WalkDir(agent.DefaultPromptsFS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, readErr := fs.ReadFile(agent.DefaultPromptsFS(), path)
		if readErr != nil {
			return readErr
		}
		text := collapseSpace(string(data))
		if strings.Contains(text, retiredLatenessClaim) {
			t.Errorf("prompts/%s still presents 4h19m as a %q (drellem2/pogo#183)", path, retiredLatenessClaim)
		}
		if strings.Contains(text, current) {
			quoting++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk prompts: %v", err)
	}
	if quoting < 2 {
		t.Fatalf("found the current lateness wording in only %d prompt files, want >= 2 — the walk is broken, not the prompts", quoting)
	}

	for _, rel := range []string{"ARCHITECTURE.md", filepath.Join("cmd", "pogo", "main.go")} {
		data, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := collapseSpace(string(data))
		if strings.Contains(text, retiredLatenessClaim) {
			t.Errorf("%s still presents 4h19m as a %q (drellem2/pogo#183)", rel, retiredLatenessClaim)
		}
		if !strings.Contains(text, current) {
			t.Errorf("%s no longer quotes the current lateness wording %q — positive control for the check above", rel, current)
		}
	}
}
