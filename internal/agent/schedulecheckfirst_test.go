package agent

// The boot-time schedule procedure, enforced as a property of the shipped
// corpus rather than as a sentence someone has to keep re-reading (mg-49b1).
//
// Every prompt that registers a `pogo schedule` id used to tell its agent to
// re-register unconditionally on startup, justified with "idempotent via --id,
// so re-running it costs nothing". The justification is where the damage is:
// `--id` IS the dedup key, so the first half is true and you will not stack
// duplicates — but "does not duplicate" was read as "costs nothing", and it is
// not. Re-registering REPLACES the entry and zeroes its lifetime completion
// counters (scheduler.Add; TestReregistration_StillZeroesTheLifetimeCounters
// pins that as deliberate), which is the evidence internal/ackwatch uses to
// tell a fire that was WORKED from one that merely arrived. Doing it at boot
// discards that on every boot, including the boot after an outage.
//
// Fixing only the instruction would leave the reason in place to regenerate it
// the next time someone tidies a file, which is why this test checks the SHAPE
// of the procedure and not the presence of a sentence.
//
// # Two premises that are NOT the reason, and must not come back as one
//
// Re-registration used to also discard an outstanding fire, so an agent that
// booted, re-registered, did the catch-up fire's work and then ran the exact
// ack the fire handed it was refused. That is FIXED — scheduler.Add carries a
// still-redeemable token across a same-(agent, id) re-register (mg-3cbb,
// carryOutstandingFireLocked). An "ack before you re-register" ordering rule
// would encode a dead bug and leave the destructive step in the procedure.
//
// And mg-de08 — a pogod bounce reaping the whole fleet's mail-check-* — was
// the original reason the unconditional re-register was called load-bearing.
// It was mg-de08's own option (2), the "cheap, partial" one it described as
// leaving the fleet's mail loop resting on prompt wording. Option (1) shipped
// instead (cmd/pogod/gcgate.go): an absent registry entry is UNKNOWN, never
// GONE, and the reap is held until the first auto-start sweep plus a settle.
//
// # What mg-de08 DOES leave behind, and why it shapes the assertions
//
// mg-de08's proximate cause was a check-then-skip that failed: a PM ran
// `pogo schedule list`, saw its two sweeps (which are not named mail-check-*
// and so survived the reap), concluded "already registered", and never noticed
// the reaped id was the third one. It then looked scheduled, answered nudges
// and diagnosed healthy with no mail loop for two hours.
//
// A check-then-repair procedure is an artifact of exactly the kind that failed
// there, so the assertions below are the ones that separate the two: the check
// must come FIRST (an agent that meets a registration command before a listing
// runs it), and the corpus must say the check is per-ID rather than a glance at
// a row count.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// registerCmd matches a schedule REGISTRATION — `pogo schedule <agent> --cron`
// — and nothing else. The agent slot is deliberately loose: the corpus spells
// it as a literal (`doctor`), a template var (`{{.Coordinator}}`), a
// placeholder (`pm-<your-name>`) and a shell var (`$POGO_AGENT_NAME`).
//
// `--cron` on the same line is what makes this a registration: `pogo schedule
// list`, `pogo schedule rm` and `pogo schedule ack` never carry it.
var registerCmd = regexp.MustCompile(`pogo schedule +[^ \n]+ +--cron`)

// listCmd matches the CHECK.
var listCmd = regexp.MustCompile(`pogo schedule list`)

// dontReregister matches the directive that makes the check load-bearing: a
// present, correct id must be left alone. Several phrasings are accepted
// because the files differ in voice; what is not accepted is a corpus that
// registers without ever saying when NOT to.
var dontReregister = regexp.MustCompile(`(?i)(do not re-?register|don't re-?register|never re-?register|re-?register only|register only (if|when|a |the ))`)

// perIDCheck matches the corpus stating that the check is against a specific
// id, not against the listing being non-empty — the mg-de08 failure.
var perIDCheck = regexp.MustCompile(`(?i)(non-empty listing is not the check|read the id column|check per id|your exact id|exact id is present)`)

// registeringPrompts returns every shipped prompt containing a registration,
// as path -> contents.
func registeringPrompts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		b, err := fs.ReadFile(DefaultPromptsFS(), path)
		if err != nil {
			return err
		}
		if registerCmd.Match(b) {
			out[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no shipped prompt contains a `pogo schedule <agent> --cron` registration; " +
			"either the corpus moved or registerCmd stopped matching it — this test is checking nothing")
	}
	return out
}

