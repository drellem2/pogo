package memcheck

import (
	"errors"
	"strings"
	"testing"
)

// fakeResolver builds a StatusResolver from a fixed map. IDs absent from the map
// resolve to StateUnknown — which is exactly what a real box does for a
// not-found or ambiguous short ID, and the reason the resolver is injectable: the
// two failure paths that matter most cannot be produced on demand from a live
// tracker.
func fakeResolver(statuses map[string]string) StatusResolver {
	return func(id string) (ItemState, string) {
		s, ok := statuses[id]
		if !ok {
			return StateUnknown, ""
		}
		return StatusState(s), s
	}
}

// The real index lines this check was built and calibrated against (mg-cb71).
// They are verbatim from the development box, because the whole difficulty of
// this check is that stale and maintained lines look alike, and a hand-written
// fixture would be written to whatever rule the code already implements.
const (
	// STALE: the item is shelved; the hook still presents it as blocked.
	lineStale8614 = "- [bridget bot Manage-Channels perm](bridget-bot-manage-channels-perm.md) — per-agent routing config written (mg-8614) but blocked: bot can't create channels without the perm Daniel must grant"
	// STALE: the item is archived; the hook still presents Piece 2 as blocked.
	lineStaleE996 = "- [S6-QA Checkpoint 2 AMBER](s6qa-checkpoint2-amber.md) — mg-e996: Piece 2 blocked; Step 6 grounded forms consumable only parametrically (S2 ε₂ unwired, cascade uncomposed, hand-built witnesses); S1-E did NOT close the Checkpoint-1 gap"
	// CORRECT: archived, and the line says so. A keyword-only filter flags this.
	lineCorrectBa0b = "- [(superseded) Post-η₅ decision RESOLVED via option 3](project_post_eta5_decision_open.md) — mg-ba0b done."
	// CORRECT: generic guidance. The tense word is in a conditional clause and
	// the archived id is a trailing citation of the incident behind the guidance.
	lineGuidanceBc47 = "- [Proactively monitor live release gates](feedback_proactivity.md) — when a Daniel-GO'd build/gate I own is in flight, pull status actively; don't wait on 10-min mail-checks (mayor had to proceed without me on v0.4.0/mg-bc47)"
	// CORRECT: guidance whose own text records the resolution.
	lineGuidance9299 = "- [Verify Daniel-pending blocker premise before re-surfacing](feedback_verify_daniel_pending_blocker_premise.md) — every evening sweep, cheap state-probe stale Daniel-action asks; Daniel may have resolved part without updating the ticket (mg-9299 \"tap repo missing\" → tap exists; real blocker was token, 3 days of wrong-framing digest mentions)"
	// UNKNOWN: mg-3119 is genuinely ambiguous on this box (two archived twins).
	lineAmbiguous3119 = "- [S7-F bridge ChainLiftData reconcile](s7f-bridge-chainliftdata-reconcile.md) — Checkpoint 3 RED; the bridge consumes a ChainLiftData; R2 mg-3119 reconciled the contract, R1 mg-974c settled F7a GREEN; Piece 3 unblocked"
)

// TestPositiveControl_StalenessFiresOnStaleHook is the required positive control
// for the staleness axis: PLANT a hook naming a known-archived item and observe
// the check FIRE.
//
// This check is built almost entirely out of suppressions — resolution words,
// proximity, unknown-id silence — and every one of them is a way to make it
// never fire. Its silence means nothing until its firing has been observed.
func TestPositiveControl_StalenessFiresOnStaleHook(t *testing.T) {
	// Two hooks naming items in the two distinct terminal states.
	data := []byte("# Memory index\n\n" + lineStale8614 + "\n" + lineStaleE996 + "\n")
	resolve := fakeResolver(map[string]string{
		"mg-8614": "shelved",
		"mg-e996": "archived",
	})

	hooks := StaleCheck("MEMORY.md", data, resolve).Hooks
	if len(hooks) != 2 {
		t.Fatalf("StaleCheck found %d stale hooks, want 2 — the check cannot fire on planted stale hooks: %+v", len(hooks), hooks)
	}
	if hooks[0].Line != 3 || hooks[1].Line != 4 {
		t.Errorf("lines = %d,%d, want 3,4 (1-indexed)", hooks[0].Line, hooks[1].Line)
	}
	if hooks[0].Assertion != "blocked" {
		t.Errorf("Assertion = %q, want %q", hooks[0].Assertion, "blocked")
	}
	// The reader needs the status to rewrite the hook rather than delete it.
	if !strings.Contains(hooks[0].Items[0], "shelved") {
		t.Errorf("Items = %v, want the resolved status named", hooks[0].Items)
	}
	if !strings.Contains(hooks[1].Items[0], "archived") {
		t.Errorf("Items = %v, want the resolved status named", hooks[1].Items)
	}
}

