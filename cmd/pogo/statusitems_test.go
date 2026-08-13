package main

// Tests for the bounds `pogo status` puts on its work-item section (mg-ce23).
//
// The unit tests below fix the two rules — a line is cut to a visible width, a
// group is cut to a count — and the process-level tests at the bottom measure
// the whole frame, because the byte count is the finding: 348 items produced a
// 102KB dashboard that every agent invoking the command paid for in tokens.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// bigListing builds a `mg list` block with n items per group, each carrying a
// title of titleRunes runes and mg's real trailing styling, so both bounds and
// the ANSI strip have something to bite on.
func bigListing(groups []string, n, titleRunes int) string {
	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "%s:\n", g)
		for i := 0; i < n; i++ {
			title := strings.Repeat("é", titleRunes)
			fmt.Fprintf(&b, "  mg-%s%02d    task     %s \x1b[2m[pogo, %s]\x1b[0m \x1b[34mhuman\x1b[0m\n",
				g[:2], i, title, g)
		}
	}
	return b.String()
}

func TestTruncateVisible_CutsToWidthAndMarksTheCut(t *testing.T) {
	got := truncateVisible(strings.Repeat("a", 200), 100)
	if want := strings.Repeat("a", 100) + "…"; got != want {
		t.Errorf("truncateVisible(200 a's, 100) = %q", got)
	}
}

// A line already within the bound is returned byte-identical: no ellipsis, no
// re-encoding, nothing.
func TestTruncateVisible_LeavesShortLinesAlone(t *testing.T) {
	line := "  mg-2a50    task     RED LINE BREACHED \x1b[2m[pogo]\x1b[0m"
	if got := truncateVisible(line, 100); got != line {
		t.Errorf("a line under the bound was rewritten:\n got %q\nwant %q", got, line)
	}
}

// Width is counted in runes. mg's titles are full of em-dashes and accented
// text; a byte-counting cut lands mid-rune and prints a replacement character,
// which is a defect of exactly the same family as the one being fixed — output
// that is wrong because nobody measured what it was measuring.
func TestTruncateVisible_CountsRunesNotBytes(t *testing.T) {
	line := strings.Repeat("—", 50) // 3 bytes each, 150 bytes
	got := truncateVisible(line, 10)
	if want := strings.Repeat("—", 10) + "…"; got != want {
		t.Errorf("rune-counting cut = %q, want %q", got, want)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("cut produced a replacement character: %q", got)
	}
}

// Escapes do not consume width — a styled line must not be cut shorter than an
// unstyled one carrying the same text.
func TestTruncateVisible_EscapesCostNoWidth(t *testing.T) {
	styled := "\x1b[2m" + strings.Repeat("a", 20) + "\x1b[0m"
	got := truncateVisible(styled, 20)
	if got != styled {
		t.Errorf("escapes were counted as width: %q", got)
	}
}

// A cut inside a styled run closes the style. Leaving it open bleeds mg's dim
// attribute over everything the terminal prints afterwards.
func TestTruncateVisible_ClosesAnOpenStyleAtTheCut(t *testing.T) {
	styled := "ab\x1b[2m" + strings.Repeat("c", 50) + "\x1b[0m tail"
	got := truncateVisible(styled, 10)
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("cut inside a styled run left the style open: %q", got)
	}
	if strings.Count(got, "c") != 8 {
		t.Errorf("wrong visible width: %q", got)
	}
}

// The converse: a cut made after the style was already reset must not append a
// second reset it does not need.
func TestTruncateVisible_NoResetWhenNoStyleIsOpen(t *testing.T) {
	line := "\x1b[2mab\x1b[0m" + strings.Repeat("c", 50)
	got := truncateVisible(line, 10)
	if strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("appended a reset with no style open: %q", got)
	}
}

