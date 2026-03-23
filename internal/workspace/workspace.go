// Package workspace provides an LSP-style workspace manager that serves
// workspace/symbol queries across multiple repositories indexed by pogo.
package workspace

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/search"
)

// SymbolKind mirrors LSP SymbolKind values.
type SymbolKind int

const (
	SymbolFile          SymbolKind = 1
	SymbolModule        SymbolKind = 2
	SymbolNamespace     SymbolKind = 3
	SymbolPackage       SymbolKind = 4
	SymbolClass         SymbolKind = 5
	SymbolMethod        SymbolKind = 6
	SymbolProperty      SymbolKind = 7
	SymbolField         SymbolKind = 8
	SymbolConstructor   SymbolKind = 9
	SymbolEnum          SymbolKind = 10
	SymbolInterface     SymbolKind = 11
	SymbolFunction      SymbolKind = 12
	SymbolVariable      SymbolKind = 13
	SymbolConstant      SymbolKind = 14
	SymbolString        SymbolKind = 15
	SymbolNumber        SymbolKind = 16
	SymbolBoolean       SymbolKind = 17
	SymbolArray         SymbolKind = 18
	SymbolObject        SymbolKind = 19
	SymbolKey           SymbolKind = 20
	SymbolNull          SymbolKind = 21
	SymbolEnumMember    SymbolKind = 22
	SymbolStruct        SymbolKind = 23
	SymbolEvent         SymbolKind = 24
	SymbolOperator      SymbolKind = 25
	SymbolTypeParameter SymbolKind = 26
)

// Location describes where a symbol was found.
type Location struct {
	Path string `json:"path"`
	Line uint32 `json:"line"`
}

// WorkspaceSymbol represents a symbol found across the workspace.
type WorkspaceSymbol struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	KindName      string     `json:"kindName"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName"`
	RepoPath      string     `json:"repoPath"`
}

// SymbolQuery holds parameters for a workspace/symbol request.
type SymbolQuery struct {
	// Query is the symbol name or pattern to search for.
	Query string `json:"query"`
	// RepoPath limits the search to a single repo (optional).
	RepoPath string `json:"repoPath,omitempty"`
	// Limit caps the total number of results (0 = default 100).
	Limit int `json:"limit,omitempty"`
}

// SymbolResponse is the response for a workspace/symbol query.
type SymbolResponse struct {
	Symbols   []WorkspaceSymbol `json:"symbols"`
	RepoCount int               `json:"repoCount"`
}

// Manager coordinates workspace queries across multiple repos.
type Manager struct {
	mu            sync.RWMutex
	searchTimeout time.Duration
}

// New creates a new workspace Manager.
func New() *Manager {
	return &Manager{
		searchTimeout: 10 * time.Second,
	}
}

// symbol definition patterns per language
var symbolPatterns = []*symbolPattern{
	// Go
	{regexp.MustCompile(`^\s*func\s+\(.*?\)\s+(\w+)`), SymbolMethod, "method"},
	{regexp.MustCompile(`^\s*func\s+(\w+)`), SymbolFunction, "function"},
	{regexp.MustCompile(`^\s*type\s+(\w+)\s+struct\b`), SymbolStruct, "struct"},
	{regexp.MustCompile(`^\s*type\s+(\w+)\s+interface\b`), SymbolInterface, "interface"},
	{regexp.MustCompile(`^\s*type\s+(\w+)`), SymbolClass, "type"},
	{regexp.MustCompile(`^\s*const\s+(\w+)`), SymbolConstant, "constant"},
	{regexp.MustCompile(`^\s*var\s+(\w+)`), SymbolVariable, "variable"},
	// Python
	{regexp.MustCompile(`^\s*class\s+(\w+)`), SymbolClass, "class"},
	{regexp.MustCompile(`^\s*def\s+(\w+)`), SymbolFunction, "function"},
	// JavaScript/TypeScript
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?class\s+(\w+)`), SymbolClass, "class"},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`), SymbolFunction, "function"},
	{regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\(|function)`), SymbolFunction, "function"},
	{regexp.MustCompile(`^\s*(?:export\s+)?interface\s+(\w+)`), SymbolInterface, "interface"},
	{regexp.MustCompile(`^\s*(?:export\s+)?enum\s+(\w+)`), SymbolEnum, "enum"},
	// Rust
	{regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+(\w+)`), SymbolFunction, "function"},
	{regexp.MustCompile(`^\s*(?:pub\s+)?struct\s+(\w+)`), SymbolStruct, "struct"},
	{regexp.MustCompile(`^\s*(?:pub\s+)?enum\s+(\w+)`), SymbolEnum, "enum"},
	{regexp.MustCompile(`^\s*(?:pub\s+)?trait\s+(\w+)`), SymbolInterface, "trait"},
	{regexp.MustCompile(`^\s*impl\s+(\w+)`), SymbolClass, "impl"},
	// Java/Kotlin
	{regexp.MustCompile(`^\s*(?:public|private|protected)?\s*(?:static\s+)?class\s+(\w+)`), SymbolClass, "class"},
	{regexp.MustCompile(`^\s*(?:public|private|protected)?\s*(?:static\s+)?interface\s+(\w+)`), SymbolInterface, "interface"},
}

