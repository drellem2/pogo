package project

import (
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

func TestInjectToken(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		token    string
		expected string
	}{
		{
			name:     "github https url",
			url:      "https://github.com/user/repo",
			token:    "ghp_abc123",
			expected: "https://x-access-token:ghp_abc123@github.com/user/repo",
		},
		{
			name:     "non-github url unchanged",
			url:      "https://gitlab.com/user/repo",
			token:    "ghp_abc123",
			expected: "https://gitlab.com/user/repo",
		},
		{
			name:     "empty token",
			url:      "https://github.com/user/repo",
			token:    "",
			expected: "https://x-access-token:@github.com/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectToken(tt.url, tt.token)
			if got != tt.expected {
				t.Errorf("injectToken(%q, %q) = %q, want %q", tt.url, tt.token, got, tt.expected)
			}
		})
	}
}

func TestNewGitHubDiscovery(t *testing.T) {
	// Verify construction doesn't panic and fields are set
	cfg := &config.Config{
		Mode:         config.ModeCloud,
		GitHubToken:  "test-token",
		WorkspaceDir: t.TempDir(),
	}
	d := NewGitHubDiscovery(cfg)
	if d == nil {
		t.Fatal("NewGitHubDiscovery returned nil")
	}
	if d.cfg != cfg {
		t.Error("config not stored correctly")
	}
	if d.interval == 0 {
		t.Error("interval should be non-zero")
	}

	// Repos() should return empty slice initially
	repos := d.Repos()
	if len(repos) != 0 {
		t.Errorf("expected 0 repos initially, got %d", len(repos))
	}
}
