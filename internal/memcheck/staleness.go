package memcheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// STALE TENSE-BEARING ASSERTIONS — the third way an auto-memory store fails, and
// the one that is hardest to notice because it does not look like a failure at
// all.
//
// An index line that says `blocked: bot can't create channels` about a work item
// is making a claim that DECAYS. The item gets shelved, archived, or done; the
// hook does not move. What the agent then loads at session start is a confident,
// well-formed assertion about the present that stopped being true weeks ago.
// A size check reads it as healthy because it is the right length, and a parity
// check reads it as healthy because the file it names exists. It is current-
// looking, indexed, and wrong.
//
// Measured across all 16 memory dirs on the development box (mg-cb71): 6 index
// lines carried both a tense-bearing word and a short work-item ID; of the 3
// whose IDs resolved uniquely, 2 were genuinely stale — a hook still presenting
// a SHELVED item as "blocked", and another still presenting an ARCHIVED audit as
// "Piece 2 blocked".
//
// == THE PREDICATE IS CONSISTENCY, NOT KEYWORDS ==
//
// The third resolvable line is why. It reads `(superseded) Post-η₅ decision
// RESOLVED via option 3 — mg-ba0b done.`, and mg-ba0b is archived. A
// keyword-only filter flags it; it is CORRECTLY MAINTAINED. The line already
// says the thing resolved. Firing on it would train a reader to dismiss the
// check, and the natural response to a "stale" verdict is to DELETE the hook —
// so a false positive here does not merely waste attention, it destroys a
// correct memory.
//
// So a line fires only when all four hold:
//
//  1. it makes a live-state assertion (Assertions, word-boundary matched),
//  2. the assertion sits NEXT TO the ID it is about (AssertionProximityChars),
//  3. it does NOT also record a resolution (Resolutions),
//  4. every work-item ID on it resolves UNIQUELY to a terminal status.
//
// Condition 2 came out of running conditions 1, 3 and 4 against the real corpus
// and reading what they caught. A generic guidance memory — "when a build/gate I
// own is in flight, pull status actively ... (mayor had to proceed without me on
// v0.4.0/mg-bc47)" — satisfies all three: `in flight` is an assertion, no
// resolution word appears, and mg-bc47 is archived. But it is not stale. The
// tense word sits in a conditional clause describing when the guidance applies,
// and the ID is a trailing citation of the incident that produced the guidance.
// The claim and the ID are not about each other. See AssertionProximityChars.
//
// == THE INSTRUMENT CAVEAT ==
//
// Short-ID lookup is not reliable on a real box, and this was verified rather
// than assumed. Both failure modes are live:
//
//	mg show mg-3119  ->  ambiguous — 2 work items share this ID (exit 4)
//	mg show mg-zzzz  ->  not found (exit 3)
//
// A checker that resolves IDs MUST treat ambiguous or not-found as StateUnknown
// and stay silent. Never as stale. A false "stale" verdict is worse than no
// check at all, for the deletion reason above. StaleCheck therefore suppresses a
// whole line if ANY id on it is unknown: if one claim on the line cannot be
// assessed, the line's overall consistency cannot be either.
//
// This bias toward silence is deliberate and it does cost recall — a line that
// pairs a resolution word with a second, genuinely stale ID will be missed. That
// trade is the right way round here.

// ItemState is the coarse classification of a work item's status that staleness
// turns on. The exact status string is carried alongside for reporting; only the
// three-way split drives the decision.
type ItemState string

const (
	// StateUnknown means the ID did not resolve to exactly one item — not
	// found, ambiguous across archive partitions, or the resolver failed. It is
	// the SAFE state: an unknown ID never contributes to a stale verdict.
	StateUnknown ItemState = "unknown"
	// StateLive means the item is still in play, so a hook asserting an open
	// state about it is consistent.
	StateLive ItemState = "live"
	// StateTerminal means the item has reached an end state — done, archived, or
	// shelved — so a hook still asserting an open state about it has decayed.
	StateTerminal ItemState = "terminal"
)

// StatusResolver maps a short work-item ID to its state and its raw status
// string. It is a PARAMETER rather than a hard-wired call to any tracker, so
// this package stays pure and the whole predicate is testable against fabricated
// states — including the ambiguous and not-found paths, which are the ones that
// matter most and which a real box cannot be asked to produce on demand.
//
// A resolver MUST return StateUnknown for anything it cannot resolve to exactly
// one item. See NewMgResolver.
type StatusResolver func(id string) (ItemState, string)