// TestStalenessSilentOnMaintainedLines is the negative control, run against the
// three real lines that a keyword-only check gets wrong. Each is a distinct
// suppression, and each false positive here would provoke the deletion of a
// CORRECT hook — which is why a false "stale" is worse than no check at all.
func TestStalenessSilentOnMaintainedLines(t *testing.T) {
	resolve := fakeResolver(map[string]string{
		"mg-ba0b": "archived",
		"mg-bc47": "archived",
		"mg-9299": "archived",
	})
	cases := []struct {
		name, line, why string
	}{
		{"records its own resolution", lineCorrectBa0b,
			"line says RESOLVED/done; a keyword filter flags it because the LINKED FILENAME ends in _open.md"},
		{"tense word in a conditional clause", lineGuidanceBc47,
			"the assertion is 109 chars from the id — different clauses, not a claim about that item"},
		{"guidance that names its resolution", lineGuidance9299,
			"line says 'resolved'; the id is a trailing citation 222 chars away"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hooks := StaleCheck("MEMORY.md", []byte(tc.line+"\n"), resolve).Hooks
			if len(hooks) != 0 {
				t.Errorf("false STALE verdict on a correctly maintained line (%s):\n  %+v", tc.why, hooks)
			}
		})
	}
}

// TestUnknownIDIsSilentNeverStale pins the instrument caveat, which is the single
// most important property of this check.
//
// Short-ID lookup is unreliable on a real box — colliding 4-hex IDs and archive
// orphans are both live conditions, verified directly:
//
//	mg show mg-3119  ->  ambiguous — 2 work items share this ID (exit 4)
//	mg show mg-zzzz  ->  not found (exit 3)
//
// An unresolvable ID must produce SILENCE, never a stale verdict, because the
// natural response to "stale" is deleting the hook.
func TestUnknownIDIsSilentNeverStale(t *testing.T) {
	// Nothing resolves at all — the shape of a box where mg is missing or every
	// lookup is ambiguous.
	none := fakeResolver(nil)
	for _, line := range []string{lineStale8614, lineStaleE996, lineAmbiguous3119} {
		if hooks := StaleCheck("MEMORY.md", []byte(line+"\n"), none).Hooks; len(hooks) != 0 {
			t.Errorf("unresolvable id produced a verdict: %+v", hooks)
		}
	}

	// One unknown id ALONGSIDE a terminal one still suppresses the whole line: if
	// one claim on the line cannot be assessed, neither can the line's overall
	// consistency.
	mixed := fakeResolver(map[string]string{"mg-974c": "archived"}) // mg-3119 absent = ambiguous
	if hooks := StaleCheck("MEMORY.md", []byte(lineAmbiguous3119+"\n"), mixed).Hooks; len(hooks) != 0 {
		t.Errorf("a line with one unknown id and one terminal id fired: %+v", hooks)
	}
}

