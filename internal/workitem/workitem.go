// Package workitem reads macguffin work items from the filesystem.
package workitem

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
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

// List reads all work items from the macguffin workspace.
// It scans available/, claimed/, and done/ directories.
func List() ([]WorkItem, error) {
	return listFrom(workspaceDir())
}

// listFrom reads work items from a given workspace root (for testing).
func listFrom(root string) ([]WorkItem, error) {
	var items []WorkItem
	for _, sd := range statusDirs {
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
			items = append(items, item)
		}
	}
	return items, nil
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
