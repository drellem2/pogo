package wedgewatch

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// This file is mg-fc8d item (2): read the agent's OWN claim about how long it
// has been working, so it can be put beside how long its process has actually
// existed.
//
// It is the half that matters most, because it needs no enumeration. markers.go
// can only recognise a dead end somebody has already met; this recognises that
// the agent's own arithmetic does not add up, whatever prompt it is parked at.
// On both incident nights the whole diagnosis was two numbers side by side:
//
//	uptime   13h44m        (from the process table — honest)
//	declared "Baked for 3m 2s"  (from the session — impossible)
//
// # Unreadable is a THIRD answer, not a pass
//
// ParseDeclaredWork returns ok=false when it cannot find a counter, and the
// watcher treats that as "could not look" — it emits an error event and falls
// back to a different stall signal. It never treats it as agreement. That rule
// is inherited from internal/credexpiry's absence-as-evidence trap, and it is
// the single most load-bearing convention in this whole family of detectors:
// the fault being detected is, in every case, an instrument that read healthy
// because it could not see.

// counterStem is a phrase the harness prints adjacent to its elapsed counter.
//
// Stems are matched against the whitespace-stripped buffer (see Normalize), so
// they are written here without spaces. The harness renders these in a TUI
// status line whose spacing is cursor-move escapes; agent.StripANSI deletes
// those rather than substituting spaces, which is why "Baked for 3m 2s" arrives
// as "Bakedfor3m2s" and why the stems are spelled that way.
type counterStem struct {
	// text is the normalized phrase to find.
	text string
	// after searches this many bytes following the stem for a duration.
	after int
	// before searches this many bytes preceding the stem. Used for stems the
	// harness prints AFTER the number.
	before int
}

// defaultStems covers the forms observed on this box. It is ordered by
// specificity: the first stem that yields a duration wins.
//
// The list is short and will drift with the harness. That is acceptable here in
// a way it is not in markers.go, because a stem that stops matching makes the
// counter UNREADABLE — which is reported loudly as an error, not silently as
// health. A marker that stops matching, by contrast, just goes quiet; that
// asymmetry is why mg-f36b could hide for two months and this cannot.
//
// # What mg-20eb found when all four original stems missed at once
//
// The package doc predicted stem drift. What it did not predict was every stem
// failing on every agent simultaneously, which is what the harness on this box
// did by 2026-08-09 — 40 judgements, 0 verdicts. Sampling the live PTYs of five
// agents (doctor, mayor, architect, pm-pogo, and the polecat that wrote this)
// found three separate changes, and only one of them is ordinary drift:
//
//  1. The completed-turn line now reads "✻ worked for 55s". Not "Baked for".
//  2. The in-flight counter moved into a spinner parenthetical whose VERB is
//     randomized — "cerebrating…", "crystallizing…", "slithering…", "Baking…"
//     are all live renderings of the same line — so no verb can anchor it. What
//     is stable is the shape: "(11m53s · ↓ 29.6k tokens)".
//  3. "esc to interrupt" left that parenthetical and became part of a
//     PERMANENT hint bar: "⏵⏵ bypass permissions on (shift+tab to cycle) · esc
//     to interrupt · ← for agents".
//
// (3) is the one worth understanding, because that stem did not stop matching —
// it matches on every agent on every pass, and carries no number. A stem that
// matches a permanently-rendered string is a FALSE ANCHOR: it will keep
// reporting "found the status line, no counter in it" long after the status
// line has moved, and whatever number happens to drift within its window
// becomes the agent's declared work. It is kept for older harness versions and
// demoted to last, so any anchor that is still attached to a real counter wins
// first. Only firstDuration/lastDuration's onlySeparators rule kept it from
// reading the spinner's repaint digits as a counter.
// # Why the order is current-harness first, and legacy after
//
// lastDurationNear takes the LAST occurrence of a stem, so a status line that
// repaints at the tail of the buffer normally beats an earlier mention of the
// same phrase in a transcript. That protection does NOT hold across stems: a
// higher-priority stem quoted once, anywhere, beats a lower-priority stem
// rendered live at the tail. An agent reading this very file has "Baked for 3m
// 2s" in its own PTY.
//
// So the stems the CURRENT harness actually renders go first. Nothing is lost
// on an older harness — the current stems are strings it never emits, so they
// fail and fall through — and on this one a quoted legacy counter can no longer
// outrank a live one.
var defaultStems = []counterStem{
	// "(11m53s · ↓ 29.6k tokens)" — the 2026-08-09 in-flight parenthetical.
	// Anchored on the token arrow rather than on the spinner verb, because the
	// verb is randomized per render and the arrow is not.
	//
	// It outranks "workedfor" below deliberately. Both are current, but this
	// one is the LIVE counter and that one is the previous turn's total, which
	// is frozen for the whole of the current turn — so preferring it would read
	// a legitimately long turn as a frozen counter beside a large uptime, which
	// is the exact shape of a wedge.
	{text: "·↓", before: 24},
	// "✻ worked for 55s" — the 2026-08-09 completed-turn line, replacing
	// "Baked for". Read when no turn is in flight.
	{text: "workedfor", after: 24},
	// "Baked for 3m 2s" — the completed-turn form on the harness of 2026-08-04,
	// and the exact string read off both wedged terminals.
	{text: "bakedfor", after: 24},
	// "Baking for 3m 2s" — the in-flight variant of the same line.
	{text: "bakingfor", after: 24},
	// "(2m 56s · esc to interrupt)" — the older spinner parenthetical. The
	// number can sit on either side of the phrase depending on harness version,
	// so both directions are searched. LAST because this phrase is also the
	// permanent hint bar on the current harness; see the false-anchor note.
	{text: "esctointerrupt", after: 24, before: 24},
	// "tokens · 2m 56s" style status lines put the elapsed time immediately
	// before the interrupt hint; this catches the variant that omits it.
	{text: "esctocancel", after: 24, before: 24},
}

