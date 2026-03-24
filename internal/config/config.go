package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPort = 10000
	DefaultBind = "127.0.0.1"
)

// RunMode represents the operating mode of the pogo daemon.
type RunMode int

const (
	// ModeFull means everything is running: agents, refinery, indexing, HTTP.
	ModeFull RunMode = iota
	// ModeIndexOnly means only project indexing, search, and HTTP are running.
	// Agents and refinery are stopped.
	ModeIndexOnly
)

// String returns the human-readable name of the run mode.
func (m RunMode) String() string {
	switch m {
	case ModeFull:
		return "full"
	case ModeIndexOnly:
		return "index-only"
	default:
		return "unknown"
	}
}

// ServerMode represents whether pogo runs in local or cloud mode.
type ServerMode string

const (
	// ModeLocal is the default: filesystem-based project discovery.
	ModeLocal ServerMode = "local"
	// ModeCloud uses GitHub for project discovery instead of local filesystem.
	ModeCloud ServerMode = "cloud"
)

// Config holds pogo daemon configuration.
type Config struct {
	Port         int
	Bind         string
	Mode         ServerMode
	GitHubToken  string
	WorkspaceDir string
	Refinery     RefineryConfig
}

// RefineryConfig holds merge queue configuration.
type RefineryConfig struct {
	Enabled      bool
	PollInterval time.Duration
}

// Load reads configuration from (in priority order):
//  1. POGO_PORT environment variable
//  2. ~/.config/pogo/config.toml [server] port field
//  3. Default (10000)
func Load() *Config {
	cfg := &Config{
		Port: DefaultPort,
		Bind: DefaultBind,
		Mode: ModeLocal,
		Refinery: RefineryConfig{
			Enabled:      true,
			PollInterval: 30 * time.Second,
		},
	}

	// Try config file first (lowest priority, overridden by env)
	if fileCfg, err := loadConfigFile(); err == nil {
		if fileCfg.Port != 0 {
			cfg.Port = fileCfg.Port
		}
		if fileCfg.Bind != "" {
			cfg.Bind = fileCfg.Bind
		}
		if fileCfg.Mode != "" {
			cfg.Mode = fileCfg.Mode
		}
		if fileCfg.GitHubToken != "" {
			cfg.GitHubToken = fileCfg.GitHubToken
		}
		if fileCfg.WorkspaceDir != "" {
			cfg.WorkspaceDir = fileCfg.WorkspaceDir
		}
	}

	// Environment variables override config file
	if portStr := os.Getenv("POGO_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			cfg.Port = port
		}
	}
	if bind := os.Getenv("POGO_BIND"); bind != "" {
		cfg.Bind = bind
	}
	if mode := os.Getenv("POGO_MODE"); mode != "" {
		if mode == "cloud" {
			cfg.Mode = ModeCloud
		} else if mode == "local" {
			cfg.Mode = ModeLocal
		}
	}
	if token := os.Getenv("POGO_GITHUB_TOKEN"); token != "" {
		cfg.GitHubToken = token
	}
	if wsDir := os.Getenv("POGO_WORKSPACE_DIR"); wsDir != "" {
		cfg.WorkspaceDir = wsDir
	}

	// Set default workspace dir for cloud mode
	if cfg.Mode == ModeCloud && cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = "/workspace/repos"
	}

	return cfg
}

// ServerURL returns the base URL for connecting to the pogo daemon.
func (c *Config) ServerURL() string {
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

// ListenAddr returns the address string for the server to listen on.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.Port)
}

// IsCloud reports whether the daemon is configured for cloud mode.
func (c *Config) IsCloud() bool {
	return c.Mode == ModeCloud
}

// ConfigDir returns the pogo configuration directory path.
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pogo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pogo")
}

// ConfigFilePath returns the path to the pogo config file.
func ConfigFilePath() string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// loadConfigFile reads port from the TOML config file.
// Only parses the minimal subset needed: [server] section with port key.
func loadConfigFile() (*Config, error) {
	path := ConfigFilePath()
	if path == "" {
		return nil, fmt.Errorf("no config path")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{}
	currentSection := ""
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") {
			currentSection = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch currentSection {
		case "server":
			switch key {
			case "port":
				if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
					cfg.Port = port
				}
			case "bind":
				cfg.Bind = val
			case "mode":
				val = strings.Trim(val, "\"")
				if val == "cloud" {
					cfg.Mode = ModeCloud
				} else if val == "local" {
					cfg.Mode = ModeLocal
				}
			case "github_token":
				cfg.GitHubToken = strings.Trim(val, "\"")
			case "workspace_dir":
				cfg.WorkspaceDir = strings.Trim(val, "\"")
			}
		case "refinery":
			switch key {
			case "enabled":
				cfg.Refinery.Enabled = val == "true"
			case "poll_interval":
				val = strings.Trim(val, "\"")
				if d, err := time.ParseDuration(val); err == nil {
					cfg.Refinery.PollInterval = d
				}
			}
		}
	}

	return cfg, scanner.Err()
}