// TestBlindResolverIsNotHealth is the regression test for a bug this check
// SHIPPED WITH and that its own positive control caught.
//
// macOS ships /usr/bin/mg — an unrelated micro-emacs clone. On a box where the
// real tracker is not on PATH, MgAvailable() finds that one and returns true,
// every `mg show --json` returns garbage, every id goes StateUnknown, no hook
// fires, and the doctor reported "no stale hooks". A blind instrument reporting
// healthy, which is the precise failure mode this whole ticket is about: it was
// reproduced by accident while running the required control, on a check written
// specifically to avoid it.
//
// An availability probe cannot catch it, because the wrong binary IS available.
// The honest signal is the hit rate.
func TestBlindResolverIsNotHealth(t *testing.T) {
	blind := fakeResolver(nil) // every lookup fails, as the wrong `mg` does
	data := []byte(lineStale8614 + "\n" + lineStaleE996 + "\n")

	rep := StaleCheck("MEMORY.md", data, blind)
	if len(rep.Hooks) != 0 {
		t.Fatalf("blind resolver produced verdicts: %+v", rep.Hooks)
	}
	if rep.Attempted == 0 {
		t.Fatal("Attempted = 0 on two lines carrying resolvable-looking ids; the hit rate cannot be judged")
	}
	if rep.Resolved != 0 {
		t.Fatalf("Resolved = %d with a resolver that resolves nothing", rep.Resolved)
	}
	if !rep.Blind() {
		t.Error("Blind() = false while every lookup failed — an empty Hooks list would be read as health")
	}
}

// TestNoCandidateLinesIsNotBlind pins the other side: Attempted == 0 must NOT be
// reported as blindness. The assertion and resolution filters are purely
// textual, so "no line makes a live-state claim about a work item" is true
// whether or not the tracker is reachable, and warning there would make the
// check permanently noisy on every healthy index.
func TestNoCandidateLinesIsNotBlind(t *testing.T) {
	data := []byte("# Memory index\n\n- [Plain](plain.md) — an ordinary hook with no claim and no id\n")
	rep := StaleCheck("MEMORY.md", data, fakeResolver(nil))
	if rep.Attempted != 0 {
		t.Errorf("Attempted = %d, want 0 — no line names a work item", rep.Attempted)
	}
	if rep.Blind() {
		t.Error("Blind() = true with nothing to look up; a healthy index would warn forever")
	}
}

// TestResolvedCountsPartialVisibility: a resolver that answers some lookups is
// not blind, even though the unanswered ones stay silent.
func TestResolvedCountsPartialVisibility(t *testing.T) {
	// mg-8614 resolves; mg-e996 does not.
	partial := fakeResolver(map[string]string{"mg-8614": "shelved"})
	rep := StaleCheck("MEMORY.md", []byte(lineStale8614+"\n"+lineStaleE996+"\n"), partial)
	if rep.Blind() {
		t.Error("Blind() = true with one id resolved")
	}
	if len(rep.Hooks) != 1 {
		t.Fatalf("Hooks = %d, want 1 (the resolvable stale one)", len(rep.Hooks))
	}
	if rep.Attempted != 2 || rep.Resolved != 1 {
		t.Errorf("Attempted/Resolved = %d/%d, want 2/1", rep.Attempted, rep.Resolved)
	}
}

// TestLiveItemIsNotStale: a hook asserting an open state about an item that is
// genuinely still open is CORRECT and must not fire. This is the base case the
// whole check exists to leave alone.
func TestLiveItemIsNotStale(t *testing.T) {
	for _, status := range []string{"available", "claimed", "pending"} {
		resolve := fakeResolver(map[string]string{"mg-8614": status})
		if hooks := StaleCheck("MEMORY.md", []byte(lineStale8614+"\n"), resolve).Hooks; len(hooks) != 0 {
			t.Errorf("status %q (live) produced a stale verdict: %+v", status, hooks)
		}
	}
}

// TestPendingKeywordAgainstPendingStatus pins the collision named in the code: the
// assertion word `pending` and the macguffin status `pending` are different
// things, and a hook saying "pending" about a pending item is exactly consistent.
func TestPendingKeywordAgainstPendingStatus(t *testing.T) {
	line := "- [Thing](thing.md) — mg-abcd pending review from Daniel"
	if hooks := StaleCheck("MEMORY.md", []byte(line+"\n"), fakeResolver(map[string]string{"mg-abcd": "pending"})).Hooks; len(hooks) != 0 {
		t.Errorf("`pending` hook on a `pending`-status item fired: %+v", hooks)
	}
	// The same line over an archived item IS stale.
	if hooks := StaleCheck("MEMORY.md", []byte(line+"\n"), fakeResolver(map[string]string{"mg-abcd": "archived"})).Hooks; len(hooks) != 1 {
		t.Errorf("`pending` hook on an archived item did not fire: %+v", hooks)
	}
}

