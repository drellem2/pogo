// Package xref provides cross-repo operations: find-references and dependency
// graph analysis across all pogo-indexed repositories.
package xref

// RefKind classifies how a symbol is referenced.
type RefKind string

const (
	RefDefinition RefKind = "definition"
	RefImport     RefKind = "import"
	RefCall       RefKind = "call"
)

// Reference is a single cross-repo reference to a symbol.
type Reference struct {
	Repo    string  `json:"repo"`
	File    string  `json:"file"`
	Line    uint32  `json:"line"`
	Content string  `json:"content"`
	Kind    RefKind `json:"kind"`
}

// RefsResult groups references by repository.
type RefsResult struct {
	Symbol string      `json:"symbol"`
	Refs   []*RepoRefs `json:"refs"`
	Total  int         `json:"total"`
}

// RepoRefs holds all references within a single repo.
type RepoRefs struct {
	Repo  string      `json:"repo"`
	Refs  []Reference `json:"refs"`
	Error string      `json:"error,omitempty"`
}

// DepEdge represents a dependency from one repo to another.
type DepEdge struct {
	From       string `json:"from"`        // repo path
	To         string `json:"to"`          // repo path
	ImportPath string `json:"import_path"` // Go import path
}

// DepGraph is the full dependency graph across indexed repos.
type DepGraph struct {
	Nodes []DepNode `json:"nodes"`
	Edges []DepEdge `json:"edges"`
}

// DepNode is a repo in the dependency graph.
type DepNode struct {
	Repo       string `json:"repo"`
	ModulePath string `json:"module_path,omitempty"`
}