// workItemID matches a macguffin short ID: `mg-` plus exactly 4 hex digits.
//
// Anchored on both sides against hex-adjacent characters so a longer token is
// not mined for a 4-digit prefix.
var workItemID = regexp.MustCompile(`\bmg-[0-9a-f]{4}\b`)

// Assertions are the phrases that make an index line's claim tense-bearing —
// a statement about a state that can expire.
//
// `OPEN:` KEEPS ITS COLON, and that is a measured decision, not punctuation
// taste. A bare case-insensitive `open` matches memory FILENAMES: the correctly
// maintained mg-ba0b line links `project_post_eta5_decision_open.md`, and a
// colonless pattern flags it on the strength of a link target rather than a
// claim. The colon is what makes the match a claim.
var Assertions = []string{
	"OPEN:",
	"pending",
	"blocked",
	"in flight",
}

// AssertionProximityChars is how far apart an assertion phrase and a work-item
// ID may sit on one line and still be read as being about each other.
//
// An index hook that asserts a live state ABOUT an item writes them as one
// clause, because that is what a hook is. When they are far apart the line has
// changed subject: the assertion belongs to a different clause, and the ID is a
// citation rather than the thing being asserted about.
//
// MEASURED on the real corpus, not chosen (mg-cb71). Character distance from
// assertion to nearest ID:
//
//	13   "...routing config written (mg-8614) but blocked: bot can't..."   STALE
//	17   "...— mg-e996: Piece 2 blocked; Step 6 grounded forms..."         STALE
//	109  "...when a build/gate I own is in flight, ... (v0.4.0/mg-bc47)"   guidance
//	222  "Verify Daniel-pending blocker premise ... (mg-9299 ... )"        guidance
//
// 60 sits with ~3.5x margin on both sides of the gap between the two
// populations. Fitted to four lines, so treat it as a working bound rather than a
// constant of nature — but note which way it errs. Raising it admits false
// "stale" verdicts, whose natural remedy is deleting a correct hook; lowering it
// only makes the check quieter. When in doubt, lower.
const AssertionProximityChars = 60

// Resolutions are the phrases that record an assertion having been settled. A
// line carrying one is treated as maintained, whatever its work item's status.
//
// `unblocked` earns its place twice over. It is a resolution word, and it also
// CONTAINS `blocked` — so a substring test for the assertion matches the exact
// opposite claim. Word-boundary matching handles that (see hasPhrase), and
// listing it here handles the rest.
var Resolutions = []string{
	"resolved",
	"unblocked",
	"superseded",
	"done",
	"closed",
	"landed",
	"merged",
}

// StaleHook is one index line whose tense-bearing claim has outlived the work
// item it names.
type StaleHook struct {
	// IndexPath and Line locate the defect; Line is 1-indexed so it can be
	// pasted after a colon.
	IndexPath string
	Line      int
	// Text is the offending index line, verbatim.
	Text string
	// Assertion is the phrase that made this line a claim about the present.
	Assertion string
	// Items lists the terminal work items named on the line, as "mg-xxxx
	// (archived)" — what makes the claim wrong, and what a reader needs in order
	// to rewrite the hook rather than delete it.
	Items []string
}

// StaleReport is the outcome of scanning one index, and it carries the
// resolver's own hit rate alongside the findings.
//
// Attempted/Resolved exist so a caller can tell "nothing stale" apart from
// "could not look" — a distinction this check got wrong once, in the exact shape
// the ticket behind it warns about. macOS ships /usr/bin/mg, an unrelated
// micro-emacs clone. On a box where the real tracker is not on PATH,
// MgAvailable() finds THAT one, every lookup returns garbage, every id goes
// StateUnknown, no hook fires — and the check reported "no stale hooks", which is
// a blind instrument reporting healthy.
//
// An availability probe cannot fix this on its own: the wrong binary IS
// available. The honest signal is the hit rate. If lookups were attempted and
// none resolved, the resolver is blind and the check must say so.
type StaleReport struct {
	// Hooks are the stale index lines found.
	Hooks []StaleHook
	// Attempted is how many distinct work-item IDs were looked up, and Resolved
	// how many of those came back as a known status. Attempted > 0 with
	// Resolved == 0 means the resolver could not see anything — report that, do
	// NOT report the empty Hooks list as health. See Blind.
	Attempted int
	Resolved  int
}

