package promptcli

import (
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Template rendering (false-positive source #1)
// ---------------------------------------------------------------------------

// Placeholder is what an unresolvable template action or an angle-bracket
// stand-in becomes. A token containing it is never validated and never
// reported: the prompt is not claiming anything about it.
const Placeholder = "\x00PH\x00"

// templateValues renders the value actions that shipped prompts interpolate.
//
// Two of them appear INSIDE a command name — `pogo agent spawn-{{.Worker}}`
// resolves to `spawn-polecat`, a real subcommand — so stripping rather than
// rendering turns a correct instruction into a phantom finding. The rest are
// arguments, where the value is irrelevant and Placeholder is enough.
//
// An action not listed here becomes Placeholder, which suppresses rather than
// reports: a new template variable must not be able to manufacture a finding.
var templateValues = map[string]string{
	".Coordinator":      "mayor",
	".CoordinatorTitle": "mayor",
	".Worker":           "polecat",
	".WorkerTitle":      "polecat",
}

var (
	reTemplateAction = regexp.MustCompile(`\{\{-?\s*(.*?)\s*-?\}\}`)
	reTemplateCtl    = regexp.MustCompile(`^(if|else|end|range|with|block|define|template)\b`)
)

// Render resolves template actions to text that can be parsed as commands.
//
// Control actions (`{{if …}}`, `{{else}}`, `{{end}}`) are DELETED rather than
// evaluated, which leaves every branch's text in the stream. That is
// deliberate: both arms of `{{if eq .Provider "claude"}}` ship to some reader,
// so both arms are in scope for the check. Evaluating the template with one
// provider would leave the other arm permanently unchecked — the exact shape of
// gap this package exists to close.
//
// Line count is preserved: an action is replaced within its line, never across
// one, so reported line numbers still point into the file on disk.
func Render(text string) string {
	return reTemplateAction.ReplaceAllStringFunc(text, func(m string) string {
		sub := reTemplateAction.FindStringSubmatch(m)
		inner := sub[1]
		if reTemplateCtl.MatchString(inner) {
			return ""
		}
		if v, ok := templateValues[inner]; ok {
			return v
		}
		return Placeholder
	})
}

// ---------------------------------------------------------------------------
// Code contexts
// ---------------------------------------------------------------------------

// commandFenceLangs are the fenced-block info strings whose contents are read
// as commands. A block is skipped unless its language says "this is a shell".
//
// This is the whole of false-positive source #4 and most of #2. pm-template.md
// carries a ```markdown block containing the prose line "Filing as mg if
// approved.", which a language-blind reader takes for an `mg if` invocation.
// Scoping to shell fences drops it without a heuristic.
var commandFenceLangs = map[string]bool{
	"": true, "bash": true, "sh": true, "shell": true, "zsh": true, "console": true,
}

// codeSpan is one stretch of text read as a command line, with the 1-based line
// number it starts on.
type codeSpan struct {
	Line int
	Text string
}

var (
	reFence    = regexp.MustCompile("^(\\s*)```+\\s*([A-Za-z0-9_+-]*)\\s*$")
	reHeredoc  = regexp.MustCompile(`<<-?\s*'?"?([A-Za-z_][A-Za-z0-9_]*)'?"?`)
	reInline   = regexp.MustCompile("`([^`\n]+)`")
	reContLine = regexp.MustCompile(`\\\s*$`)
)

// codeSpans yields every command context in a rendered prompt: the lines of
// shell fenced blocks (continuations joined, heredoc bodies dropped) and every
// inline code span outside a fence.
func codeSpans(text string) []codeSpan {
	var out []codeSpan
	lines := strings.Split(text, "\n")

	inFence, fenceIsShell := false, false
	heredocEnd := ""
	pending, pendingLine := "", 0

	flush := func() {
		if pending != "" {
			out = append(out, codeSpan{Line: pendingLine, Text: pending})
			pending, pendingLine = "", 0
		}
	}

	for i, raw := range lines {
		lineNo := i + 1
		if m := reFence.FindStringSubmatch(raw); m != nil {
			flush()
			if inFence {
				inFence, fenceIsShell, heredocEnd = false, false, ""
			} else {
				inFence = true
				fenceIsShell = commandFenceLangs[strings.ToLower(m[2])]
			}
			continue
		}

		if inFence {
			if !fenceIsShell {
				continue
			}
			t := strings.TrimSpace(raw)
			// A heredoc body is data the command is fed, not a command. The
			// polecat templates send whole prose bodies this way.
			if heredocEnd != "" {
				if t == heredocEnd {
					heredocEnd = ""
				}
				continue
			}
			if t == "" || strings.HasPrefix(t, "#") {
				flush()
				continue
			}
			if pending == "" {
				pendingLine = lineNo
			}
			pending += " " + t
			if reContLine.MatchString(t) {
				pending = reContLine.ReplaceAllString(pending, " ")
				continue
			}
			if m := reHeredoc.FindStringSubmatch(t); m != nil {
				heredocEnd = m[1]
			}
			flush()
			continue
		}

		for _, m := range reInline.FindAllStringSubmatch(raw, -1) {
			out = append(out, codeSpan{Line: lineNo, Text: m[1]})
		}
	}
	flush()
	return out
}

// ---------------------------------------------------------------------------
// Invocations (false-positive source #2: placeholders)
// ---------------------------------------------------------------------------

// Invocation is one `mg …` / `pogo …` command line found in a code context,
// resolved as far as the surface allows.
type Invocation struct {
	Line int
	Path []string // resolved command path, e.g. {"mg","spend"}
	// Unresolved is the first bare word that was not a subcommand of Path, when
	// Path names a command group. Empty otherwise.
	Unresolved string
	Flags      []string // long flag names, without "--", in order of appearance
	Raw        string
	// Skipped is set when a placeholder stood where a command name belongs, so
	// neither the path nor its flags can be judged.
	Skipped bool
}

var (
	// A command starts at the beginning of a segment, not mid-identifier:
	// `pogo-cat-<name>` and `.../bin/pogo` are not invocations.
	reCmdStart  = regexp.MustCompile(`(?:^|[\s;&|(])((?:mg|pogo))\s+(\S.*)$`)
	reBareWord  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reFlagToken = regexp.MustCompile(`^--([a-zA-Z][a-zA-Z0-9-]*)`)
)

// Roots are the CLIs this package checks. `git`, `gh`, `jq` and `bash` appear
// throughout the corpus and are deliberately out of scope: they are not this
// project's tools, their surfaces are not ours to pin, and a wrong `gh` flag is
// not the failure mg-21b1 is about.
var Roots = []string{"mg", "pogo"}

func isPlaceholder(tok string) bool {
	return strings.Contains(tok, Placeholder) ||
		strings.HasPrefix(tok, "<") || strings.Contains(tok, "<") && strings.Contains(tok, ">") ||
		strings.HasPrefix(tok, "$")
}

// extractInvocations parses every command context into resolved invocations.
func extractInvocations(spans []codeSpan, surfaces map[string]*Surface) []Invocation {
	var out []Invocation
	for _, sp := range spans {
		// A single span can hold several commands (`a && b`, `x | y`).
		for _, seg := range splitSegments(sp.Text) {
			m := reCmdStart.FindStringSubmatch(seg)
			if m == nil {
				continue
			}
			root := m[1]
			surf, ok := surfaces[root]
			if !ok {
				continue
			}
			inv := parseOne(sp.Line, root, m[2], surf)
			out = append(out, inv)
		}
	}
	return out
}

var reSegSplit = regexp.MustCompile(`\s(?:\|\||&&|\||;)\s`)

func splitSegments(s string) []string {
	parts := reSegSplit.Split(s, -1)
	// Also treat the whole string as a candidate: a leading `mg` with no
	// separator is the common case and Split already returns it as parts[0].
	return parts
}

func parseOne(line int, root, rest string, surf *Surface) Invocation {
	inv := Invocation{Line: line, Path: []string{root}, Raw: strings.TrimSpace(root + " " + rest)}
	toks := strings.Fields(rest)

	i := 0
	for ; i < len(toks); i++ {
		tok := toks[i]
		if strings.HasPrefix(tok, "-") {
			break
		}
		if isPlaceholder(tok) {
			// A placeholder where a command name could go: if the path so far
			// is a group, we cannot tell which subcommand was meant, so nothing
			// downstream is judgeable.
			if n, ok := surf.Lookup(inv.Path); ok && len(n.Subs) > 0 {
				inv.Skipped = true
				return inv
			}
			continue
		}
		if !reBareWord.MatchString(tok) {
			// A positional argument (an mg id, a path, a quoted string).
			continue
		}
		next := append(append([]string{}, inv.Path...), tok)
		if _, ok := surf.Lookup(next); ok {
			inv.Path = next
			continue
		}
		// Not a subcommand. Only a command GROUP that takes NO positional
		// arguments can be wrong about this: `mg show mg-21b1` and
		// `pogo schedule mayor --cron …` are both correct, and both look like a
		// misspelled subcommand from here.
		if n, ok := surf.Lookup(inv.Path); ok && len(n.Subs) > 0 && !n.TakesArgs {
			inv.Unresolved = tok
			return inv
		}
		break
	}

	for ; i < len(toks); i++ {
		tok := toks[i]
		fm := reFlagToken.FindStringSubmatch(tok)
		if fm == nil {
			continue
		}
		if strings.Contains(fm[1], Placeholder) {
			continue
		}
		inv.Flags = append(inv.Flags, fm[1])
	}
	return inv
}

// ---------------------------------------------------------------------------
// Absence claims (the mg-4bb9 class) and omission instructions (mg-159a)
// ---------------------------------------------------------------------------

// AbsenceClaim is a prompt sentence asserting that some part of the CLI does
// not exist. mg-4bb9 is one of these and it was false; mayor.md's "There is no
// `pogo server restart`" is one of these and it is true. Only the tool can say
// which, which is why the claim is extracted rather than trusted.
type AbsenceClaim struct {
	Line int
	// Scope is the command the denial is about, resolved from the nearest
	// command mention in the same sentence or paragraph.
	Scope []string
	// Literal is a flag or command the sentence named exactly, e.g. "--note"
	// or "pogo server restart". Checked for EXACT existence.
	Literal string
	// Stems are the head words of a denied noun phrase ("append/comment
	// subcommand" -> append, comment). Checked as substrings, and only when
	// Literal is empty. See Node.NamesContaining for why the two differ.
	Stems     []string
	Retracted bool
	Sentence  string
}

// OmissionClaim is a prompt instruction to leave a flag off. Whether that is
// true is not visible in `--help` — cobra does not print conditional
// requirement — so it is answered by an OmissionRule supplied by the caller.
type OmissionClaim struct {
	Line  int
	Scope []string
	Flag  string
	// Types are the scope values the instruction applies to, taken from the
	// same table row or sentence.
	Types []string
	// CatchAll is set when the instruction is scoped to "anything else" /
	// "everything else" / "by default" rather than to named values.
	CatchAll  bool
	Retracted bool
	Sentence  string
}

// retractionMarkers suppress a claim that is being QUOTED IN ORDER TO WITHDRAW
// IT (false-positive source #3).
//
// This matters more than it looks. mg-4bb9's own fix necessarily contains the
// false claim as a quotation — "It said `mg edit` had \"no append/comment
// subcommand\"" — and an absence-predicate with no retraction handling scores
// that careful correction WORSE than the sloppy original. A control that
// punishes explaining your fix teaches people to delete the explanation.
//
// Two ways to be suppressed, and prompt authors should prefer the first:
//
//  1. the explicit marker `<!-- promptcli:retracted -->` anywhere in the
//     paragraph, which is unambiguous and does not depend on phrasing; or
//  2. one of the past-tense/withdrawal phrasings below, which is what the
//     already-shipped corrections happen to use.
var retractionMarkers = []string{
	"<!-- promptcli:retracted -->",
	"asserted the opposite",
	"used to say",
	"used to claim",
	"said the opposite",
	"was wrong until",
	"shipped wrong",
	"this bullet asserted",
	"it said",
	"until mg-",
	"stops being",
	"no longer true",
	"the old claim",
	"pre-fix",
	"corrected by mg-",
}

var (
	reSentenceSplit = regexp.MustCompile(`(?:\. |\.\n|; |\? |! |\n)`)
	reCmdMention    = regexp.MustCompile("`((?:mg|pogo)\\s+[a-z][a-z0-9 -]*)`")
	reBareMention   = regexp.MustCompile(`\b(mg|pogo)\s+([a-z][a-z0-9-]*)`)

	// existenceDenial is the phrasing that asserts a thing IS NOT THERE.
	existenceDenial = `(?:there is no|there's no|has no|have no|is no|does not have (?:a|an)?|` +
		`doesn't have (?:a|an)?|does not take (?:a|an)?|doesn't take (?:a|an)?)`

	// denialVerbs adds a bare "no", which is only safe where a kind noun has to
	// follow it. On its own it reads a PROHIBITION as a denial:
	// polecat-triage.md's "No commits, no branch pushes, no `gh pr create`, no
	// `pogo refinery submit`" is a list of things not to do, and every one of
	// them exists.
	denialVerbs = `(?:` + existenceDenial + `|no)`

	// "there is no append/comment subcommand" — a denied ENGLISH NOUN, matched
	// by stem, because a noun is all the sentence gives you.
	reNoSubcmd = regexp.MustCompile(`(?i)\b` + denialVerbs + `\s+` +
		"(?:`([^`]+)`|([A-Za-z][A-Za-z/_-]*(?:\\s+[A-Za-z][A-Za-z/_-]*)?))" +
		`\s*(subcommand|sub-command|flag|option|command|argument)\b`)

	// "There is no `pogo server restart`;" — a denied LITERAL with no kind
	// noun after it. Both of the corpus's true absence claims are written this
	// way, and without this pattern the thing they warn you off is re-reported
	// as an instruction to run it.
	reNoLiteral = regexp.MustCompile(`(?i)\b` + existenceDenial + "\\s+`([^`]+)`")

	// "`mg show <id> --body` does not exist".
	reNotExist = regexp.MustCompile("(?i)`([^`]+)`\\s+(?:does not|doesn't|do not|don't)\\s+exist")

	reOmit = regexp.MustCompile("(?i)\\bomit(?:ting)?\\s+(?:the\\s+)?`--([a-zA-Z][a-zA-Z0-9-]*)`")

	reCatchAll = regexp.MustCompile(`(?i)\b(anything else|everything else|any other|all other|otherwise|by default|the default)\b`)
	reBackTick = regexp.MustCompile("`([^`]+)`")
)

func hasRetraction(s string) bool {
	l := strings.ToLower(s)
	for _, m := range retractionMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// paragraphs splits rendered text into paragraphs with their starting line.
func paragraphs(text string) []codeSpan {
	var out []codeSpan
	lines := strings.Split(text, "\n")
	cur, start := []string{}, 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, codeSpan{Line: start, Text: strings.Join(cur, "\n")})
			cur = nil
		}
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		if len(cur) == 0 {
			start = i + 1
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

// resolveScope finds the command a prose claim is about: the last command
// mention at or before the claim, preferring a backticked one.
func resolveScope(before string, surfaces map[string]*Surface) []string {
	best := []string(nil)
	consider := func(words []string) {
		if len(words) == 0 {
			return
		}
		surf, ok := surfaces[words[0]]
		if !ok {
			return
		}
		path := []string{words[0]}
		for _, w := range words[1:] {
			next := append(append([]string{}, path...), w)
			if _, ok := surf.Lookup(next); !ok {
				break
			}
			path = next
		}
		best = path
	}
	for _, m := range reCmdMention.FindAllStringSubmatch(before, -1) {
		consider(strings.Fields(m[1]))
	}
	if best == nil {
		for _, m := range reBareMention.FindAllStringSubmatch(before, -1) {
			consider([]string{m[1], m[2]})
		}
	}
	return best
}

var kindNouns = map[string]bool{
	"subcommand": true, "sub-command": true, "flag": true,
	"option": true, "command": true, "argument": true,
}

// extractProseClaims pulls absence and omission claims out of the prose.
func extractProseClaims(text string, surfaces map[string]*Surface) ([]AbsenceClaim, []OmissionClaim) {
	var abs []AbsenceClaim
	var omits []OmissionClaim

	for _, para := range paragraphs(text) {
		retracted := hasRetraction(para.Text)
		offset := 0
		for _, sent := range reSentenceSplit.Split(para.Text, -1) {
			idx := strings.Index(para.Text[offset:], sent)
			if idx < 0 {
				idx = 0
			}
			absStart := offset + idx
			offset = absStart + len(sent)
			line := para.Line + strings.Count(para.Text[:absStart], "\n")
			ctx := para.Text[:offset]

			for _, m := range reNoSubcmd.FindAllStringSubmatch(sent, -1) {
				claim := AbsenceClaim{Line: line, Retracted: retracted, Sentence: strings.TrimSpace(sent)}
				lit, bare := m[1], m[2]
				if lit != "" {
					claim.Literal = strings.TrimSpace(lit)
				} else {
					for _, w := range strings.FieldsFunc(bare, func(r rune) bool { return r == '/' || r == ' ' }) {
						w = strings.ToLower(strings.Trim(w, "-_"))
						if w == "" || kindNouns[w] || isStopWord(w) {
							continue
						}
						claim.Stems = append(claim.Stems, w)
					}
					if len(claim.Stems) == 0 {
						continue
					}
				}
				claim.Scope = resolveScope(ctx, surfaces)
				abs = append(abs, claim)
			}
			for _, m := range reNoLiteral.FindAllStringSubmatch(sent, -1) {
				claim := AbsenceClaim{
					Line: line, Retracted: retracted,
					Literal:  strings.TrimSpace(m[1]),
					Sentence: strings.TrimSpace(sent),
				}
				claim.Scope = resolveScope(ctx, surfaces)
				abs = append(abs, claim)
			}
			for _, m := range reNotExist.FindAllStringSubmatch(sent, -1) {
				claim := AbsenceClaim{
					Line: line, Retracted: retracted,
					Literal:  strings.TrimSpace(m[1]),
					Sentence: strings.TrimSpace(sent),
				}
				claim.Scope = resolveScope(ctx, surfaces)
				abs = append(abs, claim)
			}
			for _, m := range reOmit.FindAllStringSubmatch(sent, -1) {
				oc := OmissionClaim{
					Line: line, Flag: m[1], Retracted: retracted,
					CatchAll: reCatchAll.MatchString(sent),
					Sentence: strings.TrimSpace(sent),
				}
				oc.Scope = resolveScope(ctx, surfaces)
				for _, b := range reBackTick.FindAllStringSubmatch(sent, -1) {
					v := strings.TrimSpace(b[1])
					if reBareWord.MatchString(v) {
						oc.Types = append(oc.Types, v)
					}
				}
				sort.Strings(oc.Types)
				omits = append(omits, oc)
			}
		}
	}
	return abs, omits
}

// isStopWord drops English filler that would otherwise become a stem and match
// half the flag list ("no SUCH flag", "no OTHER command").
func isStopWord(w string) bool {
	switch w {
	case "a", "an", "the", "such", "other", "separate", "single", "real",
		"dedicated", "explicit", "extra", "second", "own", "its", "this", "that",
		"any", "each", "more", "less", "one", "two", "way", "and", "or":
		return true
	}
	return false
}
