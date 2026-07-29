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
	// ModTime is the work item file's last-modified time. It is the best
	// available proxy for how long an item has sat in its current status
	// directory (mg rewrites/moves the file on status transitions), which the
	// stall watcher uses to age unclaimed `available` items. Populated by
	// ListFrom; zero when the file could not be stat'd.
	ModTime time.Time `json:"mod_time,omitempty"`
}

// workspaceDir returns the macguffin workspace root.
func workspaceDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".macguffin", "work")
}

// statusDirs maps directory names to work item status values.
var statusDirs = []struct {
	dir    string
	status string
}{
	{"available", "available"},
	{"claimed", "claimed"},
	{"done", "done"},
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
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			item, err := parseWorkItem(filepath.Join(dir, e.Name()), sd.status)
			if err != nil {
				continue // skip unparseable files
			}
			if info, err := e.Info(); err == nil {
				item.ModTime = info.ModTime()
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
// the caller's filter. An empty filter selects every status.
func statusRequested(status string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, s := range filter {
		if s == status {
			return true
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
		}
	}

	// Read first markdown heading as title
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			item.Title = strings.TrimPrefix(line, "# ")
			break
		}
	}

	return item, nil
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
