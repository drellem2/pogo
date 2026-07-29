package claude

// Live re-test of the Claude profile's NudgeProfile.SubmitDelay (mg-68c8).
//
// docs/investigations/nudge-claude-code-workaround.md §3 specified this
// protocol in 2026-05 and named a polecat to run it; nothing ever did, so the
// shipped 50ms was a belief rather than a measurement. This file is that
// protocol as code, so the next re-test costs a command rather than a redesign
// — the same shape codex and cursor calibrations already use (an opt-in,
// real-binary e2e beside the provider it calibrates).
//
// The question is narrow: does Claude Code's Ink composer still swallow the
// submit when the nudge body and the "\r" arrive in ONE write, and does it
// still submit when they arrive in TWO writes separated by SubmitDelay? The
// answer is version-pinned — record the harness version with every run.
//
//	POGO_CLAUDE_SUBMITDELAY_E2E=1 go test ./internal/claude/ \
//	    -run TestClaudeSubmitDelayStillRequired -v -timeout 30m
//
// Knobs (all optional):
//
//	POGO_CLAUDE_SUBMITDELAY_ITERS=10   probes per variant (default 10)
//	POGO_CLAUDE_SUBMITDELAY_OUT=path   where to write the results YAML
//
// It is opt-in because it drives a real, authenticated `claude` and spends a
// real model turn on every probe that submits. There is deliberately no CI job
// (as with codex and cursor): CI has no Claude credentials.
//
// # Why submission is detected in the session transcript, not on screen
//
// §3 proposed scanning the PTY tee for the probe string and distinguishing
// "echoed in the conversation" from "sitting in the input box". Both regions
// render the same characters into the same byte stream, so that discrimination
// is a screen-region heuristic over a redraw stream — exactly the kind of
// brittle string-position matching that has already bitten pogo twice
// (gh#76/mg-d06a's space-collapse, and the ready sentinel that stopped matching
// in v2.1.x). Claude Code offers an unambiguous alternative pogo already
// depends on elsewhere: it appends every SUBMITTED user message to the session
// JSONL under ~/.claude/projects/<slug>/ — the file internal/synthfail reads,
// located by this package's own SessionTranscriptGlob. A probe that stays in
// the composer never appears there. So "submitted" is a file fact, not a
// pixel-adjacency guess. The PTY byte volume after each probe is recorded too,
// as an independent cross-check: a submitted probe drives a whole response
// render, an unsubmitted one only a composer repaint.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/drellem2/pogo/internal/agent"
)

const submitDelayE2EEnv = "POGO_CLAUDE_SUBMITDELAY_E2E"

// Probe pacing. These are per-probe budgets, not measurements of Claude: the
// classification is a file fact, so the windows only have to be generous
// enough that a slow turn is not miscounted as a swallowed submit.
const (
	// submitWindow bounds the wait for a probe to appear in the session
	// transcript. §3 used 3s against a PTY-echo detector; the transcript
	// detector is asynchronous with the model's first token, so the window is
	// widened and the observed latency recorded instead. Anything classified
	// submitted reports how long it took, so a systematic drift is visible
	// rather than hidden behind a threshold.
	submitWindow = 20 * time.Second

	// settleQuiet / settleBudget: how long the PTY must be silent before the
	// next probe fires. Reusing the profile's own IdleThreshold keeps the rig
	// honest about what pogo considers "idle".
	settleBudget = 90 * time.Second

	// composerClearBudget bounds the post-probe cleanup that empties the
	// composer after an unsubmitted probe, so probe N+1 starts from an empty
	// box rather than inheriting N's stranded text.
	composerClearBudget = 10 * time.Second
)

// probeBodySuffix pads each probe to a realistic nudge length, and it is
// load-bearing — do not shorten it to tidy the logs.
//
// Claude Code's paste heuristic is chunk-SIZE sensitive, which the §3 protocol
// did not anticipate. Swept on 2.1.220 while building this rig: a single write
// (body + "\r" in one syscall) of <= 63 bytes SUBMITS, and one of >= 64 bytes
// does not — a sharp boundary with no grey zone, recorded in
// docs/investigations/mg-09b6-test-results.yaml. A rig that probed with a short
// token alone would therefore report "bug fixed" and be wrong about every real
// nudge, since real nudge bodies are a message plus the scheduler's
// "[scheduler id=...]" metadata line — comfortably past 64 bytes. The probe
// must look like the traffic the delay exists to protect.
const probeBodySuffix = " is a pogo PTY submit test. Reply with only that token and nothing else."