// Blind reports whether the resolver failed on every ID it was given, so no
// conclusion about staleness is available either way.
//
// Attempted == 0 is NOT blind: the assertion and resolution filters are purely
// textual, so "no line makes a live-state claim about a work item" is a true
// statement regardless of whether the tracker is reachable.
func (r StaleReport) Blind() bool {
	return r.Attempted > 0 && r.Resolved == 0
}

// StaleCheck scans an already-read index for lines whose live-state assertions
// contradict the resolved status of the work items they name.
//
// Pure apart from resolve: the file contents come in as data so a fixture can be
// checked without touching disk, and every status decision is the resolver's.
// An empty Hooks list is the expected result on a maintained index — but check
// Blind() before reading it as one.
func StaleCheck(indexPath string, data []byte, resolve StatusResolver) StaleReport {
	var rep StaleReport
	if resolve == nil {
		return rep
	}
	var out []StaleHook
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		idMatches := workItemID.FindAllStringIndex(line, -1)
		if len(idMatches) == 0 {
			continue
		}
		asserts := phraseMatches(line, Assertions)
		if len(asserts) == 0 {
			continue
		}
		// A line that records its own resolution is maintained, not stale.
		if len(phraseMatches(line, Resolutions)) > 0 {
			continue
		}
		// Any unknown id makes the whole line unassessable. Silence, never
		// "stale" — see the instrument caveat above. This is checked across
		// EVERY id on the line, not just the nearby ones: if one claim on the
		// line cannot be assessed, the line's consistency cannot be either.
		var terminal []string
		unknown := false
		for _, id := range dedupe(idsOf(line, idMatches)) {
			state, status := resolve(id)
			rep.Attempted++
			if state != StateUnknown {
				rep.Resolved++
			}
			switch state {
			case StateTerminal:
				terminal = append(terminal, id+" ("+status+")")
			case StateUnknown:
				unknown = true
			}
		}
		if unknown || len(terminal) == 0 {
			continue
		}
		// The assertion must be about an id, not merely on the same line as one.
		assertion := nearestAssertion(asserts, idMatches)
		if assertion == "" {
			continue
		}
		out = append(out, StaleHook{
			IndexPath: indexPath,
			Line:      i + 1,
			Text:      line,
			Assertion: assertion,
			Items:     terminal,
		})
	}
	rep.Hooks = out
	return rep
}

// nearestAssertion returns the first assertion phrase that sits within
// AssertionProximityChars of some work-item ID, or "" if none does.
func nearestAssertion(asserts []phraseMatch, ids [][]int) string {
	for _, a := range asserts {
		for _, id := range ids {
			d := id[0] - a.at
			if d < 0 {
				d = -d
			}
			if d <= AssertionProximityChars {
				return a.phrase
			}
		}
	}
	return ""
}

// idsOf extracts the matched ID strings for the offsets FindAllStringIndex gave.
func idsOf(line string, matches [][]int) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, line[m[0]:m[1]])
	}
	return out
}

// StaleCheckFile is StaleCheck over a file on disk.
func StaleCheckFile(indexPath string, resolve StatusResolver) (StaleReport, error) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return StaleReport{}, err
	}
	return StaleCheck(indexPath, data, resolve), nil
}

// phraseMatch is one occurrence of a listed phrase, with the byte offset it
// starts at so proximity can be measured.
type phraseMatch struct {
	phrase string
	at     int
}

// phraseMatches returns every whole-word occurrence of any phrase in want,
// in order of position.
//
// Comparison is case-insensitive EXCEPT that a phrase containing an uppercase
// letter is matched case-sensitively — which is how `OPEN:` stays a deliberate
// marker rather than a match on any prose "open:".
func phraseMatches(line string, want []string) []phraseMatch {
	lower := strings.ToLower(line)
	var out []phraseMatch
	for _, p := range want {
		hay, needle := lower, strings.ToLower(p)
		if p != needle {
			hay, needle = line, p
		}
		for _, at := range phraseOffsets(hay, needle) {
			out = append(out, phraseMatch{phrase: p, at: at})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// phraseOffsets returns every offset at which needle occurs in hay bounded by
// non-word characters.
//
// The boundary is the whole point: `unblocked` must not satisfy a search for
// `blocked`, because those are opposite claims and a plain substring test cannot
// tell them apart. A needle ending in punctuation (`OPEN:`) carries its own
// trailing boundary and needs no check on the character after it.
func phraseOffsets(hay, needle string) []int {
	if needle == "" {
		return nil
	}
	needsTrailing := isWordByte(needle[len(needle)-1])
	var out []int
	for off := 0; off <= len(hay)-len(needle); {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			break
		}
		at := off + i
		end := at + len(needle)
		beforeOK := at == 0 || !isWordByte(hay[at-1])
		afterOK := !needsTrailing || end == len(hay) || !isWordByte(hay[end])
		if beforeOK && afterOK {
			out = append(out, at)
		}
		off = at + 1
	}
	return out
}

// isWordByte reports whether c is a letter, digit, or underscore.
func isWordByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_':
		return true
	}
	return false
}

