package main

// Bounding the work-item section of `pogo status` (mg-ce23).
//
// The dashboard's work-item section is the rendered text of `mg list`, printed
// one line per item with no bound of any kind. That is 96% of the frame: with
// 348 items and titles routinely 200-400 characters — this fleet writes titles
// that carry the finding — a single `pogo status` was 102KB, ~25,000 tokens,
// and every agent that ran it paid that.
//
// Two bounds are applied here, and both belong to `status` rather than to mg:
//
//   - Each rendered line is cut to statusItemWidth visible columns. A summary
//     whose line width is whatever the longest title happens to be is not a
//     summary; `mg show <id>` is the tool for a full title.
//   - Each status group prints at most statusGroupCap items, followed by a line
//     naming how many were elided. The section otherwise grows linearly with
//     the backlog forever — it was 21KB in early August and 102KB a week later,
//     with nothing in between having changed but the number of items.
//
// `--full` turns both bounds off, so nothing is reachable only through another
// tool.
//
// The counts line is what makes the elision honest: a reader who sees ten
// items under `available:` learns from it that there are 285, which the
// unbounded listing never said either — it had to be counted by hand.
//
// The same rule is applied to this file's own output. The counts line is
// bounded by the number of status groups (four), the elision line is one line
// per group, and neither carries a title. A remedy for unbounded output that
// itself grew with the backlog would be the defect wearing the fix's clothes.

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/drellem2/pogo/internal/agent"
)

// stdoutIsTerminal reports whether the dashboard is being drawn for a human at
// a terminal. It is a variable so a test can pin either answer without needing
// a pty: the styled branch is otherwise unreachable from a test whose child
// process writes to a pipe.
var stdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// stripANSIText removes ANSI escapes from a rendered text block.
func stripANSIText(s string) string { return string(agent.StripANSI([]byte(s))) }

const (
	// statusItemWidth is the maximum number of visible columns one work-item
	// line may occupy in the text dashboard. 100 fits an id, a type, an
	// assignee-length tail and ~70 characters of title — enough to recognize
	// an item you already know, which is what a dashboard is for.
	statusItemWidth = 100

	// statusGroupCap is how many item lines each status group prints before
	// the rest are elided into a count.
	statusGroupCap = 10

	// statusElisionHint names the escape hatch on every elision line, so the
	// full listing is one flag away from wherever the reader noticed it was
	// missing.
	statusElisionHint = "pogo status --full"
)

// workItemGroup is one status group of a rendered `mg list` block: the header
// line ("available:") and the item lines under it.
//
// Item lines are indented by mg and headers are not, which is the whole
// distinction — the same one filterWorkItemsByAssignee relies on. Anything
// unindented starts a new group, so mg's own notices ("No work items.") become
// a header with no items and are printed verbatim rather than parsed.
type workItemGroup struct {
	Header string
	Items  []string
}

// parseWorkItemGroups splits a rendered `mg list` block into its status groups
// in the order mg emitted them. Blank lines are dropped; items appearing
// before any header land in a leading group with an empty Header, so a sliced
// block is neither dropped nor miscounted.
func parseWorkItemGroups(block string) []workItemGroup {
	var groups []workItemGroup
	for _, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			groups = append(groups, workItemGroup{Header: line})
			continue
		}
		if len(groups) == 0 {
			groups = append(groups, workItemGroup{})
		}
		groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, line)
	}
	return groups
}

// workItemCounts renders the counts line: the total, then one count per group
// that has items, named by its header with the trailing colon removed. It
// returns "" when no group has any items, so a block that is only a notice
// ("No work items.") does not grow a "0 items" line above it.
func workItemCounts(groups []workItemGroup) string {
	total := 0
	var parts []string
	for _, g := range groups {
		if len(g.Items) == 0 {
			continue
		}
		total += len(g.Items)
		name := strings.TrimSuffix(strings.TrimSpace(g.Header), ":")
		if name == "" {
			name = "ungrouped"
		}
		parts = append(parts, fmt.Sprintf("%d %s", len(g.Items), name))
	}
	if total == 0 {
		return ""
	}
	noun := "items"
	if total == 1 {
		noun = "item"
	}
	return fmt.Sprintf("%d %s: %s", total, noun, strings.Join(parts, ", "))
}

