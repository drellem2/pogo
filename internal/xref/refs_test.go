package xref

import (
	"testing"
)

func TestClassifyRef(t *testing.T) {
	tests := []struct {
		content string
		want    RefKind
	}{
		{"func Foo() {", RefDefinition},
		{"func (r *Bar) Baz() error {", RefDefinition},
		{"type Foo struct {", RefDefinition},
		{"var Foo = 42", RefDefinition},
		{"const Foo = 42", RefDefinition},
		{`"github.com/drellem2/pogo"`, RefImport},
		{`import "github.com/drellem2/pogo"`, RefImport},
		{"result := Foo(bar)", RefCall},
		{"x.Foo()", RefCall},
		{"if Foo != nil {", RefCall},
	}

	for _, tt := range tests {
		got := classifyRef(tt.content)
		if got != tt.want {
			t.Errorf("classifyRef(%q) = %q, want %q", tt.content, got, tt.want)
		}
	}
}

func TestFindReferences(t *testing.T) {
	mockSearch := func(query string, onResult func(*SearchResponse)) error {
		onResult(&SearchResponse{
			Index: IndexedProject{Root: "/repo1/"},
			Results: SearchResults{
				Files: []FileMatch{
					{
						Path: "main.go",
						Matches: []ChunkMatch{
							{Line: 10, Content: "func Foo() {"},
							{Line: 20, Content: "result := Foo()"},
						},
					},
				},
			},
		})
		onResult(&SearchResponse{
			Index: IndexedProject{Root: "/repo2/"},
			Results: SearchResults{
				Files: []FileMatch{
					{
						Path: "lib.go",
						Matches: []ChunkMatch{
							{Line: 5, Content: `"github.com/example/Foo"`},
						},
					},
				},
			},
		})
		return nil
	}

	var repos []*RepoRefs
	err := FindReferences(mockSearch, "Foo", func(rr *RepoRefs) {
		repos = append(repos, rr)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	// repo1: definition + call
	if len(repos[0].Refs) != 2 {
		t.Fatalf("repo1: expected 2 refs, got %d", len(repos[0].Refs))
	}
	if repos[0].Refs[0].Kind != RefDefinition {
		t.Errorf("repo1 ref[0]: expected definition, got %s", repos[0].Refs[0].Kind)
	}
	if repos[0].Refs[1].Kind != RefCall {
		t.Errorf("repo1 ref[1]: expected call, got %s", repos[0].Refs[1].Kind)
	}

	// repo2: import
	if len(repos[1].Refs) != 1 {
		t.Fatalf("repo2: expected 1 ref, got %d", len(repos[1].Refs))
	}
	if repos[1].Refs[0].Kind != RefImport {
		t.Errorf("repo2 ref[0]: expected import, got %s", repos[1].Refs[0].Kind)
	}
}

func TestFindReferencesAll(t *testing.T) {
	mockSearch := func(query string, onResult func(*SearchResponse)) error {
		onResult(&SearchResponse{
			Index: IndexedProject{Root: "/repo1/"},
			Results: SearchResults{
				Files: []FileMatch{
					{Path: "a.go", Matches: []ChunkMatch{{Line: 1, Content: "func Bar() {"}}},
				},
			},
		})
		return nil
	}

	result, err := FindReferencesAll(mockSearch, "Bar")
	if err != nil {
		t.Fatal(err)
	}
	if result.Symbol != "Bar" {
		t.Errorf("expected symbol Bar, got %s", result.Symbol)
	}
	if result.Total != 1 {
		t.Errorf("expected total 1, got %d", result.Total)
	}
}

func TestFindReferencesError(t *testing.T) {
	mockSearch := func(query string, onResult func(*SearchResponse)) error {
		onResult(&SearchResponse{
			Index: IndexedProject{Root: "/repo1/"},
			Error: "index not ready",
		})
		return nil
	}

	var repos []*RepoRefs
	err := FindReferences(mockSearch, "Foo", func(rr *RepoRefs) {
		repos = append(repos, rr)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].Error != "index not ready" {
		t.Errorf("expected error 'index not ready', got %q", repos[0].Error)
	}
}
