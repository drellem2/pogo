package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"
)

// Long-nudge delivery (mg-8a70).
//
// pasteHarnessHelper is a fake harness with Claude Code 2.1.283's input
// behaviour, reduced to the part that bit: it decides "typed or pasted" per
// read(), and a paste followed by further typing before the submit is lost —
// the harness submits only what came after it. Every submit is recorded
// through the REAL receipt writer (RecordSubmitPrompt), so the confirm path
// reads the same format it reads in production. Each read's size is logged, so
// a test can assert on how the kernel actually delivered the bytes.
//
// The measured real-binary numbers these stand in for: reads of ~900 bytes or
// more are pastes; a 1143-byte write arrives as reads of [1022, 121]; the
// submitted prompt was the 121-byte tail (3 of 3 runs, and 91 of the 128
// truncated fires in the polecat transcripts).

const (
	pasteHelperEnv      = "POGO_TEST_PASTE_HARNESS"
	pasteHelperReadsEnv = "POGO_TEST_PASTE_READS"
	pasteHelperDelayEnv = "POGO_TEST_PASTE_READ_DELAY_MS"
	// pasteHelperThreshold stands in for Claude Code's paste threshold, which
	// was measured between 800 (typed) and 900 (pasted) bytes per read.
	pasteHelperThreshold = 850
)

// longFireMessage is the 1143-byte scheduler fire the investigation started
// from (t036c, token 0590a095), verbatim.
const longFireMessage = "Check your mail with BOTH `mg mail list t036c` AND `mg mail list mg-036c` and handle any unread messages — act on any reviewer findings or re-review requests and mail your reply back; otherwise no-op. Read both every time: mailboxes are created on first delivery, so your mail is in whichever box your senders happened to type, and reading only one is silent when it is the wrong one. If `mg mail read` refuses a message as a cross-box read, that is NOT a permissions error — it compares against $POGO_AGENT_NAME, which your work-item box is not. It is still your mail: re-run with --force.\n\n[scheduler id=mail-check-mg-036c due=2026-09-26T02:20:00+01:00 fired=2026-09-26T02:20:14+01:00 ack=0590a095]\nHow late am I: compare due=2026-09-26T02:20:00+01:00 against the CURRENT clock — NOT against fired=, which is when these bytes were sent, not when you are reading them (measured gap between sent and read: 4h19m). Lateness is graded: if any of this work's reads depend on WHEN they run, mark those stale and answer the rest normally.\nWhen this fire's work is done, run: pogo schedule ack mail-check-mg-036c --agent t036c --token 0590a095"

func TestPasteHarnessHelper(t *testing.T) {
	if os.Getenv(pasteHelperEnv) == "" {
		t.Skip("helper process for the long-nudge tests; not a standalone test")
	}
	if _, err := term.MakeRaw(0); err != nil {
		os.Stderr.WriteString("MakeRaw: " + err.Error() + "\n")
		os.Exit(2)
	}
	receipt := os.Getenv("WITNESS")
	reads, _ := os.OpenFile(os.Getenv(pasteHelperReadsEnv), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	delay, _ := strconv.Atoi(os.Getenv(pasteHelperDelayEnv))
	os.Stdout.WriteString("ready\r\n")

	var composer []byte
	pasteLost := false
	buf := make([]byte, 65536)
	for {
		// A reader slower than the writer is the worst case: it lets the tty
		// queue fill to its limit before the harness looks.
		time.Sleep(time.Duration(delay) * time.Millisecond)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			os.Exit(0)
		}
		chunk := buf[:n]
		reads.WriteString(strconv.Itoa(n) + "\n")
		if string(chunk) == "\r" {
			if len(composer) > 0 {
				RecordSubmitPrompt(receipt, string(composer))
			}
			composer, pasteLost = nil, false
			continue
		}
		body := strings.TrimSuffix(string(chunk), "\r")
		switch {
		case n >= pasteHelperThreshold:
			// A paste: held as a placeholder. Typing after it drops it.
			composer = append(composer, body...)
			pasteLost = true
		case pasteLost:
			composer = []byte(body)
			pasteLost = false
		default:
			composer = append(composer, body...)
		}
		if strings.HasSuffix(string(chunk), "\r") {
			RecordSubmitPrompt(receipt, string(composer))
			composer, pasteLost = nil, false
		}
	}
}

// spawnPasteHarness spawns the helper as an agent with a receipt signal.
func spawnPasteHarness(t *testing.T, reg *Registry, name string, readDelayMS int) (a *Agent, receipt, readsLog string) {
	t.Helper()
	receipt = witnessFile(t)
	readsLog = filepath.Join(t.TempDir(), "reads")
	a, err := reg.Spawn(SpawnRequest{
		Name:    name,
		Type:    TypePolecat,
		Command: []string{os.Args[0], "-test.run=^TestPasteHarnessHelper$", "-test.timeout=120s"},
		Env: []string{
			pasteHelperEnv + "=1",
			"WITNESS=" + receipt,
			pasteHelperReadsEnv + "=" + readsLog,
			pasteHelperDelayEnv + "=" + strconv.Itoa(readDelayMS),
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a.receiptFile = receipt
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(string(a.RecentOutput(4096)), "ready") {
		if time.Now().After(deadline) {
			t.Fatalf("paste harness never became ready; output %q", a.RecentOutput(4096))
		}
		time.Sleep(20 * time.Millisecond)
	}
	return a, receipt, readsLog
}

func readSizes(t *testing.T, path string) []int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open reads log: %v", err)
	}
	defer f.Close()
	var out []int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n, _ := strconv.Atoi(sc.Text())
		out = append(out, n)
	}
	return out
}