// durationPattern matches the harness's elapsed-time rendering after
// whitespace has been stripped: "13h44m2s", "3m2s", "45s", "1h2m", "7h".
//
// Alternatives are longest-first because Go's regexp is leftmost-FIRST for
// alternation (RE2's POSIX leftmost-longest mode is opt-in and not used here),
// so "3m2s" must be offered before "3m" or the seconds would be dropped.
var durationPattern = regexp.MustCompile(
	`\d+h\d+m\d+s|\d+h\d+m|\d+m\d+s|\d+h\d+s|\d+h|\d+m|\d+s`)

// componentPattern splits a matched duration into its parts.
var componentPattern = regexp.MustCompile(`(\d+)([hms])`)

// ParseDeclaredWork extracts the agent's own elapsed-work counter from raw PTY
// bytes.
//
// It returns the value from the LAST stem occurrence in the buffer — the most
// recent render — and ok=false if no stem yields a duration. A false here means
// "I could not read the counter", never "the counter is fine".
func ParseDeclaredWork(raw []byte) (time.Duration, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	// Lower-cased as well as whitespace-stripped: the harness capitalises the
	// stem at the start of a status line ("Baked for …") and not mid-line
	// ("… esc to interrupt"), and a case-sensitive table would silently match
	// one rendering and miss the other. The duration grammar is all digits and
	// lower-case h/m/s, so this costs nothing.
	clean := strings.ToLower(string(Normalize(agent.StripANSI(raw))))
	for _, stem := range defaultStems {
		if d, ok := lastDurationNear(clean, stem); ok {
			return d, true
		}
	}
	return 0, false
}

// lastDurationNear finds the final occurrence of stem in s and parses a
// duration from the window around it.
func lastDurationNear(s string, stem counterStem) (time.Duration, bool) {
	idx := lastIndex(s, stem.text)
	for idx >= 0 {
		if d, ok := durationInWindow(s, idx, len(stem.text), stem); ok {
			return d, true
		}
		// The last occurrence carried no number — an ordinary transcript
		// mention of the phrase, say. Keep walking backwards rather than giving
		// up, so one stray mention cannot blind the check.
		idx = lastIndex(s[:idx], stem.text)
	}
	return 0, false
}

// durationInWindow looks immediately after the stem, then immediately before
// it. "Immediately" is what makes this safe: a duration two hundred bytes away
// is somebody else's number.
func durationInWindow(s string, idx, stemLen int, stem counterStem) (time.Duration, bool) {
	if stem.after > 0 {
		end := min(idx+stemLen+stem.after, len(s))
		if d, ok := firstDuration(s[idx+stemLen : end]); ok {
			return d, true
		}
	}
	if stem.before > 0 {
		start := max(idx-stem.before, 0)
		if d, ok := lastDuration(s[start:idx]); ok {
			return d, true
		}
	}
	return 0, false
}

// firstDuration parses the earliest duration in window, requiring nothing but
// separator characters to stand between the stem and the number.
//
// The adjacency requirement is the point. "Bakedfor3m2s" parses and so does
// "esctointerrupt·2m56s", because the harness puts a middot between them — but
// "Bakedfor…somethingelse…3m2s" does not, because a number that far away is
// somebody else's.
func firstDuration(window string) (time.Duration, bool) {
	loc := durationPattern.FindStringIndex(window)
	if loc == nil || !onlySeparators(window[:loc[0]]) {
		return 0, false
	}
	return parseComponents(window[loc[0]:loc[1]])
}

