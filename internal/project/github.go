package project

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// GitHubRepo represents a repository discovered from GitHub.
type GitHubRepo struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// GitHubDiscovery periodically fetches the authenticated user's repos from
// GitHub and registers them as pogo projects. Repos are cloned on demand to
// the configured workspace directory.
type GitHubDiscovery struct {
	cfg      *config.Config
	mu       sync.RWMutex
	repos    []GitHubRepo
	quit     chan struct{}
	done     chan struct{}
	interval time.Duration
}

// NewGitHubDiscovery creates a new discovery instance. Call Start() to begin
// periodic polling.
func NewGitHubDiscovery(cfg *config.Config) *GitHubDiscovery {
	return &GitHubDiscovery{
		cfg:      cfg,
		interval: 5 * time.Minute,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins periodic GitHub repo discovery. It performs an initial fetch
// synchronously then polls in the background.
func (d *GitHubDiscovery) Start() error {
	if err := d.refresh(); err != nil {
		return fmt.Errorf("github discovery: initial fetch failed: %w", err)
	}
	go d.loop()
	return nil
}

// Stop shuts down the background polling loop.
func (d *GitHubDiscovery) Stop() {
	close(d.quit)
	<-d.done
}

// Repos returns the most recently discovered repos.
func (d *GitHubDiscovery) Repos() []GitHubRepo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]GitHubRepo, len(d.repos))
	copy(out, d.repos)
	return out
}

// EnsureCloned clones a repo into the workspace directory if it isn't already
// present. Returns the local path to the repo.
func (d *GitHubDiscovery) EnsureCloned(repo GitHubRepo) (string, error) {
	localPath := filepath.Join(d.cfg.WorkspaceDir, repo.Name)

	// Already cloned?
	if _, err := os.Stat(filepath.Join(localPath, ".git")); err == nil {
		return localPath, nil
	}

	if err := os.MkdirAll(d.cfg.WorkspaceDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create workspace dir: %w", err)
	}

	cloneURL := repo.URL
	args := []string{"clone", "--depth=1", cloneURL, localPath}

	cmd := exec.Command("git", args...)
	// Configure credential helper if a token is available
	if d.cfg.GitHubToken != "" {
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("GIT_ASKPASS=echo"),
			fmt.Sprintf("GIT_TERMINAL_PROMPT=0"),
		)
		// Use token in the URL for HTTPS clones
		cloneURL = injectToken(repo.URL, d.cfg.GitHubToken)
		args = []string{"clone", "--depth=1", cloneURL, localPath}
		cmd = exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
		)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone failed: %w: %s", err, string(output))
	}

	log.Printf("github discovery: cloned %s to %s", repo.Name, localPath)

	// Register with pogo project system
	p := Project{Path: addSlashToPath(localPath)}
	Add(&p)

	return localPath, nil
}

func (d *GitHubDiscovery) loop() {
	defer close(d.done)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.quit:
			return
		case <-ticker.C:
			if err := d.refresh(); err != nil {
				log.Printf("github discovery: refresh failed: %v", err)
			}
		}
	}
}

func (d *GitHubDiscovery) refresh() error {
	repos, err := fetchGitHubRepos(d.cfg.GitHubToken)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.repos = repos
	d.mu.Unlock()

	// Register any already-cloned repos as projects
	for _, r := range repos {
		localPath := filepath.Join(d.cfg.WorkspaceDir, r.Name)
		if _, err := os.Stat(filepath.Join(localPath, ".git")); err == nil {
			normalizedPath := addSlashToPath(localPath)
			if GetProjectByPath(normalizedPath) == nil {
				p := Project{Path: normalizedPath}
				Add(&p)
			}
		}
	}

	log.Printf("github discovery: found %d repos", len(repos))
	return nil
}

// fetchGitHubRepos uses the gh CLI to list repos for the authenticated user.
func fetchGitHubRepos(token string) ([]GitHubRepo, error) {
	cmd := exec.Command("gh", "repo", "list", "--json", "name,url,description", "--limit", "100")
	if token != "" {
		cmd.Env = append(os.Environ(), fmt.Sprintf("GH_TOKEN=%s", token))
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh repo list failed: %w", err)
	}

	var repos []GitHubRepo
	if err := json.Unmarshal(output, &repos); err != nil {
		return nil, fmt.Errorf("failed to parse gh output: %w", err)
	}
	return repos, nil
}

// injectToken inserts a GitHub token into an HTTPS clone URL.
// e.g. https://github.com/user/repo -> https://x-access-token:TOKEN@github.com/user/repo
func injectToken(cloneURL, token string) string {
	const prefix = "https://github.com/"
	if len(cloneURL) > len(prefix) && cloneURL[:len(prefix)] == prefix {
		return "https://x-access-token:" + token + "@github.com/" + cloneURL[len(prefix):]
	}
	return cloneURL
}
