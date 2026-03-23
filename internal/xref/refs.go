package xref

import (
	"strings"
)

// SearchResponse mirrors the client.SearchResponse shape so xref doesn't
// depend on the client package (avoiding import cycles).
type SearchResponse struct {
	Index   IndexedProject `json:"index"`
	Results SearchResults  `json:"results"`
	Error   string         `json:"error"`
}

type IndexedProject struct {
	Root   string   `json:"root"`
	Paths  []string `json:"paths"`
	Status string   `json:"indexing_status"`
}

type SearchResults struct {
	Files []FileMatch `json:"files"`
}

type FileMatch struct {
	Path    string       `json:"path"`
	Matches []ChunkMatch `json:"matches"`
}

type ChunkMatch struct {
	Line    uint32 `json:"line"`
	Content string `json:"content"`
}

// SearchAllFunc is the function signature for streaming search across all repos.
type SearchAllFunc func(query string, onResult func(*SearchResponse)) error

// classifyRef inspects a match line to determine the kind of reference.
func classifyRef(content string) RefKind {
	trimmed := strings.TrimSpace(content)

	// Definition: func/type/var/const declarations
	if strings.HasPrefix(trimmed, "func ") ||
		strings.HasPrefix(trimmed, "type ") ||
		strings.HasPrefix(trimmed, "var ") ||
		strings.HasPrefix(trimmed, "const ") {
		return RefDefinition
	}

	// Import: inside import blocks or single-line imports
	if strings.Contains(trimmed, `"`) &&
		(strings.HasPrefix(trimmed, `"`) || strings.HasPrefix(trimmed, `import`)) {
		return RefImport
	}

	return RefCall
}

// FindReferences searches for a symbol across all indexed repos and classifies
// each match. It streams results per-repo.
func FindReferences(searchAll SearchAllFunc, symbol string, onRepo func(*RepoRefs)) error {
	return searchAll(symbol, func(resp *SearchResponse) {
		rr := &RepoRefs{Repo: resp.Index.Root}
		if resp.Error != "" {
			rr.Error = resp.Error
			onRepo(rr)
			return
		}
		for _, f := range resp.Results.Files {
			for _, m := range f.Matches {
				rr.Refs = append(rr.Refs, Reference{
					Repo:    resp.Index.Root,
					File:    f.Path,
					Line:    m.Line,
					Content: m.Content,
					Kind:    classifyRef(m.Content),
				})
			}
		}
		if len(rr.Refs) > 0 || rr.Error != "" {
			onRepo(rr)
		}
	})
}

// FindReferencesAll is the non-streaming variant that collects all results.
func FindReferencesAll(searchAll SearchAllFunc, symbol string) (*RefsResult, error) {
	result := &RefsResult{Symbol: symbol}
	err := FindReferences(searchAll, symbol, func(rr *RepoRefs) {
		result.Refs = append(result.Refs, rr)
		result.Total += len(rr.Refs)
	})
	return result, err
}
