// Package workitem reads macguffin work items from the filesystem.
package workitem

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkItem represents a macguffin work item with its core fields.
type WorkItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
	Type     string `json:"type,omitempty"`
	Priority string `json:"priority,omitempty"`
	Tags     string `json:"tags,omitempty"`
	// Repo is the `repo:` frontmatter value — the repository the item's work
	// belongs to. Read by the dispatch-pairing gate, which routes on the repo an
	// item names rather than on anything a filer has to remember to set.
	Repo string `json:"repo,omitempty"`
	// Depends is the raw `depends:` frontmatter value, kept verbatim exactly as
	// Tags is. mg writes it as an inline YAML sequence — `depends: [mg-1234]`.
	// Use DependsList for the split form.
	Depends string `json:"depends,omitempty"`
	// Workflow and Stage are the `workflow:` and `stage:` lines of the STATE
	// CARRIER BLOCK — the run of `key: value` lines that leads a work item's
	// BODY, immediately under its `# title` heading. They are not frontmatter:
	// mg has no such fields, and the convention is the coordinator's (see the
	// "State carrier" section of internal/agent/prompts/mayor.md).
	//
	// They are parsed here anyway, because `stage: gated` is a DISPATCH GATE
	// (config.IsStageGated) and a gate has to be readable by the code that
	// enforces it. Until mg-69b1 the stage line was read only by the coordinator
	// that wrote it, so an item the workflow considered gated was fully
	// dispatchable — observed live on three gh-issue carriers, which the priority
	// wake then offered up as "ready and unclaimed".
	//
	// Empty is the ordinary case: most items carry no carrier block at all, and
	// an absent line reads as "this item declares no stage", never as a stage.
	Workflow string `json:"workflow,omitempty"`
	Stage    string `json:"stage,omitempty"`
	// CarrierUnreadable is the parse's THIRD OUTCOME, and it exists because the
	// first two were not enough (mg-27d4). Until it did, scanCarrierBlock had
	// exactly two answers — a carrier, or no carrier — and it resolved "there is
	// carrier-shaped content in this body that I could not reach" as NO CARRIER.
	// That is the fail-open direction: an item whose gate declaration the parser
	// cannot see parses as declaring no gate, and therefore does not gate.
	//
	// One line of prose above the block is all it takes, because the block is
	// only visible when it leads the body. That is not a hypothetical shape:
	// measured over the live store on 2026-08-12, 14 of 29 build carriers and 4
	// of 27 review carriers were already in it — the common case on the build
	// side, not an edge case. `mg show` renders those blocks perfectly to a human
	// while the parser sees nothing, so the failure is silent in both directions.
	//
	// Set means: this body declares a `workflow:`/`stage:` line, unquoted and at
	// column 0, BELOW where the leading-block scan stopped. It carries no stage
	// value of its own on purpose — the point is that the value could not be
	// read, and a consumer must not act as though it had one.
	//
	// EVERY DISPATCH GATE MUST TREAT THIS AS GATED (see agent.MGDispatchGate and
	// stallwatch): an item whose gate cannot be read is precisely the item not to
	// dispatch. Reading it as ungated is how mg-69b1's failure — a re-dispatch
	// posting a second acknowledgement comment on a stranger's open GitHub issue
	// — stayed reachable by a filer putting one line too high.
	CarrierUnreadable bool `json:"carrier_unreadable,omitempty"`
	// ModTime is the work item file's last-modified time. It is the best
	// available proxy for how long an item has sat in its current status
	// directory (mg rewrites/moves the file on status transitions), which the
	// stall watcher uses to age unclaimed `available` items. Populated by
	// ListFrom; zero when the file could not be stat'd.
	ModTime time.Time `json:"mod_time,omitempty"`
	// CompletedAt is when the item was marked done, taken from the mtime of its
	// sibling `<id>.result.json`. Populated only for items in done/, and zero
	// when no result file exists.
	//
	// IT IS NOT ModTime, AND THE DIFFERENCE IS LOAD-BEARING. `mg done` RENAMES
	// the item file into done/, and a rename preserves mtime — so for a done
	// item that was never body-edited, ModTime equals its CREATION time, hours
	// or days before completion. Measured on the live store: every audit item
	// checked had ModTime == its `created:` frontmatter to the second, while the
	// sibling result.json was written at the moment of the merge. A consumer
	// that aged a done item by ModTime would be measuring how long ago it was
	// FILED and reporting it as how long ago it FINISHED.
	//
	// Zero is a real answer and must not be read as "just now" or "long ago": an
	// item completed without a result file has no recorded completion time at
	// all. Callers should count those separately rather than fold them into
	// either verdict.
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// TagList splits the raw `tags:` frontmatter value into individual tags. mg
// writes it as an inline YAML sequence — `tags: [pogo, ops, blocked-on-daniel]`
// — and parseWorkItem keeps it verbatim, so every consumer that wants one tag
// rather than the whole line would otherwise re-derive this split.
// parseFrontmatterLine already strips a balanced pair of brackets; stripping
// again here is harmless and means the method also works on a hand-built
// WorkItem. Surrounding quotes are stripped, empty entries dropped, and both
// `tags: []` and a missing tags line yield nil.
func (w WorkItem) TagList() []string {
	return splitInlineList(w.Tags)
}