// probeResult is one row of the results YAML.
type probeResult struct {
	Variant   string
	Iter      int
	Result    string // submitted | not-submitted | timeout
	LatencyMS int64
	PTYBytes  int
	Notes     string
}

// teeBuf collects PTY output with a last-write timestamp, so the rig can ask
// both "what has been rendered" and "has it gone quiet".
type teeBuf struct {
	mu    sync.Mutex
	buf   []byte
	last  time.Time
	total int
}

func newTeeBuf() *teeBuf { return &teeBuf{last: time.Now()} }

func (t *teeBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	// Ink repaints continuously; keep only a recent window so a long run does
	// not grow the buffer without bound.
	if len(t.buf) > 1<<20 {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-(1<<19):]...)
	}
	t.last = time.Now()
	t.total += len(p)
	return len(p), nil
}

func (t *teeBuf) snapshot() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

func (t *teeBuf) counter() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

func (t *teeBuf) quietFor() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.last)
}

// waitQuiet blocks until the PTY has been silent for d, or budget expires.
func (t *teeBuf) waitQuiet(d, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if t.quietFor() >= d {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func TestClaudeSubmitDelayStillRequired(t *testing.T) {
	if os.Getenv(submitDelayE2EEnv) == "" {
		t.Skipf("live Claude Code re-test; set %s=1 to run", submitDelayE2EEnv)
	}
	bin, err := exec.LookPath(Provider.Binary)
	if err != nil {
		t.Skipf("%s not on PATH: %v", Provider.Binary, err)
	}

	iters := 10
	if v := os.Getenv("POGO_CLAUDE_SUBMITDELAY_ITERS"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &iters); n != 1 || err != nil {
			t.Fatalf("bad POGO_CLAUDE_SUBMITDELAY_ITERS=%q", v)
		}
	}

	version := harnessVersion(t, bin)
	t.Logf("harness: %s", version)

	// A fresh working directory per run: no prior conversation to resume, and
	// a transcript directory that holds this run's probes and nothing else.
	// EvalSymlinks because macOS hands out /var/... temp dirs that Claude Code
	// records under their /private/var real path — the slug must match what it
	// actually writes.
	workdir := t.TempDir()
	if real, err := filepath.EvalSymlinks(workdir); err == nil {
		workdir = real
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	transcriptGlob := filepath.Join(home, SessionTranscriptGlob(workdir))
	t.Logf("workdir=%s\ntranscripts=%s", workdir, transcriptGlob)

	tee := newTeeBuf()
	ptmx := startClaude(t, bin, workdir, tee)

	profile := Provider.Nudge
	waitComposerReady(t, tee, ptmx, profile.InitialNudgeTimeout)
	if !tee.waitQuiet(profile.IdleThreshold, settleBudget) {
		t.Fatalf("composer never settled after ready:\n%s", tail(tee.snapshot(), 1500))
	}
	t.Logf("composer ready and settled; probing with SubmitDelay=%s terminator=%q",
		profile.SubmitDelay, profile.SubmitTerminator)

	// Interleaved A,B,A,B,... so any drift across the run hits both variants
	// equally (§3, "Randomization + replication").
	var results []probeResult
	for i := 1; i <= iters; i++ {
		for _, variant := range []string{"A", "B"} {
			r := runProbe(t, tee, ptmx, transcriptGlob, profile, variant, i)
			results = append(results, r)
			t.Logf("probe %s-%02d: %s (%dms, %d PTY bytes) %s",
				r.Variant, r.Iter, r.Result, r.LatencyMS, r.PTYBytes, r.Notes)
		}
	}

	aRate := submitRate(results, "A")
	bRate := submitRate(results, "B")
	conclusion, action := classify(aRate, bRate, iters)
	t.Logf("variant A (single write, no delay): %d/%d submitted", aRate, iters)
	t.Logf("variant B (split write, %s delay):  %d/%d submitted", profile.SubmitDelay, bRate, iters)
	t.Logf("conclusion: %s -> %s", conclusion, action)

	out := os.Getenv("POGO_CLAUDE_SUBMITDELAY_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "submitdelay-results.yaml")
	}
	yaml := renderYAML(version, workdir, profile, results, aRate, bRate, iters, conclusion, action)
	if err := os.WriteFile(out, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write results: %v", err)
	}
	t.Logf("results written to %s\n%s", out, yaml)

	// The rig asserts only that it produced a usable measurement. "Bug still
	// present" is a legitimate outcome, not a failure — §5 pre-committed that.
	// What IS a failure is a run that measured nothing: if variant B, the
	// shipped configuration, cannot submit, the rig is broken (wrong sentinel,
	// unauthenticated harness, wedged composer) and its verdict on variant A
	// means nothing.
	if bRate == 0 {
		t.Fatalf("variant B (the shipped split write) submitted 0/%d — the rig, not the delay, is what this run measured; last PTY output:\n%s",
			iters, tail(tee.snapshot(), 2000))
	}
}

// startClaude spawns the harness under a polecat-owned PTY at pogo's default
// winsize and tees its output. No agent.Registry, no pogo state: §3's
// "must not disrupt production crew agents" constraint is met by never
// entering the registry at all.
func startClaude(t *testing.T, bin, workdir string, tee *teeBuf) *os.File {
	t.Helper()
	cmd := exec.Command(bin, Provider.NonInteractiveFlags...)
	cmd.Dir = workdir
	cmd.Env = spawnEnv()

	// 200x50 is agent.defaultPTYCols x defaultPTYRows, the winsize every pogo
	// agent gets. Composer wrapping is width-sensitive, so the rig must not
	// measure a different terminal than production uses.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 200, Rows: 50})
	if err != nil {
		t.Fatalf("start %s under pty: %v", bin, err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				tee.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return ptmx
}

// spawnEnv is the parent environment minus Claude Code's own session markers.
//
// This test is itself usually run BY a Claude Code agent (that is the point —
// §3 assigns it to a polecat), so the inherited environment carries CLAUDECODE
// and CLAUDE_CODE_* from the surrounding session. A harness that believes it is
// a nested child is not the harness pogod spawns, and the whole measurement is
// about the composer's behaviour in a top-level session. Anthropic credentials
// (ANTHROPIC_*) are deliberately kept: the probe needs to authenticate.
func spawnEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "CLAUDE_") || name == "CLAUDECODE" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// waitComposerReady drives the same readiness gate production uses, dismissing
// the workspace-trust dialog with the same detector TrustDialogHook uses.
func waitComposerReady(t *testing.T, tee *teeBuf, ptmx *os.File, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	trusted := false
	for time.Now().Before(deadline) {
		out := tee.snapshot()
		if composerReady(out) {
			return
		}
		if !trusted && matchesTrustDialog(out) {
			time.Sleep(300 * time.Millisecond)
			if _, err := ptmx.WriteString("\r"); err != nil {
				t.Fatalf("dismiss trust dialog: %v", err)
			}
			trusted = true
		}
		time.Sleep(TrustDialogPollInterval)
	}
	t.Fatalf("composer never reported ready within %s (sentinels %q); last output:\n%s",
		budget, readySentinels(), tail(tee.snapshot(), 2000))
}

// runProbe delivers one probe and classifies it.
//
// Variant A writes body+terminator in ONE write — the configuration that would
// ship if SubmitDelay were removed. Variant B writes them separately with the
// profile's SubmitDelay between, which is exactly what Agent.Nudge does.
func runProbe(t *testing.T, tee *teeBuf, ptmx *os.File, transcriptGlob string, profile agent.NudgeProfile, variant string, iter int) probeResult {
	t.Helper()
	token := fmt.Sprintf("PROBE-%s-%02d", variant, iter)
	body := token + probeBodySuffix

	before := tee.counter()
	start := time.Now()
	switch variant {
	case "A":
		if _, err := ptmx.WriteString(body + profile.SubmitTerminator); err != nil {
			t.Fatalf("probe %s write: %v", token, err)
		}
	case "B":
		if _, err := ptmx.WriteString(body); err != nil {
			t.Fatalf("probe %s body write: %v", token, err)
		}
		time.Sleep(profile.SubmitDelay)
		if _, err := ptmx.WriteString(profile.SubmitTerminator); err != nil {
			t.Fatalf("probe %s submit write: %v", token, err)
		}
	}

	res := probeResult{Variant: variant, Iter: iter, Result: "timeout"}
	deadline := time.Now().Add(submitWindow)
	for time.Now().Before(deadline) {
		if transcriptHas(transcriptGlob, token) {
			res.Result = "submitted"
			res.LatencyMS = time.Since(start).Milliseconds()
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if res.Result != "submitted" {
		// The token never reached the session transcript inside the window.
		// Distinguish "still sitting in the composer" (the paste-detection
		// failure mode) from "vanished entirely" by looking for it on screen.
		if strings.Contains(collapse(string(agent.StripANSI(tee.snapshot()))), token) {
			res.Result = "not-submitted"
			res.Notes = "token rendered on screen but absent from session transcript: retained in composer"
		} else {
			res.Notes = "token neither submitted nor visible on screen"
		}
	}
	res.PTYBytes = tee.counter() - before

	// Leave the composer empty for the next probe regardless of outcome.
	clearComposer(t, tee, ptmx, profile)
	tee.waitQuiet(profile.IdleThreshold, settleBudget)
	return res
}

// clearComposer empties the input box after a probe. An unsubmitted probe
// leaves its body (plus the literal newline the terminator became) in the
// composer; without this, probe N+1 would be typed onto the end of probe N's
// stranded text and measure nothing.
func clearComposer(t *testing.T, tee *teeBuf, ptmx *os.File, profile agent.NudgeProfile) {
	t.Helper()
	// Ctrl+U (kill-line), NOT Escape. Escape looks like the obvious choice and
	// is wrong: sending it to an idle composer left the TUI in a state where
	// the next probe's keystrokes produced no render at all (4 PTY bytes over
	// 20s), so every probe after the first measured nothing. Ctrl+U repaints an
	// emptied composer and the following probe types normally — measured while
	// building this rig against 2.1.220.
	if _, err := ptmx.WriteString("\x15"); err != nil {
		t.Fatalf("clear composer: %v", err)
	}
	tee.waitQuiet(500*time.Millisecond, composerClearBudget)
}

// transcriptHas reports whether any session transcript matching glob contains
// token. Claude Code appends a user message the moment it is submitted, so
// presence here is proof of submission and absence is proof it is not.
func transcriptHas(glob, token string) bool {
	matches, err := filepath.Glob(glob)
	if err != nil {
		return false
	}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(token)) {
			return true
		}
	}
	return false
}

func harnessVersion(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func submitRate(rs []probeResult, variant string) int {
	n := 0
	for _, r := range rs {
		if r.Variant == variant && r.Result == "submitted" {
			n++
		}
	}
	return n
}

// classify maps the two rates onto §5's pre-committed dispositions.
func classify(aRate, bRate, iters int) (conclusion, action string) {
	switch {
	case bRate < iters:
		// The shipped configuration itself is flaky; neither "keep" nor
		// "remove" is supportable from this run.
		return "inconclusive", "investigate: the split write did not submit reliably either"
	case aRate == 0:
		return "bug-still-present", "keep"
	case aRate == iters:
		return "bug-fixed", "remove"
	default:
		return "partial-trigger", "report to architect: paste-detection timing may have loosened"
	}
}

func renderYAML(version, workdir string, profile agent.NudgeProfile, rs []probeResult, aRate, bRate, iters int, conclusion, action string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s §3 protocol, executed by mg-68c8.\n", "docs/investigations/nudge-claude-code-workaround.md")
	fmt.Fprintf(&b, "# Regenerate: POGO_CLAUDE_SUBMITDELAY_E2E=1 go test ./internal/claude/ -run TestClaudeSubmitDelayStillRequired -v -timeout 30m\n")
	fmt.Fprintf(&b, "claude_version: %q\n", version)
	fmt.Fprintf(&b, "test_date: %q\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "host_os: %q\n", runtime.GOOS)
	fmt.Fprintf(&b, "pty_size: \"200x50\"\n")
	fmt.Fprintf(&b, "workdir: %q\n", workdir)
	fmt.Fprintf(&b, "profile:\n")
	fmt.Fprintf(&b, "  submit_delay: %q\n", profile.SubmitDelay.String())
	fmt.Fprintf(&b, "  submit_terminator: %q\n", profile.SubmitTerminator)
	fmt.Fprintf(&b, "  idle_threshold: %q\n", profile.IdleThreshold.String())
	fmt.Fprintf(&b, "probe_body_bytes: %d  # chunk size matters — see probeBodySuffix\n", len("PROBE-A-01")+len(probeBodySuffix))
	fmt.Fprintf(&b, "test_runs:\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "  - variant: %s\n    iter: %d\n    result: %s\n    latency_ms: %d\n    pty_bytes: %d\n",
			r.Variant, r.Iter, r.Result, r.LatencyMS, r.PTYBytes)
		if r.Notes != "" {
			fmt.Fprintf(&b, "    notes: %q\n", r.Notes)
		}
	}
	fmt.Fprintf(&b, "summary:\n")
	fmt.Fprintf(&b, "  variant_a_submit_rate: \"%d/%d\"\n", aRate, iters)
	fmt.Fprintf(&b, "  variant_b_submit_rate: \"%d/%d\"\n", bRate, iters)
	fmt.Fprintf(&b, "  conclusion: %q\n", conclusion)
	fmt.Fprintf(&b, "  recommended_action: %q\n", action)
	return b.String()
}

func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(agent.StripANSI(b))
}
