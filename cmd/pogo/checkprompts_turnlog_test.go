package main

// The turn-completion clause (mg-a270) is prompt text that ships to every crew
// agent, so it belongs under the same CLI-surface control as the shipped
// corpus — but it cannot be reached by that control, because it deliberately
// lives outside the prompts embed. See turnlog.PromptClause for why: two of the
// three prompts it has to reach are user-authored and no installer may write
// them, so the clause is injected by the renderer instead of shipped as a file,
// and promptcli.CheckFS walks files.
//
// The gap matters more here than anywhere in the corpus. A wrong flag in a
// shipped prompt is wrong for the agents that read that prompt; a wrong flag in
// this clause is wrong for every agent on the machine at once, in the one
// instruction whose failure is silent — an agent whose `pogo turn-done`
// invocation is rejected does not stop working, it just stops being visible,
// which is the exact condition the clause exists to end.

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/promptcli"
	"github.com/drellem2/pogo/internal/turnlog"
)

// TestTurnLogClauseMatchesTheCLISurface checks the clause's invocations against
// the real `pogo` built from this revision.
func TestTurnLogClauseMatchesTheCLISurface(t *testing.T) {
	checker, _ := liveChecker(t)
	findings := checker.CheckFile("internal/turnlog/clause.go (PromptClause)", turnlog.PromptClause)
	for _, f := range findings {
		t.Errorf("%s\n    %s", f, strings.TrimSpace(f.Evidence))
	}
}

// TestTurnLogClauseCheckCanFail is that check's positive control: the same
// checker, over the same clause with the flag misspelled, must flag it. Without
// this the test above passes whether or not promptcli can see the clause's text
// at all — the failure mode of a check pointed at nothing.
func TestTurnLogClauseCheckCanFail(t *testing.T) {
	checker, _ := liveChecker(t)
	broken := strings.Replace(turnlog.PromptClause, "pogo turn-done --note=", "pogo turn-done --notes=", 1)
	if broken == turnlog.PromptClause {
		t.Fatal("the clause no longer contains `pogo turn-done --note=`; this control is not exercising anything")
	}
	findings := checker.CheckFile("broken clause", broken)
	var flagged bool
	for _, f := range findings {
		if strings.Contains(f.String(), "notes") {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("the CLI-surface check did not flag `--notes` on `pogo turn-done`; "+
			"a clean result from the real clause therefore means nothing. findings=%v",
			findingStrings(findings))
	}
}

func findingStrings(fs []promptcli.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.String())
	}
	return out
}