// statusItemOptions is how the caller states the two things this renderer
// cannot observe for itself: whether the reader asked for the unbounded
// listing, and whether the destination can render mg's styling.
type statusItemOptions struct {
	// Full disables both bounds — every item, every title in full.
	Full bool
	// Styled keeps mg's ANSI escapes. False strips them, which is the case
	// whenever stdout is not a terminal: piped and captured output is read by
	// a machine or an agent, and there the escapes are bytes that cost tokens
	// and confuse parsing while styling nothing.
	Styled bool
}

// renderWorkItems returns the lines of the work-item section for a rendered
// `mg list` block, WITHOUT the section's own two-space indent — the caller
// adds that, as it always has.
func renderWorkItems(block string, opts statusItemOptions) []string {
	if !opts.Styled {
		block = string(agent.StripANSI([]byte(block)))
	}
	groups := parseWorkItemGroups(block)

	// bound is applied to EVERY line this function emits, including the ones
	// it adds itself. A header and a counts line are both built from mg's
	// text, so a renderer that bounded only the item lines would be a
	// width-bounded section with two unbounded lines in it — this ticket's
	// defect in a smaller font.
	bound := func(line string) string {
		if opts.Full {
			return line
		}
		return truncateVisible(line, statusItemWidth)
	}

	var out []string
	if counts := workItemCounts(groups); counts != "" {
		out = append(out, bound(counts))
	}
	for _, g := range groups {
		if g.Header != "" {
			out = append(out, bound(g.Header))
		}
		shown := g.Items
		if !opts.Full && len(shown) > statusGroupCap {
			shown = shown[:statusGroupCap]
		}
		for _, item := range shown {
			out = append(out, bound(item))
		}
		if elided := len(g.Items) - len(shown); elided > 0 {
			out = append(out, fmt.Sprintf("  … %d more (%s)", elided, statusElisionHint))
		}
	}
	return out
}

// truncateVisible cuts a line to at most max visible columns, appending "…"
// when it cuts.
//
// ANSI escapes are copied through without counting toward the width, and a
// style still open at the cut is closed with a reset — a truncation that left
// a style open would bleed mg's dim attribute over the rest of the terminal.
// Counting is per rune, not per byte: mg's titles are full of em-dashes and
// cutting one in half prints a replacement character.
func truncateVisible(line string, max int) string {
	if max <= 0 {
		return line
	}
	var b strings.Builder
	visible := 0
	styleOpen := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			esc, next := scanEscape(line, i)
			b.WriteString(esc)
			if strings.HasSuffix(esc, "m") {
				styleOpen = !isResetSGR(esc)
			}
			i = next
			continue
		}
		if visible >= max {
			b.WriteString("…")
			if styleOpen {
				b.WriteString("\x1b[0m")
			}
			return b.String()
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		b.WriteString(line[i : i+size])
		visible++
		i += size
	}
	// Nothing was cut, so return the input rather than the rebuilt copy.
	return line
}

// scanEscape returns the escape sequence starting at i and the index just past
// it. A lone ESC at the end of the string returns itself, so the scan always
// advances and cannot loop.
func scanEscape(s string, i int) (string, int) {
	j := i + 1
	if j < len(s) && s[j] == '[' {
		j++
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
			j++
		}
		if j < len(s) {
			j++ // final byte
		}
		return s[i:j], j
	}
	if j < len(s) {
		j++
	}
	return s[i:j], j
}

// isResetSGR reports whether an SGR sequence turns styling off — ESC[m,
// ESC[0m, and ESC[0;0m all do.
func isResetSGR(esc string) bool {
	params := strings.TrimSuffix(strings.TrimPrefix(esc, "\x1b["), "m")
	if params == "" {
		return true
	}
	for _, p := range strings.Split(params, ";") {
		if strings.TrimLeft(p, "0") != "" {
			return false
		}
	}
	return true
}