// TestUnblockedIsNotBlocked pins the substring trap that motivated word-boundary
// matching: `unblocked` CONTAINS `blocked` and asserts the exact opposite.
func TestUnblockedIsNotBlocked(t *testing.T) {
	if got := phraseOffsets("piece 3 unblocked", "blocked"); len(got) != 0 {
		t.Errorf("`blocked` matched inside `unblocked` at %v — a substring test reads the opposite claim", got)
	}
	if got := phraseOffsets("piece 3 blocked", "blocked"); len(got) != 1 {
		t.Errorf("`blocked` did not match a real occurrence: %v", got)
	}
}

// TestOpenKeepsItsColon pins the measured reason `OPEN:` is listed with a colon.
// A colonless case-insensitive `open` matches memory FILENAMES — the correctly
// maintained mg-ba0b line links `project_post_eta5_decision_open.md` — and would
// flag it on the strength of a link target rather than a claim.
func TestOpenKeepsItsColon(t *testing.T) {
	for _, a := range Assertions {
		if strings.EqualFold(a, "open") {
			t.Fatalf("Assertions contains bare %q; it matches filenames like project_..._open.md", a)
		}
	}
	filenameOnly := "- [T](project_post_eta5_decision_open.md) — mg-abcd"
	if got := phraseMatches(filenameOnly, Assertions); len(got) != 0 {
		t.Errorf("a link target ending in _open.md matched an assertion: %+v", got)
	}
	realClaim := "- [T](t.md) — mg-abcd OPEN: awaiting Daniel"
	if got := phraseMatches(realClaim, Assertions); len(got) != 1 || got[0].phrase != "OPEN:" {
		t.Errorf("`OPEN:` did not match a real claim: %+v", got)
	}
}

// TestProximityBoundIsMeasured re-checks the calibration of
// AssertionProximityChars against the four real lines it was fitted to, so
// changing the constant without re-measuring fails the build.
func TestProximityBoundIsMeasured(t *testing.T) {
	// Distances measured on the real lines: 13 and 17 for the stale pair, 109 and
	// 222 for the guidance pair.
	const (
		maxStale    = 17
		minGuidance = 109
	)
	if AssertionProximityChars <= maxStale {
		t.Errorf("AssertionProximityChars = %d, must exceed %d or the measured stale hooks stop firing",
			AssertionProximityChars, maxStale)
	}
	if AssertionProximityChars >= minGuidance {
		t.Errorf("AssertionProximityChars = %d, must stay under %d or measured guidance lines produce false STALE verdicts",
			AssertionProximityChars, minGuidance)
	}
}

// TestStatusState pins the status vocabulary split, including that an unrecognized
// status is UNKNOWN rather than assumed either way — a status this code has never
// heard of is not evidence for any verdict.
func TestStatusState(t *testing.T) {
	cases := map[string]ItemState{
		"done":        StateTerminal,
		"shelved":     StateTerminal,
		"archived":    StateTerminal,
		"available":   StateLive,
		"claimed":     StateLive,
		"pending":     StateLive,
		"ARCHIVED":    StateTerminal, // case-insensitive
		" archived ":  StateTerminal, // trimmed
		"":            StateUnknown,
		"quarantined": StateUnknown, // a status added upstream after this code
	}
	for status, want := range cases {
		if got := StatusState(status); got != want {
			t.Errorf("StatusState(%q) = %v, want %v", status, got, want)
		}
	}
}

