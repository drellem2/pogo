package refinery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// QAStatus represents the result of checking for a paired QA work item.
type QAStatus int

const (
	// QANotRequired means no QA item exists for this work item (opt-in).
	QANotRequired QAStatus = iota
	// QAPassed means a QA item exists and is done or archived.
	QAPassed
	// QAPending means a QA item exists but is not yet done.
	QAPending
)

// checkQAGate checks whether a paired QA work item exists for the given
// work item ID. QA is opt-in: if no QA item exists, the merge proceeds.
//
// It scans the macguffin work directories for any item with type: qa and
// source: <workID>. If found in done/ or archive/, the gate passes. If
// found elsewhere (available/, claimed/, pending/), the gate holds.
func checkQAGate(macguffinDir string, workID string) (QAStatus, string) {
	if macguffinDir == "" || workID == "" {
		return QANotRequired, ""
	}

	workDir := filepath.Join(macguffinDir, "work")

	// Check done/ and archive/ first (passing states)
	for _, dir := range []string{"done", "archive"} {
		found, qaID := findQAItem(filepath.Join(workDir, dir), workID)
		if found {
			return QAPassed, qaID
		}
	}

	// Check non-done states (pending states)
	for _, dir := range []string{"available", "claimed", "pending"} {
		found, qaID := findQAItem(filepath.Join(workDir, dir), workID)
		if found {
			return QAPending, qaID
		}
	}

	return QANotRequired, ""
}

// findQAItem scans a directory (and subdirectories) for a work item file
// with type: qa and source: <workID> in its YAML frontmatter.
func findQAItem(dir string, workID string) (bool, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, ""
	}

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			// Recurse into subdirectories (e.g. archive/2026-03/)
			if found, id := findQAItem(path, workID); found {
				return found, id
			}
			continue
		}
		if !strings.Contains(entry.Name(), ".md") {
			continue
		}
		if isQAItemFor(path, workID) {
			id := extractFrontmatterField(path, "id")
			return true, id
		}
	}
	return false, ""
}

// isQAItemFor checks if a markdown file has type: qa and source: <workID>
// in its YAML frontmatter.
func isQAItemFor(path string, workID string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	fm := parseFrontmatter(string(data))
	return fm["type"] == "qa" && fm["source"] == workID
}

// extractFrontmatterField reads a single field from YAML frontmatter.
func extractFrontmatterField(path string, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parseFrontmatter(string(data))[key]
}

// parseFrontmatter extracts key-value pairs from YAML frontmatter delimited
// by --- lines. Only handles simple scalar values (not arrays or maps).
func parseFrontmatter(content string) map[string]string {
	result := make(map[string]string)

	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return result
	}

	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		result[key] = val
	}

	return result
}

// defaultMacguffinDir returns the default macguffin directory path.
func defaultMacguffinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".macguffin")
}

// qaHoldError is returned when a merge is held due to a pending QA item.
// It is not retryable — the item should be re-queued for later.
type qaHoldError struct {
	qaID   string
	workID string
}

func (e *qaHoldError) Error() string {
	return fmt.Sprintf("QA gate hold: item %s has pending QA %s (not yet done)", e.workID, e.qaID)
}

// isQAHold checks if an error is a QA hold error.
func isQAHold(err error) bool {
	_, ok := err.(*qaHoldError)
	return ok
}