// checkFirst is the property, factored out so the positive controls below can
// run it against deliberately-regressed text. It returns one complaint per
// violation, empty when the text is well-formed.
func checkFirst(body string) []string {
	var bad []string
	reg := registerCmd.FindStringIndex(body)
	if reg == nil {
		return nil
	}
	list := listCmd.FindStringIndex(body)
	switch {
	case list == nil:
		bad = append(bad, "registers a schedule but never tells the agent to run `pogo schedule list` first: "+
			"the procedure is unconditional re-registration, which zeroes the entry's completion counters on every boot")
	case list[0] > reg[0]:
		bad = append(bad, "the `pogo schedule ... --cron` registration appears BEFORE the `pogo schedule list` check: "+
			"an agent working top-down runs the registration and only then reads that it was conditional")
	}
	if !dontReregister.MatchString(body) {
		bad = append(bad, "no directive saying a present, correct id must be left alone "+
			"(e.g. \"do not re-register\" / \"register only if\"): a check with no consequence is not a check")
	}
	if !perIDCheck.MatchString(body) {
		bad = append(bad, "does not say the check is against a specific id rather than a non-empty listing — "+
			"mg-de08's agent read a row count, kept a reaped mail-check, and went two hours deaf while diagnosing healthy")
	}
	return bad
}

// TestBootScheduleProcedureIsCheckThenRepair is the guard.
func TestBootScheduleProcedureIsCheckThenRepair(t *testing.T) {
	for path, body := range registeringPrompts(t) {
		for _, complaint := range checkFirst(body) {
			t.Errorf("%s: %s", path, complaint)
		}
	}
}

// TestBootScheduleProcedureGuardCanFail is the positive control. Without it,
// a green run above is consistent with checkFirst never being able to fail —
// the "check pointed at nothing" shape this repo has been bitten by before
// (see cmd/pogo/checkprompts_turnlog_test.go for the same pairing).
//
// Each case regresses real corpus text the way it would actually regress: by
// someone tidying the file back toward the pre-mg-49b1 procedure.
func TestBootScheduleProcedureGuardCanFail(t *testing.T) {
	prompts := registeringPrompts(t)
	body, ok := prompts["mayor.md"]
	if !ok {
		t.Fatal("mayor.md is not among the registering prompts; the controls below have no subject")
	}
	if got := checkFirst(body); len(got) != 0 {
		t.Fatalf("mayor.md must be clean before it can be regressed: %v", got)
	}

	t.Run("check removed", func(t *testing.T) {
		regressed := strings.ReplaceAll(body, "pogo schedule list", "pogo schedule show")
		assertFlagged(t, regressed, "never tells the agent to run")
	})

	t.Run("registration moved above the check", func(t *testing.T) {
		reg := registerCmd.FindStringIndex(body)
		list := listCmd.FindStringIndex(body)
		if reg == nil || list == nil || list[0] > reg[0] {
			t.Fatalf("mayor.md is not in check-then-register order to begin with (list=%v reg=%v)", list, reg)
		}
		// Hoist a copy of the registration command above the check, which is
		// exactly what "put the command back at the top of the section" does.
		regressed := body[:list[0]] + body[reg[0]:reg[1]] + " ...\n\n" + body[list[0]:]
		assertFlagged(t, regressed, "BEFORE the `pogo schedule list` check")
	})

	t.Run("directive softened back to idempotence", func(t *testing.T) {
		regressed := dontReregister.ReplaceAllString(body, "it is idempotent, so re-run it")
		assertFlagged(t, regressed, "must be left alone")
	})

	t.Run("per-id check softened to a row count", func(t *testing.T) {
		regressed := perIDCheck.ReplaceAllString(body, "you should see an entry")
		assertFlagged(t, regressed, "against a specific id")
	})
}

func assertFlagged(t *testing.T, regressed, want string) {
	t.Helper()
	got := checkFirst(regressed)
	for _, c := range got {
		if strings.Contains(c, want) {
			return
		}
	}
	t.Errorf("the regressed text was not flagged with %q; complaints=%v — "+
		"a clean result from the real corpus therefore means nothing", want, got)
}