func lastSubmit(t *testing.T, receipt string) SubmitRecord {
	t.Helper()
	recs, err := ReadSubmits(receipt)
	if err != nil || len(recs) == 0 {
		t.Fatalf("no submission recorded (err %v)", err)
	}
	return recs[len(recs)-1]
}

// TestLongNudgeSingleWriteLosesItsHead is the red control: with chunking off —
// pogo's write before mg-8a70 — the kernel splits the 1143-byte body at the
// tty queue limit and the harness submits only the tail. If this ever stops
// reproducing, the fake no longer models the defect and the green test below
// proves nothing.
func TestLongNudgeSingleWriteLosesItsHead(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, receipt, readsLog := spawnPasteHarness(t, reg, "paste-red", 150)
	a.nudge.InputChunkBytes = 0

	if err := a.NudgeWithMode(longFireMessage, NudgeImmediate, 5*time.Second); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	waitForSubmits(t, receipt, 1, 5*time.Second)

	rec := lastSubmit(t, receipt)
	if rec.Len >= len([]rune(longFireMessage)) {
		t.Fatalf("control did not reproduce: a single %d-byte write arrived whole (reads %v)",
			len(longFireMessage), readSizes(t, readsLog))
	}
	t.Logf("DEFECT REPRODUCED: reads %v, harness submitted %d of %d runes beginning %q",
		readSizes(t, readsLog), rec.Len, len([]rune(longFireMessage)), rec.Head)
}

// TestLongNudgeArrivesWholeInDrainGatedPieces is the fix: the same body, from a
// reader just as slow, arrives in reads no larger than the provider's chunk and
// is submitted whole.
func TestLongNudgeArrivesWholeInDrainGatedPieces(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, receipt, readsLog := spawnPasteHarness(t, reg, "paste-green", 150)
	if a.nudge.InputChunkBytes <= 0 || a.nudge.InputChunkBytes >= pasteHelperThreshold {
		t.Fatalf("default InputChunkBytes = %d; want 0 < n < %d", a.nudge.InputChunkBytes, pasteHelperThreshold)
	}

	if err := a.NudgeWithMode(longFireMessage, NudgeImmediate, 5*time.Second); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	waitForSubmits(t, receipt, 1, 10*time.Second)

	sizes := readSizes(t, readsLog)
	for _, n := range sizes {
		if n > a.nudge.InputChunkBytes {
			t.Errorf("a read of %d bytes exceeds the %d-byte chunk (reads %v)", n, a.nudge.InputChunkBytes, sizes)
		}
	}
	rec := lastSubmit(t, receipt)
	if v, _ := judgeDelivery(longFireMessage, []SubmitRecord{rec}); v != deliveryIntact || rec.Len != len([]rune(longFireMessage)) {
		t.Fatalf("submitted %d of %d runes (verdict %v), beginning %q; reads %v",
			rec.Len, len([]rune(longFireMessage)), v, rec.Head, sizes)
	}
}

// TestNudgeConfirmReportsAMangledDelivery pins the detection half: when the
// harness submits only the tail, confirm mode no longer logs success — it
// returns ErrNudgeMangled and records nudge_unconfirmed outcome=mangled.
func TestNudgeConfirmReportsAMangledDelivery(t *testing.T) {
	logPath := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, _, _ := spawnPasteHarness(t, reg, "paste-confirm", 150)
	a.nudge.InputChunkBytes = 0 // reproduce the pre-fix write

	err = a.NudgeWithModeCorrelated(longFireMessage, NudgeConfirm, 9*time.Second, "0590a095")
	if !errors.Is(err, ErrNudgeMangled) {
		t.Fatalf("confirm on a head-truncated delivery returned %v; want ErrNudgeMangled", err)
	}

	sent, unconfirmed := nudgeEvents(t, logPath)
	if len(sent) != 0 {
		t.Errorf("a mangled delivery was ALSO logged as nudge_sent: %v", sent)
	}
	if len(unconfirmed) != 1 || unconfirmed[0]["outcome"] != "mangled" {
		t.Fatalf("want one nudge_unconfirmed outcome=mangled, got %v", unconfirmed)
	}
	if unconfirmed[0]["fire_token"] != "0590a095" {
		t.Errorf("mangled event lost its fire token: %v", unconfirmed[0])
	}
}

// TestNudgeConfirmRecordsAnIntactLongDelivery: with the fix in place, the same
// fire is confirmed and its content check is recorded as intact.
func TestNudgeConfirmRecordsAnIntactLongDelivery(t *testing.T) {
	logPath := useTempEventLog(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, _, _ := spawnPasteHarness(t, reg, "paste-confirm-ok", 150)
	if err := a.NudgeWithModeCorrelated(longFireMessage, NudgeConfirm, 9*time.Second, "tok"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	sent, unconfirmed := nudgeEvents(t, logPath)
	if len(unconfirmed) != 0 {
		t.Errorf("unexpected nudge_unconfirmed: %v", unconfirmed)
	}
	if len(sent) != 1 || sent[0]["content_check"] != "intact" {
		t.Fatalf("want one nudge_sent content_check=intact, got %v", sent)
	}
}

func waitForSubmits(t *testing.T, receipt string, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if c, _ := CountSubmits(receipt); c >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d submissions after %s", n, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// nudgeEvents returns the details of the nudge_sent and nudge_unconfirmed
// events in the log at path.
func nudgeEvents(t *testing.T, path string) (sent, unconfirmed []map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read event log: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var e struct {
			EventType string         `json:"event_type"`
			Details   map[string]any `json:"details"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch e.EventType {
		case "nudge_sent":
			sent = append(sent, e.Details)
		case "nudge_unconfirmed":
			unconfirmed = append(unconfirmed, e.Details)
		}
	}
	return sent, unconfirmed
}