// lastDuration parses the latest duration in window, requiring nothing but
// separators after it — the mirror of firstDuration, for stems the harness
// prints AFTER the number ("(2m 56s · esc to interrupt)").
func lastDuration(window string) (time.Duration, bool) {
	locs := durationPattern.FindAllStringIndex(window, -1)
	if len(locs) == 0 {
		return 0, false
	}
	last := locs[len(locs)-1]
	if !onlySeparators(window[last[1]:]) {
		return 0, false
	}
	return parseComponents(window[last[0]:last[1]])
}

// onlySeparators reports whether s carries no ASCII letters or digits — i.e.
// whether it is punctuation, box-drawing, or the middot the status line uses as
// a field separator. Non-ASCII bytes are treated as separators: the harness's
// decorations ("·", "⎿", "✻") are all multi-byte and none of them can be part
// of a duration.
func onlySeparators(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			return false
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func parseComponents(text string) (time.Duration, bool) {
	parts := componentPattern.FindAllStringSubmatch(text, -1)
	if len(parts) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, p := range parts {
		n, err := strconv.Atoi(p[1])
		if err != nil {
			return 0, false
		}
		switch p[2] {
		case "h":
			total += time.Duration(n) * time.Hour
		case "m":
			total += time.Duration(n) * time.Minute
		case "s":
			total += time.Duration(n) * time.Second
		}
	}
	return total, true
}

func lastIndex(s, sub string) int {
	if sub == "" {
		return -1
	}
	for i := len(s) - len(sub); i >= 0; i-- {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// DiscrepancyInput is everything the counter/uptime cross-check needs. Passed
// as a struct so the rule reads as a single expression rather than a six-
// argument call whose meaning depends on parameter order.
type DiscrepancyInput struct {
	// Uptime is how long the process has existed — the honest number.
	Uptime time.Duration
	// Declared is the agent's own counter, and DeclaredRead says whether it
	// could be parsed.
	Declared     time.Duration
	DeclaredRead bool
	// FrozenFor is how long Declared has held this single value unbroken.
	FrozenFor time.Duration
}

// Discrepancy reports whether the cross-check fires, and why.
//
// THE FREEZE TEST IS THE RULE. The ratio alone is not a wedge signal and must
// never be used as one: the declared counter measures ONE TURN, so a perfectly
// healthy agent seven hours into its life and three seconds into a new turn
// also shows a tiny counter beside a huge uptime. Every agent in the fleet
// would report, constantly, and the detector would be muted inside a day.
//
// What made 13h44m beside "2m 56s" damning was that the counter did not move.
// Had it been advancing it would have read 13h; had turns been starting and
// finishing it would have read a different value at every sample. One value,
// unchanged across a window spanning several 10-minute mail-check fires, means
// no turn began and none ended — the fires were delivered and absorbed. That is
// the fault, stated in terms of the agent's own instrumentation.
//
// The ratio and MinUptime survive as guards, not as the signal: they keep a
// young process and a merely-slightly-behind counter out of the report.
func Discrepancy(in DiscrepancyInput, th Thresholds) (bool, string) {
	th = th.withDefaults()
	if !in.DeclaredRead {
		return false, "the declared work counter could not be parsed — this is UNREADABLE, not healthy; the watcher falls back to event-log staleness"
	}
	if in.Uptime < th.MinUptime {
		return false, "process is younger than " + th.MinUptime.String()
	}
	if in.FrozenFor < th.FreezeHoldDown {
		return false, "the counter has not held one value long enough (" +
			in.FrozenFor.Round(time.Second).String() + " < " + th.FreezeHoldDown.String() +
			"); a working or merely-idle agent moves it on every turn"
	}
	// A zero declared value cannot be scaled, and a counter reading zero beside
	// hours of uptime is the same fault in its most extreme form — so it passes
	// the ratio by construction rather than dividing by zero.
	if in.Declared > 0 && float64(in.Uptime) < th.Ratio*float64(in.Declared) {
		return false, "uptime is only " +
			strconv.FormatFloat(float64(in.Uptime)/float64(in.Declared), 'f', 1, 64) +
			"x the declared counter, below the " +
			strconv.FormatFloat(th.Ratio, 'f', -1, 64) + "x threshold"
	}
	return true, "the agent's own counter has read " +
		in.Declared.Round(time.Second).String() + " unchanged for " +
		in.FrozenFor.Round(time.Second).String() + " while its process has existed for " +
		in.Uptime.Round(time.Second).String() +
		" — no turn has started or finished in that window, whatever the screen is drawing"
}