// DependsList splits the raw `depends:` frontmatter value into individual work
// item ids, with the same normalization TagList applies to `tags:` — the two
// fields are written in the same inline-sequence shape by mg, so they are split
// by the same code rather than by two copies that can disagree. Both
// `depends: []` and a missing depends line yield nil.
func (w WorkItem) DependsList() []string {
	return splitInlineList(w.Depends)
}

// splitInlineList splits one raw inline YAML sequence value into its entries.
func splitInlineList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = raw[1 : len(raw)-1]
	}
	var out []string
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		t = strings.Trim(t, `"'`)
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// workspaceDir returns the macguffin workspace root.
func workspaceDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".macguffin", "work")
}

// statusDirs maps directory names to work item status values.
//
// byDefault says whether a scan with no explicit status filter reads the
// directory. It exists for pending/, which holds items mg has parked because a
// gate on them has not opened — most often an unmet `depends:`. Those are FILED
// items and a search of the store must be able to reach them, but they are not
// part of the working set, and every caller predating this field means
// available+claimed+done when it says "all". So pending/ is reachable only by
// naming it, and no existing caller's results move.
var statusDirs = []struct {
	dir       string
	status    string
	byDefault bool
}{
	{"available", "available", true},
	{"claimed", "claimed", true},
	{"done", "done", true},
	{"pending", "pending", false},
}

// List reads work items from the macguffin workspace, optionally filtered to
// the given statuses. With no arguments it scans available/, claimed/, and
// done/.
func List(statuses ...string) ([]WorkItem, error) {
	return ListFrom(workspaceDir(), statuses...)
}

// ListFrom reads work items from a given workspace root, optionally filtered
// to the given statuses ("available", "claimed", "done"). The filter applies
// at the directory level — non-matching status directories are never read or
// parsed — so callers that need only one status skip the cost of walking the
// others (done/ grows unbounded over a long run). No statuses means all.
// Exported so out-of-package consumers (e.g. the stall watcher) can point it
// at a test workspace or an alternate root rather than the default
// ~/.macguffin/work.
func ListFrom(root string, statuses ...string) ([]WorkItem, error) {
	return listFrom(root, false, statuses...)
}

