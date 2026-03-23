package refinery

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// QAGateResult represents the outcome of a QA gate check.
type QAGateResult int

const (
	// QAGateProceed means no QA item exists (opt-in) or QA is done.
	QAGateProceed QAGateResult = iota
	// QAGateHold means a QA item exists but is not yet done.
	QAGateHold
)

// checkQAGate checks the macguffin workspace for a QA work item linked to the
// given work ID. The QA gate is opt-in: if no QA item exists, merging proceeds
// normally. If a QA item exists and is done/archived, merging proceeds. If a QA
// item exists but is not done, the merge is held.
func (r *Refinery) checkQAGate(workID string) (QAGateResult, string) {
	if r.cfg.MacguffinDir == "" {
		return QAGateProceed, ""
	}

	// Search directories in order of "done-ness".
	// done/ and archive/ mean QA is complete.
	// available/, claimed/, pending/ mean QA is still in progress.
	doneStateDirs := []string{"done", "archive"}
	pendingStateDirs := []string{"available", "claimed", "pending"}

	// Check done dirs first — if QA is done, proceed immediately.
	for _, dir := range doneStateDirs {
		found, itemID := findQAItem(r.cfg.MacguffinDir, dir, workID)
		if found {
			log.Printf("refinery: QA gate passed for %s (qa item %s is done)", workID, itemID)
			return QAGateProceed, itemID
		}
	}

	// Check pending dirs — if QA exists but not done, hold.
	for _, dir := range pendingStateDirs {
		found, itemID := findQAItem(r.cfg.MacguffinDir, dir, workID)
		if found {
			log.Printf("refinery: QA gate holding %s (qa item %s not yet done)", workID, itemID)
			return QAGateHold, itemID
		}
	}

	// No QA item found — opt-in, proceed normally.
	return QAGateProceed, ""
}

// findQAItem scans a macguffin work directory for a work item with type: qa
// and source matching the given workID. The dir parameter is relative to the
// macguffin base (e.g. "done", "available"). For "archive", it searches all
// subdirectories. Returns whether found and the item ID if found.
func findQAItem(baseDir, dir, workID string) (bool, string) {
	searchDir := filepath.Join(baseDir, dir)

	// Walk the directory tree (handles archive/YYYY-MM/ subdirs).
	entries, err := walkWorkItems(searchDir)
	if err != nil {
		return false, ""
	}

	for _, path := range entries {
		id, isQA, source := parseWorkItemFrontmatter(path)
		if isQA && source == workID {
			return true, id
		}
	}
	return false, ""
}

// walkWorkItems returns all .md files under the given directory, recursively.
func walkWorkItems(dir string) ([]string, error) {
	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors (dir may not exist)
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// parseWorkItemFrontmatter reads a macguffin work item file and extracts the
// id, type, and source fields from YAML frontmatter. Returns the id, whether
// the type is "qa", and the source value.
func parseWorkItemFrontmatter(path string) (id string, isQA bool, source string) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			// End of frontmatter
			break
		}

		if !inFrontmatter {
			continue
		}

		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "id":
			id = val
		case "type":
			isQA = val == "qa"
		case "source":
			source = val
		}
	}

	return id, isQA, source
}

// StatusHeld is returned when a merge is held waiting for QA.
const StatusHeld MergeStatus = "held"

// holdMergeRequest moves the MR back to the queue with a held status and
// records why it's being held.
func (r *Refinery) holdMergeRequest(mr *MergeRequest, qaItemID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mr.Status = StatusHeld
	mr.Error = fmt.Sprintf("held: QA item %s not yet done", qaItemID)
	// Put it back at the end of the queue so other MRs can proceed.
	r.queue = append(r.queue, mr)
	log.Printf("refinery: MR %s held (QA item %s pending), re-queued", mr.ID, qaItemID)
}