// TestParseMgShow covers the resolver's decision layer against real captured
// `mg show --json` output, including both unresolvable shapes.
func TestParseMgShow(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		runErr     error
		wantState  ItemState
		wantStatus string
	}{
		{
			name:       "resolved shelved item",
			out:        `{"id":"mg-8614","type":"task","status":"shelved","title":"bridget: write channels.toml"}`,
			wantState:  StateTerminal,
			wantStatus: "shelved",
		},
		{
			name:      "resolved live item",
			out:       `{"id":"mg-cb71","status":"claimed"}`,
			wantState: StateLive,
			// status carried through for reporting
			wantStatus: "claimed",
		},
		{
			// Verbatim from `mg show mg-3119 --json` on the development box.
			name:      "ambiguous short id",
			out:       `{"error":{"code":"ambiguous_id","category":"conflict","exit":4,"message":"mg-3119: ambiguous — 2 work items share this ID","retryable":false}}`,
			runErr:    errors.New("exit status 4"),
			wantState: StateUnknown,
		},
		{
			name:      "not found: nonzero exit, no usable stdout",
			out:       ``,
			runErr:    errors.New("exit status 3"),
			wantState: StateUnknown,
		},
		{
			name:      "nonzero exit but parseable body is still unknown",
			out:       `{"id":"mg-abcd","status":"archived"}`,
			runErr:    errors.New("exit status 1"),
			wantState: StateUnknown,
		},
		{
			name:      "garbage output",
			out:       `not json at all`,
			wantState: StateUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, status := parseMgShow([]byte(tc.out), tc.runErr)
			if state != tc.wantState {
				t.Errorf("state = %v, want %v", state, tc.wantState)
			}
			if tc.wantStatus != "" && status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
		})
	}
}

// TestStaleCheckNilResolverIsSilent: with no way to resolve an ID there is no
// evidence for any verdict, so the check degrades to inert rather than guessing.
func TestStaleCheckNilResolverIsSilent(t *testing.T) {
	if rep := StaleCheck("MEMORY.md", []byte(lineStale8614+"\n"), nil); rep.Hooks != nil {
		t.Errorf("nil resolver produced verdicts: %+v", rep.Hooks)
	}
}

// TestLinesWithoutIDsAreIgnored: staleness is only decidable for a claim that
// names a resolvable item. A tense-bearing line with no id is unfalsifiable here
// and must not fire.
func TestLinesWithoutIDsAreIgnored(t *testing.T) {
	data := []byte("- [T](t.md) — blocked on Daniel granting the perm\n" +
		"- [U](u.md) — OPEN: pending review\n")
	if hooks := StaleCheck("MEMORY.md", data, fakeResolver(map[string]string{"mg-8614": "archived"})).Hooks; len(hooks) != 0 {
		t.Errorf("lines with no work-item id fired: %+v", hooks)
	}
}

// TestResolverIsCalledOncePerDistinctID pins the memoization contract on the
// pure side: a duplicated id on one line is resolved once. On the mg-backed
// resolver each lookup is a process spawn, and the same id recurs across the 16
// memory dirs on the development box.
func TestResolverIsCalledOncePerDistinctID(t *testing.T) {
	calls := map[string]int{}
	resolve := func(id string) (ItemState, string) {
		calls[id]++
		return StateTerminal, "archived"
	}
	line := "- [T](t.md) — mg-abcd blocked, see mg-abcd and mg-abcd\n"
	StaleCheck("MEMORY.md", []byte(line), resolve)
	if calls["mg-abcd"] != 1 {
		t.Errorf("resolver called %d times for one distinct id, want 1", calls["mg-abcd"])
	}
}

// TestWorkItemIDAnchoring: a 4-hex id must not be mined out of a longer token.
func TestWorkItemIDAnchoring(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"mg-8614", 1},
		{"(mg-8614)", 1},
		{"mg-8614.", 1},
		{"v0.4.0/mg-bc47", 1},
		{"mg-86140", 0}, // longer than 4 hex digits
		{"xmg-8614", 0}, // not at a word boundary
		{"mg-861", 0},   // too short
		{"mg-zzzz", 0},  // not hex
		{"MG-8614", 0},  // ids are lowercase
		{"mg-8614 mg-e996", 2},
	}
	for _, tc := range cases {
		if got := len(workItemID.FindAllString(tc.in, -1)); got != tc.want {
			t.Errorf("workItemID on %q found %d, want %d", tc.in, got, tc.want)
		}
	}
}
