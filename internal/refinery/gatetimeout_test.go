package refinery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGateTimeoutKillsAndReportsWhatItSaw checks the timeout does the two
// things mg-8595 requires of it together: it stops an unbounded wait, AND it
// reports the evidence it acted on. A timeout that only said "deadline
// exceeded" would convert "indistinguishable" into "killed arbitrarily",
// which the ticket calls out explicitly as not a fix.
func TestGateTimeoutKillsAndReportsWhatItSaw(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	// Talks briefly, then hangs: the shape of a gate that wedged partway.
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo starting; sleep 30\"]\ntimeout = \"250ms\"\n")

	mr := &MergeRequest{ID: "mr-timeout", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	start := time.Now()
	out, ran, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a gate that outlives its timeout must fail")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the timeout did not actually stop the wait: took %s", elapsed)
	}
	var te *gateTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected a gateTimeoutError, got %T: %v", err, err)
	}
	if !te.EverSpoke || te.OutputLines != 1 {
		t.Errorf("the error must carry what was observed: EverSpoke=%v lines=%d, want true/1", te.EverSpoke, te.OutputLines)
	}
	msg := err.Error()
	// The wording became "was KILLED at its <timeout> timeout" in mg-e565, so
	// the first line denies the verdict reading a gate timeout used to invite.
	// What this test asserts is unchanged: the bound, the observation and the
	// operator's way out are all still in the message.
	for _, want := range []string{"KILLED at its 250ms timeout", "produced 1 line ", "silent for", "raise [gates] timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout error should contain %q, got: %s", want, msg)
		}
	}
	// The partial output the gate did produce is not thrown away.
	if !strings.Contains(out, "starting") {
		t.Errorf("output captured before the kill must be preserved, got: %s", out)
	}
	if len(ran) != 1 {
		t.Errorf("the timed-out gate must be reported as having run, got %v", ran)
	}
	// The progress record is sealed rather than left mid-beat.
	if mr.Progress == nil || mr.Progress.EndTime.IsZero() {
		t.Error("a timed-out gate must still seal its progress record")
	}
}

// TestGateTimeoutOnASilentGate covers the case the heartbeat cannot resolve on
// its own: a gate that never says anything. The timeout is what bounds it, and
// the error has to say honestly that there was nothing to observe.
func TestGateTimeoutOnASilentGate(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"sleep 30\"]\ntimeout = \"200ms\"\n")

	mr := &MergeRequest{ID: "mr-silent-timeout", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	_, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr)
	if err == nil {
		t.Fatal("expected the silent gate to be killed")
	}
	var te *gateTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected a gateTimeoutError, got %T: %v", err, err)
	}
	if te.EverSpoke {
		t.Error("a gate that produced nothing must not be reported as having spoken")
	}
	if !strings.Contains(err.Error(), "no output at all") {
		t.Errorf("the error must say the gate produced nothing, got: %s", err.Error())
	}
}

// TestGateProgressRecordsTheTimeoutDeadline checks the record tells an
// operator how long the unresolvable wait can last. "Wait or act?" needs a
// bound, not just a timestamp.
func TestGateProgressRecordsTheTimeoutDeadline(t *testing.T) {
	r := newProgressTestRefinery(t, 10*time.Millisecond)
	wtDir := t.TempDir()
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo hi\"]\ntimeout = \"5m\"\n")

	mr := &MergeRequest{ID: "mr-deadline", Status: StatusProcessing}
	r.byID[mr.ID] = mr
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err != nil {
		t.Fatal(err)
	}
	p := mr.Progress
	if p.TimeoutAt.IsZero() {
		t.Fatal("a bounded gate must record when the bound expires")
	}
	if d := time.Until(p.TimeoutAt); d < 4*time.Minute || d > 5*time.Minute {
		t.Errorf("timeout deadline should be ~5m out, got %s", d)
	}

	// With the bound removed, the record must not imply one.
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"echo hi\"]\ntimeout = \"0\"\n")
	mr2 := &MergeRequest{ID: "mr-unbounded", Status: StatusProcessing}
	r.byID[mr2.ID] = mr2
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr2); err != nil {
		t.Fatal(err)
	}
	if !mr2.Progress.TimeoutAt.IsZero() {
		t.Error("an unbounded gate must not record a timeout deadline")
	}
}

