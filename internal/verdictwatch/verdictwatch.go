// Package verdictwatch reports every work item that reached `done` or
// `archived` whose FILER never received a verdict mail from the worker.
//
// # The predicate, in one line
//
//	AN ITEM REACHING done (OR archived) WITH NO VERDICT MAIL RECEIVED BY ITS
//	FILER IS A DROPPED VERDICT.
//
// Both halves are read from macguffin's own store and nothing else:
//
//   - the landing      `work.done` / `work.archive` in events.jsonl
//   - the filer        `creator:` in the item's own frontmatter
//   - the worker       `polecat-<name>` in the item's result sidecar
//   - the verdict mail a message in the filer's mailbox whose `From:` is that
//     worker
//
// # Where this came from, and why it moved
//
// This is a port of `verdictwatch.py`, the deliverable of macguffin's mg-bf3f.
// mg-f911 audited that instrument, CONFIRMED it, and repaired its rotted
// proof-of-life — and left one defect standing: NOTHING RAN IT. Zero pogo
// schedules, zero cron entries, zero references outside its own directory. It
// sat in `research/onethird_program/code/verdict_delivery_bf3f/`, and code in a
// research working directory has no runner by construction.
//
// Registering a schedule that reached into that directory would have made the
// fleet's verdict-integrity detector depend on the layout of an unrelated
// project's scratch tree. Porting it here instead puts it on the same footing as
// its eight siblings — check-acks, check-commit-body, check-intake,
// check-mailloops, check-prompts, check-staleness, check-strandedmail,
// check-teardown — every one of which is a read-only detector that reports a
// condition and takes no action. That is the family's actual membership
// criterion (pm-pogo, mg-f5dd): not which subsystem it reads, not which repo the
// state lives in. check-teardown already reads mg state and asks GitHub about
// it; check-strandedmail already reads the macguffin mail tree.
//
// # It REPORTS ONLY, and that is a boundary, not an implementation stage
//
// Nothing here files a ticket, sends the missing verdict, or edits an item. If a
// future version should FILE the missing verdict, that is a DIFFERENT command
// and it must not live in this family. A detector whose report is trusted is one
// that has never surprised its reader by doing something.
//
// # What this detector cannot see, stated here and not in a footnote
//
//	D-1  A verdict delivered by any channel other than macguffin mail — a commit
//	     subject, a docs/ file, a spoken handover — is invisible to it and is
//	     reported as DROPPED. That is the intended polarity: the original
//	     complaint is precisely that commit subjects are not delivery.
//	D-2  An item whose worker cannot be resolved is NOT reported as delivered and
//	     NOT reported as dropped. It goes in its own UNDECIDABLE bucket, sized
//	     and listed, because a detector that silently absorbs what it cannot
//	     reach is the defect this whole lineage keeps rediscovering.
//	D-3  A verdict mailed to somebody OTHER than the filer (to mayor, say) is not
//	     delivery to the filer and is reported as DROPPED. MailsElsewhere on each
//	     row records it, because it discriminates a dead process from a live one
//	     that did not mail the filer.
package verdictwatch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/mailbox"
)

// Status is one item's verdict-delivery state. Three values, not two: see D-2.
type Status string

const (
	// Delivered — the filer's mailbox holds a message from the worker.
	Delivered Status = "DELIVERED"
	// Dropped — the item landed and the filer's mailbox holds nothing from the
	// worker. The finding this detector exists to produce.
	Dropped Status = "DROPPED"
	// Undecidable — the worker could not be resolved from mg's own store, so
	// neither verdict can be reached. Counted and listed, never folded into
	// either of the other two.
	Undecidable Status = "UNDECIDABLE"
)

// Mail is the verdict message that satisfied the predicate, named so a reader
// can go and read it rather than take this report's word for it.
type Mail struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	State   string `json:"state"`
	// N is how many messages from the worker the filer holds. The row names the
	// earliest; this says whether there were others.
	N int `json:"n"`
}