func TestRenderWorkItems_CountsEveryItemAndCapsEachGroup(t *testing.T) {
	block := bigListing([]string{"available", "claimed"}, 25, 300)
	lines := renderWorkItems(block, statusItemOptions{})
	out := strings.Join(lines, "\n")

	if lines[0] != "50 items: 25 available, 25 claimed" {
		t.Errorf("counts line = %q", lines[0])
	}
	// The counts are over EVERY item, not over the ones printed: an elision
	// that reported the size of what survived it would be useless.
	for _, want := range []string{"… 15 more (pogo status --full)"} {
		if strings.Count(out, want) != 2 {
			t.Errorf("expected one %q per group, got %d:\n%s", want, strings.Count(out, want), out)
		}
	}
	for _, g := range []string{"av", "cl"} {
		for i := 0; i < statusGroupCap; i++ {
			if !strings.Contains(out, fmt.Sprintf("mg-%s%02d", g, i)) {
				t.Errorf("item %s%02d inside the cap was not printed:\n%s", g, i, out)
			}
		}
		if strings.Contains(out, fmt.Sprintf("mg-%s%02d", g, statusGroupCap)) {
			t.Errorf("item %s%02d past the cap was printed:\n%s", g, statusGroupCap, out)
		}
	}
	// EVERY emitted line, not only the item lines: the bound covers the ones
	// this renderer adds itself.
	for _, l := range lines {
		if n := len([]rune(stripANSIText(l))); n > statusItemWidth+1 {
			t.Errorf("line of %d visible columns exceeds the %d bound: %q", n, statusItemWidth, l)
		}
	}
}

// A group header long enough to break the bound is cut like anything else.
// mg's headers are short today, which is exactly why this would go unnoticed.
func TestRenderWorkItems_BoundsItsOwnLinesToo(t *testing.T) {
	long := strings.Repeat("x", 300)
	block := long + ":\n  mg-0001    task     short\n"
	for _, l := range renderWorkItems(block, statusItemOptions{}) {
		if n := len([]rune(l)); n > statusItemWidth+1 {
			t.Errorf("renderer emitted its own %d-column line: %q", n, l[:60])
		}
	}
}

// --full turns both bounds off. Without this the full title is reachable only
// through another command, which is a worse dashboard, not a bounded one.
func TestRenderWorkItems_FullDisablesBothBounds(t *testing.T) {
	block := bigListing([]string{"available"}, 25, 300)
	out := strings.Join(renderWorkItems(block, statusItemOptions{Full: true}), "\n")

	if strings.Contains(out, "more (pogo status") {
		t.Errorf("--full still elided items:\n%s", out[:200])
	}
	if !strings.Contains(out, "mg-av24") {
		t.Error("--full dropped the last item")
	}
	if !strings.Contains(out, strings.Repeat("é", 300)) {
		t.Error("--full truncated a title")
	}
	if !strings.HasPrefix(out, "25 items: 25 available\n") {
		t.Errorf("--full dropped the counts line: %q", out[:40])
	}
}

// Styling survives to a terminal and is stripped for anything else. The strip
// is the half that was reported: piped output carried literal escapes into
// files, transcripts and agent context, styling nothing.
func TestRenderWorkItems_StylingFollowsTheDestination(t *testing.T) {
	block := "available:\n  mg-0001    task     short \x1b[2m[pogo]\x1b[0m \x1b[34mhuman\x1b[0m\n"

	styled := strings.Join(renderWorkItems(block, statusItemOptions{Styled: true}), "\n")
	if !strings.Contains(styled, "\x1b[34mhuman\x1b[0m") {
		t.Errorf("a terminal lost mg's styling: %q", styled)
	}

	plain := strings.Join(renderWorkItems(block, statusItemOptions{}), "\n")
	if strings.ContainsRune(plain, 0x1b) {
		t.Errorf("escapes leaked into non-terminal output: %q", plain)
	}
	if !strings.Contains(plain, "[pogo] human") {
		t.Errorf("stripping ate the text as well as the escapes: %q", plain)
	}
}

// mg's own notices are unindented, so they parse as a header with no items.
// They must pass through verbatim and must not acquire a "0 items" line.
func TestRenderWorkItems_PassesNoticesThrough(t *testing.T) {
	out := strings.Join(renderWorkItems("No work items.", statusItemOptions{}), "\n")
	if out != "No work items." {
		t.Errorf("notice was rewritten: %q", out)
	}
}

