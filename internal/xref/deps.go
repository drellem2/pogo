package xref

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ProjectInfo holds the minimal project data needed for dependency analysis.
type ProjectInfo struct {
	Path string
}

// GetProjectsFunc returns all known projects.
type GetProjectsFunc func() ([]ProjectInfo, error)

// readModulePath reads the module path from a go.mod file.
func readModulePath(repoRoot string) string {
	gomod := filepath.Join(repoRoot, "go.mod")
	f, err := os.Open(gomod)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

// BuildDepGraph constructs a dependency graph across all indexed repos.
// It reads go.mod to find module paths, then searches for import statements
// that reference other local repos.
func BuildDepGraph(getProjects GetProjectsFunc, searchAll SearchAllFunc) (*DepGraph, error) {
	projs, err := getProjects()
	if err != nil {
		return nil, err
	}

	// Step 1: Build module path → repo mapping
	type repoInfo struct {
		path       string
		modulePath string
	}
	repos := make([]repoInfo, 0, len(projs))
	moduleToRepo := make(map[string]string) // module path → repo path

	for _, p := range projs {
		modPath := readModulePath(p.Path)
		repos = append(repos, repoInfo{path: p.Path, modulePath: modPath})
		if modPath != "" {
			moduleToRepo[modPath] = p.Path
		}
	}

	graph := &DepGraph{}

	// Step 2: Add all repos as nodes
	for _, r := range repos {
		graph.Nodes = append(graph.Nodes, DepNode{
			Repo:       r.path,
			ModulePath: r.modulePath,
		})
	}

	if len(moduleToRepo) == 0 {
		return graph, nil
	}

	// Step 3: For each module path, search across all repos for imports
	for modPath, modRepo := range moduleToRepo {
		searchAll(modPath, func(resp *SearchResponse) {
			if resp.Error != "" || resp.Index.Root == modRepo {
				return // skip self-references and errors
			}
			for _, f := range resp.Results.Files {
				for _, m := range f.Matches {
					trimmed := strings.TrimSpace(m.Content)
					// Match Go import lines containing the module path in quotes
					if strings.Contains(trimmed, `"`+modPath) ||
						strings.Contains(trimmed, `"`+modPath+`"`) ||
						strings.Contains(trimmed, `"`+modPath+"/") {
						graph.Edges = append(graph.Edges, DepEdge{
							From:       resp.Index.Root,
							To:         modRepo,
							ImportPath: modPath,
						})
						return // one edge per repo pair per module
					}
				}
			}
		})
	}

	// Deduplicate edges
	seen := make(map[string]bool)
	deduped := graph.Edges[:0]
	for _, e := range graph.Edges {
		key := e.From + "|" + e.To + "|" + e.ImportPath
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, e)
		}
	}
	graph.Edges = deduped

	return graph, nil
}