func TestParseGateTimeout(t *testing.T) {
	tests := []struct {
		raw   string
		want  time.Duration
		wantK bool
	}{
		{`"45m"`, 45 * time.Minute, true},
		{"45m", 45 * time.Minute, true},
		{"90s", 90 * time.Second, true},
		{"1h30m", 90 * time.Minute, true},
		{"30", 30 * time.Minute, true}, // bare number reads as minutes
		{"0", 0, true},
		{`"off"`, 0, true},
		{"none", 0, true},
		{"DISABLED", 0, true},
		{"", 0, false},
		{"soon", 0, false},
		{"-5m", 0, false}, // a negative bound is a typo, not "immediately"
	}
	for _, tc := range tests {
		got, ok := parseGateTimeout(tc.raw)
		if ok != tc.wantK || got != tc.want {
			t.Errorf("parseGateTimeout(%q) = (%s, %v), want (%s, %v)", tc.raw, got, ok, tc.want, tc.wantK)
		}
	}
}

// TestGateTimeoutConfigPrecedence pins the three states apart: configured,
// configured-as-unbounded, and unconfigured. Collapsing the last two would
// mean an omitted key silently removes the bound.
func TestGateTimeoutConfigPrecedence(t *testing.T) {
	dir := t.TempDir()

	writeGateConfig(t, dir, "[gates]\ncommands = [\"true\"]\n")
	cfg := parseRefineryConfig(filepath.Join(dir, ".pogo", "refinery.toml"))
	if cfg.GateTimeoutSet {
		t.Error("an omitted timeout must not read as configured")
	}
	if got := cfg.gateTimeout(); got != defaultGateTimeout {
		t.Errorf("unconfigured timeout = %s, want the %s default", got, defaultGateTimeout)
	}

	writeGateConfig(t, dir, "[gates]\ntimeout = \"0\"\n")
	cfg = parseRefineryConfig(filepath.Join(dir, ".pogo", "refinery.toml"))
	if !cfg.GateTimeoutSet {
		t.Error("timeout = \"0\" must read as deliberately configured")
	}
	if got := cfg.gateTimeout(); got != 0 {
		t.Errorf("timeout = \"0\" should mean unbounded, got %s", got)
	}

	writeGateConfig(t, dir, "[gates]\ntimeout = \"12m\"\n")
	cfg = parseRefineryConfig(filepath.Join(dir, ".pogo", "refinery.toml"))
	if got := cfg.gateTimeout(); got != 12*time.Minute {
		t.Errorf("timeout = \"12m\" = %s, want 12m", got)
	}

	// An unreadable value must keep the default bound, not remove it: a typo
	// that silently disabled the timeout would be the worst of both worlds.
	writeGateConfig(t, dir, "[gates]\ntimeout = \"eventually\"\n")
	cfg = parseRefineryConfig(filepath.Join(dir, ".pogo", "refinery.toml"))
	if cfg.GateTimeoutSet {
		t.Error("an unreadable timeout must not read as configured")
	}
	if got := cfg.gateTimeout(); got != defaultGateTimeout {
		t.Errorf("unreadable timeout = %s, want the %s default", got, defaultGateTimeout)
	}
}

// TestGateTimeoutInheritedFromOriginConfig checks the timeout follows the same
// worktree-over-origin merge as every other gate knob, so a repo can set it
// once without every branch carrying the file.
func TestGateTimeoutInheritedFromOriginConfig(t *testing.T) {
	r := newProgressTestRefinery(t, time.Second)
	origin, wt := t.TempDir(), t.TempDir()
	writeGateConfig(t, origin, "[gates]\ntimeout = \"7m\"\n")
	writeGateConfig(t, wt, "[gates]\ncommands = [\"true\"]\n")

	cfg := r.loadConfig(wt, origin)
	if !cfg.GateTimeoutSet || cfg.gateTimeout() != 7*time.Minute {
		t.Errorf("worktree config with no timeout should inherit origin's 7m, got set=%v %s",
			cfg.GateTimeoutSet, cfg.gateTimeout())
	}

	// A worktree that sets its own value wins.
	writeGateConfig(t, wt, "[gates]\ncommands = [\"true\"]\ntimeout = \"2m\"\n")
	if got := r.loadConfig(wt, origin).gateTimeout(); got != 2*time.Minute {
		t.Errorf("worktree timeout should win over origin's, got %s", got)
	}
}