// Row is one landed work item's verdict-delivery state.
type Row struct {
	ID    string `json:"id"`
	Filer string `json:"filer"`
	// Worker is the name EXACTLY as the branch spells it. The alternate
	// spellings in WorkerNames are for MATCHING; only this one is for READING.
	// The python original printed sorted(names)[0] here, which prefers
	// `mg-y0120` over `y0120` alphabetically and so printed a name that agent
	// never used (its DEFECT-1).
	Worker string `json:"worker,omitempty"`
	// Resolver says HOW the worker was identified: "sidecar" (mg recorded it),
	// or "shape" (inferred from the polecat naming convention and accepted only
	// because that name actually wrote to this filer). A reader who cannot tell
	// the two apart cannot weigh the row.
	Resolver    string   `json:"resolver,omitempty"`
	WorkerNames []string `json:"worker_names,omitempty"`
	// Landed is the RFC3339 stamp of the work.done / work.archive event,
	// verbatim as mg wrote it.
	Landed string `json:"landed"`
	// Kind is "done" or "archived".
	Kind      string `json:"kind"`
	DoneActor string `json:"done_actor,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    Status `json:"status"`
	// VerdictMail is set only on a DELIVERED row.
	VerdictMail *Mail `json:"verdict_mail,omitempty"`
	// MailsElsewhere counts messages this worker sent to somebody who was not
	// the filer. It discriminates a worker that died from one that mailed the
	// wrong addressee — opposite repairs, and the report must not make them look
	// the same.
	MailsElsewhere int `json:"mails_elsewhere"`
	// MailboxExists is false when the filer has no mailbox at all, which is a
	// different finding from an empty one: nobody has ever written to them, so
	// every one of their items is dropped BY CONSTRUCTION.
	MailboxExists bool `json:"mailbox_exists"`
}

// Report is the outcome of one scan.
type Report struct {
	Root  string `json:"root"`
	Filer string `json:"filer,omitempty"`
	Since string `json:"since,omitempty"`

	Scanned     int `json:"scanned"`
	Delivered   int `json:"delivered"`
	Dropped     int `json:"dropped"`
	Undecidable int `json:"undecidable"`

	// Rows holds every scanned item, oldest landing first. The original's stated
	// purpose is that a backlog can be RECOVERED and not merely alarmed about,
	// and recovery is done in landing order.
	Rows []Row `json:"rows,omitempty"`

	// MissingBoxes names filers with no mailbox at all, sorted.
	MissingBoxes []string `json:"missing_boxes,omitempty"`

	// CollapsedCopies counts extra on-disk copies of an already-seen work item
	// — the live store holds sixteen, archived under two months each. Reported
	// rather than silently absorbed: a number that quietly shrinks the
	// population is the same class of thing this detector exists to catch.
	CollapsedCopies int `json:"collapsed_copies,omitempty"`

	// Blind names, in words, every reason this run measured nothing. A non-empty
	// Blind means the counts below it are not a result — see InstrumentFailure.
	Blind []string `json:"blind,omitempty"`
}

// InstrumentFailure reports whether this run could not look, as distinct from
// having looked and found nothing.
//
// THIS IS THE DEFECT THE WHOLE LINEAGE KEEPS REDISCOVERING. A check that cannot
// distinguish "clean" from "could not look" certifies a state it never observed.
// It is not hypothetical here: on 2026-08-09 both check-intake and
// check-teardown returned INSTRUMENT FAILURE during a four-hour network outage
// rather than exiting 0, which is the behaviour this copies.
//
// The shape it guards against for THIS detector is specific and quiet. Every
// landing comes from events.jsonl. Lose that file — renamed, rotated, a store
// pointed one directory too high — and the scan finds zero landed items, reports
// "0 dropped", and exits 0. A green census would read as fleet health while
// every verdict in the fleet went missing.
func (r Report) InstrumentFailure() bool { return len(r.Blind) > 0 }

// Actionable reports whether the run found at least one dropped verdict.
//
// UNDECIDABLE rows are deliberately NOT actionable: they are a statement about
// this detector's reach, not about the fleet's behaviour, and a detector that
// cried wolf over its own blind spot would be muted long before the run that
// mattered.
func (r Report) Actionable() bool { return r.Dropped > 0 }

// DroppedRows returns the findings, oldest landing first.
func (r Report) DroppedRows() []Row { return r.rowsWith(Dropped) }

// DeliveredRows returns the rows that satisfied the predicate.
func (r Report) DeliveredRows() []Row { return r.rowsWith(Delivered) }

// UndecidableRows returns the rows this detector could not reach.
func (r Report) UndecidableRows() []Row { return r.rowsWith(Undecidable) }

func (r Report) rowsWith(s Status) []Row {
	var out []Row
	for _, row := range r.Rows {
		if row.Status == s {
			out = append(out, row)
		}
	}
	return out
}

// Options scopes one scan.
type Options struct {
	// Root is the macguffin store. Empty means DefaultRoot().
	Root string
	// Filer restricts the scan to items filed by one agent. Empty scans EVERY
	// filer — this detector is fleet-wide by construction, which is most of the
	// argument for it living here rather than in the project that discovered it.
	Filer string
	// Since is an RFC3339 prefix; items that landed before it are excluded.
	// Compared as a string, so "2026-08-05" and a full stamp both work.
	Since string
}

// Scan runs the predicate over the store.
//
// A store it cannot read produces a Report whose Blind list says so, never an
// empty report — see InstrumentFailure. The error return is reserved for an IO
// failure severe enough that the walk itself could not proceed, and a caller
// must treat it exactly as it treats InstrumentFailure: this run measured
// nothing.
func Scan(opts Options) (Report, error) {
	root := opts.Root
	if root == "" {
		root = DefaultRoot()
	}
	rep := Report{Root: root, Filer: opts.Filer, Since: opts.Since}
	if root == "" {
		rep.Blind = append(rep.Blind, "no macguffin store root could be resolved (MG_ROOT unset and no home directory)")
		return rep, nil
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		rep.Blind = append(rep.Blind, fmt.Sprintf("the macguffin store %s is not a readable directory", root))
		return rep, nil
	}

	items, err := loadItems(filepath.Join(root, "work"))
	if err != nil {
		return rep, fmt.Errorf("read the work tree under %s: %w", root, err)
	}
	items, rep.CollapsedCopies = dedupeByID(items)
	if len(items) == 0 {
		rep.Blind = append(rep.Blind, fmt.Sprintf("the work tree %s holds no work items at all", filepath.Join(root, "work")))
	}

	landings, err := loadLandings(filepath.Join(root, "events.jsonl"))
	if err != nil {
		// The landing half of the predicate. Without it every item looks
		// un-landed and the scan reports a clean fleet — the exact quiet failure
		// this detector exists to make impossible.
		rep.Blind = append(rep.Blind, fmt.Sprintf("the landing half is unreadable: %v (every item would read as never landed, and the scan would report a clean fleet)", err))
		return rep, nil
	}

	mailRoot := filepath.Join(root, "mail")
	if fi, err := os.Stat(mailRoot); err != nil || !fi.IsDir() {
		// The delivery half. Without it every landed item looks undelivered and
		// the scan reports the whole fleet as dropped — loud rather than quiet,
		// but still not a measurement.
		rep.Blind = append(rep.Blind, fmt.Sprintf("the delivery half is unreadable: %s is not a readable directory (every landed item would read as dropped)", mailRoot))
		return rep, nil
	}

	sentBy := loadMailSent(filepath.Join(root, "events.jsonl"))

	// One mailbox read per filer, not per item: a fleet-wide scan of this store
	// covers hundreds of items across a few dozen filers.
	type index struct {
		byFrom map[string][]message
		exists bool
	}
	boxes := map[string]*index{}
	loadBox := func(agent string) *index {
		if idx, ok := boxes[agent]; ok {
			return idx
		}
		msgs, exists := loadMailbox(mailRoot, agent)
		idx := &index{byFrom: map[string][]message{}, exists: exists}
		for _, m := range msgs {
			key := mailbox.Canonical(m.From)
			idx.byFrom[key] = append(idx.byFrom[key], m)
		}
		boxes[agent] = idx
		return idx
	}

	missing := map[string]bool{}
	for _, item := range items {
		land, ok := landings[item.ID]
		if !ok {
			continue // never landed: outside the predicate entirely
		}
		if opts.Since != "" && land.TS < opts.Since {
			continue
		}
		if item.Creator == "" {
			// No filer means no addressee, so the predicate has nothing to say.
			continue
		}
		if opts.Filer != "" && item.Creator != opts.Filer {
			continue
		}
		idx := loadBox(item.Creator)
		if !idx.exists {
			missing[item.Creator] = true
		}

		names, resolver := resolveWorker(item, idx.byFrom)
		row := Row{
			ID:            item.ID,
			Filer:         item.Creator,
			Worker:        item.DeclaredWorker,
			Resolver:      resolver,
			WorkerNames:   names,
			Landed:        land.TS,
			Kind:          land.Kind,
			DoneActor:     land.Actor,
			Title:         item.Title,
			MailboxExists: idx.exists,
		}
		if row.Worker == "" && len(names) > 0 {
			row.Worker = names[0]
		}
		if len(names) == 0 {
			row.Status = Undecidable
			rep.Rows = append(rep.Rows, row)
			continue
		}

		var hits []message
		for _, n := range names {
			hits = append(hits, idx.byFrom[mailbox.Canonical(n)]...)
			for _, e := range sentBy[mailbox.Canonical(n)] {
				if mailbox.Canonical(e.To) != mailbox.Canonical(item.Creator) {
					row.MailsElsewhere++
				}
			}
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].Date < hits[j].Date })
		if len(hits) > 0 {
			row.Status = Delivered
			row.VerdictMail = &Mail{
				ID: hits[0].ID, Date: hits[0].Date, Subject: hits[0].Subject,
				State: hits[0].State, N: len(hits),
			}
		} else {
			row.Status = Dropped
		}
		rep.Rows = append(rep.Rows, row)
	}

	// Oldest landing first. Ties broken by id so a re-scan of an unchanged store
	// renders byte-identically: a report that reshuffles looks like it changed,
	// and a reader watching for change learns to stop reading it.
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		if rep.Rows[i].Landed != rep.Rows[j].Landed {
			return rep.Rows[i].Landed < rep.Rows[j].Landed
		}
		return rep.Rows[i].ID < rep.Rows[j].ID
	})

	rep.Scanned = len(rep.Rows)
	for _, row := range rep.Rows {
		switch row.Status {
		case Delivered:
			rep.Delivered++
		case Dropped:
			rep.Dropped++
		case Undecidable:
			rep.Undecidable++
		}
	}
	for a := range missing {
		rep.MissingBoxes = append(rep.MissingBoxes, a)
	}
	sort.Strings(rep.MissingBoxes)

	// An UNSCOPED run that judged nothing has not measured a clean fleet; it has
	// failed to find the population. The operator asked about every filer since
	// the beginning of the store, and this store always holds landed items.
	//
	// A SCOPED run that comes back empty is a different thing and stays exit 0:
	// `--filer somebody-who-never-filed` is an answer to the question asked, and
	// the python original is pinned to that behaviour by its own construction
	// suite. The two are separated here rather than collapsed, because the
	// operator supplying a filter is the one fact that tells them apart.
	if rep.Scanned == 0 && opts.Filer == "" && opts.Since == "" {
		rep.Blind = append(rep.Blind, "an unscoped scan judged 0 landed items; the population is missing, not clean")
	}
	return rep, nil
}

// ---------------------------------------------------------------- items

var itemFile = regexp.MustCompile(`^(mg-[0-9a-z]+)\.md(?:\.\d+)?$`)

var (
	reCreator = regexp.MustCompile(`(?m)^creator:\s*(\S+)\s*$`)
	reTitle   = regexp.MustCompile(`(?m)^#\s+(.*)$`)
	reBranch  = regexp.MustCompile(`^polecat-(.+)$`)
)

