package stallwatch

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/workitem"
)

// blockedCountsFile is the blocked-reminder's notice-count store, relative to
// pogod's state dir. pogod passes the full path as Options.BlockedStatePath.
const blockedCountsFile = "blocked-reminders.json"

// BlockedStatePath is where pogod keeps the blocked-reminder's counts under
// pogoHome. It is exported so the daemon and its wiring test name one path.
func BlockedStatePath(pogoHome string) string {
	return filepath.Join(pogoHome, "stallwatch", blockedCountsFile)
}

// blockedRecord is how many notices one (item, blocked-on agent) pair has drawn
// and when the last one went out (drellem2/pogo#300).
//
// The blocked-reminder used to keep this in lastNudge, keyed by item alone and
// pruned the moment the item stopped being a candidate. That made max_notices a
// cap per blocked PERIOD rather than per block: an item that was reassigned,
// claimed, waived or self-set for one tick and then blocked again read as new
// and drew the full four notices again — and a pogod restart (the nightly
// deploy restarts it daily) reset every count too. One item drew 10 notices in
// 15 hours that way against a cap of four.
//
// So the record outlives the block. When its pair stops being a candidate the
// record is stamped ClearedAt and kept for the retention window
// (blocked_reminder_count_ttl, default 24h); a re-block on the SAME agent inside
// the window resumes the count, cooldown included. A re-block on a DIFFERENT
// agent is a different key and starts fresh: that agent has never been told. The
// record is dropped early when the item closes (reaches done/).
type blockedRecord struct {
	Item      string    `json:"item"`
	BlockedOn string    `json:"blocked_on"`
	Last      time.Time `json:"last"`
	Count     int       `json:"count"`
	ClearedAt time.Time `json:"cleared_at,omitempty"`
}

// blockedKey keys a record on (item id, normalised blocked-on name). The name
// is "" for a bare `blocked:`, which is a pair like any other.
func blockedKey(id, who string) string { return id + "\x00" + who }

// blockedFile is the on-disk shape. Version lets a later change refuse a file
// it cannot read rather than misread it.
type blockedFile struct {
	Version int             `json:"version"`
	Records []blockedRecord `json:"records"`
}

const blockedFileVersion = 1

// loadBlockedCounts reads the store at path. Every failure — no file, an
// unreadable one, bad JSON, an unknown version — FAILS OPEN: the watcher starts
// with no counts, which means each standing block draws a notice again. That is
// the pre-#300 restart behaviour, and it is the safe direction: a broken store
// must never silence a reminder nobody has received. A file that exists but
// cannot be used is logged, because otherwise the extra notices it causes look
// exactly like the bug this store fixes.
func loadBlockedCounts(path string) map[string]blockedRecord {
	recs := make(map[string]blockedRecord)
	if path == "" {
		return recs
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("stallwatch: blocked-reminder counts unreadable, starting fresh: %v", err)
		}
		return recs
	}
	var f blockedFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("stallwatch: blocked-reminder counts at %s are not valid JSON, starting fresh: %v", path, err)
		return recs
	}
	if f.Version != blockedFileVersion {
		log.Printf("stallwatch: blocked-reminder counts at %s have version %d (want %d), starting fresh", path, f.Version, blockedFileVersion)
		return recs
	}
	for _, r := range f.Records {
		if r.Item == "" || r.Count <= 0 {
			continue
		}
		recs[blockedKey(r.Item, r.BlockedOn)] = r
	}
	return recs
}

