// Package memcheck detects when an auto-memory MEMORY.md index is approaching
// the harness read cliff — the point past which the file stops loading in full.
//
// The cap is a TOKEN budget, not a byte budget. This distinction is the whole
// reason this package is shaped the way it is. An earlier version compared the
// file's size in BYTES against a constant that was really a token count, which
// made it fire at roughly a quarter of the true cliff for ordinary index
// content — a false catastrophe alarm. That mattered more than mere
// over-conservatism: the warn text tells a reader every memory is about to
// vanish, which provokes urgent compaction, and urgent compaction of the shared
// durable record is exactly where memories get lost. A miscalibrated alarm can
// cause the loss it warns about.
//
// Bytes cannot proxy for tokens because the ratio is content-dependent. Across
// the measured corpus (see memcheck_test.go) realistic file content ranges from
// ~1.8 bytes/token (dense JSON) to ~3.5 bytes/token (flat English prose) — a
// near-2x spread. No single byte number can be right for both, so this package
// estimates TOKENS and compares against a token cap.
//
// What the failure actually looks like: on the Read-tool path it is NOT silent.
// The harness refuses the read with an explicit error naming both numbers —
// "File content (57023 tokens) exceeds maximum allowed tokens (25000)" — or
// serves a partial view with a visible notice. It is still a total loss of the
// index as a whole (the caller gets an error instead of the file), but the
// earlier claim in this doc that it fails with "no error" was wrong.
//
// This package converts that cliff into a standing signal: it reports when a
// MEMORY.md crosses a warn threshold BELOW the cap, and names the token-heaviest
// index lines so the fix has a target. It DETECTS ONLY. It never rewrites
// MEMORY.md — compaction is a destructive rewrite of the shared durable record
// and stays a deliberate, human-verified judgment call (see mg-15c0). CheckFile
// opens the file read-only and returns data; it has no path that writes.
package memcheck

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// HarnessReadCapTokens is the maximum number of TOKENS the harness Read tool
// will return for a single file. A MEMORY.md over this stops loading in full,
// taking every memory it indexes down with it.
//
// This is an EXTERNAL invariant: it is a property of the harness, not of pogo,
// and it lives outside this codebase. It is not discoverable at runtime, so it
// is pinned here as a SINGLE named constant. Everything downstream derives from
// it — see WarnThresholdTokens.
//
// MEASURED, not inferred. The harness states this number itself when it refuses
// an over-cap read; the value here was read off that refusal rather than
// estimated from a file that once failed:
//
//	Read(133380-byte fixture) ->
//	  "File content (57023 tokens) exceeds maximum allowed tokens (25000)"
//
// A 41082-byte fixture — 1.6x the byte figure this constant used to hold — read
// back in full, all 527 lines, which is the direct falsification of the old
// byte reading. Update THIS line when the harness cap changes, and the warn
// point moves with it.
//
// SCOPE: this is the cap on the Read TOOL path, which is what was measured. The
// separate session-start auto-injection of MEMORY.md into context is a
// different mechanism and its budget is NOT verified here; do not read this
// constant as a statement about that path.
const HarnessReadCapTokens = 25000

// WarnFraction is the fraction of the read cap at which memcheck warns. We warn
// well before the cliff (0.8 => at 80% of the cap) so there is headroom to
// compact deliberately rather than discovering the loss after the fact.
//
// This value is load-bearing for correctness, not just taste: EstimateTokens is
// a heuristic with bounded error, so the warn point must sit far enough below
// the cap that even a maximal UNDER-estimate still fires before the cliff. The
// requirement is WarnFraction < 1 - worstUnderEstimate; the measured worst
// under-estimate is ~10.6%, so 0.8 clears it with room. TestWarnFractionAbsorbs
// EstimatorError enforces exactly that relationship against the corpus, so
// raising this constant without re-measuring will fail the build.
const WarnFraction = 0.8

// WarnThresholdTokens is the derived warn point in tokens. It TRACKS the cap by
// construction: change HarnessReadCapTokens and this moves with it. It is never
// a hardcoded count.
func WarnThresholdTokens() int {
	return int(float64(HarnessReadCapTokens) * WarnFraction)
}

// Token-estimator coefficients, fitted against a corpus of nine fixtures whose
// true token counts were measured by the harness itself (the counts and the
// fixtures live in memcheck_test.go, which re-checks this fit on every run).
//
// The model is structural rather than a flat bytes/N divisor because a flat
// divisor cannot span the corpus: the best one still mis-estimates by -10%/+76%,
// and the +76% end is the false-alarm bug all over again. Counting what the
// tokenizer actually charges for — letters, digit runs, punctuation, non-ASCII
// runes, and the per-line number prefix the harness prepends — holds every
// fixture to within ~10.6%.
//
// These are empirical coefficients. Do not "tidy" them toward rounder numbers
// without re-running the corpus test.
const (
	tokensPerAlphaChar  = 0.36
	tokensPerDigitChar  = 0.6
	tokensPerPunctChar  = 1.1
	tokensPerNonASCII   = 1.25
	tokensPerLinePrefix = 1.0
)