type item struct {
	ID      string
	Path    string
	Creator string
	Title   string
	// DeclaredWorker is the worker name exactly as the result sidecar's branch
	// spells it, or "" when the sidecar names none.
	DeclaredWorker string
	// SidecarNames are the spellings to MATCH a From: against.
	SidecarNames []string
}

// loadItems reads every work item under workRoot, at every status, recursively.
//
// The walk is recursive rather than a fixed list of status directories because
// archived items live under `work/archive/<month>/`, and an archived item is
// squarely inside the predicate — "done OR archived". internal/workitem's
// ListAllFrom reads a flat set of status directories and does not parse
// `creator:`, so it cannot answer this question; widening it would move the
// results every existing caller gets.
//
// The `.md.<pid>` form is matched too. A CLAIMED item is not `mg-bf3f.md` on
// disk: macguffin stamps the owning pid into the filename. That is harmless for
// this predicate (a landed item is back to a plain `.md`) and is handled anyway,
// because the original's DEFECT-5 was precisely a glob that could not see a
// single in-flight item.
func loadItems(workRoot string) ([]item, error) {
	var out []item
	err := filepath.WalkDir(workRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A single unreadable subdirectory must not abort the walk: the
			// dropped verdict this scan exists to find could be in the next one.
			// A workRoot that does not exist at all is reported by the caller as
			// an empty population, which is Blind.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		m := itemFile.FindStringSubmatch(d.Name())
		if m == nil {
			return nil
		}
		text, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		it := item{ID: m[1], Path: path}
		if c := reCreator.FindSubmatch(text); c != nil {
			it.Creator = string(c[1])
		}
		if t := reTitle.FindSubmatch(text); t != nil {
			it.Title = strings.TrimSpace(string(t[1]))
		}
		it.DeclaredWorker, it.SidecarNames = sidecarWorker(path)
		out = append(out, it)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

// dedupeByID collapses copies of the same work item to one row and returns how
// many were collapsed.
//
// THIS IS NOT DEFENSIVE PROGRAMMING; the live store holds sixteen of them —
// items archived under two different months, so `work/archive/2026-03/mg-3119.md`
// and `work/archive/2026-05/mg-3119.md` are both on disk. Counted twice, one
// dropped verdict is reported as two, and the second listing sends its reader
// chasing an item they have already looked at. A backlog report that inflates is
// a backlog report nobody finishes.
//
// The preference is DECLARED rather than incidental. A copy carrying a result
// sidecar wins, because the worker identity is the scarce input and preferring
// the copy that HAS one can only move a row UNDECIDABLE -> DELIVERED/DROPPED,
// never manufacture a drop against a worker nobody named. Ties break on the
// path, so a re-scan of an unchanged store picks the same copy — the original
// took whichever the glob yielded last, which is a coin flip over which copy's
// `creator:` the report is about.
func dedupeByID(items []item) ([]item, int) {
	best := map[string]item{}
	collapsed := 0
	for _, it := range items {
		prev, seen := best[it.ID]
		if !seen {
			best[it.ID] = it
			continue
		}
		collapsed++
		if preferItem(it, prev) {
			best[it.ID] = it
		}
	}
	out := make([]item, 0, len(best))
	for _, it := range best {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, collapsed
}

func preferItem(candidate, incumbent item) bool {
	if len(candidate.SidecarNames) != len(incumbent.SidecarNames) {
		return len(candidate.SidecarNames) > len(incumbent.SidecarNames)
	}
	return candidate.Path > incumbent.Path
}

// sidecarWorker reads `branch: polecat-<name>` out of the item's result
// sidecar. The branch is the only worker identity macguffin RECORDS.
func sidecarWorker(itemPath string) (declared string, names []string) {
	base := itemPath
	if i := strings.Index(base, ".md"); i >= 0 {
		base = base[:i]
	}
	data, err := os.ReadFile(base + ".result.json")
	if err != nil {
		return "", nil
	}
	var side struct {
		Branch string `json:"branch"`
	}
	if json.Unmarshal(data, &side) != nil {
		return "", nil
	}
	m := reBranch.FindStringSubmatch(strings.TrimSpace(side.Branch))
	if m == nil {
		return "", nil
	}
	return m[1], []string{m[1]}
}

// resolveWorker returns the names to match a From: against, and how they were
// obtained.
//
// DECLARED ASYMMETRY, carried over verbatim from the original and load-bearing.
// The `shape` fallback is consulted ONLY when the sidecar is silent, and it is
// accepted ONLY when the name it proposes actually appears as a From: in that
// filer's mailbox. So it can move a row UNDECIDABLE -> DELIVERED and can NEVER
// move one to DROPPED. It can only shrink the reported drop count, never inflate
// it. A resolver that could manufacture drops would make this report
// unfalsifiable.
func resolveWorker(it item, byFrom map[string][]message) (names []string, resolver string) {
	if len(it.SidecarNames) > 0 {
		return it.SidecarNames, "sidecar"
	}
	var hits []string
	for _, cand := range shapeNames(it.ID) {
		if _, ok := byFrom[mailbox.Canonical(cand)]; ok {
			hits = append(hits, cand)
		}
	}
	if len(hits) == 0 {
		return nil, ""
	}
	sort.Strings(hits)
	return hits, "shape"
}

var idSuffix = regexp.MustCompile(`^mg-([0-9a-f]{4})$`)

// shapeNames enumerates the agent names the polecat naming convention would give
// a worker on this item.
//
// The sidecar is the only worker identity mg RECORDS, but it is not the only one
// mg CARRIES: a polecat working `mg-9a19` is named `9a19`, or `z9a19`, or
// `mg-9a19` — the id's four hex characters with at most a one-character
// generation prefix. Resolving from the sidecar alone put two items that had
// been hand-verified as DELIVERED into UNDECIDABLE, which is the same silence
// the original ticket was about (its DEFECT-2).
//
// Deliberately narrow: at most ONE alphanumeric character of prefix, or the
// literal `cat-` prefix. `mg-` spellings need no entry because every comparison
// runs through mailbox.Canonical, which is what mg itself does. A wider rule
// starts matching unrelated agents.
func shapeNames(id string) []string {
	m := idSuffix.FindStringSubmatch(id)
	if m == nil {
		return nil
	}
	s := m[1]
	out := []string{s, "cat-" + s}
	for _, c := range "abcdefghijklmnopqrstuvwxyz0123456789" {
		out = append(out, string(c)+s, "cat-"+string(c)+s)
	}
	return out
}

// ---------------------------------------------------------------- events

type landing struct {
	TS    string
	Kind  string
	Actor string
}

// loadLandings reads the first landing of every item out of events.jsonl.
//
// An item that goes done and is later archived lands ONCE, at `done`. An item
// archived without ever going done lands at `archive` — the predicate says "done
// OR archived" and both are terminal for the filer's purposes.
//
// A missing or unreadable events.jsonl is an ERROR, not an empty map. It is the
// landing half of the predicate: returning an empty map would make every item
// read as never landed and the scan report a clean fleet.
func loadLandings(path string) (map[string]landing, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]landing{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"work.`) {
			continue
		}
		var e struct {
			Type   string `json:"type"`
			ItemID string `json:"item_id"`
			TS     string `json:"ts"`
			Actor  string `json:"actor"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		var kind string
		switch e.Type {
		case "work.done":
			kind = "done"
		case "work.archive":
			kind = "archived"
		default:
			continue
		}
		if e.ItemID == "" {
			continue
		}
		prev, ok := out[e.ItemID]
		if !ok || (prev.Kind == "archived" && kind == "done") {
			out[e.ItemID] = landing{TS: e.TS, Kind: kind, Actor: e.Actor}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type sentEvent struct {
	From string `json:"from"`
	To   string `json:"to"`
	TS   string `json:"ts"`
}

// loadMailSent indexes every mail.sent event by canonical sender, which is what
// MailsElsewhere counts against. A store with no readable events file yields an
// empty index; the landing read above has already declared that blindness, so
// this one does not re-declare it.
func loadMailSent(path string) map[string][]sentEvent {
	out := map[string][]sentEvent{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"mail.sent"`) {
			continue
		}
		var e struct {
			Type string `json:"type"`
			sentEvent
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "mail.sent" {
			continue
		}
		key := mailbox.Canonical(e.From)
		out[key] = append(out[key], e.sentEvent)
	}
	return out
}

// ---------------------------------------------------------------- mail

type message struct {
	ID      string
	State   string
	From    string
	Subject string
	Date    string
}

var header = regexp.MustCompile(`(?m)^([A-Za-z-]+):[ \t]*(.*)$`)

// loadMailbox reads every message in an agent's mailbox: unread, read AND
// ARCHIVED. Returns the messages and whether the mailbox exists at all — which
// is a different thing from an empty mailbox, because "nobody ever wrote to
// them" and "they got no verdicts" are different findings.
//
// The maildir is read DIRECTLY rather than through `mg mail list --json`, and
// that is not a shortcut. `--all` on that command means read plus unread; it
// does NOT include archived mail. A filer who received a verdict and then filed
// it away would read as never having received one, which would turn ordinary
// inbox hygiene into a fleet-wide false alarm.
func loadMailbox(mailRoot, agent string) ([]message, bool) {
	box := filepath.Join(mailRoot, agent)
	if fi, err := os.Stat(box); err != nil || !fi.IsDir() {
		return nil, false
	}
	var msgs []message
	for _, sub := range []string{"new", "cur", "archive"} {
		dir := filepath.Join(box, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			head, _, _ := strings.Cut(string(data), "\n\n")
			m := message{ID: e.Name(), State: sub}
			for _, kv := range header.FindAllStringSubmatch(head, -1) {
				switch strings.ToLower(kv[1]) {
				case "from":
					m.From = strings.TrimSpace(kv[2])
				case "subject":
					m.Subject = strings.TrimSpace(kv[2])
				case "date":
					m.Date = strings.TrimSpace(kv[2])
				}
			}
			msgs = append(msgs, m)
		}
	}
	return msgs, true
}

// ---------------------------------------------------------------- root

// DefaultRoot resolves the macguffin store the same way mg and the dispatch
// gates do: an explicit MG_ROOT wins, then $HOME/.macguffin.
//
// Under a test binary with no MG_ROOT it returns "" rather than the developer's
// live store. This package only reads, so the blast radius of getting it wrong
// is a test that passes or fails on the contents of one machine's ~/.macguffin —
// exactly the kind of result this package exists to distrust. Tests pass an
// explicit root.
func DefaultRoot() string {
	if root := os.Getenv("MG_ROOT"); root != "" {
		return root
	}
	if testing.Testing() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin")
}