// --- process-level: the frame an operator and an agent actually get ---

// The headline measurement. A backlog the size of this fleet's produced a
// 102KB frame; the bound has to hold on the whole command, not just on the
// renderer, and the byte count is the thing the ticket is about.
func TestStatus_WorkItemSectionIsBounded(t *testing.T) {
	listing := bigListing([]string{"available", "claimed", "done"}, 120, 400)
	if len(listing) < 100_000 {
		t.Fatalf("fixture is too small to be a test of this: %d bytes", len(listing))
	}
	stdout, stderr, code := runPogoEnv(t, stubPogod(t), stubMGEnvWith(t, listing), "status")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, stderr)
	}
	if len(stdout) > 8_000 {
		t.Errorf("frame is %d bytes from a %d-byte listing — the section is not bounded",
			len(stdout), len(listing))
	}
	items := workItemsSection(t, stdout)
	if !strings.Contains(items, "360 items: 120 available, 120 claimed, 120 done") {
		t.Errorf("counts line missing or wrong:\n%s", items)
	}
	if !strings.Contains(items, "… 110 more (pogo status --full)") {
		t.Errorf("elision line missing:\n%s", items)
	}
	// Scoped to the section: the refinery rows legitimately print branch and
	// author strings and are bounded by the queue, not by the backlog.
	for _, line := range strings.Split(items, "\n") {
		if n := len([]rune(line)); n > statusItemWidth+4 {
			t.Errorf("work-item line of %d columns: %q", n, line)
		}
	}
}

// stdout here is a pipe, so this is the reported case exactly: escapes in
// output that is being captured rather than displayed.
func TestStatus_NoANSIWhenStdoutIsNotATerminal(t *testing.T) {
	stdout, _, code := runPogoEnv(t, stubPogod(t), stubMGEnv(t), "status")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if i := strings.IndexRune(stdout, 0x1b); i >= 0 {
		t.Errorf("escape sequence at byte %d of piped output: %q", i, stdout[max(0, i-40):min(len(stdout), i+20)])
	}
	// The guard is only worth anything if the input had escapes to leak.
	if !strings.ContainsRune(mgFixture, 0x1b) {
		t.Fatal("fixture carries no ANSI — this test cannot fail")
	}
}

// --full is the escape hatch, and it has to work through the real command:
// a bound whose documented way out does not work is a bound that lost data.
func TestStatus_FullPrintsEverything(t *testing.T) {
	listing := bigListing([]string{"available"}, 25, 300)
	stdout, _, code := runPogoEnv(t, stubPogod(t), stubMGEnvWith(t, listing), "status", "--full")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	items := workItemsSection(t, stdout)
	if !strings.Contains(items, "mg-av24") {
		t.Errorf("--full dropped the last item:\n%s", items)
	}
	if !strings.Contains(items, strings.Repeat("é", 300)) {
		t.Error("--full truncated a title")
	}
	if strings.ContainsRune(items, 0x1b) {
		t.Error("--full is not a reason to leak escapes into a pipe")
	}
}

// --json is the machine surface: the listing is whole (no cut, no cap) and
// carries no escapes, which are cost without meaning to a parser.
func TestStatus_JSONIsUnboundedAndUnstyled(t *testing.T) {
	listing := bigListing([]string{"available"}, 25, 300)
	stdout, _, code := runPogoEnv(t, stubPogod(t), stubMGEnvWith(t, listing), "--json", "status")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("not one object: %v", err)
	}
	items, _ := obj["work_items"].(string)
	if !strings.Contains(items, "mg-av24") {
		t.Error("--json capped the listing")
	}
	if !strings.Contains(items, strings.Repeat("é", 300)) {
		t.Error("--json truncated a title")
	}
	if strings.ContainsRune(items, 0x1b) {
		t.Errorf("--json carried mg's escapes: %q", items[:120])
	}
	if strings.Contains(items, "items: 25 available") {
		t.Error("--json grew the text renderer's counts line")
	}
}
