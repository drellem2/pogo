package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/workitem"
)

// # A worker whose item is finished is not using the repo (drellem2/pogo#128)
//
// The per-repo cap counts live worker PROCESSES, and the resource it protects is
// one repository's test suite run concurrently. Those diverge for a worker whose
// work item is already terminal: it has nothing left to build, and it stays live
// only until the done-reaper stops it — two minutes when its PTY goes quiet, and
// until the done timer when it does not. Counting it refuses a dispatch that
// would have run against an idle suite, which is how a finished worker held a
// slot in the gh#128 evening.
//
// ItemStatusReader is how the cap — and `pogo agent list` — learn an item's
// status. It is a seam for the reason FlowReader is: tests drive it without a
// macguffin store, and production reads the store directly rather than through
// `mg show`, because the cap is evaluated on every dispatch and every `pogo host
// load`, and a subprocess per live worker on each is a price the file read does
// not charge.

// ItemStatusReader reads a work item's status ("available", "claimed", "done",
// "archived", ...). An ERROR means "could not tell", and every caller fails OPEN
// on it: the cap counts the worker exactly as it did before #128, because a
// worker we could not classify might be building. An item that does not exist
// is an error too — there is no status to report.
type ItemStatusReader interface {
	ReadItemStatus(workItemID string) (status string, err error)
}

// ItemStatusReaderFunc adapts a function to ItemStatusReader.
type ItemStatusReaderFunc func(string) (string, error)

// ReadItemStatus implements ItemStatusReader.
func (f ItemStatusReaderFunc) ReadItemStatus(id string) (string, error) { return f(id) }

// IsTerminalItemStatus reports whether status is one a work item does not leave
// by being worked on: done or archived. The same pair client.MGWorkItemDone
// reads as terminal, which is what the done-reaper keys on — so the cap stops
// counting a worker at exactly the state the reaper is waiting to stop it in.
func IsTerminalItemStatus(status string) bool {
	return status == "done" || status == "archived"
}

// MGItemStatusReader is the production ItemStatusReader: the status as the
// macguffin store's directory layout records it.
type MGItemStatusReader struct {
	// Root overrides the macguffin store location; empty resolves through
	// macguffinStoreRoot, which under a test binary is a throwaway store.
	Root string
}

// ReadItemStatus implements ItemStatusReader.
//
// The live status directories are searched first, exactly as workitem.FindFrom
// searches them, and only then the archive. That order matches `mg show`: a
// live id that ALSO names an archived item (the collision mgShowJSON documents)
// reads as its live status, never as archived.
func (m MGItemStatusReader) ReadItemStatus(id string) (string, error) {
	root := macguffinStoreRoot(m.Root)
	id = strings.TrimSpace(id)
	if root == "" {
		return "", fmt.Errorf("no macguffin store root")
	}
	if id == "" {
		return "", fmt.Errorf("empty work item id")
	}
	work := filepath.Join(root, "work")
	item, found, err := workitem.FindFrom(work, id)
	if err != nil {
		return "", err
	}
	if found {
		return item.Status, nil
	}
	// workitem.FindFrom refuses ids with separators; mirror that before
	// building a glob from one.
	if strings.ContainsAny(id, `/\*?[`) {
		return "", fmt.Errorf("work item %q not found", id)
	}
	if matches, err := filepath.Glob(filepath.Join(work, "archive", "*", id+".md")); err == nil && len(matches) > 0 {
		if _, err := os.Stat(matches[0]); err == nil {
			return "archived", nil
		}
	}
	return "", fmt.Errorf("work item %q not found", id)
}

// SetItemStatusReader installs the status reader the per-repo cap and /agents
// use. Passing nil restores MGItemStatusReader{}.
func (r *Registry) SetItemStatusReader(s ItemStatusReader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.itemStatusReader = s
}

func (r *Registry) getItemStatusReader() ItemStatusReader {
	r.mu.RLock()
	s := r.itemStatusReader
	r.mu.RUnlock()
	if s == nil {
		return MGItemStatusReader{}
	}
	return s
}