// EstimateTokens approximates how many tokens the harness will charge for data.
//
// It is an ESTIMATE with measured error bounds (~±10.6% across the corpus), not
// a tokenizer. It exists because the real tokenizer is not available in-process
// and the quantity that matters — the harness's token count — cannot be
// obtained any other way at check time. Callers must treat the result as
// approximate and keep enough headroom to absorb the error; that is what
// WarnFraction is for.
func EstimateTokens(data []byte) int {
	var alpha, digit, punct, nonASCII, lines int
	lines = 1
	for _, r := range string(data) {
		switch {
		case r == '\n':
			lines++
		case r > unicode.MaxASCII:
			nonASCII++
		case unicode.IsSpace(r):
			// Whitespace is charged through the tokens it merges into
			// (BPE binds a leading space to the following word), so it
			// carries no cost of its own here.
		case unicode.IsLetter(r):
			alpha++
		case unicode.IsDigit(r):
			digit++
		default:
			punct++
		}
	}
	est := float64(alpha)*tokensPerAlphaChar +
		float64(digit)*tokensPerDigitChar +
		float64(punct)*tokensPerPunctChar +
		float64(nonASCII)*tokensPerNonASCII +
		float64(lines)*tokensPerLinePrefix
	return int(math.Round(est))
}

// Line is one index line of a MEMORY.md, paired with its byte length and its
// estimated token cost. Tokens is what ranks it: a line can be modest in bytes
// and expensive in tokens (dense punctuation, slugs, non-ASCII), and it is the
// token cost that pushes the index toward the cliff.
type Line struct {
	Text   string
	Bytes  int
	Tokens int
}

// Result is the outcome of checking one MEMORY.md file.
type Result struct {
	Path string
	// SizeBytes is reported for human context only. It is NOT what the
	// threshold compares against — see EstTokens.
	SizeBytes       int
	EstTokens       int
	ThresholdTokens int
	CapTokens       int
	// Approaching is true when the file is at or past the warn threshold — i.e.
	// approaching the read cliff. It is the signal the doctor turns into a warn.
	Approaching bool
	// FattestLines holds the token-heaviest index lines (heaviest first),
	// populated only when Approaching. These are the actionable target: hooks
	// that grew into paragraphs are what push the index toward the cliff.
	FattestLines []Line
}

// CheckFile reads path (read-only) and evaluates it against the warn threshold.
// It never modifies the file.
func CheckFile(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	return Check(path, data), nil
}

// Check evaluates already-read file contents. It is pure — no I/O — so a fixture
// can be checked without touching the filesystem. numFattest controls how many
// of the longest lines are reported on firing.
func Check(path string, data []byte) Result {
	r := Result{
		Path:            path,
		SizeBytes:       len(data),
		EstTokens:       EstimateTokens(data),
		ThresholdTokens: WarnThresholdTokens(),
		CapTokens:       HarnessReadCapTokens,
	}
	if r.EstTokens >= r.ThresholdTokens {
		r.Approaching = true
		r.FattestLines = fattestLines(data, 3)
	}
	return r
}

// fattestLines returns the n token-heaviest non-blank lines, heaviest first.
// Ties keep source order (stable sort) for deterministic output.
func fattestLines(data []byte, n int) []Line {
	var lines []Line
	for _, raw := range strings.Split(string(data), "\n") {
		t := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(t) == "" {
			continue
		}
		lines = append(lines, Line{Text: t, Bytes: len(t), Tokens: EstimateTokens([]byte(t))})
	}
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Tokens > lines[j].Tokens
	})
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// PogoAgentMemoryGlob is pogo's OWN agent-memory index glob, relative to home:
// ~/.pogo/agents/<type>/<name>/memory/MEMORY.md. It lives here, rather than in a
// provider, because pogo writes it for every agent whatever harness that agent
// runs — it is harness-independent by construction, not a Claude artifact.
const PogoAgentMemoryGlob = ".pogo/agents/*/*/memory/MEMORY.md"

// Locate returns the auto-memory MEMORY.md index paths to check under home.
//
// harnessGlobs are home-relative globs supplied by the CALLER — one per harness
// that ships an auto-memory index (see agent.Provider.MemoryIndexGlobs and
// providers.MemoryIndexGlobs). They are a parameter, not a literal, so this
// package names no harness's dotdir. That is the whole point: the read cliff
// this package detects is a property of any harness, and a hard-coded
// ~/.claude here made a neutral-sounding check silently Claude-only — on a
// codex/pi/cursor install it globbed a path that could never exist while no
// equivalent covered the harness actually in use.
//
// pogo's own agent-memory root is always included; it is harness-independent.
// Missing roots simply contribute nothing; a glob error on one root does not
// stop the others. The result is sorted and de-duplicated for deterministic
// output.
func Locate(home string, harnessGlobs []string) []string {
	patterns := []string{filepath.Join(home, filepath.FromSlash(PogoAgentMemoryGlob))}
	for _, g := range harnessGlobs {
		if g == "" {
			continue
		}
		patterns = append(patterns, filepath.Join(home, filepath.FromSlash(g)))
	}
	var found []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			continue
		}
		found = append(found, matches...)
	}
	sort.Strings(found)
	// De-duplicate: two providers may declare overlapping roots, and a path
	// checked twice would be warned about twice.
	var uniq []string
	for i, p := range found {
		if i == 0 || p != found[i-1] {
			uniq = append(uniq, p)
		}
	}
	return uniq
}
