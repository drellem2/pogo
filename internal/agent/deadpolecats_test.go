package agent

import (
	"os/exec"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/gitgc"
)

// TestPolecatOwnerVerdicts_OnlyDeadLicensesRemoval pins the mapping gh #97
// turns on: gitgc.OwnerGone is what lets a sweep destroy a worktree whose `git
// status` FAILED, so it must be reachable from exactly one witness verdict —
// the one the store documents as "positive evidence of death and it is safe to
// reap on".
//
// The three negative arms are not padding. Each is a different way of having
// NOTHING, and each is a plausible mis-mapping:
//
//   - Alive is the obvious one, and the only one nobody would get wrong.
//   - Unreadable is a pid that answers signals with an identity we could not
//     confirm. Something is alive; we cannot say it is ours. That is a
//     cannot-tell, and mapping a cannot-tell to death is the whole defect
//     class this ticket sits in.
//   - NoRecord is the dangerous one, because it is the DEFAULT state of any
//     unwitnessed name and reads like "not there". Absence of a record is
//     absence of evidence; a sweep that read it as death would put every
//     unwitnessed worktree into the destructive arm at once.
func TestPolecatOwnerVerdicts_OnlyDeadLicensesRemoval(t *testing.T) {
	sandboxWitness(t)

	alivePid := liveProcess(t)
	if err := RecordPolecatWitness("alive", alivePid, "mg-fd39"); err != nil {
		t.Fatalf("record alive: %v", err)
	}
	dead := exec.Command("sleep", "600")
	if err := dead.Start(); err != nil {
		t.Fatalf("start dead: %v", err)
	}
	if err := RecordPolecatWitness("dead", dead.Process.Pid, "mg-fd39"); err != nil {
		t.Fatalf("record dead: %v", err)
	}
	_ = dead.Process.Kill()
	_, _ = dead.Process.Wait()

	verdicts, err := PolecatOwnerVerdicts()
	if err != nil {
		t.Fatalf("PolecatOwnerVerdicts: %v", err)
	}
	if verdicts["dead"] != gitgc.OwnerGone {
		t.Errorf("a provably-dead polecat = %v, want %v — nothing else can ever reclaim its unreadable tree",
			verdicts["dead"], gitgc.OwnerGone)
	}
	if verdicts["alive"] != gitgc.OwnerUnproven {
		t.Errorf("a live polecat = %v, want %v", verdicts["alive"], gitgc.OwnerUnproven)
	}

	// WitnessNoRecord: a name the store has never heard of. The map must not
	// answer OwnerGone for it, and the zero value of the type it is read
	// through must not either.
	if got, ok := verdicts["never-witnessed"]; ok || got != gitgc.OwnerUnproven {
		t.Errorf("an unwitnessed name = %v (present=%v), want the OwnerUnproven zero value and no entry — "+
			"no record is not evidence of death", got, ok)
	}
	var zero gitgc.WorktreeOwner
	if zero != gitgc.OwnerUnproven {
		t.Fatalf("gitgc.WorktreeOwner zero value is %v, not OwnerUnproven — every absent key in "+
			"Options.OwnerVerdicts would license a removal", zero)
	}

	// WitnessUnreadable: blind the identity probe so the live pid still answers
	// signals but cannot be confirmed as ours.
	prev := procStartFn
	procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { procStartFn = prev })

	blind, err := PolecatOwnerVerdicts()
	if err != nil {
		t.Fatalf("PolecatOwnerVerdicts (blind): %v", err)
	}
	if blind["alive"] != gitgc.OwnerUnproven {
		t.Errorf("an UNREADABLE polecat = %v, want %v — something is alive on that pid and we could not "+
			"confirm whose; 'cannot tell' must never license destroying files we also could not read",
			blind["alive"], gitgc.OwnerUnproven)
	}
	if blind["dead"] != gitgc.OwnerGone {
		t.Errorf("a pid that holds nothing stays dead regardless of the probe: got %v", blind["dead"])
	}
}