// dedupe returns ids with duplicates removed, order preserved.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// terminalStatuses are the macguffin statuses that mean a work item has stopped
// moving. The full vocabulary is available, claimed, pending, done, shelved,
// archived; the other three are live.
//
// `pending` is LIVE and the collision with the assertion word is worth naming:
// an index hook saying "pending" about an item whose status is `pending` is
// exactly consistent, and must not fire.
var terminalStatuses = map[string]bool{
	"done":     true,
	"shelved":  true,
	"archived": true,
}

// StatusState classifies a raw status string. Anything outside the known
// vocabulary is StateUnknown rather than assumed live or terminal — a status
// this code has never heard of is not evidence for either verdict.
func StatusState(status string) ItemState {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case terminalStatuses[s]:
		return StateTerminal
	case s == "available" || s == "claimed" || s == "pending":
		return StateLive
	}
	return StateUnknown
}

// mgShowResult is the shape of `mg show <id> --json` that matters here. On
// failure mg emits an error object instead of an item, which is how ambiguous
// and not-found arrive.
type mgShowResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseMgShow turns one `mg show --json` invocation into a state.
//
// Split out from the exec call so the decisions that matter are testable against
// real captured output — in particular the two that must yield StateUnknown:
//
//	{"error":{"code":"ambiguous_id",...}}   two archived twins share the short ID
//	(exit 3, no usable stdout)              no such item
//
// runErr is honoured even when stdout parses, because a nonzero exit means mg
// did not answer the question asked.
func parseMgShow(out []byte, runErr error) (ItemState, string) {
	var r mgShowResult
	if err := json.Unmarshal(out, &r); err != nil {
		return StateUnknown, ""
	}
	if r.Error != nil {
		return StateUnknown, ""
	}
	if runErr != nil || r.Status == "" {
		return StateUnknown, ""
	}
	return StatusState(r.Status), r.Status
}

// NewMgResolver returns a StatusResolver backed by the `mg` CLI, memoized for
// the life of the resolver.
//
// Memoization is not micro-optimization: the same work-item ID recurs across
// indexes (16 memory dirs on the development box), each lookup is a process
// spawn, and `pogo doctor` is expected to be cheap enough to run reflexively.
//
// If `mg` is not on PATH the resolver returns StateUnknown for everything, which
// makes the staleness check silently inert rather than an error. That is the
// correct degradation: with no way to resolve an ID there is no evidence for any
// verdict, and the caller can distinguish "nothing stale" from "could not look"
// by testing MgAvailable first.
func NewMgResolver() StatusResolver {
	if !MgAvailable() {
		return func(string) (ItemState, string) { return StateUnknown, "" }
	}
	var mu sync.Mutex
	type entry struct {
		state  ItemState
		status string
	}
	cache := map[string]entry{}
	return func(id string) (ItemState, string) {
		mu.Lock()
		if e, ok := cache[id]; ok {
			mu.Unlock()
			return e.state, e.status
		}
		mu.Unlock()
		out, err := exec.Command("mg", "show", id, "--json").Output()
		state, status := parseMgShow(out, err)
		mu.Lock()
		cache[id] = entry{state, status}
		mu.Unlock()
		return state, status
	}
}

// MgAvailable reports whether SOMETHING named `mg` is on PATH.
//
// It is a necessary check and NOT a sufficient one, and the gap is not
// hypothetical: macOS ships /usr/bin/mg, an unrelated micro-emacs clone, so on a
// box without the real tracker this returns true and every lookup then fails.
// Callers must ALSO consult StaleReport.Blind() before reading an empty findings
// list as health. See StaleReport.
func MgAvailable() bool {
	_, err := exec.LookPath("mg")
	return err == nil
}

// SortHooks orders hooks by index path then line, for deterministic reporting.
func SortHooks(hooks []StaleHook) {
	sort.SliceStable(hooks, func(i, j int) bool {
		if hooks[i].IndexPath != hooks[j].IndexPath {
			return hooks[i].IndexPath < hooks[j].IndexPath
		}
		return hooks[i].Line < hooks[j].Line
	})
}
