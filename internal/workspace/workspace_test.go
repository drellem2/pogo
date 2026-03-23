package workspace

import (
	"testing"
)

func TestClassifySymbol(t *testing.T) {
	tests := []struct {
		content  string
		wantName string
		wantKind SymbolKind
		wantOK   bool
	}{
		// Go
		{"func main() {", "main", SymbolFunction, true},
		{"func (s *Server) Start() error {", "Start", SymbolMethod, true},
		{"type Manager struct {", "Manager", SymbolStruct, true},
		{"type Reader interface {", "Reader", SymbolInterface, true},
		{"type SymbolKind int", "SymbolKind", SymbolClass, true},
		{"const maxRetries = 5", "maxRetries", SymbolConstant, true},
		{"var searchService = createBasicSearch()", "searchService", SymbolVariable, true},

		// Python
		{"class MyClass:", "MyClass", SymbolClass, true},
		{"def hello_world():", "hello_world", SymbolFunction, true},
		{"  def method(self):", "method", SymbolFunction, true},

		// JavaScript/TypeScript
		{"export class Router {", "Router", SymbolClass, true},
		{"export default class App {", "App", SymbolClass, true},
		{"function handleRequest(req) {", "handleRequest", SymbolFunction, true},
		{"export async function fetchData() {", "fetchData", SymbolFunction, true},
		{"export const handler = (req) => {", "handler", SymbolFunction, true},
		{"export interface Config {", "Config", SymbolInterface, true},
		{"export enum Status {", "Status", SymbolEnum, true},

		// Rust
		{"pub fn new() -> Self {", "new", SymbolFunction, true},
		{"struct Point {", "Point", SymbolStruct, true},
		{"pub enum Color {", "Color", SymbolEnum, true},
		{"pub trait Display {", "Display", SymbolInterface, true},
		{"impl Server {", "Server", SymbolClass, true},

		// Non-symbols
		{"// This is a comment", "", 0, false},
		{"x := 42", "", 0, false},
		{"import \"fmt\"", "", 0, false},
	}

	for _, tt := range tests {
		name, kind, _, ok := classifySymbol(tt.content)
		if ok != tt.wantOK {
			t.Errorf("classifySymbol(%q): ok=%v, want %v", tt.content, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if name != tt.wantName {
			t.Errorf("classifySymbol(%q): name=%q, want %q", tt.content, name, tt.wantName)
		}
		if kind != tt.wantKind {
			t.Errorf("classifySymbol(%q): kind=%d, want %d", tt.content, kind, tt.wantKind)
		}
	}
}

func TestSymbolQueryDefaults(t *testing.T) {
	q := SymbolQuery{Query: "test"}
	if q.Limit != 0 {
		t.Errorf("expected zero-value Limit, got %d", q.Limit)
	}
	if q.RepoPath != "" {
		t.Errorf("expected empty RepoPath, got %q", q.RepoPath)
	}
}
