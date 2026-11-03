package stallwatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
)

// blockSetterLog answers one question for the blocked-reminder: WHO wrote an
// item's current assignee value (drellem2/pogo#161 defect 1).
//
// `blocked:mayor` written by pm-pogo is a request mayor received, and the
// reminder exists to deliver it. `blocked:mayor` written by mayor is a statement
// mayor made — "I release this, when the condition I already know about holds" —
// and reminding mayor of it asks the coordinator to reconsider a decision it
// took a minute earlier. On this store 20 of 53 recorded `blocked:` assignments
// were self-set (t7476's measurement, not re-derived here), and the reporter's
// incident was six of them arriving as one burst.
//
// The work item does not record its setter — the frontmatter carries the value,
// not the hand that wrote it — so the answer comes from macguffin's event log,
// `events.jsonl`, which sits beside the work tree and carries an `actor` on every
// work.created and work.edited event. verdictwatch already reads the same file.
//
// # Fail open, as acceptance rather than advice
//
// Suppression is the dangerous direction. A reminder that wrongly fires is noise
// someone can complain about; one that wrongly suppresses is an item nobody is
// told about, which is the failure class the reminder exists to prevent. So
// setter() answers "" — and the caller sends the notice — whenever the answer is
// anything short of certain:
//
//   - the log is missing, unreadable, or was never read;
//   - no event in the log ever set this item's assignee (the log is rotated, or
//     the field was written by hand);
//   - the LAST assignee-writing event recorded a value other than the item's
//     current one (something wrote the field without leaving an event);
//   - that event's type carries an assignee but does not attribute it — a
//     work.unclaim restores an assignee, and the actor of an unclaim did not
//     choose the block it restored;
//   - that event's actor is empty.
//
// The actor is self-asserted (MG_ACTOR / POGO_AGENT_NAME, falling back to the
// OS user, and every agent here is one unix user), so a match is a strong
// heuristic and not a proof. The fallback can only produce an OS username, which
// does not name an agent, so it fails toward the notice too.
//
// # Cost
//
// The log grows monotonically, so it is read INCREMENTALLY: the first call reads
// it once, and every later call reads only the bytes appended since. The index
// it builds holds one entry per item that ever had an assignee written, which is
// bounded by the store, not by the log. A shrunk or replaced file (rotation) is
// detected by size and identity and re-read from the start. A trailing line
// without its newline is a write in progress and is left for the next call.
type blockSetterLog struct {
	path string

	mu     sync.Mutex
	info   os.FileInfo
	offset int64
	// last maps item id → the most recent event that wrote its assignee.
	last map[string]assigneeWrite
	// ok is false until a read succeeds, and goes false again on any failed
	// read, so a log that vanished answers "unknown" rather than a stale index.
	ok bool
}

// assigneeWrite is one event that wrote an item's assignee. actor is "" when the
// event type does not attribute the value (see blockSetterLog).
type assigneeWrite struct {
	value string
	actor string
}

func newBlockSetterLog(path string) *blockSetterLog {
	return &blockSetterLog{path: path, last: make(map[string]assigneeWrite)}
}

// refresh reads whatever has been appended since the last call. It returns
// false when the log cannot be read, in which case every setter() answer is "".
func (l *blockSetterLog) refresh() bool {
	if l == nil || l.path == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.Open(l.path)
	if err != nil {
		l.ok = false
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		l.ok = false
		return false
	}
	if l.info == nil || !os.SameFile(l.info, fi) || fi.Size() < l.offset {
		l.offset = 0
		l.last = make(map[string]assigneeWrite)
	}
	l.info = fi
	if _, err := f.Seek(l.offset, io.SeekStart); err != nil {
		l.ok = false
		return false
	}
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// io.EOF with a partial line is a write in progress: do not
			// consume it, so the next call reads it whole.
			if err != io.EOF {
				l.ok = false
				return false
			}
			break
		}
		l.offset += int64(len(line))
		l.apply(line)
	}
	l.ok = true
	return true
}

// apply folds one log line into the index. Lines that are not work events, or
// that do not write an assignee, are ignored; a malformed line is ignored too,
// which can only leave an older (or no) entry in place — and an older entry
// whose value no longer matches the item reads as unknown, so it fails open.
func (l *blockSetterLog) apply(line []byte) {
	if !bytes.Contains(line, []byte(`"work.`)) || !bytes.Contains(line, []byte(`"assignee`)) {
		return
	}
	var e struct {
		Type          string  `json:"type"`
		ItemID        string  `json:"item_id"`
		Actor         string  `json:"actor"`
		Fields        string  `json:"fields"`
		Assignee      *string `json:"assignee"`
		AssigneeAfter *string `json:"assignee_after"`
	}
	if json.Unmarshal(line, &e) != nil || e.ItemID == "" {
		return
	}
	switch {
	case e.Type == "work.created" && e.Assignee != nil:
		l.last[e.ItemID] = assigneeWrite{value: *e.Assignee, actor: e.Actor}
	case e.Type == "work.edited" && e.AssigneeAfter != nil && fieldListHas(e.Fields, "assignee"):
		l.last[e.ItemID] = assigneeWrite{value: *e.AssigneeAfter, actor: e.Actor}
	case e.Assignee != nil:
		// Some other work event carried an assignee (work.unclaim restores
		// one). It is the latest write, but not one whose actor chose the
		// value — record it unattributed so the answer is "unknown".
		l.last[e.ItemID] = assigneeWrite{value: *e.Assignee}
	case e.AssigneeAfter != nil:
		l.last[e.ItemID] = assigneeWrite{value: *e.AssigneeAfter}
	}
}

// setter returns the agent that wrote id's assignee as current, or "" when that
// is not known with certainty. Call refresh first.
func (l *blockSetterLog) setter(id, current string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ok {
		return ""
	}
	w, found := l.last[id]
	if !found || normName(w.value) != normName(current) {
		return ""
	}
	return normName(w.actor)
}

// fieldListHas reports whether a comma-separated `fields` value names field.
func fieldListHas(fields, field string) bool {
	for _, f := range strings.Split(fields, ",") {
		if strings.TrimSpace(f) == field {
			return true
		}
	}
	return false
}

func normName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
