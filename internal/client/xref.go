package client

import (
	"github.com/drellem2/pogo/internal/xref"
)

// adaptSearchAll wraps SearchAllStreaming into the xref.SearchAllFunc signature.
func adaptSearchAll(query string, onResult func(*xref.SearchResponse)) error {
	return SearchAllStreaming(query, func(resp *SearchResponse) {
		// Convert client types to xref types
		xresp := &xref.SearchResponse{
			Index: xref.IndexedProject{
				Root:   resp.Index.Root,
				Paths:  resp.Index.Paths,
				Status: resp.Index.Status,
			},
			Error: resp.Error,
		}
		for _, f := range resp.Results.Files {
			xf := xref.FileMatch{Path: f.Path}
			for _, m := range f.Matches {
				xf.Matches = append(xf.Matches, xref.ChunkMatch{
					Line:    m.Line,
					Content: m.Content,
				})
			}
			xresp.Results.Files = append(xresp.Results.Files, xf)
		}
		onResult(xresp)
	})
}

// adaptGetProjects wraps GetProjects into the xref.GetProjectsFunc signature.
func adaptGetProjects() ([]xref.ProjectInfo, error) {
	projs, err := GetProjects()
	if err != nil {
		return nil, err
	}
	result := make([]xref.ProjectInfo, len(projs))
	for i, p := range projs {
		result[i] = xref.ProjectInfo{Path: p.Path}
	}
	return result, nil
}

// FindReferences searches for a symbol across all indexed repos and returns
// classified references (definition, import, call). Results stream per-repo.
func FindReferences(symbol string, onRepo func(*xref.RepoRefs)) error {
	return xref.FindReferences(adaptSearchAll, symbol, onRepo)
}

// FindReferencesAll collects all cross-repo references for a symbol.
func FindReferencesAll(symbol string) (*xref.RefsResult, error) {
	return xref.FindReferencesAll(adaptSearchAll, symbol)
}

// BuildDepGraph constructs a dependency graph across all indexed repos
// by analyzing Go module paths and import statements.
func BuildDepGraph() (*xref.DepGraph, error) {
	return xref.BuildDepGraph(adaptGetProjects, adaptSearchAll)
}
