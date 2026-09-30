package agent

import (
	"net/http"
	"strings"
	"testing"
)

// TestSpawnRefusesAWorktreePolecatOnPogoHome is mg-752a3's dispatch half: a
// polecat cut from $POGO_HOME has one exit, a refinery submit, and the
// refinery refuses that repo — so the spawn is refused up front, through the
// real handler, before a worker is spent.
func TestSpawnRefusesAWorktreePolecatOnPogoHome(t *testing.T) {
	live := installPolecatTemplate(t) // sets POGO_HOME to the tree holding the template

	rr := spawnPolecat(t, newDrainTestRegistry(t), SpawnPolecatAPIRequest{
		Name: "q752a", Id: "mg-752a", Repo: live + "/", Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("worktree spawn on $POGO_HOME: status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	for _, want := range []string{"refusing to spawn a worktree polecat", "$POGO_HOME", "never pulls", "commit directly", "--no-worktree"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("refusal does not say %q: %s", want, rr.Body.String())
		}
	}
}

// TestPogoHomeSpawnRefusalScope pins what the gate does NOT refuse: a sibling
// repo, and an in-place dispatch on the live tree (which is the sanctioned
// write path). Asked of the predicate directly, so no worker is launched.
func TestPogoHomeSpawnRefusalScope(t *testing.T) {
	live := installPolecatTemplate(t)
	sibling := t.TempDir()

	if got := pogoHomeSpawnRefusal(live, true); got == "" {
		t.Fatal("control: a worktree dispatch on $POGO_HOME was not refused")
	}
	if got := pogoHomeSpawnRefusal(sibling, true); got != "" {
		t.Errorf("a worktree dispatch on a sibling repo was refused: %s", got)
	}
	if got := pogoHomeSpawnRefusal(live, false); got != "" {
		t.Errorf("an in-place dispatch on $POGO_HOME was refused: %s", got)
	}
	if got := pogoHomeSpawnRefusal("", true); got != "" {
		t.Errorf("a dispatch with no repo was refused: %s", got)
	}
}