// saveBlockedCountsLocked writes the store atomically (temp file + rename). A
// failed write is logged and otherwise ignored: the in-memory counts still hold
// for the life of the process. The caller holds w.mu.
func (w *Watcher) saveBlockedCountsLocked() {
	if w.blockedPath == "" {
		return
	}
	f := blockedFile{Version: blockedFileVersion, Records: make([]blockedRecord, 0, len(w.blocked))}
	for _, r := range w.blocked {
		f.Records = append(f.Records, r)
	}
	sort.Slice(f.Records, func(i, j int) bool {
		if f.Records[i].Item != f.Records[j].Item {
			return f.Records[i].Item < f.Records[j].Item
		}
		return f.Records[i].BlockedOn < f.Records[j].BlockedOn
	})
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.blockedPath), 0o755); err != nil {
		log.Printf("stallwatch: cannot save blocked-reminder counts: %v", err)
		return
	}
	tmp := w.blockedPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Printf("stallwatch: cannot save blocked-reminder counts: %v", err)
		return
	}
	if err := os.Rename(tmp, w.blockedPath); err != nil {
		log.Printf("stallwatch: cannot save blocked-reminder counts: %v", err)
	}
}

// blockedCountTTL is how long a record is kept after its block clears.
func (w *Watcher) blockedCountTTL() time.Duration {
	if w.cfg.BlockedReminderCountTTL > 0 {
		return w.cfg.BlockedReminderCountTTL
	}
	return config.DefaultBlockedReminderCountTTL
}

// itemClosed reports whether id has reached done/. Any other answer — still
// open, claimed, parked, archived, missing, or a read error — is "not closed",
// and the record then lives out its retention window instead.
func (w *Watcher) itemClosed(id string) bool {
	it, ok, err := workitem.FindFrom(w.workRoot, id)
	return err == nil && ok && it.Status == "done"
}

// selectBlockedDue is selectDue for the blocked-reminder, over the persisted
// per-(item, blocked-on) records instead of lastNudge (drellem2/pogo#300).
//
// Same contract as selectDue — returns what may be notified now and a selection
// describing what was held back — with one difference: a record whose pair is no
// longer a candidate is RETIRED, not deleted. See blockedRecord.
func (w *Watcher) selectBlockedDue(candidates []workitem.WorkItem, now time.Time, base time.Duration) ([]workitem.WorkItem, selection) {
	w.mu.Lock()
	defer w.mu.Unlock()

	keys := make([]string, len(candidates))
	live := make(map[string]bool, len(candidates))
	for i, it := range candidates {
		who, _ := config.BlockedOn(it.Assignee)
		keys[i] = blockedKey(it.ID, normName(who))
		live[keys[i]] = true
	}

	changed := false
	ttl := w.blockedCountTTL()
	for k, r := range w.blocked {
		if live[k] {
			continue
		}
		if r.ClearedAt.IsZero() {
			r.ClearedAt = now
			w.blocked[k] = r
			changed = true
		}
		if now.Sub(r.ClearedAt) >= ttl || w.itemClosed(r.Item) {
			delete(w.blocked, k)
			changed = true
		}
	}

	sel := selection{repeats: make(map[string]int)}
	var due []workitem.WorkItem
	for i, it := range candidates {
		k := keys[i]
		r, seen := w.blocked[k]
		if seen && !r.ClearedAt.IsZero() {
			// Re-blocked on the same agent inside the window: resume.
			r.ClearedAt = time.Time{}
			w.blocked[k] = r
			changed = true
		}
		if seen && now.Sub(r.Last) < repeatCooldown(base, w.cfg.RepeatBackoffCap, r.Count) {
			sel.suppressed = append(sel.suppressed, it.ID)
			continue
		}
		if !seen {
			who, _ := config.BlockedOn(it.Assignee)
			r = blockedRecord{Item: it.ID, BlockedOn: normName(who)}
		}
		r.Last = now
		r.Count++
		w.blocked[k] = r
		changed = true
		due = append(due, it)
		if r.Count > 1 {
			sel.repeats[it.ID] = r.Count
		}
		if next := repeatCooldown(base, w.cfg.RepeatBackoffCap, r.Count); next > sel.nextBackoff {
			sel.nextBackoff = next
		}
	}
	if len(sel.repeats) == 0 {
		sel.repeats = nil
	}
	if changed {
		w.saveBlockedCountsLocked()
	}
	return due, sel
}
