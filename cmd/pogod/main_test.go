package main

import (
	"path/filepath"
	"testing"
)

// clean must not append a trailing separator: /file's path argument may name
// a file, and lstat("/repo/file.go/") fails with ENOTDIR (mg-88cc).
func TestCleanDoesNotAppendSeparator(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"file path unchanged", sep + filepath.Join("repo", "main.go"), sep + filepath.Join("repo", "main.go")},
		{"trailing separator stripped from file path", sep + filepath.Join("repo", "main.go") + sep, sep + filepath.Join("repo", "main.go")},
		{"trailing separator stripped from dir path", sep + "repo" + sep, sep + "repo"},
		{"redundant separators collapsed", sep + "repo" + sep + sep + "sub", sep + filepath.Join("repo", "sub")},
		{"root unchanged", sep, sep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clean(tc.in); got != tc.want {
				t.Errorf("clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
