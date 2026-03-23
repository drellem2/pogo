package xref

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDepGraph(t *testing.T) {
	// Create temp dirs with go.mod files
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repoA") + "/"
	repoB := filepath.Join(tmp, "repoB") + "/"
	os.MkdirAll(repoA, 0755)
	os.MkdirAll(repoB, 0755)

	os.WriteFile(filepath.Join(repoA, "go.mod"), []byte("module github.com/example/repoA\n"), 0644)
	os.WriteFile(filepath.Join(repoB, "go.mod"), []byte("module github.com/example/repoB\n"), 0644)

	getProjects := func() ([]ProjectInfo, error) {
		return []ProjectInfo{{Path: repoA}, {Path: repoB}}, nil
	}

	// repoB imports repoA
	searchAll := func(query string, onResult func(*SearchResponse)) error {
		if query == "github.com/example/repoA" {
			onResult(&SearchResponse{
				Index: IndexedProject{Root: repoB},
				Results: SearchResults{
					Files: []FileMatch{
						{
							Path: "main.go",
							Matches: []ChunkMatch{
								{Line: 5, Content: `"github.com/example/repoA"`},
							},
						},
					},
				},
			})
		}
		return nil
	}

	graph, err := BuildDepGraph(getProjects, searchAll)
	if err != nil {
		t.Fatal(err)
	}

	if len(graph.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(graph.Nodes))
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(graph.Edges))
	}

	edge := graph.Edges[0]
	if edge.From != repoB {
		t.Errorf("expected edge from %s, got %s", repoB, edge.From)
	}
	if edge.To != repoA {
		t.Errorf("expected edge to %s, got %s", repoA, edge.To)
	}
	if edge.ImportPath != "github.com/example/repoA" {
		t.Errorf("expected import path github.com/example/repoA, got %s", edge.ImportPath)
	}
}

func TestBuildDepGraphNoModules(t *testing.T) {
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repoA") + "/"
	os.MkdirAll(repoA, 0755)
	// No go.mod

	getProjects := func() ([]ProjectInfo, error) {
		return []ProjectInfo{{Path: repoA}}, nil
	}
	searchAll := func(query string, onResult func(*SearchResponse)) error {
		return nil
	}

	graph, err := BuildDepGraph(getProjects, searchAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(graph.Nodes))
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("expected 0 edges, got %d", len(graph.Edges))
	}
}

func TestBuildDepGraphSelfRefSkipped(t *testing.T) {
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repoA") + "/"
	os.MkdirAll(repoA, 0755)
	os.WriteFile(filepath.Join(repoA, "go.mod"), []byte("module github.com/example/repoA\n"), 0644)

	getProjects := func() ([]ProjectInfo, error) {
		return []ProjectInfo{{Path: repoA}}, nil
	}

	// Self-reference should be skipped
	searchAll := func(query string, onResult func(*SearchResponse)) error {
		onResult(&SearchResponse{
			Index: IndexedProject{Root: repoA},
			Results: SearchResults{
				Files: []FileMatch{
					{Path: "main.go", Matches: []ChunkMatch{{Line: 3, Content: `"github.com/example/repoA/pkg"`}}},
				},
			},
		})
		return nil
	}

	graph, err := BuildDepGraph(getProjects, searchAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("expected 0 edges (self-ref skipped), got %d", len(graph.Edges))
	}
}

func TestBuildDepGraphDeduplication(t *testing.T) {
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repoA") + "/"
	repoB := filepath.Join(tmp, "repoB") + "/"
	os.MkdirAll(repoA, 0755)
	os.MkdirAll(repoB, 0755)
	os.WriteFile(filepath.Join(repoA, "go.mod"), []byte("module github.com/example/repoA\n"), 0644)
	os.WriteFile(filepath.Join(repoB, "go.mod"), []byte("module github.com/example/repoB\n"), 0644)

	getProjects := func() ([]ProjectInfo, error) {
		return []ProjectInfo{{Path: repoA}, {Path: repoB}}, nil
	}

	// Multiple files in repoB import repoA — should produce only one edge
	searchAll := func(query string, onResult func(*SearchResponse)) error {
		if query == "github.com/example/repoA" {
			onResult(&SearchResponse{
				Index: IndexedProject{Root: repoB},
				Results: SearchResults{
					Files: []FileMatch{
						{Path: "a.go", Matches: []ChunkMatch{{Line: 3, Content: `"github.com/example/repoA"`}}},
						{Path: "b.go", Matches: []ChunkMatch{{Line: 5, Content: `"github.com/example/repoA/pkg"`}}},
					},
				},
			})
		}
		return nil
	}

	graph, err := BuildDepGraph(getProjects, searchAll)
	if err != nil {
		t.Fatal(err)
	}
	// The function returns after first match per repo, so only 1 edge
	if len(graph.Edges) != 1 {
		t.Fatalf("expected 1 deduplicated edge, got %d", len(graph.Edges))
	}
}
