package agent

// A ratchet tying the coordinator prompt's stated auto-renudge budget to the
// constants that actually produce it (mg-eb54).
//
// WHAT THIS GUARDS. mg-feb3 moved first-start recovery into pogod, and mg-eb54
// retired the coordinator's manual "nudge an unstarted worker at ~30-60s"
// workaround that had been shadowing it. The retirement only holds if the
// prompt's number is RIGHT: the whole instruction is "do not nudge inside the
// ~75s window", and 75s is not a fact about the world — it is
// DefaultStartVerifyDelay × DefaultStartVerifyMaxAttempts. Halve the delay and
// the prompt still says 75s, so the coordinator waits out a window that closed
// at 37s. Raise it and the coordinator nudges inside a live recovery, which is
// exactly the failure being retired: a manual keystroke into an agent that is
// mid-recovery, and an outcome nobody can read afterwards because the
// workaround validated itself.
//
// WHY A RATCHET AND NOT A SWEEP. The two constants are ordinary tuning knobs
// with a plausible reason to move (a slower harness, a noisier fleet). Whoever
// moves them is editing Go and has no reason to grep a prompt tree — this
// failure arrives through a change that is locally correct. A ratchet fails at
// the moment of authoring, in the package being edited, which is the only place
// the author is looking.
//
// IT SHIPS DEMONSTRATED-ABLE-TO-FAIL. This ticket's own record is three
// instruments that returned a clean, plausible, entirely false result — a zsh
// word-split that made a polling loop structurally incapable of firing and so
// reported every polecat unstarted, a `>/dev/null` that destroyed the error
// being investigated, and a spawn that failed with rc=0. A check convened to
// protect a measurement has no business asserting anything until it has been
// shown to fail on a corpus that should fail it, so renudgeBudgetProblems is
// exercised in both directions below.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// coordinatorPromptPath is the shipped coordinator template. It is the only
// prompt that states the budget, because it is the only agent that decides
// whether to nudge an unstarted worker.
const coordinatorPromptPath = "prompts/mayor.md"

// renudgeBudgetProblems reports every way body's account of the auto-renudge
// budget disagrees with the constants, plus the reintroduction of the retired
// workaround. Returned rather than t.Errorf'd so the same predicate can be run
// against a deliberately-broken fixture and required to fire.
func renudgeBudgetProblems(body string) []string {
	delaySecs := int(DefaultStartVerifyDelay / time.Second)
	budgetSecs := delaySecs * DefaultStartVerifyMaxAttempts

	var problems []string
	for _, want := range []string{
		// The mechanism, in the shape a reader can check against the log:
		// N attempts, D apart.
		fmt.Sprintf("%d attempts, %ds apart", DefaultStartVerifyMaxAttempts, delaySecs),
		// The total, which is the number the do-not-nudge rule is about.
		fmt.Sprintf("~%ds budget", budgetSecs),
		// And the rule itself, stated against that same total. A prompt that
		// names the budget but never tells the coordinator to stay out of it
		// has documented the daemon without retiring the workaround.
		fmt.Sprintf("~%ds window", budgetSecs),
	} {
		if !strings.Contains(body, want) {
			problems = append(problems, fmt.Sprintf("missing %q — the prompt's auto-renudge budget no longer matches DefaultStartVerifyDelay(%s) × DefaultStartVerifyMaxAttempts(%d)", want, DefaultStartVerifyDelay, DefaultStartVerifyMaxAttempts))
		}
	}

	// The retired workaround, verbatim. It came back by copy once already
	// across the prompt corpus (mg-96ad); this is the cheap guard against it
	// coming back here.
	if strings.Contains(body, "hasn't claimed its work item within ~30-60 seconds") {
		problems = append(problems, "the retired ~30-60s manual unstarted-worker nudge is back; pogod owns first-start recovery since mg-feb3 (mg-eb54)")
	}

	return problems
}

// TestRenudgeBudget_PromptMatchesConstants is the ratchet: the shipped
// coordinator prompt must state the budget the code actually enforces.
func TestRenudgeBudget_PromptMatchesConstants(t *testing.T) {
	data, err := defaultPrompts.ReadFile(coordinatorPromptPath)
	if err != nil {
		t.Fatalf("read embedded %s: %v", coordinatorPromptPath, err)
	}
	for _, p := range renudgeBudgetProblems(string(data)) {
		t.Errorf("%s: %s", coordinatorPromptPath, p)
	}
}

// TestRenudgeBudget_CheckCanFail proves the predicate fires, in both
// directions, before anything is concluded from its silence.
func TestRenudgeBudget_CheckCanFail(t *testing.T) {
	shipped, err := defaultPrompts.ReadFile(coordinatorPromptPath)
	if err != nil {
		t.Fatalf("read embedded %s: %v", coordinatorPromptPath, err)
	}
	body := string(shipped)

	t.Run("exoneration boundary", func(t *testing.T) {
		// The corrected prose must PASS, or the check punishes its own remedy.
		if problems := renudgeBudgetProblems(body); len(problems) != 0 {
			t.Fatalf("shipped prompt should be clean, got %v", problems)
		}
	})

	t.Run("drifted constant is caught", func(t *testing.T) {
		// Simulate someone halving DefaultStartVerifyDelay without touching the
		// prompt: the prompt keeps the old numbers, which is precisely the
		// silent case, so drift it in the corpus instead.
		delaySecs := int(DefaultStartVerifyDelay / time.Second)
		drifted := strings.ReplaceAll(body,
			fmt.Sprintf("%d attempts, %ds apart", DefaultStartVerifyMaxAttempts, delaySecs),
			fmt.Sprintf("%d attempts, %ds apart", DefaultStartVerifyMaxAttempts, delaySecs/2))
		if drifted == body {
			t.Fatal("fixture did not change — the drift control is not exercising anything")
		}
		if problems := renudgeBudgetProblems(drifted); len(problems) == 0 {
			t.Error("a drifted delay produced no problem; the ratchet cannot see the failure it exists for")
		}
	})

	t.Run("reintroduced workaround is caught", func(t *testing.T) {
		regressed := body + "\n- **Unstarted workers**: If a worker hasn't claimed its work item within ~30-60 seconds, nudge it.\n"
		problems := renudgeBudgetProblems(regressed)
		if len(problems) != 1 || !strings.Contains(problems[0], "retired") {
			t.Errorf("expected exactly one problem naming the retired workaround, got %v", problems)
		}
	})
}