type symbolPattern struct {
	re       *regexp.Regexp
	kind     SymbolKind
	kindName string
}

// classifySymbol attempts to extract a symbol name and kind from a line of code.
func classifySymbol(content string) (name string, kind SymbolKind, kindName string, ok bool) {
	for _, sp := range symbolPatterns {
		if m := sp.re.FindStringSubmatch(content); m != nil {
			return m[1], sp.kind, sp.kindName, true
		}
	}
	return "", 0, "", false
}

// QuerySymbols searches for symbols matching the query across workspace repos.
func (m *Manager) QuerySymbols(ctx context.Context, q SymbolQuery) (*SymbolResponse, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	projs := project.Projects()
	if len(projs) == 0 {
		return &SymbolResponse{Symbols: []WorkspaceSymbol{}}, nil
	}

	// Filter to specific repo if requested
	if q.RepoPath != "" {
		rp := q.RepoPath
		if rp[len(rp)-1] != '/' {
			rp += "/"
		}
		var filtered []project.Project
		for _, p := range projs {
			if p.Path == rp || strings.HasSuffix(p.Path, "/"+rp) || strings.Contains(p.Path, q.RepoPath) {
				filtered = append(filtered, p)
			}
		}
		projs = filtered
	}

	type repoResult struct {
		symbols []WorkspaceSymbol
		err     error
	}

	results := make(chan repoResult, len(projs))
	var wg sync.WaitGroup

	for _, p := range projs {
		wg.Add(1)
		go func(proj project.Project) {
			defer wg.Done()
			symbols, err := m.searchRepo(ctx, proj.Path, q.Query)
			results <- repoResult{symbols: symbols, err: err}
		}(p)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allSymbols []WorkspaceSymbol
	repoCount := 0
	for rr := range results {
		if rr.err != nil {
			continue
		}
		if len(rr.symbols) > 0 {
			repoCount++
			allSymbols = append(allSymbols, rr.symbols...)
		}
	}

	// Truncate to limit
	if len(allSymbols) > limit {
		allSymbols = allSymbols[:limit]
	}

	return &SymbolResponse{
		Symbols:   allSymbols,
		RepoCount: repoCount,
	}, nil
}

// searchRepo searches a single repo for symbol definitions matching the query.
func (m *Manager) searchRepo(ctx context.Context, repoPath string, query string) ([]WorkspaceSymbol, error) {
	// Use zoekt search via the search service to find matches
	results, err := search.SearchService.Search(repoPath, query, m.searchTimeout.String())
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", repoPath, err)
	}

	var symbols []WorkspaceSymbol
	for _, file := range results.Files {
		for _, match := range file.Matches {
			name, kind, kindName, ok := classifySymbol(match.Content)
			if !ok {
				// Not a recognized symbol definition — still include as
				// a generic match if it contains the query term
				if !strings.Contains(strings.ToLower(match.Content), strings.ToLower(query)) {
					continue
				}
				name = query
				kind = SymbolVariable
				kindName = "match"
			}

			symbols = append(symbols, WorkspaceSymbol{
				Name:     name,
				Kind:     kind,
				KindName: kindName,
				Location: Location{
					Path: file.Path,
					Line: match.Line,
				},
				RepoPath: repoPath,
			})
		}
	}
	return symbols, nil
}