// ListAllFrom is ListFrom plus the items ListFrom cannot see: a claim renames
// the file to `<id>.md.<pid>`, so ListFrom's ".md" suffix filter silently drops
// every item in claimed/. That is harmless for its two existing callers, which
// both ask only for "available" — but it is not harmless for a caller that is
// SEARCHING the store for an item, because "claimed" would come back as "absent"
// and the caller would conclude the item does not exist.
//
// The dispatch-pairing gate is exactly such a caller: it refuses a dispatch when
// no paired item can be found, so an audit ticket that someone happened to be
// working on would read as never filed. ListFrom's behaviour is left alone
// rather than widened, so nothing that depends on the narrow filter changes.
func ListAllFrom(root string, statuses ...string) ([]WorkItem, error) {
	return listFrom(root, true, statuses...)
}

func listFrom(root string, includeClaimSuffixed bool, statuses ...string) ([]WorkItem, error) {
	var items []WorkItem
	for _, sd := range statusDirs {
		if !statusRequested(sd.status, statuses) {
			continue
		}
		dir := filepath.Join(root, sd.dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !itemFileName(e.Name(), includeClaimSuffixed) {
				continue
			}
			item, err := parseWorkItem(filepath.Join(dir, e.Name()), sd.status)
			if err != nil {
				continue // skip unparseable files
			}
			if info, err := e.Info(); err == nil {
				item.ModTime = info.ModTime()
			}
			if sd.status == "done" {
				item.CompletedAt = resultFileTime(dir, e.Name())
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// FindFrom returns the single work item with the given id from a workspace
// root, searching the status directories in the order listed in statusDirs
// (available, claimed, done) and stopping at the first hit. The bool reports
// whether it was found; a missing item is not an error, because "no such item"
// is a routine answer for a caller holding an id from an unknown source.
//
// This exists because ListFrom is the wrong primitive for a by-id question in
// two ways. It parses every item in every scanned directory to answer about
// one, and — the load-bearing half — its ".md" suffix filter cannot see
// claimed/ at all: a claim renames the file to <id>.md.<pid> (see
// agent.claimHeld, which learned this the same way). A by-id lookup built on
// ListFrom would therefore report a claimed item as absent, silently, which is
// exactly the shape of failure a gate must not have.
//
// Lookup is a direct stat of <dir>/<id>.md first, falling back to a prefix scan
// only on a miss — so the common case costs one open and the claimed/ case
// still resolves.
func FindFrom(root, id string) (WorkItem, bool, error) {
	// An empty id would build "<dir>/.md"; a separator or ".." in one would let
	// a caller's unvalidated string walk out of the workspace. Neither is a
	// legal macguffin id, so both are "not found" rather than a read.
	if id == "" || strings.ContainsAny(id, `/\`) || id == ".." {
		return WorkItem{}, false, nil
	}
	for _, sd := range statusDirs {
		// Default statuses only, matching what this function searched before
		// pending/ was added to statusDirs. A by-id lookup that started
		// returning parked items would change the answer every existing caller
		// gets — including two dispatch gates — for a reason unrelated to why
		// pending/ became reachable.
		if !sd.byDefault {
			continue
		}
		dir := filepath.Join(root, sd.dir)
		item, err := parseWorkItem(filepath.Join(dir, id+".md"), sd.status)
		if err == nil {
			return item, true, nil
		}
		// Miss on the exact name. In claimed/ the file carries a .<pid> suffix,
		// so fall back to a scan for <id>.md* before giving up on this status.
		name, ok, err := findByPrefix(dir, id+".md")
		if err != nil {
			return WorkItem{}, false, err
		}
		if !ok {
			continue
		}
		item, err = parseWorkItem(filepath.Join(dir, name), sd.status)
		if err != nil {
			continue // present but unparseable — same treatment as ListFrom
		}
		return item, true, nil
	}
	return WorkItem{}, false, nil
}

// resultFileTime returns the mtime of the `<id>.result.json` that mg writes
// beside a completed item, or the zero time when there is none.
//
// The result file is written at `mg done`, so its mtime is the completion
// instant — unlike the item file's own mtime, which a rename carries over from
// filing time. `<id>` is taken from the item file's NAME rather than from its
// `id:` frontmatter, so an item whose frontmatter is missing or disagrees with
// its filename still resolves to the file actually sitting next to it. A claim
// suffix (`<id>.md.<pid>`) is stripped too, for the case of a claimed-looking
// name that ended up in done/.
func resultFileTime(dir, entryName string) time.Time {
	base := entryName
	if i := strings.Index(base, ".md"); i >= 0 {
		base = base[:i]
	}
	if base == "" {
		return time.Time{}
	}
	info, err := os.Stat(filepath.Join(dir, base+".result.json"))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// itemFileName reports whether a directory entry names a work item file.
// `<id>.md` always counts; `<id>.md.<pid>` — the name a claim leaves behind —
// counts only when the caller asked to see claimed items.
func itemFileName(name string, includeClaimSuffixed bool) bool {
	if strings.HasSuffix(name, ".md") {
		return true
	}
	return includeClaimSuffixed && strings.Contains(name, ".md.")
}

// findByPrefix returns the first entry in dir whose name starts with prefix. A
// missing directory is not an error — an absent claimed/ or done/ simply holds
// no items.
func findByPrefix(dir, prefix string) (string, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			return e.Name(), true, nil
		}
	}
	return "", false, nil
}

// statusRequested reports whether a status directory should be scanned given
// the caller's filter. An empty filter selects every DEFAULT status — see
// statusDirs.byDefault for why "all" does not mean "every directory on disk".
func statusRequested(status string, filter []string) bool {
	if len(filter) == 0 {
		return statusIsDefault(status)
	}
	for _, s := range filter {
		if s == status {
			return true
		}
	}
	return false
}

// statusIsDefault reports whether a status is in the set an unfiltered scan
// reads. An unknown status is not, so adding a directory to statusDirs cannot
// silently widen an existing caller.
func statusIsDefault(status string) bool {
	for _, sd := range statusDirs {
		if sd.status == status {
			return sd.byDefault
		}
	}
	return false
}

// parseWorkItem reads a macguffin work item markdown file and extracts
// frontmatter fields. The status is set from the containing directory.
func parseWorkItem(path, status string) (WorkItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return WorkItem{}, err
	}
	defer f.Close()

	item := WorkItem{Status: status}
	scanner := bufio.NewScanner(f)

	// Expect opening ---
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return WorkItem{}, os.ErrInvalid
	}

	// Read frontmatter key: value pairs until closing ---
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, val, ok := parseFrontmatterLine(line)
		if !ok {
			continue
		}
		switch key {
		case "id":
			item.ID = val
		case "assignee":
			item.Assignee = val
		case "type":
			item.Type = val
		case "priority":
			item.Priority = val
		case "tags":
			item.Tags = val
		case "repo":
			item.Repo = val
		case "depends":
			item.Depends = val
		}
	}

	// Read first markdown heading as title.
	//
	// Everything before it is DISCARDED, and that discard is the second way a
	// carrier goes unread (mg-27d4). A block written between the frontmatter's
	// closing `---` and the title heading is consumed here and never reaches
	// scanCarrierBlock — measured live on mg-779b and mg-9863, both sitting in
	// available/ with `stage: gated` three lines above their heading, both
	// invisible to the gate that reads this parse. (Their `assignee: parked` was
	// holding them, which is luck, not a mechanism: the stage line was doing
	// nothing.) So the skipped lines are checked on the way past.
	found, preTitleCarrier := false, false
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "# ") {
			item.Title = strings.TrimPrefix(line, "# ")
			found = true
			break
		}
		if key, _, ok := parseCarrierLine(raw); ok && isGateBearingCarrierKey(key) {
			preTitleCarrier = true
		}
	}
	if found {
		item.Workflow, item.Stage, item.CarrierUnreadable = scanCarrierBlock(scanner)
	}
	// A pre-title declaration is unreadable whatever the body below turns out to
	// hold, and so is a file with no title heading at all to scan from.
	if preTitleCarrier {
		item.CarrierUnreadable = true
	}

	return item, nil
}

// carrierBlockScanLimit bounds how many body lines scanCarrierBlock will read.
// The scan normally stops at the first line that is not `key: value` shaped —
// for the overwhelming majority of items that is the FIRST body line, so this
// limit only binds on a body that is nothing but such lines. It exists so that
// listFrom, which parses every item in done/, cannot be made to read a large
// file line by line by an unusual body.
const carrierBlockScanLimit = 16

// scanCarrierBlock reads the state carrier block from the body lines following
// the title heading and returns its `workflow:` and `stage:` values. Both are
// empty when the body has no carrier block, which is the ordinary case.
//
// THE PARSE IS DELIBERATELY STRICT, AND THE STRICTNESS IS THE POINT. The block
// is the run of unindented `key: value` lines that STARTS the body; the scan
// stops at the first line that is not one. It does not search the body for a
// `stage:` line, because bodies discuss stages: mg-69b1 — the ticket that made
// the stage line load-bearing — quotes «set `stage: gated`» inside a prose
// paragraph, and a body-wide search would have gated the bug report about the
// gate. A prose line is not a declaration, and only a declaration may gate.
//
// Leading blank lines are skipped so a body that puts a blank line under its
// heading still parses; anything else ends the block before it starts.
//
// # The third return value, and why two were not enough (mg-27d4)
//
// Stopping at the first non-carrier line is right, but "I stopped" and "there
// was nothing here" were being reported with the same two values. So a body
// whose block sits one line below a lead-in sentence — a PR link, a "Triage
// this:" — returned empty workflow and empty stage, indistinguishable from an
// ordinary item that declares nothing, and its `stage: gated` did not gate.
//
// unreadable closes that: after the block ends, the scan keeps looking for a
// GATE-BEARING declaration line further down, and reports one if it finds it
// WITHOUT reading its value. The value is deliberately not returned — the whole
// finding is that this body's declarations are not where a declaration goes,
// and picking one out of the middle of a body is the body-wide search this
// parser exists to refuse (see TestCarrierBlockIgnoresProseThatQuotesIt).
func scanCarrierBlock(scanner *bufio.Scanner) (workflow, stage string, unreadable bool) {
	seen := 0
	started := false
	for scanner.Scan() && seen < carrierBlockScanLimit {
		seen++
		line := scanner.Text()
		if !started && strings.TrimSpace(line) == "" {
			continue
		}
		key, val, ok := parseCarrierLine(line)
		if !ok {
			return workflow, stage, scanUnreachableCarrier(scanner, line)
		}
		started = true
		switch key {
		case "workflow":
			workflow = val
		case "stage":
			stage = val
		}
	}
	// Falling out of the loop means the block ran to carrierBlockScanLimit and
	// was TRUNCATED, not that it ended. Whatever is on line 17 was never read, so
	// this is the same "could not reach" state and gets the same answer. No real
	// carrier block is 16 lines long; the shipped one is three.
	return workflow, stage, seen >= carrierBlockScanLimit
}

// unreachableCarrierScanLimit bounds how far past the end of the leading block
// scanUnreachableCarrier keeps looking. It is not the same kind of number as
// carrierBlockScanLimit: that one bounds a scan that normally stops on its first
// line, while this one runs on every item that has any body at all, including
// every item in done/ — where bodies run to a thousand lines and the store is
// 25MB. Reading all of it to be sure is affordable exactly once and not on every
// list.
//
// It is also a semantic bound and not only a budget. The convention is that the
// carrier block LEADS the body; a `stage:` line 60 lines into a narrative is not
// a misplaced carrier block, it is prose that happens to be shaped like one, and
// treating it as a gate would be the body-wide search this file refuses.
//
// Measured over the live store when this landed: of the 18 items with an
// unreachable carrier, the deepest sat 3 body lines below the title. The margin
// is wide because the bound is meant to be uncontroversial, not tight.
//
// THE RESIDUAL IS REAL AND IS STATED HERE RATHER THAN LEFT TO BE DISCOVERED: a
// carrier block pushed past this many lines is still invisible and still reads
// as ungated. That is the same fail-open this constant exists to shrink, made
// much harder to reach rather than impossible.
const unreachableCarrierScanLimit = 40

// scanUnreachableCarrier reports whether a gate-bearing carrier declaration
// appears below the point where the leading block ended — the state mg-27d4
// named: "there is carrier-shaped content here that I could not reach".
//
// stopLine is the line that ended the block; it is examined too, since a body
// can open with a single lead-in sentence and put the block directly under it.
//
// Two narrowings, each of which is what keeps a fail-closed signal from
// stranding work that is fine:
//
//   - Only `workflow:` and `stage:` count. Those are the lines that carry a
//     GATE; `gh:` ties a ticket to an issue and changes no dispatch decision, so
//     flagging a body for one would refuse work over a line that could not have
//     gated it either way.
//   - Fenced code blocks are skipped. A body that quotes the carrier convention
//     inside ``` is documenting it, not declaring it — the mayor prompt's own
//     example block has exactly this shape, and every ticket that pastes it
//     would otherwise gate itself. Indented quotations need no special case:
//     parseCarrierLine already requires column 0.
func scanUnreachableCarrier(scanner *bufio.Scanner, stopLine string) bool {
	fenced := false
	line := stopLine
	for seen := 0; seen < unreachableCarrierScanLimit; seen++ {
		if isCodeFence(line) {
			fenced = !fenced
		} else if !fenced {
			if key, _, ok := parseCarrierLine(line); ok && isGateBearingCarrierKey(key) {
				return true
			}
		}
		if !scanner.Scan() {
			return false
		}
		line = scanner.Text()
	}
	return false
}

// isGateBearingCarrierKey reports whether a carrier key is one whose misplaced
// declaration is worth refusing dispatch over. `workflow:` and `stage:` are:
// `stage:` IS the gate (config.IsStageGated) and `workflow:` is what says a body
// is carrying one. `gh:` is not — it ties a ticket to an issue and changes no
// dispatch decision, so gating an item over a stray one would refuse work for a
// line that could not have gated it either way.
func isGateBearingCarrierKey(key string) bool {
	return key == "workflow" || key == "stage"
}

// isCodeFence reports whether a line opens or closes a markdown fenced block.
// Only column-0 fences matter, because only column-0 lines can parse as carrier
// declarations in the first place.
func isCodeFence(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

// parseCarrierLine splits one carrier-block line into its key and value. It is
// stricter than parseFrontmatterLine, which it deliberately does not reuse:
// that one accepts any line containing a colon, which in a BODY means most
// English sentences.
//
// Three requirements, each ruling out a shape of prose that would otherwise
// read as a declaration:
//
//   - the key starts at column 0 and is one identifier-shaped word, so an
//     indented quotation of a carrier line is not one;
//   - the colon is followed by a space or ends the line;
//   - the value is a single whitespace-free token. Every field the convention
//     defines is one — `gh-issue`, `gated`, `drellem2/pogo#104`, `required` —
//     and this is what separates a declaration from a sentence that happens to
//     start `Reported: the daemon wedged overnight`.
func parseCarrierLine(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = line[:idx]
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c == '_' || c == '-':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return "", "", false
		}
	}
	rest := line[idx+1:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", "", false
	}
	val = strings.TrimSpace(rest)
	if strings.ContainsAny(val, " \t") {
		return "", "", false
	}
	return strings.ToLower(key), val, true
}

// parseFrontmatterLine splits "key: value" from YAML-like frontmatter.
func parseFrontmatterLine(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	// Strip surrounding brackets from arrays like [tag1, tag2]
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
		val = val[1 : len(val)-1]
	}
	return key, val, true
}
