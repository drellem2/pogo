package agent

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveClaudeLongNudge is the before/after measurement for mg-8a70 against
// the REAL Claude Code binary, through pogo's own Nudge and confirm path. It is
// skipped unless POGO_LIVE_CLAUDE_DIR names a directory claude already trusts
// (so no workspace-trust dialog) and POGO_LIVE_POGO names a pogo binary built
// from this tree (the receipt hook).
//
// It makes NO model call: the UserPromptSubmit hook records the receipt with
// the real `pogo hook prompt-submit` and then exits 2, which makes Claude Code
// discard the prompt instead of sending it.
//
//	go build -o /tmp/pogo-live ./cmd/pogo
//	POGO_LIVE_CLAUDE_DIR=<trusted dir> POGO_LIVE_POGO=/tmp/pogo-live \
//	  go test ./internal/agent -run TestLiveClaudeLongNudge -v -count=1
func TestLiveClaudeLongNudge(t *testing.T) {
	dir, pogoBin := os.Getenv("POGO_LIVE_CLAUDE_DIR"), os.Getenv("POGO_LIVE_POGO")
	if dir == "" || pogoBin == "" {
		t.Skip("live measurement; set POGO_LIVE_CLAUDE_DIR and POGO_LIVE_POGO")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	receipt := filepath.Join(t.TempDir(), "live.submits")
	settings, _ := json.Marshal(map[string]any{
		"skipDangerousModePermissionPrompt": true,
		"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": pogoBin + " hook prompt-submit; echo blocked-by-live-test >&2; exit 2",
		}}}}},
	})

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(5 * time.Second)
	a, err := reg.Spawn(SpawnRequest{
		Name:    "live-long-nudge",
		Type:    TypePolecat,
		Command: []string{claude, "--dangerously-skip-permissions", "--settings", string(settings)},
		// The package's TestMain sandboxes HOME; claude needs the real one
		// (its login and its trust list), which passwd still knows.
		Env: []string{"POGO_SUBMIT_RECEIPT=" + receipt, "TERM=xterm-256color", "HOME=" + realHome(t)},
		Dir: dir,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a.receiptFile = receipt
	deadline := time.Now().Add(60 * time.Second)
	for !a.sawPromptReady() {
		if time.Now().After(deadline) {
			t.Fatalf("claude never showed its composer; screen:\n%s", StripANSI(a.RecentOutput(4000)))
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)

	trials := 3
	for _, chunk := range []int{0, a.nudge.InputChunkBytes} {
		intact, mangled, other := 0, 0, 0
		for i := 0; i < trials; i++ {
			a.nudge.InputChunkBytes = chunk
			err := a.NudgeWithMode(longFireMessage, NudgeConfirm, 9*time.Second)
			recs, _ := ReadSubmits(receipt)
			var got SubmitRecord
			if len(recs) > 0 {
				got = recs[len(recs)-1]
			}
			switch {
			case err == nil:
				intact++
			case errors.Is(err, ErrNudgeMangled):
				mangled++
			default:
				other++
				t.Logf("chunk=%d trial %d: %v", chunk, i, err)
			}
			t.Logf("chunk=%d trial %d: err=%v received %d runes, head %q",
				chunk, i, err, got.Len, strings.SplitN(got.Head, "\n", 2)[0])
			time.Sleep(2 * time.Second)
		}
		t.Logf("InputChunkBytes=%d: confirmed-intact %d, mangled %d, other %d (of %d)",
			chunk, intact, mangled, other, trials)
		if chunk > 0 && intact != trials {
			t.Errorf("with chunking, %d of %d long nudges were not confirmed intact", trials-intact, trials)
		}
	}
}

func realHome(t *testing.T) string {
	u, err := user.Current()
	if err != nil {
		t.Skipf("no passwd entry: %v", err)
	}
	return u.HomeDir
}
